package main

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"zeros3-z3/lab"
	"zeros3-z3/modelz3"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

type family struct {
	Name, Format string
	Revs         []string
}

func families() []family {
	return []family{
		{"Qwen2.5-0.5B BF16 base>Instruct", "st", []string{"st-qwen05/base", "st-qwen05/instruct"}},
		{"SmolLM2-135M BF16 star (base, instruct, sft-only, shakespeare)", "st", []string{"st-smollm2-star/base", "st-smollm2-star/instruct", "st-smollm2-star/sft-only", "st-smollm2-star/shakespeare"}},
		{"SmolLM2-135M GGUF Q4_K_M base>instruct", "gguf", []string{"gguf-smollm2/qf-base-Q4_K_M", "gguf-smollm2/qf-inst-Q4_K_M"}},
		{"SmolLM2-135M GGUF Q8_0 base>instruct", "gguf", []string{"gguf-smollm2/qf-base-Q8_0", "gguf-smollm2/qf-inst-Q8_0"}},
		{"Qwen2.5-0.5B GGUF Q4_K_M base>instruct", "gguf", []string{"gguf-qwen05/qf-base-Q4_K_M", "gguf-qwen05/qf-inst-Q4_K_M"}},
		{"Qwen3-0.6B GGUF Q4_K_M base>post", "gguf", []string{"gguf-qwen3/mr-base-Q4_K_M", "gguf-qwen3/mr-post-Q4_K_M"}},
		{"Pythia-70M GGUF f16 chain (REAL_REEXPORT)", "gguf", []string{"derived/py70/step1000.f16.gguf", "derived/py70/step2000.f16.gguf", "derived/py70/step4000.f16.gguf", "derived/py70/step8000.f16.gguf", "derived/py70/main.f16.gguf"}},
		{"Pythia-70M GGUF Q4_K_M chain (REAL_REEXPORT)", "gguf", []string{"derived/py70/step1000.Q4_K_M.gguf", "derived/py70/step2000.Q4_K_M.gguf", "derived/py70/step4000.Q4_K_M.gguf", "derived/py70/step8000.Q4_K_M.gguf", "derived/py70/main.Q4_K_M.gguf"}},
	}
}

type policy struct {
	Name  string
	pol   modelz3.Policy
	bases func(anchor, prev map[string]*modelz3.BaseTensor) []map[string]*modelz3.BaseTensor
	cdc   bool
	// first: policy for revision 0 when it differs (nil = pol)
	first *modelz3.Policy
	// lockout: revision 0 is stored as CDC chunks only (today's production), later revisions delta against it
	lockout bool
	greedy  bool
}

func policies() []policy {
	one := func(a, p map[string]*modelz3.BaseTensor) []map[string]*modelz3.BaseTensor {
		return []map[string]*modelz3.BaseTensor{a}
	}
	prev := func(a, p map[string]*modelz3.BaseTensor) []map[string]*modelz3.BaseTensor {
		return []map[string]*modelz3.BaseTensor{p}
	}
	both := func(a, p map[string]*modelz3.BaseTensor) []map[string]*modelz3.BaseTensor {
		return []map[string]*modelz3.BaseTensor{a, p}
	}
	full := modelz3.Policy{Delta: true, Planes: true}
	return []policy{
		{Name: "cdc", cdc: true},
		{Name: "struct", pol: modelz3.Policy{}},
		{Name: "struct+planes", pol: modelz3.Policy{Planes: true}},
		{Name: "anchor(h<=2)", pol: withHops(full, 2), bases: one},
		{Name: "previous(h<=3)", pol: withHops(full, 3), bases: prev},
		{Name: "anchor|previous(h<=3)", pol: withHops(full, 3), bases: both},
		{Name: "cdc-first>anchor", pol: withHops(full, 2), bases: one, lockout: true},
		{Name: "greedy per revision", pol: withHops(full, 3), bases: both, greedy: true},
	}
}

func withHops(p modelz3.Policy, h int) modelz3.Policy { p.MaxHops = h; return p }

// rawBases names the tensors of a file that is NOT stored as components (CDC only): a descendant
// that deltas against them must carry the base component itself.
func rawBases(f *file) map[string]*modelz3.BaseTensor {
	m := map[string]*modelz3.BaseTensor{}
	for _, t := range f.lay.Tensors() {
		b := make([]byte, t.Size())
		check(readAtFull(f, b, t.Begin))
		m[t.Name] = &modelz3.BaseTensor{Name: t.Name, Type: t.Type, Shape: t.Shape, SHA: sha256.Sum256(b), Size: t.Size(), Src: f.src, Off: t.Begin, Width: t.Width, Raw: true}
	}
	return m
}

func layoutOf(f *file, format string) *modelz3.Layout {
	if f.lay != nil {
		return f.lay
	}
	var l *modelz3.Layout
	var err error
	if format == "gguf" {
		l, err = modelz3.GGUFLayout(f.f, f.Size)
	} else {
		l, _, err = modelz3.STLayout(f.f, f.Size)
	}
	check(err)
	f.lay = l
	return l
}

type revRes struct {
	Cost  modelz3.Cost
	Stats *modelz3.EncStats
	Exact bool
	Dec   time.Duration
}

func runDeep(s *store, filter string, prove bool) {
	pk := modelz3.NewPacker()
	for _, fam := range families() {
		if filter != "" && !strings.Contains(fam.Name, filter) {
			continue
		}
		fmt.Printf("\n### %s\n", fam.Name)
		var raw int64
		files := make([]*file, len(fam.Revs))
		for i, r := range fam.Revs {
			files[i] = s.get(r)
			layoutOf(files[i], fam.Format)
			raw += files[i].Size
		}
		fmt.Printf("%-24s | %-40s | %s\n", "policy (% of revision bytes, pack rule)", "per-revision incremental", "cumulative (unpacked) | retained last rev alone | exact, decode last rev")
		for _, p := range policies() {
			if (p.lockout && len(files) < 2) || (p.greedy && files[0].Size > 600<<20) {
				continue
			}
			store := repz3.NewMemStore()
			var anchor, prev map[string]*modelz3.BaseTensor
			var parts []string
			var cum modelz3.Cost
			exact := true
			var lastDec time.Duration
			var lastID repz3.SHA
			chosen := []string{}
			for i, f := range files {
				enc := func(pol modelz3.Policy, bases []map[string]*modelz3.BaseTensor) (*kinds.Bundle, map[string]*modelz3.BaseTensor) {
					b, bts, _, err := modelz3.EncodeModel(f.src, f.lay, bases, store.Has, pol)
					check(err)
					return b, bts
				}
				var b *kinds.Bundle
				var bts map[string]*modelz3.BaseTensor
				var err error
				name := p.Name
				switch {
				case p.cdc || (p.lockout && i == 0):
					b, err = kinds.CDC(readAll(f))
					check(err)
					if p.lockout {
						bts = rawBases(f)
					}
				case p.greedy:
					type cand struct {
						name string
						b    *kinds.Bundle
						bts  map[string]*modelz3.BaseTensor
						c    modelz3.Cost
					}
					var cs []cand
					add := func(n string, b *kinds.Bundle, bts map[string]*modelz3.BaseTensor) {
						cs = append(cs, cand{n, b, bts, modelz3.DryCost(store, b, pk)})
					}
					cb, err := kinds.CDC(readAll(f))
					check(err)
					add("cdc", cb, rawBases(f))
					b1, t1 := enc(modelz3.Policy{Planes: true}, nil)
					add("struct+planes", b1, t1)
					if i > 0 {
						b2, t2 := enc(withHops(modelz3.Policy{Delta: true, Planes: true}, 2), []map[string]*modelz3.BaseTensor{anchor})
						add("anchor", b2, t2)
						b3, t3 := enc(withHops(modelz3.Policy{Delta: true, Planes: true}, 3), []map[string]*modelz3.BaseTensor{prev})
						add("previous", b3, t3)
						b4, t4 := enc(withHops(modelz3.Policy{Delta: true, Planes: true}, 3), []map[string]*modelz3.BaseTensor{anchor, prev})
						add("hybrid", b4, t4)
					}
					best := 0
					for k := range cs {
						if cs[k].c.Total(true) < cs[best].c.Total(true) {
							best = k
						}
					}
					b, bts, name = cs[best].b, cs[best].bts, cs[best].name
					chosen = append(chosen, name)
				default:
					var bases []map[string]*modelz3.BaseTensor
					if p.bases != nil && i > 0 {
						bases = p.bases(anchor, prev)
					}
					b, bts = enc(p.pol, bases)
				}
				id, c, err := modelz3.Commit(store, b)
				check(err)
				cum.Raw += c.Raw
				cum.Packed += c.Packed
				cum.Recipe += c.Recipe
				cum.Objects += c.Objects
				parts = append(parts, fmt.Sprintf("%.1f", pct(c.Total(true), f.Size)))
				if prove || i == len(files)-1 {
					t0 := time.Now()
					ok := reconstructOK(store, id, f)
					lastDec = time.Since(t0)
					exact = exact && ok
				}
				lastID = id
				if i == 0 {
					anchor = bts
					if anchor == nil {
						anchor = rawBases(f)
					}
				}
				prev = bts
				if prev == nil {
					prev = rawBases(f)
				}
				_ = name
			}
			ret, err := modelz3.Retained(store, lastID, pk)
			if p.cdc {
				ret, err = modelz3.Retained(store, lastID, pk)
			}
			note := ""
			if p.greedy {
				note = " picks " + strings.Join(chosen, ",")
			}
			if err != nil {
				fmt.Printf("%-24s | retained: %v\n", p.Name, err)
				continue
			}
			fmt.Printf("%-24s | %-40s | %5.1f%% (%5.1f%%) | %5.1f%% | exact %v dec %.0f MiB/s%s\n", p.Name, strings.Join(parts, " / "), pct(cum.Total(true), raw), pct(cum.Raw+cum.Recipe, raw),
				pct(ret.Packed+ret.Recipe, files[len(files)-1].Size), exact, float64(files[len(files)-1].Size)/1048576/lastDec.Seconds(), note)
		}
	}
	printHWM()
}

func reconstructOK(st *repz3.MemStore, id repz3.SHA, f *file) bool {
	env := kinds.NewEnvLimits(st, repz3.Limits{MaxDepth: repz3.HardMaxDepth, MaxWhole: 1 << 30})
	h := sha256.New()
	n, sum, err := env.Reconstruct(id, h)
	_ = sum
	if err != nil {
		fmt.Fprintln(os.Stderr, "reconstruct:", err)
		return false
	}
	// compare with the original bytes in a streaming pass
	got := h.Sum(nil)
	want := sha256.New()
	_, err = io.Copy(want, io.NewSectionReader(f.f, 0, f.Size))
	check(err)
	return n == f.Size && bytes.Equal(got, want.Sum(nil))
}

func printHWM() {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM") {
			fmt.Println("peak RSS:", strings.TrimSpace(strings.TrimPrefix(l, "VmHWM:")))
		}
	}
}

// inflateMiBs measures how fast DEFLATE records of the given payloads inflate (the store's share of
// reading a packed residual), single core.
func inflateMiBs(objs [][]byte) float64 {
	var zs [][]byte
	var total int64
	for _, o := range objs {
		var buf bytes.Buffer
		w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
		w.Write(o)
		w.Close()
		zs = append(zs, buf.Bytes())
		total += int64(len(o))
	}
	t0 := time.Now()
	for _, z := range zs {
		io.Copy(io.Discard, flate.NewReader(bytes.NewReader(z)))
	}
	return float64(total) / 1048576 / time.Since(t0).Seconds()
}

var _ = lab.PackedLen
