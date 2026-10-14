package tdelta

import (
	"math/rand"
	"testing"
)

func benchPair() (base, tgt []byte) {
	r := rand.New(rand.NewSource(3))
	return pair(r, 4<<20, 2, 0.9)
}

func BenchmarkPXREncode(b *testing.B) {
	base, tgt := benchPair()
	b.SetBytes(int64(len(tgt)))
	for i := 0; i < b.N; i++ {
		EncodePXR(base, tgt, 2)
	}
}

func BenchmarkPXRDecode(b *testing.B) {
	base, tgt := benchPair()
	p, _ := EncodePXR(base, tgt, 2)
	b.SetBytes(int64(len(tgt)))
	for i := 0; i < b.N; i++ {
		DecodePXR(base, p, len(tgt), 2)
	}
}
