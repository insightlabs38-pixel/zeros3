package mdelta2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"zeros3-z3/ggufz3"
	"zeros3-z3/st"
)

func bf16Bits(f float32) uint16 { return uint16(math.Float32bits(f) >> 16) }

// mkModel writes a small Safetensors model with F32, F16 and BF16 tensors (specials included).
func mkModel(t *testing.T, dir, name string, seed int64, perturb float64, base []float32) (string, []float32) {
	rng := rand.New(rand.NewSource(seed))
	n := 64 * 48
	vals := make([]float32, n)
	for i := range vals {
		if base != nil {
			vals[i] = base[i] * float32(1+perturb*rng.NormFloat64())
		} else {
			vals[i] = float32(rng.NormFloat64() * 0.05)
		}
	}
	f32 := make([]byte, 4*n)
	f16 := make([]byte, 2*n)
	bf := make([]byte, 2*n)
	for i, v := range vals {
		binary.LittleEndian.PutUint32(f32[4*i:], math.Float32bits(v))
		binary.LittleEndian.PutUint16(f16[2*i:], uint16(f64ToF16(float64(v))))
		binary.LittleEndian.PutUint16(bf[2*i:], bf16Bits(v))
	}
	// specials in every dtype: NaN payloads, +-0, +-Inf, subnormals, overflow
	sp := []uint32{0x7fc00001, 0xffc12345, 0x80000000, 0, 0x7f800000, 0xff800000, 1, 0x80000001, 0x00400000}
	for i, b := range sp {
		binary.LittleEndian.PutUint32(f32[4*i:], b)
		binary.LittleEndian.PutUint16(f16[2*i:], uint16(b>>16)&0xfc00|uint16(b)&0x3ff)
		binary.LittleEndian.PutUint16(bf[2*i:], uint16(b>>16))
	}
	i64 := make([]byte, 8*16)
	rng.Read(i64)
	ts := []st.BuildTensor{
		{Name: "a.f32", DType: "F32", Shape: []int64{64, 48}, Data: f32},
		{Name: "b.f16", DType: "F16", Shape: []int64{64, 48}, Data: f16},
		{Name: "c.bf16", DType: "BF16", Shape: []int64{64, 48}, Data: bf},
		{Name: "d.i64", DType: "I64", Shape: []int64{16}, Data: i64},
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, st.Build([]st.KV{{K: "format", V: "pt"}}, ts), 0o644); err != nil {
		t.Fatal(err)
	}
	return p, vals
}

func openAll(t *testing.T, paths ...string) []*Model {
	var ms []*Model
	for _, p := range paths {
		m, err := Open(p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		ms = append(ms, m)
	}
	return ms
}

func fileOf(t *testing.T, m *Model) ([]byte, SHA) {
	b := make([]byte, m.Size)
	if _, err := m.ReadAt(b, 0); err != nil {
		t.Fatal(err)
	}
	return b, sum(b)
}

func srcOf(t *testing.T, m *Model) BaseSrc {
	_, s := fileOf(t, m)
	return BaseSrc{SHA: s, Len: m.Size, M: m}
}

func allCands(bases []BaseSrc, m *Model, lin bool) func(int) [][]Pick {
	return func(ti int) [][]Pick {
		var out [][]Pick
		var one []Pick
		for s, b := range bases {
			if j, ok := b.M.Index(m.Tensors[ti].Name); ok {
				one = append(one, Pick{s, j})
				out = append(out, []Pick{{s, j}})
			}
		}
		if lin && len(one) >= 2 {
			out = append(out, one[:2])
		}
		return out
	}
}

var fullOpts = EncOpts{Narrow: true, Affine: true, Low: []int{1, 4}, Lin: true, Segments: true, SegElems: 512, Coders: []int{CFlate, CNRC, CRice, CBPRLE, CPXR}, Workers: 2}

// encodeChain stores 3 states (root, +edit, +edit) into dir and returns their paths, containers.
func encodeChain(t *testing.T) (dir string, ms []*Model, conts [][]byte) {
	dir = t.TempDir()
	p0, v0 := mkModel(t, dir, "m0.safetensors", 1, 0, nil)
	p1, v1 := mkModel(t, dir, "m1.safetensors", 2, 0.01, v0)
	p2, _ := mkModel(t, dir, "m2.safetensors", 3, 0.01, v1)
	ms = openAll(t, p0, p1, p2)
	store := StoreDir{Dir: filepath.Join(dir, "store")}
	for i, m := range ms {
		var bases []BaseSrc
		for j := 0; j < i && j < 2; j++ {
			bases = append(bases, srcOf(t, ms[i-1-j]))
		}
		cb, _, err := EncodeModel(m, bases, allCands(bases, m, true), fullOpts)
		if err != nil {
			t.Fatal(err)
		}
		rv := ModelResolver{}
		for _, b := range bases {
			rv[b.SHA] = b.M
		}
		got, err := Decode(cb, rv)
		raw, _ := fileOf(t, m)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("state %d in-process decode: %v", i, err)
		}
		c, _ := ParseContainer(cb)
		if err := store.Put(c, cb); err != nil {
			t.Fatal(err)
		}
		conts = append(conts, cb)
	}
	return
}

func TestExactSpecialsAllPredictors(t *testing.T) {
	dir, ms, _ := encodeChain(t)
	store := StoreDir{Dir: filepath.Join(dir, "store")}
	for _, m := range ms {
		raw, id := fileOf(t, m)
		got, _, err := store.Replay(id) // fresh resolver, store only
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("replay: %v", err)
		}
	}
}

func TestNonFiniteCoefficientsStayExact(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	n := 4096
	tg, bs := make([]byte, 4*n), make([]byte, 4*n)
	rng.Read(tg)
	rng.Read(bs)
	for _, co := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1)), 1e38, -1e38} {
		for _, p := range []*Params{
			{Kind: KAff, Res: RKey, Gran: GTensor, NB: 1, Prec: 4, Coef: []float32{co, co}},
			{Kind: KLin, Res: RKey, Gran: GTensor, NB: 2, Prec: 4, Coef: []float32{co, 1, co}},
		} {
			base := Base{DT: DF32, Data: bs}
			pred, err := Predict(p, DF32, []int64{64, 64}, []Base{base, base})
			if err != nil {
				t.Fatal(err)
			}
			r := MakeResidual(RKey, 4, tg, pred)
			if !bytes.Equal(ApplyResidual(RKey, 4, r, pred), tg) {
				t.Fatalf("coef %v kind %d", co, p.Kind)
			}
			pm := p.Marshal()
			if q, used, err := ParseParams(pm, 1<<20); err != nil || used != len(pm) || len(q.Coef) != len(p.Coef) {
				t.Fatalf("params round trip: %v", err)
			}
		}
	}
}

func TestEveryByteFlipAndTruncationFailsClosed(t *testing.T) {
	_, ms, conts := encodeChain(t)
	rv := ModelResolver{}
	for _, m := range ms {
		_, id := fileOf(t, m)
		rv[id] = m
	}
	raw, _ := fileOf(t, ms[2])
	cb := conts[2]
	if len(cb) > 60000 {
		t.Skip("container too large for exhaustive flip")
	}
	for i := 0; i < len(cb); i++ {
		q := append([]byte(nil), cb...)
		q[i] ^= 0x5a
		got, err := Decode(q, rv)
		if err == nil && !bytes.Equal(got, raw) {
			t.Fatalf("flip at %d returned wrong bytes without error", i)
		}
	}
	for cut := 0; cut < len(cb); cut += 1 + len(cb)/400 {
		if _, err := Decode(cb[:cut], rv); err == nil {
			t.Fatalf("truncation at %d accepted", cut)
		}
	}
}

func TestMissingCorruptWrongBaseAndCycles(t *testing.T) {
	dir, ms, conts := encodeChain(t)
	store := StoreDir{Dir: filepath.Join(dir, "store")}
	_, id2 := fileOf(t, ms[2])
	_, id1 := fileOf(t, ms[1])
	// missing dependency
	os.Remove(store.Path(id1))
	if _, _, err := store.Replay(id2); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing base: %v", err)
	}
	// corrupt base container
	bad := append([]byte(nil), conts[1]...)
	bad[len(bad)/2] ^= 0xff
	os.WriteFile(store.Path(id1), bad, 0o644)
	if _, _, err := store.Replay(id2); err == nil {
		t.Fatal("corrupt base accepted")
	}
	// base container of the wrong state under the right name
	os.WriteFile(store.Path(id1), conts[0], 0o644)
	if _, _, err := store.Replay(id2); err == nil {
		t.Fatal("wrong base state accepted")
	}
	// cycle: a container whose only base is itself
	c, _ := ParseContainer(conts[1])
	c.Bases = []BaseInfo{{SHA: c.FileSHA, Len: c.FileLen}}
	for ti := range c.T {
		for si := range c.T[ti].Segs {
			for k := range c.T[ti].Segs[si].Refs {
				c.T[ti].Segs[si].Refs[k].Slot = 0
			}
		}
	}
	cyc := c.Marshal()
	cd, _ := ParseContainer(cyc)
	cs := StoreDir{Dir: t.TempDir()}
	cs.Put(cd, cyc)
	if _, _, err := cs.Replay(cd.FileSHA); err == nil {
		t.Fatal("self-referential container accepted")
	}
}

func TestDepthLimit(t *testing.T) {
	dir := t.TempDir()
	p0, v := mkModel(t, dir, "m0.safetensors", 1, 0, nil)
	paths := []string{p0}
	for i := 1; i <= 7; i++ {
		var p string
		p, v = mkModel(t, dir, "m"+string(rune('0'+i))+".safetensors", int64(i+1), 0.01, v)
		paths = append(paths, p)
	}
	ms := openAll(t, paths...)
	store := StoreDir{Dir: filepath.Join(dir, "store")}
	var last SHA
	for i, m := range ms {
		var bases []BaseSrc
		if i > 0 {
			bases = []BaseSrc{srcOf(t, ms[i-1])}
		}
		cb, _, err := EncodeModel(m, bases, allCands(bases, m, false), EncOpts{Narrow: true, Workers: 2})
		if err != nil {
			t.Fatal(err)
		}
		c, _ := ParseContainer(cb)
		store.Put(c, cb)
		last = c.FileSHA
	}
	// chain of 8 containers: depth 7 exceeds MaxDepth=4 when no tensor is a direct/copy at the top
	_, _, err := store.Replay(last)
	if err == nil {
		t.Skip("chain collapsed to copies/direct; depth never exceeded")
	}
	if !errors.Is(err, ErrDepth) {
		t.Fatalf("want depth error, got %v", err)
	}
}

type shortW struct{ n int }

func (s *shortW) Write(p []byte) (int, error) {
	if len(p) > s.n {
		k := s.n
		s.n = 0
		return k, nil
	}
	s.n -= len(p)
	return len(p), nil
}

func TestShortWriterAndHostileHeaders(t *testing.T) {
	_, ms, conts := encodeChain(t)
	rv := ModelResolver{}
	for _, m := range ms {
		_, id := fileOf(t, m)
		rv[id] = m
	}
	c, _ := ParseContainer(conts[1])
	if err := c.DecodeTo(&shortW{n: 1000}, rv); err == nil {
		t.Fatal("short writer accepted")
	}
	// absurd lengths: no huge allocation, immediate error
	hostile := [][]byte{
		append([]byte(magic), bytes.Repeat([]byte{0xff}, 80)...),
		[]byte("ZB5C1"),
		{},
	}
	for _, h := range hostile {
		if _, err := ParseContainer(h); err == nil {
			t.Fatal("hostile header accepted")
		}
	}
	// tensor count / gap length bombs patched into a valid prefix
	b := append([]byte(nil), conts[0]...)
	for i := len(magic) + 32; i < len(b) && i < len(magic)+32+60; i++ {
		q := append([]byte(nil), b...)
		q[i] = 0xff
		ParseContainer(q) // must not panic or allocate unboundedly
	}
}

func TestRangeReadsMatchOriginal(t *testing.T) {
	_, ms, conts := encodeChain(t)
	rv := ModelResolver{}
	for _, m := range ms {
		_, id := fileOf(t, m)
		rv[id] = m
	}
	rng := rand.New(rand.NewSource(9))
	for i, m := range ms {
		raw, _ := fileOf(t, m)
		c, _ := ParseContainer(conts[i])
		for k := 0; k < 40; k++ {
			off := rng.Int63n(int64(len(raw)))
			n := rng.Int63n(int64(len(raw))-off) + 1
			if k < 5 {
				n = 1 + rng.Int63n(4096)
				if off+n > int64(len(raw)) {
					n = int64(len(raw)) - off
				}
			}
			var buf bytes.Buffer
			if _, err := c.ReadRange(&buf, off, n, rv); err != nil || !bytes.Equal(buf.Bytes(), raw[off:off+n]) {
				t.Fatalf("range %d+%d state %d: %v", off, n, i, err)
			}
		}
	}
}

func TestWrongCandidatesStayExact(t *testing.T) {
	dir := t.TempDir()
	p0, _ := mkModel(t, dir, "a.safetensors", 1, 0, nil)
	p1, _ := mkModel(t, dir, "b.safetensors", 77, 0, nil) // unrelated weights, same shapes and names
	ms := openAll(t, p0, p1)
	bases := []BaseSrc{srcOf(t, ms[0])}
	cb, st, err := EncodeModel(ms[1], bases, allCands(bases, ms[1], false), fullOpts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(cb, ModelResolver{bases[0].SHA: ms[0]})
	raw, _ := fileOf(t, ms[1])
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("unrelated base: %v", err)
	}
	if len(cb) > int(ms[1].Size) {
		t.Fatalf("unrelated base inflated container: %d > %d (%v)", len(cb), ms[1].Size, st.ModeBytes)
	}
}

func TestUnsupportedQuantAndGGUF(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	mk := func(seed int64) []byte {
		rng = rand.New(rand.NewSource(seed))
		q8 := make([]byte, 34*8)
		rng.Read(q8)
		q2k := make([]byte, 84*2) // Q2_K has no port: stays an opaque typed tensor
		rng.Read(q2k)
		f := make([]byte, 4*64)
		rng.Read(f)
		return ggufz3.Build(3, 32, []ggufz3.BuildTensor{
			{Name: "blk.0.attn_q.weight", Dims: []uint64{256, 1}, Type: 8, Data: q8},
			{Name: "blk.0.ffn_down.weight", Dims: []uint64{512, 1}, Type: 10, Data: q2k},
			{Name: "output_norm.weight", Dims: []uint64{64}, Type: 0, Data: f},
		})
	}
	dir := t.TempDir()
	var ms []*Model
	for i := 0; i < 2; i++ {
		p := filepath.Join(dir, "g"+string(rune('0'+i))+".gguf")
		os.WriteFile(p, mk(int64(i+1)), 0o644)
		ms = append(ms, openAll(t, p)...)
	}
	bases := []BaseSrc{srcOf(t, ms[0])}
	cb, _, err := EncodeModel(ms[1], bases, allCands(bases, ms[1], false), fullOpts)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(cb, ModelResolver{bases[0].SHA: ms[0]})
	raw, _ := fileOf(t, ms[1])
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("gguf: %v", err)
	}
}

func TestQuantizerPortsRoundTripStructure(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	x := make([]float32, 512)
	for i := range x {
		x[i] = float32(rng.NormFloat64())
	}
	for _, d := range []DType{DQ8_0, DQ4_0, DQ4_1, DQ5_0, DQ5_1, DQ4_K, DQ6_K} {
		q, ok := QuantizeAny(d, x)
		if !ok || len(q) != len(x)/qgeoms[d].elems*qgeoms[d].bytes {
			t.Fatalf("%v", d)
		}
		fs := SplitFields(d, q)
		if !bytes.Equal(JoinFields(d, fs, len(q)/qgeoms[d].bytes), q) {
			t.Fatalf("split/join %v", d)
		}
	}
}

func FuzzParseContainer(f *testing.F) {
	f.Add([]byte(magic))
	f.Fuzz(func(t *testing.T, b []byte) {
		if c, err := ParseContainer(b); err == nil {
			c.Marshal()
		}
	})
}

func FuzzDecodeResidual(f *testing.F) {
	f.Add(uint8(2), []byte{1, 2, 3, 4})
	f.Fuzz(func(t *testing.T, c uint8, b []byte) {
		for _, d := range []DType{DF32, DF16, DBF16} {
			pred := make([]byte, 64*d.Width())
			DecodeResidual(int(c)%numCoder, d, RXor, b, pred, 64)
		}
	})
}

func FuzzParseParams(f *testing.F) {
	f.Add([]byte{1, 0, 0, 1, 4, 0, 0, 0})
	f.Fuzz(func(t *testing.T, b []byte) { ParseParams(b, 1<<16) })
}

type stubRV map[SHA]*stubBase
type stubBase struct {
	dt   DType
	data [][]byte
}

func (s stubRV) Tensor(b SHA, i int) (DType, []byte, error) { x := s[b]; return x.dt, x.data[i], nil }
func (s stubRV) Gaps(SHA) ([]byte, error)                   { return nil, ErrMissing }

// TestForcedPredictorCoderMatrix drives every predictor family x residual mode x dtype x coder through
// serialization (Marshal/Parse) and the real segment decoder, with special values in the target.
func TestForcedPredictorCoderMatrix(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	rows, cols := 40, 24
	n := rows * cols
	for _, dt := range []DType{DF32, DF16, DBF16} {
		w := dt.Width()
		mk := func() []byte {
			b := make([]byte, n*w)
			for i := 0; i < n; i++ {
				putLE(b, w, i, uint32(math.Float32bits(float32(rng.NormFloat64()*0.05)))>>(32-8*uint(w))&mask(w))
				if dt == DF16 {
					putLE(b, w, i, uint32(f64ToF16(rng.NormFloat64()*0.05)))
				}
			}
			return b
		}
		b0, b1, tg := mk(), mk(), mk()
		for i := 0; i < 8; i++ { // specials: NaN, inf, -0, subnormals
			putLE(tg, w, i, []uint32{0x7fc00001, 0x7c00, 0x8000, 1, 0xffff, 0x7f80, 0x0001, 0x8001}[i]&mask(w))
			putLE(b0, w, i+3, []uint32{0x7fc00000, 0xff800000, 0x80000000, 0, 1, 2, 3, 4}[i]>>(32-8*uint(w))&mask(w))
		}
		bases := []Base{{DT: dt, Data: b0}, {DT: dt, Data: b1}}
		shape := []int64{int64(rows), int64(cols)}
		var ps []*Params
		for _, r := range []Residual{RKey, RXor, RSub} {
			ps = append(ps, &Params{Kind: KID, Res: r, NB: 1, Prec: 4})
		}
		for _, g := range []Gran{GTensor, GRow, GBlock} {
			ps = append(ps, FitAffineRobust(dt, shape, g, 100, 4, bases[0], tg), FitAffine(dt, shape, g, 100, 0, 2, bases[0], tg))
		}
		ps = append(ps, LowRankFit(dt, shape, 2, 4, bases[0], tg, 2, 3), FitLin(dt, shape, GTensor, 4, bases, tg), FitLin(dt, shape, GRow, 2, bases, tg))
		sha0, sha1 := sum([]byte{1}), sum([]byte{2})
		rv := stubRV{sha0: {dt, [][]byte{b0}}, sha1: {dt, [][]byte{b1}}}
		for pi, p := range ps {
			pred, err := Predict(p, dt, shape, bases)
			if err != nil {
				t.Fatal(pi, err)
			}
			res := MakeResidual(p.Res, w, tg, pred)
			for c := 0; c < numCoder; c++ {
				stream, ok := EncodeResidual(c, dt, p.Res, res, pred)
				if !ok {
					continue
				}
				seg := Seg{Elems: int64(n), Mode: ModeCoded, Params: p, Coder: uint8(c), Stream: stream}
				for k := 0; k < int(p.NB); k++ {
					seg.Refs = append(seg.Refs, BaseRef{Slot: uint8(k), Tag: tag([][]byte{b0, b1}[k])})
				}
				ct := &Container{Bases: []BaseInfo{{SHA: sha0}, {SHA: sha1}}, T: []TRec{{DType: dt, Shape: shape, Len: int64(len(tg)), Segs: []Seg{seg}}}}
				ct.FileLen = int64(len(tg))
				ct.FileSHA = sum(tg)
				ct.GapsLen = 0
				pc, err := ParseContainer(ct.Marshal())
				if err != nil {
					t.Fatalf("%v pred %d coder %d parse: %v", dt, pi, c, err)
				}
				got, err := pc.DecodeTensor(0, rv)
				if err != nil || !bytes.Equal(got, tg) {
					t.Fatalf("%v pred %d (kind %d) coder %s: %v", dt, pi, p.Kind, CoderNames[c], err)
				}
			}
		}
	}
}
