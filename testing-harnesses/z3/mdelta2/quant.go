package mdelta2

import (
	"encoding/binary"
	"math"

	"zeros3-z3/qcore"
)

// ggml block layouts used by the quantized lane. Field order within a block is the on-disk order.
type qfield struct{ off, n int }

type qgeom struct {
	elems, bytes int
	fields       []qfield // d, [m|dmin], [scales], [qh], qs ...
}

var qgeoms = map[DType]qgeom{
	DQ8_0: {32, 34, []qfield{{0, 2}, {2, 32}}},
	DQ4_0: {32, 18, []qfield{{0, 2}, {2, 16}}},
	DQ4_1: {32, 20, []qfield{{0, 2}, {2, 2}, {4, 16}}},
	DQ5_0: {32, 22, []qfield{{0, 2}, {2, 4}, {6, 16}}},
	DQ5_1: {32, 24, []qfield{{0, 2}, {2, 2}, {4, 4}, {8, 16}}},
	DQ4_K: {256, 144, []qfield{{0, 2}, {2, 2}, {4, 12}, {16, 128}}},
	DQ5_K: {256, 176, []qfield{{0, 2}, {2, 2}, {4, 12}, {16, 32}, {48, 128}}},
	DQ6_K: {256, 210, []qfield{{208, 2}, {192, 16}, {128, 64}, {0, 128}}},
}

// SplitFields returns one stream per block field (field-major), the layout a typed coder sees.
func SplitFields(d DType, data []byte) [][]byte {
	g := qgeoms[d]
	nb := len(data) / g.bytes
	out := make([][]byte, len(g.fields))
	for fi, f := range g.fields {
		s := make([]byte, 0, nb*f.n)
		for b := 0; b < nb; b++ {
			s = append(s, data[b*g.bytes+f.off:b*g.bytes+f.off+f.n]...)
		}
		out[fi] = s
	}
	return out
}

// JoinFields inverts SplitFields.
func JoinFields(d DType, fs [][]byte, nb int) []byte {
	g := qgeoms[d]
	out := make([]byte, nb*g.bytes)
	for fi, f := range g.fields {
		for b := 0; b < nb; b++ {
			copy(out[b*g.bytes+f.off:], fs[fi][b*f.n:(b+1)*f.n])
		}
	}
	return out
}

func f16bits(x float32) uint16 { return uint16(f64ToF16(float64(x))) }
func f16val(h uint16) float32  { return float32(f16ToF64(uint32(h))) }

func roundAway(x float32) float32 { // C roundf: half away from zero
	if x < 0 {
		return -float32(math.Floor(float64(-x) + 0.5))
	}
	return float32(math.Floor(float64(x) + 0.5))
}

// FloatSource converts tensor payload elements to float32 (the quantizer input).
func FloatsOf(dt DType, data []byte, viaF16 bool) []float32 {
	w := dt.Width()
	n := len(data) / w
	out := make([]float32, n)
	for i := 0; i < n; i++ {
		v := float32(toF64(dt, le(data, w, i)))
		if viaF16 {
			v = f16val(f16bits(v))
		}
		out[i] = v
	}
	return out
}

var qcoreType = map[DType]qcore.Type{DQ8_0: qcore.Q8_0, DQ4_0: qcore.Q4_0, DQ4_1: qcore.Q4_1, DQ5_0: qcore.Q5_0, DQ5_1: qcore.Q5_1,
	DQ4_K: qcore.Q4_K, DQ5_K: qcore.Q5_K, DQ6_K: qcore.Q6_K}

// QuantizeAny reproduces ggml's reference (non-imatrix) quantizer through qcore; ok=false for
// types without a port or a length that is not whole blocks.
func QuantizeAny(t DType, x []float32) ([]byte, bool) {
	qt, has := qcoreType[t]
	if !has {
		return nil, false
	}
	el, by := qcore.Block(qt)
	if len(x)%el != 0 {
		return nil, false
	}
	out := make([]byte, len(x)/el*by)
	if qcore.Quantize(out, qt, x, el, nil, qcore.Strict) != nil {
		return nil, false
	}
	return out, true
}

// Quantize is QuantizeAny restricted to the simple (non-K) block types.
func Quantize(t DType, x []float32) ([]byte, bool) {
	if t == DQ4_K || t == DQ5_K || t == DQ6_K {
		return nil, false
	}
	return QuantizeAny(t, x)
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Dequantize8 returns the float32 values of Q8_0 blocks (for the rescale predictor).
func q8vals(blk []byte) (d float32, q []int8) {
	d = f16val(binary.LittleEndian.Uint16(blk))
	q = make([]int8, 32)
	for i := range q {
		q[i] = int8(blk[2+i])
	}
	return
}

// Q8Rescale predicts target Q8_0 payload from base Q8_0 payload and the target's scale field:
// q_t = clamp(round(q_b * d_b / d_t)). tScales are the target d fields (2 bytes per block).
func Q8Rescale(base []byte, tScales []byte) []byte {
	nb := len(base) / 34
	out := make([]byte, nb*32)
	for b := 0; b < nb; b++ {
		bd, bq := q8vals(base[b*34 : (b+1)*34])
		td := f16val(binary.LittleEndian.Uint16(tScales[2*b:]))
		for j := 0; j < 32; j++ {
			var q float32
			if td != 0 {
				q = roundAway(float32(bq[j]) * bd / td)
			}
			if q > 127 {
				q = 127
			}
			if q < -127 {
				q = -127
			}
			out[b*32+j] = byte(int8(q))
		}
	}
	return out
}

// BlockBytes is the byte size of one block of a quantized dtype (0 if unknown).
func BlockBytes(d DType) int { return qgeoms[d].bytes }
