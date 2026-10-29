package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestRepStructuredLabWireFidelity(t *testing.T) {
	raw, err := os.ReadFile("testing-harnesses/z4/testdata/a3-structured/lab-structured.json")
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
	raw, err := os.ReadFile("testing-harnesses/z4/testdata/a3-structured/lab-structured.json")
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
	raw, err := os.ReadFile("testing-harnesses/z4/testdata/a3-structured/lab-structured.json")
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
	raw, err := os.ReadFile("testing-harnesses/z4/testdata/a3-structured/lab-structured.json")
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
	raw, err := os.ReadFile("testing-harnesses/z4/testdata/a3-structured/lab-structured.json")
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

func TestRepStructuredPrefixExpansionBudget(t *testing.T) {
	var bw repSSBlockWriter
	key := bytes.Repeat([]byte{'k'}, 1<<20)
	for i := 0; i < 20; i++ {
		key[len(key)-9] = byte(i)
		if err := bw.Add(key, nil, i == 0, -1); err != nil {
			t.Fatal(err)
		}
	}
	block := bw.Finish()
	if len(block) > 2<<20 {
		t.Fatalf("fixture did not share prefixes: %d", len(block))
	}
	var keys []byte
	var err error
	_, err = repSSWalkBlock(block, repStructuredSSTLimits(), func(_ int, key, val []byte) error { keys, err = repStructuredAppendKey(keys, key, 16<<20); return err })
	if !errors.Is(err, repSSErrLimit) {
		t.Fatalf("prefix expansion did not hit byte budget: %v", err)
	}
}

func TestRepStructuredZstdWindowBeforeAllocation(t *testing.T) {
	// A valid frame header declaring a 128 MiB window; no payload is needed to
	// reject it under the embedded page's policy before allocating history.
	payload := []byte{0x28, 0xb5, 0x2f, 0xfd, 0, 0x88}
	c := repPQNewCompressor(repPQCodecZstd, repStructuredParquetLimits())
	_, _, err := c.Analyze(payload, 1)
	if err == nil || !strings.Contains(err.Error(), "window size") {
		t.Fatalf("embedded frame bound: %v", err)
	}
}

func TestRepStructuredOrdinaryPutCrashFloor(t *testing.T) {
	raw, err := os.ReadFile("testing-harnesses/z4/testdata/a3-structured/lab-structured.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []repKind{repKindSST, repKindParquet} {
		var data []byte
		for _, f := range fixtures {
			r, e := unmarshalRep(f.Root, repProdLimits)
			if e != nil {
				t.Fatal(e)
			}
			if r.Kind == kind {
				data = f.Input
				break
			}
		}
		if data == nil {
			t.Fatal("missing frozen kind")
		}
		for _, point := range []string{hookRepRootBeforeFormat, hookRepRootAfterFormat, hookRepRootBeforeRename, hookRepRootAfterRename, hookRepBeforeNamespace} {
			t.Run(fmt.Sprintf("kind%d/%s", kind, point), func(t *testing.T) {
				l := newRepLab(t, repModePrefer)
				l.put("old", []byte("prior authoritative bytes"))
				withTestHook(t, func(p string) {
					if p == point {
						panic(simulatedCrash{point: p})
					}
				})
				runExpectingSimulatedCrash(t, func() { _, _ = l.s.PutObject("b", "new", data, "x", nil) })
				testHook = nil
				l.close()
				l.open()
				want := 8
				if point == hookRepRootBeforeFormat {
					want = 1
				}
				if l.s.format.StoreFormatVersion != want {
					t.Fatalf("floor %d, want %d", l.s.format.StoreFormatVersion, want)
				}
				if _, _, e := l.s.GetObject("b", "new"); e == nil {
					t.Fatal("uncommitted structured object visible")
				}
				l.exact("structured PUT crash")
				l.offline(func() {
					if _, e := gcCollect(l.dir, true); e != nil {
						t.Fatal(e)
					}
				})
				if l.s.format.StoreFormatVersion != want {
					t.Fatal("GC downgraded floor")
				}
				l.put("new", data)
				l.exact("structured retry")
			})
		}
	}
}

func TestRepStructuredCumulativeModelCompatibility(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	l.put("model", repZ4ModelSafetensors(t, bytes.Repeat([]byte{1, 2, 3, 4}, 16384)))
	if l.s.format.StoreFormatVersion != 7 {
		t.Fatal("Model floor before structured integration")
	}
	raw, e := os.ReadFile("testing-harnesses/z4/testdata/a3-structured/lab-structured.json")
	if e != nil {
		t.Fatal(e)
	}
	var fs []repLabCodecFixture
	if e = json.Unmarshal(raw, &fs); e != nil {
		t.Fatal(e)
	}
	seen := map[repKind]bool{}
	for _, f := range fs {
		r, e := unmarshalRep(f.Root, repProdLimits)
		if e != nil {
			t.Fatal(e)
		}
		if seen[r.Kind] {
			continue
		}
		seen[r.Kind] = true
		l.put(fmt.Sprintf("kind-%d", r.Kind), f.Input)
	}
	if !seen[repKindSST] || !seen[repKindParquet] || l.s.format.StoreFormatVersion != 8 {
		t.Fatal("missing cumulative structured floor")
	}
	l.exact("Model plus SST plus Parquet")
	l.close()
	l.open()
	l.exact("cumulative decoder restart")
}

func TestRepStructuredNodeItemTotalOverflow(t *testing.T) {
	nodes := make([]repPQnodeMember, 16)
	for i := range nodes {
		nodes[i] = repPQnodeMember{member: 0, items: 1 << 60}
	}
	rec := &repPQRecipe{Cols: []repPQColRec{{Phys: repPQTInt32, HasV: true, Vm: nodes}}, Digests: []repPQID{{}}}
	raw, _ := rec.Marshal()
	if _, err := repPQParseRecipe(raw, repStructuredParquetLimits()); err == nil {
		t.Fatal("wrapped 2^64 stream-item total accepted as zero")
	}
}
