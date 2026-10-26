package zstdz3

import (
	"encoding/binary"
	"math/bits"
)

// revReader reads a Zstandard backward bitstream. The last byte carries a 1-bit end
// marker above the data bits; reading starts at the highest data bit and moves down,
// so the first value read is the last one written.
type revReader struct {
	b   []byte
	pos int // data bits not yet read: bits [0,pos) remain
}

func newRevReader(b []byte) (*revReader, error) {
	if len(b) == 0 {
		return nil, corrupt("empty bitstream")
	}
	last := b[len(b)-1]
	if last == 0 {
		return nil, corrupt("bitstream without end marker")
	}
	return &revReader{b: b, pos: (len(b)-1)*8 + bits.Len8(last) - 1}, nil
}

func (r *revReader) load(i int) uint64 {
	if i+8 <= len(r.b) {
		return binary.LittleEndian.Uint64(r.b[i:])
	}
	var v uint64
	for k := 0; k < 8 && i+k < len(r.b); k++ {
		v |= uint64(r.b[i+k]) << (8 * k)
	}
	return v
}

// get returns bits [pos-n,pos) with the caller guaranteeing 0 <= n <= 32, n <= pos.
func (r *revReader) get(n int) uint32 {
	if n == 0 {
		return 0
	}
	s := r.pos - n
	return uint32(r.load(s>>3)>>(uint(s)&7)) & (1<<uint(n) - 1)
}

// read consumes n bits (n <= 32); ok is false if fewer remain.
func (r *revReader) read(n int) (uint32, bool) {
	if n > r.pos {
		return 0, false
	}
	v := r.get(n)
	r.pos -= n
	return v, true
}

// peek returns the next n bits (n <= 32) without consuming, zero-extended on the right
// past the beginning of the stream (Huffman lookups near the end).
func (r *revReader) peek(n int) uint32 {
	if n <= r.pos {
		return r.get(n)
	}
	return r.get(r.pos) << uint(n-r.pos)
}

func (r *revReader) skip(n int) bool {
	if n > r.pos {
		return false
	}
	r.pos -= n
	return true
}

// revWriter builds a backward bitstream: values are appended least-significant-bit
// first and finish adds the end marker and zero padding.
type revWriter struct {
	buf []byte
	acc uint64
	n   int
}

func (w *revWriter) write(v uint32, n int) {
	if n == 0 {
		return
	}
	w.acc |= uint64(v) << uint(w.n)
	w.n += n
	for w.n >= 8 {
		w.buf = append(w.buf, byte(w.acc))
		w.acc >>= 8
		w.n -= 8
	}
}

func (w *revWriter) finish() []byte {
	w.write(1, 1)
	if w.n > 0 {
		w.buf = append(w.buf, byte(w.acc))
		w.acc, w.n = 0, 0
	}
	return w.buf
}

// fwdReader reads an LSB-first forward bitstream (the FSE table description).
type fwdReader struct {
	b   []byte
	pos int // bits consumed
}

func (r *fwdReader) read(n int) (uint32, bool) {
	if r.pos+n > len(r.b)*8 {
		return 0, false
	}
	var v uint64
	for k, i := 0, r.pos>>3; k < 6 && i+k < len(r.b); k++ {
		v |= uint64(r.b[i+k]) << (8 * k)
	}
	v = v >> (uint(r.pos) & 7) & (1<<uint(n) - 1)
	r.pos += n
	return uint32(v), true
}

func (r *fwdReader) bytes() int { return (r.pos + 7) >> 3 }
