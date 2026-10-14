package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"

	"zeros3-z3/binref"
)

// runRuns lists the literal runs of masked-cur against masked-base inside one
// region: what is left after reference normalization, and where.
func runRuns(args []string) {
	fs := flag.NewFlagSet("runs", flag.ExitOnError)
	a := fs.String("base", "", "base")
	b := fs.String("cur", "", "cur")
	reg := fs.String("region", ".text", "region name")
	um := fs.String("units", "auto", "units")
	maxN := fs.Int("n", 25, "runs to print")
	raw := fs.Bool("raw", false, "no normalization (raw bytes)")
	fs.Parse(args)
	base, cur := mustLoad(*a), mustLoad(*b)
	c := cfg{setFull, binref.IdentSymbol, *um}
	nb, _, _ := normalize(base, c)
	nc, _, _ := normalize(cur, c)
	pay := func(bn *bin, n *binref.Norm) []byte {
		for i, r := range bn.Im.Regions {
			if r.Name == *reg {
				if *raw {
					return bn.Data[r.Off : r.Off+r.Len]
				}
				return n.Masked[r.Off : r.Off+r.Len]
			}
			_ = i
		}
		return nil
	}
	bp, cp := pay(base, nb), pay(cur, nc)
	d := deltaRuns(bp, cp, true)
	fmt.Printf("%s: %d bytes, literal %d in %d runs\n", *reg, len(cp), d.Literal, len(d.Runs))
	var hist = map[int]int{}
	for _, r := range d.Runs {
		n := r[1] - r[0]
		k := 1
		for k < n {
			k *= 2
		}
		hist[k]++
	}
	var ks []int
	for k := range hist {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	var parts []string
	for _, k := range ks {
		parts = append(parts, fmt.Sprintf("<=%d:%d", k, hist[k]))
	}
	fmt.Println("run-length histogram:", strings.Join(parts, " "))
	for i, r := range d.Runs {
		if i >= *maxN {
			break
		}
		lo, hi := r[0], r[1]
		show := min(hi-lo, 24)
		ctx := cp[max(lo-6, 0):lo]
		fmt.Printf("  @%-8d len %-5d ctx % x | % x\n", lo, hi-lo, ctx, cp[lo:lo+show])
	}
}
