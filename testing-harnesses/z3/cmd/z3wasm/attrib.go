package main

import (
	"bytes"
	"fmt"
	"os"

	"zeros3-z3/pdelta"
	"zeros3-z3/wasmz3"
)

func init() { cmds["attrib"] = attribCmd }

func load(p string) (d, masked []byte, cat []uint8, err error) {
	if d, err = os.ReadFile(p); err != nil {
		return
	}
	dir, err := wasmz3.Parse(bytes.NewReader(d), int64(len(d)), wasmz3.Limits{})
	if err != nil {
		return
	}
	m, err := wasmz3.Analyze(dir, d, wasmz3.Limits{})
	if err != nil {
		return
	}
	masked, cat = wasmz3.Categorize(m, d)
	return
}

func attribRow(title string, a pdelta.Attr) {
	pc := func(v int) string { return fmt.Sprintf("%5.1f", 100*float64(v)/float64(max(a.Raw, 1))) }
	resid := a.Removed[wasmz3.CatLiteral] + a.Removed[wasmz3.CatStruct] + a.Removed[wasmz3.CatLen] + a.Removed[wasmz3.CatCustom]
	fmt.Printf("%-44s raw %8d (%5.1f%% of %d) | code/data %s | structure %s | index %s | addr %s | length %s | custom %s | resid %s\n",
		title, a.Raw, 100*float64(a.Raw)/float64(max(a.Size, 1)), a.Size,
		pc(a.Remaining[wasmz3.CatLiteral]), pc(a.Remaining[wasmz3.CatStruct]), pc(a.Removed[wasmz3.CatIdx]+a.Remaining[wasmz3.CatIdx]), pc(a.Removed[wasmz3.CatAddr]+a.Remaining[wasmz3.CatAddr]),
		pc(a.Remaining[wasmz3.CatLen]), pc(a.Remaining[wasmz3.CatCustom]), pc(resid))
}

// attrib OLD NEW [OLD NEW ...]
func attribCmd(args []string) error {
	for i := 0; i+1 < len(args); i += 2 {
		bd, bm, _, err := load(args[i])
		if err != nil {
			return err
		}
		cd, cm, cat, err := load(args[i+1])
		if err != nil {
			return err
		}
		attribRow(short(args[i])+" -> "+short(args[i+1]), pdelta.Attribute(bd, cd, bm, cm, cat))
	}
	return nil
}

func short(p string) string {
	n := 0
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			n++
			if n == 3 {
				return p[i+1:]
			}
		}
	}
	return p
}
