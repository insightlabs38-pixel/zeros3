package main

import (
	"fmt"
	"sort"

	"zeros3-z3/mdelta2"
)

// cmdInv prints tensor-type byte inventory of model files.
func cmdInv(args []string) {
	for _, p := range args {
		m, err := mdelta2.Open(p)
		must(err)
		by := map[string][2]int64{}
		for _, t := range m.Tensors {
			k := t.DType.String()
			if t.DType == mdelta2.DQOther || t.DType == mdelta2.DRaw {
				k = fmt.Sprintf("%s/ggml%d", k, t.GGMLType)
			}
			v := by[k]
			v[0]++
			v[1] += t.Size()
			by[k] = v
		}
		ks := []string{}
		for k := range by {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		fmt.Printf("%s (%d bytes):", p, m.Size)
		for _, k := range ks {
			fmt.Printf(" %s n=%d %.1fMB;", k, by[k][0], float64(by[k][1])/1e6)
		}
		fmt.Println()
		m.Close()
	}
}
