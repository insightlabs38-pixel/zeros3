package mdelta2

import (
	"encoding/binary"
)

// NRC ("numeric residual coder"): residual words are coded as bit length (rANS, adaptive tables
// chosen per tensor) plus raw mantissa bits. The prediction's exponent conditions the symbol:
// in shift mode the symbol is bitlen + relativeExponent, which makes the distribution of a
// value-domain update independent of the weight magnitude (one small table instead of many).

func expField(d DType, bits uint32) int {
	switch d {
	case DF32:
		return int(bits>>23) & 0xff
	case DBF16:
		return int(bits>>7) & 0xff
	}
	return int(bits>>10) & 0x1f
}

type nrcCfg struct {
	shift bool
	nc    int  // exponent contexts: 1, 4 or 16
	prev  bool // 4x contexts from the previous element's symbol relative to the tensor mean symbol
}

func (c nrcCfg) byte() byte {
	b := byte(0)
	if c.shift {
		b = 1
	}
	switch c.nc {
	case 4:
		b |= 2
	case 16:
		b |= 4
	}
	if c.prev {
		b |= 8
	}
	return b
}

func cfgFromByte(b byte) (nrcCfg, bool) {
	c := nrcCfg{shift: b&1 != 0, nc: 1, prev: b&8 != 0}
	if b&^15 != 0 {
		return c, false
	}
	switch (b >> 1) & 3 {
	case 0:
	case 1:
		c.nc = 4
	case 2:
		c.nc = 16
	default:
		return c, false
	}
	return c, true
}

const nrcSyms = 96

// exps computes per-element relative exponents (0..63) and the base used.
func exps(d DType, pred []byte) ([]uint8, int) {
	w := d.Width()
	n := len(pred) / w
	e := make([]uint8, n)
	emax := 0
	raw := make([]uint8, n)
	for i := 0; i < n; i++ {
		x := expField(d, le(pred, w, i))
		raw[i] = uint8(x)
		if x > emax {
			emax = x
		}
	}
	eb := emax - 63
	if eb < 0 {
		eb = 0
	}
	for i := range e {
		v := int(raw[i]) - eb
		if v < 0 {
			v = 0
		}
		e[i] = uint8(v)
	}
	return e, eb
}

func nrcCtx(c nrcCfg, er uint8, prev, mu int) int {
	x := 0
	switch c.nc {
	case 4:
		x = int(er >> 4)
	case 16:
		x = int(er >> 2)
	}
	if !c.prev {
		return x
	}
	d := prev - mu
	k := 0
	switch {
	case d < -2:
	case d <= 0:
		k = 1
	case d <= 2:
		k = 2
	default:
		k = 3
	}
	return x*4 + k
}

func (c nrcCfg) tables() int {
	if c.prev {
		return c.nc * 4
	}
	return c.nc
}

func nrcSym(c nrcCfg, s int, er uint8) int {
	if c.shift {
		return s + int(er)
	}
	return s
}

// NRCEncode codes residual words against prediction exponents; it tries every configuration and
// keeps the smallest payload.
func NRCEncode(d DType, resid, pred []byte) []byte {
	w := d.Width()
	n := len(resid) / w
	er, eb := exps(d, pred)
	var best []byte
	for _, c := range []nrcCfg{{true, 1, false}, {false, 1, false}, {true, 4, false}, {false, 4, false}} {
		p := nrcEncodeCfg(c, w, resid, er, eb, n)
		if best == nil || len(p) < len(best) {
			best = p
		}
	}
	return best
}

func nrcEncodeCfg(c nrcCfg, w int, resid []byte, er []uint8, eb, n int) []byte {
	cnt := make([][]uint32, c.tables())
	for i := range cnt {
		cnt[i] = make([]uint32, nrcSyms)
	}
	var rawBits int
	syms := make([]uint8, n)
	var ssum int
	for i := 0; i < n; i++ {
		r := le(resid, w, i)
		s := bitlen(r)
		sy := nrcSym(c, s, er[i])
		syms[i] = uint8(sy)
		ssum += sy
		if s > 1 {
			rawBits += s - 1
		}
	}
	mu := 0
	if n > 0 {
		mu = ssum / n
	}
	ctxAt := make([]uint8, n)
	for i := 0; i < n; i++ {
		prev := mu
		if i > 0 {
			prev = int(syms[i-1])
		}
		x := nrcCtx(c, er[i], prev, mu)
		ctxAt[i] = uint8(x)
		cnt[x][syms[i]]++
	}
	freqs := make([][]uint16, c.tables())
	tabs := make([]*ransTab, c.tables())
	hdr := []byte{c.byte(), byte(eb), byte(mu)}
	for i := range cnt {
		freqs[i] = normFreqs(cnt[i])
		hdr = appendFreqs(hdr, freqs[i])
		tabs[i], _ = newTab(freqs[i])
	}
	// raw bits, forward order
	raw := make([]byte, (rawBits+7)/8)
	var bp int
	for i := 0; i < n; i++ {
		r := le(resid, w, i)
		s := bitlen(r)
		if s > 1 {
			v := uint64(r) & (1<<uint(s-1) - 1)
			for b := 0; b < s-1; b++ {
				if v>>uint(b)&1 != 0 {
					raw[bp>>3] |= 1 << uint(bp&7)
				}
				bp++
			}
		}
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
	if len(payload) < 3 || len(pred) != n*w {
		return nil, ErrCorrupt
	}
	c, ok := cfgFromByte(payload[0])
	if !ok {
		return nil, ErrCorrupt
	}
	eb := int(payload[1])
	mu := int(payload[2])
	pos := 3
	tabs := make([]*ransTab, c.tables())
	for i := range tabs {
		f, k, err := readFreqs(payload[pos:], nrcSyms)
		if err != nil {
			return nil, err
		}
		pos += k
		if tabs[i], err = newTab(f); err != nil {
			return nil, err
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
	out := make([]byte, n*w)
	var bp uint64
	nb := uint64(len(raw)) * 8
	prevSym := mu
	for i := 0; i < n; i++ {
		pe := expField(d, le(pred, w, i)) - eb
		if pe < 0 {
			pe = 0
		}
		er := uint8(pe)
		t := tabs[nrcCtx(c, er, prevSym, mu)]
		sy, err := dec.get(i&1, t)
		if err != nil {
			return nil, err
		}
		prevSym = sy
		s := sy
		if c.shift {
			s = sy - int(er)
		}
		if s < 0 || s > 8*w {
			return nil, ErrCorrupt
		}
		var r uint32
		if s == 1 {
			r = 1
		} else if s > 1 {
			if bp+uint64(s-1) > nb {
				return nil, ErrCorrupt
			}
			var v uint32
			for b := 0; b < s-1; b++ {
				v |= uint32(raw[bp>>3]>>uint(bp&7)&1) << uint(b)
				bp++
			}
			r = 1<<uint(s-1) | v
		}
		putLE(out, w, i, r)
	}
	return out, nil
}
