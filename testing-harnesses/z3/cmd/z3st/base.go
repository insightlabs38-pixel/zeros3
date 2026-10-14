package main

import (
	"flag"
	"fmt"
	"strings"

	"zeros3-z3/tdelta"
)

// simpleCost is the cheapest of xor+z, plane xor+z and sparse for one aligned pair.
func simpleCost(dtype string, a, b []byte, w int) int {
	best := len(b)
	for _, c := range []tdelta.Codec{tdelta.XORZ, tdelta.Plane, tdelta.Sparse} {
		if p, err := tdelta.EncodeFor(c, dtype, a, b, w); err == nil && len(p) < best {
			best = len(p)
		}
	}
	return best
}

// baseCmd (Z3-09M): which candidate base would have produced the cheapest simple
// delta for each target tensor?
func baseCmd(args []string) {
	fs := flag.NewFlagSet("base", flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fams := fs.String("families", "", "family globs")
	targets := fs.Int("targets", 0, "only the last N checkpoints of a family are targets (0 = all after r0)")
	fs.Parse(args)
	cks, order := load(*dir, split(*fams))
	fmt.Println("Per changed target tensor: simple exact delta cost (min of xor+z, plane xor+z, sparse; payload bytes, standalone = raw), summed over target tensors, % of the target tensors' raw bytes.")
	fmt.Println()
	fmt.Println("| family | targets | target tensors | raw MiB | adjacent previous (same name) | oldest = r0 (same name) | best earlier checkpoint (same name, oracle) | best same-name or same-shape-other-name in any earlier (oracle) | share where adjacent is optimal % | share where another same-name checkpoint wins % | share where a different-name tensor wins % |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, fam := range order {
		c := cks[fam]
		lo := 1
		if *targets > 0 {
			lo = max(1, len(c)-*targets)
		}
		var raw, adj, oldest, bestSame, bestAny int64
		var nT, adjBest, otherSame, otherName int
		for j := lo; j < len(c); j++ {
			b := c[j]
			type tj struct{ cand [][]int64 }
			res := make([][5]int64, len(b.f.Tensors))
			ok := make([]bool, len(b.f.Tensors))
			parallel(len(b.f.Tensors), func(k int) {
				tb := &b.f.Tensors[k]
				pb := b.payload(tb)
				w := tb.Bits / 8
				if tb.Bits%8 != 0 || w == 0 {
					w = 1
				}
				adjC, oldC, bsame, bany := int64(len(pb)), int64(len(pb)), int64(len(pb)), int64(len(pb))
				changedVsAdj := false
				for i := 0; i < j; i++ {
					a := c[i]
					ta, found := a.f.Lookup(tb.Name)
					if found && ta.DType == tb.DType && equalInts(ta.Shape, tb.Shape) {
						cost := int64(simpleCost(tb.DType, a.payload(ta), pb, w))
						if i == j-1 {
							adjC = cost
							changedVsAdj = a.hashes()[tb.Name] != b.hashes()[tb.Name]
						}
						if i == 0 {
							oldC = cost
						}
						bsame = min(bsame, cost)
						bany = min(bany, cost)
					}
					// other tensors of the same dtype and size in this earlier checkpoint
					for q := range a.f.Tensors {
						tq := &a.f.Tensors[q]
						if tq.Name != tb.Name && tq.DType == tb.DType && equalInts(tq.Shape, tb.Shape) {
							bany = min(bany, int64(simpleCost(tb.DType, a.payload(tq), pb, w)))
						}
					}
				}
				if changedVsAdj {
					res[k] = [5]int64{int64(len(pb)), adjC, oldC, bsame, bany}
					ok[k] = true
				}
			})
			for k := range res {
				if !ok[k] {
					continue
				}
				nT++
				raw += res[k][0]
				adj += res[k][1]
				oldest += res[k][2]
				bestSame += res[k][3]
				bestAny += res[k][4]
				switch {
				case res[k][1] <= res[k][4]:
					adjBest++
				case res[k][3] <= res[k][4]:
					otherSame++
				default:
					otherName++
				}
			}
		}
		fmt.Printf("| %s | %d | %d | %.1f | %s | %s | %s | %s | %s | %s | %s |\n", strings.TrimPrefix(fam, "rw-"), len(c)-lo, nT, float64(raw)/(1<<20), pct(adj, raw), pct(oldest, raw), pct(bestSame, raw), pct(bestAny, raw),
			pct(int64(adjBest), int64(nT)), pct(int64(otherSame), int64(nT)), pct(int64(otherName), int64(nT)))
	}
}
