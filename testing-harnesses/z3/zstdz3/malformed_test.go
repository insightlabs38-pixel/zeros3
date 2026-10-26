package zstdz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"runtime"
	"testing"
)

func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// frameOf wraps pre-encoded blocks in a frame with the given header bytes.
func frameOf(hdr []byte, blocks ...[]byte) []byte {
	b := append(le32(magicStd), hdr...)
	for _, k := range blocks {
		b = append(b, k...)
	}
	return b
}

func blk(last bool, typ BlockType, content []byte, size int) []byte {
	return append(blockHeader(last, typ, size), content...)
}

// goodStream is a small multi-block frame with a checksum: raw, RLE, and a compressed
// block with Huffman literals and sequences.
func goodStream(t testing.TB) []byte {
	f := newFx(t, true)
	f.raw([]byte("the quick brown fox jumps over the lazy dog, the quick brown fox"), false)
	f.rle('q', 40, false)
	var lits []byte
	for i := 0; i < 24; i++ {
		lits = append(lits, byte('a'+i%7))
	}
	f.compressed(BlockRec{LitMode: litComp, LitForm: 2, TreeDesc: fxTree}, lits, []seqRec{{ll: 3, ml: 6, of: 4 + 20}, {ll: 0, ml: 9, of: 1}, {ll: 5, ml: 4, of: 3}}, true)
	c, _ := f.finish()
	return c
}

// compLits is a compressed block starting with a (treeless or compressed) literals
// section of the given regenerated size over body, followed by an empty sequences section.
func compLits(mode uint8, regen int, body ...byte) []byte {
	h, err := litHeader(mode, 0, regen, len(body))
	if err != nil {
		panic(err)
	}
	return append(append(h, body...), 0)
}

func parseAll(b []byte, lim Limits) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.New("PANIC")
		}
	}()
	_, err = Parse(bytes.NewReader(b), io.Discard, nil, Options{Limits: lim})
	return
}

func TestMalformedMatrix(t *testing.T) {
	good := goodStream(t)
	if err := parseAll(good, Limits{}); err != nil {
		t.Fatalf("good stream: %v", err)
	}
	mut := func(f func(b []byte) []byte) []byte { return f(append([]byte(nil), good...)) }
	hdr := []byte{0x00, 0x58}
	rawBlk := func(last bool) []byte { return blk(last, BlockRaw, []byte("abcdefgh"), 8) }
	// a compressed block whose content is exactly c
	cblk := func(c []byte) []byte { return blk(true, BlockCompressed, c, len(c)) }
	cases := []struct {
		name string
		data []byte
		lim  Limits
		want error
	}{
		{"corrupt magic", mut(func(b []byte) []byte { b[1] ^= 0xff; return b }), Limits{}, ErrCorrupt},
		{"magicless frame (rejected, never reinterpreted)", good[4:], Limits{}, ErrCorrupt},
		{"legacy frame", append(le32(0xFD2FB525), 0, 0, 0, 0), Limits{}, ErrUnsupported},
		{"reserved frame bit", mut(func(b []byte) []byte { b[4] |= 8; return b }), Limits{}, ErrCorrupt},
		{"truncated header", good[:5], Limits{}, ErrCorrupt},
		{"window too large", frameOf([]byte{0x00, 0xff}, rawBlk(true)), Limits{}, ErrLimit},
		{"configured window", frameOf([]byte{0x00, 0x58}, rawBlk(true)), Limits{MaxWindow: 1 << 20}, ErrLimit},
		{"dictionary id", frameOf([]byte{0x01, 0x58, 0x07}, rawBlk(true)), Limits{}, ErrUnsupported},
		{"reserved block type", frameOf(hdr, []byte{0x07, 0, 0}), Limits{}, ErrCorrupt},
		{"block larger than the window", frameOf([]byte{0x00, 0x00}, blk(true, BlockRaw, make([]byte, 2000), 2000)), Limits{}, ErrCorrupt},
		{"block overrun", frameOf(hdr, append(blockHeader(true, BlockRaw, 100), 1, 2, 3)), Limits{}, ErrCorrupt},
		{"no final block", frameOf(hdr, rawBlk(false)), Limits{}, ErrCorrupt},
		{"content size smaller than the data", frameOf([]byte{0x80, 0x58, 7, 0, 0, 0}, rawBlk(true)), Limits{}, ErrCorrupt},
		{"content size larger than the data", frameOf([]byte{0x80, 0x58, 20, 0, 0, 0}, rawBlk(true)), Limits{}, ErrCorrupt},
		{"single segment content size", frameOf([]byte{0x20, 0x07}, blk(true, BlockRaw, []byte("abcdefgh"), 8)), Limits{}, ErrCorrupt},
		{"content size 100 TiB", frameOf([]byte{0xc0, 0x58, 0, 0, 0, 0, 0, 0x64, 0, 0}, rawBlk(true)), Limits{}, ErrLimit},
		{"decoded size past the configured limit", frameOf(hdr, blk(false, BlockRLE, []byte{1}, 100000), blk(true, BlockRLE, []byte{1}, 100000)), Limits{MaxFrameDecoded: 150000}, ErrLimit},
		{"expansion ratio", frameOf(hdr, bytes.Repeat(blk(false, BlockRLE, []byte{1}, 100000), 20), blk(true, BlockRLE, []byte{1}, 100000)), Limits{MaxRatio: 1}, ErrLimit},
		{"wrong checksum", mut(func(b []byte) []byte { b[len(b)-1] ^= 1; return b }), Limits{}, ErrCorrupt},
		{"trailing partial frame", append(append([]byte(nil), good...), 0x28, 0xb5), Limits{}, ErrCorrupt},
		{"trailing garbage frame", append(append([]byte(nil), good...), 1, 2, 3, 4), Limits{}, ErrCorrupt},
		{"empty stream", nil, Limits{}, ErrCorrupt},
		{"frame limit", append(append([]byte(nil), good...), good...), Limits{MaxFrames: 1}, ErrLimit},
		{"block limit", good, Limits{MaxBlocks: 2}, ErrLimit},
		{"decoded limit", good, Limits{MaxTotalDecoded: 50}, ErrLimit},
		{"empty compressed block", frameOf(hdr, cblk(nil)), Limits{}, ErrCorrupt},
		{"truncated literals header", frameOf(hdr, cblk([]byte{0x02})), Limits{}, ErrCorrupt},
		{"treeless without tree", frameOf(hdr, cblk(compLits(litTreeles, 10, 1, 2, 3, 4))), Limits{}, ErrCorrupt},
		{"huffman weights size zero", frameOf(hdr, cblk(compLits(litComp, 10, 0, 1, 2, 3))), Limits{}, ErrCorrupt},
		{"incomplete huffman tree", frameOf(hdr, cblk(compLits(litComp, 10, 130, 0x22, 0x10, 1))), Limits{}, ErrCorrupt},
		{"huffman weight beyond 11", frameOf(hdr, cblk(compLits(litComp, 10, 129, 0xc1, 1))), Limits{}, ErrCorrupt},
		{"huffman tree without longest codes", frameOf(hdr, cblk(compLits(litComp, 10, 134, 0x33, 0x33, 0x33, 0x30, 1))), Limits{}, ErrCorrupt},
		{"sequence count overflow", frameOf(hdr, cblk([]byte{0x00, 0xff, 0xff, 0xff, 0x00, 1, 2, 3})), Limits{}, ErrCorrupt},
		{"reserved sequence mode bits", frameOf(hdr, cblk([]byte{0x00, 0x01, 0x03, 0x01})), Limits{}, ErrCorrupt},
		{"repeat table without previous", frameOf(hdr, cblk([]byte{0x00, 0x01, 0xc0, 0x01})), Limits{}, ErrCorrupt},
		{"rle symbol out of range", frameOf(hdr, cblk([]byte{0x00, 0x01, 0x40, 0xff, 0x01})), Limits{}, ErrCorrupt},
		{"fse accuracy log too large", frameOf(hdr, cblk([]byte{0x00, 0x01, 0x80, 0x0f, 0xff, 0xff, 0x01})), Limits{}, ErrCorrupt},
		{"malformed fse description", frameOf(hdr, cblk([]byte{0x00, 0x01, 0x80, 0x00, 0x00, 0x00, 0x01})), Limits{}, ErrCorrupt},
		{"missing sequence bitstream", frameOf(hdr, cblk([]byte{0x00, 0x01, 0x00})), Limits{}, ErrCorrupt},
		{"bitstream end marker missing", frameOf(hdr, cblk([]byte{0x00, 0x01, 0x00, 0x00})), Limits{}, ErrCorrupt},
		{"bytes after empty sequences", frameOf(hdr, cblk([]byte{0x00, 0x00, 0x00})), Limits{}, ErrCorrupt},
	}
	for _, c := range cases {
		err := parseAll(c.data, c.lim)
		t.Logf("%-34s %v", c.name, err)
		if err == nil || !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

// TestMalformedSequences builds well-formed sequence sections whose decisions are
// invalid for the data they run over.
func TestMalformedSequences(t *testing.T) {
	enc := func(window byte, history []byte, trip []seqRec, lits []byte, plainLen int) []byte {
		var st codingState
		rec := BlockRec{Type: BlockCompressed, Last: true, LitMode: litRaw, Regen: uint32(len(lits)), NSeq: uint32(len(trip))}
		content, err := buildCompressed(&st, &rec, make([]byte, plainLen), trip)
		if err != nil {
			t.Fatal(err)
		}
		var blocks [][]byte
		if len(history) > 0 {
			blocks = append(blocks, blk(false, BlockRaw, history, len(history)))
		}
		return frameOf([]byte{0x00, window}, append(blocks, blk(true, BlockCompressed, content, len(content)))...)
	}
	hist := bytes.Repeat([]byte("0123456789"), 30)
	preds := [3]*fseTable{predTables[0], predTables[1], predTables[2]}
	seqBlock := func(n int, claim int, trip []seqRec) []byte {
		stream, err := encodeSeqs(preds, trip, [3]uint32{})
		if err != nil {
			t.Fatal(err)
		}
		content := append([]byte{litRaw | 4<<3}, make([]byte, 4)...)
		content = append(content, seqCountHeader(claim)...)
		content = append(append(content, 0), stream...)
		return frameOf([]byte{0x00, 0x58}, blk(false, BlockRaw, hist, len(hist)), blk(true, BlockCompressed, content, len(content)))
	}
	three := []seqRec{{ll: 1, ml: 4, of: 7}, {ll: 1, ml: 4, of: 8}, {ll: 2, ml: 5, of: 9}}
	big := bytes.Repeat([]byte("0123456789"), 100)
	for _, c := range []struct {
		name string
		data []byte
		want error
	}{
		{"offset before history", enc(0x58, hist[:20], []seqRec{{ll: 0, ml: 4, of: 4 + 40}}, nil, 4), ErrCorrupt},
		{"offset beyond the window", enc(0x00, hist, []seqRec{{ll: 0, ml: 4, of: 4 + 1500}}, nil, 4), ErrCorrupt},
		{"first block offset", enc(0x58, nil, []seqRec{{ll: 2, ml: 4, of: 4 + 5}}, []byte("ab"), 6), ErrCorrupt},
		{"repeat offset zero", enc(0x58, hist, []seqRec{{ll: 0, ml: 4, of: 4}, {ll: 0, ml: 4, of: 3}}, nil, 8), nil}, // rep0-1 = 0 only after offset 1: valid chain below
		{"more literals than the section", fewLiterals(t, hist), ErrCorrupt},
		{"sequence count above the stream", seqBlock(3, 4, three), ErrCorrupt},
		{"sequence count below the stream", seqBlock(3, 2, three), ErrCorrupt},
		{"match decodes past the block maximum", frameOf([]byte{0x00, 0x00}, blk(false, BlockRaw, big[:1000], 1000), blk(false, BlockRaw, big[:1000], 1000),
			blk(true, BlockCompressed, mustSeqBlock(t, []seqRec{{ll: 0, ml: 1500, of: 4 + 10}}), len(mustSeqBlock(t, []seqRec{{ll: 0, ml: 1500, of: 4 + 10}})))), ErrCorrupt},
	} {
		err := parseAll(c.data, Limits{})
		if c.want == nil {
			continue
		}
		if err == nil || !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
	// repeat offset resolving to zero: offset 1 then the zero-literal rep3 code (rep0-1)
	bad := enc(0x58, hist, []seqRec{{ll: 0, ml: 4, of: 4}, {ll: 0, ml: 4, of: 3}}, nil, 8)
	if err := parseAll(bad, Limits{}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("repeat offset of zero: %v", err)
	}
}

// mustSeqBlock is a compressed block with no literals and the given sequences.
func mustSeqBlock(t *testing.T, trip []seqRec) []byte {
	var st codingState
	var plainLen int
	for _, s := range trip {
		plainLen += int(s.ll + s.ml)
	}
	rec := BlockRec{Type: BlockCompressed, LitMode: litRaw, NSeq: uint32(len(trip))}
	c, err := buildCompressed(&st, &rec, make([]byte, plainLen), trip)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// fewLiterals is a block whose only sequence wants 5 literals from a 3-byte section.
func fewLiterals(t *testing.T, hist []byte) []byte {
	stream, err := encodeSeqs([3]*fseTable{predTables[0], predTables[1], predTables[2]}, []seqRec{{ll: 5, ml: 4, of: 5}}, [3]uint32{})
	if err != nil {
		t.Fatal(err)
	}
	content := append([]byte{litRaw | 3<<3, 'a', 'b', 'c', 1, 0}, stream...)
	return frameOf([]byte{0x00, 0x58}, blk(false, BlockRaw, hist, len(hist)), blk(true, BlockCompressed, content, len(content)))
}

func TestTruncations(t *testing.T) {
	for _, s := range [][]byte{goodStream(t), frameOf([]byte{0x20, 0x08}, blk(true, BlockRaw, []byte("abcdefgh"), 8)), frameOf([]byte{0x00, 0x58}, blk(true, BlockRLE, []byte{9}, 300))} {
		boundaries := map[int]bool{len(s): true}
		for n := 0; n < len(s); n++ {
			err := parseAll(s[:n], Limits{})
			if err == nil && !boundaries[n] {
				t.Fatalf("prefix %d of %d accepted", n, len(s))
			}
			if err != nil && err.Error() == "PANIC" {
				t.Fatalf("prefix %d panicked", n)
			}
		}
	}
}

func TestHostileSizesDoNotAllocate(t *testing.T) {
	// A declared 8 EiB / 100 TiB / 10 GiB frame over a few bytes must neither be
	// believed nor allocated.
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	for _, fcs := range []uint64{1 << 46, 10 << 30, 1<<63 - 1} {
		h := append([]byte{0xe0}, binary.LittleEndian.AppendUint64(nil, fcs)...)
		_ = parseAll(frameOf(h, blk(true, BlockRaw, []byte("abc"), 3)), Limits{})
		_ = parseAll(frameOf(h, blk(false, BlockRaw, []byte("abc"), 3)), Limits{MaxFrameDecoded: 1 << 50, MaxWindow: 1 << 50})
	}
	runtime.ReadMemStats(&m1)
	if d := m1.TotalAlloc - m0.TotalAlloc; d > 64<<20 {
		t.Fatalf("hostile headers allocated %d bytes", d)
	}
}

func TestSkippableAndConcatenated(t *testing.T) {
	a := frameOf([]byte{0x04, 0x58}, blk(true, BlockRaw, []byte("first frame "), 12))
	x := newXXH64()
	x.write([]byte("first frame "))
	a = append(a, le32(uint32(x.sum()))...)
	b := frameOf([]byte{0x00, 0x58}, blk(true, BlockRLE, []byte{'z'}, 5))
	skip := append(append(le32(magicSkip|7), le32(5)...), 9, 8, 7, 6, 5)
	for _, c := range []struct {
		name  string
		parts [][]byte
		want  string
	}{
		{"single", [][]byte{a}, "first frame "},
		{"two frames", [][]byte{a, b}, "first frame zzzzz"},
		{"skippable first", [][]byte{skip, a}, "first frame "},
		{"skippable between", [][]byte{a, skip, b}, "first frame zzzzz"},
		{"skippable only", [][]byte{skip}, ""},
		{"two skippables", [][]byte{skip, skip, b}, "zzzzz"},
	} {
		comp := bytes.Join(c.parts, nil)
		res := roundTrip(t, comp, []byte(c.want))
		wantSkip := 0
		for _, p := range c.parts {
			if bytes.Equal(p, skip) {
				wantSkip++
			}
		}
		if res.Stats.Skippable != wantSkip || res.Stats.Frames != len(c.parts)-wantSkip {
			t.Errorf("%s: %d standard, %d skippable frames", c.name, res.Stats.Frames, res.Stats.Skippable)
		}
	}
}

func TestXXH64Vectors(t *testing.T) {
	for _, v := range []struct {
		in   string
		want uint64
	}{
		{"", 0xEF46DB3751D8E999},
		{"a", 0xD24EC4F1A98C6E5B},
		{"abc", 0x44BC2CF5AD770999},
		{"Nobody inspects the spammish repetition", 0xFBCEA83C8A378BF1},
	} {
		x := newXXH64()
		x.write([]byte(v.in))
		if got := x.sum(); got != v.want {
			t.Errorf("xxh64(%q) = %#x, want %#x", v.in, got, v.want)
		}
	}
	// chunking must not matter
	data := sampleText(1000, 3)
	whole := newXXH64()
	whole.write(data)
	for _, step := range []int{1, 7, 31, 32, 33, 100} {
		x := newXXH64()
		for i := 0; i < len(data); i += step {
			x.write(data[i:min(i+step, len(data))])
		}
		if x.sum() != whole.sum() {
			t.Errorf("chunk %d differs", step)
		}
	}
}

func TestPredefinedTables(t *testing.T) {
	for i, want := range []int{64, 32, 64} { // LL, OF, ML
		var sum int
		for _, c := range predTables[i].counts {
			if c == -1 {
				sum++
			} else {
				sum += int(c)
			}
		}
		if sum != want {
			t.Errorf("predefined table %d sums to %d", i, sum)
		}
	}
}
