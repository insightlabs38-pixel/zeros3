package main

import (
	"flag"
	"fmt"

	"zeros3-z3/binref"
)

func runStat(args []string) {
	fs := flag.NewFlagSet("stat", flag.ExitOnError)
	a := fs.String("bin", "", "")
	um := fs.String("units", "auto", "")
	fs.Parse(args)
	b := mustLoad(*a)
	n, _, err := normalize(b, cfg{setFull, binref.IdentSymbol, *um})
	if err != nil {
		fatal(err)
	}
	fmt.Printf("%+v\n", n.Stat)
	if b.Pe != nil {
		i := b.Pe
		fmt.Printf("PE: sections %d base-reloc entries %d (slots %d) import descriptors %d exports %d RUNTIME_FUNCTION %d coff symbols %d certificate bytes %d slots by directory %v\n", len(i.Sections), i.BaseRelocs, i.RelocSlots, i.Imports, i.Exports, i.Pdata, i.Syms, i.CertSize, i.SlotsByDir)
	}
	for _, s := range n.Streams {
		if s.Kind == "refs" {
			fmt.Printf("refs stream of %s: %d B\n", b.Im.Regions[s.Region].Name, len(s.Data))
		}
	}
}

func runDbg(args []string) {
	fs := flag.NewFlagSet("dbg", flag.ExitOnError)
	a := fs.String("base", "", "")
	b := fs.String("cur", "", "")
	fs.Parse(args)
	base, cur := mustLoad(*a), mustLoad(*b)
	c := cfg{setFull, binref.IdentSymbol, "pcln"}
	nb, _, _ := normalize(base, c)
	nc, _, _ := normalize(cur, c)
	for i, r := range cur.Im.Regions {
		if r.Name != ".text" {
			continue
		}
		var br binref.Region
		for _, x := range base.Im.Regions {
			if x.Name == ".text" {
				br = x
			}
		}
		bm, cm := nb.Masked[br.Off:br.Off+br.Len], nc.Masked[r.Off:r.Off+r.Len]
		bo, co := cdcObjs(bm), cdcObjs(cm)
		n, tot := Cost(bo, co)
		fmt.Println("region", i, "base chunks", len(bo), "cur chunks", len(co), "new bytes", n, "of", len(cm), tot)
		for k := 0; k < 12 && k < len(bo) && k < len(co); k++ {
			fmt.Println(k, bo[k].size, co[k].size, bo[k].key == co[k].key)
		}
		d := deltaRuns(bm, cm, true)
		fmt.Println("runs", len(d.Runs), "first", d.Runs[:min(5, len(d.Runs))], "last", d.Runs[max(0, len(d.Runs)-3):])
	}
}
