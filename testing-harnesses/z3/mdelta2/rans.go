package mdelta2

import (
	"encoding/binary"
	"errors"
	"math/bits"
)

// Static-table byte-renormalized rANS with two interleaved states.
const (
	ransBits = 12
	ransM    = 1 << ransBits
	ransL    = 1 << 23
)

var ErrCorrupt = errors.New("mdelta2: corrupt stream")

type ransTab struct {
	freq  []uint16 // per symbol
	start []uint16
	dec   []uint16 // slot -> symbol (ransM entries)
}

// normFreqs scales counts to total ransM keeping every present symbol >= 1.
func normFreqs(cnt []uint32) []uint16 {
	var tot uint64
	for _, c := range cnt {
		tot += uint64(c)
	}
	f := make([]uint16, len(cnt))
	if tot == 0 {
		return f
	}
	sum := 0
	big := 0
	for i, c := range cnt {
		if c == 0 {
			continue
		}
		v := int(uint64(c) * ransM / tot)
		if v < 1 {
			v = 1
		}
		f[i] = uint16(v)
		sum += v
		if cnt[i] > cnt[big] {
			big = i
		}
	}
	d := ransM - sum
	if d >= 0 {
		f[big] += uint16(d)
		return f
	}
	// over-allocated (many rare symbols): shave from the largest repeatedly
	for d < 0 {
		m := 0
		for i := range f {
			if f[i] > f[m] {
				m = i
			}
		}
		if f[m] <= 1 {
			break
		}
		t := -d
		if int(f[m])-1 < t {
			t = int(f[m]) - 1
		}
		f[m] -= uint16(t)
		d += t
	}
	return f
}

func newTab(freq []uint16) (*ransTab, error) {
	t := &ransTab{freq: freq, start: make([]uint16, len(freq)+1), dec: make([]uint16, ransM)}
	s := 0
	for i, f := range freq {
		t.start[i] = uint16(s)
		for j := 0; j < int(f); j++ {
			if s+j >= ransM {
				return nil, ErrCorrupt
			}
			t.dec[s+j] = uint16(i)
		}
		s += int(f)
	}
	if s != ransM && s != 0 {
		return nil, ErrCorrupt
	}
	t.start[len(freq)] = uint16(s)
	return t, nil
}

func appendFreqs(b []byte, f []uint16) []byte {
	nz := 0
	for _, v := range f {
		if v > 0 {
			nz++
		}
	}
	b = binary.AppendUvarint(b, uint64(nz))
	prev := 0
	for i, v := range f {
		if v > 0 {
			b = binary.AppendUvarint(b, uint64(i-prev))
			b = binary.AppendUvarint(b, uint64(v))
			prev = i
		}
	}
	return b
}

func readFreqs(b []byte, nsym int) ([]uint16, int, error) {
	f := make([]uint16, nsym)
	pos := 0
	nz, k := binary.Uvarint(b)
	if k <= 0 || nz > uint64(nsym) {
		return nil, 0, ErrCorrupt
	}
	pos += k
	sym := 0
	for i := uint64(0); i < nz; i++ {
		d, k1 := binary.Uvarint(b[pos:])
		if k1 <= 0 {
			return nil, 0, ErrCorrupt
		}
		pos += k1
		v, k2 := binary.Uvarint(b[pos:])
		if k2 <= 0 || v == 0 || v > ransM {
			return nil, 0, ErrCorrupt
		}
		pos += k2
		sym += int(d)
		if sym >= nsym {
			return nil, 0, ErrCorrupt
		}
		f[sym] = uint16(v)
	}
	return f, pos, nil
}

// ransEnc encodes (ctx,sym) pairs in order given reversed by the caller; two lanes by index parity.
type ransEnc struct {
	out [2][]byte
	x   [2]uint32
}

func newRansEnc() *ransEnc { e := &ransEnc{}; e.x = [2]uint32{ransL, ransL}; return e }

func (e *ransEnc) put(lane int, f, s uint32) {
	x := e.x[lane]
	xmax := ((ransL >> ransBits) << 8) * f
	for x >= xmax {
		e.out[lane] = append(e.out[lane], byte(x))
		x >>= 8
	}
	e.x[lane] = (x/f)<<ransBits + x%f + s
}

// finish returns lane streams concatenated: [len0 uvarint][bytes0 reversed][state0][bytes1 reversed][state1].
func (e *ransEnc) finish() []byte {
	var b []byte
	for l := 0; l < 2; l++ {
		x := e.x[l]
		s := append([]byte(nil), e.out[l]...)
		s = append(s, byte(x), byte(x>>8), byte(x>>16), byte(x>>24))
		for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
			s[i], s[j] = s[j], s[i]
		}
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	return b
}

type ransDec struct {
	buf [2][]byte
	pos [2]int
	x   [2]uint32
}

func newRansDec(b []byte) (*ransDec, int, error) {
	d := &ransDec{}
	p := 0
	for l := 0; l < 2; l++ {
		n, k := binary.Uvarint(b[p:])
		if k <= 0 || n < 4 || uint64(len(b)-p-k) < n {
			return nil, 0, ErrCorrupt
		}
		p += k
		d.buf[l] = b[p : p+int(n)]
		p += int(n)
		s := d.buf[l]
		d.x[l] = uint32(s[0])<<24 | uint32(s[1])<<16 | uint32(s[2])<<8 | uint32(s[3])
		d.pos[l] = 4
	}
	return d, p, nil
}

func (d *ransDec) get(lane int, t *ransTab) (int, error) {
	x := d.x[lane]
	slot := x & (ransM - 1)
	s := int(t.dec[slot])
	x = uint32(t.freq[s])*(x>>ransBits) + slot - uint32(t.start[s])
	for x < ransL {
		if d.pos[lane] >= len(d.buf[lane]) {
			return 0, ErrCorrupt
		}
		x = x<<8 | uint32(d.buf[lane][d.pos[lane]])
		d.pos[lane]++
	}
	d.x[lane] = x
	return s, nil
}

func bitlen(v uint32) int { return bits.Len32(v) }
