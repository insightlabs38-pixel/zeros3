// Package qcore reproduces llama.cpp/ggml block quantizers bit-exactly (float32 semantics of the
// C reference, no fused multiply-add, x86 float->int conversion semantics). Standard library only.
//
// Scope: the scalar quantize_row_*_ref paths and the importance-weighted (imatrix) *_impl paths of
// ggml-quants.c for Q4_0 Q4_1 Q5_0 Q5_1 Q8_0 Q2_K Q3_K Q4_K Q5_K Q6_K IQ4_NL IQ4_XS. The output is a
// function of (type, values, row length, importance vector); callers verify it against the stored
// producer bytes before relying on it, so an unknown producer variant costs bytes, never correctness.
package qcore

import (
	"errors"
	"math"
)

// Type is the ggml tensor type id.
type Type uint8

const (
	Q4_0   Type = 2
	Q4_1   Type = 3
	Q5_0   Type = 6
	Q5_1   Type = 7
	Q8_0   Type = 8
	Q2_K   Type = 10
	Q3_K   Type = 11
	Q4_K   Type = 12
	Q5_K   Type = 13
	Q6_K   Type = 14
	IQ4_NL Type = 20
	IQ4_XS Type = 23
)

type geom struct {
	elems, bytes int
	name         string
}

var geoms = map[Type]geom{
	Q4_0: {32, 18, "Q4_0"}, Q4_1: {32, 20, "Q4_1"}, Q5_0: {32, 22, "Q5_0"}, Q5_1: {32, 24, "Q5_1"}, Q8_0: {32, 34, "Q8_0"},
	Q2_K: {256, 84, "Q2_K"}, Q3_K: {256, 110, "Q3_K"}, Q4_K: {256, 144, "Q4_K"}, Q5_K: {256, 176, "Q5_K"}, Q6_K: {256, 210, "Q6_K"},
	IQ4_NL: {32, 18, "IQ4_NL"}, IQ4_XS: {256, 136, "IQ4_XS"},
}

// Supported reports whether t has a quantizer here.
func Supported(t Type) bool { _, ok := geoms[t]; return ok }

// Block returns the elements and bytes of one block of t (0, 0 if unsupported).
func Block(t Type) (elems, bytes int) { g := geoms[t]; return g.elems, g.bytes }

func (t Type) String() string {
	if g, ok := geoms[t]; ok {
		return g.name
	}
	return "ggml?"
}

var (
	ErrType  = errors.New("qcore: unsupported quantization type")
	ErrShape = errors.New("qcore: row length is not a multiple of the block size")
	ErrSize  = errors.New("qcore: buffer size mismatch")
)

// OutLen is the encoded size of n elements in rows of nPerRow.
func OutLen(t Type, n, nPerRow int) (int, error) {
	g, ok := geoms[t]
	if !ok {
		return 0, ErrType
	}
	if nPerRow <= 0 || nPerRow%g.elems != 0 || n%nPerRow != 0 {
		return 0, ErrShape
	}
	if n/g.elems > math.MaxInt/g.bytes {
		return 0, ErrSize
	}
	return n / g.elems * g.bytes, nil
}

// Arith is the floating-point code generation of the producer build. The quantizers are float
// programs whose results depend on which multiply-adds the producer's compiler fused.
type Arith uint8

const (
	// Strict: no contraction, every operation rounded (ISO C; matches non-FMA producer builds).
	Strict Arith = 0
	// GCCFMA: GCC 13 -O3 with FMA and the default -ffp-contract=fast on x86-64 (generic tuning),
	// derived site by site from the compiler's GIMPLE for ggml-quants.c at llama.cpp b3991.
	// Only the importance-weighted paths are derived; the reference paths return ErrArith.
	GCCFMA Arith = 1
)

var ErrArith = errors.New("qcore: arithmetic profile not available for this quantizer path")

// Quantize writes ggml_quantize_chunk(t, x, rows of nPerRow, qw) into dst. qw is the per-column
// importance vector (len nPerRow) or nil for the reference (non-imatrix) quantizer.
func Quantize(dst []byte, t Type, x []float32, nPerRow int, qw []float32, ar Arith) error {
	n, err := OutLen(t, len(x), nPerRow)
	if err != nil {
		return err
	}
	if ar > GCCFMA || (ar == GCCFMA && (qw == nil || t == Q8_0)) {
		return ErrArith
	}
	fm := ar == GCCFMA
	if len(dst) != n || (qw != nil && len(qw) != nPerRow) {
		return ErrSize
	}
	g := geoms[t]
	rowBytes := nPerRow / g.elems * g.bytes
	for r := 0; r*nPerRow < len(x); r++ {
		row, y := x[r*nPerRow:(r+1)*nPerRow], dst[r*rowBytes:(r+1)*rowBytes]
		switch t {
		case Q4_0, Q5_0:
			if qw == nil {
				rowQ40Q50Ref(t, row, y)
			} else {
				rowQ40Q50Impl(fm, t, row, y, qw)
			}
		case Q4_1, Q5_1:
			if qw == nil {
				rowQ41Q51Ref(t, row, y)
			} else {
				rowQ41Q51Impl(fm, t, row, y, qw)
			}
		case Q8_0:
			rowQ80(row, y)
		case Q2_K:
			if qw == nil {
				rowQ2KRef(row, y)
			} else {
				rowQ2KImpl(fm, row, y, qw)
			}
		case Q3_K:
			if qw == nil {
				rowQ3KRef(row, y)
			} else {
				rowQ3KImpl(fm, row, y, qw)
			}
		case Q4_K:
			rowQ4Q5K(fm, row, y, qw, 15)
		case Q5_K:
			rowQ4Q5K(fm, row, y, qw, 31)
		case Q6_K:
			rowQ6K(fm, row, y, qw)
		case IQ4_NL:
			rowIQ4(fm, row, y, qw, 32)
		case IQ4_XS:
			rowIQ4(fm, row, y, qw, 256)
		}
	}
	return nil
}

// ---- float32 helpers (explicit conversions forbid fusion on every architecture) ----

func mul(a, b float32) float32 { return float32(a * b) }

// fma32 is the correctly rounded float32 a*b+c: the product is exact in float64; the sum is
// rounded to odd in float64 (TwoSum error decides), which makes the final rounding to float32 exact.
func fma32(a, b, c float32) float32 {
	p, q := float64(a)*float64(b), float64(c)
	s := p + q
	if s-s != 0 { // Inf/NaN: nothing to correct
		return float32(s)
	}
	bb := s - p
	e := (p - (s - bb)) + (q - bb) // exact error of p+q
	if e != 0 {
		u := math.Float64bits(s)
		if u&1 == 0 { // move to the odd neighbour on the side of the error
			if (e > 0) == (s > 0) {
				u++
			} else {
				u--
			}
			if s == 0 { // cannot happen (s == 0 implies e == 0); kept for safety
				u = math.Float64bits(e)
			}
			s = math.Float64frombits(u)
		}
	}
	return float32(s)
}

// madd is a*b+c: fused in the contracted build (fm), separately rounded otherwise.
func madd(fm bool, a, b, c float32) float32 {
	if fm {
		return fma32(a, b, c)
	}
	return float32(a*b) + c
}

// ni is nearest_int(a*b); the contracted build fuses the product into the magic-constant add.
func ni(fm bool, a, b float32) int32 {
	if fm {
		return int32(math.Float32bits(fma32(a, b, 12582912.0))&0x007fffff) - 0x00400000
	}
	return nearestInt(float32(a * b))
}
func fabs(x float32) float32 { return math.Float32frombits(math.Float32bits(x) &^ (1 << 31)) }
func sqrt(x float32) float32 { return float32(math.Sqrt(float64(x))) }

// nearestInt is ggml's nearest_int (round half to even through a float add).
func nearestInt(f float32) int32 {
	v := f + 12582912.0
	return int32(math.Float32bits(v)&0x007fffff) - 0x00400000
}

// cvt is the x86 float->int32 truncation (cvttss2si): NaN and out-of-range give MinInt32.
func cvt(v float32) int32 {
	if v != v || v >= 2147483648.0 || v < -2147483648.0 {
		return math.MinInt32
	}
	return int32(v)
}

func roundf(x float32) float32 { // C roundf: half away from zero
	return float32(math.Round(float64(x)))
}

func clamp(l, lo, hi int32) int32 {
	if l < lo {
		return lo
	}
	if l > hi {
		return hi
	}
	return l
}

func put16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }

// F16 is GGML_FP32_TO_FP16 (round to nearest even, IEEE semantics incl. NaN/Inf/subnormals).
func F16(x float32) uint16 {
	b := math.Float32bits(x)
	sign := uint16(b>>16) & 0x8000
	e := int(b>>23) & 0xff
	m := b & 0x7fffff
	switch {
	case e == 0xff:
		if m != 0 {
			return sign | 0x7e00 | uint16(m>>13) // quiet NaN, payload kept as F16C does
		}
		return sign | 0x7c00
	case e == 0 && m == 0:
		return sign
	}
	m |= 1 << 23
	if e == 0 { // float32 subnormal: below half of the smallest half subnormal
		return sign
	}
	he := e - 127 + 15
	var out uint32
	var shift uint
	if he >= 31 {
		return sign | 0x7c00
	} else if he <= 0 {
		if he < -10 {
			return sign
		}
		shift = uint(14 - he)
		out = m >> shift
	} else {
		shift = 13
		out = uint32(he)<<10 | (m>>13)&0x3ff
	}
	rem := m & (1<<shift - 1)
	half := uint32(1) << (shift - 1)
	if rem > half || (rem == half && out&1 == 1) {
		out++ // may carry into the exponent, which is the correct rounding
	}
	return sign | uint16(out)
}

// F16to32 is GGML_FP16_TO_FP32.
func F16to32(h uint16) float32 {
	s := uint32(h>>15) << 31
	e := uint32(h>>10) & 31
	m := uint32(h) & 1023
	switch {
	case e == 0:
		if m == 0 {
			return math.Float32frombits(s)
		}
		e = 1
		for m&1024 == 0 {
			m <<= 1
			e--
		}
		return math.Float32frombits(s | (e+112)<<23 | (m&1023)<<13)
	case e == 31:
		return math.Float32frombits(s | 0x7f800000 | m<<13)
	}
	return math.Float32frombits(s | (e+112)<<23 | m<<13)
}
