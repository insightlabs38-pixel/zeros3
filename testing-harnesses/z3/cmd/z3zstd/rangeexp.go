package main

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"

	"zeros3-z3/zstdz3"
)

// cmdRange measures what a ranged read of the compressed object costs when the object is
// served by replay from a checkpoint: plaintext fetched from the semantic child (block
// plaintext plus the model's window), unit bytes, skeleton bytes and blocks replayed,
// against reading the compressed prefix a native decoder would need.
func cmdRange(args []string) {
	fs := flag.NewFlagSet("range", flag.ExitOnError)
	in := fs.String("in", "", "zstd file")
	n := fs.Int("n", 24, "random positions per cell")
	fs.Parse(args)
	zst, err := os.ReadFile(*in)
	must(err)
	fmt.Printf("%s: %d bytes\n", *in, len(zst))
	fmt.Printf("%-9s %-6s %-5s %8s %9s | %7s %11s %11s %9s %9s | %12s\n", "sequences", "range", "step", "cp bytes", "cp/blocks", "blocks", "plain(blk)", "plain(win)", "units", "skeleton", "native prefix")
	for _, nomodel := range []bool{false, true} {
		for _, step := range []int{1, 8, 16} {
			var plain, units bytes.Buffer
			res, err := zstdz3.Parse(bytes.NewReader(zst), &plain, &units, zstdz3.Options{NoSeqModel: nomodel, CheckpointEvery: step})
			must(err)
			cps := res.Checkpoints
			var cpBytes int
			for i := range cps {
				cpBytes += len(cps[i].Marshal())
			}
			offs := make([]int64, len(cps))
			for i := range cps {
				offs[i] = cps[i].CompOff
			}
			pb, ub := plain.Bytes(), units.Bytes()
			for _, span := range []int64{4 << 10, 64 << 10, 1 << 20} {
				r := rand.New(rand.NewSource(int64(span)))
				var blocks, pblk, pwin, unitB, skel, nat float64
				for k := 0; k < *n; k++ {
					off := r.Int63n(int64(len(zst)) - span)
					i := sort.Search(len(offs), func(i int) bool { return offs[i] > off }) - 1
					cp := cps[i]
					var out bytes.Buffer
					st, err := zstdz3.ReplayFrom(&out, res.Skeleton, &cp, bytes.NewReader(ub[cp.UnitOff:]), bytes.NewReader(pb), off+span)
					must(err)
					if !bytes.Equal(out.Bytes(), zst[cp.CompOff:cp.CompOff+int64(out.Len())]) || cp.CompOff+int64(out.Len()) < off+span {
						must(fmt.Errorf("ranged replay at %d differs", off))
					}
					blocks += float64(st.Blocks)
					pblk += float64(st.PlainBytes)
					pwin += float64(st.WindowBytes)
					unitB += float64(st.UnitBytes)
					skel += float64(st.SkelBytes)
					nat += float64(off + span)
				}
				f := float64(*n)
				mode := "model"
				if nomodel {
					mode = "explicit"
				}
				fmt.Printf("%-9s %-6s %-5d %8d %9.1f | %7.1f %11s %11s %9s %9s | %12s\n", mode, hum(float64(span)), step, cpBytes, float64(cpBytes)/float64(len(cps)),
					blocks/f, hum(pblk/f), hum(pwin/f), hum(unitB/f), hum(skel/f), hum(nat/f))
			}
		}
	}
}
