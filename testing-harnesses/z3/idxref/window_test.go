package idxref

import (
	"bytes"
	"io"
	"math/rand"
	"testing"
)

func TestWindowViewExactAcrossMaskAndExcision(t *testing.T) {
	rng := rand.New(rand.NewSource(4104))
	source := make([]byte, 512<<10)
	rng.Read(source)
	patches := []Patch{{Off: 2, Len: 4, Bytes: [5]byte{1, 2, 3, 4}}, {Off: 65534, Len: 5, Bytes: [5]byte{5, 6, 7, 8, 9}}, {Off: 200000, Len: 2}, {Off: int64(len(source)) - 4, Len: 4}}
	excised := []Span{{0, 1}, {100, 50}, {70000, 333}, {int64(len(source)) - 2, 2}}
	masked := append([]byte(nil), source...)
	for _, p := range patches {
		copy(masked[p.Off:p.Off+int64(p.Len)], p.Bytes[:p.Len])
	}
	var expected []byte
	pos := int64(0)
	for _, s := range excised {
		expected = append(expected, masked[pos:s.Off]...)
		pos = s.Off + s.Len
	}
	expected = append(expected, masked[pos:]...)
	v, err := NewWindowView(bytes.NewReader(source), int64(len(source)), excised, patches)
	if err != nil {
		t.Fatal(err)
	}
	all, err := io.ReadAll(io.NewSectionReader(v, 0, v.Size()))
	if err != nil || !bytes.Equal(all, expected) {
		t.Fatal("full", err)
	}
	for j := 0; j < 1000; j++ {
		off := rng.Intn(len(expected))
		n := rng.Intn(65537)
		buf := make([]byte, n)
		got, err := v.ReadAt(buf, int64(off))
		want := len(expected) - off
		if want > n {
			want = n
		}
		if got != want || !bytes.Equal(buf[:got], expected[off:off+want]) || got < n && err != io.EOF || got == n && err != nil {
			t.Fatalf("range %d+%d: %d %v", off, n, got, err)
		}
	}
	h, err := FNVRange(v, 0, v.Size(), make([]byte, 777))
	if err != nil || h != FNV64(FNVInit, expected) {
		t.Fatal("hash", err)
	}
}
func TestWindowViewRejectsGeometry(t *testing.T) {
	for _, x := range []struct {
		sp []Span
		p  []Patch
	}{{sp: []Span{{4, 7}}}, {sp: []Span{{4, 3}, {2, 1}}}, {p: []Patch{{Off: 1, Len: 0}}}, {p: []Patch{{Off: 1, Len: 6}}}, {p: []Patch{{Off: 9, Len: 2}}}, {p: []Patch{{Off: 1, Len: 3}, {Off: 2, Len: 3}}}} {
		if _, err := NewWindowView(bytes.NewReader(make([]byte, 10)), 10, x.sp, x.p); err == nil {
			t.Fatal("accepted invalid geometry")
		}
	}
}
