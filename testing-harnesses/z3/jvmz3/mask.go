package jvmz3

// MaskSlot overwrites a slot with its mask: 0 for mandatory slots, 1 for slots whose zero means
// "absent" (so a masked present slot stays distinguishable from an absent one).
func MaskSlot(b []byte, s *Slot) {
	p := b[s.Off : s.Off+int64(s.Len)]
	for i := range p {
		p[i] = 0
	}
	if s.Opt {
		p[len(p)-1] = 1
	}
}

// Mask masks every selected slot (all when sel is nil) in place.
func Mask(b []byte, slots []Slot, sel func(*Slot) bool) {
	for i := range slots {
		if sel == nil || sel(&slots[i]) {
			MaskSlot(b, &slots[i])
		}
	}
}
