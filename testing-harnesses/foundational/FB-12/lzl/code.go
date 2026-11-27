package lzl

// Symbol coding shared by the encoder (rcEnc) and the price model.

func encTree(e *rcEnc, p []prob, bits uint, sym uint32) {
	m := uint32(1)
	for i := int(bits) - 1; i >= 0; i-- {
		b := (sym >> uint(i)) & 1
		e.bit(&p[m], b)
		m = m<<1 | b
	}
}

func encTreeRev(e *rcEnc, p []prob, bits uint, sym uint32) {
	m := uint32(1)
	for range bits {
		b := sym & 1
		sym >>= 1
		e.bit(&p[m], b)
		m = m<<1 | b
	}
}

func priceTree(p []prob, bits uint, sym uint32) uint32 {
	var c uint32
	m := uint32(1)
	for i := int(bits) - 1; i >= 0; i-- {
		b := (sym >> uint(i)) & 1
		c += priceBit(p[m], b)
		m = m<<1 | b
	}
	return c
}

func priceTreeRev(p []prob, bits uint, sym uint32) uint32 {
	var c uint32
	m := uint32(1)
	for range bits {
		b := sym & 1
		sym >>= 1
		c += priceBit(p[m], b)
		m = m<<1 | b
	}
	return c
}

func decTree(d *rcDec, p []prob, bits uint) uint32 {
	m := uint32(1)
	for range bits {
		m = m<<1 | d.bit(&p[m])
	}
	return m - (1 << bits)
}

func decTreeRev(d *rcDec, p []prob, bits uint) uint32 {
	m, sym := uint32(1), uint32(0)
	for i := range bits {
		b := d.bit(&p[m])
		m = m<<1 | b
		sym |= b << i
	}
	return sym
}

// ---- literals ----

func encLit(e *rcEnc, p *[0x300]prob, sym byte) {
	s := uint32(sym) | 0x100
	for s < 0x10000 {
		e.bit(&p[s>>8], (s>>7)&1)
		s <<= 1
	}
}

func encLitMatched(e *rcEnc, p *[0x300]prob, sym, match byte) {
	offs, s, mb := uint32(0x100), uint32(sym)|0x100, uint32(match)
	for s < 0x10000 {
		mb <<= 1
		e.bit(&p[offs+(mb&offs)+(s>>8)], (s>>7)&1)
		s <<= 1
		offs &^= mb ^ s
	}
}

func priceLit(p *[0x300]prob, sym byte) uint32 {
	var c uint32
	s := uint32(sym) | 0x100
	for s < 0x10000 {
		c += priceBit(p[s>>8], (s>>7)&1)
		s <<= 1
	}
	return c
}

func priceLitMatched(p *[0x300]prob, sym, match byte) uint32 {
	var c uint32
	offs, s, mb := uint32(0x100), uint32(sym)|0x100, uint32(match)
	for s < 0x10000 {
		mb <<= 1
		c += priceBit(p[offs+(mb&offs)+(s>>8)], (s>>7)&1)
		s <<= 1
		offs &^= mb ^ s
	}
	return c
}

func decLit(d *rcDec, p *[0x300]prob) byte {
	s := uint32(1)
	for s < 0x100 {
		s = s<<1 | d.bit(&p[s])
	}
	return byte(s)
}

func decLitMatched(d *rcDec, p *[0x300]prob, match byte) byte {
	offs, s, mb := uint32(0x100), uint32(1), uint32(match)
	for s < 0x100 {
		mb <<= 1
		b := d.bit(&p[offs+(mb&offs)+s])
		s = s<<1 | b
		if (mb>>8)&1 != b {
			offs = 0
		}
	}
	return byte(s)
}

// ---- lengths (l = length - minMatch, 0..271) ----

func encLen(e *rcEnc, m *lenModel, l uint32, ps int) {
	switch {
	case l < 8:
		e.bit(&m.choice, 0)
		encTree(e, m.low[ps][:], lenLowBits, l)
	case l < 16:
		e.bit(&m.choice, 1)
		e.bit(&m.choice2, 0)
		encTree(e, m.mid[ps][:], lenMidBits, l-8)
	default:
		e.bit(&m.choice, 1)
		e.bit(&m.choice2, 1)
		encTree(e, m.high[:], lenHighBits, l-16)
	}
}

func priceLen(m *lenModel, l uint32, ps int) uint32 {
	switch {
	case l < 8:
		return priceBit(m.choice, 0) + priceTree(m.low[ps][:], lenLowBits, l)
	case l < 16:
		return priceBit(m.choice, 1) + priceBit(m.choice2, 0) + priceTree(m.mid[ps][:], lenMidBits, l-8)
	}
	return priceBit(m.choice, 1) + priceBit(m.choice2, 1) + priceTree(m.high[:], lenHighBits, l-16)
}

func decLen(d *rcDec, m *lenModel, ps int) uint32 {
	if d.bit(&m.choice) == 0 {
		return decTree(d, m.low[ps][:], lenLowBits)
	}
	if d.bit(&m.choice2) == 0 {
		return 8 + decTree(d, m.mid[ps][:], lenMidBits)
	}
	return 16 + decTree(d, m.high[:], lenHighBits)
}

// ---- distances (dd = distance - 1) ----

func encDist(e *rcEnc, m *model, dd uint32, ls int) {
	slot := distSlot(dd)
	encTree(e, m.slot[ls][:], slotBits, slot)
	if slot < 4 {
		return
	}
	fb := uint(slot>>1) - 1
	base := (2 | slot&1) << fb
	red := dd - base
	if slot < endPosModel {
		encTreeRev(e, m.spec[slot][:], fb, red)
		return
	}
	e.direct(red>>alignBits, fb-alignBits)
	encTreeRev(e, m.align[:], alignBits, red&(1<<alignBits-1))
}

func priceDist(m *model, dd uint32, ls int) uint32 {
	slot := distSlot(dd)
	c := priceTree(m.slot[ls][:], slotBits, slot)
	if slot < 4 {
		return c
	}
	fb := uint(slot>>1) - 1
	base := (2 | slot&1) << fb
	red := dd - base
	if slot < endPosModel {
		return c + priceTreeRev(m.spec[slot][:], fb, red)
	}
	return c + uint32(fb-alignBits)<<priceBits + priceTreeRev(m.align[:], alignBits, red&(1<<alignBits-1))
}

func decDist(d *rcDec, m *model, ls int) uint32 {
	slot := decTree(d, m.slot[ls][:], slotBits)
	if slot < 4 {
		return slot
	}
	fb := uint(slot>>1) - 1
	base := (2 | slot&1) << fb
	if slot < endPosModel {
		return base + decTreeRev(d, m.spec[slot][:], fb)
	}
	return base + d.direct(fb-alignBits)<<alignBits + decTreeRev(d, m.align[:], alignBits)
}
