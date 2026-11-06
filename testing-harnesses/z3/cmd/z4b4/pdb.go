package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"zeros3-z3/graphnorm"
	"zeros3-z3/semcas"
	"zeros3-z3/simdelta"
)

func pdbArchiveCost(a *graphnorm.Archive, e *graphnorm.PDBEncoding) int64 {
	_ = sha256.Sum256
	c0 := a.Put("prog", e.Program)
	c1 := a.Put("cols", e.Blobs[0])
	inc := c0 + c1
	parts := fmt.Sprintf("prog=%d cols=%d", c0, c1)
	for i, n := range e.Names {
		c := a.Put("heap"+n, e.Blobs[1+i])
		parts += fmt.Sprintf(" %s=%d(%d raw)", n, c, len(e.Blobs[1+i]))
		inc += c
	}
	if os.Getenv("B4_VERBOSE") != "" {
		fmt.Println("   ", parts)
	}
	return inc + a.Ref(1)
}

func cmdPDB(files []string) {
	var data [][]byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		must(err)
		data = append(data, b)
	}
	prod, fine := semcas.ProdCDC(), semcas.FineCDC()
	raw, srch := graphnorm.NewArchive(2), graphnorm.NewArchive(2)
	// whole-file archive with B1 deltas = B1 control (no structure)
	whole := graphnorm.NewArchive(2)
	whole.Probe = 3
	var b1 int64
	fmt.Printf("%-24s %9s %9s %9s %9s %9s\n", "revision", "prodCDC", "fineCDC", "B1whole", "rawCols", "strong")
	for i, d := range data {
		ip, ifn := prod.Add(d), fine.Add(d)
		bw := whole.Put("pdb", d) + whole.Ref(1)
		best := ifn
		if i > 0 && bw < best {
			best = bw
		}
		b1 += best
		er, err := graphnorm.EncodePDB(d, false)
		must(err)
		es, err := graphnorm.EncodePDB(d, true)
		must(err)
		cr, cs := pdbArchiveCost(raw, er), pdbArchiveCost(srch, es)
		fmt.Printf("%-24s %9d %9d %9d %9d %9d\n", filepath.Base(files[i]), ip, ifn, best, cr, cs)
	}
	fmt.Printf("%-24s %9d %9d %9d %9d %9d\n", "CUMULATIVE", prod.Total, fine.Total, b1, raw.Total, srch.Total)
	_ = simdelta.Algorithms
}

func cmdDEX(files []string) {
	var data [][]byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		must(err)
		data = append(data, b)
	}
	prod, fine := semcas.ProdCDC(), semcas.FineCDC()
	whole, raw, srch := graphnorm.NewArchive(2), graphnorm.NewArchive(2), graphnorm.NewArchive(2)
	var b1 int64
	spec := specialCosts("dex", files)
	fmt.Printf("%-16s %10s %10s %10s %10s %10s %10s\n", "revision", "prodCDC", "fineCDC", "B1whole", "rawCols", "strong", "dexz3(cum)")
	for i, d := range data {
		ip, ifn := prod.Add(d), fine.Add(d)
		bw := whole.Put("dex", d) + whole.Ref(1)
		best := ifn
		if i > 0 && bw < best {
			best = bw
		}
		b1 += best
		cost := func(a *graphnorm.Archive, e *graphnorm.DEXEncoding) int64 {
			return a.Put("prog", e.Program) + a.Put("cols", e.Blobs[0]) + a.Put("data", e.Blobs[1]) + a.Ref(1)
		}
		er, err := graphnorm.EncodeDEX(d, false)
		must(err)
		es, err := graphnorm.EncodeDEX(d, true)
		must(err)
		fmt.Printf("%-16s %10d %10d %10d %10d %10d %10d\n", filepath.Base(files[i]), ip, ifn, best, cost(raw, er), cost(srch, es), spec[i])
	}
	fmt.Printf("%-16s %10d %10d %10d %10d %10d %10d\n", "CUMULATIVE", prod.Total, fine.Total, b1, raw.Total, srch.Total, spec[len(spec)-1])
}

// cmdCLR reports bespoke kind-12 vs CDC vs whole-file B1 delta in the common model (no generic CLR adapter).
func cmdCLR(files []string) {
	prod, fine := semcas.ProdCDC(), semcas.FineCDC()
	whole := graphnorm.NewArchive(2)
	var b1 int64
	spec := specialCosts("clr", files)
	fmt.Printf("%-14s %10s %10s %10s %10s\n", "revision", "prodCDC", "fineCDC", "B1whole", "clrz3(cum)")
	for i, f := range files {
		d, err := os.ReadFile(f)
		must(err)
		ip, ifn := prod.Add(d), fine.Add(d)
		bw := whole.Put("clr", d) + whole.Ref(1)
		best := ifn
		if i > 0 && bw < best {
			best = bw
		}
		b1 += best
		fmt.Printf("%-14s %10d %10d %10d %10d\n", filepath.Base(f), ip, ifn, best, spec[i])
	}
	fmt.Printf("%-14s %10d %10d %10d %10d\n", "CUMULATIVE", prod.Total, fine.Total, b1, spec[len(spec)-1])
}
