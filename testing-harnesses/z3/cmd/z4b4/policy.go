package main

import (
	"fmt"
	"os"
	"strings"

	"zeros3-z3/graphnorm"
)

// evalPolicy returns the cumulative hybrid (templates + B1 deltas) cost of a history.
func evalPolicy(data [][]byte, pol graphnorm.Policy) int64 {
	a := graphnorm.NewArchive(2)
	for _, d := range data {
		e, err := graphnorm.EncodeLLVM(d, pol)
		must(err)
		archiveCost(a, e, false)
	}
	return a.Total
}

func polName(p graphnorm.Policy) string {
	var s []string
	for i, n := range []string{"types", "values", "attrs", "md", "localabs", "localmd"} {
		if p.Bits()>>i&1 != 0 {
			s = append(s, n)
		}
	}
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, "+")
}

// cmdPolicy: greedy policy synthesis on the first `train` files, evaluated on all files.
func cmdPolicy(train int, files []string) {
	var data [][]byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		must(err)
		data = append(data, b)
	}
	cur := graphnorm.PolicyAll
	best := evalPolicy(data[:train], cur)
	fmt.Printf("train start %-40s %d\n", polName(cur), best)
	for pass := 0; pass < 2; pass++ {
		improved := false
		for bit := 0; bit < 6; bit++ {
			cand := graphnorm.PolicyFromBits(cur.Bits() ^ (1 << bit))
			c := evalPolicy(data[:train], cand)
			fmt.Printf("  try toggle %d -> %-36s %d\n", bit, polName(cand), c)
			if c < best {
				best, cur, improved = c, cand, true
			}
		}
		if !improved {
			break
		}
	}
	fmt.Printf("SELECTED %s (train cost %d)\n", polName(cur), best)
	fmt.Println("held-out comparison on the full history (policy was not tuned on later revisions):")
	for _, p := range []graphnorm.Policy{cur, graphnorm.PolicyAll, graphnorm.PolicyNone} {
		fmt.Printf("  %-40s full=%d\n", polName(p), evalPolicy(data, p))
	}
}
