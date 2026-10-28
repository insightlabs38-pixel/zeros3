package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func repI2Safetensors(t testing.TB, data []byte) []byte {
	t.Helper()
	header := fmt.Sprintf(`{"weights":{"dtype":"BF16","shape":[%d],"data_offsets":[0,%d]}}`, len(data)/2, len(data))
	out := binary.LittleEndian.AppendUint64(nil, uint64(len(header)))
	out = append(out, header...)
	return append(out, data...)
}
func TestRepI2StreamingModelLifecycle(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	plain := bytes.Repeat([]byte{0, 60, 1, 60, 2, 60, 3, 60}, 1<<16)
	for rev := 0; rev < 5; rev++ {
		plain[rev*23] ^= byte(rev + 1)
		data := repI2Safetensors(t, plain)
		l.put("weights", data)
		entry, _ := l.manifest("weights")
		root := repMustRoot(t, l.s, entry.manifestUUID)
		src := l.s.newRepSource(root.Descs, nil)
		env := &repEnv{Src: src, Limits: repProdLimits}
		call, e := env.open(root.Rep)
		if e != nil {
			t.Fatal(e)
		}
		if call.Rep.Kind != repKindModel {
			t.Fatal("not model")
		}
		names, e := repMDModelNames(call)
		if e != nil || len(names) != 1 {
			t.Fatalf("names %v %v", names, e)
		}
		loc := names["weights"]
		got, e := repMDReadModelRange(call, loc[0]+5, 7001)
		if e != nil || !bytes.Equal(got, plain[5:7006]) {
			t.Fatalf("range: %v", e)
		}
		for i, m := range call.Rep.Members {
			if m.Role != repDerived {
				continue
			}
			ch, e := call.child(i)
			if e != nil {
				t.Fatal(e)
			}
			h, e := repModelHops(ch)
			if e != nil || h > 3 || (rev == 2 && h != 3) {
				t.Fatalf("hops%d %v", h, e)
			}
			t.Logf("revision%d tensor hops%d", rev, h)
		}
		src.close()
		l.exact("model revision")
	}
	l.close()
	l.open()
	l.exact("model restart")
}

func TestRepI2GGUFAndUnsupportedModel(t *testing.T) {
	data, e := os.ReadFile("testing-harnesses/testdata/i2-draft/model.gguf")
	if e != nil {
		t.Fatal(e)
	}
	l := newRepLab(t, repModePrefer)
	l.put("gguf", data)
	entry, _ := l.manifest("gguf")
	root := repMustRoot(t, l.s, entry.manifestUUID)
	src := l.s.newRepSource(root.Descs, nil)
	defer src.close()
	env := &repEnv{Src: src, Limits: repProdLimits}
	call, e := env.open(root.Rep)
	if e != nil || call.Rep.Kind != repKindModel {
		t.Fatalf("model kind: %v", e)
	}
	names, e := repMDModelNames(call)
	if e != nil || len(names) != 2 {
		t.Fatalf("names %v %v", names, e)
	}
	for _, loc := range names {
		got, e := repMDReadModelRange(call, loc[0]+3, min(80, loc[1]-3))
		if e != nil || !bytes.Equal(got, data[loc[0]+3:loc[0]+3+int64(len(got))]) {
			t.Fatal(e)
		}
	}
	l.exact("gguf")
	bad := append([]byte(nil), data...)
	binary.LittleEndian.PutUint32(bad[4:8], 99)
	l.put("unsupported", bad)
	entry, _ = l.manifest("unsupported")
	if repHasRoot(l.s, entry.manifestUUID) {
		t.Fatal("unknown GGUF version represented")
	}
	l.exact("unknown version CDC fallback")
}

// A sparse but chunk-distinct source tests scale without retaining a GiB []byte.
// This is a memory control, not model-storage or real-training economics.
func TestRepI2ModelGiBBounded(t *testing.T) {
	const size = int64(1 << 30)
	header := ""
	for i := 0; i < 4; i++ {
		payload := size - 8 - int64(len(header))
		header = fmt.Sprintf(`{"weights":{"dtype":"BF16","shape":[%d],"data_offsets":[0,%d]}}`, payload/2, payload)
		if len(header)%2 != 0 {
			header += " "
		}
	}
	payload := size - 8 - int64(len(header))
	if payload%2 != 0 {
		t.Fatal("alignment")
	}
	prefix := binary.LittleEndian.AppendUint64(nil, uint64(len(header)))
	prefix = append(prefix, header...)
	f, e := os.Create(filepath.Join(t.TempDir(), "bounded.safetensors"))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if e := f.Truncate(size); e != nil {
		t.Fatal(e)
	}
	if _, e := f.WriteAt(prefix, 0); e != nil {
		t.Fatal(e)
	}
	for off := int64(256 << 10); off < size-8; off += 256 << 10 {
		var marker [8]byte
		binary.LittleEndian.PutUint64(marker[:], uint64(off))
		if _, e := f.WriteAt(marker[:], off); e != nil {
			t.Fatal(e)
		}
	}
	h := sha256.New()
	if _, e := io.Copy(h, io.NewSectionReader(f, 0, size)); e != nil {
		t.Fatal(e)
	}
	var want [32]byte
	h.Sum(want[:0])
	l := newRepLab(t, repModePrefer)
	b := l.s.newRepBuilder(size, nil, nil)
	defer b.close()
	start := time.Now()
	peak := peakHeapGrowth(func() {
		r, e := b.encodeModel(f, size)
		if e != nil {
			t.Fatal(e)
		}
		id, e := b.finishRep(r)
		if e != nil {
			t.Fatal(e)
		}
		if e := b.flush(); e != nil {
			t.Fatal(e)
		}
		if e := b.proof(id, size, want); e != nil {
			t.Fatal(e)
		}
		reach, e := b.env.reachable(id)
		if e != nil || len(reach.Order) < 3 {
			t.Fatalf("reachability: %v", e)
		}
	})
	t.Logf("GiB model analysis+serialized proof: peak heap growth %.2fMiB, elapsed %s", float64(peak)/(1<<20), time.Since(start).Round(time.Millisecond))
	if peak > 192<<20 {
		t.Fatalf("model analyzer heap grew %d bytes", peak)
	}
}

func TestRepI2ManagedEncoderFidelity(t *testing.T) {
	raw, e := repI2FixtureJSON(t)
	if e != nil {
		t.Fatal(e)
	}
	var fixtures []repLabCodecFixture
	if e := json.Unmarshal(raw, &fixtures); e != nil {
		t.Fatal(e)
	}
	for _, f := range fixtures {
		if !strings.HasSuffix(f.Name, ".dex") && !strings.HasSuffix(f.Name, ".dll") {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			l := newRepLab(t, repModePrefer)
			b := l.s.newRepBuilder(int64(len(f.Input)), nil, nil)
			defer b.close()
			kind := repKindCLR
			var meta []byte
			var comps [][]byte
			if strings.HasSuffix(f.Name, ".dex") {
				kind = repKindDEX
				p, e := repDXParse(bytes.NewReader(f.Input), int64(len(f.Input)), repDXLimits{MaxFile: 32 << 20})
				if e != nil {
					t.Fatal(e)
				}
				r, e := repDXEncode(p, f.Input, repDXConfig{K0: 3, Cut: repManagedUnitCutBytes(4096)})
				if e != nil {
					t.Fatal(e)
				}
				meta, comps = r.Meta, r.Comps
			} else {
				p, e := repCLParse(bytes.NewReader(f.Input), int64(len(f.Input)), repCLLimits{})
				if e != nil {
					t.Fatal(e)
				}
				r, e := repCLEncode(p, f.Input, repCLConfig{Auto: 1, Cut: repManagedUnitCutBytes(4096)})
				if e != nil {
					t.Fatal(e)
				}
				meta, comps = r.Meta, r.Comps
			}
			r, e := b.managedParts(kind, int64(len(f.Input)), meta, comps)
			if e != nil {
				t.Fatal(e)
			}
			root, e := r.marshal()
			if e != nil || !bytes.Equal(root, f.Root) {
				t.Fatalf("lab envelope/recipe/groups differ: %v", e)
			}
			id, e := b.finishRep(r)
			if e != nil {
				t.Fatal(e)
			}
			if e := b.flush(); e != nil {
				t.Fatal(e)
			}
			if e := b.proof(id, int64(len(f.Input)), sha256.Sum256(f.Input)); e != nil {
				t.Fatal(e)
			}
			reach, e := b.env.reachable(id)
			if e != nil {
				t.Fatal(e)
			}
			for id := range b.written {
				if _, ok := reach.Role[id]; !ok {
					t.Fatal("hidden write")
				}
			}
			// The ordinary object path deliberately keeps managed creation as CDC.
			l.put("managed", f.Input)
			entry, _ := l.manifest("managed")
			if repHasRoot(l.s, entry.manifestUUID) {
				t.Fatal("managed creation enabled")
			}
		})
	}
}
