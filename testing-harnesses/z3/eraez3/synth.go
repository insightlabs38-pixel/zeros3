package eraez3

import (
	"bytes"
	"crypto/sha256"
	"math/rand/v2"
)

func rlpBytes(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		return b
	}
	return append(rlpLen(0x80, len(b)), b...)
}

func rlpLen(base byte, n int) []byte {
	if n < 56 {
		return []byte{base + byte(n)}
	}
	if n < 256 {
		return []byte{base + 55 + 1, byte(n)}
	}
	return []byte{base + 55 + 2, byte(n >> 8), byte(n)}
}

func RlpList(items ...[]byte) []byte {
	var p []byte
	for _, it := range items {
		p = append(p, it...)
	}
	return append(rlpLen(0xc0, len(p)), p...)
}

// SynthBlocks builds n blocks with header-like structure (parent hash chain, repeated
// roots, small varying numbers); every 5th block has a transaction.
func SynthBlocks(seed uint64, first uint64, n int) []Block {
	rng := rand.New(rand.NewPCG(seed, 9))
	var out []Block
	parent := make([]byte, 32)
	rng.Uint64()
	for i := 0; i < n; i++ {
		num := first + uint64(i)
		h := RlpList(rlpBytes(parent), rlpBytes(bytes.Repeat([]byte{0x1d}, 32)), rlpBytes(make([]byte, 20)), rlpBytes(sha256Of(num)), rlpBytes(bytes.Repeat([]byte{0x56}, 32)),
			rlpBytes([]byte{0x01, byte(num >> 8), byte(num)}), rlpBytes([]byte{byte(rng.Uint32())}), rlpBytes(nil))
		parent = sha256Of(num + 1<<40)
		b := Block{Header: h, Body: RlpList(RlpList(), RlpList()), Receipts: RlpList(), TD: make([]byte, 32)}
		b.TD[31], b.TD[30] = byte(num), byte(num>>8)
		if i%5 == 4 {
			b.Body = RlpList(RlpList(rlpBytes(sha256Of(num+7))), RlpList())
			b.Receipts = RlpList(RlpList(rlpBytes([]byte{2}), rlpBytes([]byte{1}), rlpBytes([]byte{0x52, 0x08}), RlpList()))
		}
		out = append(out, b)
	}
	return out
}

func sha256Of(n uint64) []byte {
	var b [8]byte
	for i := range b {
		b[i] = byte(n >> (8 * i))
	}
	s := sha256.Sum256(b[:])
	return s[:]
}
