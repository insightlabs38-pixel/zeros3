package clrz3

// Bounded signature-blob skeleton parser (ECMA-335 II.23.2). It does not build a type tree: it
// walks the blob and reports where each embedded TypeDefOrRefEncoded value sits (offset, width of
// the compressed integer, value). Everything else in a signature is local structure and stays
// raw. A blob that does not parse exactly (including any trailing byte) is opaque: nothing in it
// is normalized.

// Blob kinds as recorded in the layout stream (0 = opaque).
const (
	BlobOpaque byte = iota
	BlobMethod
	BlobField
	BlobLocal
	BlobProp
	BlobType
	BlobInst
	numBlobKinds
)

// SigSlot is an embedded type reference inside a signature blob.
type SigSlot struct {
	Off   int    // byte offset in the blob content
	Width int    // 1, 2 or 4 bytes of compressed integer
	Val   uint32 // TypeDefOrRef coded value (row<<2 | tag)
}

const (
	maxSigDepth = 48
	maxSigSlots = 1 << 16
)

type sigp struct {
	strict bool // check the values of embedded references (a masked blob has zeros there)
	b      []byte
	p      int
	slots  []SigSlot
	depth  int
	bad    bool
}

func (s *sigp) fail() { s.bad = true }

func (s *sigp) byte() byte {
	if s.bad || s.p >= len(s.b) {
		s.bad = true
		return 0
	}
	c := s.b[s.p]
	s.p++
	return c
}

func (s *sigp) peek() byte {
	if s.bad || s.p >= len(s.b) {
		return 0xff
	}
	return s.b[s.p]
}

// cu reads a compressed unsigned integer and returns its value and encoded width.
func (s *sigp) cu() (uint32, int) {
	if s.bad || s.p >= len(s.b) {
		s.bad = true
		return 0, 0
	}
	c := s.b[s.p]
	switch {
	case c&0x80 == 0:
		s.p++
		return uint32(c), 1
	case c&0xC0 == 0x80:
		if s.p+2 > len(s.b) {
			s.bad = true
			return 0, 0
		}
		v := uint32(c&0x3f)<<8 | uint32(s.b[s.p+1])
		s.p += 2
		return v, 2
	case c&0xE0 == 0xC0:
		if s.p+4 > len(s.b) {
			s.bad = true
			return 0, 0
		}
		v := uint32(c&0x1f)<<24 | uint32(s.b[s.p+1])<<16 | uint32(s.b[s.p+2])<<8 | uint32(s.b[s.p+3])
		s.p += 4
		return v, 4
	}
	s.bad = true
	return 0, 0
}

// count reads a compressed count that must be plausible for the bytes that remain.
func (s *sigp) count() int {
	v, _ := s.cu()
	if s.bad || uint64(v) > uint64(len(s.b)-s.p) {
		s.bad = true
		return 0
	}
	return int(v)
}

func (s *sigp) typeRef() {
	at := s.p
	v, w := s.cu()
	if s.bad {
		return
	}
	if s.strict && (v&3 == 3 || v>>2 == 0) {
		s.bad = true
		return
	}
	if len(s.slots) >= maxSigSlots {
		s.bad = true
		return
	}
	s.slots = append(s.slots, SigSlot{at, w, v})
}

func (s *sigp) customMods() {
	for !s.bad {
		c := s.peek()
		if c != 0x1f && c != 0x20 {
			return
		}
		s.p++
		s.typeRef()
	}
}

func (s *sigp) typ() {
	if s.bad {
		return
	}
	if s.depth++; s.depth > maxSigDepth {
		s.bad = true
		return
	}
	defer func() { s.depth-- }()
	e := s.byte()
	switch {
	case e >= 0x02 && e <= 0x0e, e == 0x16, e == 0x18, e == 0x19, e == 0x1c:
	case e == 0x0f: // PTR
		s.customMods()
		if s.peek() == 0x01 {
			s.p++
		} else {
			s.typ()
		}
	case e == 0x10: // BYREF
		s.typ()
	case e == 0x11, e == 0x12:
		s.typeRef()
	case e == 0x13, e == 0x1e: // VAR, MVAR
		s.cu()
	case e == 0x14: // ARRAY
		s.typ()
		s.cu() // rank
		for i, n := 0, s.count(); i < n && !s.bad; i++ {
			s.cu()
		}
		for i, n := 0, s.count(); i < n && !s.bad; i++ {
			s.cu()
		}
	case e == 0x15: // GENERICINST
		if c := s.byte(); c != 0x11 && c != 0x12 {
			s.bad = true
			return
		}
		s.typeRef()
		n := s.count()
		if n == 0 {
			s.bad = true
		}
		for i := 0; i < n && !s.bad; i++ {
			s.typ()
		}
	case e == 0x1b: // FNPTR
		s.method()
	case e == 0x1d: // SZARRAY
		s.customMods()
		s.typ()
	default:
		s.bad = true
	}
}

// param is Param / RetType: CustomMod* (BYREF? Type | TYPEDBYREF | VOID).
func (s *sigp) param(ret bool) {
	s.customMods()
	switch s.peek() {
	case 0x10:
		s.p++
		s.typ()
	case 0x16:
		s.p++
	case 0x01:
		if !ret {
			s.bad = true
		}
		s.p++
	default:
		s.typ()
	}
}

func (s *sigp) method() {
	if s.depth++; s.depth > maxSigDepth {
		s.bad = true
		return
	}
	defer func() { s.depth-- }()
	cc := s.byte()
	if cc&0x0f > 5 || cc&0x0f == 0x06 {
		s.bad = true
		return
	}
	if cc&0x10 != 0 {
		s.cu() // generic parameter count
	}
	n := s.count()
	s.param(true)
	for i := 0; i < n && !s.bad; i++ {
		if s.peek() == 0x41 { // SENTINEL
			s.p++
		}
		s.param(false)
	}
}

// ParseSig walks a signature blob of the given kind and returns the embedded type references
// (strict also checks that each is a plausible TypeDef/TypeRef/TypeSpec coded value).
// ok is false when the blob is not exactly one well-formed signature of that kind.
func ParseSig(kind byte, b []byte, strict bool) (slots []SigSlot, ok bool) {
	s := &sigp{b: b, strict: strict}
	switch kind {
	case BlobMethod:
		s.method()
	case BlobField:
		if s.byte() != 0x06 {
			return nil, false
		}
		s.customMods()
		s.typ()
	case BlobLocal:
		if s.byte() != 0x07 {
			return nil, false
		}
		for i, n := 0, s.count(); i < n && !s.bad; i++ {
			for !s.bad {
				c := s.peek()
				if c == 0x1f || c == 0x20 {
					s.p++
					s.typeRef()
				} else if c == 0x45 {
					s.p++
				} else {
					break
				}
			}
			if s.peek() == 0x10 {
				s.p++
			}
			if s.peek() == 0x16 {
				s.p++
			} else {
				s.typ()
			}
		}
	case BlobProp:
		if c := s.byte(); c&0x0f != 0x08 {
			return nil, false
		}
		n := s.count()
		s.param(false)
		for i := 0; i < n && !s.bad; i++ {
			s.param(false)
		}
	case BlobType:
		s.typ()
	case BlobInst:
		if s.byte() != 0x0a {
			return nil, false
		}
		n := s.count()
		if n == 0 {
			return nil, false
		}
		for i := 0; i < n && !s.bad; i++ {
			s.typ()
		}
	default:
		return nil, false
	}
	if s.bad || s.p != len(b) {
		return nil, false
	}
	return s.slots, true
}

// SniffKind picks the signature family of a MemberRef / StandAloneSig blob from its first byte.
func SniffKind(b []byte) byte {
	if len(b) == 0 {
		return BlobOpaque
	}
	switch b[0] & 0x0f {
	case 0x06:
		return BlobField
	case 0x07:
		return BlobLocal
	case 0x08:
		return BlobProp
	case 0x0a:
		return BlobInst
	}
	if b[0]&0x0f <= 5 {
		return BlobMethod
	}
	return BlobOpaque
}

// KindOf is the blob kind a column of signature family sig gives to blob content b.
func KindOf(sig Sig, b []byte) byte {
	switch sig {
	case SigAny:
		return SniffKind(b)
	case SigMethod:
		return BlobMethod
	case SigField:
		return BlobField
	case SigProp:
		return BlobProp
	case SigType:
		return BlobType
	case SigInst:
		return BlobInst
	}
	return BlobOpaque
}

// maskCompressed overwrites the value bits of an embedded compressed integer and keeps the width
// prefix, so a masked signature still parses to the same shape.
func maskCompressed(b []byte, width int) {
	switch width {
	case 1:
		b[0] = 0
	case 2:
		b[0], b[1] = 0x80, 0
	case 4:
		b[0], b[1], b[2], b[3] = 0xC0, 0, 0, 0
	}
}

// putCompressed writes v in the given width; ok is false when it does not fit.
func putCompressed(b []byte, width int, v uint32) bool {
	switch width {
	case 1:
		if v >= 0x80 {
			return false
		}
		b[0] = byte(v)
	case 2:
		if v >= 0x4000 {
			return false
		}
		b[0], b[1] = 0x80|byte(v>>8), byte(v)
	case 4:
		if v >= 0x20000000 {
			return false
		}
		b[0], b[1], b[2], b[3] = 0xC0|byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
	default:
		return false
	}
	return true
}
