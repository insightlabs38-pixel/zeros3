package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"testing"
)

func repI2FixtureJSON(t testing.TB) ([]byte, error) {
	t.Helper()
	f, e := os.Open("testing-harnesses/testdata/i2-draft/lab-i2.json.gz")
	if e != nil {
		return nil, e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return nil, e
	}
	defer gz.Close()
	return io.ReadAll(io.LimitReader(gz, 16<<20))
}
func TestRepI2LabWireFidelity(t *testing.T) {
	raw, err := repI2FixtureJSON(t)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 8 {
		t.Fatalf("fixture count %d", len(fixtures))
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			src := newRepMem()
			for text, data := range f.Objects {
				want, err := decodeHexSHA256(text)
				if err != nil {
					t.Fatal(err)
				}
				if got := src.put(data); got != want {
					t.Fatalf("component digest mismatch: %s", text)
				}
			}
			r, err := unmarshalRep(f.Root, repProdLimits)
			if err != nil || !bytes.Equal(r.Recipe, f.LabRecipe) {
				t.Fatalf("translated lab recipe: %v", err)
			}
			raw, err := r.marshal()
			if err != nil || !bytes.Equal(raw, f.Root) {
				t.Fatalf("production wire roundtrip: %v", err)
			}
			root := src.put(f.Root)
			env := &repEnv{Src: src, Limits: repProdLimits}
			var out bytes.Buffer
			n, sum, err := env.reconstruct(root, &out)
			if err != nil || n != int64(len(f.Input)) || sum != sha256.Sum256(f.Input) || !bytes.Equal(out.Bytes(), f.Input) {
				t.Fatalf("lab-wire decode n=%d err=%v", n, err)
			}
			reach, err := env.reachable(root)
			if err != nil {
				t.Fatal(err)
			}
			for id := range src.seen {
				if _, ok := reach.Role[id]; !ok {
					t.Fatalf("decoder read an unprotected component %x", id)
				}
			}
		})
	}
}
