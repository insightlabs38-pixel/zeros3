package binref

// ScanX86 finds x86-64 reference slots in executable regions. It is a pattern
// scan, not a disassembler: every candidate must decode to a target inside a
// mapped region, and only instruction forms with no trailing immediate are
// recognised. A false positive costs a wasted reference record, never
// correctness (slots are listed explicitly and the round trip is verified),
// so the scan favours precision over recall.
func ScanX86(im *Image) []Slot {
	var out []Slot
	if im.Prepare() != nil || im.Arch != "x86-64" {
		return nil
	}
	for ri := range im.Regions {
		r := &im.Regions[ri]
		if !r.Mapped || !r.Exec {
			continue
		}
		b := im.Data[r.Off : r.Off+r.Len]
		for i := 0; i < len(b); i++ {
			c := b[i]
			if (c == 0xE8 || c == 0xE9) && i+5 <= len(b) {
				d := int32(le(b[i+1:], 4))
				t := r.VA + uint64(i) + 5 + uint64(int64(d))
				if tr := im.regionAtVA(t); tr >= 0 && im.Regions[tr].Exec {
					out = append(out, Slot{Off: r.Off + i + 1, Enc: EncRel32, Class: ClassBranch})
				}
			}
			if c == 0x0F && i+6 <= len(b) && b[i+1]&0xF0 == 0x80 {
				d := int32(le(b[i+2:], 4))
				t := r.VA + uint64(i) + 6 + uint64(int64(d))
				if tr := im.regionAtVA(t); tr >= 0 && im.Regions[tr].Exec {
					out = append(out, Slot{Off: r.Off + i + 2, Enc: EncRel32, Class: ClassBranch})
				}
			}
			// modrm mod=00 rm=101 (RIP-relative disp32) at i, slot at i+1.
			if c&0xC7 == 0x05 && i >= 1 && i+5 <= len(b) {
				if bias, ok := ripForm(b, i); ok && i+5+int(bias) <= len(b) {
					d := int32(le(b[i+1:], 4))
					if d == 0 {
						continue
					}
					t := r.VA + uint64(i) + 5 + uint64(bias) + uint64(int64(d))
					if im.regionAtVA(t) >= 0 {
						out = append(out, Slot{Off: r.Off + i + 1, Enc: EncRel32, Bias: bias, Class: ClassRIP})
					}
				}
			}
		}
	}
	return out
}

// ripForm reports whether the modrm byte at b[i] belongs to an instruction
// form with a RIP-relative operand, and how many immediate bytes follow the
// displacement (the Bias of the slot).
func ripForm(b []byte, i int) (bias uint8, ok bool) {
	op, modrm := b[i-1], b[i]
	if op == 0xFF {
		return 0, modrm == 0x15 || modrm == 0x25 || modrm == 0x05 || modrm == 0x0D // call/jmp/inc/dec [rip+d]
	}
	if i >= 5 && b[i-5] == 0x62 { // EVEX: any opcode takes a modrm; map 3 (0F3A) carries an imm8
		if b[i-4]&7 == 3 {
			return 1, true
		}
		return 0, true
	}
	if i >= 4 && b[i-4] == 0xC4 { // VEX3
		if b[i-3]&0x1F == 3 {
			return 1, true
		}
		return 0, true
	}
	if vecOp(op) && i >= 3 && b[i-3] == 0xC5 { // VEX2 moves / arithmetic
		return 0, true
	}
	if i >= 3 && b[i-3] == 0x0F && b[i-2] == 0x38 { // three-byte 0F 38 xx (AES, ...)
		return 0, true
	}
	if i >= 2 && b[i-2] == 0x0F {
		switch {
		case op >= 0x90 && op <= 0x9F: // setcc
			return 0, true
		}
		switch op {
		case 0x10, 0x11, 0x28, 0x29, 0x2A, 0x2C, 0x2D, 0x2E, 0x2F, 0x51, 0x54, 0x57, 0x58, 0x59, 0x5C, 0x5E, 0x6F, 0x7F, 0xAF, 0xEF, 0x74, 0x75, 0x76, 0x6E, 0x7E, 0xB0, 0xB1, 0xB6, 0xB7, 0xBE, 0xBF, 0xC1, 0xD6:
			return 0, true
		}
		return 0, false
	}
	switch op {
	case 0x80, 0x83, 0xC6, 0x6B:
		return 1, true
	case 0x81, 0xC7, 0x69:
		return 4, true
	case 0xF6, 0xF7:
		if (modrm>>3)&7 > 1 {
			return 0, true
		}
		if op == 0xF6 {
			return 1, true
		}
		return 4, true
	case 0x8B, 0x8D, 0x89, 0x88, 0x8A, 0x63:
		return 0, true
	case 0x85, 0x84, 0x03, 0x2B, 0x3B, 0x39, 0x01, 0x29, 0x0B, 0x23, 0x33, 0x31, 0x21, 0x09:
		return 0, true
	}
	return 0, false
}

// ScanPtr lists aligned 8-byte words in mapped non-executable regions whose
// value is an address inside a mapped region (absolute pointers).
func ScanPtr(im *Image) []Slot {
	var out []Slot
	if im.Prepare() != nil {
		return nil
	}
	for ri := range im.Regions {
		r := &im.Regions[ri]
		if !r.Mapped || r.Exec {
			continue
		}
		for o := 0; o+8 <= r.Len; o += 8 {
			v := le(im.Data[r.Off+o:], 8)
			if v != 0 && im.regionAtVA(v) >= 0 {
				out = append(out, Slot{Off: r.Off + o, Enc: EncAbs64, Class: ClassPtr})
			}
		}
	}
	return out
}

func vecOp(op byte) bool {
	switch op {
	case 0x10, 0x11, 0x28, 0x29, 0x6F, 0x7F, 0x6E, 0x7E, 0xEF, 0xDB, 0xEB, 0x74, 0x75, 0x76, 0x58, 0x59, 0x5C, 0x2A, 0x2C, 0x2D, 0x12, 0x13, 0x16, 0x17, 0xE7, 0x2B:
		return true
	}
	return false
}
