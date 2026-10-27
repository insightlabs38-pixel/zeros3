package main

import (
	"fmt"
	"sort"

	"zeros3-z3/modelz3"
)

// fieldDef splits a ggml block into its scale/min fields and its quantised payload (ggml-common.h).
type fieldDef struct {
	scale [][2]int // byte ranges [lo,hi) of d / dmin / scales / mins
	size  int
}

var fieldDefs = map[string]fieldDef{
	"Q4_0": {[][2]int{{0, 2}}, 18}, "Q4_1": {[][2]int{{0, 4}}, 20}, "Q5_0": {[][2]int{{0, 2}}, 22}, "Q5_1": {[][2]int{{0, 4}}, 24},
	"Q8_0": {[][2]int{{0, 2}}, 34}, "Q4_K": {[][2]int{{0, 16}}, 144}, "Q5_K": {[][2]int{{0, 16}}, 176},
	"Q6_K": {[][2]int{{192, 210}}, 210}, "Q2_K": {[][2]int{{0, 16}, {80, 84}}, 84}, "Q3_K": {[][2]int{{96, 110}}, 110},
}

// runFields answers: is the churn between a base and its fine-tune in the quantised payload or in the scale fields?
func runFields(s *store, a, b string) {
	fa, fb := s.get(a), s.get(b)
	la, lb := layoutOf(fa, "gguf"), layoutOf(fb, "gguf")
	pairs, _ := modelz3.Match(la, lb)
	type acc struct {
		n, blocks                       int64
		scaleBytes, scaleEq             int64
		payBytes, payEq                 int64
		scaleBlocksEq, blocksEq         int64
		scalePacked, payPacked, allPack int64
	}
	by := map[string]*acc{}
	for _, p := range pairs {
		t := p.Target
		fd, ok := fieldDefs[t.Type]
		if !ok {
			continue
		}
		tb, bb := make([]byte, t.Size()), make([]byte, t.Size())
		check(readAtFull(fb, tb, t.Begin))
		check(readAtFull(fa, bb, p.Base.Begin))
		d := by[t.Type]
		if d == nil {
			d = &acc{}
			by[t.Type] = d
		}
		isScale := make([]bool, fd.size)
		for _, r := range fd.scale {
			for i := r[0]; i < r[1]; i++ {
				isScale[i] = true
			}
		}
		nb := len(tb) / fd.size
		var sc, py []byte
		for k := 0; k < nb; k++ {
			bl := k * fd.size
			sEq, all := true, true
			for i := 0; i < fd.size; i++ {
				x := tb[bl+i] ^ bb[bl+i]
				if isScale[i] {
					sc = append(sc, x)
					d.scaleBytes++
					if x == 0 {
						d.scaleEq++
					} else {
						sEq = false
					}
				} else {
					py = append(py, x)
					d.payBytes++
					if x == 0 {
						d.payEq++
					}
				}
				if x != 0 {
					all = false
				}
			}
			d.blocks++
			if sEq {
				d.scaleBlocksEq++
			}
			if all {
				d.blocksEq++
			}
		}
		// plane-separate each class the way the residual component is laid out
		sw, pw := 0, 0
		for _, f := range isScale {
			if f {
				sw++
			} else {
				pw++
			}
		}
		d.scalePacked += modelz3.PackedLen(modelz3.Planes(nil, sc, sw))
		d.payPacked += modelz3.PackedLen(modelz3.Planes(nil, py, pw))
		d.n += t.Size()
	}
	fmt.Printf("%s > %s (aligned, same tensor/type; XOR planes, DEFLATE under the pack rule)\n", a, b)
	fmt.Printf("%-6s %10s | %-22s | %-22s | %-14s | %s\n", "type", "bytes", "scale fields: eq / packed", "payload: eq / packed", "blocks eq/scaleEq", "scale share of packed residual")
	var ks []string
	for k := range by {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		d := by[k]
		fmt.Printf("%-6s %10d | %5.1f%% eq  %5.1f%% pk | %5.1f%% eq  %5.1f%% pk | %5.1f%% / %5.1f%% | %5.1f%%\n", k, d.n, pct(d.scaleEq, d.scaleBytes), pct(d.scalePacked, d.scaleBytes), pct(d.payEq, d.payBytes), pct(d.payPacked, d.payBytes),
			pct(d.blocksEq, d.blocks), pct(d.scaleBlocksEq, d.blocks), pct(d.scalePacked, d.scalePacked+d.payPacked))
	}
}
