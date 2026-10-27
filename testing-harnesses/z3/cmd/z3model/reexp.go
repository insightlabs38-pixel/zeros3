package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"zeros3-z3/modelz3"
)

func autoLayout(f *file) *modelz3.Layout {
	if f.lay != nil {
		return f.lay
	}
	var l *modelz3.Layout
	var err error
	switch {
	case strings.HasSuffix(f.Name, ".gguf") || strings.Contains(f.Name, "gguf"):
		l, err = modelz3.GGUFLayout(f.f, f.Size)
	case strings.HasSuffix(f.Name, ".bin") || strings.Contains(f.Name, "pt-"):
		l, _, err = modelz3.PTLayout(f.f, f.Size, nil)
	default:
		l, _, err = modelz3.STLayout(f.f, f.Size)
	}
	check(err)
	f.lay = l
	return l
}

// runReexport measures layout-only re-exports (real payload bytes, changed framing / order / shard
// assignment / producer) against the original stored first.
func runReexport(s *store) {
	type job struct {
		class, label, a string
		bs              []string
	}
	dd := "derived/reexport/"
	jobs := []job{
		{"GGUF", "header grows (extra KV): tensors shift", "gguf-smollm2/qf-inst-Q4_K_M", []string{dd + "smollm2-inst-Q4_K_M.addkv.gguf"}},
		{"GGUF", "tensors in reverse order", "gguf-smollm2/qf-inst-Q4_K_M", []string{dd + "smollm2-inst-Q4_K_M.reverse.gguf"}},
		{"GGUF", "alignment 32 -> 64", "gguf-smollm2/qf-inst-Q4_K_M", []string{dd + "smollm2-inst-Q4_K_M.align64.gguf"}},
		{"Safetensors", "tensors in reverse order", "st-smollm2-star/instruct", []string{dd + "smollm2-inst.reverse.safetensors"}},
		{"Safetensors", "1 file -> 3 shards", "st-smollm2-star/instruct", []string{dd + "smollm2-inst.shards3/model-00001-of-00003.safetensors", dd + "smollm2-inst.shards3/model-00002-of-00003.safetensors", dd + "smollm2-inst.shards3/model-00003-of-00003.safetensors"}},
		{"Safetensors", "1 file -> 5 shards", "st-smollm2-star/instruct", []string{dd + "smollm2-inst.shards5/model-00001-of-00005.safetensors", dd + "smollm2-inst.shards5/model-00002-of-00005.safetensors", dd + "smollm2-inst.shards5/model-00003-of-00005.safetensors", dd + "smollm2-inst.shards5/model-00004-of-00005.safetensors", dd + "smollm2-inst.shards5/model-00005-of-00005.safetensors"}},
		{"Safetensors", "one real tensor swapped (embedding from sft-only)", "st-smollm2-star/instruct", []string{dd + "smollm2-inst.onetensor.safetensors"}},
		{"PyTorch ZIP", "re-saved by current torch (other producer version)", "pt-pythia70m/main", []string{dd + "pythia70m-main/resave.bin"}},
		{"PyTorch ZIP", "re-saved, key order reversed", "pt-pythia70m/main", []string{dd + "pythia70m-main/reversed.bin"}},
	}
	pk := modelz3.NewPacker()
	fmt.Printf("%-12s %-52s | %6s %6s %6s %6s | %s\n", "format", "re-export of the same real tensors (cost % of the re-exported bytes)", "cdc", "struct", "str+cd", "hdr+cdc", "framing % of B")
	for _, j := range jobs {
		a := s.get(j.a)
		autoLayout(a)
		var cost [4]int64
		var total, framing int64
		for m, mode := range []string{modelz3.CutCDC, modelz3.CutStruct, modelz3.CutStructCDC, modelz3.CutHdrCDC} {
			seen := modelz3.Seen{}
			seen.Add(a.cut(mode))
			for _, bn := range j.bs {
				b := s.get(bn)
				autoLayout(b)
				c, err := modelz3.CostOf(b.cut(mode), seen, pk)
				check(err)
				seen.Add(b.cut(mode))
				cost[m] += c.Total(true)
				if m == 0 {
					total += b.Size
					framing += b.lay.FramingBytes()
				}
			}
		}
		fmt.Printf("%-12s %-52s | %6.2f %6.2f %6.2f %6.2f | %.3f\n", j.class, j.label, pct(cost[0], total), pct(cost[1], total), pct(cost[2], total), pct(cost[3], total), pct(framing, total))
	}
	_ = filepath.Join
}
