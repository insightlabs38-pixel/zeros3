package clrz3

import (
	"encoding/binary"
	"testing"
)

func tiny(code ...byte) []byte { return append([]byte{byte(len(code))<<2 | 2}, code...) }

func fat(maxStack uint16, locals uint32, code []byte, extra ...byte) []byte {
	fl := uint16(0x3003 | 0x10)
	if len(extra) > 0 {
		fl |= 0x08
	}
	b := binary.LittleEndian.AppendUint16(nil, fl)
	b = binary.LittleEndian.AppendUint16(b, maxStack)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(code)))
	b = binary.LittleEndian.AppendUint32(b, locals)
	b = append(b, code...)
	if len(extra) > 0 {
		for len(b)%4 != 0 {
			b = append(b, 0)
		}
		b = append(b, extra...)
	}
	return b
}

func le4(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func TestParseBody(t *testing.T) {
	str := le4(0x70000010)
	meth := le4(0x06000005)
	member := le4(0x0a000009)
	typ := le4(0x01000003)
	cases := []struct {
		name    string
		body    []byte
		ok      bool // strict
		okLoose bool // non-strict (masked file)
		toks    int
		clauses int
		fat     bool
	}{
		{"tiny ret", tiny(0x2a), true, true, 0, 0, false},
		{"ldstr call ret", tiny(cat([]byte{0x72}, str, []byte{0x28}, meth, []byte{0x2a})...), true, true, 2, 0, false},
		{"callvirt MemberRef, newobj, ldfld", tiny(cat([]byte{0x6f}, member, []byte{0x73}, member, []byte{0x7b}, le4(0x04000002), []byte{0x2a})...), true, true, 3, 0, false},
		{"ldtoken typeref / castclass typespec", tiny(cat([]byte{0xd0}, typ, []byte{0x74}, le4(0x1b000001), []byte{0x2a})...), true, true, 2, 0, false},
		{"prefix opcodes ldftn initobj constrained", tiny(cat([]byte{0xfe, 0x06}, meth, []byte{0xfe, 0x15}, typ, []byte{0xfe, 0x16}, typ, []byte{0x2a})...), true, true, 3, 0, false},
		{"switch with 3 targets", tiny(cat([]byte{0x45}, le4(3), le4(0), le4(1), le4(2), []byte{0x2a})...), true, true, 0, 0, false},
		{"branches and locals stay raw", tiny(0x16, 0x2c, 0x01, 0x14, 0x2a, 0xfe, 0x0c, 0x01, 0x00, 0x2a), true, true, 0, 0, false},
		{"unknown opcode", tiny(0xe1, 0x2a), false, false, 0, 0, false},
		{"unknown extended opcode", tiny(0xfe, 0x30, 0x2a), false, false, 0, 0, false},
		{"truncated operand", tiny(0x28, 0x01, 0x00), false, false, 0, 0, false},
		{"switch past the end", tiny(cat([]byte{0x45}, le4(1000), []byte{0x2a})...), false, false, 0, 0, false},
		{"call to a Field token", tiny(cat([]byte{0x28}, le4(0x04000001), []byte{0x2a})...), false, true, 1, 0, false},
		{"masked tokens (zero)", tiny(cat([]byte{0x28}, le4(0), []byte{0x72}, le4(0), []byte{0x2a})...), false, true, 2, 0, false},
		{"fat with locals token", fat(8, 0x11000007, []byte{0x2a}), true, true, 0, 0, true},
		{"fat with a bad locals token", fat(8, 0x06000007, []byte{0x2a}), false, true, 0, 0, true},
		{"fat with a small catch clause", fat(8, 0, []byte{0x00, 0x2a}, cat([]byte{0x01, 0x10, 0, 0}, binary.LittleEndian.AppendUint16(nil, 0), binary.LittleEndian.AppendUint16(nil, 0), []byte{1}, binary.LittleEndian.AppendUint16(nil, 1), []byte{1}, typ)...), true, true, 0, 1, true},
		{"fat with a finally clause (no token)", fat(8, 0, []byte{0x00, 0x2a}, cat([]byte{0x01, 0x10, 0, 0}, binary.LittleEndian.AppendUint16(nil, 2), binary.LittleEndian.AppendUint16(nil, 0), []byte{1}, binary.LittleEndian.AppendUint16(nil, 1), []byte{1}, le4(0))...), true, true, 0, 1, true},
		{"fat with a fat catch clause", fat(8, 0, []byte{0x00, 0x2a}, cat([]byte{0x41, 0x1c, 0, 0}, le4(0), le4(0), le4(1), le4(1), le4(1), typ)...), true, true, 0, 1, true},
		{"bad header", []byte{0x01, 0x2a}, false, false, 0, 0, false},
		{"fat header too short", []byte{0x13, 0x30, 0x00}, false, false, 0, 0, false},
		{"EH section size 3", fat(8, 0, []byte{0x2a}, 0x01, 0x03, 0, 0), false, false, 0, 0, true},
	}
	for _, c := range cases {
		for _, strict := range []bool{true, false} {
			d := append([]byte{0, 0, 0}, c.body...)
			b, ok := ParseBody(d, 3, strict)
			want := c.ok
			if !strict {
				want = c.okLoose
			}
			if ok != want {
				t.Errorf("%s (strict=%v): ok=%v want %v", c.name, strict, ok, want)
				continue
			}
			if !ok || !strict {
				continue
			}
			if len(b.Toks) != c.toks || len(b.Clauses) != c.clauses || b.Fat != c.fat {
				t.Errorf("%s: %d tokens %d clauses fat=%v; want %d %d %v", c.name, len(b.Toks), len(b.Clauses), b.Fat, c.toks, c.clauses, c.fat)
			}
			if b.End > int64(len(d)) || b.Off != 3 {
				t.Errorf("%s: body span [%d,%d)", c.name, b.Off, b.End)
			}
		}
	}
}

func TestOpcodeTable(t *testing.T) {
	// the standard one-byte opcodes are 0x00-0x13, 0x14-0x23, 0x25-0x6F, 0x70-0x76, 0x79-0x8F,
	// 0x90-0xA5, 0xB3-0xBA, 0xC2, 0xC3, 0xC6, 0xD0-0xE0 (ECMA-335 III.1.2); everything else is reserved
	reserved := func(op int) bool {
		switch {
		case op == 0x24, op == 0x77, op == 0x78:
			return true
		case op >= 0xa6 && op <= 0xb2, op >= 0xbb && op <= 0xc1, op == 0xc4, op == 0xc5, op >= 0xc7 && op <= 0xcf, op >= 0xe1:
			return true
		}
		return false
	}
	for op := 0; op < 256; op++ {
		if (opd1[op] == oBad) != reserved(op) {
			t.Errorf("one-byte opcode %#x: defined=%v reserved=%v", op, opd1[op] != oBad, reserved(op))
		}
	}
	for _, op := range []int{0x08, 0x10, 0x1b, 0x1f} {
		if opd2[op] != oBad {
			t.Errorf("extended opcode %#x should be reserved", op)
		}
	}
	if opd2[0x06] != oMethod || opd2[0x15] != oType || opd2[0x1c] != oType || opd2[0x09] != oU2 || opd2[0x12] != oU1 {
		t.Error("extended opcode table")
	}
	if opd1[0x72] != oString || opd1[0x7b] != oField || opd1[0x28] != oMethod || opd1[0x29] != oSig || opd1[0xd0] != oTok || opd1[0x45] != oSwitch {
		t.Error("one-byte opcode table")
	}
}
