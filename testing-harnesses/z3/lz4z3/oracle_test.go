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

// TestHCStress reproduces python-lz4 high-compression blocks (B6_LZ4HC = gen_hc_stress.py output).
func TestHCStress(t *testing.T) {
	dir := os.Getenv("B6_LZ4HC")
	if dir == "" {
		t.Skip("B6_LZ4HC not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "hc*.lz4"))
	for _, f := range files {
		blk, _ := os.ReadFile(f)
		plain, seqs, err := Decode(blk, -1, Limits{})
		if err != nil {
			t.Fatal(f, err)
		}
		lv := int(filepath.Base(f)[2] - '0')
		got, ok := HCSeqs(plain, lv)
		if !ok || commonPrefix(got, seqs) != len(seqs) || len(got) != len(seqs) {
			t.Errorf("%s: level %d sequences differ", filepath.Base(f), lv)
		}
	}
	t.Logf("%d HC blocks", len(files))
}
