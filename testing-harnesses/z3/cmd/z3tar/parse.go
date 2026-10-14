package main

import (
	"fmt"
	"path"
	"strings"

	"zeros3-z3/tarz3"
)

func pathMatch(p, s string) (bool, error) { return path.Match(p, s) }

func runParse(revs map[string][]*rev, order []string) {
	fmt.Println("| family | rev | tar MiB | members | payload members | prefixed (pax/gnu) | formats | types | header+struct % | payload % | pad+trailer % | status |")
	fmt.Println("|---|---|---:|---:|---:|---:|---|---|---:|---:|---:|---|")
	for _, f := range order {
		for _, r := range revs[f] {
			if r.perr != nil {
				fmt.Printf("| %s | %s | %.1f | - | - | - | - | - | - | - | - | UNSUPPORTED: %v |\n", short(f), r.label, float64(len(r.plain))/(1<<20), r.perr)
				continue
			}
			a := r.ar
			s, pays := tarz3.Split(r.plain, a)
			var pb int64
			pm, pre := 0, 0
			for _, p := range pays {
				pb += int64(len(p))
				pm++
			}
			for _, m := range a.Members {
				if m.Prefix > 0 {
					pre++
				}
			}
			var fm, ty []string
			for _, k := range sortedKeys(a.Formats) {
				fm = append(fm, fmt.Sprintf("%s:%d", k, a.Formats[k]))
			}
			for _, k := range sortedKeys(a.Types) {
				ty = append(ty, fmt.Sprintf("%c:%d", k, a.Types[k]))
			}
			n := int64(len(r.plain))
			fmt.Printf("| %s | %s | %.1f | %d | %d | %d | %s | %s | %s | %s | %s | ok |\n", short(f), r.label, float64(n)/(1<<20), len(a.Members), pm, pre,
				strings.Join(fm, " "), strings.Join(ty, " "), pct(int64(len(s))-a.TrailerLen(), n), pct(pb, n), pct(n-pb-(int64(len(s))-a.TrailerLen()), n))
		}
	}
}
