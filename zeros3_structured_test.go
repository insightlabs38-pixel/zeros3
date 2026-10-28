package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"strings"
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

// Unsupported producer features must preserve the authoritative CDC path.
func TestRepStructuredFallbackAndLifecycle(t *testing.T) {
	for _, path := range []string{"testing-harnesses/z3/sstz3/testdata/pebble-v7.sst", "testing-harnesses/z3/parquetz3/testdata/pa_gzip.parquet", "testing-harnesses/z3/parquetz3/testdata/pa_nested.parquet"} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			l := newRepLab(t, repModePrefer)
			l.put("unsupported", data)
			entry, _ := l.manifest("unsupported")
			root, err := l.s.loadRepRoot(entry.manifestUUID)
			if strings.HasSuffix(path, "pa_nested.parquet") {
				if err != nil || root == nil {
					t.Fatalf("mixed opaque fixture lost root: %v", err)
				}
				env := &repEnv{Src: l.s.newRepSource(root.Descs, nil), Limits: repProdLimits}
				call, e := env.open(root.Rep)
				if e != nil {
					t.Fatal(e)
				}
				rec, e := repSCparquetRecipe(call)
				if e != nil {
					t.Fatal(e)
				}
				pr, e := repPQParseRecipe(rec, repStructuredParquetLimits())
				if e != nil {
					t.Fatal(e)
				}
				opaque := false
				for _, seg := range pr.Segs {
					if seg.Kind == repPQSegOpaque {
						opaque = true
					}
				}
				if !opaque {
					t.Fatal("nested payload not preserved opaque")
				}
			} else {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("want absent sidecar: %v", err)
				}
				if root != nil {
					t.Fatal("unsupported input unexpectedly represented")
				}
			}

			l.exact("fallback")
		})
	}
	raw, err := os.ReadFile("testing-harnesses/testdata/i3-draft/lab-structured.json")
	if err != nil {
		t.Fatal(err)
	}
	var fs []repLabCodecFixture
	if err = json.Unmarshal(raw, &fs); err != nil {
		t.Fatal(err)
	}
	l := newRepLab(t, repModePrefer)
	for _, f := range fs {
		l.put(f.Name, f.Input)
		entry, _ := l.manifest(f.Name)
		root, err := l.s.loadRepRoot(entry.manifestUUID)
		if err != nil || root == nil {
			t.Fatalf("missing structured root: %v", err)
		}
	}
	l.exact("native kinds")
	l.close()
	l.open()
	l.exact("restart")
}

type repStructuredNoRead struct{}

func (repStructuredNoRead) ReadAt([]byte, int64) (int, error) { panic("limit must precede input read") }
func TestRepStructuredPreallocationAndRecipeGuards(t *testing.T) {
	e := &repPQencoder{r: repStructuredNoRead{}, opt: &repPQOptions{Limits: repStructuredParquetLimits()}}
	if _, err := e.read(0, 1<<30); !errors.Is(err, repPQErrLimit) {
		t.Fatalf("large framing: %v", err)
	}
	d := &repPQdecoder{lim: repPQLimits{MaxPage: 32}}
	cs := &repPQcolState{spec: repPQcolSpec{Phys: repPQTInt32}, v: &repPQcursor{grp: make([]byte, 36), parse: repPQparseFixed(4), have: true}}
	if _, err := d.readValues(cs, 9); !errors.Is(err, repPQErrLimit) {
		t.Fatalf("canonical expansion: %v", err)
	}
	raw, err := os.ReadFile("testing-harnesses/testdata/i3-draft/lab-structured.json")
	if err != nil {
		t.Fatal(err)
	}
	var fs []repLabCodecFixture
	if err = json.Unmarshal(raw, &fs); err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		r, err := unmarshalRep(f.Root, repProdLimits)
		if err != nil {
			t.Fatal(err)
		}
		r.Size++
		call := &repCall{Rep: r}
		if r.Kind == repKindSST {
			_, err = repSCsstRecipe(call)
		} else {
			_, err = repSCparquetRecipe(call)
		}
		if !errors.Is(err, errRepCorrupt) {
			t.Fatalf("mismatched outer size %s: %v", f.Name, err)
		}
	}
}

func FuzzRepStructuredRecipes(f *testing.F) {
	raw, err := os.ReadFile("testing-harnesses/testdata/i3-draft/lab-structured.json")
	if err != nil {
		f.Fatal(err)
	}
	var fs []repLabCodecFixture
	if err = json.Unmarshal(raw, &fs); err != nil {
		f.Fatal(err)
	}
	for _, v := range fs {
		f.Add(v.Root)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			return
		}
		r, err := unmarshalRep(data, repProdLimits)
		if err != nil {
			return
		}
		c := &repCall{Rep: r}
		switch r.Kind {
		case repKindSST:
			_, _ = repSCsstRecipe(c)
		case repKindParquet:
			_, _ = repSCparquetRecipe(c)
		}
	})
}
