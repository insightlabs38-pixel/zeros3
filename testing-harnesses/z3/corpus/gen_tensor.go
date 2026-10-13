package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func sortStrings(s []string) { sort.Strings(s) }

func hashHex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Element layouts for weight-like data: narrow exponent range, random
// mantissa, so bytes are neither constant nor uniformly random.
type dtype struct {
	name       string
	size       int
	mantBits   uint
	expBits    uint
	expBias    int
	elemPrefix string
}

var (
	dtBF16 = dtype{"BF16", 2, 7, 8, 127, "bf16"}
	dtFP16 = dtype{"F16", 2, 10, 5, 15, "f16"}
	dtFP32 = dtype{"F32", 4, 23, 8, 127, "f32"}
)

func randWeights(dt dtype, n int, r *RNG) []byte {
	out := make([]byte, n-n%dt.size)
	for i := 0; i+dt.size <= len(out); i += dt.size {
		v := r.U64()
		sign := v & 1
		exp := uint64(dt.expBias - 2 - int(v>>1&3))
		mant := (v >> 8) & (1<<dt.mantBits - 1)
		bits := sign<<(dt.mantBits+dt.expBits) | exp<<dt.mantBits | mant
		switch dt.size {
		case 2:
			binary.LittleEndian.PutUint16(out[i:], uint16(bits))
		case 4:
			binary.LittleEndian.PutUint32(out[i:], uint32(bits))
		}
	}
	return out
}

// perturb flips the lowest mantissa bits of a fraction of elements in place,
// modelling a small training step.
func perturb(b []byte, elemSize int, frac float64, r *RNG) {
	n := len(b) / elemSize
	k := int(float64(n) * frac)
	if k < 1 {
		k = 1
	}
	for ; k > 0; k-- {
		b[r.Intn(n)*elemSize] ^= byte(1 + r.Intn(3))
	}
}

// ---- Safetensors ---------------------------------------------------------

type tensor struct {
	Name  string
	DT    dtype
	Shape []int
	Data  []byte
}

func (t *tensor) setShape(cols int) {
	elems := len(t.Data) / t.DT.size
	t.Shape = []int{elems / cols, cols}
}

func newTensor(name string, dt dtype, bytes, cols int, r *RNG) *tensor {
	cols = max(1, cols)
	rows := max(1, bytes/dt.size/cols)
	t := &tensor{Name: name, DT: dt, Data: randWeights(dt, rows*cols*dt.size, r)}
	t.Shape = []int{rows, cols}
	return t
}

// modelTensors builds a small transformer-shaped tensor list totalling ~size bytes.
func modelTensors(size int, r *RNG) []*tensor {
	unit := size / 12
	ts := []*tensor{newTensor("model.embed_tokens.weight", dtBF16, 2*unit, 256, r)}
	for l := 0; l < 4; l++ {
		ts = append(ts,
			newTensor(fmt.Sprintf("model.layers.%d.self_attn.q_proj.weight", l), dtBF16, unit, 256, r),
			newTensor(fmt.Sprintf("model.layers.%d.mlp.up_proj.weight", l), dtFP16, unit, 256, r))
	}
	ts = append(ts,
		newTensor("model.norm.weight", dtFP32, 4096, 1024, r),
		newTensor("model.rotary.inv_freq", dtFP32, 1024, 256, r),
		newTensor("lm_head.weight", dtBF16, size-ts0(ts)-5120, 256, r))
	return ts
}

func ts0(ts []*tensor) int {
	n := 0
	for _, t := range ts {
		n += len(t.Data)
	}
	return n
}

func jsonStr(s string) string { return strconv.Quote(s) }

// writeSafetensors lays tensor data out in dataOrder and writes header keys
// in metaOrder (indices into ts).
func writeSafetensors(ts []*tensor, dataOrder, metaOrder []int, metadata map[string]string) []byte {
	off := make([]int, len(ts))
	pos := 0
	var data bytes.Buffer
	for _, i := range dataOrder {
		off[i] = pos
		data.Write(ts[i].Data)
		pos += len(ts[i].Data)
	}
	var h strings.Builder
	h.WriteString("{")
	sep := ""
	if len(metadata) > 0 {
		keys := make([]string, 0, len(metadata))
		for k := range metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		h.WriteString(`"__metadata__":{`)
		for j, k := range keys {
			if j > 0 {
				h.WriteString(",")
			}
			h.WriteString(jsonStr(k) + ":" + jsonStr(metadata[k]))
		}
		h.WriteString("}")
		sep = ","
	}
	for _, i := range metaOrder {
		t := ts[i]
		dims := make([]string, len(t.Shape))
		for k, d := range t.Shape {
			dims[k] = strconv.Itoa(d)
		}
		fmt.Fprintf(&h, `%s%s:{"dtype":%q,"shape":[%s],"data_offsets":[%d,%d]}`,
			sep, jsonStr(t.Name), t.DT.name, strings.Join(dims, ","), off[i], off[i]+len(t.Data))
		sep = ","
	}
	h.WriteString("}")
	for h.Len()%8 != 0 {
		h.WriteString(" ")
	}
	out := make([]byte, 8, 8+h.Len()+data.Len())
	binary.LittleEndian.PutUint64(out, uint64(h.Len()))
	out = append(out, h.String()...)
	return append(out, data.Bytes()...)
}

func identity(n int) []int {
	p := make([]int, n)
	for i := range p {
		p[i] = i
	}
	return p
}

func tensorKeys(ts []*tensor) map[string]string {
	m := map[string]string{}
	for _, t := range ts {
		m[t.Name] = contentKey(t.Data)
	}
	return m
}

func init() {
	st := func(name string, fn func(c *Ctx, r *RNG, i int, ts *[]*tensor, dord, mord *[]int) string) {
		register(Family{Name: name, Format: "safetensors", Workload: "model-weights", Ext: ".safetensors",
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				ts := modelTensors(c.size, r)
				dord, mord := identity(len(ts)), identity(len(ts))
				prev := tensorKeys(ts)
				meta := map[string]string{"format": "pt"}
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					desc := "base"
					var truth map[string][]string
					if i > 0 {
						desc = fn(c, r, i, &ts, &dord, &mord)
						if len(dord) != len(ts) || len(mord) != len(ts) {
							dord, mord = identity(len(ts)), identity(len(ts))
						}
						cur := tensorKeys(ts)
						truth = truthSets(prev, cur)
						prev = cur
					}
					return writeSafetensors(ts, dord, mord, meta), desc, truth
				})
			}})
	}
	st("st-modify-sparse", func(c *Ctx, r *RNG, i int, ts *[]*tensor, d, m *[]int) string {
		t := (*ts)[r.Intn(len(*ts))]
		perturb(t.Data, t.DT.size, 0.001, r)
		return "perturb 0.1% of elements in " + t.Name
	})
	st("st-checkpoint-mostly", func(c *Ctx, r *RNG, i int, ts *[]*tensor, d, m *[]int) string {
		names := []string{}
		for k := 0; k < 2; k++ {
			t := (*ts)[r.Intn(len(*ts))]
			perturb(t.Data, t.DT.size, 0.05, r)
			names = append(names, t.Name)
		}
		return "perturb 5% of elements in 2 tensors: " + strings.Join(names, ",")
	})
	st("st-finetune-dense", func(c *Ctx, r *RNG, i int, ts *[]*tensor, d, m *[]int) string {
		for _, t := range *ts {
			perturb(t.Data, t.DT.size, 0.05, r)
		}
		return "perturb 5% of elements in every tensor"
	})
	st("st-reorder-meta", func(c *Ctx, r *RNG, i int, ts *[]*tensor, d, m *[]int) string {
		*m = r.Perm(len(*ts))
		return "permute header key order; data layout unchanged"
	})
	st("st-reorder-data", func(c *Ctx, r *RNG, i int, ts *[]*tensor, d, m *[]int) string {
		*d = r.Perm(len(*ts))
		return "permute tensor data layout; offsets rewritten"
	})
	st("st-add-remove", func(c *Ctx, r *RNG, i int, ts *[]*tensor, d, m *[]int) string {
		if i%2 == 1 {
			*ts = append(*ts, newTensor(fmt.Sprintf("model.extra.%d.weight", i), dtBF16, c.size/12, 256, r))
			return "add tensor"
		}
		k := r.Intn(len(*ts) - 2)
		name := (*ts)[k].Name
		*ts = append((*ts)[:k:k], (*ts)[k+1:]...)
		return "remove tensor " + name
	})
	st("st-rename", func(c *Ctx, r *RNG, i int, ts *[]*tensor, d, m *[]int) string {
		t := (*ts)[r.Intn(len(*ts))]
		old := t.Name
		t.Name += "_v" + strconv.Itoa(i)
		return "rename " + old + " -> " + t.Name
	})
	st("st-reshape-modify", func(c *Ctx, r *RNG, i int, ts *[]*tensor, d, m *[]int) string {
		t := (*ts)[1+r.Intn(8)]
		t.setShape(256 >> (i % 3))
		perturb(t.Data, t.DT.size, 0.005, r)
		return "reshape and perturb 0.5% of " + t.Name
	})
	st("st-unrelated", func(c *Ctx, r *RNG, i int, ts *[]*tensor, d, m *[]int) string {
		*ts = modelTensors(c.size, r)
		return "independent model, same architecture"
	})
}

// ---- GGUF ----------------------------------------------------------------

// GGUF subset: v3, little-endian, scalar metadata (u32, f32, string), tensor
// types F32/F16/Q4_0/Q8_0, general.alignment 32. No arrays.

const (
	ggufF32  = 0
	ggufF16  = 1
	ggufQ4_0 = 2
	ggufQ8_0 = 8
)

type gtensor struct {
	Name string
	Type uint32
	Elem int
	Data []byte
}

func ggufBlock(t uint32) (elems, bytes int) {
	switch t {
	case ggufQ4_0:
		return 32, 18
	case ggufQ8_0:
		return 32, 34
	case ggufF16:
		return 1, 2
	}
	return 1, 4
}

// quantBlocks produces quantised-style blocks: f16 scale + packed values.
func quantBlocks(t uint32, nblocks int, r *RNG) []byte {
	_, bs := ggufBlock(t)
	out := make([]byte, 0, nblocks*bs)
	for i := 0; i < nblocks; i++ {
		out = append(out, randWeights(dtFP16, 2, r)...)
		out = append(out, r.Bytes(bs-2)...)
	}
	return out
}

func newGTensor(name string, t uint32, bytes int, r *RNG) *gtensor {
	e, bs := ggufBlock(t)
	nb := max(1, bytes/bs)
	g := &gtensor{Name: name, Type: t, Elem: nb * e}
	if t == ggufQ4_0 || t == ggufQ8_0 {
		g.Data = quantBlocks(t, nb, r)
	} else {
		g.Data = randWeights(map[uint32]dtype{ggufF16: dtFP16, ggufF32: dtFP32}[t], nb*bs, r)
	}
	return g
}

type gkv struct {
	Key  string
	Kind uint32 // 4 u32, 6 f32, 8 string
	S    string
	N    uint32
}

func gstr(b *bytes.Buffer, s string) {
	binary.Write(b, binary.LittleEndian, uint64(len(s)))
	b.WriteString(s)
}

func writeGGUF(kvs []gkv, ts []*gtensor) []byte {
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("GGUF")
	binary.Write(&b, le, uint32(3))
	binary.Write(&b, le, uint64(len(ts)))
	binary.Write(&b, le, uint64(len(kvs)))
	for _, kv := range kvs {
		gstr(&b, kv.Key)
		binary.Write(&b, le, kv.Kind)
		switch kv.Kind {
		case 8:
			gstr(&b, kv.S)
		default:
			binary.Write(&b, le, kv.N)
		}
	}
	off := 0
	for _, t := range ts {
		gstr(&b, t.Name)
		binary.Write(&b, le, uint32(1))
		binary.Write(&b, le, uint64(t.Elem))
		binary.Write(&b, le, t.Type)
		binary.Write(&b, le, uint64(off))
		off += (len(t.Data) + 31) &^ 31
	}
	for b.Len()%32 != 0 {
		b.WriteByte(0)
	}
	for _, t := range ts {
		b.Write(t.Data)
		for b.Len()%32 != 0 {
			b.WriteByte(0)
		}
	}
	return b.Bytes()
}

func baseKVs(name string) []gkv {
	return []gkv{
		{Key: "general.architecture", Kind: 8, S: "llama"},
		{Key: "general.name", Kind: 8, S: name},
		{Key: "general.alignment", Kind: 4, N: 32},
		{Key: "general.quantization_version", Kind: 4, N: 2},
		{Key: "llama.block_count", Kind: 4, N: 6},
		{Key: "llama.context_length", Kind: 4, N: 4096},
	}
}

func init() {
	g := func(name string, dup bool, fn func(c *Ctx, r *RNG, i int, kvs []gkv, ts []*gtensor) string) {
		register(Family{Name: name, Format: "gguf", Workload: "model-weights", Ext: ".gguf",
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				unit := c.size / 14
				ts := []*gtensor{newGTensor("token_embd.weight", ggufQ8_0, 2*unit, r)}
				for l := 0; l < 5; l++ {
					a := newGTensor(fmt.Sprintf("blk.%d.attn_q.weight", l), ggufQ4_0, unit, r)
					f := newGTensor(fmt.Sprintf("blk.%d.ffn_up.weight", l), ggufQ8_0, unit, r)
					if dup && l > 0 {
						a.Data = append([]byte(nil), ts[1].Data...)
						a.Elem = ts[1].Elem
					}
					ts = append(ts, a, f)
				}
				ts = append(ts, newGTensor("output_norm.weight", ggufF32, 4096, r))
				kvs := baseKVs("z3-model")
				prev := map[string]string{}
				for _, t := range ts {
					prev[t.Name] = contentKey(t.Data)
				}
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					desc := "base"
					var truth map[string][]string
					if i > 0 {
						desc = fn(c, r, i, kvs, ts)
						cur := map[string]string{}
						for _, t := range ts {
							cur[t.Name] = contentKey(t.Data)
						}
						truth = truthSets(prev, cur)
						prev = cur
					}
					if dup {
						seen := map[string][]string{}
						for _, t := range ts {
							k := contentKey(t.Data)
							seen[k] = append(seen[k], t.Name)
						}
						var groups []string
						for _, names := range seen {
							if len(names) > 1 {
								groups = append(groups, strings.Join(names, "="))
							}
						}
						sort.Strings(groups)
						if truth == nil {
							truth = map[string][]string{}
						}
						truth["duplicate_groups"] = groups
					}
					return writeGGUF(kvs, ts), desc, truth
				})
			}})
	}
	g("gguf-metadata", false, func(c *Ctx, r *RNG, i int, kvs []gkv, ts []*gtensor) string {
		kvs[1].S = "z3-model-rev" + strings.Repeat("x", 11*i) + strconv.Itoa(i)
		kvs[3].N = uint32(2 + i)
		return "change general.name (length grows) and quantization_version"
	})
	g("gguf-modify-tensor", false, func(c *Ctx, r *RNG, i int, kvs []gkv, ts []*gtensor) string {
		k := 1 + r.Intn(len(ts)-2)
		*ts[k] = *newGTensor(ts[k].Name, ts[k].Type, len(ts[k].Data), r)
		return "re-randomise " + ts[k].Name
	})
	g("gguf-requant", false, func(c *Ctx, r *RNG, i int, kvs []gkv, ts []*gtensor) string {
		for _, t := range ts {
			if t.Type != ggufQ4_0 && t.Type != ggufQ8_0 {
				continue
			}
			_, bs := ggufBlock(t.Type)
			nb := len(t.Data) / bs
			for k := 0; k < nb/30+1; k++ {
				b := r.Intn(nb) * bs
				copy(t.Data[b:b+2], randWeights(dtFP16, 2, r))
				t.Data[b+2+r.Intn(bs-2)] ^= byte(1 + r.Intn(255))
			}
		}
		return "rewrite ~3% of quantised blocks in every quantised tensor"
	})
	g("gguf-repeated", true, func(c *Ctx, r *RNG, i int, kvs []gkv, ts []*gtensor) string {
		k := 1 + 2*r.Intn(5)
		perturb(ts[k].Data, 1, 0.01, r)
		return "edit one copy of repeated tensor " + ts[k].Name
	})
}
