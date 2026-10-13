package deflate

import (
	"bytes"
	"encoding/hex"
	"math/rand"
	"testing"
)

func TestGoldenStored(t *testing.T) {
	var w bw
	w.stored(true, 0, []byte("abc"))
	s := check(t, "stored", w.done(0), []byte("abc"))
	if s.Blocks[0].Kind != Stored || !s.Blocks[0].Final {
		t.Fatalf("%+v", s.Blocks[0])
	}

	// non-zero padding bits are preserved wherever they appear
	for _, pad := range []uint64{0x1f, 0x15, 0x01} {
		var w bw
		w.stored(false, pad, []byte("x"))
		w.stored(false, pad, nil)
		w.stored(true, pad, []byte("yz"))
		s := check(t, "stored-pad", w.done(0), []byte("xyz"))
		if s.Blocks[0].PadLen != 5 || uint64(s.Blocks[0].PadBits) != pad&31 {
			t.Fatalf("pad %d/%d", s.Blocks[0].PadBits, s.Blocks[0].PadLen)
		}
	}
	// a stored block after a fixed block begins mid-byte
	w = bw{}
	w.fixed(false, lits("hi"))
	w.stored(true, 0x2, []byte("!"))
	check(t, "fixed-then-stored", w.done(0), []byte("hi!"))

	// maximum stored length
	data := make([]byte, 65535)
	rand.New(rand.NewSource(1)).Read(data)
	w = bw{}
	w.stored(true, 0, data)
	check(t, "stored-max", w.done(0), data)
}

func TestGoldenEmptyAndTiny(t *testing.T) {
	var w bw
	w.fixed(true, nil)
	b := w.done(0)
	if hex.EncodeToString(b) != "0300" {
		t.Fatalf("empty fixed = %x", b)
	}
	check(t, "empty", b, []byte{})
	w = bw{}
	w.stored(true, 0, nil)
	check(t, "empty-stored", w.done(0), []byte{})
	// empty non-final blocks of every kind, then data
	w = bw{}
	w.fixed(false, nil)
	w.stored(false, 0, nil)
	w.dynamic(false, mkDyn(assign(257, 256), assign(1, 0), 0, 0, 0), nil)
	w.fixed(true, lits("q"))
	s := check(t, "empty-blocks", w.done(0), []byte("q"))
	if st := s.Stats(); st.EmptyBlocks != 3 || st.Blocks != 4 {
		t.Fatalf("%+v", st)
	}
}

func TestGoldenTailPadding(t *testing.T) {
	for _, pad := range []uint64{0, 0xff, 0x2a} {
		var w bw
		w.fixed(true, lits("pad"))
		b := w.done(pad)
		s := check(t, "tail-pad", b, []byte("pad"))
		if s.TailPadLen != uint8((8-(3+3*8+7)%8)%8) {
			t.Fatalf("tail len %d", s.TailPadLen)
		}
	}
}

func TestGoldenFixedMatches(t *testing.T) {
	// all match lengths with overlap (distance 1), canonical spelling
	var items []item
	items = append(items, lit('x'))
	var want []byte
	want = append(want, 'x')
	for l := 3; l <= 258; l++ {
		items = append(items, mt(l, 1))
		want = append(want, bytes.Repeat([]byte{'x'}, l)...)
	}
	var w bw
	w.fixed(true, items)
	s := check(t, "all-lengths", w.done(0), want)
	if st := s.Stats(); st.MaxLength != 258 || st.Matches != 256 {
		t.Fatalf("%+v", st)
	}

	// length 258 spelled as symbol 284 + extra 31: valid for compress/flate and zlib
	w = bw{}
	w.fixed(true, []item{lit('a'), {lit: -1, length: 258, dist: 1, forceSym: 284, forceExtra: 31}})
	b := w.done(0)
	s = check(t, "len258-as-284", b, bytes.Repeat([]byte{'a'}, 259))
	if s.Stats().NonCanonicalLengths != 1 {
		t.Fatal("non-canonical length not recorded")
	}
	// the canonical spelling is a different bitstream with identical plaintext
	w = bw{}
	w.fixed(true, []item{lit('a'), mt(258, 1)})
	canon258 := w.done(0)
	if bytes.Equal(canon258, b) {
		t.Fatal("spellings should differ")
	}
	check(t, "len258-canonical", canon258, bytes.Repeat([]byte{'a'}, 259))
}

func TestGoldenDistances(t *testing.T) {
	// 32 KiB of history in a stored block, then matches at every distance-code boundary
	hist := make([]byte, 32768)
	rand.New(rand.NewSource(2)).Read(hist)
	var items []item
	var want = append([]byte(nil), hist...)
	add := func(l, d int) {
		items = append(items, mt(l, d))
		from := len(want) - d
		for i := 0; i < l; i++ {
			want = append(want, want[from+i])
		}
	}
	for c := 0; c < 30; c++ {
		add(3, tDstBase[c])
		hi := tDstBase[c] + 1<<uint(tDstXtra[c]) - 1
		add(258, min(hi, 32768))
	}
	var w bw
	w.stored(false, 0, hist) // a match reaching back across the block boundary
	w.fixed(true, items)
	s := check(t, "distances", w.done(0), want)
	if st := s.Stats(); st.MaxDistance != 32768 {
		t.Fatalf("max distance %d", st.MaxDistance)
	}
}

func TestGoldenOverlapAndCrossBlock(t *testing.T) {
	var w bw
	w.fixed(false, append(lits("ab"), mt(5, 2))) // abababa
	w.fixed(true, []item{mt(7, 7), mt(4, 1)})    // copy across block boundary, then run
	check(t, "overlap", w.done(0), []byte("abababaabababaaaaa"))
}

// dynamic fixtures: the same code lengths spelled in different header encodings
func TestGoldenDynamicSpellings(t *testing.T) {
	ll := assign(259, 'a', 'b', 'c', 256, 257, 258)
	dist := assign(5, 2, 4)
	items := append(lits("abcabc"), mt(3, 3), mt(4, 6), lit('c'))
	var first []byte
	seen := map[string]bool{}
	for mode := 0; mode <= 2; mode++ {
		for _, hclen := range []int{0, 15} {
			for _, pad := range []int{0, 4} {
				d := mkDyn(ll, dist, mode, hclen, pad)
				if got := expand(d.seq); len(got) != len(d.lit)+len(d.dist) {
					t.Fatalf("spelling covers %d", len(got))
				}
				var w bw
				w.dynamic(true, d, items)
				b := w.done(0)
				s := check(t, "dyn-spelling", b, nil)
				if first == nil {
					first = s.Plain
				} else if !bytes.Equal(first, s.Plain) {
					t.Fatal("plaintext differs between spellings")
				}
				seen[string(b)] = true
				bl := s.Blocks[0]
				if int(bl.HCLEN) != d.hclen || len(bl.CLSyms) != len(d.seq) {
					t.Fatalf("header not captured: hclen %d/%d syms %d/%d", bl.HCLEN, d.hclen, len(bl.CLSyms), len(d.seq))
				}
				for i, c := range d.seq {
					if int(bl.CLSyms[i].Sym) != c.sym || int(bl.CLSyms[i].Extra) != c.extra {
						t.Fatalf("code-length symbol %d differs", i)
					}
				}
			}
		}
	}
	if len(seen) < 8 {
		t.Fatalf("only %d distinct encodings of the same trees", len(seen))
	}
}

func TestGoldenDynamicEdgeCases(t *testing.T) {
	// repeat that crosses the literal/distance boundary: trailing zero lit lengths + leading zero dist lengths
	ll := assign(259, 'a', 'b', 256, 257)
	dist := append([]int{0}, assign(2, 0, 1)...) // dist symbols 1 and 2 used
	d := mkDyn(ll, dist, 1, 0, 6)
	var w bw
	w.dynamic(true, d, append(lits("abab"), mt(3, 2)))
	b := w.done(0)
	s := check(t, "cross-boundary-repeat", b, []byte("abababa"))
	found := false
	for _, c := range s.Blocks[0].CLSyms {
		if c.Sym == 17 || c.Sym == 18 {
			found = true
		}
	}
	if !found {
		t.Fatal("no zero-run symbol used")
	}

	// only literals: all-zero distance lengths (RFC: no distance codes)
	ll = assign(257, 'x', 'y', 256)
	dd := mkDyn(ll, []int{0}, 1, 0, 0)
	w = bw{}
	w.dynamic(true, dd, lits("xyxxy"))
	check(t, "no-distance-codes", w.done(0), []byte("xyxxy"))

	// one distance code of length 1 (incomplete, explicitly allowed)
	dd = mkDyn(assign(258, 'x', 256, 257), []int{1}, 0, 0, 0)
	w = bw{}
	w.dynamic(true, dd, append(lits("xx"), mt(3, 1)))
	check(t, "single-distance-code", w.done(0), []byte("xxxxx"))

	// only an end-of-block code of length 1
	dd = mkDyn(assign(257, 256), []int{0}, 0, 0, 0)
	w = bw{}
	w.dynamic(true, dd, nil)
	check(t, "eob-only", w.done(0), []byte{})

	// full alphabet: 286 literal/length symbols and 30 distances, every extra-bit value at the extremes
	var usedLit []int
	for i := 0; i < 286; i++ {
		usedLit = append(usedLit, i)
	}
	var usedDist []int
	for i := 0; i < 30; i++ {
		usedDist = append(usedDist, i)
	}
	big := mkDyn(assign(286, usedLit...), assign(30, usedDist...), 1, 15, 0)
	hist := make([]byte, 40000)
	rand.New(rand.NewSource(3)).Read(hist)
	w = bw{}
	w.stored(false, 0, hist)
	var items2 []item
	for c := 0; c < 29; c++ {
		items2 = append(items2, mt(tLenBase[c], 1+c))
		items2 = append(items2, mt(min(258, tLenBase[c]+1<<uint(tLenXtra[c])-1), 32768))
	}
	for c := 0; c < 30; c++ {
		items2 = append(items2, mt(3, min(tDstBase[c]+1<<uint(tDstXtra[c])-1, 32768)))
	}
	w.dynamic(true, big, items2)
	check(t, "full-alphabet", w.done(0x5), nil)
}

func TestGoldenMixedBlocks(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	var w bw
	w.stored(false, 0x1, []byte("stored "))
	w.fixed(false, lits("fixed "))
	ll := assign(259, 'd', 'y', 'n', 'a', ' ', 256, 257)
	w.dynamic(false, mkDyn(ll, assign(2, 0, 1), 1, 0, 0), append(lits("dyna"), mt(3, 2), lit('n')))
	w.stored(false, 0x7, nil)
	w.fixed(true, append(lits("end"), mt(3, 1+r.Intn(3))))
	s := check(t, "mixed", w.done(0x3), nil)
	st := s.Stats()
	if st.StoredBlocks != 2 || st.FixedBlocks != 2 || st.DynamicBlocks != 1 {
		t.Fatalf("%+v", st)
	}
}

// Real producers: small streams from zlib (several strategies) and GNU gzip
// for the same text, captured once. They exercise encoder choices the Go compressor never makes.
var producerFixtures = map[string]string{
	"zlib-L6":       "2bc94855282ccd4cce56482aca2fcf5348cbaf50c82acd2d2856c82f4b2d5228014ae72456552aa4e4a75b8379442a56484c4fcccc5348cc4b81b0f448d13c646c0200",
	"zlib-huffonly": "05c1890184200c00c156b6021bb86aa24440255104bfea6fa625e5e8795a19abdfc6ec0f4b2ffb895f5a6949d9e47b091e7fb4a41c3d4f2b63f5db98fd61e9653ff14b2b2d299b7c2fc12312251b620189926da025e5e8795a19abdfc6ec0f4b2ffb895f5a6949d9e47b091e7fb4a41c3d4f2b63f5db98fd61e9653ff14b2b2d299b7c2fc12312251b620189926da025e5e8795a19abdfc6ec0f4b2ffb895f5a6949d9e47b091e7fb4a41c3d4f2b63f5db98fd61e9653ff14b2b2d299b7c2fc12312251b620189926de00f",
	"zlib-L0":       "013e01c1fe74686520717569636b2062726f776e20666f78206a756d7073206f76657220746865206c617a7920646f673b2074686520717569636b2062726f776e20666f78206a756d7073206f76657220746865206c617a7920646f6720616761696e20616e6420616761696e2e2074686520717569636b2062726f776e20666f78206a756d7073206f76657220746865206c617a7920646f673b2074686520717569636b2062726f776e20666f78206a756d7073206f76657220746865206c617a7920646f6720616761696e20616e6420616761696e2e2074686520717569636b2062726f776e20666f78206a756d7073206f76657220746865206c617a7920646f673b2074686520717569636b2062726f776e20666f78206a756d7073206f76657220746865206c617a7920646f6720616761696e20616e6420616761696e2e20",
}

func TestProducerFixtures(t *testing.T) {
	for name, h := range producerFixtures {
		b, err := hex.DecodeString(h)
		if err != nil {
			t.Fatal(err)
		}
		check(t, name, b, nil)
	}
}
