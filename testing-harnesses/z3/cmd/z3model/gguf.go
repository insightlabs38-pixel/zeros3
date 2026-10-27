package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"zeros3-z3/modelz3"
)

type rel struct{ Class, Label, A, B string }

func pct(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

// ggufRels is the relationship list of the GGUF study: the target B is stored after the base A.
func ggufRels() []rel {
	var r []rel
	for _, q := range []string{"Q4_K_M", "Q5_K_M", "Q6_K", "Q8_0"} {
		r = append(r, rel{"same-quant fine-tune", "SmolLM2-135M base>instruct " + q + " (QuantFactory)", "gguf-smollm2/qf-base-" + q, "gguf-smollm2/qf-inst-" + q})
	}
	r = append(r,
		rel{"same-quant fine-tune", "Qwen2.5-0.5B base>instruct Q4_K_M (QuantFactory)", "gguf-qwen05/qf-base-Q4_K_M", "gguf-qwen05/qf-inst-Q4_K_M"},
		rel{"same-quant fine-tune", "Qwen3-0.6B base>post Q4_K_M (mradermacher)", "gguf-qwen3/mr-base-Q4_K_M", "gguf-qwen3/mr-post-Q4_K_M"},
		rel{"cross-quant", "SmolLM2 instruct Q4_K_M>Q5_K_M", "gguf-smollm2/qf-inst-Q4_K_M", "gguf-smollm2/qf-inst-Q5_K_M"},
		rel{"cross-quant", "SmolLM2 instruct Q5_K_M>Q6_K", "gguf-smollm2/qf-inst-Q5_K_M", "gguf-smollm2/qf-inst-Q6_K"},
		rel{"cross-quant", "SmolLM2 instruct Q6_K>Q8_0", "gguf-smollm2/qf-inst-Q6_K", "gguf-smollm2/qf-inst-Q8_0"},
		rel{"cross-quant", "SmolLM2 base Q4_K_M > instruct Q8_0", "gguf-smollm2/qf-base-Q4_K_M", "gguf-smollm2/qf-inst-Q8_0"},
		rel{"same-quant, other producer", "SmolLM2 instruct Q4_K_M QuantFactory>bartowski(imatrix)", "gguf-smollm2/qf-inst-Q4_K_M", "gguf-smollm2-bartowski/bw-inst-Q4_K_M"},
		rel{"same-quant, other producer", "SmolLM2 instruct Q8_0 QuantFactory>bartowski", "gguf-smollm2/qf-inst-Q8_0", "gguf-smollm2-bartowski/bw-inst-Q8_0"},
		rel{"same-quant, other producer", "Qwen2.5-0.5B-Instruct Q4_K_M QuantFactory>official Qwen", "gguf-qwen05/qf-inst-Q4_K_M", "gguf-qwen05/qwen-inst-q4_k_m"},
		rel{"metadata/producer re-export", "Qwen2.5-0.5B-Instruct Q4_K_M QuantFactory>DhruvalLabs", "gguf-qwen05/qf-inst-Q4_K_M", "gguf-qwen05/dh-inst-Q4_K_M"},
		rel{"unrelated, same arch+quant", "SmolLM2 instruct vs SmolLM v1 Q4_K_M", "gguf-smollm2/qf-inst-Q4_K_M", "gguf-smollm-v1/qf-v1-Q4_K_M"},
		rel{"unrelated, same arch+quant", "SmolLM2 instruct vs SmolLM v1 Q8_0", "gguf-smollm2/qf-inst-Q8_0", "gguf-smollm-v1/qf-v1-Q8_0"},
	)
	for _, q := range []string{"Q4_K_M", "Q8_0", "f16"} {
		for _, p := range [][2]string{{"step1000", "step2000"}, {"step2000", "step4000"}, {"step4000", "step8000"}, {"step8000", "main"}, {"step1000", "main"}} {
			r = append(r, rel{"REEXPORT checkpoints " + q, fmt.Sprintf("Pythia-70M %s>%s %s", p[0], p[1], q), "derived/py70/" + p[0] + "." + q + ".gguf", "derived/py70/" + p[1] + "." + q + ".gguf"})
		}
	}
	r = append(r, rel{"REEXPORT cross-quant", "Pythia-70M step8000 f16>Q4_K_M", "derived/py70/step8000.f16.gguf", "derived/py70/step8000.Q4_K_M.gguf"},
		rel{"REEXPORT cross-quant", "Pythia-70M step8000 Q8_0>Q4_K_M", "derived/py70/step8000.Q8_0.gguf", "derived/py70/step8000.Q4_K_M.gguf"})
	return r
}

func runGGUF(s *store, filter string) {
	pk := modelz3.NewPacker()
	fmt.Printf("%-62s | %6s %6s %6s %6s | %5s %5s %5s %5s %5s | %6s %6s %6s | %s\n", "relationship (cost % of target, packed; B stored after A)",
		"cdc", "struct", "str+cd", "4k", "whole", "natAl", "nat*", "4kAl", "4k*", "alDlt", "stand", "best", "delta modes (% tensor bytes)")
	last := ""
	for _, r := range ggufRels() {
		if filter != "" && !strings.Contains(r.Class+r.Label, filter) {
			continue
		}
		if r.Class != last {
			fmt.Println("##", r.Class)
			last = r.Class
		}
		a, b := s.get(r.A), s.get(r.B)
		for _, f := range []*file{a, b} {
			if f.lay == nil {
				l, err := modelz3.GGUFLayout(f.f, f.Size)
				check(err)
				f.lay = l
			}
		}
		t0 := time.Now()
		var cs [4]float64
		for i, m := range []string{modelz3.CutCDC, modelz3.CutStruct, modelz3.CutStructCDC, modelz3.CutFixed4K} {
			seen := modelz3.Seen{}
			seen.Add(a.cut(m))
			c, err := modelz3.CostOf(b.cut(m), seen, pk)
			check(err)
			cs[i] = pct(c.Total(true), b.Size)
		}
		id, err := modelz3.IdentityOf(a.src, a.lay, b.src, b.lay)
		check(err)
		dr, err := modelz3.DeltaOf(a.src, a.lay, b.src, b.lay)
		check(err)
		raw, dl, standalone, best := dr.Raw, dr.Delta, dr.Standalone, dr.Best
		var modes []string
		for _, m := range []string{"raw", "standalone", "standalone-planes", "xor", "xor-planes"} {
			if v := dr.Modes[m]; v > 0 {
				modes = append(modes, fmt.Sprintf("%s %.0f", m, pct(v, raw)))
			}
		}
		fmt.Printf("%-62s | %6.1f %6.1f %6.1f %6.1f | %5.1f %5.1f %5.1f %5.1f %5.1f | %6.1f %6.1f %6.1f | %s  (%.1fs)\n", r.Label, cs[0], cs[1], cs[2], cs[3],
			pct(id.Whole, id.Tensors), pct(id.AlignedNative, id.MatchedNative), pct(id.AnyNative, id.Tensors), pct(id.Aligned4K, id.Matched), pct(id.Any4K, id.Tensors),
			pct(dl, raw), pct(standalone, raw), pct(best, raw), strings.Join(modes, ", "), time.Since(t0).Seconds())
	}
	_ = sort.Strings
	_ = os.Stdout
}
