package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Mechanically reused frozen-wire proof harness; all five immutable managed roots retained.
func repZ4ManagedFixtureJSON(t testing.TB) ([]byte, error) {
	t.Helper()
	f, e := os.Open("testing-harnesses/z4/testdata/a4-managed/lab-managed.json.gz")
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
func TestRepZ4ManagedLabWireFidelity(t *testing.T) {
	raw, err := repZ4ManagedFixtureJSON(t)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 5 {
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

// Mechanically preserved immutable node framing for frozen encoder comparisons.
func (b *repBuilder) repZ4ManagedParts(k repKind, size int64, meta []byte, comps [][]byte) (*repObject, error) {
	r := &repObject{Kind: k, Size: size, Recipe: meta}
	if k != repKindDEX && k != repKindCLR {
		return nil, errRepUnsupported
	}
	if e := repManagedBounds(&repCall{Rep: r}); e != nil {
		return nil, e
	}
	var node []byte
	count := 0
	var total int64
	flush := func() error {
		if len(node) == 0 {
			return nil
		}
		id, e := b.putBlob(node)
		if e != nil {
			return e
		}
		r.Members = append(r.Members, repMember{Role: repStored, SHA: id, Size: int64(len(node))})
		node = nil
		count = 0
		return nil
	}
	for _, comp := range comps {
		n := int64(len(comp))
		if n <= 0 || n > 1<<20 || total > 128<<20-n {
			return nil, errRepLimit
		}
		total += n
		id, e := b.putBlob(comp)
		if e != nil {
			return nil, e
		}
		node = append(node, id[:]...)
		count++
		if (int(id[0])<<8|int(id[1]))%64 == 0 || count >= 256 {
			if e := flush(); e != nil {
				return nil, e
			}
		}
	}
	if e := flush(); e != nil {
		return nil, e
	}
	return r, nil
}

func TestRepZ4ManagedEncoderFidelity(t *testing.T) {
	raw, e := repZ4ManagedFixtureJSON(t)
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []repLabCodecFixture
	if e = json.Unmarshal(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			expected, e := unmarshalRep(f.Root, repProdLimits)
			if e != nil {
				t.Fatal(e)
			}
			var meta []byte
			var comps [][]byte
			emit := func(b []byte) error { comps = append(comps, append([]byte(nil), b...)); return nil }
			switch expected.Kind {
			case repKindDEX:
				parsed, e := repDXParse(bytes.NewReader(f.Input), int64(len(f.Input)), repDXLimits{})
				if e != nil {
					t.Fatal(e)
				}
				cfg := repDXStreamConfig{TempDir: t.TempDir(), Emit: emit, Cut: func(r io.ReaderAt, s []repDXSec) ([]int, error) { return repMCUnitCutAt(r, s, 4096) }}
				prefix := int(expected.Recipe[5])
				if prefix&128 != 0 {
					cfg.Auto = prefix & 127
				} else {
					cfg.K0 = prefix
				}
				result, e := repDXEncodeAt(parsed, bytes.NewReader(f.Input), cfg)
				if e != nil {
					t.Fatal(e)
				}
				meta = result.Meta
			case repKindCLR:
				parsed, e := repCLParse(bytes.NewReader(f.Input), int64(len(f.Input)), repCLLimits{})
				if e != nil {
					t.Fatal(e)
				}
				result, e := repCLEncodeAt(parsed, bytes.NewReader(f.Input), repCLStreamConfig{K0: int(expected.Recipe[4]), Auto: int(expected.Recipe[5]), TempDir: t.TempDir(), Emit: emit, Cut: func(r io.ReaderAt, s []repIXSec) ([]int, error) { return repMCUnitCutAt(r, s, 4096) }})
				if e != nil {
					t.Fatal(e)
				}
				meta = result.Meta
			default:
				t.Fatal("unexpected frozen kind", expected.Kind)
			}
			l := newRepLab(t, repModePrefer)
			b := l.s.newRepBuilder(int64(len(f.Input)), nil, nil)
			defer b.close()
			rep, e := b.repZ4ManagedParts(expected.Kind, int64(len(f.Input)), meta, comps)
			if e != nil {
				t.Fatal(e)
			}
			wire, e := rep.marshal()
			if e != nil || !bytes.Equal(wire, f.Root) {
				t.Fatalf("frozen root changed: %v", e)
			}
			id, e := b.finishRep(rep)
			if e != nil {
				t.Fatal(e)
			}
			if e = b.flush(); e != nil {
				t.Fatal(e)
			}
			if e = b.proof(id, int64(len(f.Input)), sha256.Sum256(f.Input)); e != nil {
				t.Fatal(e)
			}
			reach, e := b.env.reachable(id)
			if e != nil {
				t.Fatal(e)
			}
			for id := range b.written {
				if _, ok := reach.Role[id]; !ok {
					t.Fatal("unprotected write", id)
				}
			}
		})
	}
}

func TestRepZ4ManagedLifecycleAndCapability(t *testing.T) {
	raw, e := repZ4ManagedFixtureJSON(t)
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []repLabCodecFixture
	if e = json.Unmarshal(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	l := newRepLab(t, repModePrefer)
	for _, f := range fixtures {
		l.put(f.Name, f.Input)
		entry, man := l.manifest(f.Name)
		root := repMustRoot(t, l.s, entry.manifestUUID)
		source := l.s.newRepSource(root.Descs, nil)
		env := &repEnv{Src: source, Limits: repProdLimits}
		call, e := env.open(root.Rep)
		if e != nil {
			t.Fatal(e)
		}
		if call.Rep.Kind != repKindDEX && call.Rep.Kind != repKindCLR {
			t.Fatal("wrong managed kind", call.Rep.Kind)
		}
		source.close()
		for _, mode := range []repMode{repModeOff, repModeShadow, repModePrefer} {
			l.s.setRepMode(mode)
			for _, r := range []byteRange{{0, int64(len(f.Input)) - 1}, {0, 3}, {int64(len(f.Input)) / 2, int64(len(f.Input))/2 + 31}, {int64(len(f.Input)) - 5, int64(len(f.Input)) - 1}} {
				got := repReadAll(t, l.s, man, r)
				if !bytes.Equal(got, f.Input[r.start:r.end+1]) {
					t.Fatal("mode/range mismatch", mode, r)
				}
			}
		}
		l.s.setRepMode(repModePrefer)
	}
	if l.s.format.StoreFormatVersion != storeFormatVersionManaged {
		t.Fatal("managed capability floor", l.s.format.StoreFormatVersion)
	}
	l.exact("managed initial")
	l.offline(func() {
		if _, e := compactStore(l.dir, repSmallCompact()); e != nil {
			t.Fatal(e)
		}
	})
	l.exact("managed compact")
	l.offline(func() {
		if _, e := gcCollect(l.dir, true); e != nil {
			t.Fatal(e)
		}
	})
	l.exact("managed GC")
	l.close()
	l.open()
	l.exact("managed restart")
	for _, pattern := range []string{"zeros3-refs-*", "zeros3-sorted-refs-*", "zeros3-ref-run-*"} {
		names, e := filepath.Glob(filepath.Join(l.dir, "tmp", pattern))
		if e != nil || len(names) != 0 {
			t.Fatal("staging leak", names, e)
		}
	}
}

func TestRepZ4ManagedDefaultAndMalformedFallback(t *testing.T) {
	raw, e := repZ4ManagedFixtureJSON(t)
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []repLabCodecFixture
	if e = json.Unmarshal(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	off := newRepLab(t, repModeOff)
	for _, f := range fixtures {
		off.put(f.Name, f.Input)
		entry, _ := off.manifest(f.Name)
		if repHasRoot(off.s, entry.manifestUUID) {
			t.Fatal("creation enabled by default")
		}
	}
	if off.s.format.StoreFormatVersion >= storeFormatVersionManaged {
		t.Fatal("off mode raised managed capability")
	}
	off.exact("managed disabled")
	on := newRepLab(t, repModePrefer)
	for i, data := range [][]byte{[]byte("MZ malformed PE"), []byte("dex\n malformed DEX")} {
		key := fmt.Sprintf("bad-%d", i)
		on.put(key, data)
		entry, _ := on.manifest(key)
		if repHasRoot(on.s, entry.manifestUUID) {
			t.Fatal("malformed managed root published")
		}
	}
	if on.s.format.StoreFormatVersion >= storeFormatVersionManaged {
		t.Fatal("fallback raised managed capability")
	}
	on.exact("malformed fallback")
}

func TestRepManagedOrdinaryPutCrashFloor(t *testing.T) {
	raw, err := repZ4ManagedFixtureJSON(t)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []repKind{repKindDEX, repKindCLR} {
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
				want := 9
				if point == hookRepRootBeforeFormat {
					want = 1
				}
				if l.s.format.StoreFormatVersion != want {
					t.Fatalf("floor %d, want %d", l.s.format.StoreFormatVersion, want)
				}
				if _, _, e := l.s.GetObject("b", "new"); e == nil {
					t.Fatal("uncommitted managed object visible")
				}
				l.exact("managed PUT crash")
				l.offline(func() {
					if _, e := gcCollect(l.dir, true); e != nil {
						t.Fatal(e)
					}
				})
				if l.s.format.StoreFormatVersion != want {
					t.Fatal("GC downgraded floor")
				}
				l.put("new", data)
				l.exact("managed retry")
			})
		}
	}
}

func FuzzRepZ4ManagedDecode(f *testing.F) {
	raw, err := repZ4ManagedFixtureJSON(f)
	if err != nil {
		f.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		f.Fatal(err)
	}
	for i, x := range fixtures {
		f.Add(x.Root, uint8(i))
	}
	f.Fuzz(func(t *testing.T, data []byte, pick uint8) {
		if len(data) > 1<<20 {
			return
		}
		fixture := fixtures[int(pick)%len(fixtures)]
		src := newRepMem()
		for _, object := range fixture.Objects {
			src.put(object)
		}
		root := src.put(data)
		env := &repEnv{Src: src, Limits: repLimits{MaxDepth: 4, MaxMembers: 4096, MaxRepBytes: 1 << 20, MaxWhole: 2 << 20, MaxObjects: 4096}}
		env.reconstruct(root, io.Discard)
		env.reachable(root)
	})
}

// Exercise every frozen dependency, not just a selected convenient component.
func TestRepManagedDependencyAndWriterFailures(t *testing.T) {
	raw, e := repZ4ManagedFixtureJSON(t)
	if e != nil {
		t.Fatal(e)
	}
	var fs []repLabCodecFixture
	if e = json.Unmarshal(raw, &fs); e != nil {
		t.Fatal(e)
	}
	for _, f := range fs {
		t.Run(f.Name, func(t *testing.T) {
			src := newRepMem()
			for _, b := range f.Objects {
				src.put(b)
			}
			root := src.put(f.Root)
			for id, original := range src.objs {
				for _, mode := range []string{"missing", "corrupt", "truncated"} {
					switch mode {
					case "missing":
						delete(src.objs, id)
					case "corrupt":
						b := append([]byte(nil), original...)
						if len(b) > 0 {
							b[len(b)/2] ^= 1
						}
						src.objs[id] = b
					case "truncated":
						if len(original) > 0 {
							src.objs[id] = original[:len(original)-1]
						}
					}
					env := &repEnv{Src: src, Limits: repProdLimits}
					if _, _, e := env.reconstruct(root, io.Discard); e == nil {
						t.Fatalf("accepted %s dependency %x", mode, id)
					}
					src.objs[id] = original
				}
			}
			env := &repEnv{Src: src, Limits: repProdLimits}
			if _, _, e := env.reconstruct(root, repZ4ModelShortWriter{}); e == nil {
				t.Fatal("accepted short writer")
			}
		})
	}
}

func TestRepManagedCumulativeCompatibility(t *testing.T) {
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
	raw, e = repZ4ManagedFixtureJSON(t)
	if e != nil {
		t.Fatal(e)
	}
	// The lab retains Input slices as its expected bytes; JSON reuse would overwrite them.
	fs = nil
	if e = json.Unmarshal(raw, &fs); e != nil {
		t.Fatal(e)
	}
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
	if !seen[repKindDEX] || !seen[repKindCLR] || l.s.format.StoreFormatVersion != 9 {
		t.Fatal("missing cumulative managed floor")
	}
	l.exact("Model plus SST plus Parquet plus DEX plus CLR")
	l.close()
	l.open()
	l.exact("cumulative decoder restart")
}
