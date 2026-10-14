package ir

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"zeros3-z3/deflate"
)

// Units carry no literal byte values: rewriting every literal of a stream
// (and its plaintext) leaves skeleton and units byte-identical.
func TestUnitsExcludeLiteralValues(t *testing.T) {
	raw := goDeflate(sample(120<<10, 11), 6)
	s, err := deflate.Parse(raw, deflate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := deflate.Parse(raw, deflate.Options{})
	for bi := range s2.Blocks {
		b := &s2.Blocks[bi]
		var present []int
		for sym := 0; sym < 256; sym++ {
			if b.Kind == deflate.Fixed || (b.Kind == deflate.Dynamic && sym < len(b.LitLens) && b.LitLens[sym] > 0) {
				present = append(present, sym)
			}
		}
		next := map[byte]byte{}
		for i, sym := range present {
			next[byte(sym)] = byte(present[(i+1)%len(present)])
		}
		pos := b.PlainOff
		for i, tk := range b.Tokens {
			if tk.IsLiteral() {
				v := next[byte(tk.Sym())] // rotate within the block's literal alphabet
				s2.Plain[pos] = v
				b.Tokens[i] = deflate.Literal(v)
				pos++
			} else {
				pos += tk.Length()
			}
		}
	}
	// (the rewritten stream is not replayable: other literal lengths change the
	// bit layout; units are compared through the internal unit builder)
	cs := cuts(s, flatten(s), Policy{PTokRun, 4 << 10}) // cut points depend on plaintext, so fix them
	units := func(s *deflate.Stream, enc func([]Run) []byte) [][]byte {
		f := flatten(s)
		starts := append([]int{0}, cs...)
		ends := append(append([]int(nil), cs...), len(f.toks))
		var out [][]byte
		for i := range starts {
			runs, _ := toRuns(f.toks[starts[i]:ends[i]])
			out = append(out, enc(runs))
		}
		return out
	}
	for name, enc := range map[string]func([]Run) []byte{"E1": encodeE1, "E2": func(r []Run) []byte { return encodeE2(r, nil) }} {
		a, b := units(s, enc), units(s2, enc)
		if len(a) != len(b) || len(a) < 5 {
			t.Fatalf("%s: unit counts %d vs %d", name, len(a), len(b))
		}
		for i := range a {
			if !bytes.Equal(a[i], b[i]) {
				t.Fatalf("%s: unit %d depends on literal values", name, i)
			}
		}
	}
}

// Encoding is deterministic.
func TestEncodeDeterministic(t *testing.T) {
	raw := goDeflate(sample(80<<10, 5), 9)
	s, _ := deflate.Parse(raw, deflate.Options{})
	for _, c := range testCodecs {
		a, _ := Encode(s, Config{Codec: c, Policy: Policy{PTokRun, 2 << 10}, Profile: 1})
		b, _ := Encode(s, Config{Codec: c, Policy: Policy{PTokRun, 2 << 10}, Profile: 1})
		if !bytes.Equal(a.Skeleton, b.Skeleton) || len(a.Units) != len(b.Units) {
			t.Fatalf("%v: nondeterministic skeleton/unit count", c)
		}
		for i := range a.Units {
			if !bytes.Equal(a.Units[i], b.Units[i]) {
				t.Fatalf("%v: unit %d differs between runs", c, i)
			}
		}
	}
}

func unitSet(t *testing.T, plain []byte, level int, pol Policy, codec Codec) map[[32]byte]int {
	s, err := deflate.Parse(goDeflate(plain, level), deflate.Options{})
	if err != nil {
		t.Fatal(err)
	}
	pk, err := Encode(s, Config{Codec: codec, Policy: pol, Profile: AutoProfile})
	if err != nil {
		t.Fatal(err)
	}
	m := map[[32]byte]int{}
	for _, u := range pk.Units {
		m[sha256.Sum256(u)] = len(u)
	}
	return m
}

// An insertion in the middle must leave the units well before and well after
// it shared; a block-per-unit policy cannot do that for large blocks.
func TestUnitsSurviveInsertion(t *testing.T) {
	base := sample(1500<<10, 21)
	edit := append(append(append([]byte(nil), base[:700<<10]...), []byte("<<inserted text that was not here before>>")...), base[700<<10:]...)
	for _, c := range testCodecs {
		if c == E1 || c == E1Flate {
			continue
		}
		a := unitSet(t, base, 9, Policy{PTokRun, 8 << 10}, c)
		b := unitSet(t, edit, 9, Policy{PTokRun, 8 << 10}, c)
		kept, total := 0, 0
		for k, n := range b {
			total += n
			if _, ok := a[k]; ok {
				kept += n
			}
		}
		t.Logf("%v: %.1f%% of unit bytes survive a mid-stream insertion (%d units)", c, 100*float64(kept)/float64(total), len(b))
		if float64(kept) < 0.6*float64(total) {
			t.Errorf("%v: only %.1f%% of unit bytes survive", c, 100*float64(kept)/float64(total))
		}
	}
}
