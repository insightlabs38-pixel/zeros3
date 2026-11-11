package graphcore

import (
	"os"
	"path/filepath"
	"testing"
)

// Experiment (P6): would additional column predictors pay off on the pinned Portable PDB
// histories? Prices every column with the encoder's own estimator (deflate pack size).
func TestPDBPredictorExperiment(t *testing.T) {
	dir := corpusDir()
	if dir == "" {
		t.Skip("B4_CORPUS not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "pdb", "*-netstandard2.0.pdb"))
	var cur, d2, best3 int64
	type agg struct{ raw, delta, delta2, xorp int64 }
	byCol := map[string]*agg{}
	for _, f := range files {
		d, _ := os.ReadFile(f)
		tables, _, _, err := parsePDB(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, tb := range tables {
			if tb.Rows == 0 {
				continue
			}
			off := tb.Off
			vals := make([][]uint64, len(tb.Cols))
			for c := range vals {
				vals[c] = make([]uint64, tb.Rows)
			}
			for r := 0; r < tb.Rows; r++ {
				for c, col := range tb.Cols {
					var v uint64
					for k := col.W - 1; k >= 0; k-- {
						v = v<<8 | uint64(d[off+k])
					}
					vals[c][r] = v
					off += col.W
				}
			}
			for c, col := range tb.Cols {
				v := vals[c]
				raw := packedLen(uvarints(v))
				dl := make([]uint64, len(v))
				d2l := make([]uint64, len(v))
				xo := make([]uint64, len(v))
				var p, pd, px uint64
				for i, x := range v {
					dl[i] = Zig(int64(x - p))
					dd := int64(x-p) - int64(pd)
					d2l[i] = Zig(dd)
					pd = x - p
					xo[i] = x ^ px
					p, px = x, x
				}
				delta, delta2, xp := packedLen(uvarints(dl)), packedLen(uvarints(d2l)), packedLen(uvarints(xo))
				c1 := min(raw, delta)
				cur += c1
				d2 += min(c1, delta2)
				best3 += min(c1, delta2, xp)
				k := tb.Name + "." + col.Name
				if byCol[k] == nil {
					byCol[k] = &agg{}
				}
				byCol[k].raw += raw
				byCol[k].delta += delta
				byCol[k].delta2 += delta2
				byCol[k].xorp += xp
			}
		}
	}
	t.Logf("column bytes, best of {raw,delta}=%d  +delta2=%d (%.2f%%)  +xor=%d (%.2f%%)", cur, d2, 100*float64(cur-d2)/float64(cur), best3, 100*float64(cur-best3)/float64(cur))
}
