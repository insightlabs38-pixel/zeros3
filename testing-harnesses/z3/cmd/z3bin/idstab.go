package main

import (
	"bytes"
	"flag"
	"fmt"

	"zeros3-z3/binref"
)

// runIdStab asks how many functions keep byte-identical reference-masked
// contents across two builds, and shows where the others differ.
func runIdStab(args []string) {
	fs := flag.NewFlagSet("idstab", flag.ExitOnError)
	a := fs.String("base", "", "base")
	b := fs.String("cur", "", "cur")
	um := fs.String("units", "auto", "units")
	show := fs.Int("n", 8, "examples")
	fs.Parse(args)
	base, cur := mustLoad(*a), mustLoad(*b)
	c := cfg{setFull, binref.IdentSymbol, *um}
	nb, _, _ := normalize(base, c)
	nc, _, _ := normalize(cur, c)
	get := func(bn *bin, n *binref.Norm) map[string][]byte {
		m := map[string][]byte{}
		for _, u := range bn.Im.CleanUnits(bn.units(*um)) {
			off, ok := bn.Im.OffOfVA(u.VA)
			if !ok || u.Name == "" || off+int(u.Size) > len(n.Masked) {
				continue
			}
			m[u.Name] = n.Masked[off : off+int(u.Size)]
		}
		return m
	}
	bm, cm := get(base, nb), get(cur, nc)
	same, diff, missing := 0, 0, 0
	shown := 0
	for name, cb := range cm {
		bb, ok := bm[name]
		switch {
		case !ok:
			missing++
		case bytes.Equal(bb, cb):
			same++
		default:
			diff++
			if shown < *show {
				shown++
				k := 0
				for k < len(bb) && k < len(cb) && bb[k] == cb[k] {
					k++
				}
				fmt.Printf("  %s len %d/%d first diff @%d: % x | % x\n", name, len(bb), len(cb), k, bb[k:min(k+10, len(bb))], cb[k:min(k+10, len(cb))])
			}
		}
	}
	fmt.Printf("units by name: identical %d, differ %d, new/missing %d\n", same, diff, missing)
}
