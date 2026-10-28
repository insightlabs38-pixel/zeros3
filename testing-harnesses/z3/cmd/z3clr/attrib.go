package main

import (
	"bytes"
	"fmt"
	"os"

	"zeros3-z3/clrz3"
	"zeros3-z3/pdelta"
)

func init() { cmds["attrib"] = attribCmd }

func loadMasked(p string) (d, m []byte, cat []uint8, err error) {
	d, err = os.ReadFile(p)
	if err != nil {
		return
	}
	f, err := clrz3.Parse(bytes.NewReader(d), int64(len(d)), clrz3.Limits{})
	if err != nil {
		return
	}
	m, cat, err = clrz3.Categorize(f, d)
	return
}

// attribRow prints the Z3-33 attribution of the literal bytes of NEW against OLD.
func attribRow(title string, a pdelta.Attr) {
	pc := func(v int) string { return fmt.Sprintf("%5.1f", 100*float64(v)/float64(max(a.Raw, 1))) }
	resid := a.Removed[clrz3.CatLiteral] + a.Removed[clrz3.CatMeta]
	fmt.Printf("%-38s raw %7d (%5.1f%% of %d) | literal %s | true-meta %s | idx %s | tok %s | rva/layout %s | debug/res/sign %s | resid %s\n",
		title, a.Raw, 100*float64(a.Raw)/float64(max(a.Size, 1)), a.Size,
		pc(a.Remaining[clrz3.CatLiteral]), pc(a.Remaining[clrz3.CatMeta]),
		pc(a.Removed[clrz3.CatIdx]+a.Remaining[clrz3.CatIdx]), pc(a.Removed[clrz3.CatTok]+a.Remaining[clrz3.CatTok]),
		pc(a.Removed[clrz3.CatLayout]+a.Remaining[clrz3.CatLayout]), pc(a.Remaining[clrz3.CatOther]+a.Removed[clrz3.CatOther]), pc(resid))
}

// attrib OLD NEW [OLD NEW ...]
func attribCmd(args []string) error {
	for i := 0; i+1 < len(args); i += 2 {
		bd, bm, _, err := loadMasked(args[i])
		if err != nil {
			return err
		}
		cd, cm, cat, err := loadMasked(args[i+1])
		if err != nil {
			return err
		}
		attribRow(shortPair(args[i], args[i+1]), pdelta.Attribute(bd, cd, bm, cm, cat))
	}
	return nil
}

func shortPair(a, b string) string {
	tail := func(p string) string {
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
	return tail(a) + " -> " + tail(b)
}
