package mdelta2

import (
	"encoding/binary"
	"math"
)

// NRC ("numeric residual coder"): residual words are coded as bit length (rANS, tables chosen per
// tensor) plus raw mantissa bits. The prediction's exponent conditions the symbol: in shift mode
// the symbol is bitlen + relativeExponent, which makes the distribution of a value-domain update
// independent of the weight magnitude (one small table instead of many). A neighbour-magnitude
// context was tried and removed: it gained <0.01% on every real residual (updates are noise).

func expField(d DType, bits uint32) int {
	switch d {
	case DF32:
		return int(bits>>23) & 0xff
	case DBF16:
		return int(bits>>7) & 0xff
	}
	return int(bits>>10) & 0x1f
}

func expShiftMask(d DType) (uint, int) {
	switch d {
	case DF32:
		return 23, 0xff
	case DBF16:
		return 7, 0xff
	}
	return 10, 0x1f
}

type nrcCfg struct {
	shift bool
	nc    int // exponent contexts: 1 or 4
}

func (c nrcCfg) byte() byte {
	b := byte(0)
	if c.shift {
		b = 1
	}
	if c.nc == 4 {
		b |= 2
	}
	return b
}

func cfgFromByte(b byte) (nrcCfg, bool) {
	if b&^3 != 0 {
		return nrcCfg{}, false
	}
	c := nrcCfg{shift: b&1 != 0, nc: 1}
	if b&2 != 0 {
		c.nc = 4
	}
	return c, true
}

const nrcSyms = 96

// exps computes per-element relative exponents (0..63) and the base used.
func exps(d DType, pred []byte) ([]uint8, int) {
	w := d.Width()
	n := len(pred) / w
	sh, mk := expShiftMask(d)
	raw := make([]uint8, n)
	emax := 0
	for i := 0; i < n; i++ {
		var v uint32
		switch w {
		case 4:
			v = binary.LittleEndian.Uint32(pred[4*i:])
		case 2:
			v = uint32(binary.LittleEndian.Uint16(pred[2*i:]))
		default:
			v = uint32(pred[i])
		}
		x := int(v>>sh) & mk
		raw[i] = uint8(x)
		if x > emax {
			emax = x
		}
	}
	eb := emax - 63
	if eb < 0 {
		eb = 0
	}
	for i := range raw {
		v := int(raw[i]) - eb
		if v < 0 {
			v = 0
		}
		raw[i] = uint8(v)
	}
	return raw, eb
}

func nrcCtx(c nrcCfg, er uint8) int {
	if c.nc == 4 {
		return int(er >> 4)
	}
	return 0
}

func nrcSym(c nrcCfg, s int, er uint8) int {
	if c.shift {
		return s + int(er)
	}
	return s
}

// nrcEstimate predicts the payload size of a configuration from histograms (no stream built).
func nrcEstimate(c nrcCfg, w int, resid []byte, er []uint8, n int) float64 {
	cnt := make([]int, c.nc*nrcSyms)
	tot := make([]int, c.nc)
	raw := 0
	for i := 0; i < n; i++ {
		r := le(resid, w, i)
		s := bitlen(r)
		x := nrcCtx(c, er[i])
		cnt[x*nrcSyms+nrcSym(c, s, er[i])]++
		tot[x]++
		if s > 1 {
			raw += s - 1
		}
	}
	bitsTotal := float64(raw)
	tab := 0
	for x := 0; x < c.nc; x++ {
		for s := 0; s < nrcSyms; s++ {
			if v := cnt[x*nrcSyms+s]; v > 0 {
				bitsTotal -= float64(v) * math.Log2(float64(v)/float64(tot[x]))
				tab += 3
			}
		}
	}
	return bitsTotal/8 + float64(tab)
}

// NRCEncode codes residual words against prediction exponents with the best configuration.
func NRCEncode(d DType, resid, pred []byte) []byte {
	w := d.Width()
	n := len(resid) / w
	er, eb := exps(d, pred)
	best, bestE := nrcCfg{true, 1}, math.Inf(1)
	for _, c := range []nrcCfg{{true, 1}, {false, 1}, {true, 4}, {false, 4}} {
		if e := nrcEstimate(c, w, resid, er, n); e < bestE {
			best, bestE = c, e
		}
	}
	return nrcEncodeCfg(best, w, resid, er, eb, n)
}

func nrcEncodeCfg(c nrcCfg, w int, resid []byte, er []uint8, eb, n int) []byte {
	cnt := make([][]uint32, c.nc)
	for i := range cnt {
		cnt[i] = make([]uint32, nrcSyms)
	}
	var rawBits int
	syms := make([]uint8, n)
	ctxAt := make([]uint8, n)
	for i := 0; i < n; i++ {
		r := le(resid, w, i)
		s := bitlen(r)
		sy := nrcSym(c, s, er[i])
		syms[i] = uint8(sy)
		x := nrcCtx(c, er[i])
		ctxAt[i] = uint8(x)
		cnt[x][sy]++
		if s > 1 {
			rawBits += s - 1
		}
	}
	tabs := make([]*ransTab, c.nc)
	hdr := []byte{c.byte(), byte(eb)}
	for i := range cnt {
		f := normFreqs(cnt[i])
		hdr = appendFreqs(hdr, f)
		tabs[i], _ = newTab(f)
	}
	raw := make([]byte, 0, (rawBits+7)/8)
	var acc uint64
	var nacc uint
	for i := 0; i < n; i++ {
		r := le(resid, w, i)
		if s := bitlen(r); s > 1 {
			nb := uint(s - 1)
			acc |= (uint64(r) & (1<<nb - 1)) << nacc
			nacc += nb
			for nacc >= 8 {
				raw = append(raw, byte(acc))
				acc >>= 8
				nacc -= 8
			}
		}
	}
	if nacc > 0 {
		raw = append(raw, byte(acc))
	}
	enc := newRansEnc()
	for i := n - 1; i >= 0; i-- {
		t := tabs[ctxAt[i]]
		sy := int(syms[i])
		enc.put(i&1, uint32(t.freq[sy]), uint32(t.start[sy]))
	}
	out := hdr
	out = binary.AppendUvarint(out, uint64(len(raw)))
	out = append(out, enc.finish()...)
	return append(out, raw...)
}

// NRCDecode inverts NRCEncode given the same prediction; n elements of width w.
func NRCDecode(d DType, payload, pred []byte, n int) ([]byte, error) {
	w := d.Width()
	if len(payload) < 2 || len(pred) != n*w {
		return nil, ErrCorrupt
	}
	c, ok := cfgFromByte(payload[0])
	if !ok {
		return nil, ErrCorrupt
	}
	eb := int(payload[1])
	pos := 2
	// flat decode tables: entry = freq<<19 | bias<<7 | sym  (bias = slot - start)
	flat := make([][ransM]uint32, c.nc)
	for i := 0; i < c.nc; i++ {
		f, k, err := readFreqs(payload[pos:], nrcSyms)
		if err != nil {
			return nil, err
		}
		pos += k
		t, err := newTab(f)
		if err != nil {
			return nil, err
		}
		for slot := 0; slot < ransM; slot++ {
			sy := uint32(t.dec[slot])
			flat[i][slot] = uint32(t.freq[sy])<<19 | (uint32(slot)-uint32(t.start[sy]))<<7 | sy
		}
	}
	rl, k := binary.Uvarint(payload[pos:])
	if k <= 0 || rl > uint64(n)*uint64(8*w) {
		return nil, ErrCorrupt
	}
	pos += k
	dec, k2, err := newRansDec(payload[pos:])
	if err != nil {
		return nil, err
	}
	pos += k2
	if uint64(len(payload)-pos) != rl {
		return nil, ErrCorrupt
	}
	raw := payload[pos:]
	nbits := uint64(len(raw)) * 8
	out := make([]byte, n*w)
	sh, mk := expShiftMask(d)
	x0, x1 := dec.x[0], dec.x[1]
	b0, b1 := dec.buf[0], dec.buf[1]
	p0, p1 := dec.pos[0], dec.pos[1]
	var bp uint64
	for i := 0; i < n; i++ {
		var pv uint32
		switch w {
		case 4:
			pv = binary.LittleEndian.Uint32(pred[4*i:])
		case 2:
			pv = uint32(binary.LittleEndian.Uint16(pred[2*i:]))
		default:
			pv = uint32(pred[i])
		}
		er := int(pv>>sh)&mk - eb
		if er < 0 {
			er = 0
		} else if er > 63 {
			er = 63 // only reachable with a corrupt base-exponent byte
		}
		tab := &flat[0]
		if c.nc == 4 {
			tab = &flat[er>>4]
		}
		var e uint32
		if i&1 == 0 {
			e = tab[x0&(ransM-1)]
			x0 = (e>>19)*(x0>>ransBits) + (e>>7)&0xfff
			for x0 < ransL {
				if p0 >= len(b0) {
					return nil, ErrCorrupt
				}
				x0 = x0<<8 | uint32(b0[p0])
				p0++
			}
		} else {
			e = tab[x1&(ransM-1)]
			x1 = (e>>19)*(x1>>ransBits) + (e>>7)&0xfff
			for x1 < ransL {
				if p1 >= len(b1) {
					return nil, ErrCorrupt
				}
				x1 = x1<<8 | uint32(b1[p1])
				p1++
			}
		}
		s := int(e & 127)
		if c.shift {
			s -= er
		}
		if s < 0 || s > 8*w {
			return nil, ErrCorrupt
		}
		var r uint32
		if s == 1 {
			r = 1
		} else if s > 1 {
			nb := uint(s - 1)
			if bp+uint64(nb) > nbits {
				return nil, ErrCorrupt
			}
			var v uint64
			byteIdx := int(bp >> 3)
			if byteIdx+8 <= len(raw) {
				v = binary.LittleEndian.Uint64(raw[byteIdx:])
			} else {
				for j := 0; byteIdx+j < len(raw) && j < 8; j++ {
					v |= uint64(raw[byteIdx+j]) << (8 * uint(j))
				}
			}
			v >>= uint(bp & 7)
			r = 1<<nb | uint32(v&(1<<nb-1))
			bp += uint64(nb)
		}
		switch w {
		case 4:
			binary.LittleEndian.PutUint32(out[4*i:], r)
		case 2:
			binary.LittleEndian.PutUint16(out[2*i:], uint16(r))
		default:
			out[i] = byte(r)
		}
	}
	return out, nil
}
