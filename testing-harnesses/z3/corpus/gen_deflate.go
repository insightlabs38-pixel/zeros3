package corpus

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Raw RFC1951 streams (no wrapper) from the guaranteed Go-stdlib producers plus
// a tiny fixed-Huffman greedy emitter, so the corpus holds stored, fixed and
// dynamic blocks. Mutations mirror the gzip families and act on the plaintext.

// deflateGo compresses with compress/flate at level (0 = stored, -2 = Huffman only).
func deflateGo(plain []byte, level int) []byte {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, level)
	if err != nil {
		panic(err)
	}
	w.Write(plain)
	w.Close()
	return buf.Bytes()
}

// DeflateFixed encodes plain as fixed-Huffman blocks using a single-candidate
// hash matcher. It is deliberately unlike zlib so that predictors keyed to real
// producers face an unfamiliar encoder. Blocks end every 16384 tokens.
func DeflateFixed(plain []byte) []byte {
	w := &bitWriter{}
	const maxTok = 16384
	head := make([]int32, 1<<15)
	for i := range head {
		head[i] = -1
	}
	type tok struct{ lit, dist int32 } // dist 0: literal in lit; else length in lit
	var toks []tok
	flush := func(final bool) {
		if final {
			w.bits(1, 1)
		} else {
			w.bits(0, 1)
		}
		w.bits(1, 2) // BTYPE=01
		for _, t := range toks {
			if t.dist == 0 {
				w.fixedSym(int(t.lit))
				continue
			}
			lc, le, lv := lenCode(int(t.lit))
			w.fixedSym(lc)
			w.bits(uint32(lv), uint(le))
			dc, de, dv := distCode(int(t.dist))
			w.huff(uint32(dc), 5)
			w.bits(uint32(dv), uint(de))
		}
		w.fixedSym(256)
		toks = toks[:0]
	}
	i := 0
	for i < len(plain) {
		bestLen, bestDist := 0, 0
		if i+3 <= len(plain) {
			h := (uint32(plain[i])<<10 ^ uint32(plain[i+1])<<5 ^ uint32(plain[i+2])) & (1<<15 - 1)
			if c := int(head[h]); c >= 0 && i-c <= 32768 {
				n := 0
				for n < 258 && i+n < len(plain) && plain[c+n] == plain[i+n] {
					n++
				}
				if n >= 3 {
					bestLen, bestDist = n, i-c
				}
			}
			head[h] = int32(i)
		}
		step := 1
		if bestLen > 0 {
			toks = append(toks, tok{int32(bestLen), int32(bestDist)})
			step = bestLen
		} else {
			toks = append(toks, tok{int32(plain[i]), 0})
		}
		for j := 1; j < step && i+j+3 <= len(plain); j++ {
			p := i + j
			h := (uint32(plain[p])<<10 ^ uint32(plain[p+1])<<5 ^ uint32(plain[p+2])) & (1<<15 - 1)
			head[h] = int32(p)
		}
		i += step
		if len(toks) >= maxTok && i < len(plain) {
			flush(false)
		}
	}
	flush(true)
	return w.finish()
}

type bitWriter struct {
	buf []byte
	acc uint64
	n   uint
}

func (w *bitWriter) bits(v uint32, n uint) { // LSB-first
	w.acc |= uint64(v) << w.n
	w.n += n
	for w.n >= 8 {
		w.buf = append(w.buf, byte(w.acc))
		w.acc >>= 8
		w.n -= 8
	}
}

func (w *bitWriter) huff(code uint32, n uint) { // Huffman codes are packed MSB-first
	var r uint32
	for i := uint(0); i < n; i++ {
		r |= (code >> i & 1) << (n - 1 - i)
	}
	w.bits(r, n)
}

func (w *bitWriter) fixedSym(s int) {
	switch {
	case s < 144:
		w.huff(uint32(0x30+s), 8)
	case s < 256:
		w.huff(uint32(0x190+s-144), 9)
	case s < 280:
		w.huff(uint32(s-256), 7)
	default:
		w.huff(uint32(0xC0+s-280), 8)
	}
}

func (w *bitWriter) finish() []byte {
	if w.n > 0 {
		w.buf = append(w.buf, byte(w.acc))
		w.acc, w.n = 0, 0
	}
	return w.buf
}

var (
	lenBase = [29]int{3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 23, 27, 31, 35, 43, 51, 59, 67, 83, 99, 115, 131, 163, 195, 227, 258}
	lenExtr = [29]int{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 0}
	dstBase = [30]int{1, 2, 3, 4, 5, 7, 9, 13, 17, 25, 33, 49, 65, 97, 129, 193, 257, 385, 513, 769, 1025, 1537, 2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577}
	dstExtr = [30]int{0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13}
)

func lenCode(l int) (sym, extra, val int) {
	for c := 28; c >= 0; c-- {
		if l >= lenBase[c] {
			return 257 + c, lenExtr[c], l - lenBase[c]
		}
	}
	panic("length")
}

func distCode(d int) (sym, extra, val int) {
	for c := 29; c >= 0; c-- {
		if d >= dstBase[c] {
			return c, dstExtr[c], d - dstBase[c]
		}
	}
	panic("distance")
}

// plainChain produces the shared plaintext revision chain for DEFLATE families.
// kind: text | repeat | random. mut: insert-mid | insert-begin | delete | replace | append | multi.
func plainChain(c *Ctx, kind, mut string, emit func(i int, plain []byte, mutation string) error) error {
	r := c.RNG("data")
	var plain []byte
	for i := 0; i <= c.Cfg.Revisions; i++ {
		if i == 0 {
			switch kind {
			case "text":
				plain = Text(r, 3*c.size)
			case "repeat":
				plain = records(r, 6*c.size)
			default:
				plain = r.Bytes(c.size)
			}
		} else {
			mid := len(plain) / 2
			switch mut {
			case "insert-mid":
				plain = splice(plain, mid, 0, insertion(kind, r, 200))
			case "insert-begin":
				plain = splice(plain, 0, 0, insertion(kind, r, 200))
			case "delete":
				plain = splice(plain, mid, 500, nil)
			case "replace":
				at := r.Intn(len(plain) - 300)
				copy(plain[at:at+300], insertion(kind, r, 300))
			case "append":
				plain = append(plain, Text(r, 2048)...)
			case "multi":
				at := r.Intn(len(plain) - 64)
				plain = splice(plain, at, r.Intn(16), insertion(kind, r, 16+r.Intn(48)))
			}
		}
		m := "base"
		if i > 0 {
			m = mut + " on plaintext"
		}
		if err := emit(i, plain, m); err != nil {
			return err
		}
	}
	return nil
}

func plainTruth(plain []byte, extra map[string]string) map[string][]string {
	sum := sha256.Sum256(plain)
	t := map[string][]string{
		"plain_size":   {strconv.Itoa(len(plain))},
		"plain_sha256": {hex.EncodeToString(sum[:])},
	}
	for k, v := range extra {
		t[k] = []string{v}
	}
	return t
}

func init() {
	type spec struct {
		name, kind, mut, producer string
		enc                       func([]byte) []byte
	}
	gz := func(l int) func([]byte) []byte { return func(p []byte) []byte { return deflateGo(p, l) } }
	specs := []spec{
		{"deflate-L0-insert-mid", "text", "insert-mid", "go-flate-L0 (stored blocks)", gz(0)},
		{"deflate-L1-insert-mid", "text", "insert-mid", "go-flate-L1", gz(1)},
		{"deflate-L6-insert-mid", "text", "insert-mid", "go-flate-L6", gz(6)},
		{"deflate-L9-insert-mid", "text", "insert-mid", "go-flate-L9", gz(9)},
		{"deflate-huffonly-insert-mid", "text", "insert-mid", "go-flate-HuffmanOnly", gz(flate.HuffmanOnly)},
		{"deflate-fixed-insert-mid", "text", "insert-mid", "z3-fixed-greedy (fixed Huffman)", DeflateFixed},
		{"deflate-L6-insert-begin", "text", "insert-begin", "go-flate-L6", gz(6)},
		{"deflate-L6-delete", "text", "delete", "go-flate-L6", gz(6)},
		{"deflate-L6-replace", "text", "replace", "go-flate-L6", gz(6)},
		{"deflate-L6-append", "text", "append", "go-flate-L6", gz(6)},
		{"deflate-L6-repetitive", "repeat", "replace", "go-flate-L6", gz(6)},
		{"deflate-L6-incompressible", "random", "insert-mid", "go-flate-L6", gz(6)},
		{"deflate-L6-multirev", "text", "multi", "go-flate-L6", gz(6)},
	}
	for _, s := range specs {
		s := s
		register(Family{Name: s.name, Format: "deflate", Workload: "deflate", Ext: ".deflate", ToolDependent: true,
			gen: func(c *Ctx, emit func(Artifact) error) error {
				prev := ""
				return plainChain(c, s.kind, s.mut, func(i int, plain []byte, mutation string) error {
					a := Artifact{Revision: "r" + strconv.Itoa(i), Mutation: mutation, Parent: prev,
						Data:  s.enc(plain),
						Truth: plainTruth(plain, map[string]string{"producer": s.producer, "logical_format": "raw RFC1951"})}
					prev = a.Revision
					return emit(a)
				})
			}})
	}
}
