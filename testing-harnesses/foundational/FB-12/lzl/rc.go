// Package lzl is "LZMA-lite": an LZ77 parser with an adaptive binary range
// coder, written for the X6 experiment with the Go standard library only.
// The model follows LZMA's published structure (12-state machine, lc=3 literal
// contexts with matched-literal coding, two length coders, 6-bit distance slots
// with reverse-tree footers and 4 align bits, rep0..rep3 and short rep).
// Every frame is independent: the model is reset and the window is the frame.
package lzl

import "math"

const (
	probBits  = 11
	probInit  = 1 << (probBits - 1)
	moveBits  = 5
	topValue  = 1 << 24
	priceBits = 4 // prices are in 1/16 bit
)

type prob uint16

// rcEnc is the LZMA range encoder (carry propagation through a cache byte).
type rcEnc struct {
	low       uint64
	rng       uint32
	cache     byte
	cacheSize int64
	out       []byte
}

func (e *rcEnc) reset(dst []byte) {
	e.low, e.rng, e.cache, e.cacheSize, e.out = 0, 0xFFFFFFFF, 0, 1, dst[:0]
}

func (e *rcEnc) shiftLow() {
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
		e.cache = byte(e.low >> 24)
	}
	e.cacheSize++
	e.low = (e.low & 0x00FFFFFF) << 8
}

func (e *rcEnc) bit(p *prob, b uint32) {
	bound := (e.rng >> probBits) * uint32(*p)
	if b == 0 {
		e.rng = bound
		*p += (1<<probBits - *p) >> moveBits
	} else {
		e.low += uint64(bound)
		e.rng -= bound
		*p -= *p >> moveBits
	}
	for e.rng < topValue {
		e.rng <<= 8
		e.shiftLow()
	}
}

func (e *rcEnc) direct(v uint32, n uint) {
	for n > 0 {
		n--
		e.rng >>= 1
		if (v>>n)&1 != 0 {
			e.low += uint64(e.rng)
		}
		for e.rng < topValue {
			e.rng <<= 8
			e.shiftLow()
		}
	}
}

// finish flushes the coder. The first output byte of an LZMA range coder is
// always 0 and is dropped; trailing zero bytes are dropped too, because the
// decoder reads zeros past the end of its input (the frame length is known
// out of band, as a pack record header carries it).
func (e *rcEnc) finish() []byte {
	for range 5 {
		e.shiftLow()
	}
	out := e.out
	if len(out) == 0 || out[0] != 0 {
		panic("lzl: range coder invariant: first byte not zero")
	}
	out = out[1:]
	for len(out) > 0 && out[len(out)-1] == 0 {
		out = out[:len(out)-1]
	}
	return out
}

type rcDec struct {
	rng, code uint32
	in        []byte
	pos       int
	over      int // bytes read past the end (zeros)
}

func (d *rcDec) next() uint32 {
	if d.pos < len(d.in) {
		b := d.in[d.pos]
		d.pos++
		return uint32(b)
	}
	d.over++
	return 0
}

func (d *rcDec) reset(in []byte) {
	d.in, d.pos, d.over, d.rng, d.code = in, 0, 0, 0xFFFFFFFF, 0
	for range 4 { // the dropped leading 0 byte is implicit
		d.code = d.code<<8 | d.next()
	}
}

func (d *rcDec) bit(p *prob) uint32 {
	bound := (d.rng >> probBits) * uint32(*p)
	var b uint32
	if d.code < bound {
		d.rng = bound
		*p += (1<<probBits - *p) >> moveBits
	} else {
		d.code -= bound
		d.rng -= bound
		*p -= *p >> moveBits
		b = 1
	}
	if d.rng < topValue {
		d.rng <<= 8
		d.code = d.code<<8 | d.next()
	}
	return b
}

func (d *rcDec) direct(n uint) uint32 {
	var v uint32
	for ; n > 0; n-- {
		d.rng >>= 1
		var b uint32
		if d.code >= d.rng {
			d.code -= d.rng
			b = 1
		}
		v = v<<1 | b
		if d.rng < topValue {
			d.rng <<= 8
			d.code = d.code<<8 | d.next()
		}
	}
	return v
}

// ---- prices -----------------------------------------------------------------

var probPrices [1 << (probBits - priceBits)]uint32

func init() {
	// price of coding a bit whose probability is p/2048, in 1/16 bit (LZMA's table method)
	for i := range probPrices {
		w := float64(i<<priceBits+(1<<(priceBits-1))) / float64(1<<probBits)
		probPrices[i] = uint32(-math.Log2(w)*16 + 0.5)
	}
}

func priceBit(p prob, b uint32) uint32 {
	if b == 0 {
		return probPrices[p>>priceBits]
	}
	return probPrices[(1<<probBits-p)>>priceBits]
}
