package deflate

import (
	"bytes"
	"compress/flate"
	"errors"
	"io"
	"math/rand"
	"testing"
)

func goInflate(b []byte) ([]byte, error) {
	return io.ReadAll(flate.NewReader(bytes.NewReader(b)))
}

func mustFail(t *testing.T, name string, b []byte, opt Options, want error) {
	t.Helper()
	s, err := Parse(b, opt)
	if err == nil {
		t.Fatalf("%s: accepted (%d blocks, %d bytes)", name, len(s.Blocks), len(s.Plain))
	}
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("%s: error %v, want %v", name, err, want)
	}
	// compress/flate must not accept what we reject, except for resource limits and trailing data
	if !errors.Is(err, ErrLimit) && !errors.Is(err, ErrTrailing) {
		if _, gerr := goInflate(b); gerr == nil {
			t.Fatalf("%s: compress/flate accepts a stream we reject (%v)", name, err)
		}
	}
}

func TestMalformedBlockLevel(t *testing.T) {
	mustFail(t, "btype3", []byte{0x07}, Options{}, ErrCorrupt)
	mustFail(t, "empty", nil, Options{}, ErrTruncated)

	var w bw
	w.header(true, 0)
	w.pad(0)
	w.bits(5|0x1234<<16, 32) // NLEN != ^LEN
	w.bits(0, 40)
	mustFail(t, "stored-nlen", w.done(0), Options{}, ErrCorrupt)

	w = bw{}
	w.header(true, 0)
	w.pad(0)
	w.bits(10|(^uint64(10)&0xffff)<<16, 32)
	w.bits(0x4142, 16) // 2 of 10 bytes
	mustFail(t, "stored-truncated", w.done(0), Options{}, ErrTruncated)

	w = bw{}
	w.header(true, 1)
	w.huff(0xC0+(286-280), 8) // symbol 286
	mustFail(t, "fixed-sym286", w.done(0), Options{}, ErrCorrupt)
	w = bw{}
	w.header(true, 1)
	w.huff(0x30+'a', 8)
	w.huff(0xC0+(287-280), 8) // symbol 287
	mustFail(t, "fixed-sym287", w.done(0), Options{}, ErrCorrupt)
	w = bw{}
	w.header(true, 1)
	w.huff(0x30+'a', 8)
	w.huff(1, 7)  // symbol 257: length 3
	w.huff(30, 5) // distance code 30
	w.bits(0, 13)
	mustFail(t, "fixed-dist30", w.done(0), Options{}, ErrCorrupt)

	w = bw{}
	w.fixed(true, append(lits("ab"), mt(3, 3)))
	mustFail(t, "dist-too-far", w.done(0), Options{}, ErrCorrupt)
	w = bw{}
	w.fixed(true, []item{mt(3, 1)})
	mustFail(t, "match-first", w.done(0), Options{}, ErrCorrupt)
	w = bw{}
	w.stored(false, 0, make([]byte, 32767))
	w.fixed(true, []item{mt(3, 32768)})
	mustFail(t, "window-edge", w.done(0), Options{}, ErrCorrupt)

	w = bw{}
	w.header(true, 1)
	w.huff(0x30+'a', 8) // no end-of-block
	mustFail(t, "no-eob", w.done(0), Options{}, ErrTruncated)

	w = bw{}
	w.fixed(true, lits("a"))
	mustFail(t, "trailing", append(w.done(0), 0), Options{}, ErrTrailing)
}

func TestMalformedDynamicHeaders(t *testing.T) {
	okLit := assign(259, 'a', 256, 257)
	okDist := assign(1, 0)
	good := mkDyn(okLit, okDist, 0, 0, 0)

	cases := []struct {
		name string
		mod  func(d *dyn)
	}{
		{"hlit-287", func(d *dyn) { d.hlit = 30 }},
		{"hdist-31", func(d *dyn) { d.hdist = 30 }},
		{"cl-oversubscribed", func(d *dyn) { d.clLens = make([]int, 19); d.clLens[0], d.clLens[1], d.clLens[2] = 1, 1, 1 }},
		{"cl-incomplete", func(d *dyn) { d.clLens = make([]int, 19); d.clLens[0], d.clLens[1] = 2, 2 }},
		{"cl-single-len2", func(d *dyn) { d.clLens = make([]int, 19); d.clLens[0] = 2 }},
		{"cl-empty", func(d *dyn) { d.clLens = make([]int, 19); d.seq = nil }},
		{"repeat16-first", func(d *dyn) {
			d.clLens = assign(19, 16, 0)
			d.seq = []clItem{{16, 0}}
		}},
		{"repeat-overrun", func(d *dyn) {
			d.clLens = assign(19, 18, 0)
			d.seq = []clItem{{18, 127}, {18, 127}, {18, 127}} // 3*138 > 260
		}},
		{"no-eob-code", func(d *dyn) {
			l := append([]int(nil), okLit...)
			l[256] = 0
			l[258] = 2 // keep the code non-empty
			*d = mkDyn(l, okDist, 0, 0, 0)
		}},
		{"lit-oversubscribed", func(d *dyn) {
			l := append([]int(nil), okLit...)
			l['a'], l[256], l[257] = 1, 1, 1
			*d = mkDyn(l, okDist, 0, 0, 0)
		}},
		{"lit-incomplete", func(d *dyn) {
			l := append([]int(nil), okLit...)
			l['a'] = 3
			*d = mkDyn(l, okDist, 0, 0, 0)
		}},
		{"dist-oversubscribed", func(d *dyn) { *d = mkDyn(okLit, []int{1, 1, 1}, 0, 0, 0) }},
		{"dist-incomplete", func(d *dyn) { *d = mkDyn(okLit, []int{0, 2, 2}, 0, 0, 0) }},
	}
	for _, c := range cases {
		d := good
		c.mod(&d)
		var w bw
		w.dynamic(true, d, lits("a"))
		mustFail(t, c.name, w.done(0), Options{}, ErrCorrupt)
	}

	// a match with no distance codes
	d := mkDyn(assign(258, 'a', 256, 257), []int{0}, 0, 0, 0)
	var w bw
	w.header(true, 2)
	w.bits(uint64(d.hlit)|uint64(d.hdist)<<5|uint64(d.hclen)<<10, 14)
	for i := 0; i < d.hclen+4; i++ {
		w.bits(uint64(d.clLens[tClOrder[i]]), 3)
	}
	cc := canon(d.clLens)
	for _, c := range d.seq {
		w.huff(cc[c.sym], uint(d.clLens[c.sym]))
	}
	lc := canon(d.lit)
	w.huff(lc['a'], uint(d.lit['a']))
	w.huff(lc[257], uint(d.lit[257])) // length 3, but there is no distance code to read
	w.bits(0, 8)
	mustFail(t, "match-without-distance-codes", w.done(0), Options{}, ErrCorrupt)
}

func TestLimits(t *testing.T) {
	var w bw
	items := []item{lit('a')}
	for i := 0; i < 5000; i++ {
		items = append(items, mt(258, 1))
	}
	w.fixed(true, items)
	bomb := w.done(0)
	mustFail(t, "output-limit", bomb, Options{MaxOutput: 1 << 20}, ErrLimit)
	if _, err := Parse(bomb, Options{MaxOutput: 2 << 20}); err != nil {
		t.Fatal(err)
	}
	mustFail(t, "token-limit", bomb, Options{MaxTokens: 100}, ErrLimit)

	w = bw{}
	for i := 0; i < 9; i++ {
		w.fixed(false, nil)
	}
	w.fixed(true, nil)
	mustFail(t, "block-limit", w.done(0), Options{MaxBlocks: 5}, ErrLimit)

	w = bw{}
	w.stored(false, 0, make([]byte, 1000))
	w.stored(true, 0, make([]byte, 1000))
	mustFail(t, "stored-output-limit", w.done(0), Options{MaxOutput: 1500}, ErrLimit)

	// trailing bytes: allowed on request, Consumed reports the boundary
	w = bw{}
	w.fixed(true, lits("hi"))
	b := w.done(0)
	s, err := Parse(append(append([]byte(nil), b...), 1, 2, 3), Options{AllowTrailing: true})
	if err != nil || s.Consumed != len(b) {
		t.Fatalf("consumed %v err %v want %d", s, err, len(b))
	}
}

// Every strict prefix of a valid stream is an error.
func TestEveryPrefixFails(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	text := make([]byte, 3000)
	for i := range text {
		text[i] = "abcdefgh"[r.Intn(8)]
	}
	var streams [][]byte
	for _, lvl := range []int{0, 1, 6, 9, flate.HuffmanOnly} {
		var buf bytes.Buffer
		fw, _ := flate.NewWriter(&buf, lvl)
		fw.Write(text)
		fw.Close()
		streams = append(streams, buf.Bytes())
	}
	var w bw
	w.stored(false, 0x5, []byte("abc"))
	w.fixed(false, lits("def"))
	w.dynamic(true, mkDyn(assign(258, 'a', 'b', 256, 257), assign(1, 0), 1, 0, 0), append(lits("ab"), mt(3, 1)))
	streams = append(streams, w.done(0))
	for si, st := range streams {
		check(t, "prefix-base", st, nil)
		for k := 0; k < len(st); k++ {
			if _, err := Parse(st[:k], Options{AllowTrailing: true}); err == nil {
				t.Fatalf("stream %d: prefix of %d/%d bytes accepted", si, k, len(st))
			}
		}
	}
}

// Random byte and bit mutations: accept/reject must agree with compress/flate
// (trailing data aside) and every accepted stream must satisfy the invariants.
func TestMutationParity(t *testing.T) {
	r := rand.New(rand.NewSource(6))
	text := make([]byte, 6000)
	for i := range text {
		text[i] = "the quick brown fox "[r.Intn(20)]
	}
	var seeds [][]byte
	for _, lvl := range []int{0, 1, 6, 9, flate.HuffmanOnly} {
		var buf bytes.Buffer
		fw, _ := flate.NewWriter(&buf, lvl)
		fw.Write(text)
		fw.Close()
		seeds = append(seeds, buf.Bytes())
	}
	accepted, rejected := 0, 0
	for i := 0; i < 6000; i++ {
		b := append([]byte(nil), seeds[i%len(seeds)]...)
		for n := 1 + r.Intn(3); n > 0; n-- {
			switch r.Intn(3) {
			case 0:
				b[r.Intn(len(b))] ^= 1 << uint(r.Intn(8))
			case 1:
				b[r.Intn(len(b))] = byte(r.Intn(256))
			default:
				if len(b) > 8 {
					j := r.Intn(len(b) - 4)
					b = append(b[:j], b[j+1+r.Intn(3):]...)
				}
			}
		}
		s, err := Parse(b, Options{AllowTrailing: true})
		ref, gerr := goInflate(b)
		if err == nil {
			accepted++
			if gerr != nil {
				t.Fatalf("mutation %d: accepted, compress/flate says %v", i, gerr)
			}
			if !bytes.Equal(ref, s.Plain) {
				t.Fatalf("mutation %d: plaintext differs from compress/flate", i)
			}
			re, rerr := s.Bytes()
			if rerr != nil || !bytes.Equal(re, b[:s.Consumed]) {
				t.Fatalf("mutation %d: replay not exact (%v)", i, rerr)
			}
		} else {
			rejected++
			if gerr == nil {
				t.Fatalf("mutation %d: rejected (%v), compress/flate accepts %d bytes", i, err, len(ref))
			}
		}
	}
	t.Logf("accepted %d, rejected %d", accepted, rejected)
}
