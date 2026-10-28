package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func repZ4ModelFixtureJSON(t testing.TB) ([]byte, error) {
	t.Helper()
	f, e := os.Open("testing-harnesses/z4/testdata/a2-model/lab-i2.json.gz")
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
func TestRepZ4ModelLabWireFidelity(t *testing.T) {
	raw, err := repZ4ModelFixtureJSON(t)
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 3 {
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

func repZ4ModelSafetensors(t testing.TB, data []byte) []byte {
	t.Helper()
	header := fmt.Sprintf(`{"weights":{"dtype":"BF16","shape":[%d],"data_offsets":[0,%d]}}`, len(data)/2, len(data))
	out := binary.LittleEndian.AppendUint64(nil, uint64(len(header)))
	out = append(out, header...)
	return append(out, data...)
}
func TestRepZ4ModelStreamingModelLifecycle(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	plain := bytes.Repeat([]byte{0, 60, 1, 60, 2, 60, 3, 60}, 1<<16)
	for rev := 0; rev < 5; rev++ {
		plain[rev*23] ^= byte(rev + 1)
		data := repZ4ModelSafetensors(t, plain)
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

func TestRepZ4ModelGGUFAndUnsupportedModel(t *testing.T) {
	data, e := os.ReadFile("testing-harnesses/z4/testdata/a2-model/model.gguf")
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
func TestRepZ4ModelModelGiBBounded(t *testing.T) {
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

func TestRepZ4ModelBaseSurvivesRootRetirement(t *testing.T) {
	l := newRepLab(t, repModePrefer)
	plain := bytes.Repeat([]byte{0, 60, 1, 60}, 1<<16)
	l.put("base", repZ4ModelSafetensors(t, plain))
	baseEntry, _ := l.manifest("base")
	baseRoot := repMustRoot(t, l.s, baseEntry.manifestUUID)
	plain[111] ^= 3
	l.put("derived", repZ4ModelSafetensors(t, plain))
	entry, _ := l.manifest("derived")
	derivedRoot := repMustRoot(t, l.s, entry.manifestUUID)
	src := l.s.newRepSource(derivedRoot.Descs, nil)
	env := &repEnv{Src: src, Limits: repProdLimits}
	call, e := env.open(derivedRoot.Rep)
	if e != nil {
		t.Fatal(e)
	}
	var tensor *repCall
	for i, m := range call.Rep.Members {
		if m.Role == repDerived {
			tensor, e = call.child(i)
			if e != nil {
				t.Fatal(e)
			}
			break
		}
	}
	if tensor == nil {
		t.Fatal("missing tensor")
	}
	h, e := repModelHops(tensor)
	if e != nil || h != 2 {
		t.Fatalf("expected actual base dependency, hops%d %v", h, e)
	}
	reach, e := env.reachable(derivedRoot.Rep)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := reach.Role[baseRoot.Rep]; ok {
		t.Fatal("derived tensor depends on the entire earlier object")
	}
	src.close()
	if e := l.s.DeleteObject("b", "base"); e != nil {
		t.Fatal(e)
	}
	delete(l.want, "base")
	l.offline(func() {
		zero := 0
		if _, e := pruneHistoryStore(l.dir, historyPruneOptions{Bucket: "b", Key: "base", KeepLast: &zero}, true); e != nil {
			t.Fatal(e)
		}
		if _, e := gcCollect(l.dir, true); e != nil {
			t.Fatal(e)
		}
	})
	l.exact("base object retired, derived tensor retained")
	l.offline(func() {
		if _, e := repackStore(l.dir, repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100, Compress: true}); e != nil {
			t.Fatal(e)
		}
	})
	l.exact("base dependencies repacked")
}

func FuzzRepZ4ModelDecode(f *testing.F) {
	raw, err := repZ4ModelFixtureJSON(f)
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

type repZ4ModelShortWriter struct{}

func (repZ4ModelShortWriter) Write([]byte) (int, error) { return 0, nil }
func TestRepZ4ModelShortWriterAndAmbiguousNames(t *testing.T) {
	src := newRepMem()
	plain := []byte{0, 1, 2, 3}
	id := src.put(repMDPlaneChunks(plain, 2, 2))
	r := &repObject{Kind: repKindModel, Size: 4, Members: []repMember{{Role: repStored, SHA: id, Size: 4}}, Recipe: repMDBuildModelRecipe([]repMDModelOp{{Tag: repMDopPlanes, Res: 0, W: 2, E: 2}})}
	env := &repEnv{Src: src, Limits: repProdLimits}
	c := env.call([32]byte{}, r, 1, nil, repZ4ModelShortWriter{})
	if e := (repMDmodelDecoder{}).Decode(c); !errors.Is(e, io.ErrShortWrite) {
		t.Fatalf("short writer: %v", e)
	}
	r.Size = 8
	r.Recipe = repMDBuildModelRecipe([]repMDModelOp{{Tag: repMDopCopy, M: 0, Name: "same"}, {Tag: repMDopCopy, M: 0, Name: "same"}})
	if _, e := repMDModelNames(c); !errors.Is(e, errRepAmbiguous) {
		t.Fatalf("ambiguous tensor names: %v", e)
	}
}
