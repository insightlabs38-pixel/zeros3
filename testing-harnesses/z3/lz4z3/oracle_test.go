package lz4z3

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPredictorsMatchLibrary compares the predictors with blocks written by the C library
// (python-lz4 1.9.4). B6_LZ4FX points at a directory of N_ACC.src / N_ACC.lz4 pairs.
func TestPredictorsMatchLibrary(t *testing.T) {
	dir := os.Getenv("B6_LZ4FX")
	if dir == "" {
		t.Skip("B6_LZ4FX not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.src"))
	bad := 0
	for _, f := range files {
		src, _ := os.ReadFile(f)
		lz, _ := os.ReadFile(strings.TrimSuffix(f, ".src") + ".lz4")
		acc := 1
		base := strings.TrimSuffix(filepath.Base(f), ".src")
		if i := strings.LastIndex(base, "_"); i >= 0 {
			acc = atoi(base[i+1:])
		}
		dec, _, err := Decode(lz, len(src), Limits{})
		if err != nil || !bytes.Equal(dec, src) {
			t.Fatalf("%s: decode: %v", f, err)
		}
		got, _ := PredictBlock(PredDefault5, src, acc)
		if !bytes.Equal(got, lz) {
			bad++
			t.Errorf("%s: predictor differs (%d vs %d bytes)", base, len(got), len(lz))
		}
	}
	t.Logf("%d fixtures, %d mismatches", len(files), bad)
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}
