package carz3

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func getter(blocks ...[]Block) Getter {
	m := map[[32]byte][]byte{}
	for _, bs := range blocks {
		for _, b := range bs {
			m[sha256.Sum256(b.Data)] = b.Data
		}
	}
	return func(s [32]byte) ([]byte, error) {
		if d, ok := m[s]; ok {
			return d, nil
		}
		return nil, errors.New("missing component")
	}
}

func small() ([]Block, []byte) {
	return Gen(GenSpec{Seed: 1, N: 40, Min: 100, Max: 3000, Fan: 6})
}

func TestCIDParse(t *testing.T) {
	v1 := CIDv1(CodecRaw, MhSHA2_256, []byte("x"))
	c, n, err := ParseCID(append(v1, 9, 9))
	if err != nil || n != len(v1) || c.Version != 1 || c.Codec != CodecRaw || c.HashCode != MhSHA2_256 || len(c.Digest) != 32 {
		t.Fatalf("v1: %+v %d %v", c, n, err)
	}
	if c.Verify([]byte("x")) != Verified || c.Verify([]byte("y")) != Mismatch {
		t.Fatal("verify")
	}
	v0 := CIDv0([]byte("x"))
	c0, n, err := ParseCID(v0)
	if err != nil || n != 34 || c0.Version != 0 || c0.Codec != CodecDagPB || c0.Verify([]byte("x")) != Verified {
		t.Fatalf("v0: %+v %v", c0, err)
	}
	if c0.Key() != CIDv1Parsed(t, CIDv1(CodecDagPB, MhSHA2_256, []byte("x"))).Key() {
		t.Fatal("v0 and v1 dag-pb of one multihash must share a native key")
	}
	if CIDv1Parsed(t, v1).Key() == c0.Key() {
		t.Fatal("different codec must differ")
	}
	fk := CIDv1Parsed(t, CIDv1(CodecRaw, MhFakeBlake, []byte("x")))
	if fk.Verify([]byte("x")) != NotVerified {
		t.Fatal("unsupported multihash must be not-verified")
	}
	for name, b := range map[string][]byte{
		"empty":        nil,
		"truncated":    v1[:10],
		"version2":     append([]byte{2}, v1[1:]...),
		"long-digest":  append([]byte{1, 0x55, 0x12, 0x41}, make([]byte, 65)...),
		"nonminimal":   append([]byte{0x81, 0x00, 0x55, 0x12, 0x20}, make([]byte, 32)...),
		"v0-truncated": v0[:20],
	} {
		if _, _, err := ParseCID(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func CIDv1Parsed(t *testing.T, b []byte) CID {
	t.Helper()
	c, _, err := ParseCID(b)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUvarint(t *testing.T) {
	for _, v := range []uint64{0, 1, 127, 128, 300, 1 << 32, 1<<63 - 1} {
		b := AppendUvarint(nil, v)
		g, n, err := Uvarint(b)
		if err != nil || n != len(b) || g != v {
			t.Errorf("%d: %d %d %v", v, g, n, err)
		}
	}
	if _, _, err := Uvarint([]byte{0x80, 0x00}); !errors.As(err, new(*UnsupportedError)) {
		t.Errorf("non-minimal: %v", err)
	}
	if _, _, err := Uvarint(bytes.Repeat([]byte{0xff}, 10)); !errors.As(err, new(*MalformedError)) {
		t.Errorf("overlong: %v", err)
	}
	if _, n, err := Uvarint([]byte{0x80}); n != 0 || err != nil {
		t.Errorf("truncated: %d %v", n, err)
	}
}

func TestHeader(t *testing.T) {
	_, root := small()
	for _, first := range []bool{true, false} {
		raw := HeaderBody([][]byte{root, root}, first)
		h, err := ParseHeader(raw, DefaultLimits())
		if err != nil || len(h.Roots) != 2 || h.Version != 1 || h.RootsFirst != first {
			t.Fatalf("roots-first=%v: %+v %v", first, h, err)
		}
	}
	if h, err := ParseHeader(HeaderBody(nil, true), DefaultLimits()); err != nil || len(h.Roots) != 0 {
		t.Fatalf("empty roots: %v", err)
	}
	good := HeaderBody([][]byte{root}, true)
	bad := map[string][]byte{
		"not-map":      {0x80},
		"indefinite":   {0xbf, 0xff},
		"extra-key":    append([]byte{0xa3}, good[1:]...),
		"version2":     bytes.Replace(good, []byte{0x01}, []byte{0x02}, 1),
		"truncated":    good[:len(good)-3],
		"trailing":     append(append([]byte(nil), good...), 0),
		"nonmin-arg":   append([]byte{0xa2, 0x78, 0x05}, good[2:]...),
		"dup-key":      append(append([]byte{0xa2}, good[1:7]...), good[1:]...),
		"bad-root-tag": bytes.Replace(good, []byte{0xd8, 0x2a}, []byte{0xd8, 0x2b}, 1),
	}
	for name, b := range bad {
		if _, err := ParseHeader(b, DefaultLimits()); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !IsFallback(err) {
			t.Errorf("%s: not a classified error: %v", name, err)
		}
	}
	lim := DefaultLimits()
	lim.MaxRoots = 1
	if _, err := ParseHeader(HeaderBody([][]byte{root, root}, true), lim); err == nil {
		t.Error("root-count bound not enforced")
	}
}

func TestBlockStream(t *testing.T) {
	bs, root := small()
	car := V1([][]byte{root}, bs)
	rd, err := NewReader(bytes.NewReader(car), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	i := 0
	for {
		e, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(e.CID.Raw, bs[i].CID) || !bytes.Equal(e.Data, bs[i].Data) || e.CID.Verify(e.Data) != Verified {
			t.Fatalf("block %d differs", i)
		}
		if !bytes.Equal(car[e.DataOff:e.DataOff+int64(len(e.Data))], e.Data) || e.End() > int64(len(car)) {
			t.Fatalf("block %d offsets", i)
		}
		i++
	}
	if i != len(bs) || rd.Pos() != int64(len(car)) {
		t.Fatalf("read %d of %d, pos %d of %d", i, len(bs), rd.Pos(), len(car))
	}
}

func TestBlockMalformed(t *testing.T) {
	bs, root := small()
	car := V1([][]byte{root}, bs[:3])
	hl := int(car[0]) + 1
	cases := map[string][]byte{
		"truncated-block":     car[:len(car)-1],
		"truncated-varint":    append(append([]byte(nil), car[:hl]...), 0x80),
		"truncated-cid":       append(append([]byte(nil), car[:hl]...), 0x05, 0x01, 0x55),
		"zero-section":        append(append([]byte(nil), car...), 0x00),
		"huge-section":        append(append([]byte(nil), car[:hl]...), AppendUvarint(nil, 1<<40)...),
		"claims-40MiB-block":  append(append([]byte(nil), car[:hl]...), AppendUvarint(nil, 40<<20)...),
		"claims-2MiB-block":   append(append([]byte(nil), car[:hl]...), append(AppendUvarint(nil, 2<<20), 0x01, 0x55, 0x12, 0x20)...),
		"overlong-varint":     append(append([]byte(nil), car[:hl]...), bytes.Repeat([]byte{0xff}, 10)...),
		"nonminimal-section":  append(append([]byte(nil), car[:hl]...), 0x80, 0x00),
		"cid-beyond-section":  append(append([]byte(nil), car[:hl]...), 0x03, 0x01, 0x55, 0x12, 0x20),
		"empty":               nil,
		"header-len-too-long": {0xff, 0x7f},
		"header-len-zero":     {0x00},
	}
	for name, b := range cases {
		_, _, err := Build(bytes.NewReader(b), int64(len(b)), DefaultOptions())
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if !IsFallback(err) {
			t.Errorf("%s: unclassified error %v", name, err)
		}
	}
	// A block count bound.
	opt := DefaultOptions()
	opt.MaxBlocks = 2
	if _, _, err := Build(bytes.NewReader(car), int64(len(car)), opt); err == nil {
		t.Error("block-count bound not enforced")
	}
	opt = DefaultOptions()
	opt.MaxBlock = 10
	if _, _, err := Build(bytes.NewReader(car), int64(len(car)), opt); err == nil {
		t.Error("block-size bound not enforced")
	}
	// A CID whose multihash does not match its payload fails the transform...
	tampered := append([]byte(nil), car...)
	tampered[len(tampered)-1] ^= 1
	if _, _, err := Build(bytes.NewReader(tampered), int64(len(tampered)), DefaultOptions()); err == nil {
		t.Error("CID/payload mismatch accepted")
	}
	// ...unless explicitly lenient, in which case the mismatch is only counted
	// and the bytes are still reproduced exactly.
	opt = DefaultOptions()
	opt.Lenient = true
	rep, st, err := Build(bytes.NewReader(tampered), int64(len(tampered)), opt)
	if err != nil || st.Mismatch != 1 {
		t.Fatalf("lenient: %v %+v", err, st)
	}
	var out bytes.Buffer
	if err := rep.Reconstruct(&out, func(s [32]byte) ([]byte, error) {
		for _, b := range bs[:3] {
			if sha256.Sum256(b.Data) == s {
				return b.Data, nil
			}
		}
		d := append([]byte(nil), bs[2].Data...)
		d[len(d)-1] ^= 1
		return d, nil
	}); err != nil || !bytes.Equal(out.Bytes(), tampered) {
		t.Fatalf("lenient reconstruct: %v", err)
	}
}

func buildRebuild(t *testing.T, name string, car []byte, gets Getter, inline int) (*Rep, *Stats) {
	t.Helper()
	opt := DefaultOptions()
	opt.InlineMax = inline
	rep, st, err := Build(bytes.NewReader(car), int64(len(car)), opt)
	if err != nil {
		t.Fatalf("%s: build: %v", name, err)
	}
	var out bytes.Buffer
	if err := rep.Reconstruct(&out, gets); err != nil || !bytes.Equal(out.Bytes(), car) {
		t.Fatalf("%s: in-memory reconstruct differs (%v)", name, err)
	}
	m, sz := rep.Marshal()
	if sz.Total != len(m) {
		t.Fatalf("%s: sizes %d != %d", name, sz.Total, len(m))
	}
	rep2, err := Unmarshal(m)
	if err != nil {
		t.Fatalf("%s: unmarshal: %v", name, err)
	}
	out.Reset()
	if err := rep2.Reconstruct(&out, gets); err != nil || !bytes.Equal(out.Bytes(), car) {
		t.Fatalf("%s: reconstruct from serialised recipe differs (%v)", name, err)
	}
	return rep, st
}

func TestRebuild(t *testing.T) {
	bs, root := small()
	v1 := V1([][]byte{root}, bs)
	g := getter(bs)
	cases := map[string][]byte{
		"v1":              v1,
		"v1-reorder":      V1([][]byte{root}, Shuffled(3, bs)),
		"v1-sorted":       V1([][]byte{root}, SortedByCID(bs)),
		"v1-no-roots":     V1(nil, bs),
		"v1-version-1st":  V1With(HeaderBody([][]byte{root}, false), bs),
		"v1-dup":          V1([][]byte{root}, append(append([]Block(nil), bs...), bs[:5]...)),
		"v1-header-only":  V1([][]byte{root}, nil),
		"v2-noindex":      V2From(v1, V2Opts{}),
		"v2-mh-index":     V2From(v1, V2Opts{IndexCodec: MultihashIndexSortedCodec}),
		"v2-sorted-index": V2From(v1, V2Opts{IndexCodec: IndexSortedCodec}),
		"v2-padded":       V2From(v1, V2Opts{PadBefore: 37, PadAfter: 11, IndexCodec: MultihashIndexSortedCodec}),
		"v2-padbyte":      V2From(v1, V2Opts{PadBefore: 5, PadAfter: 5, PadByte: 0xaa, Char: [16]byte{0x80}}),
	}
	for _, inline := range []int{0, 512} {
		for name, car := range cases {
			buildRebuild(t, name, car, g, inline)
		}
	}
	// Different CID encodings / hashes of the same payloads.
	for name, rb := range map[string][]Block{
		"v0":       Recode(bs, CodecDagPB, MhSHA2_256, true),
		"dag-pb":   Recode(bs, CodecDagPB, MhSHA2_256, false),
		"sha512":   Recode(bs, CodecRaw, MhSHA2_512, false),
		"sha3-256": Recode(bs, CodecRaw, MhSHA3_256, false),
		"fake-mh":  Recode(bs, CodecRaw, MhFakeBlake, false),
	} {
		rep, st := buildRebuild(t, name, V1([][]byte{root}, rb), g, 0)
		switch name {
		case "sha512", "sha3-256":
			if st.Verified != len(bs) || st.Derived != 0 {
				t.Errorf("%s: %+v", name, st)
			}
		case "fake-mh":
			if st.NotVerified != len(bs) {
				t.Errorf("%s: %+v", name, st)
			}
		case "v0", "dag-pb":
			if st.Derived != len(bs) {
				t.Errorf("%s: %+v", name, st)
			}
		}
		_ = rep
	}
	// A rebuildable index is derived (no bytes kept); a tampered one is kept exact.
	rep, _ := buildRebuild(t, "v2-mh-index", cases["v2-mh-index"], g, 0)
	if rep.IndexMode != IndexDerived {
		t.Errorf("index mode %d, want derived", rep.IndexMode)
	}
	tamp := append([]byte(nil), cases["v2-mh-index"]...)
	tamp[len(tamp)-1] ^= 0xff
	rep, _ = buildRebuild(t, "v2-tampered-index", tamp, g, 0)
	if rep.IndexMode != IndexExact {
		t.Errorf("tampered index mode %d, want exact", rep.IndexMode)
	}
	// An index with an unknown codec is opaque but still exact.
	unk := V2From(v1, V2Opts{})
	d := unk[:len(unk):len(unk)]
	h := DeriveHeader(make([]byte, 16), 0, int64(len(v1)), 0, true)
	copy(d[V2PragmaLen:], h[:])
	d = append(d, 0xe0, 0x07, 1, 2, 3, 4) // multicodec 0x3e0, opaque body
	rep, st := buildRebuild(t, "v2-unknown-index", d, g, 0)
	if rep.IndexMode != IndexExact || st.IndexParsed {
		t.Errorf("unknown index: mode %d parsed %v", rep.IndexMode, st.IndexParsed)
	}
}

func TestRebuildCounts(t *testing.T) {
	bs, root := small()
	rb := Recode(bs, CodecDagPB, MhSHA2_256, false)
	two := append(append([]Block(nil), bs...), rb...)
	two = append(two, bs[3])
	_, st, err := Build(bytes.NewReader(V1([][]byte{root}, two)), int64(len(V1([][]byte{root}, two))), DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if st.DupCID != 1 || st.DupPayload != len(bs)+1 || st.DupPayloadDC != len(bs) {
		t.Errorf("duplicate accounting: %+v", st)
	}
}

func TestCARv2(t *testing.T) {
	bs, root := small()
	v1 := V1([][]byte{root}, bs)
	car := V2From(v1, V2Opts{PadBefore: 3, PadAfter: 2, IndexCodec: MultihashIndexSortedCodec})
	v, err := ParseV2(bytes.NewReader(car), int64(len(car)))
	if err != nil || v.DataOffset != 54 || v.DataSize != uint64(len(v1)) || len(v.PadBefore) != 3 || len(v.PadAfter) != 2 || v.IndexRaw == nil {
		t.Fatalf("%+v %v", v, err)
	}
	idx, ok, err := ParseIndex(v.IndexRaw)
	if err != nil || !ok || len(idx.Recs) != len(bs) {
		t.Fatalf("index: ok=%v n=%d err=%v", ok, len(idx.Recs), err)
	}
	for _, r := range idx.Recs { // every record must point at a section whose digest it names
		rd, _ := NewReader(bytes.NewReader(v1[r.Offset-uint64(0):]), DefaultLimits())
		_ = rd
		l, n, _ := Uvarint(v1[r.Offset:])
		c, _, err := ParseCID(v1[int(r.Offset)+n : int(r.Offset)+n+int(l)])
		if err != nil || !bytes.Equal(c.Digest, r.Digest) || c.HashCode != r.Mh {
			t.Fatalf("index record does not name its section: %v", err)
		}
	}
	// wrapper bound violations
	mut := func(f func(b []byte)) []byte { b := append([]byte(nil), car...); f(b); return b }
	bad := map[string][]byte{
		"short":         car[:30],
		"bad-pragma":    mut(func(b []byte) { b[3] ^= 1 }),
		"offset-low":    mut(func(b []byte) { b[V2PragmaLen+16] = 10 }),
		"data-past-eof": mut(func(b []byte) { b[V2PragmaLen+25] = 0x7f }),
		"index-before":  mut(func(b []byte) { b[V2PragmaLen+32] = 60; b[V2PragmaLen+33] = 0 }),
		"index-past":    mut(func(b []byte) { b[V2PragmaLen+39] = 0x7f }),
	}
	for name, b := range bad {
		if _, _, err := Build(bytes.NewReader(b), int64(len(b)), DefaultOptions()); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !IsFallback(err) {
			t.Errorf("%s: unclassified: %v", name, err)
		}
	}
	// A truncated index body must be rejected, not misparsed.
	cut := car[:len(car)-5]
	if _, _, err := Build(bytes.NewReader(cut), int64(len(cut)), DefaultOptions()); err == nil {
		t.Error("truncated index accepted")
	}
}

func TestRealTiny(t *testing.T) {
	for _, f := range []string{"sample-rw-bs-v2.car", "sample-unixfs-v2.car"} {
		b, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		rep, st, err := Build(bytes.NewReader(b), int64(len(b)), DefaultOptions())
		if err != nil {
			t.Logf("%s: %v (fallback)", f, err)
			continue
		}
		get := func(s [32]byte) ([]byte, error) {
			for _, r := range rep.Refs {
				if r.Sum == s {
					return b[r.SrcOff : r.SrcOff+r.Len], nil
				}
			}
			return nil, errors.New("missing")
		}
		var out bytes.Buffer
		if err := rep.Reconstruct(&out, get); err != nil || !bytes.Equal(out.Bytes(), b) {
			t.Fatalf("%s: reconstruct differs: %v", f, err)
		}
		t.Logf("%s: %d bytes, %d sections, index mode %d, %+v", f, len(b), st.Sections, rep.IndexMode, st)
	}
}

// cacheDir holds the pinned real-world samples fetched by `z3car fetch`.
func cacheDir() string {
	if d := os.Getenv("Z3_CAR_CACHE"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "z3-realworld", "car")
}

func TestRealSamples(t *testing.T) {
	type want struct {
		sections int
		mode     IndexMode
		v2       bool
		fallback bool
	}
	for f, w := range map[string]want{
		"sample-v1.car":                         {1049, 0, false, false},
		"sample-wrapped-v2.car":                 {1049, IndexDerived, true, false},
		"sample-v2-indexless.car":               {1049, IndexNone, true, false},
		"sample-v1-with-zero-len-section.car":   {fallback: true},
		"sample-v1-tailing-corrupt-section.car": {fallback: true},
	} {
		b, err := os.ReadFile(filepath.Join(cacheDir(), f))
		if err != nil {
			t.Skipf("real sample %s not fetched: %v", f, err)
		}
		rep, st, err := Build(bytes.NewReader(b), int64(len(b)), DefaultOptions())
		if w.fallback {
			if err == nil || !IsFallback(err) {
				t.Errorf("%s: want a classified fallback, got %v", f, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if st.Sections != w.sections || rep.V2 != w.v2 || rep.IndexMode != w.mode {
			t.Errorf("%s: sections %d v2 %v index %d; stats %+v", f, st.Sections, rep.V2, rep.IndexMode, st)
		}
		get := func(s [32]byte) ([]byte, error) {
			for _, r := range rep.Refs {
				if r.Sum == s {
					return b[r.SrcOff : r.SrcOff+r.Len], nil
				}
			}
			return nil, errors.New("missing")
		}
		var out bytes.Buffer
		if err := rep.Reconstruct(&out, get); err != nil || !bytes.Equal(out.Bytes(), b) {
			t.Fatalf("%s: reconstruct differs: %v", f, err)
		}
		t.Logf("%s: %d B, %+v, header %d B, index %d B, derived index %v", f, len(b), st, len(rep.Header), rep.IndexRaw, rep.IndexMode == IndexDerived)
	}
}

func TestReadRange(t *testing.T) {
	bs, root := small()
	v1 := V1([][]byte{root}, bs)
	g := getter(bs)
	for name, car := range map[string][]byte{
		"v1": v1,
		"v2": V2From(v1, V2Opts{PadBefore: 9, PadAfter: 7, IndexCodec: MultihashIndexSortedCodec}),
	} {
		rep, _ := buildRebuild(t, name, car, g, 256)
		for _, c := range [][2]int64{{0, 1}, {0, int64(len(car))}, {5, 100}, {int64(len(car)) / 2, 5000}, {int64(len(car)) - 3, 3}, {int64(len(car)), 0}, {70, 0}} {
			got, _, _, err := rep.ReadRange(c[0], c[1], g)
			if err != nil || !bytes.Equal(got, car[c[0]:c[0]+c[1]]) {
				t.Errorf("%s range %v: %v", name, c, err)
			}
		}
		if _, _, _, err := rep.ReadRange(int64(len(car))-1, 2, g); err == nil {
			t.Errorf("%s: range past the end accepted", name)
		}
		// a small range far from the index fetches only the blocks it overlaps
		_, fetched, scanned, _ := rep.ReadRange(int64(len(car))/2, 100, g)
		if fetched > 3100 || scanned > len(rep.Refs) {
			t.Errorf("%s: fetched %d scanned %d", name, fetched, scanned)
		}
	}
}
