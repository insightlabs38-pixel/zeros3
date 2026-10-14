package main

import (
	"flag"
	"fmt"
	"strings"

	"zeros3-z3/lab"
)

// modesCmd: Z3-08F. Compares laboratory operating points (ratio / balanced /
// fast) on a corpus: incremental cost, entry cost, encode throughput, allocation.
func modesCmd(args []string) {
	fs := flag.NewFlagSet("modes", flag.ExitOnError)
	dir, fams := commonFlags(fs)
	reps := fs.String("reps", "cdc,irp-tok16k,irp-tok16k-bal,irp-tok16k-fast", "representations (first non-cdc is the reference)")
	refB := fs.Int64("ref-bytes", 35, "recipe bytes per reference")
	jsonOut := fs.String("json", "", "save the lab report")
	fs.Parse(args)
	cfg := lab.DefaultRunConfig(*dir)
	cfg.Reps, cfg.RefBytes, cfg.RangeReads = split(*reps), *refB, 0
	cfg.Families = split(*fams)
	rep, err := lab.Run(cfg)
	if err != nil {
		fatal(err)
	}
	if *jsonOut != "" {
		if err := lab.WriteJSON(*jsonOut, rep); err != nil {
			fatal(err)
		}
	}
	agg, order := aggregate(rep, *refB, rep.RefBytes)
	fmt.Println("| family | representation | incr % | r0 entry % | units+skeleton % of stream (r0..rN) | analyze MiB/s plaintext | alloc MiB per plaintext MiB | exact |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---|")
	type tot struct {
		logical, packed, plain, meta int64
		ms, alloc                    float64
		n                            int
	}
	sum := map[string]*tot{}
	for _, fam := range order {
		for _, a := range agg[fam] {
			if a.rep == "cdc" || a.revs == 0 {
				continue
			}
			mibs, alloc := 0.0, 0.0
			if a.plainBytes > 0 && a.analyzeMs > 0 {
				mibs = float64(a.plainBytes) / (1 << 20) / (a.analyzeMs / 1000)
				alloc = float64(a.alloc) / (1 << 20) / (float64(a.plainBytes) / (1 << 20))
			}
			fmt.Printf("| %s | %s | %s | %.1f | %s | %.1f | %.1f | %d/%d |\n", strings.TrimPrefix(fam, "extreal-"), a.rep, pct(a.packed, a.logical),
				100*float64(a.base)/float64(max(a.baseLogical, 1)), pct(a.skel+a.unitNew, a.logical), mibs, alloc, a.exact, a.revs)
			t := sum[a.rep]
			if t == nil {
				t = &tot{}
				sum[a.rep] = t
			}
			t.logical += a.logical
			t.packed += a.packed
			t.plain += a.plainBytes
			t.meta += a.skel + a.unitNew
			t.ms += a.analyzeMs
			t.alloc += float64(a.alloc)
			t.n++
		}
	}
	fmt.Println("\nByte-weighted totals over the update revisions:")
	fmt.Println("| representation | incremental % | metadata % | analyze MiB/s | alloc MiB/MiB |")
	fmt.Println("|---|---:|---:|---:|---:|")
	for _, r := range split(*reps) {
		if t := sum[r]; t != nil {
			fmt.Printf("| %s | %s | %s | %.1f | %.1f |\n", r, pct(t.packed, t.logical), pct(t.meta, t.logical), float64(t.plain)/(1<<20)/(t.ms/1000), t.alloc/float64(t.plain))
		}
	}
}
