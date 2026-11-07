package lz4z3

import (
	"encoding/binary"
	"math/bits"
)

const (
	p32a = 2654435761
	p32b = 2246822519
	p32c = 3266489917
	p32d = 668265263
	p32e = 374761393
)

// XXH32 is the xxHash 32-bit digest (LZ4 frame header, block and content checksums).
func XXH32(b []byte, seed uint32) uint32 {
	n := len(b)
	var h uint32
	if n >= 16 {
		v1, v2, v3, v4 := seed+p32a+p32b, seed+p32b, seed, seed-p32a
		for len(b) >= 16 {
			v1 = bits.RotateLeft32(v1+binary.LittleEndian.Uint32(b)*p32b, 13) * p32a
			v2 = bits.RotateLeft32(v2+binary.LittleEndian.Uint32(b[4:])*p32b, 13) * p32a
			v3 = bits.RotateLeft32(v3+binary.LittleEndian.Uint32(b[8:])*p32b, 13) * p32a
			v4 = bits.RotateLeft32(v4+binary.LittleEndian.Uint32(b[12:])*p32b, 13) * p32a
			b = b[16:]
		}
		h = bits.RotateLeft32(v1, 1) + bits.RotateLeft32(v2, 7) + bits.RotateLeft32(v3, 12) + bits.RotateLeft32(v4, 18)
	} else {
		h = seed + p32e
	}
	h += uint32(n)
	for ; len(b) >= 4; b = b[4:] {
		h = bits.RotateLeft32(h+binary.LittleEndian.Uint32(b)*p32c, 17) * p32d
	}
	for _, c := range b {
		h = bits.RotateLeft32(h+uint32(c)*p32e, 11) * p32a
	}
	h ^= h >> 15
	h *= p32b
	h ^= h >> 13
	h *= p32c
	h ^= h >> 16
	return h
}
