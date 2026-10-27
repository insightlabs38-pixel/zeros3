package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRealCorpusExact is the focused real-corpus exactness suite: every table of the first
// revision of each producer's variants and chain round-trips through every representation
// form (parse, encode, discard the analysis state, decode from the serialized recipe and the
// components alone; length, SHA-256 and bytes compared). It needs the producer corpus that
// oracle/sst/setup.sh generates (outside Git) and is skipped without it:
//
//	Z3_SST_CORPUS=$HOME/.cache/z3-sst/corpus go test ./cmd/z3sst -run RealCorpus -v
func TestRealCorpusExact(t *testing.T) {
	root := os.Getenv("Z3_SST_CORPUS")
	if root == "" {
		t.Skip("Z3_SST_CORPUS not set")
	}
	var dirs []string
	for _, p := range []string{"leveldb", "rocksdb", "pebble"} {
		for _, rev := range []string{"variants/r0", "variants/v09-reexport", "variants/v10-churn", "chain/c3"} {
			dirs = append(dirs, filepath.Join(root, p, rev))
		}
	}
	if err := cmdRT(dirs); err != nil {
		t.Fatal(err)
	}
}

// TestWorkloadValuesAreDeterministic: values depend only on (class, id, version, seed).
func TestWorkloadValuesAreDeterministic(t *testing.T) {
	for _, c := range []byte("mjblrz") {
		a, b := val(c, 7, 1, 30), val(c, 7, 1, 30)
		if string(a) != string(b) || len(a) == 0 {
			t.Fatalf("class %c: not deterministic", c)
		}
		if c != 'r' && string(val(c, 7, 2, 30)) == string(a) {
			t.Fatalf("class %c: version does not change the value", c)
		}
	}
}
