package main

import (
	"bytes"
	"flag"
	"fmt"
	"sort"

	"zeros3-z3/lab"
	"zeros3-z3/parquetz3"
)

// cmdOverhead prints what a first-sight representation of one file is made of: component
// bytes by class (logical and packed), the serialized recipe by purpose, the references.
func cmdOverhead(args []string) error {
	fs := flag.NewFlagSet("overhead", flag.ExitOnError)
	path := fs.String("file", "", "parquet file (default: pyarrow-variants/r0)")
	target := fs.Int("target", 8<<10, "group target")
	nop := fs.Bool("nopredict", false, "explicit recipes")
	fs.Parse(args)
	if *path == "" {
		sts, _, err := loadFamily("pyarrow-variants")
		if err != nil {
			return err
		}
		*path = sts[0].Path
	}
	data, err := readFile(*path)
	if err != nil {
		return err
	}
	type cl struct {
		n         int
		raw, pack int64
	}
	classes := map[string]*cl{}
	seen := map[parquetz3.ID]bool{}
	rec, sz, st, err := parquetz3.Encode(bytes.NewReader(data), int64(len(data)), parquetz3.Options{Target: *target, NoPredict: *nop}, func(class string, id parquetz3.ID, b []byte) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		c := classes[class]
		if c == nil {
			c = &cl{}
			classes[class] = c
		}
		c.n++
		c.raw += int64(len(b))
		c.pack += lab.PackedSize(b)
		return nil
	})
	if err != nil {
		return err
	}
	pr, err := parquetz3.ParseRecipe(rec, parquetz3.Limits{})
	if err != nil {
		return err
	}
	fmt.Printf("file %s: %d bytes; recipe %d bytes (%d packed); %d members (references at 96 B = %d)\n", *path, len(data), len(rec), lab.PackedSize(rec), len(pr.Digests), 96*len(pr.Digests))
	pct := func(n int64) string { return fmt.Sprintf("%.2f%%", 100*float64(n)/float64(len(data))) }
	fmt.Println("| component | objects | logical bytes | packed bytes | % target (packed) |")
	fmt.Println("|---|---:|---:|---:|---:|")
	var keys []string
	for k := range classes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var packTot int64
	for _, k := range keys {
		c := classes[k]
		packTot += c.pack
		fmt.Printf("| %s | %d | %d | %d | %s |\n", k, c.n, c.raw, c.pack, pct(c.pack))
	}
	head := int64(len(rec)) - sz.Digests
	fmt.Println("| recipe: page headers (raw, inline) | | | ", sz.Headers, "|", pct(sz.Headers), "|")
	fmt.Println("| recipe: page layout (flags, non-null counts, explicit runs) | | | ", sz.Layout, "|", pct(sz.Layout), "|")
	fmt.Println("| recipe: compression ids and references | | | ", sz.CompRefs, "|", pct(sz.CompRefs), "|")
	fmt.Println("| recipe: inline framing (magic, footer, small gaps) | | | ", sz.Frames, "|", pct(sz.Frames), "|")
	fmt.Println("| recipe: column table and stream roots | | | ", sz.Columns, "|", pct(sz.Columns), "|")
	fmt.Println("| recipe: other (counts, kinds) | | | ", sz.Other, "|", pct(sz.Other), "|")
	fmt.Println("| references (96 B per member) | | | ", 96*len(pr.Digests), "|", pct(int64(96*len(pr.Digests))), "|")
	fmt.Printf("total new-store cost = packed components %d + recipe head %d + references %d = %d (%s of the file)\n", packTot, head, 96*len(pr.Digests), packTot+head+int64(96*len(pr.Digests)), pct(packTot+head+int64(96*len(pr.Digests))))
	fmt.Printf("one component per value [ORACLE floor, not a design]: %d value records + %d level runs = %d objects, references alone %s (%s of the file) vs %d objects for the chosen grouping\n",
		st.ValueRecords, st.LevelRecords, st.ValueRecords+st.LevelRecords, human(96*(st.ValueRecords+st.LevelRecords)), pct(96*(st.ValueRecords+st.LevelRecords)), len(seen))
	fmt.Printf("stats: logical chunks %d (%s), opaque chunks %d (%s), pages %d, groups val %d lvl %d, nodes %d, predicted dictionaries %d explicit %d, comp %v, explicit comp recipe %d B, layouts std %d, stored width %d, explicit levels %d values %d\n",
		st.LogicalChunks, human(st.LogicalBytes), st.OpaqueChunks, human(st.OpaqueBytes), st.Pages, st.ValueGroups, st.LevelGroups, st.Nodes, st.DictPredicted, st.DictExplicit, st.CompPages, st.CompRecBytes, st.LayoutStd, st.LayoutWidth, st.LayoutLvl, st.LayoutVal)
	return nil
}
