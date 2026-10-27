package main

import (
	"fmt"

	"zeros3-z3/dexz3"
	"zeros3-z3/lab"
)

func init() { cmds["overhead"] = overheadCmd }

// overhead DEX...: first-sight cost of the indexed-reference representation, by part (unit-4k
// grouping, index nodes of about 64 groups, 96 B per reference).
func overheadCmd(args []string) error {
	names := []string{"string", "type", "proto", "field", "method", "offset"}
	var raw, pay, payP int64
	var refRaw, refP [6]int64
	var meta, comps, nodeB, nodeP int64
	var cdcP, fileB int64
	for _, p := range args {
		f, b, err := loadDex(p)
		if err != nil {
			return err
		}
		rep, err := dexz3.Encode(f, b, dexz3.Config{Cut: unitCut(4).Cut})
		if err != nil {
			return err
		}
		fileB += int64(len(b))
		cp, _ := lab.CDCPlan(lab.EconFile{Name: p, Data: b}, "dex")
		for _, pc := range cp.Pieces {
			if pc.Comp != nil {
				d, _ := pc.Comp.Data()
				cdcP += lab.PackedLen(d)
			}
		}
		meta += int64(len(rep.Meta)) + 4
		var ids []byte
		for i, c := range rep.Comps {
			pl, ss, _ := dexz3.SplitComp(c)
			raw += int64(len(c))
			pay += int64(len(pl))
			payP += lab.PackedLen(pl)
			for k, s := range ss {
				refRaw[k] += int64(len(s))
				refP[k] += lab.PackedLen(s)
			}
			id := sum(c)
			ids = append(ids, id[:]...)
			if (int(id[0])<<8|int(id[1]))%64 == 0 || i == len(rep.Comps)-1 {
				nodeB += int64(len(ids))
				nodeP += lab.PackedLen(ids)
				ids = nil
			}
			comps++
		}
	}
	var refPT, refRT int64
	for k := range refP {
		refPT += refP[k]
		refRT += refRaw[k]
	}
	nodes := nodeB / (32 * 64)
	fmt.Printf("%d DEX, %s raw, production CDC first sight %s packed\n", len(args), kib(fileB), kib(cdcP))
	fmt.Printf("%-34s %12s %12s\n", "part", "raw", "packed")
	fmt.Printf("%-34s %12s %12s\n", "masked payload groups", kib(pay), kib(payP))
	for k, n := range names {
		fmt.Printf("%-34s %12s %12s\n", "reference stream: "+n, kib(refRaw[k]), kib(refP[k]))
	}
	fmt.Printf("%-34s %12s %12s\n", "references total", kib(refRT), kib(refPT))
	fmt.Printf("%-34s %12d\n", "groups (objects)", comps)
	fmt.Printf("%-34s %12s %12s   (~%d nodes)\n", "index nodes", kib(nodeB), kib(nodeP), nodes)
	fmt.Printf("%-34s %12s\n", "node references x 96 B", kib(nodes*96))
	fmt.Printf("%-34s %12s\n", "meta records (header+map)", kib(meta))
	fmt.Printf("%-34s %12s\n", "component packaging (sum, packed alone)", kib(payP+refPT))
	fmt.Printf("%-34s %12s   (unflat: %s with 96 B per group)\n", "TOTAL packed", kib(payP+refPT+nodeP+nodes*96+meta), kib(payP+refPT+comps*96+meta))
	return nil
}
