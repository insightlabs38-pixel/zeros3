package gitz3

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// spyAt is a ReaderAt that is not a slice from the parser's point of view and
// records whether any read reached beyond limit.
type spyAt struct {
	b     []byte
	limit int64
	oob   bool
	reads int
}

func (s *spyAt) ReadAt(p []byte, off int64) (int, error) {
	s.reads++
	if off+int64(len(p)) > s.limit {
		s.oob = true
	}
	if off >= int64(len(s.b)) {
		return 0, io.EOF
	}
	n := copy(p, s.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

type failWriter struct{ left int }

func (f *failWriter) Write(b []byte) (int, error) {
	if len(b) > f.left {
		return 0, errors.New("sink full")
	}
	f.left -= len(b)
	return len(b), nil
}

// The ReaderAt parser yields exactly the structure of the byte-backed wrapper:
// entries, trailer, resolved ids, objects and a byte-identical index.
func TestParsePackAtMatchesBytes(t *testing.T) {
	for _, a := range algos {
		for _, v := range []string{"ofs", "ref"} {
			name := fmt.Sprintf("%s-%s", a, v)
			data, idx := fx(t, name+".pack"), fx(t, name+".idx")
			want, err := ParsePack(data, a, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			spy := &spyAt{b: data, limit: int64(len(data))}
			got, err := ParsePackAt(spy, int64(len(data)), a, Limits{})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if spy.oob {
				t.Errorf("%s: a read went past the declared size", name)
			}
			if !reflect.DeepEqual(want.Entries, got.Entries) || !bytes.Equal(want.Trailer, got.Trailer) || got.Size != int64(len(data)) {
				t.Fatalf("%s: structure differs", name)
			}
			if _, err := got.Resolve(); err != nil {
				t.Fatal(err)
			}
			if _, err := want.Resolve(); err != nil {
				t.Fatal(err)
			}
			for i := range got.Entries {
				if !bytes.Equal(got.Entries[i].ID, want.Entries[i].ID) {
					t.Fatalf("%s: entry %d id", name, i)
				}
				pg, err1 := got.Object(i, nil)
				pw, err2 := want.Object(i, nil)
				if err1 != nil || err2 != nil || !bytes.Equal(pg, pw) {
					t.Fatalf("%s: entry %d object", name, i)
				}
			}
			bi, err := BuildIdx(got)
			if err != nil || !bytes.Equal(bi, idx) {
				t.Fatalf("%s: index built from the ReaderAt pack differs: %v", name, err)
			}
			// the pack is rewritten from ranges, exactly
			var out bytes.Buffer
			if err := got.WriteRange(&out, 0, got.Size); err != nil || !bytes.Equal(out.Bytes(), data) {
				t.Fatalf("%s: streamed range differs: %v", name, err)
			}
		}
	}
}

// A source shorter than its declared size, a declared size shorter than the
// source and a hostile size all fail safely.
func TestParsePackAtShortAndHostileSources(t *testing.T) {
	for _, a := range algos {
		data := fx(t, fmt.Sprintf("%s-ofs.pack", a))
		for _, cut := range []int{0, 5, 12, 40, len(data) / 2, len(data) - 1} {
			spy := &spyAt{b: data[:cut], limit: int64(len(data))}
			if _, err := ParsePackAt(spy, int64(len(data)), a, Limits{}); err == nil {
				t.Fatalf("%s: source cut to %d of %d accepted", a, cut, len(data))
			} else if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrChecksum) {
				t.Errorf("%s: cut %d: %v", a, cut, err)
			}
			if spy.oob {
				t.Errorf("%s: cut %d: read past declared size", a, cut)
			}
		}
		spy := &spyAt{b: data, limit: int64(len(data)) - 1}
		if _, err := ParsePackAt(spy, int64(len(data))-1, a, Limits{}); err == nil {
			t.Errorf("%s: pack declared one byte short accepted", a)
		}
		if spy.oob {
			t.Errorf("%s: read past a declared-short size", a)
		}
		for _, size := range []int64{-1, 0, 11, 1 << 62} {
			spy := &spyAt{b: data, limit: size}
			if _, err := ParsePackAt(spy, size, a, Limits{}); err == nil {
				t.Errorf("%s: size %d accepted", a, size)
			}
			if spy.reads > 0 && size <= 11 || spy.oob {
				t.Errorf("%s: size %d: %d reads, oob=%v", a, size, spy.reads, spy.oob)
			}
		}
		// a bad trailer is still caught
		bad := append([]byte(nil), data...)
		bad[len(bad)-1] ^= 1
		if _, err := ParsePackAt(bytes.NewReader(bad), int64(len(bad)), a, Limits{}); !errors.Is(err, ErrChecksum) {
			t.Errorf("%s: bad trailer: %v", a, err)
		}
	}
}

// Range access cannot leave the pack, and InflateTo streams what Inflate returns.
func TestPackRangesAndInflateTo(t *testing.T) {
	data := fx(t, "sha1-ofs.pack")
	p, err := ParsePack(data, SHA1, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range [][2]int64{{0, p.Size + 1}, {-1, 3}, {5, 4}, {p.Size, p.Size + 1}} {
		if _, err := p.Range(r[0], r[1]); err == nil {
			t.Errorf("range %v accepted", r)
		}
		if _, err := p.Bytes(r[0], r[1]); err == nil {
			t.Errorf("bytes %v accepted", r)
		}
		if err := p.WriteRange(io.Discard, r[0], r[1]); err == nil {
			t.Errorf("write range %v accepted", r)
		}
	}
	buf := make([]byte, 8)
	if n, err := p.ReadAt(buf, p.Size-3); n != 3 || err == nil {
		t.Errorf("ReadAt across the end: %d %v", n, err)
	}
	if err := p.InflateTo(-1, io.Discard); err == nil {
		t.Error("entry -1")
	}
	if err := p.InflateTo(len(p.Entries), io.Discard); err == nil {
		t.Error("entry past the end")
	}
	var total int64
	for i := range p.Entries {
		var w bytes.Buffer
		if err := p.InflateTo(i, &w); err != nil {
			t.Fatal(err)
		}
		in, err := p.Inflate(i)
		if err != nil || !bytes.Equal(in, w.Bytes()) {
			t.Fatalf("entry %d: Inflate differs from InflateTo", i)
		}
		total += int64(w.Len())
		if w.Len() > 1 {
			if err := p.InflateTo(i, &failWriter{left: w.Len() - 1}); err == nil {
				t.Errorf("entry %d: a failing sink was not reported", i)
			}
		}
		eb, _ := p.EntryBytes(i)
		hb, _ := p.HeaderBytes(i)
		zb, _ := p.ZlibBytes(i)
		if !bytes.Equal(eb, append(append([]byte(nil), hb...), zb...)) {
			t.Fatalf("entry %d: header+zlib != entry", i)
		}
	}
	if total == 0 {
		t.Fatal("no data inflated")
	}
}

// A representative large pack parses through a ReaderAt without a second
// whole-pack copy: total allocation stays a small fraction of the pack size.
func TestLargePackParsesWithoutSecondCopy(t *testing.T) {
	const objs, objSize = 64, 512 << 10
	entries := make([][]byte, objs)
	for i := range entries {
		payload := make([]byte, objSize)
		rand.Read(payload)
		var z bytes.Buffer
		zw, _ := zlib.NewWriterLevel(&z, zlib.NoCompression)
		zw.Write(payload)
		zw.Close()
		e := encodeEntryHeader(nil, 3, int64(len(payload)))
		entries[i] = append(e, z.Bytes()...)
	}
	data := mkPack(SHA1, objs, entries...)
	spy := &spyAt{b: data, limit: int64(len(data))}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	p, err := ParsePackAt(spy, int64(len(data)), SHA1, Limits{})
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entries) != objs || spy.oob {
		t.Fatalf("%d entries, oob=%v", len(p.Entries), spy.oob)
	}
	if alloc := int64(after.TotalAlloc - before.TotalAlloc); alloc > p.Size/4 {
		t.Errorf("parsing a %d byte pack allocated %d bytes", p.Size, alloc)
	}
}

// The reusable mechanism files must not depend on the laboratory layer.
func TestCoreMechanismImportsNoLab(t *testing.T) {
	for _, f := range []string{"gitz3.go", "objects.go", "delta.go", "pack.go", "idx.go"} {
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range af.Imports {
			if strings.Contains(im.Path.Value, "z3/lab") || strings.Contains(im.Path.Value, "z3/fstate") {
				t.Errorf("%s imports %s", f, im.Path.Value)
			}
		}
	}
}
