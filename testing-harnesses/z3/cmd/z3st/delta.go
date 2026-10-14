package main

import (
	"flag"
	"fmt"
	"strings"

	"zeros3-z3/lab"
	"zeros3-z3/tdelta"
)

// fileCost is the physical cost of storing B given A under the "best simple
// exact tensor representation": identical same-name tensors are references,
// changed aligned tensors take the cheapest delta (or stand alone), everything
// else is stored whole (packed with the production rule).
func fileCost(r *pairResult) (cost int64, parts map[string]int64) {
	parts = map[string]int64{}
	b := r.b
	for i := range b.f.Tensors {
		t := &b.f.Tensors[i]
		ta, ok := r.a.f.Lookup(t.Name)
		if ok && ta.DType == t.DType && equalInts(ta.Shape, t.Shape) && r.a.hashes()[t.Name] == b.hashes()[t.Name] {
			parts["reference"] += refOverhead
			continue
		}
		if !ok || ta.DType != t.DType || !equalInts(ta.Shape, t.Shape) {
			v := lab.PackedLen(b.payload(t)) + refOverhead
			parts["stored whole"] += v
		}
	}
	for _, td := range r.changedList {
		parts["delta"] += int64(min(td.best, td.replace, td.n) + refOverhead)
	}
	parts["header"] = int64(8 + len(b.f.Header)) // exact header bytes (uncompressed upper bound)
	for _, v := range parts {
		cost += v
	}
	return
}

func deltaCmd(args []string) {
	fs := flag.NewFlagSet("delta", flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fams := fs.String("families", "", "family globs")
	vsFirst := fs.Bool("vs-first", false, "compare with r0")
	fs.Parse(args)
	cks, order := load(*dir, split(*fams))
	codecs := []tdelta.Codec{tdelta.Replace, tdelta.ReplacePlane, tdelta.XOR, tdelta.XORZ, tdelta.ByteDelta, tdelta.ZeroRLE, tdelta.Plane, tdelta.Sparse, tdelta.FloatSplit}
	fmt.Println("Cost of the changed aligned tensors only, % of their raw bytes (payload; +36 B reference per tensor in the `best` columns).")
	fmt.Println()
	hdr := "| family | base -> target | dtype | changed tensors | MiB |"
	sep := "|---|---|---|---:|---:|"
	for _, c := range codecs {
		hdr += " " + c.String() + " |"
		sep += "---:|"
	}
	hdr += " best per tensor (no base alone) | best incl. standalone |"
	sep += "---:|---:|"
	fmt.Println(hdr)
	fmt.Println(sep)
	type whole struct {
		name           string
		cost, cdc, raw int64
	}
	var wholes []whole
	for _, fam := range order {
		c := cks[fam]
		for j := 1; j < len(c); j++ {
			i := j - 1
			if *vsFirst {
				i = 0
			}
			r := analyzePair(c[i], c[j], pairOpts{delta: true})
			for _, dt := range sortedDT(r.stats) {
				if r.dTensors[dt] == 0 {
					continue
				}
				row := fmt.Sprintf("| %s | %s -> %s | %s | %d | %.1f |", strings.TrimPrefix(fam, "rw-"), c[i].label, c[j].label, dt, r.dTensors[dt], float64(r.dBytes[dt])/(1<<20))
				for _, cd := range codecs {
					if v, ok := r.dCost[dt][cd]; ok {
						row += " " + pct(v, r.dBytes[dt]) + " |"
					} else {
						row += " - |"
					}
				}
				row += " " + pct(r.dBestNoReplace[dt], r.dBytes[dt]) + " | " + pct(r.dBest[dt], r.dBytes[dt]) + " |"
				fmt.Println(row)
			}
			cost, _ := fileCost(r)
			wholes = append(wholes, whole{fmt.Sprintf("%s %s -> %s", strings.TrimPrefix(fam, "rw-"), c[i].label, c[j].label), cost, newChunkBytes(c[i], c[j]), int64(len(c[j].data))})
		}
	}
	fmt.Println("\nWhole-file incremental cost of the target given the base, % of the target file: raw ZeroS3 CDC (new chunk bytes) vs the best simple exact tensor representation (references + deltas + stored tensors + exact header).")
	fmt.Println()
	fmt.Println("| pair | CDC % | best simple tensor representation % |")
	fmt.Println("|---|---:|---:|")
	var sc, sw, sr int64
	for _, w := range wholes {
		fmt.Printf("| %s | %s | %s |\n", w.name, pct(w.cdc, w.raw), pct(w.cost, w.raw))
		sc += w.cdc
		sw += w.cost
		sr += w.raw
	}
	fmt.Printf("| **byte-weighted** | **%s** | **%s** |\n", pct(sc, sr), pct(sw, sr))
}

// newChunkBytes is the CDC cost of b given the chunks of a: chunk bytes of b not present in a.
func newChunkBytes(a, b *ckpt) int64 {
	have := chunkSet(a)
	var n int64
	forChunks(b, func(h [32]byte, size int) {
		if !have[h] {
			n += int64(size)
		}
	})
	return n
}
