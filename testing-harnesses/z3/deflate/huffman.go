package deflate

import (
	"math/bits"
	"sync"
)

const (
	maxCodeLen = 15
	fastBits   = 9
)

// decoder is a canonical Huffman decoder: a fast table for codes up to
// fastBits and the count/symbol arrays for the rest.
type decoder struct {
	count [maxCodeLen + 1]uint16
	sym   []uint16
	fast  [1 << fastBits]uint16 // sym<<4 | len; 0 = take the slow path
	empty bool                  // no symbols at all (only valid for distances)
}

// buildDecoder validates lens with the same acceptance rule as compress/flate
// (complete, or exactly one code of length 1, or empty) and builds a decoder.
func buildDecoder(lens []uint8) (*decoder, bool) {
	d := &decoder{}
	n := 0
	for _, l := range lens {
		if l > maxCodeLen {
			return nil, false
		}
		if l != 0 {
			d.count[l]++
			n++
		}
	}
	if n == 0 {
		d.empty = true
		return d, true
	}
	left := 1
	for l := 1; l <= maxCodeLen; l++ {
		left <<= 1
		left -= int(d.count[l])
		if left < 0 {
			return nil, false // oversubscribed
		}
	}
	if left > 0 && !(n == 1 && d.count[1] == 1) {
		return nil, false // incomplete
	}
	var offs [maxCodeLen + 2]uint16
	for l := 1; l <= maxCodeLen; l++ {
		offs[l+1] = offs[l] + d.count[l]
	}
	d.sym = make([]uint16, n)
	for s, l := range lens {
		if l != 0 {
			d.sym[offs[l]] = uint16(s)
			offs[l]++
		}
	}
	// fast table
	code, idx := 0, 0
	for l := 1; l <= fastBits; l++ {
		for i := 0; i < int(d.count[l]); i++ {
			rev := int(bits.Reverse16(uint16(code))) >> (16 - l)
			e := d.sym[idx]<<4 | uint16(l)
			for j := rev; j < 1<<fastBits; j += 1 << l {
				d.fast[j] = e
			}
			code++
			idx++
		}
		code <<= 1
	}
	return d, true
}

// decode reads one symbol. ok=false with truncated=true means the input ended;
// ok=false otherwise means an invalid code (empty or unreachable).
func (d *decoder) decode(br *bitReader) (sym int, err error) {
	br.fill()
	if e := d.fast[br.acc&(1<<fastBits-1)]; e != 0 {
		l := uint(e & 15)
		if l > br.nacc {
			return 0, ErrTruncated
		}
		br.acc >>= l
		br.nacc -= l
		return int(e >> 4), nil
	}
	if d.empty {
		return 0, corrupt(br.bitPos(), "symbol from empty code")
	}
	// slow path: canonical decode, one bit at a time
	code, first, index := 0, 0, 0
	acc := br.acc
	for l := uint(1); l <= maxCodeLen; l++ {
		if l > br.nacc {
			return 0, ErrTruncated
		}
		code |= int(acc & 1)
		acc >>= 1
		c := int(d.count[l])
		if code-c < first {
			br.acc >>= l
			br.nacc -= l
			return int(d.sym[index+(code-first)]), nil
		}
		index += c
		first += c
		first <<= 1
		code <<= 1
	}
	return 0, corrupt(br.bitPos(), "invalid Huffman code")
}

// encoder maps symbols to LSB-first-ready canonical codes.
type encoder struct {
	code []uint16 // bit-reversed canonical code
	len  []uint8
}

func buildEncoder(lens []uint8) *encoder {
	var count [maxCodeLen + 1]int
	for _, l := range lens {
		if l != 0 {
			count[l]++
		}
	}
	// RFC1951 3.2.2: next_code[l] = (next_code[l-1] + count[l-1]) << 1
	var next [maxCodeLen + 2]int
	code := 0
	count[0] = 0
	for l := 1; l <= maxCodeLen; l++ {
		code = (code + count[l-1]) << 1
		next[l] = code
	}
	e := &encoder{code: make([]uint16, len(lens)), len: lens}
	for s, l := range lens {
		if l != 0 {
			e.code[s] = bits.Reverse16(uint16(next[l])) >> (16 - l)
			next[l]++
		}
	}
	return e
}

var (
	fixedOnce sync.Once
	fixedLit  *decoder
	fixedDist *decoder
	fixedLitL []uint8
	fixedDstL []uint8
	fixedLitE *encoder
	fixedDstE *encoder
)

func initFixed() {
	fixedOnce.Do(func() {
		fixedLitL = make([]uint8, 288)
		for i := range fixedLitL {
			switch {
			case i < 144:
				fixedLitL[i] = 8
			case i < 256:
				fixedLitL[i] = 9
			case i < 280:
				fixedLitL[i] = 7
			default:
				fixedLitL[i] = 8
			}
		}
		fixedDstL = make([]uint8, 32)
		for i := range fixedDstL {
			fixedDstL[i] = 5
		}
		fixedLit, _ = buildDecoder(fixedLitL)
		fixedDist, _ = buildDecoder(fixedDstL)
		fixedLitE = buildEncoder(fixedLitL)
		fixedDstE = buildEncoder(fixedDstL)
	})
}
