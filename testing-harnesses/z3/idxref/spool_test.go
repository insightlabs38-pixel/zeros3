package idxref

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestRefSpoolTwoPassLimitsAndCleanup(t *testing.T) {
	dir := t.TempDir()
	s, e := NewRefSpool(dir, 1024)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Next(); e == nil {
		t.Fatal("read before rewind")
	}
	if e = s.Append(-1, 0, 0, nil); e == nil {
		t.Fatal("negative position")
	}
	for _, x := range []SpoolRef{{0, 2, 3, []byte{1, 2, 3}}, {0, 1, 1, nil}, {99, 4, 2, []byte{4}}} {
		if e = s.Append(x.Pos, x.Stream, x.Class, x.Bytes); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Append(4, 0, 0, nil); e == nil {
		t.Fatal("descending position")
	}
	for pass := 0; pass < 2; pass++ {
		if e = s.Rewind(); e != nil {
			t.Fatal(e)
		}
		for _, x := range []SpoolRef{{0, 2, 3, []byte{1, 2, 3}}, {0, 1, 1, nil}, {99, 4, 2, []byte{4}}} {
			y, e := s.Next()
			if e != nil || x.Pos != y.Pos || x.Stream != y.Stream || x.Class != y.Class || !bytes.Equal(x.Bytes, y.Bytes) {
				t.Fatal("record", y, e)
			}
		}
		if _, e = s.Next(); e != io.EOF {
			t.Fatal("EOF", e)
		}
	}
	if e = s.Append(100, 0, 0, nil); e == nil {
		t.Fatal("append after rewind")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 0 {
		t.Fatal("leak", e)
	}
	s, e = NewRefSpool(dir, 13)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Append(0, 0, 0, nil); e == nil {
		t.Fatal("record budget")
	}
}
func TestSortedRefSpoolPreservesStableTieOrder(t *testing.T) {
	dir := t.TempDir()
	a, e := NewSortedRefSpool(dir, 4, 16)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := NewRefSpool(dir, 256)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	for _, v := range []struct {
		pos, off int64
		value    byte
	}{{2, 8, 3}, {0, 1, 1}, {2, 7, 2}, {2, 8, 4}} {
		if e = a.Append(v.pos, v.off, 1, 2, []byte{v.value}); e != nil {
			t.Fatal(e)
		}
	}
	if e = a.CopySorted(b); e != nil {
		t.Fatal(e)
	}
	if e = b.Rewind(); e != nil {
		t.Fatal(e)
	}
	for _, want := range []byte{1, 2, 3, 4} {
		got, e := b.Next()
		if e != nil || !bytes.Equal(got.Bytes, []byte{want}) {
			t.Fatal("order", got, e)
		}
	}
	if a.index != nil {
		t.Fatal("retained sort index")
	}
}
