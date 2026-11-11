package semcore

import (
	"encoding/binary"
	"math"
)

// Sig is a layout-invariant magnitude signature (sum and sum of squares of the dequantized
// values). It only ranks candidates; it never establishes equality.
type Sig struct{ L1, L2 float64 }

// SigOf computes the signature of a typed payload; ok is false for non-numeric types.
func SigOf(dt int, b []byte) (Sig, bool) {
	var s Sig
	add := func(x float64) {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return
		}
		s.L1 += math.Abs(x)
		s.L2 += x * x
	}
	switch dt {
	case DF16, DBF16, DF32:
		w := ElemBytes(dt)
		for i := 0; i+w <= len(b); i += w {
			f, _ := toF32bits(b, dt, i/w)
			add(float64(math.Float32frombits(f)))
		}
	case DF64:
		for i := 0; i+8 <= len(b); i += 8 {
			add(math.Float64frombits(binary.LittleEndian.Uint64(b[i:])))
		}
	case DQBase + 8:
		for i := 0; i+34 <= len(b); i += 34 {
			d := float64(math.Float32frombits(f16to32(binary.LittleEndian.Uint16(b[i:]))))
			for j := 0; j < 32; j++ {
				add(d * float64(int8(b[i+2+j])))
			}
		}
	case DQBase + 2:
		for i := 0; i+18 <= len(b); i += 18 {
			d := float64(math.Float32frombits(f16to32(binary.LittleEndian.Uint16(b[i:]))))
			for j := 0; j < 16; j++ {
				add(d * float64(int(b[i+2+j]&15)-8))
				add(d * float64(int(b[i+2+j]>>4)-8))
			}
		}
	default:
		return s, false
	}
	return s, true
}

// SigDist is a symmetric relative distance between two signatures.
func SigDist(a, b Sig) float64 {
	rel := func(x, y float64) float64 {
		m := math.Max(math.Abs(x), math.Abs(y))
		if m == 0 {
			return 0
		}
		return math.Abs(x-y) / m
	}
	return rel(a.L1, b.L1) + rel(a.L2, b.L2)
}
