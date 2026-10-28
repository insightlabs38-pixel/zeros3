package wasmz3

import (
	"bytes"
	"encoding/binary"
	"testing"

	"zeros3-z3/idxref"
)

// ---- a test-only module assembler (SYNTHETIC_FIXTURE) --------------------------------------

func uleb(v uint64) []byte { return padded(v, 0) }

// padded encodes v in at least w bytes (w=0: shortest).
func padded(v uint64, w int) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 || len(out)+1 < w {
			out = append(out, c|0x80)
			continue
		}
		return append(out, c)
	}
}

func sec(id byte, parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	return append(append([]byte{id}, uleb(uint64(len(body)))...), body...)
}

func vec(items ...[]byte) []byte {
	out := uleb(uint64(len(items)))
	for _, i := range items {
		out = append(out, i...)
	}
	return out
}

func name(s string) []byte { return append(uleb(uint64(len(s))), s...) }

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// body assembles a function body (locals vector + code + end).
func body(code ...[]byte) []byte {
	b := cat(append([][]byte{vec([]byte{1, 0x7f})}, code...)...)
	b = append(b, 0x0b)
	return append(uleb(uint64(len(b))), b...)
}

var header = []byte("\x00asm\x01\x00\x00\x00")

func ft(params, results string) []byte {
	return cat([]byte{0x60}, vec(bytes1(params)...), vec(bytes1(results)...))
}

func bytes1(s string) [][]byte {
	var out [][]byte
	for _, c := range []byte(s) {
		out = append(out, []byte{c})
	}
	return out
}

// full builds a module that uses every module-level index space.
func full(callW int) []byte {
	call := func(f uint64) []byte { return append([]byte{0x10}, padded(f, callW)...) }
	types := sec(SecType, vec(ft("\x7f", "\x7f"), ft("", ""), ft("\x7f\x7f", "\x7f")))
	imports := sec(SecImport, vec(
		cat(name("env"), name("log"), []byte{0}, uleb(0)),
		cat(name("env"), name("tbl"), []byte{1, 0x70, 0, 4}),
		cat(name("env"), name("mem"), []byte{2, 0, 1}),
		cat(name("env"), name("g"), []byte{3, 0x7f, 0}),
		cat(name("env"), name("ex"), []byte{4, 0}, uleb(1)),
	))
	funcs := sec(SecFunction, vec(uleb(0), uleb(1), uleb(2)))
	tables := sec(SecTable, vec([]byte{0x70, 0, 2}))
	globals := sec(SecGlobal, vec(cat([]byte{0x7f, 1, 0x41, 7, 0x0b}), cat([]byte{0x70, 0, 0xd2}, uleb(1), []byte{0x0b})))
	globals = sec(SecGlobal, vec(cat([]byte{0x7f, 1, 0x41, 7, 0x0b}), cat([]byte{0x7f, 0, 0x23}, uleb(0), []byte{0x0b})))
	tags := sec(SecTag, vec(cat([]byte{0}, uleb(1))))
	exports := sec(SecExport, vec(
		cat(name("f1"), []byte{0}, uleb(2)),
		cat(name("t"), []byte{1}, uleb(1)),
		cat(name("m"), []byte{2}, uleb(0)),
		cat(name("g"), []byte{3}, uleb(1)),
		cat(name("tag"), []byte{4}, uleb(1)),
	))
	start := sec(SecStart, uleb(2))
	elem := sec(SecElement, vec(
		cat(uleb(0), []byte{0x41, 0, 0x0b}, vec(uleb(1), uleb(2))),
		cat(uleb(2), uleb(1), []byte{0x41, 1, 0x0b}, []byte{0}, vec(uleb(3))),
		cat(uleb(1), []byte{0}, vec(uleb(1))),
	))
	dcount := sec(SecDataCount, uleb(2))
	code := sec(SecCode, vec(
		body(call(0), []byte{0x1a}),
		body([]byte{0x02, 0x00}, call(1), []byte{0x0b, 0x03, 0x01}, []byte{0x0b}),                                                           // block type 0 (one-byte index), loop with type index
		body([]byte{0x20, 0}, []byte{0x41, 5}, call(2), []byte{0x11}, uleb(2), uleb(0), []byte{0x0b, 0x23}, uleb(0), []byte{0x24}, uleb(1)), // call_indirect, global.get/set
	))
	code = sec(SecCode, vec(
		body(call(0), []byte{0x1a}),
		body([]byte{0x02, 0x00}, call(1), []byte{0x0b}),
		body([]byte{0x20, 0, 0x41, 5}, call(2), []byte{0x11}, padded(2, callW), padded(0, callW), []byte{0x23}, padded(0, callW), []byte{0x24}, padded(1, callW), []byte{0x25, 0, 0x26, 0, 0xd2}, padded(1, callW), []byte{0x1a, 0xfc, 8}, uleb(0), []byte{0}, []byte{0xfc, 9}, uleb(1),
			[]byte{0xfc, 10, 0, 0, 0xfc, 11, 0, 0xfc, 12}, uleb(2), uleb(0), []byte{0xfc, 13}, uleb(2), []byte{0xfc, 14, 0, 0, 0xfc, 15, 0, 0xfc, 16, 0, 0xfc, 17, 0},
			[]byte{0x3f, 0, 0x40, 0, 0x28, 2, 0, 0x36, 2, 4, 0x08}, uleb(1), []byte{0x1a}),
	))
	data := sec(SecData, vec(
		cat(uleb(0), []byte{0x41, 0, 0x0b}, uleb(3), []byte{1, 2, 3}),
		cat(uleb(1), uleb(2), []byte{9, 9}),
	))
	return cat(header, types, imports, funcs, tables, sec(SecMemory, vec([]byte{0, 1})), tags, globals, exports, start, elem, dcount, code, data)
}

func analyze(t *testing.T, d []byte) (*Dir, *Module) {
	t.Helper()
	dir, err := Parse(bytes.NewReader(d), int64(len(d)), Limits{})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m, err := Analyze(dir, d, Limits{})
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	return dir, m
}

func TestLEB(t *testing.T) {
	for _, v := range []uint64{0, 1, 63, 64, 127, 128, 300, 16383, 16384, 1<<32 - 1} {
		for _, w := range []int{0, 1, 2, 3, 5} {
			b := padded(v, w)
			got, gw, ok := leb(b, 0, 5)
			if !ok || got != v || gw != len(b) {
				t.Errorf("padded(%d,%d)=%x reads %d width %d ok=%v", v, w, b, got, gw, ok)
			}
			buf := make([]byte, len(b))
			if !putLEB(buf, len(b), v) || !bytes.Equal(buf, b) {
				t.Errorf("putLEB(%d, width %d) = %x want %x", v, len(b), buf, b)
			}
		}
	}
	if _, _, ok := leb([]byte{0xff, 0xff, 0xff, 0xff, 0x7f}, 0, 5); ok {
		t.Error("u32 overflow accepted")
	}
	if _, _, ok := leb([]byte{0x80}, 0, 5); ok {
		t.Error("truncated LEB accepted")
	}
	if putLEB(make([]byte, 1), 1, 128) || putLEB(make([]byte, 2), 2, 1<<14) {
		t.Error("value that needs more bytes was written")
	}
	// s33 block types: the sign bit of the last byte must stay clear
	b := make([]byte, 1)
	if !putSLEB33(b, 1, 63) || b[0] != 63 || putSLEB33(b, 1, 64) {
		t.Errorf("one-byte s33: %x", b)
	}
	b = make([]byte, 2)
	if !putSLEB33(b, 2, 64) || b[0] != 0xc0 || b[1] != 0 || putSLEB33(b, 2, 1<<13) {
		t.Errorf("two-byte s33: %x", b)
	}
	if w, ok := lebWidth([]byte{0xff, 0xff, 0xff, 0xff, 0x0f}, 0, 5); !ok || w != 5 {
		t.Error("lebWidth")
	}
}

func TestParseDirectory(t *testing.T) {
	good := full(1)
	if _, err := Parse(bytes.NewReader(good), int64(len(good)), Limits{}); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"magic":              append([]byte("\x01asm\x01\x00\x00\x00"), good[8:]...),
		"component layer":    append([]byte("\x00asm\x0d\x00\x01\x00"), good[8:]...),
		"unknown version":    append([]byte("\x00asm\x02\x00\x00\x00"), good[8:]...),
		"short":              good[:5],
		"section overruns":   append(append([]byte(nil), header...), 1, 0x7f, 0),
		"section order":      cat(header, sec(SecFunction, vec()), sec(SecType, vec())),
		"unknown section id": cat(header, []byte{14, 0}),
		"bad size LEB":       cat(header, []byte{1, 0xff}),
	}
	for n, d := range cases {
		if _, err := Parse(bytes.NewReader(d), int64(len(d)), Limits{}); err == nil {
			t.Errorf("%s: parsed", n)
		}
	}
	// custom sections may appear anywhere and keep their names
	d := cat(header, sec(0, name("a"), []byte{1}), sec(SecType, vec()), sec(0, name("b")), sec(SecFunction, vec()))
	dir, err := Parse(bytes.NewReader(d), int64(len(d)), Limits{})
	if err != nil || len(dir.Secs) != 4 || dir.Secs[0].Name != "a" || dir.Secs[2].Name != "b" {
		t.Fatalf("custom sections: %v %+v", err, dir)
	}
}

func TestSlots(t *testing.T) {
	for _, w := range []int{1, 2, 5} {
		d := full(w)
		_, m := analyze(t, d)
		var per [NumSpaces]int
		for _, s := range m.Slots {
			per[s.Sp]++
			if int(s.W) < 1 || int(s.Off)+int(s.W) > len(d) {
				t.Fatalf("slot %+v outside the module", s)
			}
			v, gw, ok := leb(d, int(s.Off), 5)
			if !ok || gw != int(s.W) || uint32(v) != s.Val {
				t.Errorf("slot %+v does not read back (%d, %d, %v)", s, v, gw, ok)
			}
		}
		// every index space is exercised
		want := map[Space]int{SpType: 8, SpFunc: 10, SpTable: 11, SpMem: 7, SpGlobal: 4, SpTag: 2, SpElem: 2, SpData: 2}
		for sp, n := range want {
			if per[sp] != n {
				t.Errorf("width %d: %s slots %d, want %d", w, SpaceName[sp], per[sp], n)
			}
		}
		if w == 5 && per[SpFunc] > 0 {
			for _, s := range m.Slots {
				if s.Sp == SpFunc && s.Sec == SecCode && s.W != 5 {
					t.Errorf("padded call index recorded with width %d", s.W)
				}
			}
		}
		if m.Opaque != 0 || len(m.Bodies) != 3 || len(m.Funcs) != 3 || m.NImport != [5]int{1, 1, 1, 1, 1} {
			t.Errorf("width %d: opaque %d bodies %d funcs %d imports %v", w, m.Opaque, len(m.Bodies), len(m.Funcs), m.NImport)
		}
	}
}

// Instruction forms: each body must walk exactly (ok) or be opaque, and report the slots listed.
func TestInstructions(t *testing.T) {
	w := func(code ...[]byte) []byte {
		d := cat(header, sec(SecType, vec(ft("", ""))), sec(SecFunction, vec(uleb(0))), sec(SecMemory, vec([]byte{0, 1})), sec(SecCode, vec(body(code...))))
		return d
	}
	cases := []struct {
		name  string
		code  []byte
		slots int
		ok    bool
	}{
		{"nothing", nil, 0, true},
		{"i32.const raw", []byte{0x41, 0xff, 0xff, 0xff, 0xff, 0x7f, 0x1a}, 0, true},
		{"i32.const most negative", []byte{0x41, 0x80, 0x80, 0x80, 0x80, 0x78, 0x1a}, 0, true},
		{"i64.const 10 bytes", []byte{0x42, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x7f, 0x1a}, 0, true},
		{"f32/f64 const", []byte{0x43, 0, 0, 0, 0, 0x1a, 0x44, 0, 0, 0, 0, 0, 0, 0, 0, 0x1a}, 0, true},
		{"loads, stores, memory.size", []byte{0x28, 2, 0, 0x1a, 0x36, 2, 8, 0x3f, 0, 0x1a}, 1, true},
		{"br_table", []byte{0x41, 0, 0x0e, 2, 0, 0, 0}, 0, true},
		{"select with types", []byte{0x41, 0, 0x41, 0, 0x41, 0, 0x1c, 1, 0x7f, 0x1a}, 0, true},
		{"block types: empty, value, index", []byte{0x02, 0x40, 0x0b, 0x02, 0x7f, 0x0b, 0x1a, 0x02, 0x00, 0x0b, 0x02, 0xc0, 0x00, 0x0b}, 2, true},
		{"if/else", []byte{0x41, 0, 0x04, 0x40, 0x05, 0x0b}, 0, true},
		{"legacy try/catch/delegate", []byte{0x06, 0x40, 0x07, 0x00, 0x19, 0x0b, 0x06, 0x40, 0x18, 0x00}, 1, true},
		{"try_table", []byte{0x1f, 0x40, 0x02, 0x00, 0x00, 0x00, 0x02, 0x00, 0x0b}, 1, true},
		{"throw, rethrow", []byte{0x08, 0x00, 0x09, 0x00}, 1, true},
		{"call, return_call", []byte{0x10, 0, 0x12, 0}, 2, true},
		{"call_indirect", []byte{0x41, 0, 0x11, 0, 0}, 2, true},
		{"ref.null, ref.is_null, ref.func", []byte{0xd0, 0x70, 0xd1, 0x1a, 0xd2, 0, 0x1a}, 1, true},
		{"table.get/set", []byte{0x41, 0, 0x25, 0, 0x1a, 0x41, 0, 0xd0, 0x70, 0x26, 0}, 2, true},
		{"memory.copy / fill", []byte{0xfc, 10, 0, 0, 0xfc, 11, 0}, 3, true},
		{"memory.init / data.drop", []byte{0xfc, 8, 0, 0, 0xfc, 9, 0}, 3, true},
		{"table.init / elem.drop / table.copy", []byte{0xfc, 12, 0, 0, 0xfc, 13, 0, 0xfc, 14, 0, 0}, 5, true},
		{"table.grow/size/fill", []byte{0xfc, 15, 0, 0xfc, 16, 0, 0xfc, 17, 0}, 3, true},
		{"trunc_sat", []byte{0xfc, 0, 0xfc, 7}, 0, true},
		{"simd load/const/shuffle/lane", cat([]byte{0xfd, 0, 4, 0, 0xfd, 12}, make([]byte, 16), []byte{0xfd, 13}, make([]byte, 16), []byte{0xfd, 21, 1, 0xfd, 84, 0, 0, 3}), 0, true},
		{"simd arithmetic (two-byte sub-opcode)", []byte{0xfd, 0xae, 0x01, 0xfd, 0x80, 0x02}, 0, true},
		{"atomics", []byte{0xfe, 0x10, 2, 0, 0xfe, 3, 0, 0xfe, 0x4e, 3, 0}, 0, true},
		{"multi-memory memarg", []byte{0x28, 0x42, 1, 0}, 1, true},
		{"unknown opcode", []byte{0xc5}, 0, false},
		{"unknown fc sub-opcode", []byte{0xfc, 99}, 0, false},
		{"gc opcode", []byte{0xfb, 0}, 0, false},
		{"truncated immediate", []byte{0x41}, 0, false},
		{"bad value type in select", []byte{0x1c, 1, 0x63}, 0, false},
	}
	for _, c := range cases {
		d := w(c.code)
		_, m := analyze(t, d)
		got := 0
		for _, s := range m.Slots {
			if s.Sp != SpType && s.Sp != SpAddr && s.Sec == SecCode {
				got++
			}
			if s.Sp == SpType && s.Sec == SecCode {
				got++
			}
		}
		if (m.Opaque == 0) != c.ok {
			t.Errorf("%s: opaque=%d want ok=%v", c.name, m.Opaque, c.ok)
			continue
		}
		if c.ok && got != c.slots {
			t.Errorf("%s: %d code slots, want %d", c.name, got, c.slots)
		}
		if !c.ok && got != 0 {
			t.Errorf("%s: an opaque body left %d slots", c.name, got)
		}
	}
}

func TestAddressConstants(t *testing.T) {
	// a linker-relocated address is a padded 5-byte i32.const; a shortest-form constant is not
	d := cat(header, sec(SecType, vec(ft("", ""))), sec(SecFunction, vec(uleb(0))),
		sec(SecCode, vec(body([]byte{0x41, 0xe0, 0x88, 0x80, 0x80, 0x00, 0x1a, 0x41, 0x7f, 0x1a, 0x41, 0x80, 0x80, 0x80, 0x80, 0x78, 0x1a}))))
	_, m := analyze(t, d)
	n := 0
	for _, s := range m.Slots {
		if s.Sp == SpAddr {
			n++
			if s.W != 5 || s.Val != 0x460 {
				t.Errorf("address slot %+v", s)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d address slots, want 1", n)
	}
}

func TestUnsupportedModules(t *testing.T) {
	gc := cat(header, sec(SecType, vec([]byte{0x5f, 1, 0x7f, 0})))
	dir, err := Parse(bytes.NewReader(gc), int64(len(gc)), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Analyze(dir, gc, Limits{}); !isErr(err, ErrUnsupported) {
		t.Errorf("GC struct type: %v", err)
	}
	mem64 := cat(header, sec(SecMemory, vec([]byte{4, 1})))
	dir, _ = Parse(bytes.NewReader(mem64), int64(len(mem64)), Limits{})
	if _, err := Analyze(dir, mem64, Limits{}); !isErr(err, ErrUnsupported) {
		t.Errorf("memory64: %v", err)
	}
	bad := cat(header, sec(SecFunction, vec(uleb(0))))
	dir, _ = Parse(bytes.NewReader(bad), int64(len(bad)), Limits{})
	if _, err := Analyze(dir, bad, Limits{}); !isErr(err, ErrFormat) {
		t.Errorf("function without body: %v", err)
	}
}

func isErr(err, target error) bool {
	return err != nil && bytes.Contains([]byte(err.Error()), []byte(target.Error()))
}

func rtrip(t *testing.T, d []byte, cfg Config) *Rep {
	t.Helper()
	_, m := analyze(t, d)
	rep, err := Encode(m, d, cfg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	meta := append([]byte(nil), rep.Meta...)
	comps := make([][]byte, len(rep.Comps))
	for i, c := range rep.Comps {
		comps[i] = append([]byte(nil), c...)
	}
	got, err := Decode(meta, comps)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, d) {
		t.Fatalf("decoded module differs (%d vs %d bytes)", len(got), len(d))
	}
	return rep
}

func cutEvery(n int) func([]byte, []idxref.Sec) []int {
	return func(p []byte, _ []idxref.Sec) []int {
		var c []int
		for o := n; o < len(p); o += n {
			c = append(c, o)
		}
		return c
	}
}

func TestRoundTrip(t *testing.T) {
	for _, w := range []int{1, 2, 3, 5} { // exact LEB width: padded calls must come back padded
		for _, cfg := range []Config{{}, {K0: 1}, {K0: 4, Cut: cutEvery(40)}, {Auto: 12, Cut: cutEvery(25)}, {Auto: 100}, {UseNames: true}} {
			rtrip(t, full(w), cfg)
		}
	}
}

func TestExactWidthIsKept(t *testing.T) {
	// the same call target spelled in different widths decodes to the original spelling
	for _, w := range []int{1, 4, 5} {
		d := full(w)
		rep := rtrip(t, d, Config{})
		_, m := analyze(t, d)
		got, err := Decode(rep.Meta, rep.Comps)
		if err != nil {
			t.Fatal(err)
		}
		_, m2 := analyze(t, got)
		if len(m.Slots) != len(m2.Slots) {
			t.Fatalf("slot count %d vs %d", len(m.Slots), len(m2.Slots))
		}
		for i := range m.Slots {
			if m.Slots[i] != m2.Slots[i] {
				t.Errorf("slot %d: %+v vs %+v", i, m.Slots[i], m2.Slots[i])
			}
		}
	}
}

func TestWasmTamper(t *testing.T) {
	d := full(2)
	rep := rtrip(t, d, Config{Cut: cutEvery(60)})
	try := func(name string, meta []byte, comps [][]byte, mustDiffer bool) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s: panic %v", name, r)
			}
		}()
		got, err := Decode(meta, comps)
		if mustDiffer && err == nil && bytes.Equal(got, d) {
			t.Errorf("%s: decoded to the original", name)
		}
	}
	for ci, c := range rep.Comps {
		pl, ss, err := idxref.SplitComp(c, NumStreams)
		if err != nil {
			t.Fatal(err)
		}
		for si, s := range ss {
			for _, off := range []int{0, len(s) - 1} {
				if len(s) == 0 {
					continue
				}
				mut := make([][]byte, len(ss))
				for k := range ss {
					mut[k] = append([]byte(nil), ss[k]...)
				}
				mut[si][off] ^= 0x5a
				comps := append([][]byte(nil), rep.Comps...)
				comps[ci] = idxref.PackComp(pl, mut)
				try("flip reference", rep.Meta, comps, true)
			}
		}
		if len(pl) > 0 {
			mut := append([]byte(nil), pl...)
			mut[len(mut)/2] ^= 0xa5
			comps := append([][]byte(nil), rep.Comps...)
			comps[ci] = idxref.PackComp(mut, ss)
			try("flip payload", rep.Meta, comps, false)
		}
	}
	try("drop", rep.Meta, rep.Comps[1:], true)
	for n := 0; n < len(rep.Meta); n++ {
		try("truncated meta", rep.Meta[:n], rep.Comps, true)
	}
}

func TestOpaqueBodyKeepsBytes(t *testing.T) {
	// a body with an instruction the walker does not know is carried verbatim and still decodes
	d := cat(header, sec(SecType, vec(ft("", ""))), sec(SecFunction, vec(uleb(0), uleb(0))),
		sec(SecCode, vec(body([]byte{0xc5, 0x10, 0x00}), body([]byte{0x10, 0x00}))))
	rep := rtrip(t, d, Config{})
	if rep.Stats.Opaque != 1 {
		t.Errorf("%d opaque bodies, want 1", rep.Stats.Opaque)
	}
}

func TestUnsupportedFallsBack(t *testing.T) {
	gc := cat(header, sec(SecType, vec([]byte{0x5f, 1, 0x7f, 0})))
	dir, _ := Parse(bytes.NewReader(gc), int64(len(gc)), Limits{})
	if _, err := Analyze(dir, gc, Limits{}); err == nil {
		t.Error("GC module analysed")
	}
	comp := append([]byte("\x00asm\x0d\x00\x01\x00"), make([]byte, 16)...)
	if _, err := Parse(bytes.NewReader(comp), int64(len(comp)), Limits{}); err == nil {
		t.Error("component binary parsed")
	}
	_ = binary.LittleEndian
}

func TestDeterministic(t *testing.T) {
	d := full(2)
	_, m := analyze(t, d)
	var first *Rep
	for i := 0; i < 3; i++ {
		rep, err := Encode(m, d, Config{Auto: 12, Cut: cutEvery(40)})
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = rep
			continue
		}
		if !bytes.Equal(rep.Meta, first.Meta) || len(rep.Comps) != len(first.Comps) {
			t.Fatal("meta or component count differs between runs")
		}
		for k := range rep.Comps {
			if !bytes.Equal(rep.Comps[k], first.Comps[k]) {
				t.Fatalf("component %d differs between runs", k)
			}
		}
	}
}
