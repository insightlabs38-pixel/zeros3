package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"zeros3-z3/mdelta2"
	"zeros3-z3/simdelta"
)

// bitmapCost is a bitmap compressed with the production DEFLATE rule.
func bitmapBytes(bits []bool) int {
	b := make([]byte, (len(bits)+7)/8)
	for i, v := range bits {
		if v {
			b[i>>3] |= 1 << uint(i&7)
		}
	}
	return mdelta2.DeflateLen(b) + 4
}

func uvarSize(v uint64) int { var t [10]byte; return binary.PutUvarint(t[:], v) }

type sparseRow struct {
	Name        string  `json:"name"`
	Elems       int     `json:"elems"`
	EqElems     float64 `json:"frac_equal_elems"`
	EqRows      float64 `json:"frac_equal_rows"`
	EqBlocks    float64 `json:"frac_equal_blocks64"`
	Identical   bool    `json:"identical"`
	Plane       int     `json:"flate_planes"`
	NRC         int     `json:"nrc_whole"`
	RowSparse   int     `json:"row_bitmap_plus_nrc"`
	BlockSparse int     `json:"block64_bitmap_plus_nrc"`
	COO         int     `json:"coo_gaps_plus_nrc"`
	B1Adaptive  int     `json:"b1_adaptive"`
	B1Sparse    int     `json:"b1_sparse_xor"`
	Best        string  `json:"best"`
	BestBytes   int     `json:"best_bytes"`
}

// cmdSparse: sparse [-json f] TARGET BASE   (same-name float tensors; narrowed domain when exact)
func cmdSparse(args []string) {
	fl := flag.NewFlagSet("sparse", flag.ExitOnError)
	jsonOut := fl.String("json", "", "output")
	minE := fl.Int("min", 4096, "min elements")
	fl.Parse(args)
	p := fl.Args()
	t, err := mdelta2.Open(p[0])
	must(err)
	b, err := mdelta2.Open(p[1])
	must(err)
	var rows []sparseRow
	var tot struct{ elems, eq, plane, nrc, row, blk, coo, b1a, b1s, best, direct float64 }
	for i := range t.Tensors {
		tt := t.Tensors[i]
		bi, ok := b.Index(tt.Name)
		if !ok || !tt.DType.IsFloat() || int(tt.Elems()) < *minE || b.Tensors[bi].Elems() != tt.Elems() || b.Tensors[bi].DType != tt.DType {
			continue
		}
		td, _ := t.Read(i)
		bd, _ := b.Read(bi)
		cd := tt.DType
		tn, bn := td, bd
		if cd == mdelta2.DF32 {
			a, ok1 := mdelta2.Narrow(td, mdelta2.DF16)
			c, ok2 := mdelta2.Narrow(bd, mdelta2.DF16)
			if ok1 && ok2 {
				tn, bn, cd = a, c, mdelta2.DF16
			}
		}
		w := cd.Width()
		n := len(tn) / w
		r := sparseRow{Name: tt.Name, Elems: n, Identical: bytes.Equal(td, bd)}
		cols := n
		if len(tt.Shape) >= 2 {
			cols = int(tt.Shape[len(tt.Shape)-1])
		}
		nrows := n / cols
		chgE := make([]bool, n)
		chgRow := make([]bool, nrows)
		chgBlk := make([]bool, (n+63)/64)
		eq := 0
		for k := 0; k < n; k++ {
			if !bytes.Equal(tn[k*w:(k+1)*w], bn[k*w:(k+1)*w]) {
				chgE[k] = true
				chgRow[k/cols] = true
				chgBlk[k/64] = true
			} else {
				eq++
			}
		}
		count := func(v []bool) float64 {
			c := 0
			for _, x := range v {
				if !x {
					c++
				}
			}
			return float64(c) / float64(len(v))
		}
		r.EqElems, r.EqRows, r.EqBlocks = float64(eq)/float64(n), count(chgRow), count(chgBlk)
		kp := &mdelta2.Params{Kind: mdelta2.KID, Res: mdelta2.RKey, NB: 1, Prec: 4}
		base := mdelta2.Base{DT: cd, Data: bn}
		pred, _ := mdelta2.Predict(kp, cd, tt.Shape, []mdelta2.Base{base})
		res := mdelta2.MakeResidual(mdelta2.RKey, w, tn, pred)
		zero := make([]byte, len(tn))
		direct := len(mdelta2.Deflate(planesJoin(tn, w)))
		if f, ok := mdelta2.EncodeResidual(mdelta2.CFPE, cd, mdelta2.RXor, tn, make([]byte, len(tn))); ok && len(f) < direct {
			direct = len(f)
		}
		if direct > len(tn) {
			direct = len(tn)
		}
		r.Plane = mdelta2.PlaneDeflate(res, w)
		r.NRC = len(mdelta2.NRCEncode(cd, res, pred))
		// row-sparse: bitmap of changed rows + NRC over the changed rows' residual
		sub := func(sel func(k int) bool) ([]byte, []byte) {
			var rr, pp []byte
			for k := 0; k < n; k++ {
				if sel(k) {
					rr = append(rr, res[k*w:(k+1)*w]...)
					pp = append(pp, pred[k*w:(k+1)*w]...)
				}
			}
			return rr, pp
		}
		rr, pp := sub(func(k int) bool { return chgRow[k/cols] })
		r.RowSparse = bitmapBytes(chgRow) + len(mdelta2.NRCEncode(cd, rr, pp))
		rr, pp = sub(func(k int) bool { return chgBlk[k/64] })
		r.BlockSparse = bitmapBytes(chgBlk) + len(mdelta2.NRCEncode(cd, rr, pp))
		// COO: Rice-ish varint gaps (DEFLATEd) + NRC over changed elements only
		var gaps []byte
		prev := -1
		for k := 0; k < n; k++ {
			if chgE[k] {
				gaps = binary.AppendUvarint(gaps, uint64(k-prev-1))
				prev = k
			}
		}
		rr, pp = sub(func(k int) bool { return chgE[k] })
		r.COO = mdelta2.DeflateLen(gaps) + len(mdelta2.NRCEncode(cd, rr, pp))
		_ = zero
		if w == 4 || w == 2 {
			if wire, err := simdelta.Encode("adaptive", [][]byte{bn}, tn); err == nil {
				r.B1Adaptive = len(wire)
			}
			if wire, err := simdelta.Encode("sparse-xor", [][]byte{bn}, tn); err == nil {
				r.B1Sparse = len(wire)
			}
		}
		cands := map[string]int{"flate-planes": r.Plane, "nrc-whole": r.NRC, "row-sparse": r.RowSparse, "block-sparse": r.BlockSparse, "coo": r.COO, "b1-adaptive": r.B1Adaptive, "b1-sparse-xor": r.B1Sparse}
		r.Best, r.BestBytes = "direct", direct
		keys := make([]string, 0, len(cands))
		for k := range cands {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if v := cands[k]; v > 0 && v < r.BestBytes {
				r.Best, r.BestBytes = k, v
			}
		}
		if r.Identical {
			r.Best, r.BestBytes = "copy", 14
		}
		rows = append(rows, r)
		tot.elems += float64(n)
		tot.eq += float64(eq)
		tot.plane += float64(r.Plane)
		tot.nrc += float64(r.NRC)
		tot.row += float64(r.RowSparse)
		tot.blk += float64(r.BlockSparse)
		tot.coo += float64(r.COO)
		tot.b1a += float64(r.B1Adaptive)
		tot.b1s += float64(r.B1Sparse)
		tot.best += float64(r.BestBytes)
		tot.direct += float64(direct)
	}
	fmt.Printf("%s vs %s: %d tensors, %.0f elems, equal-element fraction %.4f\n  direct %.0f | flate-planes %.0f | nrc %.0f | row-sparse %.0f | block64-sparse %.0f | coo %.0f | b1-adaptive %.0f | b1-sparse-xor %.0f | per-tensor-best %.0f\n",
		p[0], p[1], len(rows), tot.elems, tot.eq/tot.elems, tot.direct, tot.plane, tot.nrc, tot.row, tot.blk, tot.coo, tot.b1a, tot.b1s, tot.best)
	if *jsonOut != "" {
		bj, _ := json.MarshalIndent(map[string]any{"target": p[0], "base": p[1], "tensors": rows, "totals": tot}, "", " ")
		os.WriteFile(*jsonOut, bj, 0o644)
	}
}

func planesJoin(b []byte, w int) []byte { return bytes.Join(mdelta2.Planes(b, w), nil) }
