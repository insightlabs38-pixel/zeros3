package ir

import "math"

// Adaptive binary range coder (LZMA-style carry propagation) with two-rate
// probability estimates. Integer-only, so encoder and decoder agree on every
// platform; floating point is used only for optional cost accounting.

const (
	probBits = 12
	rcTop    = 1 << 24
)

// bitModel estimates P(bit==0) at two adaptation speeds (shift 4 and 7) and
// averages them, which learns quickly in short units yet stays stable.
type bitModel struct{ fast, slow uint16 }

func newModels(n int) []bitModel {
	m := make([]bitModel, n)
	for i := range m {
		m[i] = bitModel{0x8000, 0x8000}
	}
	return m
}

func (m *bitModel) init() { m.fast, m.slow = 0x8000, 0x8000 }

func (m *bitModel) p() uint32 {
	p := (uint32(m.fast) + uint32(m.slow)) >> 5 // 0..4095 of 12 bits
	if p < 1 {
		p = 1
	} else if p > 4095 {
		p = 4095
	}
	return p
}

func (m *bitModel) update(bit int) {
	if bit == 0 {
		m.fast += (65535 - m.fast) >> 4
		m.slow += (65535 - m.slow) >> 7
	} else {
		m.fast -= m.fast >> 4
		m.slow -= m.slow >> 7
	}
}

// setP initialises both estimates to probability p0 (of a zero bit, 1..4095 / 4096).
func (m *bitModel) setP(p0 int) {
	v := uint16(p0 << 4)
	m.fast, m.slow = v, v
}

// Cost categories for entropy accounting.
const (
	CatKind = iota
	CatLitRun
	CatLenSym
	CatLenExtra
	CatRep
	CatDistSym
	CatDistExtra
	CatPredHit    // predictor: match/literal decisions
	CatPredLen    // predictor: length corrections
	CatPredDist   // predictor: distance rank / escape
	CatPredEscape // predictor: explicit distance after an escape
	NumCats
)

var CatNames = [NumCats]string{"kind", "litrun", "lensym", "lenextra", "rep", "distsym", "distextra", "pred-kind", "pred-len", "pred-dist", "pred-escape"}

type encoder struct {
	low       uint64
	rng       uint32
	cache     byte
	cacheSize int64
	out       []byte
}

func newEncoder() *encoder { return &encoder{rng: 0xFFFFFFFF, cacheSize: 1} }

func (e *encoder) shiftLow() {
	if uint32(e.low) < 0xFF000000 || e.low>>32 != 0 {
		carry := byte(e.low >> 32)
		temp := e.cache
		for {
			e.out = append(e.out, temp+carry)
			temp = 0xFF
			e.cacheSize--
			if e.cacheSize == 0 {
				break
			}
		}
		e.cache = byte(uint32(e.low) >> 24)
	}
	e.cacheSize++
	e.low = uint64(uint32(e.low) << 8)
}

func (e *encoder) encode(m *bitModel, bit int) {
	bound := (e.rng >> probBits) * m.p()
	if bit == 0 {
		e.rng = bound
	} else {
		e.low += uint64(bound)
		e.rng -= bound
	}
	m.update(bit)
	for e.rng < rcTop {
		e.rng <<= 8
		e.shiftLow()
	}
}

// finish flushes with as many trailing zero bits as the interval allows,
// drops the always-zero leading byte and trims trailing zero bytes (the
// decoder reads zeros past the end).
func (e *encoder) finish() []byte {
	for k := 32; k >= 0; k-- {
		mask := uint64(1)<<k - 1
		v := (e.low + mask) &^ mask
		if v < e.low+uint64(e.rng) {
			e.low = v
			break
		}
	}
	for i := 0; i < 5; i++ {
		e.shiftLow()
	}
	out := e.out[1:]
	for len(out) > 0 && out[len(out)-1] == 0 {
		out = out[:len(out)-1]
	}
	return out
}

type decoder struct {
	in   []byte
	pos  int
	rng  uint32
	code uint32
}

func newDecoder(in []byte) *decoder {
	d := &decoder{in: in, rng: 0xFFFFFFFF}
	for i := 0; i < 4; i++ {
		d.code = d.code<<8 | uint32(d.next())
	}
	return d
}

func (d *decoder) next() byte {
	if d.pos < len(d.in) {
		b := d.in[d.pos]
		d.pos++
		return b
	}
	d.pos++ // count reads past the end
	return 0
}

func (d *decoder) decode(m *bitModel) int {
	bound := (d.rng >> probBits) * m.p()
	var bit int
	if d.code < bound {
		d.rng = bound
	} else {
		d.code -= bound
		d.rng -= bound
		bit = 1
	}
	m.update(bit)
	for d.rng < rcTop {
		d.rng <<= 8
		d.code = d.code<<8 | uint32(d.next())
	}
	return bit
}

// rcio is one direction of the coder. bit() encodes the supplied bit when
// encoding and ignores it when decoding; either way it returns the bit that
// was coded, so a single model routine serves both directions.
type rcio struct {
	enc  *encoder
	dec  *decoder
	cost *[NumCats]float64
	cat  int
	// force, when >= 0 (set with forceCat), overrides cat for cost accounting.
	force int
}

func (r *rcio) bit(m *bitModel, b int) int {
	if r.dec != nil {
		return r.dec.decode(m)
	}
	if r.cost != nil {
		p := float64(m.p()) / (1 << probBits)
		if b != 0 {
			p = 1 - p
		}
		c := r.cat
		if r.force > 0 {
			c = r.force - 1
		}
		r.cost[c] -= math.Log2(p)
	}
	r.enc.encode(m, b)
	return b
}

// tree codes an nbits-wide value MSB first through a binary tree of models
// (len(ms) >= 1<<nbits).
func (r *rcio) tree(ms []bitModel, nbits int, v int) int {
	node := 1
	for i := nbits - 1; i >= 0; i-- {
		b := r.bit(&ms[node], (v>>i)&1)
		node = node<<1 | b
	}
	return node - 1<<nbits
}

// gamma codes v >= 0 as an Elias-gamma style number: the bit length of v+1 in
// unary (ctx models un[0..]) then the mantissa (mant[nb][0..3]).
func (r *rcio) gamma(un []bitModel, mant [][4]bitModel, v int) int {
	x := v + 1
	nb := 0
	if r.dec == nil {
		for t := x; t > 1; t >>= 1 {
			nb++
		}
	}
	// unary length
	n := 0
	for {
		more := 0
		if r.dec == nil && n < nb {
			more = 1
		}
		if n >= len(un) {
			break
		}
		if r.bit(&un[n], more) == 0 {
			break
		}
		n++
	}
	nb = n
	if nb >= len(mant) {
		nb = len(mant) - 1
	}
	val := 1
	for j := nb - 1; j >= 0; j-- {
		ctx := 3
		switch {
		case j == nb-1:
			ctx = 0
		case j == nb-2:
			ctx = 1 + val&1
		}
		b := r.bit(&mant[nb][ctx], (x>>j)&1)
		val = val<<1 | b
	}
	return val - 1
}
