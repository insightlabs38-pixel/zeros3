package corpus

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strconv"
	"testing"
)

func TestDeflateFamilies(t *testing.T) {
	want := map[string]int{ // expected BTYPE of the first block
		"deflate-L0-insert-mid":    0,
		"deflate-fixed-insert-mid": 1,
		"deflate-L6-insert-mid":    2,
		"deflate-L9-insert-mid":    2,
	}
	fams := gather(t, "deflate-*")
	if len(fams) < 12 {
		t.Fatalf("only %d deflate families", len(fams))
	}
	for fam, ms := range fams {
		var prev []byte
		for i, m := range ms {
			plain, err := io.ReadAll(flate.NewReader(bytes.NewReader(m.data)))
			if err != nil {
				t.Fatalf("%s %s: %v", fam, m.Revision, err)
			}
			h := sha256.Sum256(plain)
			if hex.EncodeToString(h[:]) != m.Truth["plain_sha256"][0] || strconv.Itoa(len(plain)) != m.Truth["plain_size"][0] {
				t.Errorf("%s %s: truth mismatch", fam, m.Revision)
			}
			if i > 0 && bytes.Equal(plain, prev) {
				t.Errorf("%s %s: mutation changed nothing", fam, m.Revision)
			}
			prev = plain
			if bt, ok := want[fam]; ok {
				if got := int(m.data[0]>>1) & 3; got != bt {
					t.Errorf("%s %s: first BTYPE %d, want %d", fam, m.Revision, got, bt)
				}
			}
		}
	}
}

func TestDeflateFixedEmitter(t *testing.T) {
	r := NewRNG(7, "fixed")
	cases := [][]byte{nil, {0}, []byte("abcabcabcabc"), bytes.Repeat([]byte{'x'}, 100000), Text(r, 70000), r.Bytes(50000)}
	for i, c := range cases {
		enc := DeflateFixed(c)
		got, err := io.ReadAll(flate.NewReader(bytes.NewReader(enc)))
		if err != nil || !bytes.Equal(got, c) {
			t.Fatalf("case %d: err=%v equal=%v", i, err, bytes.Equal(got, c))
		}
	}
}
