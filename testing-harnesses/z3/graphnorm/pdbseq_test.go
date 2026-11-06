package graphnorm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPDBLineOracle(t *testing.T) {
	d := corpusDir()
	if d == "" {
		t.Skip()
	}
	files, _ := filepath.Glob(filepath.Join(d, "pdb", "*-netstandard2.0.pdb"))
	a := NewArchive(2)
	raw := NewArchive(2)
	for _, f := range files {
		b, _ := os.ReadFile(f)
		h, rw, un, err := PDBLineOracle(b)
		if err != nil {
			t.Fatal(err)
		}
		_, st, _, _ := parsePDB(b)
		blob := b[st["#Blob"][0]:st["#Blob"][1]]
		t.Logf("%s: blob %d -> %d, rewritten=%d unparsed=%d  cost raw=%d norm=%d", filepath.Base(f), len(blob), len(h), rw, un, raw.Put("b", blob), a.Put("b", h))
	}
	t.Logf("cumulative blob heap: raw=%d lineNormalized=%d", raw.Total, a.Total)
}
