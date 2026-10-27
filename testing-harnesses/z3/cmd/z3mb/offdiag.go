package main

import (
	"fmt"

	"zeros3-z3/dexz3"
)

func init() {
	cmds["offdiag"] = func(args []string) error {
		seqs := make([][][]int, 2)
		for k := 0; k < 2; k++ {
			f, b, err := loadDex(args[k])
			if err != nil {
				return err
			}
			w, err := dexz3.Walk(f, b, dexz3.WalkOpts{Offsets: true, Debug: true})
			if err != nil {
				return err
			}
			type key struct {
				t   uint16
				off int64
			}
			ord := map[key]int{}
			cnt := map[uint16]int{}
			for _, it := range w.Items {
				ord[key{it.Type, it.Off}] = cnt[it.Type]
				cnt[it.Type]++
			}
			for i, o := range w.StrOff {
				ord[key{dexz3.TypeStringData, int64(o)}] = i
			}
			seqs[k] = make([][]int, 16)
			prev := make([]int, 16)
			for i := range prev {
				prev[i] = -1
			}
			for _, s := range w.Slots {
				if s.Space != dexz3.SpOffset {
					continue
				}
				o := ord[key{dexz3.OffTarget[s.Kind], int64(s.Val)}]
				seqs[k][s.Kind] = append(seqs[k][s.Kind], o-prev[s.Kind])
				prev[s.Kind] = o
			}
		}
		for kd := 1; kd < 15; kd++ {
			a, b := seqs[0][kd], seqs[1][kd]
			n := len(a)
			if len(b) < n {
				n = len(b)
			}
			diff := 0
			first := -1
			for i := 0; i < n; i++ {
				if a[i] != b[i] {
					diff++
					if first < 0 {
						first = i
					}
				}
			}
			// suffix-aligned diff
			sd := 0
			for i := 1; i <= n; i++ {
				if a[len(a)-i] != b[len(b)-i] {
					sd++
				}
			}
			fmt.Printf("kind %2d len %6d/%6d prefix-aligned diffs %6d (first %d) suffix-aligned diffs %6d\n", kd, len(a), len(b), diff, first, sd)
		}
		return nil
	}
}
