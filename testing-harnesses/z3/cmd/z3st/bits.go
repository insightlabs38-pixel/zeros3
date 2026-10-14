package main

import (
	"flag"
	"fmt"
	"strings"

	"zeros3-z3/tdelta"
)

func bitsCmd(args []string) {
	fs := flag.NewFlagSet("bits", flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fams := fs.String("families", "", "family globs")
	vsFirst := fs.Bool("vs-first", false, "compare with r0")
	fs.Parse(args)
	cks, order := load(*dir, split(*fams))
	fmt.Println("| family | base -> target | dtype | elements | bit-identical elems % | equal bytes % | zero XOR words % | mean flipped bits per changed elem | changed elems with sign / exponent / mantissa change % | exp bits flipped / mant bits flipped (per changed elem) | median log2(1+ULP distance) | P(next elem changed given changed) vs overall | 4 KiB blocks changed % | XOR order-0 entropy bits/byte (changed tensors) |")
	fmt.Println("|---|---|---|---:|---:|---:|---:|---:|---|---|---:|---|---:|---:|")
	for _, fam := range order {
		c := cks[fam]
		for j := 1; j < len(c); j++ {
			i := j - 1
			if *vsFirst {
				i = 0
			}
			r := analyzePair(c[i], c[j], pairOpts{})
			for _, dt := range sortedDT(r.stats) {
				s := r.stats[dt]
				if s.Elems == 0 {
					continue
				}
				ch := s.ChangedElems
				fl := "-"
				fb := "-"
				if dt == "F32" || dt == "F16" || dt == "BF16" {
					fl = fmt.Sprintf("%s / %s / %s", pct(s.SignChanged, ch), pct(s.ExpChanged, ch), pct(s.MantChanged, ch))
					fb = fmt.Sprintf("%.2f / %.2f", per(s.ExpBitsFlipped, ch), per(s.MantBitsFlipped, ch))
				}
				adj := "-"
				if ch > 0 {
					adj = fmt.Sprintf("%.2f vs %.2f", float64(s.AdjChangedPairs)/float64(ch), float64(ch)/float64(s.Elems))
				}
				xe := "-"
				if r.xorBytes > 0 {
					xe = fmt.Sprintf("%.2f", r.xorEntropyBits/r.xorBytes)
				}
				fmt.Printf("| %s | %s -> %s | %s | %d | %s | %s | %s | %.2f | %s | %s | %d | %s | %s | %s |\n", strings.TrimPrefix(fam, "rw-"), c[i].label, c[j].label, dt, s.Elems,
					pct2(s.EqualElems, s.Elems), pct2(s.EqualBytes, s.Bytes), pct2(s.EqualElems, s.Elems), per(s.XorPopcount, ch), fl, fb, medianLog2(s.DistLog2), adj, pct(s.BlockChanged4K, s.Blocks4K), xe)
			}
		}
	}
}

func per(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func sortedDT(m map[string]*tdelta.Stats) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
