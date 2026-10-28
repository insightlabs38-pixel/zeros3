package parquetz3

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Independently laid out block: lengths5,6,4,7. Delta minimum-2; adjusted
// deltas3,0,5 occupy one 32-value miniblock of width3 (zero padding).
func deltaSeed() []byte {
	b := binary.AppendUvarint(nil, 128)
	b = binary.AppendUvarint(b, 4)
	b = binary.AppendUvarint(b, 4)
	b = binary.AppendVarint(b, 5)
	b = binary.AppendVarint(b, -2)
	b = append(b, 3, 0, 0, 0)
	packed := make([]byte, 12)
	packed[0] = 3 | ((5 & 3) << 6)
	packed[1] = 5 >> 2
	return append(b, packed...)
}
func TestDeltaLengthValuesAndGeometry(t *testing.T) {
	prefix := deltaSeed()
	ls, n, e := deltaLengths(prefix, 4)
	if e != nil || n != len(prefix) || len(ls) != 4 {
		t.Fatalf("%v %v", ls, e)
	}
	for i, w := range []int{5, 6, 4, 7} {
		if ls[i] != w {
			t.Fatalf("length%d=%d", i, ls[i])
		}
	}
	strings := []string{"hello", "world!", "test", "payload"}
	body := bytes.Clone(prefix)
	for _, s := range strings {
		body = append(body, s...)
	}
	r, p, e := decodeDeltaLength(body, 4, Limits{})
	if e != nil {
		t.Fatal(e)
	}
	for i, s := range strings {
		if !bytes.Equal(r.at(i)[4:], []byte(s)) {
			t.Fatal("wrong logical value")
		}
	}
	out, ok := encodeDeltaLength(&r, p)
	if !ok || !bytes.Equal(out, body) {
		t.Fatal("not exact")
	}
	for _, b := range [][]byte{prefix[:len(prefix)-1], {128, 1, 3, 4, 10}, {128, 1, 4, 255, 255, 255, 255, 127}} {
		if _, _, e := deltaLengths(b, 4); e == nil {
			t.Fatal("accepted invalid geometry/count")
		}
	}
}
func FuzzDeltaLengths(f *testing.F) {
	f.Add(deltaSeed(), 4)
	f.Add([]byte{128, 1, 4, 0}, 0)
	f.Add([]byte{255, 255, 255, 255, 127}, 1)
	f.Fuzz(func(t *testing.T, b []byte, n int) {
		if len(b) > 1<<16 || n < 0 || n > 1<<12 {
			return
		}
		ls, used, e := deltaLengths(b, n)
		if e != nil {
			return
		}
		if len(ls) != n || used < 0 || used > len(b) {
			t.Fatal("invalid accepted length stream")
		}
		for _, v := range ls {
			if v < 0 || v > 1<<31-1 {
				t.Fatal("invalid length")
			}
		}
	})
}
