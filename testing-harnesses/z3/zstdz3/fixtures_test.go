package zstdz3

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"os"
	"os/exec"
	"testing"
)

// fxBuilder assembles tiny valid frames for modes that real producers rarely emit
// (RLE literals, RLE sequence tables, treeless reuse, zero-literal-length repeat
// codes). It is test-only and not a compressor: the caller chooses every mode and
// decision, the entropy streams come from the replay encoders, and the reference
// decoder (when installed) independently confirms the result.
type fxBuilder struct {
	t      testing.TB
	hdr    []byte
	out    bytes.Buffer
	plain  bytes.Buffer
	st     codingState
	hist   history
	rep    [3]uint32
	skel   Frame2
	nblock int
}

// Frame2 is a scratch name to keep the builder's skeleton view readable.
type Frame2 = FrameRec

func newFx(t testing.TB, checksum bool) *fxBuilder {
	fhd := byte(0) // not single segment, no FCS, no dictionary
	if checksum {
		fhd |= 4
	}
	f := &fxBuilder{t: t, hdr: []byte{fhd, 0x58}, rep: [3]uint32{1, 4, 8}} // window 2 MiB: exp 11, mantissa 0
	f.hist.window = 1 << 21
	binary.Write(&f.out, binary.LittleEndian, uint32(magicStd))
	f.out.Write(f.hdr)
	return f
}

func (f *fxBuilder) block(last bool, typ BlockType, content []byte, size int) {
	f.out.Write(blockHeader(last, typ, size))
	f.out.Write(content)
}

func (f *fxBuilder) raw(data []byte, last bool) {
	f.hist.buf = append(f.hist.buf, data...)
	f.plain.Write(data)
	f.block(last, BlockRaw, data, len(data))
}

func (f *fxBuilder) rle(b byte, n int, last bool) {
	d := bytes.Repeat([]byte{b}, n)
	f.hist.buf = append(f.hist.buf, d...)
	f.plain.Write(d)
	f.block(last, BlockRLE, []byte{b}, n)
}

// compressed adds a compressed block described by rec (literal and sequence decisions),
// computes its plaintext by executing the sequences, and emits the real encoding.
func (f *fxBuilder) compressed(rec BlockRec, lits []byte, trip []seqRec, last bool) {
	f.t.Helper()
	f.hist.reserve(maxBlock)
	start := len(f.hist.buf)
	if err := f.hist.exec(trip, lits, &f.rep, maxBlock); err != nil {
		f.t.Fatalf("fixture sequences invalid: %v", err)
	}
	out := f.hist.buf[start:]
	rec.Type, rec.Last, rec.DecLen, rec.Regen, rec.NSeq = BlockCompressed, last, uint32(len(out)), uint32(len(lits)), uint32(len(trip))
	content, err := buildCompressed(&f.st, &rec, out, trip)
	if err != nil {
		f.t.Fatalf("fixture block: %v", err)
	}
	if _, err := scanBlock(&f.st, content, maxBlock); err != nil {
		f.t.Fatalf("fixture block does not scan: %v", err)
	}
	f.plain.Write(out)
	f.block(last, BlockCompressed, content, len(content))
}

func (f *fxBuilder) finish() (comp, plain []byte) {
	if f.hdr[0]&4 != 0 {
		var c [4]byte
		x := newXXH64()
		x.write(f.plain.Bytes())
		binary.LittleEndian.PutUint32(c[:], uint32(x.sum()))
		f.out.Write(c[:])
	}
	return f.out.Bytes(), f.plain.Bytes()
}

// directTree describes a complete prefix code over bytes 'a'.. with the given weights
// (the last symbol's weight is implied and not written).
func directTree(ws ...uint8) []byte {
	n := int('a') + len(ws) - 1 // listed symbols 0..n-1; the implied last symbol is n
	d := make([]byte, 1+(n+1)/2)
	d[0] = byte(127 + n)
	for i, w := range ws[:len(ws)-1] {
		s := int('a') + i
		if s&1 == 0 {
			d[1+s/2] |= w << 4
		} else {
			d[1+s/2] |= w
		}
	}
	return d
}

// fxTree is a 7-symbol code over a..g: lengths 4,4,3,3,3,2,2 (weights 1,1,2,2,2,3,3).
var fxTree = directTree(1, 1, 2, 2, 2, 3, 3)

func refDecode(t testing.TB, comp []byte) ([]byte, bool) {
	bin := os.Getenv("Z3_TOOLS")
	if bin == "" {
		bin = "/root/z3cache"
	}
	bin += "/tools/zstd-1.5.7/zstd"
	if _, err := os.Stat(bin); err != nil {
		return nil, false
	}
	cmd := exec.Command(bin, "-d", "-q", "-c")
	cmd.Stdin = bytes.NewReader(comp)
	var out, eb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &eb
	if err := cmd.Run(); err != nil {
		t.Fatalf("reference zstd rejects the fixture: %v: %s", err, eb.String())
	}
	return out.Bytes(), true
}

func checkFixture(t *testing.T, comp, plain []byte) *Result {
	t.Helper()
	if ref, ok := refDecode(t, comp); ok && !bytes.Equal(ref, plain) {
		t.Fatalf("reference decoder disagrees with the fixture plaintext (%d vs %d bytes)", len(ref), len(plain))
	}
	res := roundTrip(t, comp, plain)
	if res.Stats.Blocks[BlockFallback] != 0 {
		t.Fatalf("fixture needed a fallback block")
	}
	return res
}

func TestFixtureRLELiterals(t *testing.T) {
	f := newFx(t, true)
	f.raw(bytes.Repeat([]byte("abcd"), 8), false)
	lits := bytes.Repeat([]byte{'z'}, 40)
	f.compressed(BlockRec{LitMode: litRLE, LitForm: 0}, lits[:31], nil, false)
	f.compressed(BlockRec{LitMode: litRLE, LitForm: 1}, lits, []seqRec{{ll: 10, ml: 7, of: 4 + 9}, {ll: 30, ml: 3, of: 4}}, true)
	comp, plain := f.finish()
	res := checkFixture(t, comp, plain)
	if res.Stats.LitModes[litRLE] != 2 {
		t.Fatalf("rle literal blocks: %d", res.Stats.LitModes[litRLE])
	}
}

// repeatTablesFixture is the smallest stream that reuses all three previous sequence
// tables: a first compressed block fixes the (predefined) tables, a second one declares
// repeat mode for LL, OF and ML. Reference producers emit repeat mode only on their own
// schedule, so the hermetic coverage gate depends on this committed stream.
func repeatTablesFixture(t testing.TB) (comp, plain []byte) {
	f := newFx(t, true)
	f.raw(sampleText(300, 8), false)
	l := []byte("hijklmnhijklmnhijklmnhijklmn")
	f.compressed(BlockRec{LitMode: litRaw, LitForm: 0}, l, []seqRec{{ll: 3, ml: 6, of: 4 + 40}, {ll: 5, ml: 7, of: 4 + 11}, {ll: 1, ml: 5, of: 4 + 70}}, false)
	f.compressed(BlockRec{LitMode: litRaw, LitForm: 0, Modes: 3<<6 | 3<<4 | 3<<2}, l[:20], []seqRec{{ll: 4, ml: 5, of: 4 + 40}, {ll: 6, ml: 6, of: 4 + 11}, {ll: 2, ml: 9, of: 4 + 70}}, true)
	return f.finish()
}

func TestFixtureRepeatTables(t *testing.T) {
	comp, plain := repeatTablesFixture(t)
	res := checkFixture(t, comp, plain)
	for k := 0; k < 3; k++ {
		if res.Stats.SeqModes[k][3] != 1 {
			t.Fatalf("table %d repeat mode used %d times: %+v", k, res.Stats.SeqModes[k][3], res.Stats.SeqModes)
		}
	}
}

func TestFixtureRLETables(t *testing.T) {
	// all sequences share one literal-length code, one offset code and one match-length
	// code, so each table is a single symbol (RLE mode, no state bits).
	f := newFx(t, false)
	f.raw(sampleText(200, 5), false)
	var trip []seqRec
	var lits []byte
	for i := 0; i < 6; i++ {
		trip = append(trip, seqRec{ll: 5, ml: 5, of: uint32(16 + i*2)}) // offset code 4
		lits = append(lits, []byte("qrstu")...)
	}
	rec := BlockRec{LitMode: litRaw, Modes: 1<<6 | 1<<4 | 1<<2,
		TabBytes: []byte{5, 4, 2}} // LL symbol 5, OF symbol 4, ML symbol 2
	f.compressed(rec, lits, trip, true)
	comp, plain := f.finish()
	res := checkFixture(t, comp, plain)
	if res.Stats.SeqModes[iLL][1] != 1 || res.Stats.SeqModes[iOF][1] != 1 || res.Stats.SeqModes[iML][1] != 1 {
		t.Fatalf("rle table modes: %+v", res.Stats.SeqModes)
	}
}

// TestFixtureRepeatOffsets drives the three recent offsets, the zero-literal-length
// shift of the repeat codes, and treeless reuse of a Huffman tree, with random
// decisions; the reference decoder must agree with the plaintext the executor computes.
func TestFixtureRepeatOffsets(t *testing.T) {
	r := rand.New(rand.NewSource(99))
	for round := 0; round < 40; round++ {
		f := newFx(t, round%2 == 0)
		f.raw(sampleText(300, int64(round)), false)
		tree := fxTree
		for b := 0; b < 4; b++ {
			var lits []byte
			var trip []seqRec
			rep := f.rep
			hlen := len(f.hist.buf)
			produced := 0
			for len(trip) < 1+r.Intn(30) {
				ll := r.Intn(4)
				if r.Intn(3) == 0 {
					ll = 0
				}
				ml := 3 + r.Intn(12)
				var ofv uint32
				for {
					ofv = uint32(1 + r.Intn(3))
					if r.Intn(3) == 0 {
						ofv = uint32(4 + r.Intn(hlen+produced))
					}
					probe := rep
					idx := int(ofv) - 1
					off := uint32(0)
					switch {
					case ofv > 3:
						off = ofv - 3
					case idx+b2i(ll == 0) == 3:
						off = probe[0] - 1
					default:
						off = probe[idx+b2i(ll == 0)]
					}
					if off >= 1 && int(off) <= hlen+produced+ll {
						break
					}
				}
				// advance the probe rep state exactly like exec
				switch {
				case ofv > 3:
					rep[2], rep[1], rep[0] = rep[1], rep[0], ofv-3
				default:
					idx := int(ofv) - 1 + b2i(ll == 0)
					switch idx {
					case 1:
						rep[1], rep[0] = rep[0], rep[1]
					case 2:
						rep[2], rep[1], rep[0] = rep[1], rep[0], rep[2]
					case 3:
						rep[2], rep[1], rep[0] = rep[1], rep[0], rep[0]-1
					}
				}
				for i := 0; i < ll; i++ {
					lits = append(lits, byte('a'+r.Intn(7)))
				}
				trip = append(trip, seqRec{ll: uint32(ll), ml: uint32(ml), of: ofv})
				produced += ll + ml
			}
			for i := 0; i < r.Intn(20); i++ {
				lits = append(lits, byte('a'+r.Intn(7)))
			}
			rec := BlockRec{LitMode: litComp, LitForm: uint8(r.Intn(2)) * 2, TreeDesc: tree}
			if b > 0 && r.Intn(2) == 0 {
				rec = BlockRec{LitMode: litTreeles, LitForm: uint8(r.Intn(4))}
			}
			if len(lits) < 8 {
				lits = append(lits, []byte("abcdefg")...)
			}
			if rec.LitForm == 1 || rec.LitForm == 3 || rec.LitForm == 2 { // 4 streams need enough literals
				for len(lits) < 40 {
					lits = append(lits, byte('a'+r.Intn(7)))
				}
			}
			// every literal byte must be inside the tree alphabet; sequences consume ll of them
			f.compressed(rec, lits, trip, b == 3)
		}
		comp, plain := f.finish()
		checkFixture(t, comp, plain)
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
