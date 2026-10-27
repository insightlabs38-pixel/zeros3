package dexz3

// MaskSlot overwrites a slot's bytes with the canonical mask for its encoding, preserving the
// byte length so that the surrounding structure still parses. The mask never collides with the
// "absent" encodings (offset 0, ULEB128p1 0, NO_INDEX) so a decoder can find the slot again.
func MaskSlot(b []byte, s *Slot) {
	p := b[s.Off : s.Off+int64(s.Len)]
	switch s.Enc {
	case EncU16, EncU32, EncVal:
		for i := range p {
			p[i] = 0
		}
		if s.Space == SpOffset && s.Enc == EncU32 {
			p[0] = 1
		}
	case EncULEB, EncDelta, EncP1:
		for i := range p {
			p[i] = 0x80
		}
		p[len(p)-1] = 0
		if s.Enc == EncP1 || s.Space == SpOffset { // "present" slots decode as the value 1, in the same length
			p[0] |= 1
		}
	}
}

// Mask masks every selected slot of b in place.
func Mask(b []byte, slots []Slot, sel func(*Slot) bool) {
	for i := range slots {
		if sel == nil || sel(&slots[i]) {
			MaskSlot(b, &slots[i])
		}
	}
}
