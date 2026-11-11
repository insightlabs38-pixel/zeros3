package graphcore

import (
	"os"
	"path/filepath"
	"testing"
)

func corpusDir() string { return os.Getenv("B4_CORPUS") }

func TestBitstreamRoundTripCorpus(t *testing.T) {
	d := corpusDir()
	if d == "" {
		t.Skip("B4_CORPUS unset")
	}
	files, _ := filepath.Glob(filepath.Join(d, "bc", "*.bc"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		bc, err := ParseBitstream(b)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		t.Logf("%s: %d top-level blocks tail=%d", filepath.Base(f), len(bc.Blocks), len(bc.Tail))
	}
}
