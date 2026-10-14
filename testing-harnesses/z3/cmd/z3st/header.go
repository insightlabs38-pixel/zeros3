package main

import (
	"bytes"
	"compress/flate"
	"flag"
	"fmt"
	"strings"
)

func zlen(b []byte) int {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	w.Write(b)
	w.Close()
	return buf.Len()
}

// headerCmd (Z3-09K): how much the exact header varies between checkpoints.
func headerCmd(args []string) {
	fs := flag.NewFlagSet("header", flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fams := fs.String("families", "", "family globs")
	fs.Parse(args)
	cks, order := load(*dir, split(*fams))
	fmt.Println("| family | file | header bytes | % of file | header DEFLATEd | identical to previous | same JSON order | same data order | tensors with moved offsets | header delta vs previous (DEFLATE of XOR when same length, else of the header) | __metadata__ keys changed |")
	fmt.Println("|---|---|---:|---:|---:|---|---|---|---:|---:|---|")
	for _, fam := range order {
		c := cks[fam]
		for j := range c {
			f := c[j].f
			ident, jo, do, moved, delta, mk := "-", "-", "-", "-", "-", "-"
			if j > 0 {
				r := analyzePair(c[j-1], c[j], pairOpts{})
				ident, jo, do = fmt.Sprint(r.headerIdentical), fmt.Sprint(r.jsonOrderSame), fmt.Sprint(r.dataOrderSame)
				moved = fmt.Sprint(r.offsetsDiffer)
				pa, pb := c[j-1].f.Header, f.Header
				if len(pa) == len(pb) {
					x := make([]byte, len(pb))
					for k := range x {
						x[k] = pa[k] ^ pb[k]
					}
					delta = fmt.Sprint(zlen(x))
				} else {
					delta = fmt.Sprint(zlen(pb))
				}
				var diff []string
				for _, k := range f.MetaKeys {
					if c[j-1].f.Metadata[k] != f.Metadata[k] {
						diff = append(diff, k)
					}
				}
				mk = strings.Join(diff, ",")
				if mk == "" {
					mk = "none"
				}
			}
			fmt.Printf("| %s | %s | %d | %.3f | %d | %s | %s | %s | %s | %s | %s |\n", strings.TrimPrefix(fam, "rw-"), c[j].label, f.HeaderLen, 100*float64(8+f.HeaderLen)/float64(len(c[j].data)),
				zlen(f.Header), ident, jo, do, moved, delta, mk)
		}
	}
}
