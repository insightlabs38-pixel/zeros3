package deflate

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/hex"
	"errors"
	"io"
	"testing"

	"zeros3-z3/corpus"
)

// seedStreams are small valid streams: handcrafted edge fixtures, real producer
// captures, Go compressor output at several levels and corpus streams.
func seedStreams() [][]byte {
	var out [][]byte
	add := func(b []byte) { out = append(out, b) }
	var w bw
	w.stored(true, 0x15, []byte("abc"))
	add(w.done(0))
	w = bw{}
	w.fixed(true, nil)
	add(w.done(0))
	w = bw{}
	w.fixed(true, []item{lit('a'), {lit: -1, length: 258, dist: 1, forceSym: 284, forceExtra: 31}})
	add(w.done(0x7f))
	w = bw{}
	w.stored(false, 0x3, []byte("xy"))
	w.fixed(false, append(lits("ab"), mt(5, 2)))
	w.dynamic(false, mkDyn(assign(259, 'a', 'b', 256, 257, 258), assign(5, 2, 4), 1, 15, 3), append(lits("abab"), mt(3, 3), mt(4, 6)))
	w.stored(true, 0, nil)
	add(w.done(0x2))
	w = bw{}
	w.dynamic(true, mkDyn(assign(257, 'x', 'y', 256), []int{0}, 2, 0, 0), lits("xyyx"))
	add(w.done(0))
	for _, h := range producerFixtures {
		b, _ := hex.DecodeString(h)
		add(b)
	}
	text := corpus.Text(corpus.NewRNG(9, "seed"), 4000)
	for _, lvl := range []int{0, 1, 6, 9, flate.HuffmanOnly} {
		add(goDeflate(text, lvl))
	}
	add(corpus.DeflateFixed(text))
	return out
}

// invariants checks everything that must hold for any input; returns whether it parsed.
func invariants(t *testing.T, data []byte) bool {
	t.Helper()
	opt := Options{MaxOutput: 1 << 22, MaxTokens: 1 << 21, MaxBlocks: 1 << 14, AllowTrailing: true}
	s, err := Parse(data, opt)
	ref, gerr := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(data)), int64(opt.MaxOutput)+1))
	if err != nil {
		if errors.Is(err, ErrLimit) || len(ref) > opt.MaxOutput {
			return false
		}
		if gerr == nil {
			t.Fatalf("rejected (%v) but compress/flate accepts: %d bytes", err, len(ref))
		}
		return false
	}
	if gerr != nil {
		t.Fatalf("accepted but compress/flate rejects: %v", gerr)
	}
	if !bytes.Equal(ref, s.Plain) {
		t.Fatalf("plaintext differs from compress/flate (%d vs %d bytes)", len(ref), len(s.Plain))
	}
	re, rerr := s.Bytes()
	if rerr != nil {
		t.Fatalf("replay failed: %v", rerr)
	}
	if !bytes.Equal(re, data[:s.Consumed]) {
		t.Fatalf("replay differs from the %d consumed input bytes", s.Consumed)
	}
	st := s.Stats()
	if st.PlainBytes != len(s.Plain) || st.Tokens != st.Literals+st.Matches || st.Blocks != len(s.Blocks) {
		t.Fatalf("stats inconsistent: %+v", st)
	}
	// strict mode accepts exactly when nothing trails
	_, serr := Parse(data, Options{MaxOutput: opt.MaxOutput, MaxTokens: opt.MaxTokens, MaxBlocks: opt.MaxBlocks})
	if (serr == nil) != (s.Consumed == len(data)) {
		t.Fatalf("strict/lenient disagree: consumed %d of %d, strict err %v", s.Consumed, len(data), serr)
	}
	return true
}

func FuzzParse(f *testing.F) {
	for _, b := range seedStreams() {
		f.Add(b)
	}
	f.Add([]byte{})
	f.Add([]byte{0x07})
	f.Fuzz(func(t *testing.T, data []byte) { invariants(t, data) })
}

// FuzzMutatedSeed applies structured edits to a valid stream, which reaches
// deeper than random bytes (dynamic headers, token streams).
func FuzzMutatedSeed(f *testing.F) {
	seeds := seedStreams()
	for i := range seeds {
		f.Add(uint8(i), uint16(0), uint8(0), uint8(0))
	}
	f.Fuzz(func(t *testing.T, which uint8, pos uint16, flip uint8, trunc uint8) {
		b := append([]byte(nil), seeds[int(which)%len(seeds)]...)
		if len(b) == 0 {
			return
		}
		b[int(pos)%len(b)] ^= flip
		if trunc%4 == 0 {
			b = b[:len(b)-int(trunc/4)%len(b)]
		}
		invariants(t, b)
	})
}

func FuzzGzip(f *testing.F) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Name = "n"
	w.Comment = "c"
	w.Extra = []byte{1}
	w.Write([]byte("hello hello hello"))
	w.Close()
	f.Add(buf.Bytes())
	f.Add([]byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 0, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		opt := Options{MaxOutput: 1 << 22, MaxTokens: 1 << 21, MaxBlocks: 1 << 14}
		g, rest, err := ParseGzip(data, opt, false)
		if err != nil {
			return
		}
		b, rerr := g.Bytes()
		if rerr != nil || !bytes.Equal(b, data[:len(data)-len(rest)]) {
			t.Fatalf("gzip replay not exact: %v", rerr)
		}
		zr, zerr := gzip.NewReader(bytes.NewReader(data[:len(data)-len(rest)]))
		if zerr != nil {
			t.Fatalf("compress/gzip rejects an accepted member: %v", zerr)
		}
		ref, zerr := io.ReadAll(zr)
		if zerr != nil || !bytes.Equal(ref, g.Stream.Plain) {
			t.Fatalf("compress/gzip disagrees: %v", zerr)
		}
	})
}

func TestSeedsAreValid(t *testing.T) {
	seeds := seedStreams()
	if len(seeds) < 14 {
		t.Fatalf("only %d seeds", len(seeds))
	}
	for i, b := range seeds {
		if !invariants(t, b) {
			t.Fatalf("seed %d rejected", i)
		}
	}
}
