package idxref

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
)

func TestA4ComponentLengthOverflow(t *testing.T) {
	for _, n := range []int{1, 6, 9} {
		t.Run(string(rune('a'+n)), func(t *testing.T) {
			// MaxUint64 payload plus one stream byte used to wrap to zero.
			b := binary.AppendUvarint(nil, ^uint64(0))
			b = binary.AppendUvarint(b, 1)
			for i := 1; i < n; i++ {
				b = binary.AppendUvarint(b, 1)
			}
			b = append(b, make([]byte, n-1)...)
			if _, _, e := SplitComp(b, n); e == nil {
				t.Fatal("accepted overflowed payload length")
			}
		})
	}
}

func TestA4TruncatedStagedRecords(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		s, e := NewRefSpool(t.TempDir(), 1024)
		if e != nil {
			t.Fatal(e)
		}
		defer s.Close()
		if e = s.Append(0, 0, 0, []byte{1}); e != nil {
			t.Fatal(e)
		}
		if e = s.Rewind(); e != nil {
			t.Fatal(e)
		}
		if e = s.f.Truncate(0); e != nil {
			t.Fatal(e)
		}
		if _, e = s.Next(); e != io.ErrUnexpectedEOF {
			t.Fatalf("lost staged record: %v", e)
		}
	})
	t.Run("sorted", func(t *testing.T) {
		dir := t.TempDir()
		s, e := NewSortedRefSpool(dir, 2, 1024)
		if e != nil {
			t.Fatal(e)
		}
		defer s.Close()
		d, e := NewRefSpool(dir, 1024)
		if e != nil {
			t.Fatal(e)
		}
		defer d.Close()
		for i := 0; i < 2; i++ {
			if e = s.Append(int64(i), int64(i), 0, 0, []byte{1}); e != nil {
				t.Fatal(e)
			}
		}
		if e = s.flushRun(); e != nil {
			t.Fatal(e)
		}
		if e = os.Truncate(s.runs[0], 31); e != nil {
			t.Fatal(e)
		}
		if e = s.CopySorted(d); e != io.ErrUnexpectedEOF {
			t.Fatalf("lost sorted record: %v", e)
		}
	})
}

type reviewFaultWriter struct{ err error }

func (w reviewFaultWriter) Write([]byte) (int, error) { return 0, w.err }
func TestA4SpoolIOFailureCleanup(t *testing.T) {
	sentinel := errors.New("temporary disk full")
	for _, sorted := range []bool{false, true} {
		t.Run(fmt.Sprint(sorted), func(t *testing.T) {
			dir := t.TempDir()
			if sorted {
				s, e := NewSortedRefSpool(dir, 4, 1024)
				if e != nil {
					t.Fatal(e)
				}
				s.w.Reset(reviewFaultWriter{sentinel})
				if e = s.Append(0, 0, 0, 0, []byte{1}); e != nil {
					t.Fatal(e)
				}
				if e = s.flushRun(); !errors.Is(e, sentinel) {
					t.Fatal(e)
				}
				if e = s.Close(); e != nil {
					t.Fatal(e)
				}
			} else {
				s, e := NewRefSpool(dir, 1024)
				if e != nil {
					t.Fatal(e)
				}
				s.writer.Reset(reviewFaultWriter{sentinel})
				if e = s.Append(0, 0, 0, []byte{1}); e != nil {
					t.Fatal(e)
				}
				if e = s.Rewind(); !errors.Is(e, sentinel) {
					t.Fatal(e)
				}
				if e = s.Close(); e != nil {
					t.Fatal(e)
				}
			}
			entries, e := os.ReadDir(dir)
			if e != nil || len(entries) != 0 {
				t.Fatalf("leak: %v %v", entries, e)
			}
		})
	}
}
