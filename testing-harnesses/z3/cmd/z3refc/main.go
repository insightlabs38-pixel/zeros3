// z3refc is the Z3-33 repeated-reference coder comparison: the reference streams of one DEX, one
// CLR assembly and one Wasm module are priced under (A) fixed 3-byte identity prefixes, (B) the
// production pack stage alone (the packed size of A), (A') prefixes sized by candidate-table
// size, (C) a component-local dictionary and (D) a 16-entry recency cache. Dictionary and cache
// are priced on top of the fixed 3-byte prefixes and of the adaptive ones.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"zeros3-z3/clrz3"
	"zeros3-z3/dexz3"
	"zeros3-z3/econx"
	"zeros3-z3/idxref"
	"zeros3-z3/lab"
	"zeros3-z3/wasmz3"
)

type enc func(auto int) (refs []idxref.RefRec, comps [][]byte, err error)

func dexEnc(p string) enc {
	return func(auto int) ([]idxref.RefRec, [][]byte, error) {
		d, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		f, err := dexz3.Parse(bytes.NewReader(d), int64(len(d)), dexz3.Limits{})
		if err != nil {
			return nil, nil, err
		}
		r, err := dexz3.Encode(f, d, dexz3.Config{Auto: auto, Cut: econx.UnitCut(4).Cut})
		if err != nil {
			return nil, nil, err
		}
		return r.Refs, r.Comps, nil
	}
}

func clrEnc(p string) enc {
	return func(auto int) ([]idxref.RefRec, [][]byte, error) {
		d, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		f, err := clrz3.Parse(bytes.NewReader(d), int64(len(d)), clrz3.Limits{})
		if err != nil {
			return nil, nil, err
		}
		r, err := clrz3.Encode(f, d, clrz3.Config{Auto: auto, Cut: econx.UnitCut(4).Cut})
		if err != nil {
			return nil, nil, err
		}
		return r.Refs, r.Comps, nil
	}
}

func wasmEnc(p string) enc {
	return func(auto int) ([]idxref.RefRec, [][]byte, error) {
		d, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		dir, err := wasmz3.Parse(bytes.NewReader(d), int64(len(d)), wasmz3.Limits{})
		if err != nil {
			return nil, nil, err
		}
		m, err := wasmz3.Analyze(dir, d, wasmz3.Limits{})
		if err != nil {
			return nil, nil, err
		}
		r, err := wasmz3.Encode(m, d, wasmz3.Config{Auto: auto, Cut: econx.UnitCut(4).Cut})
		if err != nil {
			return nil, nil, err
		}
		return r.Refs, r.Comps, nil
	}
}

type cost struct{ raw, packed int64 }

func price(refs []idxref.RefRec, coder func([][]byte) []byte) cost {
	type key struct {
		comp   int
		stream uint8
	}
	g := map[key][][]byte{}
	var order []key
	for _, r := range refs {
		k := key{r.Comp, r.Stream}
		if _, ok := g[k]; !ok {
			order = append(order, k)
		}
		g[k] = append(g[k], r.B)
	}
	var c cost
	for _, k := range order {
		b := coder(g[k])
		c.raw += int64(len(b))
		c.packed += lab.PackedSize(b)
	}
	return c
}

func plain(rs [][]byte) []byte { return bytes.Join(rs, nil) }
func mtf16(rs [][]byte) []byte { return idxref.MTFEncode(rs, 16) }

var autoPct = 12

func main() {
	if v := os.Getenv("Z3REFC_AUTO"); v != "" {
		fmt.Sscan(v, &autoPct)
	}
	args := os.Args[1:]
	if len(args) > 1 && args[0] == "dexecon" {
		if err := dexEcon(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: z3refc dex:FILE|clr:FILE|wasm:FILE ...")
		os.Exit(2)
	}
	fmt.Printf("%-22s %9s | %9s %9s | %9s %9s | %9s %9s | %9s %9s | %9s %9s | %9s | %s\n", "corpus", "comps", "A raw", "A packed", "A' raw", "A' packed", "C raw", "C packed", "D raw", "D packed", "A'+C raw", "A'+C pack", "A'+D pack", "winner (packed)")
	for _, a := range args {
		kind, p, _ := strings.Cut(a, ":")
		var e enc
		switch kind {
		case "dex":
			e = dexEnc(p)
		case "clr":
			e = clrEnc(p)
		case "wasm":
			e = wasmEnc(p)
		default:
			fmt.Fprintln(os.Stderr, "unknown kind", kind)
			os.Exit(2)
		}
		refs3, comps3, err := e(0)
		if err != nil {
			fmt.Fprintln(os.Stderr, a, err)
			os.Exit(1)
		}
		refsA, compsA, err := e(autoPct)
		if err != nil {
			fmt.Fprintln(os.Stderr, a, err)
			os.Exit(1)
		}
		A, C, D := price(refs3, plain), price(refs3, idxref.DictEncode), price(refs3, mtf16)
		Au, AD, AC := price(refsA, plain), price(refsA, mtf16), price(refsA, idxref.DictEncode)
		var tot3, totA int64
		for _, c := range comps3 {
			tot3 += lab.PackedSize(c)
		}
		for _, c := range compsA {
			totA += lab.PackedSize(c)
		}
		best, name := A.packed, "A K0=3"
		for _, x := range []struct {
			n string
			c cost
		}{{"A' auto", Au}, {"C dict", C}, {"D cache", D}, {"A'+C", AC}, {"A'+D", AD}} {
			if x.c.packed < best {
				best, name = x.c.packed, x.n
			}
		}
		fmt.Printf("%-22s %9d | %9d %9d | %9d %9d | %9d %9d | %9d %9d | %9d %9d | %9d | %s (%.1f%% of A; whole rep %d -> %d)\n",
			strings.TrimPrefix(a, kind+":")[max(0, len(a)-len(kind)-1-22):], len(comps3), A.raw, A.packed, Au.raw, Au.packed, C.raw, C.packed, D.raw, D.packed, AC.raw, AC.packed, AD.packed,
			name, 100*float64(best)/float64(max(A.packed, 1)), tot3, totA)
	}
}
