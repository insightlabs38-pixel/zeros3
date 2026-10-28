package main

import (
	"bytes"
	"fmt"
	"os"

	"zeros3-z3/clrz3"
	"zeros3-z3/econx"
	"zeros3-z3/idxref"
	"zeros3-z3/lab"
)

func init() { cmds["refc"] = refc }

type coderRow struct {
	Raw, Packed int64
}

// evalCoders prices the reference streams of one representation under each coder; groups are
// the (component, stream) pairs.
func evalCoders(refs []idxref.RefRec) map[string]coderRow {
	type key struct {
		comp   int
		stream uint8
	}
	g := map[key][][]byte{}
	for _, r := range refs {
		k := key{r.Comp, r.Stream}
		g[k] = append(g[k], r.B)
	}
	out := map[string]coderRow{}
	add := func(name string, b []byte) {
		r := out[name]
		r.Raw += int64(len(b))
		r.Packed += lab.PackedSize(b)
		out[name] = r
	}
	for _, rs := range g {
		add("A prefix", bytes.Join(rs, nil))
		add("C dict", idxref.DictEncode(rs))
		add("C2 split", idxref.DictSplitEncode(rs))
		add("D mtf16", idxref.MTFEncode(rs, 16))
		add("D mtf64", idxref.MTFEncode(rs, 64))
	}
	return out
}

// refc FILE... : the reference-coder comparison on CLR assemblies (K0 variants and coders).
func refc(args []string) error {
	cfgs := []struct {
		name string
		cfg  clrz3.Config
	}{{"K0=3", clrz3.Config{}}, {"auto5", clrz3.Config{Auto: 5}}, {"auto12", clrz3.Config{Auto: 12}}, {"auto25", clrz3.Config{Auto: 25}}}
	for _, p := range args {
		d, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := clrz3.Parse(bytes.NewReader(d), int64(len(d)), clrz3.Limits{})
		if err != nil {
			return err
		}
		fmt.Printf("%s (%d B)\n", p, len(d))
		for _, c := range cfgs {
			c.cfg.Cut = econx.UnitCut(4).Cut
			rep, err := clrz3.Encode(f, d, c.cfg)
			if err != nil {
				return err
			}
			res := evalCoders(rep.Refs)
			fmt.Printf("  %-7s", c.name)
			for _, n := range []string{"A prefix", "C dict", "C2 split", "D mtf16", "D mtf64"} {
				fmt.Printf(" | %s raw %7d packed %7d", n, res[n].Raw, res[n].Packed)
			}
			fmt.Println()
		}
	}
	return nil
}
