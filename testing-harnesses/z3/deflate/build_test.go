package deflate

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"testing"
	"time"
)

// Independent bitstream assembler for handcrafted fixtures. It shares no code
// with the package under test beyond the RFC tables copied here.

type bw struct {
	b   []byte
	acc uint64
	n   uint
}

func (w *bw) bits(v uint64, n uint) {
	w.acc |= v << w.n
	w.n += n
	for w.n >= 8 {
		w.b = append(w.b, byte(w.acc))
		w.acc >>= 8
		w.n -= 8
	}
}

func (w *bw) huff(code uint32, n uint) { // MSB-first code
	for i := int(n) - 1; i >= 0; i-- {
		w.bits(uint64(code>>uint(i)&1), 1)
	}
}

// pad fills to the byte boundary with the low bits of v.
func (w *bw) pad(v uint64) {
	if w.n > 0 {
		w.bits(v&(1<<(8-w.n)-1), 8-w.n)
	}
}

func (w *bw) done(tailPad uint64) []byte {
	w.pad(tailPad)
	return append([]byte(nil), w.b...)
}

func (w *bw) header(final bool, btype int) {
	f := uint64(0)
	if final {
		f = 1
	}
	w.bits(f|uint64(btype)<<1, 3)
}

func (w *bw) stored(final bool, pad uint64, data []byte) {
	w.header(final, 0)
	w.pad(pad)
	n := uint64(len(data))
	w.bits(n|(^n&0xffff)<<16, 32)
	for _, c := range data {
		w.bits(uint64(c), 8)
	}
}

var (
	tLenBase = []int{3, 4, 5, 6, 7, 8, 9, 10, 11, 13, 15, 17, 19, 23, 27, 31, 35, 43, 51, 59, 67, 83, 99, 115, 131, 163, 195, 227, 258}
	tLenXtra = []int{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 0}
	tDstBase = []int{1, 2, 3, 4, 5, 7, 9, 13, 17, 25, 33, 49, 65, 97, 129, 193, 257, 385, 513, 769, 1025, 1537, 2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577}
	tDstXtra = []int{0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13}
	tClOrder = []int{16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15}
)

// item is a token to emit: literal byte, or (length, distance) with optional
// explicit symbol/extra override for non-canonical spellings.
type item struct {
	lit        int // -1 for a match
	length     int
	dist       int
	forceSym   int // 0 = canonical
	forceExtra int
}

func lit(b byte) item  { return item{lit: int(b)} }
func mt(l, d int) item { return item{lit: -1, length: l, dist: d} }
func lits(s string) []item {
	var o []item
	for _, c := range []byte(s) {
		o = append(o, lit(c))
	}
	return o
}
func symOfLen(l int) (sym int) {
	if l == 258 {
		return 285
	}
	for i := 27; i >= 0; i-- {
		if l >= tLenBase[i] {
			return 257 + i
		}
	}
	panic("len")
}

func codeOfDist(d int) int {
	for i := 29; i >= 0; i-- {
		if d >= tDstBase[i] {
			return i
		}
	}
	panic("dist")
}

// canon assigns canonical MSB-first codes.
func canon(lens []int) []uint32 {
	maxl := 0
	for _, l := range lens {
		maxl = max(maxl, l)
	}
	count := make([]int, maxl+2)
	for _, l := range lens {
		count[l]++
	}
	count[0] = 0
	next := make([]uint32, maxl+2)
	code := uint32(0)
	for l := 1; l <= maxl; l++ {
		code = (code + uint32(count[l-1])) << 1
		next[l] = code
	}
	out := make([]uint32, len(lens))
	for i, l := range lens {
		if l > 0 {
			out[i] = next[l]
			next[l]++
		}
	}
	return out
}

func fixedLens() (lit, dist []int) {
	lit = make([]int, 288)
	for i := range lit {
		switch {
		case i < 144:
			lit[i] = 8
		case i < 256:
			lit[i] = 9
		case i < 280:
			lit[i] = 7
		default:
			lit[i] = 8
		}
	}
	dist = make([]int, 32)
	for i := range dist {
		dist[i] = 5
	}
	return
}

// emit writes items followed by end-of-block using the given code lengths.
func (w *bw) emit(items []item, litLens, distLens []int) {
	lc, dc := canon(litLens), canon(distLens)
	for _, it := range items {
		if it.lit >= 0 {
			w.huff(lc[it.lit], uint(litLens[it.lit]))
			continue
		}
		sym := symOfLen(it.length)
		extra := 0
		if sym != 285 {
			extra = it.length - tLenBase[sym-257]
		}
		if it.forceSym != 0 {
			sym, extra = it.forceSym, it.forceExtra
		}
		w.huff(lc[sym], uint(litLens[sym]))
		w.bits(uint64(extra), uint(tLenXtra[sym-257]))
		d := codeOfDist(it.dist)
		w.huff(dc[d], uint(distLens[d]))
		w.bits(uint64(it.dist-tDstBase[d]), uint(tDstXtra[d]))
	}
	w.huff(lc[256], uint(litLens[256]))
}

func (w *bw) fixed(final bool, items []item) {
	w.header(final, 1)
	l, d := fixedLens()
	w.emit(items, l, d)
}

// dyn describes a dynamic block header spelled out explicitly.
type dyn struct {
	hlit, hdist, hclen int   // raw fields
	clLens             []int // 19 entries by symbol
	seq                []clItem
	lit, dist          []int // resulting lengths
}

type clItem struct{ sym, extra int }

func (w *bw) dynamic(final bool, d dyn, items []item) {
	w.header(final, 2)
	w.bits(uint64(d.hlit)|uint64(d.hdist)<<5|uint64(d.hclen)<<10, 14)
	for i := 0; i < d.hclen+4; i++ {
		w.bits(uint64(d.clLens[tClOrder[i]]), 3)
	}
	cc := canon(d.clLens)
	for _, c := range d.seq {
		w.huff(cc[c.sym], uint(d.clLens[c.sym]))
		switch c.sym {
		case 16:
			w.bits(uint64(c.extra), 2)
		case 17:
			w.bits(uint64(c.extra), 3)
		case 18:
			w.bits(uint64(c.extra), 7)
		}
	}
	w.emit(items, d.lit, d.dist)
}

// plainSeq spells lens with no repeat codes.
func plainSeq(lens []int) []clItem {
	var o []clItem
	for _, l := range lens {
		o = append(o, clItem{l, 0})
	}
	return o
}

// expand returns the lengths a code-length sequence decodes to.
func expand(seq []clItem) []int {
	var o []int
	for _, c := range seq {
		switch {
		case c.sym < 16:
			o = append(o, c.sym)
		case c.sym == 16:
			for i := 0; i < 3+c.extra; i++ {
				o = append(o, o[len(o)-1])
			}
		case c.sym == 17:
			o = append(o, make([]int, 3+c.extra)...)
		default:
			o = append(o, make([]int, 11+c.extra)...)
		}
	}
	return o
}

// assign gives the used symbols code lengths forming a complete code over size symbols.
func assign(size int, used ...int) []int {
	out := make([]int, size)
	k := len(used)
	if k == 1 {
		out[used[0]] = 1
		return out
	}
	L := 0
	for 1<<L < k {
		L++
	}
	m := 1<<L - k
	for i, s := range used {
		if i < m {
			out[s] = L - 1
		} else {
			out[s] = L
		}
	}
	return out
}

// spell encodes a flat length array as a code-length symbol sequence.
// mode 0: no repeat codes; 1: 18/17 zero runs and 16 repeats; 2: 17 only (runs of 3..10), no 16.
func spell(all []int, mode int) []clItem {
	if mode == 0 {
		return plainSeq(all)
	}
	var o []clItem
	for i := 0; i < len(all); {
		v := all[i]
		j := i
		for j < len(all) && all[j] == v {
			j++
		}
		run := j - i
		i = j
		if v == 0 {
			for run >= 3 {
				switch {
				case mode == 1 && run >= 11:
					n := min(run, 138)
					o = append(o, clItem{18, n - 11})
					run -= n
				default:
					n := min(run, 10)
					o = append(o, clItem{17, n - 3})
					run -= n
				}
			}
			for ; run > 0; run-- {
				o = append(o, clItem{0, 0})
			}
			continue
		}
		o = append(o, clItem{v, 0})
		run--
		for mode == 1 && run >= 3 {
			n := min(run, 6)
			o = append(o, clItem{16, n - 3})
			run -= n
		}
		for ; run > 0; run-- {
			o = append(o, clItem{v, 0})
		}
	}
	return o
}

// mkDyn assembles a dyn from literal/dist lengths and a spelling. hlitMin
// widens HLIT beyond the minimum; trailing zeros are then transmitted.
func mkDyn(lit, dist []int, mode, hclen int, hlitPad int) dyn {
	nlit := len(lit) + hlitPad
	l := append(append([]int(nil), lit...), make([]int, hlitPad)...)
	all := append(l, dist...)
	seq := spell(all, mode)
	seen := map[int]bool{}
	var used []int
	for _, c := range seq {
		if !seen[c.sym] {
			seen[c.sym] = true
			used = append(used, c.sym)
		}
	}
	cl := assign(19, used...)
	// transmit at least through the last non-zero code length in clOrder
	need := 4
	for i, s := range tClOrder {
		if cl[s] != 0 && i+1 > need {
			need = i + 1
		}
	}
	if hclen < need-4 {
		hclen = need - 4
	}
	return dyn{hlit: nlit - 257, hdist: len(dist) - 1, hclen: hclen, clLens: cl, seq: seq, lit: l, dist: dist}
}

// check verifies the invariants for an accepted stream.
func check(t *testing.T, name string, stream, wantPlain []byte) *Stream {
	t.Helper()
	s, err := Parse(stream, Options{})
	if err != nil {
		t.Fatalf("%s: parse: %v", name, err)
	}
	if wantPlain != nil && !bytes.Equal(s.Plain, wantPlain) {
		t.Fatalf("%s: plaintext mismatch (%d vs %d bytes)", name, len(s.Plain), len(wantPlain))
	}
	ref, err := io.ReadAll(flate.NewReader(bytes.NewReader(stream)))
	if err != nil {
		t.Fatalf("%s: compress/flate rejects a stream we accept: %v", name, err)
	}
	if !bytes.Equal(ref, s.Plain) {
		t.Fatalf("%s: plaintext differs from compress/flate", name)
	}
	re, err := s.Bytes()
	if err != nil {
		t.Fatalf("%s: replay: %v", name, err)
	}
	if !bytes.Equal(re, stream) {
		t.Fatalf("%s: replay differs from original\n got % x\nwant % x", name, trunc(re), trunc(stream))
	}
	return s
}

func trunc(b []byte) []byte {
	if len(b) > 48 {
		return b[:48]
	}
	return b
}

func timeOf(sec int64) time.Time { return time.Unix(sec, 0) }

func mustGzip(t *testing.T, b []byte) io.Reader {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
