package zstdz3

import (
	"encoding/binary"
	"math/bits"
)

// XXH64 (seed 0), the hash behind the Zstandard content checksum (low 32 bits).
const (
	xp1 = 11400714785074694791
	xp2 = 14029467366897019727
	xp3 = 1609587929392839161
	xp4 = 9650029242287828579
	xp5 = 2870177450012600261
)

type xxh64 struct {
	v1, v2, v3, v4 uint64
	total          uint64
	mem            [32]byte
	n              int
}

func newXXH64() *xxh64 {
	x := &xxh64{}
	x.reset()
	return x
}

func (x *xxh64) reset() {
	m := uint64(xp1)
	x.v1, x.v2, x.v3, x.v4 = m+xp2, xp2, 0, -m
	x.total, x.n = 0, 0
}

func xround(acc, in uint64) uint64 { return bits.RotateLeft64(acc+in*xp2, 31) * xp1 }

func xmerge(h, v uint64) uint64 { return (h^xround(0, v))*xp1 + xp4 }

func (x *xxh64) write(p []byte) {
	x.total += uint64(len(p))
	if x.n+len(p) < 32 {
		x.n += copy(x.mem[x.n:], p)
		return
	}
	if x.n > 0 {
		k := copy(x.mem[x.n:], p)
		p = p[k:]
		x.stripe(x.mem[:])
		x.n = 0
	}
	for len(p) >= 32 {
		x.stripe(p[:32])
		p = p[32:]
	}
	x.n = copy(x.mem[:], p)
}

func (x *xxh64) stripe(b []byte) {
	x.v1 = xround(x.v1, binary.LittleEndian.Uint64(b))
	x.v2 = xround(x.v2, binary.LittleEndian.Uint64(b[8:]))
	x.v3 = xround(x.v3, binary.LittleEndian.Uint64(b[16:]))
	x.v4 = xround(x.v4, binary.LittleEndian.Uint64(b[24:]))
}

func (x *xxh64) sum() uint64 {
	var h uint64
	if x.total >= 32 {
		h = bits.RotateLeft64(x.v1, 1) + bits.RotateLeft64(x.v2, 7) + bits.RotateLeft64(x.v3, 12) + bits.RotateLeft64(x.v4, 18)
		h = xmerge(xmerge(xmerge(xmerge(h, x.v1), x.v2), x.v3), x.v4)
	} else {
		h = xp5
	}
	h += x.total
	b := x.mem[:x.n]
	for ; len(b) >= 8; b = b[8:] {
		h ^= xround(0, binary.LittleEndian.Uint64(b))
		h = bits.RotateLeft64(h, 27)*xp1 + xp4
	}
	if len(b) >= 4 {
		h ^= uint64(binary.LittleEndian.Uint32(b)) * xp1
		h = bits.RotateLeft64(h, 23)*xp2 + xp3
		b = b[4:]
	}
	for _, c := range b {
		h ^= uint64(c) * xp5
		h = bits.RotateLeft64(h, 11) * xp1
	}
	h ^= h >> 33
	h *= xp2
	h ^= h >> 29
	h *= xp3
	h ^= h >> 32
	return h
}
