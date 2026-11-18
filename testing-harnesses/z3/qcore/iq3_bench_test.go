package qcore

import (
	"math"
	"math/rand"
	"testing"
)

func BenchmarkIQ3S(b *testing.B) {
	r := rand.New(rand.NewSource(1))
	const rows, cols = 64, 1024
	x := make([]float32, rows*cols)
	for i := range x {
		x[i] = float32(r.NormFloat64() * 0.03)
	}
	qw := make([]float32, cols)
	for i := range qw {
		qw[i] = float32(math.Exp(r.NormFloat64()))
	}
	n, _ := OutLen(IQ3_S, len(x), cols)
	dst := make([]byte, n)
	b.SetBytes(int64(len(x)) * 4)
	for i := 0; i < b.N; i++ {
		Quantize(dst, IQ3_S, x, cols, qw, GCCFMA)
	}
}
