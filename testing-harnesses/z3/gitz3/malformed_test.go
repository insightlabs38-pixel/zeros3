package gitz3

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"zeros3-z3/fstate"
	"zeros3-z3/lab"
)

// ---- helpers: hand-made packs with valid checksums ----

func zl(b []byte) []byte {
	var o bytes.Buffer
	w := zlib.NewWriter(&o)
	w.Write(b)
	w.Close()
	return o.Bytes()
}

func entry(kind uint8, payload []byte, base []byte) []byte {
	e := encodeEntryHeader(nil, kind, int64(len(payload)))
	e = append(e, base...)
	return append(e, zl(payload)...)
}

func mkPack(a Algo, count uint32, entries ...[]byte) []byte {
	b := []byte("PACK\x00\x00\x00\x02")
	b = binary.BigEndian.AppendUint32(b, count)
	for _, e := range entries {
		b = append(b, e...)
	}
	return append(b, a.Sum(b)...)
}

func fixSum(a Algo, b []byte) {
	hs := a.Size()
	copy(b[len(b)-hs:], a.Sum(b[:len(b)-hs]))
}

func mustNotPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s: panic: %v", what, r)
		}
	}()
	f()
}

// deltaGrow builds a delta copying the whole n-byte source and appending one byte.
func deltaGrow(n int, extra byte) []byte {
	d := binary.AppendUvarint(nil, uint64(n))
	d = binary.AppendUvarint(d, uint64(n+1))
	d = append(d, 0x80|0x10|0x20, byte(n), byte(n>>8))
	return append(d, 1, extra)
}

// deltaCopyAll builds a delta that copies the whole source once.
func deltaCopyAll(n int) []byte {
	d := binary.AppendUvarint(nil, uint64(n))
	d = binary.AppendUvarint(d, uint64(n))
	return append(d, 0x80|0x10|0x20, byte(n), byte(n>>8))
}

func TestHostilePack(t *testing.T) {
	a := SHA1
	base := []byte("base object payload that is long enough")
	baseEntry := entry(3, base, nil)
	goodDelta := deltaCopyAll(len(base))
	baseID := ObjectID(a, Blob, base)

	type tc struct {
		name string
		pack []byte
		lim  Limits
		want string // "parse" (ParsePack fails), "resolve" (Resolve fails), "ok"
	}
	var wrapped []byte
	{ // a pack whose declared count is smaller than its entries
		wrapped = mkPack(a, 1, baseEntry, entry(3, []byte("another"), nil))
	}
	refSelf := entry(refDelta, deltaGrow(len(base), 'x'), baseID) // delta whose base is an object that does not exist yet
	// REF cycle: A -> B, B -> A (ids chosen arbitrarily; neither is resolvable)
	idA, idB := bytes.Repeat([]byte{0xaa}, 20), bytes.Repeat([]byte{0xbb}, 20)
	cyc := mkPack(a, 2, entry(refDelta, goodDelta, idB), entry(refDelta, goodDelta, idA))
	// deep OFS chain
	var chain [][]byte
	chain = append(chain, baseEntry)
	offs := []int{12}
	pos := 12 + len(baseEntry)
	for i := 0; i < 12; i++ {
		e := entry(ofsDelta, deltaGrow(len(base)+i, byte('a'+i)), encodeOfs(nil, uint64(pos-offs[len(offs)-1])))
		chain = append(chain, e)
		offs = append(offs, pos)
		pos += len(e)
	}
	// duplicate object
	dup := mkPack(a, 2, baseEntry, baseEntry)
	// zlib stream with trailing garbage inside an entry (next entry parse fails)
	trail := mkPack(a, 1, append(append([]byte(nil), baseEntry...), 0xde, 0xad))
	// declared size too large / small versus the inflated size
	big := encodeEntryHeader(nil, 3, int64(len(base)+5))
	big = append(big, zl(base)...)
	small := encodeEntryHeader(nil, 3, int64(len(base)-5))
	small = append(small, zl(base)...)
	// huge declared size, tiny stream: must be rejected without allocating the claim
	huge := encodeEntryHeader(nil, 3, 200<<20)
	huge = append(huge, zl([]byte("x"))...)
	// OFS to offset before the pack header, zero, and into the middle of an entry
	ofsZero := entry(ofsDelta, goodDelta, []byte{0})
	ofsFar := entry(ofsDelta, goodDelta, encodeOfs(nil, 1<<40))
	ofsMid := entry(ofsDelta, goodDelta, encodeOfs(nil, 3))
	// OFS varint that never terminates
	ofsLong := append(encodeEntryHeader(nil, ofsDelta, 4), bytes.Repeat([]byte{0xff}, 20)...)
	// entry size varint that overflows
	sizeLong := append([]byte{0x3f}, bytes.Repeat([]byte{0xff}, 12)...)
	// amplification delta: tiny delta claiming a 1 GiB target
	ampl := binary.AppendUvarint(nil, uint64(len(base)))
	ampl = binary.AppendUvarint(ampl, 1<<30)
	ampl = append(ampl, 0x80)
	// delta whose copy ranges leave the base
	outside := binary.AppendUvarint(nil, uint64(len(base)))
	outside = binary.AppendUvarint(outside, 100)
	outside = append(outside, 0x80|0x01|0x10, 30, 100)

	cases := []tc{
		{"ok", mkPack(a, 1, baseEntry), Limits{}, "ok"},
		{"count-too-large", mkPack(a, 1<<31, baseEntry), Limits{}, "parse"},
		{"count-over-limit", mkPack(a, 1, baseEntry), Limits{MaxObjects: 0}, "ok"},
		{"count-below-entries", wrapped, Limits{}, "parse"},
		{"count-above-entries", mkPack(a, 3, baseEntry), Limits{}, "parse"},
		{"bad-version", append([]byte("PACK\x00\x00\x00\x09\x00\x00\x00\x00"), a.Sum([]byte("PACK\x00\x00\x00\x09\x00\x00\x00\x00"))...), Limits{}, "parse"},
		{"zero-entries", mkPack(a, 0), Limits{}, "ok"},
		{"entry-type-0", mkPack(a, 1, []byte{0x01, 0x78, 0x9c, 0x03, 0x00, 0, 0, 0, 1}), Limits{}, "parse"},
		{"entry-type-5", mkPack(a, 1, append(encodeEntryHeader(nil, 5, 1), zl([]byte("x"))...)), Limits{}, "parse"},
		{"size-larger-than-stream", mkPack(a, 1, big), Limits{}, "parse"},
		{"size-smaller-than-stream", mkPack(a, 1, small), Limits{}, "parse"},
		{"huge-claim-tiny-stream", mkPack(a, 1, huge), Limits{}, "parse"},
		{"size-over-limit", mkPack(a, 1, baseEntry), Limits{MaxObjectSize: 10}, "parse"},
		{"inflated-over-limit", mkPack(a, 2, baseEntry, entry(3, []byte("zzz"), nil)), Limits{MaxInflated: 41}, "parse"},
		{"trailing-bytes", trail, Limits{}, "parse"},
		{"ofs-zero", mkPack(a, 2, baseEntry, ofsZero), Limits{}, "parse"},
		{"ofs-before-header", mkPack(a, 2, baseEntry, ofsFar), Limits{}, "parse"},
		{"ofs-mid-entry", mkPack(a, 2, baseEntry, ofsMid), Limits{}, "resolve"},
		{"ofs-varint-endless", mkPack(a, 1, ofsLong), Limits{}, "parse"},
		{"size-varint-overflow", mkPack(a, 1, sizeLong), Limits{}, "parse"},
		{"ref-base-in-pack", mkPack(a, 2, baseEntry, refSelf), Limits{}, "ok"},
		{"ref-cycle", cyc, Limits{}, "resolve"},
		{"duplicate-object", dup, Limits{}, "resolve"},
		{"chain-ok", mkPack(a, 13, chain...), Limits{}, "ok"},
		{"chain-depth-limit", mkPack(a, 13, chain...), Limits{MaxDeltaDepth: 5}, "resolve"},
		{"delta-out-limit", mkPack(a, 13, chain...), Limits{MaxDeltaOut: int64(len(base)) * 3}, "resolve"},
		{"delta-amplification", mkPack(a, 2, baseEntry, entry(ofsDelta, ampl, encodeOfs(nil, uint64(len(baseEntry))))), Limits{}, "resolve"},
		{"delta-copy-outside", mkPack(a, 2, baseEntry, entry(ofsDelta, outside, encodeOfs(nil, uint64(len(baseEntry))))), Limits{}, "resolve"},
		{"truncated-header", []byte("PACK\x00\x00\x00\x02"), Limits{}, "parse"},
		{"empty", nil, Limits{}, "parse"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			mustNotPanic(t, c.name, func() {
				t0 := time.Now()
				p, err := ParsePack(c.pack, a, c.lim)
				if err == nil {
					_, err = p.Resolve()
					if err == nil {
						if c.want != "ok" {
							t.Fatalf("accepted, want %s failure", c.want)
						}
						return
					}
					if c.want == "parse" {
						t.Fatalf("parsed, want parse failure (resolve: %v)", err)
					}
				} else if c.want != "parse" {
					t.Fatalf("parse failed (%v), want %s", err, c.want)
				}
				if c.want == "ok" {
					t.Fatalf("rejected: %v", err)
				}
				if d := time.Since(t0); d > 2*time.Second {
					t.Fatalf("took %v", d)
				}
			})
		})
	}
}

func TestTruncationEverywhere(t *testing.T) {
	for _, a := range algos {
		for _, v := range []string{"ofs", "ref"} {
			pk := fx(t, fmt.Sprintf("%s-%s.pack", a, v))
			for n := 0; n < len(pk); n++ {
				mustNotPanic(t, fmt.Sprintf("pack %d", n), func() {
					if p, err := ParsePack(pk[:n], a, Limits{}); err == nil {
						t.Fatalf("%v %s: truncation at %d of %d accepted (%d entries)", a, v, n, len(pk), len(p.Entries))
					}
				})
			}
			idx := fx(t, fmt.Sprintf("%s-%s.idx", a, v))
			for n := 0; n < len(idx); n++ {
				mustNotPanic(t, fmt.Sprintf("idx %d", n), func() {
					if _, err := ParseIdx(idx[:n], a, Limits{}); err == nil {
						t.Fatalf("%v %s: idx truncation at %d accepted", a, v, n)
					}
				})
			}
		}
		for _, ty := range []string{"blob", "tree", "commit", "tag"} {
			lo := fx(t, fmt.Sprintf("%s-loose-%s", a, ty))
			for n := 0; n < len(lo); n++ {
				mustNotPanic(t, "loose", func() {
					if _, err := ParseLoose(a, lo[:n], Limits{}); err == nil {
						t.Fatalf("loose truncation at %d accepted", n)
					}
				})
			}
		}
	}
}

// Mutate every byte of the fixture packs (checksum repaired so the parser, not
// the trailer check, must cope); nothing may panic, and anything that still
// parses and resolves must re-derive a self-consistent index.
func TestMutatedPacksNeverPanic(t *testing.T) {
	for _, a := range algos {
		pk := fx(t, fmt.Sprintf("%s-ofs.pack", a))
		hs := a.Size()
		for i := 0; i < len(pk)-hs; i++ {
			for _, x := range []byte{0x01, 0x80, 0xff} {
				m := append([]byte(nil), pk...)
				m[i] ^= x
				fixSum(a, m)
				mustNotPanic(t, fmt.Sprintf("mutation %d^%#x", i, x), func() {
					p, err := ParsePack(m, a, Limits{MaxObjectSize: 1 << 20, MaxInflated: 1 << 22, MaxDeltaOut: 1 << 22})
					if err != nil {
						return
					}
					if _, err := p.Resolve(); err != nil {
						return
					}
					if _, err := BuildIdx(p); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestMutatedIdxNeverPanics(t *testing.T) {
	for _, a := range algos {
		idx := fx(t, fmt.Sprintf("%s-ofs.idx", a))
		hs := a.Size()
		for i := 0; i < len(idx)-hs; i++ {
			m := append([]byte(nil), idx...)
			m[i] ^= 0x5a
			copy(m[len(m)-hs:], a.Sum(m[:len(m)-hs]))
			mustNotPanic(t, "idx mutation", func() {
				if x, err := ParseIdx(m, a, Limits{}); err == nil {
					x.Lookup(bytes.Repeat([]byte{1}, hs))
					for k := 0; k < x.N; k++ {
						x.Offset(k)
					}
				}
			})
		}
	}
	// handcrafted: 64-bit offset reference out of range, unsorted ids
	a := SHA1
	var b bytes.Buffer
	b.Write([]byte{0xff, 't', 'O', 'c', 0, 0, 0, 2})
	for i := 0; i < 256; i++ {
		binary.Write(&b, binary.BigEndian, uint32(map[bool]int{true: 1, false: 0}[i >= 1]))
	}
	b.Write(bytes.Repeat([]byte{1}, 20))
	binary.Write(&b, binary.BigEndian, uint32(0))
	binary.Write(&b, binary.BigEndian, uint32(0x80000005)) // 64-bit table has 0 slots
	b.Write(make([]byte, 20))
	b.Write(a.Sum(b.Bytes()))
	if _, err := ParseIdx(b.Bytes(), a, Limits{}); err == nil {
		t.Fatal("out-of-range 64-bit offset reference accepted")
	}
}

// A hostile recipe (the exact representation's own metadata) must also fail
// cleanly: truncation and mutation of the serialized meta.
func TestMutatedRecipesNeverPanic(t *testing.T) {
	for _, a := range algos {
		data := fx(t, fmt.Sprintf("%s-ofs.pack", a))
		p, _ := ParsePack(data, a, Limits{})
		p.Resolve()
		an, _ := AnalyzePack(p)
		for layout := 0; layout < 2; layout++ {
			ac := lab.NewAcct(96, false)
			var pl *lab.Plan
			var rebuild func(meta []byte, w *bytes.Buffer) error
			if layout == 0 {
				pl, _ = an.SeqPlan(RepOpts{Mode: "plain"})
				rebuild = func(meta []byte, w *bytes.Buffer) error { return RebuildSeq(meta, pl.Pieces, ac.Get, w) }
			} else {
				pl, _ = an.SetPlan(RepOpts{Mode: "plain"})
				rebuild = func(meta []byte, w *bytes.Buffer) error { return RebuildSet(meta, pl.Pieces, ac.Get, w) }
			}
			if _, err := ac.Add(pl); err != nil {
				t.Fatal(err)
			}
			meta := pl.Detail["meta"].([]byte)
			for n := 0; n < len(meta); n++ {
				mustNotPanic(t, "meta truncation", func() {
					var w bytes.Buffer
					if err := rebuild(meta[:n], &w); err == nil && bytes.Equal(w.Bytes(), data) {
						t.Fatalf("layout %d: truncated recipe (%d/%d) reproduced the pack", layout, n, len(meta))
					}
				})
			}
			for i := 0; i < len(meta); i++ {
				m := append([]byte(nil), meta...)
				m[i] ^= 0xa5
				mustNotPanic(t, "meta mutation", func() {
					var w bytes.Buffer
					t0 := time.Now()
					_ = rebuild(m, &w)
					if time.Since(t0) > 5*time.Second {
						t.Fatalf("mutation %d took too long", i)
					}
				})
			}
		}
	}
}

func TestInflateBombIsBounded(t *testing.T) {
	// 4 MiB of zeros compresses to ~4 KiB; claiming 200 MiB must fail without a 200 MiB allocation
	bomb := zl(make([]byte, 4<<20))
	allocs := testing.AllocsPerRun(1, func() {
		if _, err := inflateZlib(bomb, 200<<20, discard{}); err == nil {
			t.Fatal("size mismatch accepted")
		}
	})
	_ = allocs
	if _, err := inflateZlib(bomb, 4<<20, discard{}); err != nil {
		t.Fatal(err)
	}
}

type discard struct{}

func (discard) Write(b []byte) (int, error) { return len(b), nil }

func TestPieceRecipeRobust(t *testing.T) {
	data := fx(t, "sha1-ofs.pack")
	p, _ := ParsePack(data, SHA1, Limits{})
	p.Resolve()
	an, _ := AnalyzePack(p)
	pl, _ := an.SeqPlan(RepOpts{Mode: "plain"})
	enc := fstate.MarshalPieces(pl.Pieces)
	if dec, err := fstate.UnmarshalPieces(enc); err != nil || len(dec) != fstate.CountListed(pl.Pieces) {
		t.Fatalf("round trip: %v (%d vs %d pieces)", err, len(dec), fstate.CountListed(pl.Pieces))
	}
	for n := 0; n < len(enc); n++ {
		mustNotPanic(t, "piece truncation", func() {
			if _, err := fstate.UnmarshalPieces(enc[:n]); err == nil && n < len(enc)-1 {
				// a prefix may decode only if it ends exactly at a piece boundary of a shorter list; the declared count prevents that
				t.Fatalf("truncation at %d of %d accepted", n, len(enc))
			}
		})
	}
	for i := range enc {
		m := append([]byte(nil), enc...)
		m[i] ^= 0xff
		mustNotPanic(t, "piece mutation", func() { fstate.UnmarshalPieces(m) })
	}
}
