package numcore

import (
	"encoding/binary"
	"math"
)

// Deterministic element arithmetic. Floating-point is used only to form a prediction; every
// prediction is mapped to a bit pattern and the residual is an exact bijective difference of
// order-preserving integer keys, so any prediction (including NaN-derived ones) is exact.
// Explicit float64() conversions forbid fused multiply-add on every architecture.

func LE(b []byte, w int, i int) uint32 {
	switch w {
	case 4:
		return binary.LittleEndian.Uint32(b[4*i:])
	case 2:
		return uint32(binary.LittleEndian.Uint16(b[2*i:]))
	}
	return uint32(b[i])
}

func PutLE(b []byte, w int, i int, v uint32) {
	switch w {
	case 4:
		binary.LittleEndian.PutUint32(b[4*i:], v)
	case 2:
		binary.LittleEndian.PutUint16(b[2*i:], uint16(v))
	default:
		b[i] = byte(v)
	}
}

func Mask(w int) uint32 {
	if w >= 4 {
		return 0xFFFFFFFF
	}
	return 1<<(8*uint(w)) - 1
}

// key maps a bit pattern to an unsigned integer that is monotone in the float order (negative
// values below positive ones); it is a bijection on w-byte patterns.
func Key(bits uint32, w int) uint32 {
	s := uint32(1) << (8*uint(w) - 1)
	if bits&s != 0 {
		return (^bits) & Mask(w)
	}
	return bits | s
}

func Unkey(k uint32, w int) uint32 {
	s := uint32(1) << (8*uint(w) - 1)
	if k&s != 0 {
		return k &^ s
	}
	return (^k) & Mask(w)
}

func Zigzag(d uint32, w int) uint32 { // d is a w-byte two's complement difference
	if w >= 4 {
		x := int32(d)
		return uint32(x<<1) ^ uint32(x>>31)
	}
	x := int32(d<<(32-8*uint(w))) >> (32 - 8*uint(w)) // sign-extend
	return (uint32(x<<1) ^ uint32(x>>31)) & Mask(w)
}

func Unzigzag(z uint32, w int) uint32 {
	x := int32(z>>1) ^ -int32(z&1)
	return uint32(x) & Mask(w)
}

// f16ToF64 / bf16 conversions (exact).
func F16ToF64(h uint32) float64 {
	s := (h >> 15) & 1
	e := (h >> 10) & 0x1f
	m := h & 0x3ff
	var v float64
	switch {
	case e == 0:
		v = math.Ldexp(float64(m), -24)
	case e == 31:
		if m == 0 {
			v = math.Inf(1)
		} else {
			v = math.NaN()
		}
	default:
		v = math.Ldexp(float64(m|0x400), int(e)-25)
	}
	if s != 0 {
		v = -v
	}
	return v
}

// f64ToF16 rounds to nearest even; non-finite or overflowing values map to inf/NaN patterns.
func F64ToF16(x float64) uint32 {
	if x != x {
		return 0x7e00
	}
	var s uint32
	if math.Signbit(x) {
		s, x = 0x8000, -x
	}
	if math.IsInf(x, 0) || x >= 65520 {
		return s | 0x7c00
	}
	if x < math.Ldexp(1, -24)/2 || x == 0 {
		return s
	}
	fr, ex := math.Frexp(x) // x = fr * 2^ex, fr in [0.5,1)
	e := ex + 14            // biased exponent for a normal number
	if e <= 0 {             // subnormal: scale to units of 2^-24
		m := roundEven(math.Ldexp(x, 24))
		return s | uint32(m)
	}
	m := roundEven(math.Ldexp(fr, 11)) // 1024..2048
	if m == 2048 {
		m, e = 1024, e+1
	}
	if e >= 31 {
		return s | 0x7c00
	}
	return s | uint32(e)<<10 | uint32(m-1024)
}

func roundEven(x float64) uint64 { return uint64(math.RoundToEven(x)) }

func F64ToBF16(x float64) uint32 {
	f := float32(x)
	b := math.Float32bits(f)
	if f != f {
		return 0x7fc0
	}
	r := b + 0x7fff + ((b >> 16) & 1)
	return r >> 16
}

// toF64 decodes bits of dtype to float64 (exact).
func ToF64(d DType, bits uint32) float64 {
	switch d {
	case DF32:
		return float64(math.Float32frombits(bits))
	case DBF16:
		return float64(math.Float32frombits(bits << 16))
	}
	return F16ToF64(bits)
}

// fromF64 rounds a float64 prediction to dtype bits.
func FromF64(d DType, x float64) uint32 {
	switch d {
	case DF32:
		return math.Float32bits(float32(x))
	case DBF16:
		return F64ToBF16(x)
	}
	return F64ToF16(x)
}

func Finite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// Narrow converts an F32 payload to F16 or BF16 words when every element round-trips bit
// exactly (widen(narrow(x)) == x); ok=false otherwise. NaN payloads that do not survive make a
// tensor non-narrowable rather than approximate.
func Narrow(src []byte, to DType) (dst []byte, ok bool) {
	n := len(src) / 4
	dst = make([]byte, 2*n)
	for i := 0; i < n; i++ {
		b := binary.LittleEndian.Uint32(src[4*i:])
		var h uint32
		if to == DBF16 {
			if b&0xffff != 0 {
				return nil, false
			}
			h = b >> 16
		} else {
			x := math.Float32frombits(b)
			h = F64ToF16(float64(x))
			if math.Float32bits(float32(F16ToF64(h))) != b {
				return nil, false
			}
		}
		binary.LittleEndian.PutUint16(dst[2*i:], uint16(h))
	}
	return dst, true
}

// Widen is the exact inverse of Narrow.
func Widen(src []byte, from DType) []byte {
	n := len(src) / 2
	dst := make([]byte, 4*n)
	for i := 0; i < n; i++ {
		h := uint32(binary.LittleEndian.Uint16(src[2*i:]))
		var b uint32
		if from == DBF16 {
			b = h << 16
		} else {
			b = math.Float32bits(float32(F16ToF64(h)))
		}
		binary.LittleEndian.PutUint32(dst[4*i:], b)
	}
	return dst
}

// BF16Bits rounds a float64 to bfloat16 bits (round to nearest even via float32).
func BF16Bits(x float64) uint32 { return F64ToBF16(x) }

// f32ToF16 rounds float32 bits to nearest-even half precision using integer arithmetic only.
func F32ToF16(b uint32) uint32 {
	sign := (b >> 16) & 0x8000
	exp := int(b>>23) & 0xff
	man := b & 0x7fffff
	if exp == 255 {
		if man == 0 {
			return sign | 0x7c00
		}
		return sign | 0x7e00 | man>>13
	}
	e := exp - 127 + 15
	if e >= 31 {
		return sign | 0x7c00
	}
	if e <= 0 {
		if e < -10 {
			return sign
		}
		man |= 0x800000
		shift := uint(14 - e)
		half := man >> shift
		rem := man & (1<<shift - 1)
		mid := uint32(1) << (shift - 1)
		if rem > mid || (rem == mid && half&1 == 1) {
			half++
		}
		return sign | half
	}
	half := uint32(e)<<10 | man>>13
	rem := man & 0x1fff
	if rem > 0x1000 || (rem == 0x1000 && half&1 == 1) {
		half++
	}
	return sign | half
}

// f16ToF32 widens half-precision bits exactly with integer arithmetic.
func F16ToF32(h uint32) uint32 {
	sign := (h & 0x8000) << 16
	exp := (h >> 10) & 0x1f
	man := h & 0x3ff
	switch {
	case exp == 0:
		if man == 0 {
			return sign
		}
		e := uint32(127 - 15 + 1)
		for man&0x400 == 0 {
			man <<= 1
			e--
		}
		return sign | e<<23 | (man&0x3ff)<<13
	case exp == 31:
		return sign | 0x7f800000 | man<<13
	}
	return sign | (exp+127-15)<<23 | man<<13
}

func F32ToBF16(b uint32) uint32 {
	if b&0x7fffffff > 0x7f800000 {
		return b>>16 | 0x40
	}
	return (b + 0x7fff + (b >> 16 & 1)) >> 16
}
