package ir

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"testing"

	"zeros3-z3/corpus"
	"zeros3-z3/deflate"
)

// sample builds deterministic compressible plaintext: words, records, repeats.
func sample(n int, seed int64) []byte {
	r := rand.New(rand.NewSource(seed))
	words := make([]string, 400)
	for i := range words {
		w := make([]byte, 2+r.Intn(9))
		for j := range w {
			w[j] = byte('a' + r.Intn(26))
		}
		words[i] = string(w)
	}
	var b bytes.Buffer
	for b.Len() < n {
		switch r.Intn(10) {
		case 0:
			fmt.Fprintf(&b, "\n%08x,%d,", r.Uint32(), r.Intn(1000))
		case 1:
			if b.Len() > 4096 {
				s := b.Bytes()
				o := r.Intn(len(s) - 200)
				b.Write(append([]byte(nil), s[o:o+20+r.Intn(150)]...))
			}
		default:
			b.WriteString(words[r.Intn(len(words))])
			b.WriteByte(' ')
		}
	}
	return b.Bytes()[:n]
}

func goDeflate(plain []byte, level int) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, level)
	w.Write(plain)
	w.Close()
	return buf.Bytes()
}

type stream struct {
	name string
	raw  []byte
}

func testStreams(t testing.TB) []stream {
	plain := sample(700<<10, 1)
	noise := make([]byte, 90<<10)
	rand.New(rand.NewSource(2)).Read(noise)
	mixed := append(append(append([]byte(nil), plain[:200<<10]...), noise...), plain[200<<10:]...)
	var out []stream
	for _, lv := range []int{0, 1, 2, 4, 6, 9, -2} {
		out = append(out, stream{fmt.Sprintf("go-L%d", lv), goDeflate(plain, lv)})
	}
	out = append(out,
		stream{"go-L6-mixed", goDeflate(mixed, 6)},
		stream{"go-L1-noise", goDeflate(noise, 1)},
		stream{"fixed", corpus.DeflateFixed(plain)},
		stream{"empty", goDeflate(nil, 6)},
		stream{"tiny", goDeflate([]byte("a"), 6)},
		stream{"repeat", goDeflate(bytes.Repeat([]byte("abcabcabc"), 40000), 9)},
	)
	return out
}

// testCodecs are the codecs the shared tests exercise (predict_test.go appends Pred).
var testCodecs = []Codec{E1, E1Flate, E2}

var policies = []Policy{
	{PStream, 0}, {PBlock, 0}, {PGroup, 64 << 10}, {PContentBlocks, 64 << 10}, {PTokRun, 4 << 10}, {PTokRun, 32 << 10},
}

func roundtrip(t *testing.T, raw []byte, cfg Config) *Package {
	t.Helper()
	s, err := deflate.Parse(raw, deflate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pk, err := Encode(s, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = Reconstruct(&out, pk.Skeleton, func(i int) ([]byte, error) { return pk.Units[i], nil }, s.Plain, DecodeOptions{VerifyMatches: true})
	if err != nil {
		t.Fatalf("%v/%v: %v", cfg.Codec, cfg.Policy, err)
	}
	if out.Len() != len(raw) || sha256.Sum256(out.Bytes()) != sha256.Sum256(raw) || !bytes.Equal(out.Bytes(), raw) {
		t.Fatalf("%v/%v: replay differs (%d vs %d bytes)", cfg.Codec, cfg.Policy, out.Len(), len(raw))
	}
	// the decoded stream must also be a valid DEFLATE stream with the same plaintext
	if s2, err := deflate.Parse(out.Bytes(), deflate.Options{}); err != nil || !bytes.Equal(s2.Plain, s.Plain) {
		t.Fatalf("reparse: %v", err)
	}
	return pk
}

func TestCodecRoundtrip(t *testing.T) {
	for _, st := range testStreams(t) {
		for _, c := range []Codec{E1, E1Flate, E2} {
			for _, p := range policies {
				if testing.Short() && (c == E1Flate && p.Kind != PTokRun) {
					continue
				}
				t.Run(fmt.Sprintf("%s/%v/%v", st.name, c, p), func(t *testing.T) {
					roundtrip(t, st.raw, Config{Codec: c, Policy: p})
				})
			}
		}
	}
}

func TestLengthAndDistanceCodes(t *testing.T) {
	for l := 3; l <= 258; l++ {
		sym, extra := lengthCode(l)
		if tk := deflate.Match(sym, extra, 0, 0); tk.Length() != l || !canonical(deflate.Match(sym, extra, 0, 0)) {
			t.Fatalf("length %d -> sym %d extra %d -> %d", l, sym, extra, tk.Length())
		}
	}
	// the non-canonical spelling of 258 must be recognised and escape to explicit coding
	if canonical(deflate.Match(284, 31, 0, 0)) {
		t.Fatal("284+31 reported canonical")
	}
	for d := 1; d <= 32768; d++ {
		dc, de := distCodeOf(d)
		if tk := deflate.Match(257, 0, dc, de); tk.Distance() != d {
			t.Fatalf("distance %d -> code %d extra %d -> %d", d, dc, de, tk.Distance())
		}
	}
}

// A stream that spells length 258 as symbol 284+31 (legal, and rare) must round
// trip through every codec; the predictor reaches it through its explicit escape.
func TestNonCanonicalLengthRoundtrip(t *testing.T) {
	plain := bytes.Repeat([]byte("0123456789abcdef"), 100)
	dc, de := distCodeOf(16)
	toks := []deflate.Token{}
	for _, c := range plain[:16] {
		toks = append(toks, deflate.Literal(c))
	}
	pos := 16
	for pos+258 <= len(plain) {
		toks = append(toks, deflate.Match(284, 31, dc, de))
		pos += 258
	}
	for ; pos < len(plain); pos++ {
		toks = append(toks, deflate.Literal(plain[pos]))
	}
	var raw []byte
	for pad := 0; pad < 8 && raw == nil; pad++ {
		s := &deflate.Stream{Plain: plain, TailPadLen: uint8(pad), Blocks: []deflate.Block{{Final: true, Kind: deflate.Fixed, PlainLen: len(plain), Tokens: toks}}}
		if b, err := s.Bytes(); err == nil {
			raw = b
		}
	}
	if raw == nil {
		t.Fatal("could not build the stream")
	}
	s, err := deflate.Parse(raw, deflate.Options{})
	if err != nil || s.Stats().NonCanonicalLengths == 0 {
		t.Fatalf("parse: %v, noncanonical=%d", err, s.Stats().NonCanonicalLengths)
	}
	for _, c := range testCodecs {
		for _, p := range []Policy{{PStream, 0}, {PTokRun, 1 << 10}} {
			roundtrip(t, raw, Config{Codec: c, Policy: p, Profile: 1})
		}
	}
}
