package brotliz3

import (
	"math"
	"testing"
)

func TestLog2IntCorrectlyRounded(t *testing.T) {
	for _, v := range log2Vectors {
		if got := math.Float64bits(log2Int(v[0])); got != v[1] {
			t.Fatalf("log2Int(%d) = %#x, want %#x", v[0], got, v[1])
		}
	}
	for i := 2; i < 256; i++ { // the reference table: float-precision constants
		if math.Float32bits(float32(log2Table[i])) != log2TableBits[i] {
			t.Fatalf("log2 table entry %d", i)
		}
	}
}
