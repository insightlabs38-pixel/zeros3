package graphcore

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
	var rawTot, normTot int64
	for _, f := range files {
		b, _ := os.ReadFile(f)
		h, rw, un, err := PDBLineOracle(b)
		if err != nil {
			t.Fatal(err)
		}
		_, st, _, _ := parsePDB(b)
		blob := b[st["#Blob"][0]:st["#Blob"][1]]
		rawTot += packedLen(blob)
		normTot += packedLen(h)
		t.Logf("%s: blob %d -> %d, rewritten=%d unparsed=%d", filepath.Base(f), len(blob), len(h), rw, un)
	}
	t.Logf("packed blob heap: raw=%d lineNormalized=%d", rawTot, normTot)
}
