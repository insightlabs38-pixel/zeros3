package modelz3

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"

	"zeros3-z3/ggufz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/st"
)

// SYNTHETIC_CONTROL files (parser/ground-truth tests only, never evidence): a base and a chain of
// revisions whose tensors are unchanged, slightly moved (the low byte of some elements) or
// completely rewritten, so every encoder candidate (copy, copy-of-base, xor residual, planes) is
// exercised and the chain depth bound is reached.
type tens struct {
	name  string
	shape []int64
	data  []byte
}

type fmtSpec struct {
	name   string
	w      int // plane width: element bytes (Safetensors) / block bytes (GGUF Q8_0)
	bytesN func(shape []int64) int
	file   func([]tens) []byte
	layout func(*testing.T, *Src) *Layout
}

var specs = []fmtSpec{
	{"safetensors", 2, func(sh []int64) int {
		n := 2
		for _, d := range sh {
			n *= int(d)
		}
		return n
	}, func(ts []tens) []byte {
		var bt []st.BuildTensor
		for _, t := range ts {
			bt = append(bt, st.BuildTensor{Name: t.name, DType: "BF16", Shape: t.shape, Data: t.data})
		}
		return st.Build([]st.KV{{K: "format", V: "pt"}}, bt)
	}, func(t *testing.T, s *Src) *Layout {
		l, _, err := STLayout(s.R, s.Size)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}},
	{"gguf-q8_0", 34, func(sh []int64) int {
		n := 1
		for _, d := range sh {
			n *= int(d)
		}
		return n / 32 * 34
	}, func(ts []tens) []byte {
		var bt []ggufz3.BuildTensor
		for _, t := range ts {
			d := make([]uint64, len(t.shape))
			for i, s := range t.shape {
				d[i] = uint64(s)
			}
			bt = append(bt, ggufz3.BuildTensor{Name: t.name, Dims: d, Type: 8, Data: t.data})
		}
		return ggufz3.Build(3, 32, bt)
	}, func(t *testing.T, s *Src) *Layout {
		l, err := GGUFLayout(s.R, s.Size)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}},
}

func revisions(sp fmtSpec, n int) [][]byte {
	rng := rand.New(rand.NewSource(7))
	mk := func(name string, sh []int64) tens {
		b := make([]byte, sp.bytesN(sh))
		rng.Read(b)
		return tens{name, sh, b}
	}
	cur := []tens{mk("a.weight", []int64{64, 64}), mk("b.weight", []int64{32, 64}), mk("c.bias", []int64{64}), mk("d.weight", []int64{64, 64})}
	files := [][]byte{sp.file(cur)}
	for k := 1; k < n; k++ {
		nx := make([]tens, len(cur))
		for i, t := range cur {
			switch i % 4 {
			case 0:
				nx[i] = t
			case 1, 2:
				b := append([]byte(nil), t.data...)
				for j := 0; j < len(b); j += sp.w {
					if rng.Float64() < 0.1 {
						b[j] += byte(1 + rng.Intn(3))
					}
				}
				nx[i] = tens{t.name, t.shape, b}
			default:
				nx[i] = mk(t.name, t.shape)
			}
		}
		cur = nx
		files = append(files, sp.file(cur))
	}
	return files
}

func TestModelChainExactAndBounded(t *testing.T) {
	for _, sp := range specs {
		t.Run(sp.name, func(t *testing.T) {
			files := revisions(sp, 6)
			store := repz3.NewMemStore()
			var base map[string]*BaseTensor
			maxHops := 0
			for k, f := range files {
				src := &Src{R: bytes.NewReader(f), Size: int64(len(f)), Name: "f"}
				lay := sp.layout(t, src)
				b, bts, _, err := EncodeModel(src, lay, []map[string]*BaseTensor{base}, store.Has, Policy{Delta: true, Planes: true, Chunk: 512})
				if err != nil {
					t.Fatal(err)
				}
				id, _, err := Commit(store, b)
				if err != nil {
					t.Fatalf("file %d: %v", k, err)
				}
				if err := Prove(store, id, src); err != nil {
					t.Fatalf("file %d: %v", k, err)
				}
				for _, bt := range bts {
					maxHops = max(maxHops, bt.Hops)
				}
				// point reads: the name index, one tensor, a small range inside it
				env := kinds.NewEnvLimits(store, repz3.Limits{MaxDepth: repz3.HardMaxDepth})
				c, err := env.Open(id)
				if err != nil {
					t.Fatal(err)
				}
				names, err := kinds.ModelNames(c)
				if err != nil {
					t.Fatal(err)
				}
				for _, tn := range lay.Tensors() {
					loc, ok := names[tn.Name]
					if !ok || loc[0] != tn.Begin || loc[1] != tn.Size() {
						t.Fatalf("file %d: name index %q = %v want [%d %d]", k, tn.Name, loc, tn.Begin, tn.Size())
					}
					for _, rg := range [][2]int64{{5, 37}, {0, tn.Size()}, {tn.Size() - 3, 3}} {
						got, err := kinds.ReadModelRange(c, loc[0]+rg[0], rg[1])
						if err != nil || !bytes.Equal(got, f[loc[0]+rg[0]:loc[0]+rg[0]+rg[1]]) {
							t.Fatalf("file %d: range %v of %q: %v", k, rg, tn.Name, err)
						}
					}
				}
				base = bts
				if k == 2 {
					// liveness without a BASE primitive: every stored base a descendant's chain needs
					// is reachable from the descendant's representation alone
					r, err := env.Reachable(id)
					if err != nil {
						t.Fatal(err)
					}
					need := 0
					for _, bt := range bts {
						for s := bt; s != nil && !s.Raw && s.spec != nil; s = s.spec.base {
							if s.spec.base != nil && s.spec.base.Raw {
								need++
								if _, ok := r.Role[s.spec.base.SHA]; !ok {
									t.Fatalf("raw base %x of %q is not reachable from the descendant", s.spec.base.SHA[:6], bt.Name)
								}
							}
						}
					}
					if need == 0 {
						t.Fatal("no delta tensor in the chain")
					}
				}
			}
			if maxHops < 2 || maxHops > 3 {
				t.Fatalf("chain reached %d hops, want 2..3", maxHops)
			}
		})
	}
}

func TestChainBeyondContractDepthRefused(t *testing.T) {
	sp := specs[0]
	files := revisions(sp, 6)
	store := repz3.NewMemStore()
	var base map[string]*BaseTensor
	refused := false
	for _, f := range files {
		src := &Src{R: bytes.NewReader(f), Size: int64(len(f)), Name: "f"}
		b, bts, _, err := EncodeModel(src, sp.layout(t, src), []map[string]*BaseTensor{base}, store.Has, Policy{Delta: true, Planes: true, Chunk: 512, MaxHops: 99})
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := Commit(store, b)
		if err == nil {
			err = Prove(store, id, src)
		}
		if err != nil {
			if !errors.Is(err, repz3.ErrDepth) {
				t.Fatalf("want ErrDepth, got %v", err)
			}
			refused = true
			break
		}
		base = bts
	}
	if !refused {
		t.Fatal("an unbounded chain was reconstructed past the contract depth")
	}
}

func TestModelRecipeMalformed(t *testing.T) {
	store := repz3.NewMemStore()
	data := bytes.Repeat([]byte{7}, 64)
	id, _ := store.Put(data)
	mem := repz3.Member{Role: repz3.Stored, SHA: id, Size: 64}
	good := func() *repz3.Rep {
		return &repz3.Rep{Kind: repz3.KindModel, Size: 64, Members: []repz3.Member{mem},
			Recipe: kinds.BuildModelRecipe([]kinds.ModelOp{{Tag: kinds.ModelCopy, M: 0, Name: "x"}})}
	}
	try := func(r *repz3.Rep) error {
		raw, err := r.Marshal()
		if err != nil {
			return err
		}
		rid, _ := store.Put(raw)
		_, _, err = kinds.NewEnv(store).Reconstruct(rid, &bytes.Buffer{})
		return err
	}
	if err := try(good()); err != nil {
		t.Fatalf("good recipe: %v", err)
	}
	mut := map[string]func(*repz3.Rep){
		"empty recipe":     func(r *repz3.Rep) { r.Recipe = nil },
		"bad version":      func(r *repz3.Rep) { r.Recipe[0] = 9 },
		"truncated":        func(r *repz3.Rep) { r.Recipe = r.Recipe[:3] },
		"member index":     func(r *repz3.Rep) { r.Recipe[3] = 5 },
		"size mismatch":    func(r *repz3.Rep) { r.Size = 65 },
		"trailing":         func(r *repz3.Rep) { r.Recipe = append(r.Recipe, 0) },
		"hostile op count": func(r *repz3.Rep) { r.Recipe = []byte{1, 0xff, 0xff, 0xff, 0xff, 0x0f} },
		"zero width": func(r *repz3.Rep) {
			r.Recipe = kinds.BuildModelRecipe([]kinds.ModelOp{{Tag: kinds.ModelPlanes, Res: 0, W: 0, E: 1}})
		},
		"width not dividing": func(r *repz3.Rep) {
			r.Recipe = kinds.BuildModelRecipe([]kinds.ModelOp{{Tag: kinds.ModelPlanes, Res: 0, W: 3, E: 1}})
		},
		"huge chunk": func(r *repz3.Rep) {
			r.Recipe = kinds.BuildModelRecipe([]kinds.ModelOp{{Tag: kinds.ModelPlanes, Res: 0, W: 2, E: 1 << 40}})
		},
		"chunk bytes beyond the cap": func(r *repz3.Rep) {
			r.Recipe = kinds.BuildModelRecipe([]kinds.ModelOp{{Tag: kinds.ModelPlanes, Res: 0, W: 2, E: 1 << 24}})
		},
	}
	for name, f := range mut {
		r := good()
		f(r)
		if err := try(r); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a component whose bytes are not the ones named
	bad := good()
	bad.Members[0].SHA[0] ^= 1
	if err := try(bad); err == nil {
		t.Error("missing component accepted")
	}
}
