package main

import (
	"flag"
	"fmt"
	"sort"
	"strings"
)

// classCmd (Z3-09J): stability by generic tensor-name class, summed over the
// selected checkpoint pairs.
func classCmd(args []string) {
	fs := flag.NewFlagSet("class", flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fams := fs.String("families", "", "family globs")
	vsFirst := fs.Bool("vs-first", false, "compare with r0")
	fs.Parse(args)
	cks, order := load(*dir, split(*fams))
	fmt.Println("| family | pairs | class | tensors | share of bytes % | identical tensors % | bit-identical elements % | best exact delta of the changed tensors, % of their raw bytes (incl. standalone, +36 B/tensor) |")
	fmt.Println("|---|---:|---|---:|---:|---:|---:|---:|")
	for _, fam := range order {
		c := cks[fam]
		agg := map[string]*classAgg{}
		var total int64
		pairs := 0
		for j := 1; j < len(c); j++ {
			i := j - 1
			if *vsFirst {
				i = 0
			}
			r := analyzePair(c[i], c[j], pairOpts{delta: true})
			pairs++
			total += r.bytesB
			for k, v := range r.class {
				a := agg[k]
				if a == nil {
					a = &classAgg{}
					agg[k] = a
				}
				a.tensors += v.tensors
				a.identical += v.identical
				a.bytes += v.bytes
				a.identicalBytes += v.identicalBytes
				a.elems += v.elems
				a.equalElems += v.equalElems
				a.deltaBytes += v.deltaBytes
				a.targetChangedBytes += v.targetChangedBytes
			}
		}
		var names []string
		for k := range agg {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			a := agg[k]
			fmt.Printf("| %s | %d | %s | %d | %s | %s | %s | %s |\n", strings.TrimPrefix(fam, "rw-"), pairs, k, a.tensors/int64(pairs), pct(a.bytes, total), pct(a.identical, a.tensors),
				pct2(a.equalElems, a.elems), pct(a.deltaBytes, a.targetChangedBytes))
		}
	}
}
