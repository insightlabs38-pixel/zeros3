package clrz3

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// A test-only assembly writer (SYNTHETIC_FIXTURE): a PE32 file with one .text section holding the
// CLI header, method bodies and a compressed (#~) metadata image. It writes the tables from
// per-table row lists using its own width rules (independent of the parser's), so the width
// transitions of the parser are checked against a second implementation.

type col = uint32

type asm struct {
	rows    [NumTables][][]col // column values as stored (before width)
	str     []byte             // #Strings (starts with the empty string)
	us      []byte
	blob    []byte
	strAt   map[string]uint32
	blobAt  map[string]uint32
	usAt    map[string]uint32
	bodies  [][]byte // IL bodies in MethodDef order (nil: no body)
	padStr  int      // extra unique #Strings bytes
	padBlob int
	fat     bool // fat headers for every body
}

func newAsm() *asm {
	return &asm{str: []byte{0}, us: []byte{0}, blob: []byte{0}, strAt: map[string]uint32{"": 0}, blobAt: map[string]uint32{"": 0}, usAt: map[string]uint32{}}
}

func (a *asm) s(x string) uint32 {
	if o, ok := a.strAt[x]; ok {
		return o
	}
	o := uint32(len(a.str))
	a.str = append(append(a.str, x...), 0)
	a.strAt[x] = o
	return o
}

func comp(n int) []byte {
	switch {
	case n < 0x80:
		return []byte{byte(n)}
	case n < 0x4000:
		return []byte{0x80 | byte(n>>8), byte(n)}
	}
	return []byte{0xC0 | byte(n>>24), byte(n >> 16), byte(n >> 8), byte(n)}
}

func (a *asm) b(x []byte) uint32 {
	if o, ok := a.blobAt[string(x)]; ok {
		return o
	}
	o := uint32(len(a.blob))
	a.blob = append(append(a.blob, comp(len(x))...), x...)
	a.blobAt[string(x)] = o
	return o
}

func (a *asm) u(x string) uint32 {
	if o, ok := a.usAt[x]; ok {
		return o
	}
	o := uint32(len(a.us))
	var enc []byte
	for _, r := range x {
		enc = append(enc, byte(r), byte(r>>8))
	}
	enc = append(enc, 0)
	a.us = append(append(a.us, comp(len(enc))...), enc...)
	a.usAt[x] = o
	return o
}

func (a *asm) add(t int, v ...col) uint32 {
	a.rows[t] = append(a.rows[t], v)
	return uint32(len(a.rows[t]))
}

// cdx builds a coded-index value.
func cdx(fam, table int, row uint32) col {
	v, ok := EncodeCoded(fam, table, row)
	if !ok {
		panic("coded")
	}
	return v
}

func tok(t int, row uint32) uint32 { return uint32(t)<<24 | row }

// widths as the writer computes them (independent of File.parseTables).
func (a *asm) colWidth(c ColDef) int {
	big := func(rows int) int {
		if rows >= 1<<16 {
			return 4
		}
		return 2
	}
	switch c.K {
	case KConst, KRVA:
		return c.W
	case KStr:
		if len(a.str) > 1<<16 {
			return 4
		}
		return 2
	case KGuid:
		return 2
	case KBlob:
		if len(a.blob) > 1<<16 {
			return 4
		}
		return 2
	case KTable, KList:
		return big(len(a.rows[c.T]))
	case KCoded:
		lim := 1 << uint(16-coded[c.T].bits)
		for _, t := range coded[c.T].tabs {
			if t >= 0 && len(a.rows[t]) >= lim {
				return 4
			}
		}
		return 2
	}
	return 0
}

func le(b []byte, w int, v uint32) []byte {
	if w == 2 {
		return binary.LittleEndian.AppendUint16(b, uint16(v))
	}
	return binary.LittleEndian.AppendUint32(b, v)
}

func align(b []byte, n int) []byte {
	for len(b)%n != 0 {
		b = append(b, 0)
	}
	return b
}

// image writes the file. Method RVAs are filled in from the bodies.
func (a *asm) image() []byte {
	const textRVA, textOff = 0x2000, 0x200
	// padding to reach heap sizes
	for i := 0; i < a.padStr; i++ {
		a.s(fmt.Sprintf("pad-string-%07d", i))
	}
	for i := 0; i < a.padBlob; i++ {
		a.b(append([]byte{0xAA, byte(i), byte(i >> 8)}, bytes.Repeat([]byte{byte(i)}, 12)...))
	}
	// text: CLI header (72) | bodies | metadata
	text := make([]byte, 72)
	var bodyRVA []uint32
	for _, bd := range a.bodies {
		if bd == nil {
			bodyRVA = append(bodyRVA, 0)
			continue
		}
		fat := a.fat || len(bd) > 63
		var hdr []byte
		if fat {
			text = align(text, 4)
			hdr = binary.LittleEndian.AppendUint16(hdr, 0x3003|0x10) // fat, init locals, 3 dwords
			hdr = binary.LittleEndian.AppendUint16(hdr, 8)
			hdr = binary.LittleEndian.AppendUint32(hdr, uint32(len(bd)))
			hdr = binary.LittleEndian.AppendUint32(hdr, 0)
		} else {
			hdr = []byte{byte(len(bd))<<2 | 2}
		}
		bodyRVA = append(bodyRVA, textRVA+uint32(len(text)))
		text = append(append(text, hdr...), bd...)
	}
	for i, r := range bodyRVA {
		a.rows[TMethodDef][i][0] = r
	}
	text = align(text, 4)
	metaOff := len(text)
	var t []byte
	// #~
	var valid uint64
	var rowc []byte
	for i := 0; i < NumTables; i++ {
		if len(a.rows[i]) > 0 {
			valid |= 1 << uint(i)
			rowc = binary.LittleEndian.AppendUint32(rowc, uint32(len(a.rows[i])))
		}
	}
	hs := byte(0)
	if len(a.str) > 1<<16 {
		hs |= 1
	}
	if len(a.blob) > 1<<16 {
		hs |= 4
	}
	t = append(t, 0, 0, 0, 0, 2, 0, hs, 1)
	t = binary.LittleEndian.AppendUint64(t, valid)
	t = binary.LittleEndian.AppendUint64(t, 0)
	t = append(t, rowc...)
	for i := 0; i < NumTables; i++ {
		for _, row := range a.rows[i] {
			for ci, c := range schema[i] {
				t = le(t, a.colWidth(c), row[ci])
			}
		}
	}
	t = align(t, 4)
	guid := bytes.Repeat([]byte{0x11}, 16)
	streams := []struct {
		name string
		data []byte
	}{{"#~", t}, {"#Strings", align(append([]byte(nil), a.str...), 4)}, {"#US", align(append([]byte(nil), a.us...), 4)}, {"#GUID", guid}, {"#Blob", align(append([]byte(nil), a.blob...), 4)}}
	ver := []byte("v4.0.30319\x00\x00")
	root := []byte("BSJB")
	root = binary.LittleEndian.AppendUint16(root, 1)
	root = binary.LittleEndian.AppendUint16(root, 1)
	root = binary.LittleEndian.AppendUint32(root, 0)
	root = binary.LittleEndian.AppendUint32(root, uint32(len(ver)))
	root = append(root, ver...)
	root = binary.LittleEndian.AppendUint16(root, 0)
	root = binary.LittleEndian.AppendUint16(root, uint16(len(streams)))
	hdrLen := len(root)
	for _, s := range streams {
		hdrLen += 8 + (len(s.name)+1+3)&^3
	}
	off := hdrLen
	var body []byte
	for _, s := range streams {
		root = binary.LittleEndian.AppendUint32(root, uint32(off))
		root = binary.LittleEndian.AppendUint32(root, uint32(len(s.data)))
		root = append(root, s.name...)
		root = align(append(root, 0), 4)
		off += len(s.data)
		body = append(body, s.data...)
	}
	meta := append(root, body...)
	text = append(text, meta...)
	// CLI header
	binary.LittleEndian.PutUint32(text[0:], 72)
	binary.LittleEndian.PutUint16(text[4:], 2)
	binary.LittleEndian.PutUint16(text[6:], 5)
	binary.LittleEndian.PutUint32(text[8:], textRVA+uint32(metaOff))
	binary.LittleEndian.PutUint32(text[12:], uint32(len(meta)))
	binary.LittleEndian.PutUint32(text[16:], 1)
	// PE32 headers
	f := make([]byte, textOff)
	copy(f, "MZ")
	binary.LittleEndian.PutUint32(f[0x3c:], 0x80)
	copy(f[0x80:], "PE\x00\x00")
	c := f[0x84:]
	binary.LittleEndian.PutUint16(c[0:], 0x14c)
	binary.LittleEndian.PutUint16(c[2:], 1)
	binary.LittleEndian.PutUint16(c[16:], 224)
	binary.LittleEndian.PutUint16(c[18:], 0x2022)
	o := f[0x84+20:]
	binary.LittleEndian.PutUint16(o[0:], 0x10b)
	binary.LittleEndian.PutUint32(o[60:], textOff)
	binary.LittleEndian.PutUint32(o[92:], 16)
	binary.LittleEndian.PutUint32(o[96+14*8:], textRVA)
	binary.LittleEndian.PutUint32(o[96+14*8+4:], 72)
	sec := f[0x84+20+224:]
	copy(sec, ".text")
	rawSize := (len(text) + 0x1ff) &^ 0x1ff
	binary.LittleEndian.PutUint32(sec[8:], uint32(len(text)))
	binary.LittleEndian.PutUint32(sec[12:], textRVA)
	binary.LittleEndian.PutUint32(sec[16:], uint32(rawSize))
	binary.LittleEndian.PutUint32(sec[20:], textOff)
	binary.LittleEndian.PutUint32(sec[36:], 0x60000020)
	out := append(f, text...)
	for len(out) < textOff+rawSize {
		out = append(out, 0)
	}
	return out
}

// il assembles a method body from (opcode bytes | token) pieces.
func il(parts ...any) []byte {
	var b []byte
	for _, p := range parts {
		switch v := p.(type) {
		case byte:
			b = append(b, v)
		case []byte:
			b = append(b, v...)
		case uint32:
			b = binary.LittleEndian.AppendUint32(b, v)
		case int:
			b = append(b, byte(v))
		}
	}
	return b
}

// spec describes a synthetic library.
type spec struct {
	types, methods  int // methods per type
	attrs           int // CustomAttribute rows
	padStr, padBlob int
	fat             bool
	fields          int // fields per type
}

// build writes a library of spec.types classes, each with spec.methods methods that call the next
// class's first method, load a user string and return; plus a System.Object TypeRef with a .ctor
// MemberRef, parameters, fields and attributes.
func build(sp spec) []byte {
	a := newAsm()
	a.padStr, a.padBlob, a.fat = sp.padStr, sp.padBlob, sp.fat
	a.add(TModule, 0, a.s("synthetic.dll"), 1, 0, 0)
	asmRef := a.add(TAssemblyRef, 4, 0, 0, 0, 0, a.b([]byte{0xb7, 0x7a, 0x5c, 0x56, 0x19, 0x34, 0xe0, 0x89}), a.s("mscorlib"), 0, 0)
	objRef := a.add(TTypeRef, cdx(CResolutionScope, TAssemblyRef, asmRef), a.s("Object"), a.s("System"))
	voidSig := a.b([]byte{0x20, 0x00, 0x01})
	intSig := a.b([]byte{0x20, 0x01, 0x08, 0x08})
	ctorSig := a.b([]byte{0x20, 0x00, 0x01})
	ctor := a.add(TMemberRef, cdx(CMemberRefParent, TTypeRef, objRef), a.s(".ctor"), ctorSig)
	a.add(TTypeDef, 0, a.s("<Module>"), 0, 0, 1, 1)
	nextMethod := uint32(1)
	nextField := uint32(1)
	nextParam := uint32(1)
	for i := 0; i < sp.types; i++ {
		a.add(TTypeDef, 0x100001, a.s(fmt.Sprintf("C%05d", i)), a.s("Syn"), cdx(CTypeDefOrRef, TTypeRef, objRef), nextField, nextMethod)
		for k := 0; k < sp.fields; k++ {
			a.add(TField, 6, a.s(fmt.Sprintf("f%d", k)), a.b([]byte{0x06, 0x08}))
			nextField++
		}
		for k := 0; k < sp.methods; k++ {
			name := fmt.Sprintf("M%d", k)
			sig := voidSig
			params := 0
			if k%2 == 1 {
				sig, params = intSig, 1
			}
			a.add(TMethodDef, 0, 0, 0x86, a.s(name), sig, nextParam)
			for q := 0; q < params; q++ {
				a.add(TParam, 0, uint32(q+1), a.s("x"))
				nextParam++
			}
			nextMethod++
			target := tok(TMethodDef, uint32(1+((i+1)%max(sp.types, 1))*sp.methods))
			body := il(byte(0x72), uint32(0x70000000)|a.u(fmt.Sprintf("s%d.%d", i, k)), byte(0x26), byte(0x28), target, byte(0x28), tok(TMemberRef, ctor), byte(0x2a))
			if sp.methods == 0 {
				body = nil
			}
			a.bodies = append(a.bodies, body)
		}
	}
	for i := 0; i < sp.attrs && len(a.rows[TMethodDef]) > 0; i++ {
		a.add(TCustomAttribute, cdx(CHasCustomAttribute, TMethodDef, uint32(1+i%len(a.rows[TMethodDef]))), cdx(CCustomAttributeType, TMemberRef, ctor), a.b([]byte{1, 0, 0, 0}))
	}
	a.add(TAssembly, 0x8004, 1, 0, 0, 0, 0, 0, a.s("synthetic"), 0)
	return a.image()
}
