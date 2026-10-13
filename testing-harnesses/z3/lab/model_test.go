package lab

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func blob(tag string, n int) *Comp {
	b := bytes.Repeat([]byte(tag), n)[:n]
	return bytesComp(sum(b), b)
}

func plan(cs ...*Comp) *Plan {
	p := &Plan{Concat: true}
	for _, c := range cs {
		p.Pieces = append(p.Pieces, Piece{Comp: c, Len: c.Size})
	}
	return p
}

func TestAccountingHandComputed(t *testing.T) {
	a, b, c, d := blob("a", 100), blob("b", 200), blob("c", 300), blob("d", 50)
	ac := NewAcct(10, false)

	r1, err := ac.Add(plan(a, b, c))
	if err != nil {
		t.Fatal(err)
	}
	// 600 new bytes + 3 refs x 10.
	if r1.NewBytes != 600 || r1.MetaBytes != 30 || r1.Incremental != 630 || r1.Cumulative != 630 {
		t.Fatalf("rev1 %+v", r1)
	}
	if r1.ReuseBytes != 0 || r1.NewComponents != 3 || r1.UniqueComponents != 3 {
		t.Fatalf("rev1 reuse %+v", r1)
	}

	// a, b reused from rev1; d new; a again within the revision is also reuse.
	r2, _ := ac.Add(plan(a, b, d, a))
	if r2.NewBytes != 50 || r2.MetaBytes != 40 || r2.Incremental != 90 || r2.Cumulative != 720 {
		t.Fatalf("rev2 %+v", r2)
	}
	if r2.ReuseBytes != 100+200+100 || r2.ReusedComponents != 3 || r2.NewComponents != 1 || r2.UniqueComponents != 3 || r2.Components != 4 {
		t.Fatalf("rev2 reuse %+v", r2)
	}
}

func TestAccountingDuplicateWithinFirstRevisionAndInline(t *testing.T) {
	a := blob("a", 100)
	p := plan(a, a, a)
	p.Pieces = append(p.Pieces, Piece{Inline: []byte("hdr!"), Len: 4})
	p.MetaBytes = 7
	p.IndexBytes = 5
	r, err := NewAcct(10, false).Add(p)
	if err != nil {
		t.Fatal(err)
	}
	// new 100; meta = 3 refs*10 + 4 inline bytes + 4 length prefix + 7 = 45; index 5.
	if r.NewBytes != 100 || r.ReuseBytes != 200 || r.MetaBytes != 45 || r.IndexBytes != 5 || r.Incremental != 150 {
		t.Fatalf("%+v", r)
	}
	if r.UniqueComponents != 1 || r.Components != 3 {
		t.Fatalf("%+v", r)
	}
}

func TestAccountingIdenticalRevisionCostsOnlyMetadata(t *testing.T) {
	ac := NewAcct(96, false)
	p := plan(blob("x", 1000), blob("y", 2000))
	ac.Add(p)
	r, _ := ac.Add(p)
	if r.NewBytes != 0 || r.Incremental != 2*96 || r.Cumulative != 3000+4*96 {
		t.Fatalf("%+v", r)
	}
}

func TestAccountingRejectsInconsistentComponents(t *testing.T) {
	ac := NewAcct(1, false)
	good := blob("a", 10)
	ac.Add(plan(good))
	bad := &Comp{ID: good.ID, Size: 11}
	if _, err := ac.Add(plan(bad)); err == nil {
		t.Fatal("same id with different size accepted")
	}
	if _, err := ac.Add(plan(&Comp{Size: -1})); err == nil {
		t.Fatal("negative size accepted")
	}
	if ac.Stored() != 1 {
		t.Fatalf("failed Add mutated the store: %d components", ac.Stored())
	}
}

func TestPackedAccountingFollowsProductionRule(t *testing.T) {
	zeros := bytes.Repeat([]byte{0}, 64<<10)
	random := make([]byte, 64<<10)
	for i := range random {
		random[i] = byte(i*i*31 + i>>3 ^ i*7919)
	}
	// zeros compress far below 15/16; hash-like data must stay raw.
	h := sum(random)
	rnd := make([]byte, 0, len(random))
	for len(rnd) < len(random) {
		h = sum(h[:])
		rnd = append(rnd, h[:]...)
	}
	if got := packedLen(zeros); got >= int64(len(zeros))/16 {
		t.Errorf("zeros packed to %d", got)
	}
	if got := packedLen(rnd); got != int64(len(rnd)) {
		t.Errorf("incompressible data packed to %d, want raw %d", got, len(rnd))
	}
	if packedLen(nil) != 0 {
		t.Error("empty payload")
	}
	ac := NewAcct(0, true)
	r, err := ac.Add(plan(bytesComp(sum(zeros), zeros), bytesComp(sum(rnd), rnd)))
	if err != nil || !r.PackedKnown {
		t.Fatal(err)
	}
	if r.PackedNew != packedLen(zeros)+int64(len(rnd)) || r.IncrementalPack != r.PackedNew {
		t.Fatalf("%+v", r)
	}
}

func TestSourceBounds(t *testing.T) {
	s := BytesSource("x", []byte("0123456789"))
	if b, err := s.Span(2, 3); err != nil || string(b) != "234" {
		t.Fatal(b, err)
	}
	if b, err := s.Span(10, 0); err != nil || len(b) != 0 {
		t.Fatal("empty tail span")
	}
	const big = int64(1<<63 - 1)
	for _, c := range [][2]int64{{-1, 1}, {0, -1}, {11, 0}, {5, 6}, {big, 2}, {1, big}, {big - 1, big - 1}} {
		if _, err := s.Span(c[0], c[1]); !errors.Is(err, ErrRange) {
			t.Errorf("Span(%d,%d) = %v", c[0], c[1], err)
		}
		if _, err := s.Reader(c[0], c[1]); !errors.Is(err, ErrRange) {
			t.Errorf("Reader(%d,%d) = %v", c[0], c[1], err)
		}
	}
}

func TestReadRangeBoundsAndContent(t *testing.T) {
	a, b, c := blob("abc", 10), blob("xyz", 7), blob("q", 5)
	p := plan(a, b, c)
	p.Pieces = append(p.Pieces, Piece{Inline: []byte("INL"), Len: 3})
	ac := NewAcct(0, false)
	if _, err := ac.Add(p); err != nil {
		t.Fatal(err)
	}
	var whole bytes.Buffer
	if err := ConcatRebuild(p)(&whole, ac.Get); err != nil {
		t.Fatal(err)
	}
	all := whole.Bytes()
	if len(all) != 25 {
		t.Fatalf("len %d", len(all))
	}
	for off := int64(0); off <= 25; off++ {
		for n := int64(0); off+n <= 25; n++ {
			got, fetched, err := ReadRange(p, ac.Get, off, n)
			if err != nil || !bytes.Equal(got, all[off:off+n]) {
				t.Fatalf("range [%d,+%d): %q %v", off, n, got, err)
			}
			if n == 0 && fetched != 0 {
				t.Fatalf("empty range fetched %d", fetched)
			}
		}
	}
	// A 2-byte read straddling a/b fetches both components whole.
	if _, fetched, _ := ReadRange(p, ac.Get, 9, 2); fetched != 17 {
		t.Errorf("fetched %d, want 17", fetched)
	}
	for _, c := range [][2]int64{{-1, 1}, {0, 26}, {26, 0}, {1<<63 - 1, 2}, {5, 1<<63 - 1}} {
		if _, _, err := ReadRange(p, ac.Get, c[0], c[1]); err == nil {
			t.Errorf("ReadRange(%d,%d) accepted", c[0], c[1])
		}
	}
	if _, _, err := ReadRange(&Plan{}, ac.Get, 0, 0); err == nil {
		t.Error("non-concat plan served a range")
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestConcatRebuildPropagatesErrors(t *testing.T) {
	p := plan(blob("a", 4))
	ac := NewAcct(0, false)
	ac.Add(p)
	if err := ConcatRebuild(p)(failWriter{}, ac.Get); err == nil {
		t.Fatal("write error swallowed")
	}
	if err := ConcatRebuild(p)(io.Discard, func(ID) ([]byte, error) { return nil, io.ErrUnexpectedEOF }); err == nil {
		t.Fatal("fetch error swallowed")
	}
	if _, err := ac.Get(ID{1}); err == nil || !strings.Contains(err.Error(), "not in store") {
		t.Fatal("missing component not reported", err)
	}
}
