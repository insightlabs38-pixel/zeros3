package brotliz3_test

import (
	"encoding/binary"
	"os"
	"testing"

	"zeros3-z3/brotliz3"
)

// BenchmarkPages re-encodes real page bodies (BROTLIZ3_PAGES = dump of u32 len, body, u32 len,
// payload records written by the page probe); skipped without the file.
func BenchmarkPages(b *testing.B) {
	f := os.Getenv("BROTLIZ3_PAGES")
	if f == "" {
		b.Skip("BROTLIZ3_PAGES not set")
	}
	d, err := os.ReadFile(f)
	if err != nil {
		b.Fatal(err)
	}
	var bodies, pays [][]byte
	total := 0
	for p := 0; p < len(d); {
		n := int(binary.LittleEndian.Uint32(d[p:]))
		bodies = append(bodies, d[p+4:p+4+n])
		p += 4 + n
		total += n
		m := int(binary.LittleEndian.Uint32(d[p:]))
		pays = append(pays, d[p+4:p+4+m])
		p += 4 + m
	}
	b.SetBytes(int64(total))
	b.Run("encode", func(b *testing.B) {
		b.SetBytes(int64(total))
		for i := 0; i < b.N; i++ {
			for _, x := range bodies {
				brotliz3.Encode(x, 8, 22)
			}
		}
	})
	b.Run("decode", func(b *testing.B) {
		b.SetBytes(int64(total))
		for i := 0; i < b.N; i++ {
			for k, x := range pays {
				brotliz3.Decode(x, len(bodies[k]))
			}
		}
	})
}
