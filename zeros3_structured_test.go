package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"testing"
)

func TestRepStructuredLabWireFidelity(t *testing.T) {
	raw, err := os.ReadFile("testing-harnesses/testdata/i3-draft/lab-structured.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 9 {
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

func TestRepStructuredEncoderLabIdentity(t *testing.T) {
	raw, err := os.ReadFile("testing-harnesses/testdata/i3-draft/lab-structured.json")
	if err != nil {
		t.Fatal(err)
	}
	var fs []repLabCodecFixture
	if err = json.Unmarshal(raw, &fs); err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		t.Run(f.Name, func(t *testing.T) {
			l := newRepLab(t, repModeOff)
			b := l.s.newRepBuilder(int64(len(f.Input)), nil, nil)
			defer b.close()
			want, err := unmarshalRep(f.Root, repProdLimits)
			if err != nil {
				t.Fatal(err)
			}
			r, err := b.encodeKind(want.Kind, bytes.NewReader(f.Input), int64(len(f.Input)))
			if err != nil {
				t.Fatal(err)
			}
			got, err := r.marshal()
			if err != nil || !bytes.Equal(got, f.Root) {
				t.Fatalf("lab root differs: %v", err)
			}
			id, err := b.finishRep(r)
			if err != nil {
				t.Fatal(err)
			}
			if err = b.flush(); err != nil {
				t.Fatal(err)
			}
			if err = b.proof(id, int64(len(f.Input)), sha256.Sum256(f.Input)); err != nil {
				t.Fatal(err)
			}
			reach, err := b.env.reachable(id)
			if err != nil {
				t.Fatal(err)
			}
			for id := range b.written {
				if _, ok := reach.Role[id]; !ok {
					t.Fatalf("hidden component %x", id)
				}
			}
		})
	}
}
