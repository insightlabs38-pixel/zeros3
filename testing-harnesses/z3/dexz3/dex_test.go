package dexz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func fixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func parse(b []byte) (*File, error) { return Parse(bytes.NewReader(b), int64(len(b)), Limits{}) }

// rt encodes, discards all analysis state and decodes from Meta and the component bytes only.
func rt(t testing.TB, b []byte, cfg Config) *Rep {
	t.Helper()
	f, err := parse(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rep, err := Encode(f, b, cfg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	meta := append([]byte(nil), rep.Meta...)
	comps := make([][]byte, len(rep.Comps))
	for i, c := range rep.Comps {
		comps[i] = append([]byte(nil), c...)
	}
	rep = nil
	out, err := Decode(meta, comps)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(out, b) {
		t.Fatalf("decode differs from the input (%d vs %d bytes)", len(out), len(b))
	}
	return &Rep{Meta: meta, Comps: comps}
}

func fixedCut(n int) func([]byte, []Sec) []int {
	return func(p []byte, _ []Sec) []int {
		var out []int
		for e := n; e < len(p); e += n {
			out = append(out, e)
		}
		return out
	}
}

func sectionCut(p []byte, secs []Sec) []int {
	var out []int
	for _, s := range secs {
		out = append(out, s.End)
	}
	return out
}

func TestMinimalAndRealFixturesRoundTrip(t *testing.T) {
	min := (&tDex{}).build()
	cases := []struct {
		name string
		b    []byte
	}{
		{"empty DEX", min},
		{"one class", small([]uint16{0x000e}).build()}, // return-void
		{"api24 (DEX 037, D8)", fixture(t, "fixture-api24.dex")},
		{"api28 (DEX 039, D8)", fixture(t, "fixture-api28.dex")},
	}
	cuts := map[string]func([]byte, []Sec) []int{"one": nil, "sections": sectionCut, "fixed 700": fixedCut(700)}
	for _, c := range cases {
		for cn, cut := range cuts {
			for _, k0 := range []int{1, 2, 3, 4} {
				t.Run(fmt.Sprintf("%s/%s/K%d", c.name, cn, k0), func(t *testing.T) { rt(t, c.b, Config{K0: k0, Cut: cut}) })
			}
		}
	}
	// the real fixtures expose every kind of slot the thesis is about
	b := fixture(t, "fixture-api28.dex")
	f, _ := parse(b)
	w, err := Walk(f, b, WalkOpts{Offsets: true, Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, s := range w.Slots {
		seen[fmt.Sprintf("%s/%s", s.Cls, s.Space)] = true
	}
	for _, want := range []string{"idtab/string", "idtab/type", "idtab/proto", "idtab/offset", "classdef/type", "classdef/offset", "typelist/type", "classdata/field", "classdata/method", "classdata/offset", "insn/string", "insn/type", "insn/field", "insn/method", "codehdr/offset", "anno/type", "anno/string", "anno/offset", "handler/type"} {
		if !seen[want] {
			t.Errorf("fixture has no %s slot (seen %v)", want, len(seen))
		}
	}
}

// walkOne runs the instruction walker over one instruction and returns the operand spaces.
func walkOne(units []uint16) ([]Space, error) {
	b := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[2*i:], u)
	}
	var sp []Space
	err := walkInsns(b, len(units), func(_ int, s Space, _ bool) error { sp = append(sp, s); return nil })
	return sp, err
}

// The Dalvik opcode table, retyped from the public bytecode reference: width in code units and the
// index operands.
func TestInstructionTable(t *testing.T) {
	type row struct {
		lo, hi int
		w      int
		sp     []Space
	}
	rows := []row{
		{0x00, 0x00, 1, nil}, {0x01, 0x01, 1, nil}, {0x02, 0x02, 2, nil}, {0x03, 0x03, 3, nil}, {0x04, 0x04, 1, nil},
		{0x05, 0x05, 2, nil}, {0x06, 0x06, 3, nil}, {0x07, 0x07, 1, nil}, {0x08, 0x08, 2, nil}, {0x09, 0x09, 3, nil},
		{0x0a, 0x11, 1, nil}, {0x12, 0x12, 1, nil}, {0x13, 0x13, 2, nil}, {0x14, 0x14, 3, nil}, {0x15, 0x16, 2, nil},
		{0x17, 0x17, 3, nil}, {0x18, 0x18, 5, nil}, {0x19, 0x19, 2, nil}, {0x1a, 0x1a, 2, []Space{SpString}},
		{0x1b, 0x1b, 3, []Space{SpString}}, {0x1c, 0x1c, 2, []Space{SpType}}, {0x1d, 0x1e, 1, nil},
		{0x1f, 0x20, 2, []Space{SpType}}, {0x21, 0x21, 1, nil}, {0x22, 0x23, 2, []Space{SpType}},
		{0x24, 0x25, 3, []Space{SpType}}, {0x26, 0x26, 3, nil}, {0x27, 0x27, 1, nil}, {0x28, 0x28, 1, nil},
		{0x29, 0x29, 2, nil}, {0x2a, 0x2a, 3, nil}, {0x2b, 0x2c, 3, nil}, {0x2d, 0x31, 2, nil}, {0x32, 0x3d, 2, nil},
		{0x44, 0x51, 2, nil}, {0x52, 0x6d, 2, []Space{SpField}}, {0x6e, 0x72, 3, []Space{SpMethod}},
		{0x74, 0x78, 3, []Space{SpMethod}}, {0x7b, 0x8f, 1, nil}, {0x90, 0xaf, 2, nil}, {0xb0, 0xcf, 1, nil},
		{0xd0, 0xe2, 2, nil}, {0xfa, 0xfb, 4, []Space{SpMethod, SpProto}}, {0xfc, 0xfd, 3, nil}, {0xfe, 0xfe, 2, nil},
		{0xff, 0xff, 2, []Space{SpProto}},
	}
	valid := map[int]row{}
	for _, r := range rows {
		for op := r.lo; op <= r.hi; op++ {
			valid[op] = r
		}
	}
	for op := 0; op < 256; op++ {
		r, ok := valid[op]
		if !ok {
			// unused opcodes and the payload idents must be refused as instructions
			if _, err := walkOne([]uint16{uint16(op), 0, 0, 0, 0}); !errors.Is(err, ErrFormat) {
				t.Errorf("unused opcode %#x accepted (%v)", op, err)
			}
			continue
		}
		u := make([]uint16, r.w)
		u[0] = uint16(op)
		sp, err := walkOne(u)
		if err != nil {
			t.Errorf("opcode %#x: %v", op, err)
			continue
		}
		if fmt.Sprint(sp) != fmt.Sprint(r.sp) {
			t.Errorf("opcode %#x: operands %v, want %v", op, sp, r.sp)
		}
		// one unit short must fail, one unit long is the next (nop) instruction
		if r.w > 1 {
			if _, err := walkOne(u[:r.w-1]); !errors.Is(err, ErrFormat) {
				t.Errorf("opcode %#x: truncated instruction accepted", op)
			}
		}
		if _, err := walkOne(append(u, 0)); err != nil {
			t.Errorf("opcode %#x followed by nop: %v", op, err)
		}
	}
}

func TestPayloadPseudoInstructions(t *testing.T) {
	cases := []struct {
		name  string
		units []uint16
		ok    bool
	}{
		{"packed-switch size 2", []uint16{0x0100, 2, 0, 0, 5, 0, 6, 0, 0x000e}, true},
		{"packed-switch size 0", []uint16{0x0100, 0, 0, 0, 0x000e}, true},
		{"packed-switch overruns", []uint16{0x0100, 9, 0, 0}, false},
		{"sparse-switch size 2", []uint16{0x0200, 2, 1, 0, 2, 0, 7, 0, 8, 0, 0x000e}, true},
		{"sparse-switch overruns", []uint16{0x0200, 5, 0}, false},
		{"fill-array-data bytes", []uint16{0x0300, 1, 3, 0, 0x0201, 0x0003}, true},
		{"fill-array-data shorts", []uint16{0x0300, 2, 2, 0, 1, 2}, true},
		{"fill-array-data wide", []uint16{0x0300, 8, 1, 0, 1, 2, 3, 4}, true},
		{"fill-array-data bad width", []uint16{0x0300, 9, 1, 0, 1, 2, 3, 4, 5}, false},
		{"fill-array-data overruns", []uint16{0x0300, 4, 100, 0, 1}, false},
		{"unknown pseudo ident", []uint16{0x0400, 0, 0, 0}, false},
		{"truncated header", []uint16{0x0300, 1}, false},
	}
	for _, c := range cases {
		_, err := walkOne(c.units)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
	// switch and array payloads inside a real method round-trip
	code := []uint16{
		0x012b, 0, 0, // packed-switch v1, +payload below (offset unused by the walker)
		0x000e,
		0x0100, 2, 0, 0, 3, 0, 4, 0, // packed payload
		0x0300, 1, 2, 0, 0x0201, // fill-array-data payload (2 bytes)
		0x000e,
	}
	rt(t, small(code).build(), Config{})
}

func TestJumboReferencesAndBoundaries(t *testing.T) {
	// 70 000 strings: const-string/jumbo names string 69 999 (a 32-bit operand, beyond 16 bits)
	strs := []string{"LA;", "V", "run"}
	for i := 0; i < 70000; i++ {
		strs = append(strs, fmt.Sprintf("s%06d", i))
	}
	sort.Strings(strs)
	idx := map[string]int{}
	for i, s := range strs {
		idx[s] = i
	}
	j := idx["s069999"]
	d := small([]uint16{0x001b, uint16(j), uint16(j >> 16), 0x000e}, strs[3:]...)
	d.strs = strs
	img := d.build()
	f, err := parse(img)
	if err != nil {
		t.Fatal(err)
	}
	w, err := Walk(f, img, WalkOpts{Offsets: true})
	if err != nil {
		t.Fatal(err)
	}
	wide := 0
	for _, s := range w.Slots {
		if s.Cls == ClInsn && s.Len == 4 && s.Space == SpString && s.Val == uint32(j) {
			wide++
		}
	}
	if wide != 1 {
		t.Fatalf("jumbo operand not found as a 32-bit string slot (%d)", wide)
	}
	rt(t, img, Config{K0: 3})

	// class_data deltas across every ULEB length boundary, with canonical and padded encodings
	nf := 40000
	fs := make([]tField, nf)
	d2 := small([]uint16{0x000e})
	for i := range fs {
		fs[i] = tField{0, 1, 2}
	}
	// fields must be unique and sorted: give each a distinct name by adding strings
	strs2 := []string{"LA;", "V", "run"}
	for i := 0; i < nf; i++ {
		strs2 = append(strs2, fmt.Sprintf("f%05d", i))
	}
	sort.Strings(strs2)
	idx2 := map[string]int{}
	for i, s := range strs2 {
		idx2[s] = i
	}
	d2.strs = strs2
	d2.types = []int{idx2["LA;"], idx2["V"]}
	d2.protos = []tProto{{shorty: idx2["V"], ret: 1}}
	for i := 0; i < nf; i++ {
		fs[i] = tField{0, 1, idx2[fmt.Sprintf("f%05d", i)]}
	}
	d2.fields = fs
	d2.methods = []tMethod{{0, 0, idx2["run"]}}
	picks := []int{0, 1, 128, 256, 16639, 33023, nf - 1} // deltas 1, 127 (1 byte), 128 (2), 16383 (2), 16384 (3), 6976
	var ifields []tMember
	prev := -1
	for _, p := range picks {
		if p <= prev {
			continue
		}
		ifields = append(ifields, tMember{idx: p, flags: 1})
		prev = p
	}
	// a padded ULEB (0x81 0x00 = 1, two bytes) for the second field
	ifields[1].raw = []byte{0x81, 0x00}
	c := d2.classes[0]
	c.ifields = ifields
	d2.classes[0] = c
	img2 := d2.build()
	rt(t, img2, Config{})
	// the padded delta kept its length
	f2, _ := parse(img2)
	w2, err := Walk(f2, img2, WalkOpts{})
	if err != nil {
		t.Fatal(err)
	}
	lens := map[uint8]bool{}
	for _, s := range w2.Slots {
		if s.Enc == EncDelta {
			lens[s.Len] = true
		}
	}
	for _, l := range []uint8{1, 2, 3} {
		if !lens[l] {
			t.Errorf("no class_data delta of %d bytes (%v)", l, lens)
		}
	}
	// non-increasing members are not represented
	d3 := small([]uint16{0x000e})
	c3 := d3.classes[0]
	c3.dmethods = []tMember{{idx: 0, flags: 1}, {idx: 0, flags: 1, raw: []byte{0}}}
	d3.classes[0] = c3
	if _, err := parse(d3.build()); err != nil {
		t.Fatal(err)
	}
	img3 := d3.build()
	f3, _ := parse(img3)
	if _, err := Encode(f3, img3, Config{}); err == nil {
		t.Fatal("duplicate class_data member encoded")
	}
}

func patch32(b []byte, off int, v uint32) { binary.LittleEndian.PutUint32(b[off:], v) }
func get32(b []byte, off int) uint32      { return binary.LittleEndian.Uint32(b[off:]) }

func TestMalformedInputsAreRefused(t *testing.T) {
	base := func(mut func(b []byte)) []byte {
		d := small([]uint16{0x001a, 0, 0x000e})
		d.mutate = mut
		return d.build()
	}
	// offsets into the finished image
	hdr := func(b []byte, off int) int { return int(get32(b, off)) }
	cases := []struct {
		name string
		mut  func(b []byte)
		want error // errors.Is target; nil = any error
	}{
		{"bad magic", func(b []byte) { b[0] = 'x' }, ErrFormat},
		{"version 042", func(b []byte) { b[6] = '2'; b[5] = '4' }, ErrUnsupported},
		{"non-digit version", func(b []byte) { b[5] = 'z' }, ErrFormat},
		{"file_size lies", func(b []byte) { patch32(b, 32, get32(b, 32)+1) }, ErrFormat},
		{"header_size", func(b []byte) { patch32(b, 36, 100) }, ErrFormat},
		{"endian tag", func(b []byte) { patch32(b, 40, 0x78563412) }, ErrUnsupported},
		{"link section", func(b []byte) { patch32(b, 44, 8) }, ErrUnsupported},
		{"map offset outside", func(b []byte) { patch32(b, 52, 0xFFFFFF00) }, ErrFormat},
		{"map offset unaligned", func(b []byte) { patch32(b, 52, get32(b, 52)+1) }, ErrFormat},
		{"string_ids_size beyond limit", func(b []byte) { patch32(b, 56, 0xFFFFFFFF) }, ErrLimit},
		{"class_defs_size beyond limit", func(b []byte) { patch32(b, 96, 0x7FFFFFFF) }, ErrLimit},
		{"header disagrees with map", func(b []byte) { patch32(b, 64, get32(b, 64)+1) }, ErrFormat},
		{"data section outside", func(b []byte) { patch32(b, 104, 0xFFFFFFF0) }, ErrFormat},
		{"map entry count", func(b []byte) { patch32(b, hdr(b, 52), 0xFFFFFFFF) }, ErrFormat},
		{"map offsets not increasing", func(b []byte) { m := hdr(b, 52); patch32(b, m+4+12+8, 0) }, ErrFormat},
		{"type descriptor index", func(b []byte) { patch32(b, hdr(b, 68), 9999) }, ErrFormat},
		{"proto return type index", func(b []byte) { patch32(b, hdr(b, 76)+4, 9999) }, ErrFormat},
		{"method name index", func(b []byte) { patch32(b, hdr(b, 92)+4, 9999) }, ErrFormat},
		{"method class index", func(b []byte) { binary.LittleEndian.PutUint16(b[hdr(b, 92):], 9999) }, ErrFormat},
		{"class_data offset outside", func(b []byte) { patch32(b, hdr(b, 100)+24, 0xFFFFFF00) }, ErrFormat},
		{"class_def superclass index", func(b []byte) { patch32(b, hdr(b, 100)+8, 9999) }, ErrFormat},
		{"unterminated last string", func(b []byte) {
			n := int(get32(b, 56))
			last := hdr(b, 60) + 4*(n-1)
			o := int(get32(b, last))
			for b[o] != 0 || o < int(get32(b, last))+2 {
				o++
			}
			b[o] = 'x'
		}, ErrFormat},
	}
	for _, c := range cases {
		b := base(c.mut)
		f, err := parse(b)
		if err == nil {
			_, err = Walk(f, b, WalkOpts{Offsets: true, Debug: true})
		}
		if err == nil {
			t.Errorf("%s: accepted", c.name)
		} else if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	// code-level corruption: operand index out of range, hostile insns_size, bad tries
	code := []uint16{0x001a, 0, 0x000e}
	codeCases := []struct {
		name string
		mut  func(b []byte, codeOff int)
	}{
		{"const-string index out of range", func(b []byte, o int) { binary.LittleEndian.PutUint16(b[o+16+2:], 9999) }},
		{"insns_size beyond the section", func(b []byte, o int) { patch32(b, o+12, 1<<20) }},
		{"insns_size beyond the limit", func(b []byte, o int) { patch32(b, o+12, 1<<30) }},
		{"invalid opcode", func(b []byte, o int) { binary.LittleEndian.PutUint16(b[o+16:], 0x003e) }},
		{"tries beyond the section", func(b []byte, o int) { binary.LittleEndian.PutUint16(b[o+6:], 60000) }},
	}
	for _, c := range codeCases {
		d := small(code)
		var co int
		d.mutate = func(b []byte) {
			// the single code item is the first data item
			co = int(get32(b, 108))
			c.mut(b, co)
		}
		b := d.build()
		f, err := parse(b)
		if err == nil {
			_, err = Walk(f, b, WalkOpts{Offsets: true, Debug: true})
		}
		if err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

func TestTruncationAndHostileBytesNeverPanic(t *testing.T) {
	for _, name := range []string{"fixture-api24.dex", "fixture-api28.dex"} {
		b := fixture(t, name)
		for n := 0; n < len(b); n += 1 + len(b)/700 {
			f, err := parse(b[:n])
			if err == nil {
				if _, err = Walk(f, b[:n], WalkOpts{Offsets: true, Debug: true}); err == nil {
					t.Fatalf("%s truncated to %d bytes parsed", name, n)
				}
			}
		}
		// every byte of the header and map, flipped
		for i := 0; i < 112; i++ {
			c := append([]byte(nil), b...)
			c[i] ^= 0xff
			if f, err := parse(c); err == nil {
				if w, err := Walk(f, c, WalkOpts{Offsets: true, Debug: true}); err == nil && w != nil {
					if rep, err := Encode(f, c, Config{}); err == nil {
						if out, err := Decode(rep.Meta, rep.Comps); err != nil || !bytes.Equal(out, c) {
							t.Fatalf("header byte %d: encode succeeded but the round trip failed", i)
						}
					}
				}
			}
		}
	}
}

func TestAmbiguousIdentityFallsBack(t *testing.T) {
	// two identical strings: a string identity names two table entries
	d := small([]uint16{0x000e}, "dup", "dup")
	b := d.build()
	f, err := parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Encode(f, b, Config{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("duplicate strings: %v", err)
	}
	// two methods with one identity
	d = small([]uint16{0x000e})
	d.methods = append(d.methods, d.methods[0])
	b = d.build()
	f, _ = parse(b)
	if _, err := Encode(f, b, Config{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("duplicate methods: %v", err)
	}
}

func TestTamperedRepresentationIsRefused(t *testing.T) {
	b := fixture(t, "fixture-api28.dex")
	rep := rt(t, b, Config{Cut: fixedCut(900)})
	clone := func() ([]byte, [][]byte) {
		cs := make([][]byte, len(rep.Comps))
		for i, c := range rep.Comps {
			cs[i] = append([]byte(nil), c...)
		}
		return append([]byte(nil), rep.Meta...), cs
	}
	// Decode may ignore a byte that carries no information (the zeros inside a masked slot are
	// overwritten), but it must never return different bytes without an error: the header checksum
	// of a valid DEX is re-verified on the rebuilt file.
	wrong, refused, benign := 0, 0, 0
	try := func(name string, meta []byte, cs [][]byte) {
		out, err := Decode(meta, cs)
		switch {
		case err != nil:
			refused++
		case bytes.Equal(out, b):
			benign++
		default:
			wrong++
			t.Errorf("%s: wrong bytes returned without an error", name)
		}
	}
	for ci := range rep.Comps {
		for _, at := range []int{0, 2, len(rep.Comps[ci]) / 3, len(rep.Comps[ci]) / 2, len(rep.Comps[ci]) - 1} {
			meta, cs := clone()
			cs[ci][at] ^= 0x21
			try(fmt.Sprintf("component %d byte %d", ci, at), meta, cs)
		}
	}
	meta, cs := clone()
	meta[6+8] ^= 1 // checksum field of the header copy
	try("header checksum", meta, cs)
	meta, cs = clone()
	try("dropped component", meta, cs[1:])
	meta, cs = clone()
	try("duplicated component", meta, append(cs, cs[0]))
	meta, cs = clone()
	try("truncated meta", meta[:len(meta)-9], cs)
	meta, cs = clone()
	meta[5] = 9
	try("K0", meta, cs)
	meta, cs = clone()
	cs[0] = cs[0][:len(cs[0])/2]
	try("short component", meta, cs)
	meta, cs = clone()
	patch32(meta, 6+32, 0x7ffffff0) // file_size claims 2 GiB: refused before any allocation
	try("hostile file size", meta, cs)
	if refused <= benign || wrong != 0 {
		t.Errorf("tampering: %d refused, %d benign (information-free bytes), %d wrong", refused, benign, wrong)
	}
	// without a valid header checksum nothing inside the representation can notice a payload flip:
	// the object-level SHA-256 (repz3) is then the only guard, and the encoder says so by not
	// setting the checksum flag.
	d := small([]uint16{0x001a, 0, 0x000e})
	d.badChecksum = true
	img := d.build()
	r2 := rt(t, img, Config{})
	if r2.Meta[4]&flagAdler != 0 {
		t.Error("checksum flag set for a DEX whose header checksum is invalid")
	}
}

func repack(p []byte, ss [][]byte) []byte {
	c := binary.AppendUvarint(nil, uint64(len(p)))
	for _, s := range ss {
		c = binary.AppendUvarint(c, uint64(len(s)))
	}
	c = append(c, p...)
	for _, s := range ss {
		c = append(c, s...)
	}
	return c
}

// A masked walk finds exactly the slots of the unmasked walk: that equality is what lets the
// decoder locate every slot from the payload alone.
func TestMaskedWalkFindsTheSameSlots(t *testing.T) {
	for _, name := range []string{"fixture-api24.dex", "fixture-api28.dex"} {
		b := fixture(t, name)
		f, _ := parse(b)
		w, err := Walk(f, b, WalkOpts{Offsets: true, Debug: true})
		if err != nil {
			t.Fatal(err)
		}
		m := append([]byte(nil), b...)
		Mask(m, w.Slots, nil)
		f2, err := parse(m)
		if err != nil {
			t.Fatal(err)
		}
		w2, err := Walk(f2, m, WalkOpts{Masked: true, Offsets: true, Debug: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(w.Slots) != len(w2.Slots) {
			t.Fatalf("%s: %d slots vs %d masked", name, len(w.Slots), len(w2.Slots))
		}
		for i := range w.Slots {
			a, c := w.Slots[i], w2.Slots[i]
			if a.Off != c.Off || a.Len != c.Len || a.Enc != c.Enc || a.Space != c.Space || a.Kind != c.Kind || a.Prev != c.Prev {
				t.Fatalf("%s: slot %d differs: %+v vs %+v", name, i, a, c)
			}
		}
	}
}

func FuzzDex(f *testing.F) {
	f.Add(fixture(f, "fixture-api24.dex"))
	f.Add(fixture(f, "fixture-api28.dex"))
	f.Add(small([]uint16{0x001a, 0, 0x000e}).build())
	f.Add((&tDex{}).build())
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			return
		}
		df, err := Parse(bytes.NewReader(b), int64(len(b)), Limits{})
		if err != nil {
			return
		}
		rep, err := Encode(df, b, Config{Cut: fixedCut(1500)})
		if err != nil {
			return
		}
		out, err := Decode(rep.Meta, rep.Comps)
		if err != nil || !bytes.Equal(out, b) {
			t.Fatalf("encode succeeded but decode failed: %v", err)
		}
	})
}
