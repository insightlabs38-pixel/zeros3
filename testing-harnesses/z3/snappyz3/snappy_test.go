package snappyz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand"
	"testing"
)

// build assembles a stream from raw tag bytes so each form can be exercised directly.
func build(ulen uint64, parts ...[]byte) []byte {
	b := binary.AppendUvarint(nil, ulen)
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func lit(n int, form int) []byte { // literal of n bytes ('a'+i%7) using `form` extra length bytes
	var b []byte
	l1 := uint32(n - 1)
	if form == 0 {
		b = []byte{byte(l1) << 2}
	} else {
		b = []byte{byte(59+form) << 2}
		for k := 0; k < form; k++ {
			b = append(b, byte(l1>>(8*k)))
		}
	}
	for i := 0; i < n; i++ {
		b = append(b, byte('a'+i%7))
	}
	return b
}

func copy1(l, off int) []byte { return []byte{byte(off>>8)<<5 | byte(l-4)<<2 | 1, byte(off)} }
func copy2(l, off int) []byte { return []byte{byte(l-1)<<2 | 2, byte(off), byte(off >> 8)} }
func copy4(l, off int) []byte {
	b := []byte{byte(l-1)<<2 | 3, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(b[1:], uint32(off))
	return b
}

// roundTrip is the property every accepted stream must satisfy.
func roundTrip(t testing.TB, src []byte) ([]byte, []Cmd) {
	dec, cmds, err := Parse(src, Limits{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rec := Marshal(cmds)
	back, err := Unmarshal(rec, int64(len(dec)), Limits{})
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var out bytes.Buffer
	if err := Replay(&out, dec, back); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !bytes.Equal(out.Bytes(), src) {
		t.Fatalf("replay differs from the original stream")
	}
	return dec, cmds
}

func TestForms(t *testing.T) {
	cases := []struct {
		name string
		src  []byte
	}{
		{"empty", build(0)},
		{"literal tag 1", build(1, lit(1, 0))},
		{"literal 60 tag", build(60, lit(60, 0))},
		{"literal 61 1-byte", build(61, lit(61, 1))},
		{"literal 256 1-byte", build(256, lit(256, 1))},
		{"literal 257 2-byte", build(257, lit(257, 2))},
		{"literal 65536 2-byte", build(65536, lit(65536, 2))},
		{"literal 65537 3-byte", build(65537, lit(65537, 3))},
		{"literal nonminimal 5 in 2 bytes", build(5, lit(5, 2))},
		{"literal nonminimal 5 in 4 bytes", build(5, lit(5, 4))},
		{"copy1", build(12, lit(8, 0), copy1(4, 8))},
		{"copy1 max", build(8+11, lit(8, 0), copy1(11, 8))},
		{"copy1 offset 2047", build(2048+4, lit(2048, 2), copy1(4, 2047))},
		{"copy2 len 1", build(9, lit(8, 0), copy2(1, 3))},
		{"copy2 len 64", build(72, lit(8, 0), copy2(64, 8))},
		{"copy2 offset 65535", build(65536+5, lit(65536, 2), copy2(5, 65535))},
		{"copy4", build(13, lit(8, 0), copy4(5, 8))},
		{"copy4 far", build(70000+9, lit(70000, 3), copy4(9, 69999))},
		{"overlap offset 1", build(1+64, lit(1, 0), copy2(64, 1))},
		{"overlap offset 3 copy1", build(3+11, lit(3, 0), copy1(11, 3))},
		{"mixed", build(8+4+3+10, lit(8, 0), copy1(4, 4), lit(3, 0), copy2(10, 15))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { roundTrip(t, c.src) })
	}
}

func TestRejects(t *testing.T) {
	good := build(12, lit(8, 0), copy1(4, 8))
	cases := []struct {
		name string
		src  []byte
	}{
		{"nil", nil},
		{"non-minimal prefix", []byte{0x80, 0x00}},
		{"six-byte prefix", []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x01}},
		{"prefix above 2^32", []byte{0xff, 0xff, 0xff, 0xff, 0x1f}},
		{"truncated prefix", []byte{0x80}},
		{"offset zero copy1", build(8, lit(4, 0), []byte{1 | 0<<2, 0})},
		{"offset zero copy2", build(8, lit(4, 0), copy2(4, 0))},
		{"offset beyond output", build(8, lit(4, 0), copy2(4, 5))},
		{"copy before any output", build(4, copy2(4, 1))},
		{"declared too long", build(13, lit(8, 0), copy1(4, 8))},
		{"declared too short", build(11, lit(8, 0), copy1(4, 8))},
		{"literal overruns declared", build(3, lit(4, 0))},
		{"copy overruns declared", build(10, lit(8, 0), copy1(4, 8))},
		{"trailing garbage after full output", append(append([]byte(nil), good...), 0x00)},
		{"literal body truncated", build(8, lit(8, 0)[:5])},
		{"literal length truncated", build(300, []byte{61 << 2})},
		{"copy1 truncated", build(12, lit(8, 0), []byte{1})},
		{"copy2 truncated", build(12, lit(8, 0), []byte{2, 1})},
		{"copy4 truncated", build(12, lit(8, 0), []byte{3, 1, 0, 0})},
		{"literal 4-byte length overflow", build(8, []byte{63 << 2, 0xff, 0xff, 0xff, 0xff})},
		{"bomb: declared 2^31 from 3 bytes", build(1<<31, []byte{0})},
	}
	for _, c := range cases {
		if _, _, err := Parse(c.src, Limits{}); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	// every strict prefix of a good stream is rejected
	for i := 0; i < len(good); i++ {
		if _, _, err := Parse(good[:i], Limits{}); err == nil {
			t.Errorf("prefix %d accepted", i)
		}
	}
	// limits
	if _, _, err := Parse(good, Limits{MaxDecoded: 11}); !errors.Is(err, ErrLimit) {
		t.Errorf("MaxDecoded: %v", err)
	}
	if _, _, err := Parse(good, Limits{MaxCommands: 1}); !errors.Is(err, ErrLimit) {
		t.Errorf("MaxCommands: %v", err)
	}
}

func TestRecipeAndReplayRejects(t *testing.T) {
	dec, cmds := roundTrip(t, build(12, lit(8, 0), copy1(4, 8)))
	rec := Marshal(cmds)
	// every truncation and every single-byte mutation of the recipe either fails to
	// unmarshal, fails the replay check, or still describes a stream that decodes to dec.
	for i := 0; i <= len(rec); i++ {
		if i < len(rec) {
			if _, err := Unmarshal(rec[:i], int64(len(dec)), Limits{}); err == nil {
				t.Errorf("truncated recipe %d accepted", i)
			}
		}
	}
	for i := range rec {
		for v := 0; v < 256; v++ {
			m := append([]byte(nil), rec...)
			m[i] = byte(v)
			cm, err := Unmarshal(m, int64(len(dec)), Limits{})
			if err != nil {
				continue
			}
			var out bytes.Buffer
			if err := Replay(&out, dec, cm); err != nil {
				continue
			}
			d2, _, err := Parse(out.Bytes(), Limits{})
			if err != nil || !bytes.Equal(d2, dec) {
				t.Fatalf("mutated recipe produced an invalid stream")
			}
		}
	}
	if _, err := Unmarshal(append(append([]byte(nil), rec...), 0), int64(len(dec)), Limits{}); err == nil {
		t.Error("trailing recipe bytes accepted")
	}
	if _, err := Unmarshal(rec, 1<<40, Limits{}); !errors.Is(err, ErrLimit) {
		t.Errorf("hostile decoded length: %v", err)
	}
	// a copy that does not match the child is refused
	bad := append([]byte(nil), dec...)
	bad[10] ^= 1
	if err := Replay(new(bytes.Buffer), bad, cmds); !errors.Is(err, ErrMismatch) {
		t.Errorf("copy differing from child: %v", err)
	}
	// commands that do not cover the child
	if err := Replay(new(bytes.Buffer), dec[:11], cmds); err == nil {
		t.Error("short child accepted")
	}
	if err := Replay(new(bytes.Buffer), append(dec, 'x'), cmds); err == nil {
		t.Error("long child accepted")
	}
}

type failAfter struct{ left int }

func (f *failAfter) Write(p []byte) (int, error) {
	if len(p) > f.left {
		return 0, errors.New("sink failure")
	}
	f.left -= len(p)
	return len(p), nil
}

func TestWriterFailure(t *testing.T) {
	src := build(12, lit(8, 0), copy1(4, 8))
	dec, cmds, _ := Parse(src, Limits{})
	for n := 0; n < len(src); n++ {
		if err := Replay(&failAfter{n}, dec, cmds); err == nil {
			t.Errorf("sink failing after %d bytes was not reported", n)
		}
	}
}

// TestRandomStreams builds streams from random command lists (all forms, overlaps) and
// checks the round trip plus that a different valid encoding of the same bytes is kept
// distinct (the recipe, not the decoded bytes, determines the stream).
func TestRandomStreams(t *testing.T) {
	r := rand.New(rand.NewSource(30))
	for it := 0; it < 300; it++ {
		var out, stream []byte
		for len(out) < 1+r.Intn(20000) {
			if len(out) > 0 && r.Intn(2) == 0 {
				off := 1 + r.Intn(len(out))
				switch r.Intn(3) {
				case 0:
					if off < 2048 {
						l := 4 + r.Intn(8)
						stream = append(stream, copy1(l, off)...)
						for i := 0; i < l; i++ {
							out = append(out, out[len(out)-off])
						}
						continue
					}
					fallthrough
				case 1:
					if off < 65536 {
						l := 1 + r.Intn(64)
						stream = append(stream, copy2(l, off)...)
						for i := 0; i < l; i++ {
							out = append(out, out[len(out)-off])
						}
						continue
					}
					fallthrough
				default:
					l := 1 + r.Intn(64)
					stream = append(stream, copy4(l, off)...)
					for i := 0; i < l; i++ {
						out = append(out, out[len(out)-off])
					}
				}
			} else {
				n := 1 + r.Intn(300)
				form := 0
				if m := int(minForm(uint32(n))); m > 0 || r.Intn(4) == 0 {
					form = max(m, 1+r.Intn(4))
				}
				l := lit(n, form)
				stream = append(stream, l...)
				out = append(out, l[len(l)-n:]...)
			}
		}
		src := append(binary.AppendUvarint(nil, uint64(len(out))), stream...)
		dec, _ := roundTrip(t, src)
		if !bytes.Equal(dec, out) {
			t.Fatal("decoded bytes differ from the model")
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add(build(12, lit(8, 0), copy1(4, 8)))
	f.Add(build(1+64, lit(1, 0), copy2(64, 1)))
	f.Add(build(13, lit(8, 0), copy4(5, 8)))
	f.Add(build(61, lit(61, 1)))
	f.Fuzz(func(t *testing.T, src []byte) {
		lim := Limits{MaxDecoded: 1 << 20, MaxCommands: 1 << 16}
		dec, cmds, err := Parse(src, lim)
		if err != nil {
			return
		}
		rec := Marshal(cmds)
		back, err := Unmarshal(rec, int64(len(dec)), lim)
		if err != nil {
			t.Fatalf("own recipe rejected: %v", err)
		}
		var out bytes.Buffer
		if err := Replay(&out, dec, back); err != nil {
			t.Fatalf("replay: %v", err)
		}
		if !bytes.Equal(out.Bytes(), src) {
			t.Fatal("replay differs from the accepted stream")
		}
		d2, _, err := Parse(out.Bytes(), lim)
		if err != nil || !bytes.Equal(d2, dec) {
			t.Fatal("replayed stream decodes differently")
		}
	})
}

// TestReferenceEncoders checks that both predictors emit valid streams that decode to
// their input, across the size boundaries of the algorithm, and that Parse/Replay give
// them back byte for byte.
func TestReferenceEncoders(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	mk := func(n int, kind int) []byte {
		b := make([]byte, n)
		switch kind {
		case 0:
			r.Read(b)
		case 1: // highly repetitive
			for i := range b {
				b[i] = byte(i % 7)
			}
		default: // text-like with repeats
			words := []string{"alpha ", "bravo ", "key-0001", "value=", "{\"id\":"}
			for i := 0; i < n; {
				i += copy(b[i:], words[r.Intn(len(words))])
			}
		}
		return b
	}
	sizes := []int{0, 1, 2, 14, 15, 16, 17, 31, 32, 33, 63, 64, 100, 255, 256, 257, 1000, 4096, 4097, 5000, 8192, 8193, 16383, 16384, 16385, 40000, 65535, 65536}
	for _, enc := range []struct {
		name string
		f    func([]byte) []byte
	}{{"go", EncodeRef}, {"cpp", EncodeCPP}, {"cpp15", EncodeCPP15}} {
		for _, n := range sizes {
			for kind := 0; kind < 3; kind++ {
				src := mk(n, kind)
				out := enc.f(src)
				dec, _, err := Parse(out, Limits{})
				if err != nil || !bytes.Equal(dec, src) {
					t.Fatalf("%s n=%d kind=%d: %v", enc.name, n, kind, err)
				}
				roundTrip(t, out)
			}
		}
	}
}
