package idxref

import (
	"bytes"
	"testing"
)

func TestAutoK0(t *testing.T) {
	for _, c := range []struct{ n, pct, want int }{{1, 12, 1}, {30, 12, 1}, {31, 12, 2}, {7800, 12, 2}, {7900, 12, 3}, {1 << 20, 12, 3}, {65536, 100, 2}, {65537, 100, 3}, {5, 5, 1}} {
		if got := AutoK0(c.n, c.pct); got != c.want {
			t.Errorf("AutoK0(%d, %d) = %d, want %d", c.n, c.pct, got, c.want)
		}
	}
}

func fixed(n int) func([]byte) int { return func(b []byte) int { return n } }

func TestDictCoders(t *testing.T) {
	a, b, c := []byte{1, 2, 3}, []byte{4, 5, 6}, []byte{7, 8, 9}
	refs := [][]byte{a, b, a, a, c, b, c, a}
	enc := DictEncode(refs)
	// first occurrences carry their bytes, repeats are one-byte ordinals
	if len(enc) != len(refs)+3*3 {
		t.Errorf("dictionary code is %d bytes, want %d", len(enc), len(refs)+9)
	}
	got, err := DictDecode(enc, fixed(3))
	if err != nil || len(got) != len(refs) {
		t.Fatalf("decode: %v (%d refs)", err, len(got))
	}
	for i := range refs {
		if !bytes.Equal(got[i], refs[i]) {
			t.Errorf("ref %d: %x want %x", i, got[i], refs[i])
		}
	}
	if _, err := DictDecode([]byte{5, 1, 2, 3}, fixed(3)); err == nil {
		t.Error("ordinal beyond the dictionary accepted")
	}
	if _, err := DictDecode([]byte{0, 1, 2}, fixed(3)); err == nil {
		t.Error("truncated entry accepted")
	}
	split := DictSplitEncode(refs)
	if split[0] != 3 || len(split) != 1+9+len(refs) {
		t.Errorf("split dictionary: %x", split)
	}
	mtf := MTFEncode(refs, 2)
	if mtf[0] != 255 || !bytes.Equal(mtf[1:4], a) {
		t.Errorf("first reference must be a miss with its bytes: %x", mtf)
	}
	// a repeat of the most recent reference is rank 0
	if r := MTFEncode([][]byte{a, a}, 4); len(r) != 5 || r[4] != 0 {
		t.Errorf("recency rank: %x", r)
	}
	// the cache is bounded: with size 1 a reference two back is a miss again
	if r := MTFEncode([][]byte{a, b, a}, 1); len(r) != 12 {
		t.Errorf("bounded cache: %x", r)
	}
}
