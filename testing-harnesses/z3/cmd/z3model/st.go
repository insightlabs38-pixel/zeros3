package main

import (
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"zeros3-z3/modelz3"
	"zeros3-z3/st"
	"zeros3-z3/tdelta"
)

func stRels() []rel {
	r := []rel{
		{"base>fine-tune (Qwen2.5-0.5B BF16, 988 MB)", "Qwen2.5-0.5B base>Instruct", "st-qwen05/base", "st-qwen05/instruct"},
		{"star (SmolLM2-135M BF16)", "base>instruct", "st-smollm2-star/base", "st-smollm2-star/instruct"},
		{"star (SmolLM2-135M BF16)", "base>sft-only", "st-smollm2-star/base", "st-smollm2-star/sft-only"},
		{"star (SmolLM2-135M BF16)", "base>shakespeare", "st-smollm2-star/base", "st-smollm2-star/shakespeare"},
		{"star (SmolLM2-135M BF16)", "instruct>sft-only (sibling)", "st-smollm2-star/instruct", "st-smollm2-star/sft-only"},
		{"star (SmolLM2-135M BF16)", "sft-only>shakespeare (sibling)", "st-smollm2-star/sft-only", "st-smollm2-star/shakespeare"},
		{"unrelated", "SmolLM2-135M base (BF16) vs SmolLM v1 (F32)", "st-smollm2-star/base", "st-unrelated/smollm-v1"},
		{"unrelated", "Qwen2.5-0.5B base vs SmolLM2-135M base (BF16)", "st-qwen05/base", "st-smollm2-star/base"},
	}
	return r
}

func readAll(f *file) []byte {
	b := make([]byte, f.Size)
	_, err := io.ReadFull(io.NewSectionReader(f.f, 0, f.Size), b)
	check(err)
	return b
}

func runST(s *store, filter string, doPXR bool) {
	pk := modelz3.NewPacker()
	fmt.Printf("%-52s | %6s %6s %6s %6s | %6s %6s %6s | %6s %6s %s\n", "relationship (% of target bytes; B stored after A)", "cdc", "struct", "str+cd", "4k", "whole", "plane", "stand", "PXR", "PXR>P", "notes")
	last := ""
	for _, r := range stRels() {
		if filter != "" && !strings.Contains(r.Class+r.Label, filter) {
			continue
		}
		if r.Class != last {
			fmt.Println("##", r.Class)
			last = r.Class
		}
		a, b := s.get(r.A), s.get(r.B)
		var sf [2]*st.File
		for i, f := range []*file{a, b} {
			if f.lay == nil {
				l, sfile, err := modelz3.STLayout(f.f, f.Size)
				check(err)
				f.lay = l
				_ = sfile
			}
			sf[i], _ = st.Parse(f.f, f.Size, st.Options{AllowUnknownDType: true})
		}
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
		pxr, pxrNote := "", ""
		if doPXR && len(dr.Cand) > 0 {
			ab, bb := readAll(a), readAll(b)
			t0 := time.Now()
			cont, rep, err := tdelta.EncodeFile(bb, sf[1], ab, sf[0], tdelta.Options{NoRows: true})
			check(err)
			te := time.Since(t0)
			t0 = time.Now()
			out, err := tdelta.DecodeFile(cont, ab, sf[0])
			check(err)
			td := time.Since(t0)
			exact := len(out) == len(bb)
			for i := range out {
				if !exact || out[i] != bb[i] {
					exact = false
					break
				}
			}
			pxr = fmt.Sprintf("%6.1f", pct(int64(len(cont)), b.Size))
			pxrNote = fmt.Sprintf("PXR enc %.1f dec %.1f MiB/s exact %v modes %s", float64(b.Size)/1048576/te.Seconds(), float64(b.Size)/1048576/td.Seconds(), exact, modeMix(rep))
			runtime.GC()
		}
		fmt.Printf("%-52s | %6.1f %6.1f %6.1f %6.1f | %6.1f %6.1f %6.1f | %6s %6.1f %s\n", r.Label, cs[0], cs[1], cs[2], cs[3],
			pct(id.Whole, id.Tensors), pct(best, raw), pct(standalone, raw), pxr, pct(dl, raw), pxrNote)
	}
}

func modeMix(r *tdelta.FileRep) string {
	by := map[string]int{}
	tot := 0
	for _, t := range r.Tensors {
		by[t.Mode.String()] += t.Size
		tot += t.Size
	}
	var parts []string
	for k, v := range by {
		parts = append(parts, fmt.Sprintf("%s %.0f%%", k, pct(int64(v), int64(tot))))
	}
	return strings.Join(parts, " ")
}
