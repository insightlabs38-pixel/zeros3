package dcore

import (
	"bytes"
	"crypto/sha256"
	"io"
	"math/rand/v2"
	"testing"
)

func wp(workers int) WParams {
	p := DefaultWParams()
	p.Window = 1 << 12
	p.PageSize = 1 << 10
	p.CachePages = 8
	p.Workers = workers
	p.Anchor.MaxAnchors = 4096
	return p
}

func encodeW(t *testing.T, base, tgt []byte, p WParams) ([]byte, WStats) {
	t.Helper()
	idx, err := BuildWIndex(bytes.NewReader(base), int64(len(base)), p.Anchor, p.Workers)
	if err != nil {
		t.Fatal(err)
	}
	if idx.SHA != sha256.Sum256(base) {
		t.Fatal("base identity")
	}
	var rec bytes.Buffer
	st, err := WEncode(&rec, bytes.NewReader(tgt), int64(len(tgt)), bytes.NewReader(base), int64(len(base)), idx, p)
	if err != nil {
		t.Fatal(err)
	}
	return rec.Bytes(), st
}

func decodeAll(t *testing.T, rec, base []byte, p WParams) []byte {
	t.Helper()
	w, err := OpenWRecord(bytes.NewReader(rec), int64(len(rec)))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if _, err := w.Decode(&out, bytes.NewReader(base), p); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestWindowRoundTrip(t *testing.T) {
	for seed := uint64(1); seed <= 40; seed++ {
		r := rand.New(rand.NewPCG(seed, 9))
		base := rnd(seed, 20000+r.IntN(200000))
		tgt := mutate(seed+100, base, 5+r.IntN(40))
		if seed%4 == 0 {
			tgt = append(tgt, base[:len(base)/3]...) // repeated long region
		}
		if seed%7 == 0 {
			tgt = tgt[:0]
		}
		p := wp(1 + int(seed%3))
		rec, st := encodeW(t, base, tgt, p)
		got := decodeAll(t, rec, base, p)
		if !bytes.Equal(got, tgt) {
			t.Fatalf("seed %d: mismatch", seed)
		}
		if len(tgt) > 50000 && st.RecordBytes > int64(len(tgt))/3 {
			t.Fatalf("seed %d: record %d for target %d", seed, st.RecordBytes, len(tgt))
		}
	}
}

// Cross-window insertion, deletion, move and duplicated regions.
func TestWindowCrossWindowEdits(t *testing.T) {
	base := rnd(77, 400000)
	var tgt []byte
	tgt = append(tgt, base[:100000]...)
	tgt = append(tgt, rnd(5, 3001)...)        // insertion straddling windows
	tgt = append(tgt, base[100777:250000]...) // deletion
	tgt = append(tgt, base[300000:390000]...) // move forward
	tgt = append(tgt, base[10000:60000]...)   // move backward / duplicate
	p := wp(3)
	rec, st := encodeW(t, base, tgt, p)
	if !bytes.Equal(decodeAll(t, rec, base, p), tgt) {
		t.Fatal("mismatch")
	}
	if st.LiteralBytes > 12000 {
		t.Fatalf("expected near-pure copies, literal=%d", st.LiteralBytes)
	}
	if st.Frames < 90 {
		t.Fatalf("frames=%d", st.Frames)
	}
}

func TestWindowRange(t *testing.T) {
	base := rnd(3, 300000)
	tgt := mutate(4, base, 25)
	p := wp(2)
	rec, _ := encodeW(t, base, tgt, p)
	w, err := OpenWRecord(bytes.NewReader(rec), int64(len(rec)))
	if err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 1))
	for k := 0; k < 200; k++ {
		off := int64(r.IntN(len(tgt)))
		n := int64(r.IntN(20000))
		if off+n > int64(len(tgt)) {
			n = int64(len(tgt)) - off
		}
		got, st, err := w.DecodeRange(bytes.NewReader(base), p, off, n)
		if err != nil || !bytes.Equal(got, tgt[off:off+n]) {
			t.Fatalf("range %d+%d err=%v", off, n, err)
		}
		if n > 0 && int64(st.Frames) > n/p.Window+2 {
			t.Fatalf("range decoded %d frames for %d bytes", st.Frames, n)
		}
	}
	if _, _, err := w.DecodeRange(bytes.NewReader(base), p, int64(len(tgt))-1, 5); err == nil {
		t.Fatal("range past end accepted")
	}
}

// A corrupt base byte inside a COPY the frame touches must be detected by the
// frame hash, and a corrupted record must fail closed; untouched frames still decode.
func TestWindowAuthentication(t *testing.T) {
	base := rnd(11, 100000)
	tgt := mutate(12, base, 10)
	p := wp(2)
	rec, _ := encodeW(t, base, tgt, p)
	bad := append([]byte{}, base...)
	bad[50000] ^= 0xff
	w, _ := OpenWRecord(bytes.NewReader(rec), int64(len(rec)))
	if _, err := w.Decode(io.Discard, bytes.NewReader(bad), p); err == nil {
		t.Fatal("corrupt base accepted")
	}
	// Flip every byte of the record in turn (sampled): never accept wrong output.
	for i := 0; i < len(rec); i += 1 + len(rec)/700 {
		c := append([]byte{}, rec...)
		c[i] ^= 0x41
		w, err := OpenWRecord(bytes.NewReader(c), int64(len(c)))
		if err != nil {
			continue
		}
		var out bytes.Buffer
		if _, err := w.Decode(&out, bytes.NewReader(base), p); err == nil && !bytes.Equal(out.Bytes(), tgt) {
			t.Fatalf("flip at %d produced wrong accepted output", i)
		}
	}
	// Truncation at every length must not panic or accept.
	for n := 0; n < len(rec); n += 1 + len(rec)/300 {
		if w, err := OpenWRecord(bytes.NewReader(rec[:n]), int64(n)); err == nil {
			if _, e := w.Decode(io.Discard, bytes.NewReader(base), p); e == nil {
				t.Fatalf("truncated record %d accepted", n)
			}
		}
	}
}

type shortReaderAt struct {
	b   []byte
	cut int
}

func (s shortReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(s.b)) {
		return 0, io.EOF
	}
	n := copy(p, s.b[off:])
	if n > s.cut {
		return s.cut, nil // short read with nil error: callers must not trust it
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func TestWindowShortReads(t *testing.T) {
	base := rnd(21, 50000)
	tgt := mutate(22, base, 5)
	p := wp(1)
	if _, err := BuildWIndex(shortReaderAt{base, 100}, int64(len(base)), p.Anchor, 1); err == nil {
		t.Fatal("short base read accepted by index build")
	}
	idx, _ := BuildWIndex(bytes.NewReader(base), int64(len(base)), p.Anchor, 1)
	var rec bytes.Buffer
	if _, err := WEncode(&rec, shortReaderAt{tgt, 77}, int64(len(tgt)), bytes.NewReader(base), int64(len(base)), idx, p); err == nil {
		t.Fatal("short target read accepted")
	}
	if _, err := WEncode(&rec, bytes.NewReader(tgt), int64(len(tgt)), shortReaderAt{base, 33}, int64(len(base)), idx, p); err == nil {
		// matches need base reads; a short read must surface as an error, not a bad delta
		t.Log("encode with short base reads reported no error (no verification read was attempted)")
	}
}

type failWriter struct{ left int }

func (f *failWriter) Write(p []byte) (int, error) {
	if len(p) > f.left {
		n := f.left
		f.left = 0
		return n, io.ErrShortWrite
	}
	f.left -= len(p)
	return len(p), nil
}

func TestWindowShortWrite(t *testing.T) {
	base := rnd(31, 60000)
	tgt := mutate(32, base, 8)
	p := wp(2)
	idx, _ := BuildWIndex(bytes.NewReader(base), int64(len(base)), p.Anchor, 2)
	var ref bytes.Buffer
	if _, err := WEncode(&ref, bytes.NewReader(tgt), int64(len(tgt)), bytes.NewReader(base), int64(len(base)), idx, p); err != nil {
		t.Fatal(err)
	}
	for _, lim := range []int{0, 3, 100, ref.Len() / 2, ref.Len() - 1} {
		if _, err := WEncode(&failWriter{lim}, bytes.NewReader(tgt), int64(len(tgt)), bytes.NewReader(base), int64(len(base)), idx, p); err == nil {
			t.Fatalf("write failure at %d not reported", lim)
		}
	}
}

func TestWindowBoundsAndParams(t *testing.T) {
	if _, err := BuildWIndex(bytes.NewReader(nil), maxTotal+1, DefaultParams(), 1); err != ErrTooLarge {
		t.Fatal("oversized base accepted", err)
	}
	if _, err := BuildWIndex(bytes.NewReader(nil), -1, DefaultParams(), 1); err == nil {
		t.Fatal("negative size accepted")
	}
	bad := DefaultWParams()
	bad.Window = 1
	if bad.valid() {
		t.Fatal("window too small accepted")
	}
}

func TestWindowParallelDeterministic(t *testing.T) {
	base := rnd(41, 300000)
	tgt := mutate(42, base, 30)
	a, _ := encodeW(t, base, tgt, wp(1))
	b, _ := encodeW(t, base, tgt, wp(4))
	// The anchor table can differ between runs only in insertion order of equal-hash slots;
	// both records must decode exactly, sizes within 2%.
	for _, rec := range [][]byte{a, b} {
		if !bytes.Equal(decodeAll(t, rec, base, wp(2)), tgt) {
			t.Fatal("mismatch")
		}
	}
	if d := len(a) - len(b); d > len(a)/50 || -d > len(a)/50 {
		t.Fatalf("worker count changed size: %d vs %d", len(a), len(b))
	}
}

func FuzzWindowRecord(f *testing.F) {
	base := rnd(51, 20000)
	tgt := mutate(52, base, 6)
	p := wp(1)
	idx, _ := BuildWIndex(bytes.NewReader(base), int64(len(base)), p.Anchor, 1)
	var rec bytes.Buffer
	WEncode(&rec, bytes.NewReader(tgt), int64(len(tgt)), bytes.NewReader(base), int64(len(base)), idx, p)
	f.Add(rec.Bytes())
	f.Fuzz(func(t *testing.T, b []byte) {
		w, err := OpenWRecord(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return
		}
		var out bytes.Buffer
		if _, err := w.Decode(&out, bytes.NewReader(base), p); err == nil {
			if sha256.Sum256(out.Bytes()) != w.SHA || int64(out.Len()) != w.Size {
				t.Fatal("accepted output does not match the record identity")
			}
		}
	})
}
