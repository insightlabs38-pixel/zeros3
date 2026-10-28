package clrz3

import (
	"encoding/binary"
)

// CIL method bodies (ECMA-335 II.25.4) and a finite opcode / operand table (III.1.2, III.2). Only
// metadata-token operands are reference slots; branch targets, switch tables, locals, arguments and
// numeric immediates are method-local layout and stay raw.

type opd int8

const (
	oBad opd = iota - 1
	oNone
	oI1
	oU1
	oU2
	oI4
	oI8
	oR4
	oR8
	oBr4 // 4-byte branch displacement (raw)
	oSwitch
	oMethod
	oField
	oType
	oSig
	oString
	oTok
)

var opd1 [256]opd
var opd2 [32]opd

func init() {
	for i := range opd1 {
		opd1[i] = oBad
	}
	for i := range opd2 {
		opd2[i] = oBad
	}
	set := func(o opd, ops ...int) {
		for _, op := range ops {
			opd1[op] = o
		}
	}
	rng := func(o opd, a, b int) {
		for i := a; i <= b; i++ {
			opd1[i] = o
		}
	}
	rng(oNone, 0x00, 0x0d) // nop, break, ldarg.0-3, ldloc.0-3, stloc.0-3
	rng(oU1, 0x0e, 0x13)   // ldarg.s .. stloc.s
	rng(oNone, 0x14, 0x1e) // ldnull, ldc.i4.m1 .. ldc.i4.8
	set(oI1, 0x1f)
	set(oI4, 0x20)
	set(oI8, 0x21)
	set(oR4, 0x22)
	set(oR8, 0x23)
	rng(oNone, 0x25, 0x26)
	set(oMethod, 0x27, 0x28)
	set(oSig, 0x29)
	set(oNone, 0x2a)
	rng(oI1, 0x2b, 0x37) // br.s .. blt.un.s
	rng(oBr4, 0x38, 0x44)
	set(oSwitch, 0x45)
	rng(oNone, 0x46, 0x6e)
	set(oMethod, 0x6f)
	set(oType, 0x70, 0x71)
	set(oString, 0x72)
	set(oMethod, 0x73)
	set(oType, 0x74, 0x75)
	set(oNone, 0x76)
	set(oType, 0x79)
	set(oNone, 0x7a)
	rng(oField, 0x7b, 0x80)
	set(oType, 0x81)
	rng(oNone, 0x82, 0x8b)
	set(oType, 0x8c, 0x8d)
	set(oNone, 0x8e)
	set(oType, 0x8f)
	rng(oNone, 0x90, 0xa2)
	set(oType, 0xa3, 0xa4, 0xa5)
	rng(oNone, 0xb3, 0xba)
	set(oType, 0xc2)
	set(oNone, 0xc3)
	set(oType, 0xc6)
	set(oTok, 0xd0)
	rng(oNone, 0xd1, 0xdc)
	set(oBr4, 0xdd)
	set(oI1, 0xde)
	rng(oNone, 0xdf, 0xe0)
	// 0xFE prefix
	for _, op := range []int{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x0f, 0x11, 0x13, 0x14, 0x17, 0x18, 0x1a, 0x1d, 0x1e} {
		opd2[op] = oNone
	}
	opd2[0x06], opd2[0x07] = oMethod, oMethod
	for _, op := range []int{0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e} {
		opd2[op] = oU2
	}
	opd2[0x12], opd2[0x19] = oU1, oU1
	opd2[0x15], opd2[0x16], opd2[0x1c] = oType, oType, oType
}

// Token operand classes.
const (
	TkMethod = iota + 1
	TkField
	TkType
	TkSig
	TkString
	TkAny
)

// TokSlot is one token operand of a method body.
type TokSlot struct {
	Off   int64 // file offset of the 4-byte operand
	Class uint8
	Val   uint32
}

// Body is a parsed method body.
type Body struct {
	Off, End int64 // whole body including header and extra sections
	Fat      bool
	HdrLen   int
	CodeOff  int64
	CodeLen  int
	MaxStack uint16
	LocalOff int64 // file offset of the LocalVarSigTok operand (fat only), 0 if none
	LocalTok uint32
	Clauses  []Clause
	Toks     []TokSlot // metadata tokens of the code
	SecOff   int64
	SecEnd   int64
}

// Clause is one exception-handling clause.
type Clause struct {
	Flags      uint32
	TokOff     int64 // offset of the class token (typed catch) or 0
	TokVal     uint32
	Off        int64
	Fat        bool
	TryOff     uint32
	TryLen     uint32
	HandlerOff uint32
	HandlerLen uint32
}

const (
	maxCode    = 1 << 26
	maxClauses = 1 << 16
)

// ParseBody parses the method body at file offset off. A body that does not parse exactly makes
// ok false; the caller keeps it opaque. strict also checks the shape of every token operand (a
// masked file is parsed non-strictly: its token values are zero).
func ParseBody(d []byte, off int64, strict bool) (b Body, ok bool) {
	n := int64(len(d))
	if off < 0 || off >= n {
		return b, false
	}
	h := d[off]
	b.Off = off
	switch h & 3 {
	case 2:
		b.HdrLen = 1
		b.CodeLen = int(h >> 2)
	case 3:
		if off+12 > n {
			return b, false
		}
		fl := binary.LittleEndian.Uint16(d[off:])
		if fl>>12 != 3 {
			return b, false
		}
		b.Fat = true
		b.HdrLen = 12
		b.MaxStack = binary.LittleEndian.Uint16(d[off+2:])
		cl := binary.LittleEndian.Uint32(d[off+4:])
		if cl > maxCode {
			return b, false
		}
		b.CodeLen = int(cl)
		b.LocalOff = off + 8
		b.LocalTok = binary.LittleEndian.Uint32(d[off+8:])
		if strict && b.LocalTok != 0 && b.LocalTok>>24 != 0x11 {
			return b, false
		}
		if fl&0x08 != 0 {
			b.SecOff = -1 // present; located below
		}
	default:
		return b, false
	}
	b.CodeOff = off + int64(b.HdrLen)
	if b.CodeOff+int64(b.CodeLen) > n {
		return b, false
	}
	b.End = b.CodeOff + int64(b.CodeLen)
	if !b.walkCode(d, strict) {
		return b, false
	}
	if b.Fat && b.SecOff == -1 {
		p := b.Off + ((b.End - b.Off + 3) &^ 3) // sections are 4-aligned from the start of the body
		b.SecOff = p
		for {
			if p+4 > n {
				return b, false
			}
			kind := d[p]
			var size int64
			if kind&0x40 != 0 {
				size = int64(d[p+1]) | int64(d[p+2])<<8 | int64(d[p+3])<<16
			} else {
				size = int64(d[p+1])
			}
			if size < 4 || p+size > n {
				return b, false
			}
			switch kind & 0x3f {
			case 1: // EH table
				esz := int64(12)
				if kind&0x40 != 0 {
					esz = 24
				}
				if (size-4)%esz != 0 || (size-4)/esz > maxClauses {
					return b, false
				}
				for q := p + 4; q < p+size; q += esz {
					c := Clause{Off: q, Fat: kind&0x40 != 0}
					if c.Fat {
						c.Flags = binary.LittleEndian.Uint32(d[q:])
						c.TryOff = binary.LittleEndian.Uint32(d[q+4:])
						c.TryLen = binary.LittleEndian.Uint32(d[q+8:])
						c.HandlerOff = binary.LittleEndian.Uint32(d[q+12:])
						c.HandlerLen = binary.LittleEndian.Uint32(d[q+16:])
						if c.Flags == 0 {
							c.TokOff, c.TokVal = q+20, binary.LittleEndian.Uint32(d[q+20:])
						}
					} else {
						c.Flags = uint32(binary.LittleEndian.Uint16(d[q:]))
						c.TryOff = uint32(binary.LittleEndian.Uint16(d[q+2:]))
						c.TryLen = uint32(d[q+4])
						c.HandlerOff = uint32(binary.LittleEndian.Uint16(d[q+5:]))
						c.HandlerLen = uint32(d[q+7])
						if c.Flags == 0 {
							c.TokOff, c.TokVal = q+8, binary.LittleEndian.Uint32(d[q+8:])
						}
					}
					if strict && c.Flags == 0 && !validTok(c.TokVal, TkType) {
						return b, false
					}
					b.Clauses = append(b.Clauses, c)
				}
			case 2: // optional IL table (obsolete)
			default:
				return b, false
			}
			p += size
			b.End = p
			if kind&0x80 == 0 {
				break
			}
			p = b.Off + ((p - b.Off + 3) &^ 3)
		}
		b.SecEnd = b.End
	}
	return b, true
}

// validTok reports whether v is a plausible token of the operand class (table in the class set,
// non-zero row, string offsets are checked by the caller against the heap).
func validTok(v uint32, class uint8) bool {
	t, r := v>>24, v&0xffffff
	switch class {
	case TkString:
		return t == 0x70
	case TkSig:
		return t == 0x11 && r != 0
	case TkMethod:
		return (t == TMethodDef || t == TMemberRef || t == TMethodSpec) && r != 0
	case TkField:
		return (t == TField || t == TMemberRef) && r != 0
	case TkType:
		return (t == TTypeDef || t == TTypeRef || t == TTypeSpec) && r != 0
	case TkAny:
		return (t == TTypeDef || t == TTypeRef || t == TTypeSpec || t == TField || t == TMethodDef || t == TMemberRef || t == TMethodSpec) && r != 0
	}
	return false
}

func (b *Body) walkCode(d []byte, strict bool) bool {
	p, end := b.CodeOff, b.CodeOff+int64(b.CodeLen)
	for p < end {
		op := d[p]
		p++
		var o opd
		if op == 0xfe {
			if p >= end {
				return false
			}
			op2 := d[p]
			p++
			if int(op2) >= len(opd2) {
				return false
			}
			o = opd2[op2]
		} else {
			o = opd1[op]
		}
		var sz int64
		cls := uint8(0)
		switch o {
		case oBad:
			return false
		case oNone:
		case oI1, oU1:
			sz = 1
		case oU2:
			sz = 2
		case oI4, oR4, oBr4:
			sz = 4
		case oI8, oR8:
			sz = 8
		case oSwitch:
			if p+4 > end {
				return false
			}
			n := int64(binary.LittleEndian.Uint32(d[p:]))
			if n > (end-p-4)/4 {
				return false
			}
			sz = 4 + 4*n
		case oMethod:
			sz, cls = 4, TkMethod
		case oField:
			sz, cls = 4, TkField
		case oType:
			sz, cls = 4, TkType
		case oSig:
			sz, cls = 4, TkSig
		case oString:
			sz, cls = 4, TkString
		case oTok:
			sz, cls = 4, TkAny
		}
		if p+sz > end {
			return false
		}
		if cls != 0 {
			v := binary.LittleEndian.Uint32(d[p:])
			if strict && !validTok(v, cls) {
				return false
			}
			b.Toks = append(b.Toks, TokSlot{p, cls, v})
		}
		p += sz
	}
	return true
}
