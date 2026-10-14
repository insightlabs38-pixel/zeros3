package main

import (
	"flag"
	"fmt"
	"strings"
)

func identityCmd(args []string) {
	fs := flag.NewFlagSet("identity", flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fams := fs.String("families", "", "family globs")
	vsFirst := fs.Bool("vs-first", false, "compare every revision with r0 instead of its predecessor")
	fs.Parse(args)
	cks, order := load(*dir, split(*fams))
	fmt.Println("| family | base -> target | tensors A->B | same-name identical (n, bytes %) | same-name changed (n, bytes %) | renamed but byte-identical (n) | added (n, bytes %) | removed (n) | dtype/shape changed (n) | rename candidates (same dtype+shape) | header identical | JSON order same | data order same | offsets moved |")
	fmt.Println("|---|---|---|---|---|---:|---|---:|---:|---:|---|---|---|---:|")
	for _, fam := range order {
		c := cks[fam]
		for j := 1; j < len(c); j++ {
			i := j - 1
			if *vsFirst {
				i = 0
			}
			r := analyzePair(c[i], c[j], pairOpts{})
			fmt.Printf("| %s | %s -> %s | %d->%d | %d, %s | %d, %s | %d | %d, %s | %d | %d | %d | %v | %v | %v | %d |\n", strings.TrimPrefix(fam, "rw-"), c[i].label, c[j].label,
				r.tensorsA, r.tensorsB, r.sameNameIdentical, pct(r.sameNameIdenticalBytes, r.bytesB), r.alignedChanged, pct(r.alignedChangedBytes, r.bytesB),
				r.renamedExact, r.added, pct(r.addedBytes, r.bytesB), r.removed, r.typeShapeChanged, r.renameCandidates, r.headerIdentical, r.jsonOrderSame, r.dataOrderSame, r.offsetsDiffer)
		}
	}
}
