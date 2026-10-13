package corpus

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
)

// Raw controls: what ordinary CDC does well (and badly) with no format at all.

func splice(b []byte, off, del int, ins []byte) []byte {
	out := make([]byte, 0, len(b)-del+len(ins))
	out = append(out, b[:off]...)
	out = append(out, ins...)
	return append(out, b[off+del:]...)
}

func init() {
	raw := func(name, desc string, mut func(r *RNG, prev []byte, i int) []byte) {
		register(Family{Name: name, Format: "raw", Workload: "control", Ext: ".bin",
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				var cur []byte
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					if i == 0 {
						cur = r.Bytes(c.size - c.size%8)
						return cur, "base: random bytes", nil
					}
					cur = mut(r, cur, i)
					rel := "similar"
					if name == "raw-random" {
						rel = "unrelated"
					}
					return cur, desc, map[string][]string{"relation": {rel}}
				})
			}})
	}
	raw("raw-append", "append 1/16 of base size", func(r *RNG, p []byte, i int) []byte {
		return append(append([]byte(nil), p...), r.Bytes(len(p)/16)...)
	})
	raw("raw-insert", "insert 3 KiB at a random offset", func(r *RNG, p []byte, i int) []byte {
		return splice(p, r.Intn(len(p)), 0, r.Bytes(3072))
	})
	raw("raw-delete", "delete 3 KiB at a random offset", func(r *RNG, p []byte, i int) []byte {
		return splice(p, r.Intn(len(p)-3072), 3072, nil)
	})
	raw("raw-sparse-edit", "overwrite 8 x 64 B at random offsets", func(r *RNG, p []byte, i int) []byte {
		out := append([]byte(nil), p...)
		for k := 0; k < 8; k++ {
			r.Fill(out[r.Intn(len(out)-64):][:64])
		}
		return out
	})
	raw("raw-reorder", "swap two of eight equal regions", func(r *RNG, p []byte, i int) []byte {
		n := len(p) / 8
		a, b := r.Intn(8), r.Intn(7)
		if b >= a {
			b++
		}
		out := append([]byte(nil), p...)
		copy(out[a*n:(a+1)*n], p[b*n:(b+1)*n])
		copy(out[b*n:(b+1)*n], p[a*n:(a+1)*n])
		return out
	})
	raw("raw-random", "fresh unrelated random bytes", func(r *RNG, p []byte, i int) []byte {
		return r.Bytes(len(p))
	})
}

// DEFLATE/gzip: small logical edits, different compressed streams.

func gz(plain []byte, level int) []byte {
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, level)
	if err != nil {
		panic(err)
	}
	w.Write(plain)
	w.Close()
	return buf.Bytes()
}

// records builds highly repetitive line records with incrementing counters.
func records(r *RNG, n int) []byte {
	var b bytes.Buffer
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "{\"id\":%d,\"kind\":\"sample\",\"flags\":[1,0,1],\"v\":%d}\n", i, r.Intn(1000))
	}
	return b.Bytes()[:n]
}

func init() {
	type spec struct {
		name  string
		level int
		mut   string
		kind  string // text | repeat | random
	}
	specs := []spec{
		{"gzip-L1-insert-mid", 1, "insert-mid", "text"},
		{"gzip-L6-insert-mid", 6, "insert-mid", "text"},
		{"gzip-L9-insert-mid", 9, "insert-mid", "text"},
		{"gzip-L6-insert-begin", 6, "insert-begin", "text"},
		{"gzip-L6-delete", 6, "delete", "text"},
		{"gzip-L6-replace", 6, "replace", "text"},
		{"gzip-L6-append", 6, "append", "text"},
		{"gzip-L6-repetitive", 6, "replace", "repeat"},
		{"gzip-L6-incompressible", 6, "insert-mid", "random"},
	}
	for _, s := range specs {
		s := s
		register(Family{Name: s.name, Format: "gzip", Workload: "deflate", Ext: ".gz", ToolDependent: true,
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				var plain []byte
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					if i == 0 {
						switch s.kind {
						case "text":
							plain = Text(r, 3*c.size)
						case "repeat":
							plain = records(r, 6*c.size)
						default:
							plain = r.Bytes(c.size)
						}
					} else {
						mid := len(plain) / 2
						switch s.mut {
						case "insert-mid":
							plain = splice(plain, mid, 0, insertion(s.kind, r, 200))
						case "insert-begin":
							plain = splice(plain, 0, 0, insertion(s.kind, r, 200))
						case "delete":
							plain = splice(plain, mid, 500, nil)
						case "replace":
							// equal-length replacement of a small span
							at := r.Intn(len(plain) - 300)
							copy(plain[at:at+300], insertion(s.kind, r, 300))
						case "append":
							plain = append(plain, Text(r, 2048)...)
						}
					}
					sum := sha256.Sum256(plain)
					truth := map[string][]string{
						"level":          {strconv.Itoa(s.level)},
						"plain_size":     {strconv.Itoa(len(plain))},
						"plain_sha256":   {hex.EncodeToString(sum[:])},
						"logical_format": {"gzip single member, zero mtime"},
					}
					mut := "base"
					if i > 0 {
						mut = s.mut + " on plaintext"
					}
					return gz(plain, s.level), mut, truth
				})
			}})
	}
}

func insertion(kind string, r *RNG, n int) []byte {
	switch kind {
	case "random":
		return r.Bytes(n)
	case "repeat":
		return records(r, n)
	}
	return Text(r, n)
}
