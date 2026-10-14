package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"zeros3-z3/tdelta"
)

var stepRe = regexp.MustCompile(`(\d+)$`)

// stepOf extracts the training step from a label like "step4000" (ok=false otherwise).
func stepOf(label string) (int, bool) {
	l := strings.TrimPrefix(label, "step")
	if m := stepRe.FindString(l); m != "" && strings.HasPrefix(label, "step") {
		n, _ := strconv.Atoi(m)
		return n, true
	}
	return 0, false
}

// spacingCmd (Z3-09F): for every ordered pair of checkpoints of a family, how
// much structure survives as a function of training-step distance.
func spacingCmd(args []string) {
	fs := flag.NewFlagSet("spacing", flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fams := fs.String("families", "rw-pythia14m-train", "family globs")
	maxPairs := fs.Int("max-pairs", 400, "stop after this many pairs")
	fs.Parse(args)
	cks, order := load(*dir, split(*fams))
	fmt.Println("| family | A -> B | step distance | same-name identical tensors (bytes %) | bit-identical elements % | equal bytes % | XOR entropy bits/byte | xor+z % | plane-xor+z % | sparse % | float-split+z % | best simple exact (per tensor, incl. standalone) % | CDC new bytes % | 4 KiB blocks changed % |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	type row struct {
		dist int
		line string
	}
	var rows []row
	for _, fam := range order {
		c := cks[fam]
		cdcSets := make([]map[[32]byte]bool, len(c))
		n := 0
		for i := 0; i < len(c); i++ {
			for j := i + 1; j < len(c); j++ {
				if n >= *maxPairs {
					break
				}
				n++
				fmt.Fprintf(os.Stderr, "pair %d: %s -> %s\n", n, c[i].label, c[j].label)
				r := analyzePair(c[i], c[j], pairOpts{delta: true})
				si, oki := stepOf(c[i].label)
				sj, okj := stepOf(c[j].label)
				dist := j - i
				if oki && okj {
					dist = sj - si
				}
				var tot tdelta.Stats
				var changedBytes, xz, pl, sp, fsz int64
				for dt, s := range r.stats {
					addStatsAll(&tot, s)
					changedBytes += r.dBytes[dt]
					xz += r.dCost[dt][tdelta.XORZ]
					pl += r.dCost[dt][tdelta.Plane]
					sp += r.dCost[dt][tdelta.Sparse]
					if v, ok := r.dCost[dt][tdelta.FloatSplit]; ok {
						fsz += v
					} else {
						fsz += r.dCost[dt][tdelta.Plane]
					}
				}
				var bestAll int64
				for dt := range r.stats {
					bestAll += r.dBest[dt]
				}
				if cdcSets[i] == nil {
					cdcSets[i] = chunkSet(c[i])
				}
				var cdcNew int64
				forChunks(c[j], func(h [32]byte, sz int) {
					if !cdcSets[i][h] {
						cdcNew += int64(sz)
					}
				})
				// totals are over aligned changed tensors; unaligned bytes are charged whole in the file column
				fileCostV, _ := fileCost(r)
				line := fmt.Sprintf("| %s | %s -> %s | %d | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |", strings.TrimPrefix(fam, "rw-"), c[i].label, c[j].label, dist,
					pct(r.sameNameIdenticalBytes, r.bytesB), pct2(tot.EqualElems, tot.Elems), pct2(tot.EqualBytes, tot.Bytes), fmt.Sprintf("%.2f", ratio(r.xorEntropyBits, r.xorBytes)),
					pct(xz, changedBytes), pct(pl, changedBytes), pct(sp, changedBytes), pct(fsz, changedBytes), pct(fileCostV, int64(len(c[j].data))), pct(cdcNew, int64(len(c[j].data))),
					pct(tot.BlockChanged4K, tot.Blocks4K))
				rows = append(rows, row{dist, line})
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].dist < rows[j].dist })
	for _, r := range rows {
		fmt.Println(r.line)
	}
}

func ratio(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

func addStatsAll(dst, s *tdelta.Stats) { addStats(dst, s) }
