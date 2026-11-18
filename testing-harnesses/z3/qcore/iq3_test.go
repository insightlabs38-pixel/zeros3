package qcore

import (
	"math"
	"math/rand"
	"testing"
)

func iq3Data(rows, cols int) ([]float32, []float32) {
	r := rand.New(rand.NewSource(1))
	x := make([]float32, rows*cols)
	for i := range x {
		x[i] = float32(r.NormFloat64() * 0.03)
	}
	qw := make([]float32, cols)
	for i := range qw {
		qw[i] = float32(math.Exp(r.NormFloat64()))
	}
	return x, qw
}

// Inputs the producer aborts on (the conformance vectors "extreme" and "nonfinite", see golden.txt) are an error of
// the port, not a panic, so a derivation attempt over such a tensor fails cleanly.
func TestIQ3SAbortIsAnError(t *testing.T) {
	n := 0
	for _, v := range vectors() {
		if v.t != IQ3_S || v.name != "IQ3_S/extreme" && v.name != "IQ3_S/nonfinite" {
			continue
		}
		out, _ := OutLen(IQ3_S, len(v.x), v.npr)
		if err := Quantize(make([]byte, out), IQ3_S, v.x, v.npr, nil, Strict); err != ErrAbort {
			t.Fatalf("%s: %v", v.name, err)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("%d vectors", n)
	}
}

func BenchmarkIQ3S(b *testing.B) {
	x, qw := iq3Data(64, 1024)
	n, _ := OutLen(IQ3_S, len(x), 1024)
	dst := make([]byte, n)
	b.SetBytes(int64(len(x)) * 4)
	for i := 0; i < b.N; i++ {
		Quantize(dst, IQ3_S, x, 1024, qw, GCCFMA)
	}
}
