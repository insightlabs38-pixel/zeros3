package semcore

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func fixtures(t testing.TB) map[string][]byte {
	t.Helper()
	m := map[string][]byte{}
	for _, n := range []string{"tiny.safetensors", "tiny.gguf", "tiny.onnx", "tiny.pt"} {
		b, err := os.ReadFile(filepath.Join("..", "semcas", "testdata", n))
		if err != nil {
			t.Fatal(err)
		}
		m[n] = b
	}
	return m
}

// A ReaderAt-backed document tiles exactly like the in-memory one, and ingesting it yields the
// same recipe bytes while reading only segment-sized windows.
func TestStreamingEqualsInMemory(t *testing.T) {
	for name, data := range fixtures(t) {
		d1, err := Detect(name, data)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		d2, err := DetectAt(name, bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(d1.Segs, d2.Segs) {
			t.Fatalf("%s: segments differ", name)
		}
		s1, _ := NewStore(NewMemBackend())
		s2, _ := NewStore(NewMemBackend())
		r1, _, err := NewPlanner(s1, DefaultOptions()).Ingest(d1)
		if err != nil {
			t.Fatal(err)
		}
		r2, _, err := NewPlanner(s2, DefaultOptions()).Ingest(d2)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(r1.Marshal(), r2.Marshal()) {
			t.Fatalf("%s: streaming recipe differs", name)
		}
		var out bytes.Buffer
		if err := s2.Replay(r2, &out, 1<<20); err != nil || !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("%s: replay: %v", name, err)
		}
	}
}

type shortReader struct {
	b   []byte
	cut int64 // reads past this offset fail
}

func (s *shortReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(s.b)) {
		return 0, io.EOF
	}
	n := copy(p, s.b[off:])
	if off+int64(n) > s.cut {
		k := s.cut - off
		if k < 0 {
			k = 0
		}
		return int(k), io.ErrUnexpectedEOF
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// A source that dies mid-file must produce an error, never a recipe (no partial acceptance).
func TestStreamingShortReader(t *testing.T) {
	for name, data := range fixtures(t) {
		for _, cut := range []int64{0, 7, int64(len(data)) / 3, int64(len(data)) - 1} {
			sr := &shortReader{b: data, cut: cut}
			doc, err := DetectAt(name, sr, int64(len(data)))
			if err != nil {
				continue // failing closed at parse time is fine
			}
			s, _ := NewStore(NewMemBackend())
			if rec, _, err := NewPlanner(s, DefaultOptions()).Ingest(doc); err == nil {
				// a cut after every byte the file needs may legitimately succeed only if
				// the recipe still reproduces the file
				var out bytes.Buffer
				if err := s.Replay(rec, &out, 1<<20); err != nil || !bytes.Equal(out.Bytes(), data) {
					t.Fatalf("%s cut %d: accepted a recipe that does not reproduce the file", name, cut)
				}
			}
		}
	}
}

type failWriter struct{ n int }

func (w *failWriter) Write(p []byte) (int, error) {
	if w.n -= len(p); w.n < 0 {
		return 0, io.ErrShortWrite
	}
	return len(p), nil
}

type halfWriter struct{}

func (halfWriter) Write(p []byte) (int, error) { return len(p) / 2, nil }

func TestReplayShortAndFailingWriters(t *testing.T) {
	data := fixtures(t)["tiny.safetensors"]
	s, _ := NewStore(NewMemBackend())
	doc, _ := Detect("tiny.safetensors", data)
	rec, _, err := NewPlanner(s, DefaultOptions()).Ingest(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Replay(rec, &failWriter{n: len(data) / 2}, 1<<20); err == nil {
		t.Fatal("failing writer accepted")
	}
	if err := s.Replay(rec, halfWriter{}, 1<<20); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer: %v", err)
	}
}

// Candidate evaluation is concurrent but the result must not depend on the worker count.
func TestParallelPlannerDeterministic(t *testing.T) {
	r := rand.New(rand.NewSource(21))
	mk := func(n int) []byte { b := make([]byte, n); r.Read(b); return b }
	base := mk(2 * 128 * 96)
	wide, _ := convert(base, DBF16, DF32)
	half, _ := convert(wide, DF32, DF16)
	tr, _ := permute(base, 2, []uint64{128, 96}, []uint8{1, 0})
	files := [][]byte{
		stFile(vt{"a", "BF16", []int64{128, 96}, base}, vt{"b", "BF16", []int64{96, 128}, tr}),
		stFile(vt{"a", "F32", []int64{128, 96}, wide}),
		stFile(vt{"a", "F16", []int64{128, 96}, half}, vt{"x", "BF16", []int64{128, 96}, mk(2 * 128 * 96)}),
	}
	var snaps []string
	for _, par := range []int{1, 2, 4} {
		be := NewMemBackend()
		s, _ := NewStore(be)
		opt := DefaultOptions()
		opt.Parallel = par
		p := NewPlanner(s, opt)
		for i, f := range files {
			doc, err := DetectAt("f", bytes.NewReader(f), int64(len(f)))
			if err != nil {
				t.Fatal(err)
			}
			rec, _, err := p.Ingest(doc)
			if err != nil {
				t.Fatalf("file %d: %v", i, err)
			}
			s.PutRecipe(rec)
		}
		var keys []string
		_ = be.ListRecords(func(id ID, b []byte) error { keys = append(keys, string(id[:])+string(b)); return nil })
		sort.Strings(keys)
		h := ""
		for _, k := range keys {
			h += k
		}
		snaps = append(snaps, h)
	}
	if snaps[0] != snaps[1] || snaps[0] != snaps[2] {
		t.Fatal("store contents depend on the worker count")
	}
}

func FuzzDetectAt(f *testing.F) {
	for _, b := range fixtures(f) {
		f.Add(b)
	}
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, name := range []string{"x.onnx", "x.bin"} {
			doc, err := DetectAt(name, bytes.NewReader(b), int64(len(b)))
			if err != nil {
				continue
			}
			if err := doc.Check(); err != nil {
				t.Fatalf("accepted a document that does not tile: %v", err)
			}
		}
	})
}
