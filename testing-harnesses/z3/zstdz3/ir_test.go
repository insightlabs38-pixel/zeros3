package zstdz3

import (
	"bytes"
	"errors"
	"testing"
)

// richStream returns a stream exercising raw, RLE, Huffman (1 and 4 stream, treeless),
// model-coded sequences and a checksum, built without any external tool.
func richStream(t testing.TB) (comp, plain []byte) {
	f := newFx(t, true)
	f.raw(sampleText(500, 4), false)
	f.rle('x', 200, false)
	lits := func(n int) []byte {
		b := make([]byte, n)
		for i := range b {
			b[i] = byte('a' + (i*7+i/3)%7)
		}
		return b
	}
	trip := []seqRec{{ll: 4, ml: 7, of: 4 + 30}, {ll: 0, ml: 5, of: 2}, {ll: 6, ml: 9, of: 4 + 400}, {ll: 2, ml: 4, of: 3}, {ll: 1, ml: 12, of: 4 + 5}}
	f.compressed(BlockRec{LitMode: litComp, LitForm: 0, TreeDesc: fxTree}, lits(60), trip, false)
	f.compressed(BlockRec{LitMode: litTreeles, LitForm: 2}, lits(300), trip, false)
	f.compressed(BlockRec{LitMode: litRaw, LitForm: 1}, lits(100), trip[:2], true)
	return f.finish()
}

func parseKeep(t testing.TB, comp []byte, opt Options) (*Result, []byte, []byte) {
	var plain, units bytes.Buffer
	res, err := Parse(bytes.NewReader(comp), &plain, &units, opt)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return res, plain.Bytes(), units.Bytes()
}

func replayOf(res *Result, units, plain []byte) ([]byte, error) {
	var out bytes.Buffer
	_, err := Replay(&out, res.Raw, bytes.NewReader(units), bytes.NewReader(plain), Limits{})
	return out.Bytes(), err
}

func TestSeqModelAndExplicitBothReplay(t *testing.T) {
	comp, want := richStream(t)
	for _, nomodel := range []bool{false, true} {
		res, plain, units := parseKeep(t, comp, Options{NoSeqModel: nomodel})
		if !bytes.Equal(plain, want) {
			t.Fatal("plaintext")
		}
		out, err := replayOf(res, units, plain)
		if err != nil || !bytes.Equal(out, comp) {
			t.Fatalf("nomodel=%v: replay: %v", nomodel, err)
		}
		model := 0
		for _, b := range res.Skeleton.Frames[0].Blocks {
			if b.Model {
				model++
			}
		}
		if nomodel && model != 0 || !nomodel && model == 0 {
			t.Fatalf("nomodel=%v but %d model-coded blocks", nomodel, model)
		}
	}
}

func TestIRCanonicalAndRejects(t *testing.T) {
	comp, _ := richStream(t)
	res, _, _ := parseKeep(t, comp, Options{})
	raw := res.Raw
	sk, err := Unmarshal(raw, Limits{})
	if err != nil || !bytes.Equal(sk.Marshal(), raw) {
		t.Fatalf("Marshal(Unmarshal(x)) != x: %v", err)
	}
	for n := 0; n < len(raw); n++ {
		if _, err := Unmarshal(raw[:n], Limits{}); err == nil {
			t.Fatalf("truncation to %d accepted", n)
		}
	}
	if _, err := Unmarshal(append(append([]byte(nil), raw...), 0), Limits{}); err == nil {
		t.Fatal("trailing byte accepted")
	}
	bad := func(name string, f func(b []byte) []byte) {
		t.Helper()
		if _, err := Unmarshal(f(append([]byte(nil), raw...)), Limits{}); err == nil || !errors.Is(err, ErrIR) {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad("version", func(b []byte) []byte { b[2] = 9; return b })
	bad("magic", func(b []byte) []byte { b[0] = 'X'; return b })
	bad("frame count", func(b []byte) []byte { b[3] = 0x80; return append(b[:3], append([]byte{0x80, 0x01}, b[4:]...)...) }) // non-minimal / wrong
	bad("frame kind", func(b []byte) []byte { b[4] = 7; return b })
	bad("reserved header bit", func(b []byte) []byte { b[6] |= 8; return b })
	// every single-byte corruption must either be rejected or still round-trip canonically
	for i := range raw {
		for _, v := range []byte{0, 1, 0x7f, 0x80, 0xff} {
			b := append([]byte(nil), raw...)
			if b[i] == v {
				continue
			}
			b[i] = v
			if sk, err := Unmarshal(b, Limits{}); err == nil && !bytes.Equal(sk.Marshal(), b) {
				t.Fatalf("byte %d=%#x accepted but not canonical", i, v)
			}
		}
	}
}

func TestNonMinimalVarintRejected(t *testing.T) {
	comp, _ := richStream(t)
	res, _, _ := parseKeep(t, comp, Options{})
	// the block count after the 2-byte frame prefix is a one-byte varint; widen it
	raw := res.Raw
	i := 3 + 1 + 2 + int(raw[5]) // magic+frames+kind+hdrlen... walk to the block count
	if raw[i] >= 0x80 {
		t.Skip("layout assumption")
	}
	wide := append(append(append([]byte(nil), raw[:i]...), raw[i]|0x80, 0x00), raw[i+1:]...)
	if _, err := Unmarshal(wide, Limits{}); err == nil {
		t.Fatal("non-minimal varint accepted")
	}
}

func TestReplayDetectsTampering(t *testing.T) {
	comp, _ := richStream(t)
	res, plain, units := parseKeep(t, comp, Options{})
	flip := func(b []byte, i int) []byte { c := append([]byte(nil), b...); c[i] ^= 0x10; return c }
	if _, err := replayOf(res, units, flip(plain, len(plain)/2)); err == nil {
		t.Error("tampered plaintext replayed")
	}
	if _, err := replayOf(res, units, plain[:len(plain)-1]); err == nil {
		t.Error("short plaintext replayed")
	}
	if _, err := replayOf(res, units, append(append([]byte(nil), plain...), 0)); err == nil {
		t.Error("long plaintext replayed")
	}
	if _, err := replayOf(res, units[:len(units)-1], plain); err == nil {
		t.Error("short unit stream replayed")
	}
	if _, err := replayOf(res, append(append([]byte(nil), units...), 0), plain); err == nil {
		t.Error("long unit stream replayed")
	}
	for i := range units {
		out, err := replayOf(res, flip(units, i), plain)
		if err == nil && bytes.Equal(out, comp) {
			t.Fatalf("unit byte %d is irrelevant to the replay", i)
		}
	}
}

// TestFallbackBlock makes a compressed block valid for the decoder but not reproducible
// by the replay encoders (a non-minimal Number_of_Sequences form), and checks that it
// becomes a fallback unit, is charged, keeps later blocks (which reuse its tree)
// exact, and is reported.
func TestFallbackBlock(t *testing.T) {
	f := newFx(t, false)
	f.raw(sampleText(300, 8), false)
	lits := bytes.Repeat([]byte("abcdefgabc"), 8)
	trip := []seqRec{{ll: 4, ml: 7, of: 4 + 30}, {ll: 2, ml: 5, of: 4 + 100}}
	// first block introduces the tree with the oddly-coded count
	f.hist.reserve(maxBlock)
	start := len(f.hist.buf)
	if err := f.hist.exec(trip, lits[:6], &f.rep, maxBlock); err != nil {
		t.Fatal(err)
	}
	out := append([]byte(nil), f.hist.buf[start:]...)
	rec := BlockRec{Type: BlockCompressed, LitMode: litComp, TreeDesc: fxTree, Regen: 6, NSeq: 2, DecLen: uint32(len(out))}
	content, err := buildCompressed(&f.st, &rec, out, trip)
	if err != nil {
		t.Fatal(err)
	}
	var h litHdr
	var st2 codingState
	used, err := scanLiterals(&st2, content, maxBlock, &h)
	if err != nil {
		t.Fatal(err)
	}
	odd := append(append([]byte(nil), content[:used]...), 128, 2) // two-byte form of the count 2
	odd = append(odd, content[used+1:]...)
	if _, err := scanBlock(&f.st, odd, maxBlock); err != nil {
		t.Fatalf("odd block must stay decodable: %v", err)
	}
	f.plain.Write(out)
	f.block(false, BlockCompressed, odd, len(odd))
	// second block: treeless, reusing the tree the fallback block introduced
	f.compressed(BlockRec{LitMode: litTreeles, LitForm: 0}, lits[:20], []seqRec{{ll: 5, ml: 6, of: 4 + 50}}, true)
	comp, plain := f.finish()
	res := checkFixtureAllowFallback(t, comp, plain)
	if res.Stats.Blocks[BlockFallback] != 1 || res.Stats.Blocks[BlockCompressed] != 1 || res.Stats.FallbackBytes != len(odd) {
		t.Fatalf("blocks %v fallback bytes %d (want %d)", res.Stats.Blocks, res.Stats.FallbackBytes, len(odd))
	}
}

func checkFixtureAllowFallback(t *testing.T, comp, plain []byte) *Result {
	t.Helper()
	if ref, ok := refDecode(t, comp); ok && !bytes.Equal(ref, plain) {
		t.Fatal("reference decoder disagrees with the fixture plaintext")
	}
	return roundTrip(t, comp, plain)
}

func FuzzIRUnmarshal(f *testing.F) {
	comp, _ := richStream(f)
	res, _, _ := parseKeep(f, comp, Options{})
	f.Add(res.Raw)
	f.Add([]byte("ZS\x01\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		sk, err := Unmarshal(b, Limits{MaxFrames: 1 << 10, MaxBlocks: 1 << 12})
		if err != nil {
			return
		}
		if !bytes.Equal(sk.Marshal(), b) {
			t.Fatal("accepted a non-canonical skeleton")
		}
	})
}

func FuzzParse(f *testing.F) {
	comp, _ := richStream(f)
	f.Add(comp)
	f.Add(goodStream(f))
	f.Add(frameOf([]byte{0x00, 0x58}, blk(true, BlockRLE, []byte{3}, 100)))
	f.Fuzz(func(t *testing.T, b []byte) {
		lim := Limits{MaxFrames: 64, MaxBlocks: 256, MaxWindow: 1 << 22, MaxFrameDecoded: 1 << 22, MaxTotalDecoded: 1 << 22, MaxSkippable: 1 << 16, MaxIRBytes: 1 << 24}
		var plain, units bytes.Buffer
		res, err := Parse(bytes.NewReader(b), &plain, &units, Options{Limits: lim})
		if err != nil {
			return
		}
		var out bytes.Buffer
		if _, err := Replay(&out, res.Raw, bytes.NewReader(units.Bytes()), bytes.NewReader(plain.Bytes()), lim); err != nil {
			t.Fatalf("accepted stream does not replay: %v", err)
		}
		if !bytes.Equal(out.Bytes(), b) {
			t.Fatal("replay differs from the accepted stream")
		}
	})
}
