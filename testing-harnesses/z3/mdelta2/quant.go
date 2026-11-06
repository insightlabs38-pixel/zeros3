package mdelta2

import (
	"encoding/binary"
	"math"
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

// IsQuant reports a block-quantized dtype with a known geometry.
func (d DType) IsQuant() bool { _, ok := qgeoms[d]; return ok }

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

// Quantize reproduces ggml's reference row quantizer (quantize_row_*_ref) for the simple block types.
// ok=false for types without a port.
func Quantize(t DType, x []float32) ([]byte, bool) {
	g, has := qgeoms[t]
	if !has || len(x)%g.elems != 0 {
		return nil, false
	}
	nb := len(x) / g.elems
	out := make([]byte, nb*g.bytes)
	for b := 0; b < nb; b++ {
		blk := x[b*g.elems : (b+1)*g.elems]
		y := out[b*g.bytes : (b+1)*g.bytes]
		switch t {
		case DQ8_0:
			var amax float32
			for _, v := range blk {
				if a := float32(math.Abs(float64(v))); a > amax {
					amax = a
				}
			}
			d := amax / 127
			var id float32
			if d != 0 {
				id = 1 / d
			}
			binary.LittleEndian.PutUint16(y, f16bits(d))
			for j, v := range blk {
				y[2+j] = byte(int8(roundAway(v * id)))
			}
		case DQ4_0, DQ5_0:
			var amax, mx float32
			for _, v := range blk {
				if a := float32(math.Abs(float64(v))); a > amax {
					amax, mx = a, v
				}
			}
			div := float32(-8)
			if t == DQ5_0 {
				div = -16
			}
			d := mx / div
			var id float32
			if d != 0 {
				id = 1 / d
			}
			binary.LittleEndian.PutUint16(y, f16bits(d))
			if t == DQ4_0 {
				for j := 0; j < 16; j++ {
					x0, x1 := blk[j]*id, blk[16+j]*id
					xi0, xi1 := minI(15, int(int8(x0+8.5))), minI(15, int(int8(x1+8.5)))
					y[2+j] = byte(xi0 | xi1<<4)
				}
			} else {
				var qh uint32
				for j := 0; j < 16; j++ {
					x0, x1 := blk[j]*id, blk[16+j]*id
					xi0, xi1 := minI(31, int(int8(x0+16.5))), minI(31, int(int8(x1+16.5)))
					y[6+j] = byte(xi0&0xf | (xi1&0xf)<<4)
					qh |= uint32((xi0&0x10)>>4) << uint(j)
					qh |= uint32((xi1&0x10)>>4) << uint(j+16)
				}
				binary.LittleEndian.PutUint32(y[2:], qh)
			}
		case DQ4_1, DQ5_1:
			mn, mx := blk[0], blk[0]
			for _, v := range blk {
				if v < mn {
					mn = v
				}
				if v > mx {
					mx = v
				}
			}
			levels := float32(15)
			if t == DQ5_1 {
				levels = 31
			}
			d := (mx - mn) / levels
			var id float32
			if d != 0 {
				id = 1 / d
			}
			binary.LittleEndian.PutUint16(y, f16bits(d))
			binary.LittleEndian.PutUint16(y[2:], f16bits(mn))
			if t == DQ4_1 {
				for j := 0; j < 16; j++ {
					xi0, xi1 := minI(15, int((blk[j]-mn)*id+0.5)), minI(15, int((blk[16+j]-mn)*id+0.5))
					y[4+j] = byte(xi0 | xi1<<4)
				}
			} else {
				var qh uint32
				for j := 0; j < 16; j++ {
					xi0, xi1 := minI(31, int((blk[j]-mn)*id+0.5)), minI(31, int((blk[16+j]-mn)*id+0.5))
					y[8+j] = byte(xi0&0xf | (xi1&0xf)<<4)
					qh |= uint32((xi0&0x10)>>4) << uint(j)
					qh |= uint32((xi1&0x10)>>4) << uint(j+16)
				}
				binary.LittleEndian.PutUint32(y[4:], qh)
			}
		default:
			return nil, false
		}
	}
	return out, true
}

// QuantizeAny dispatches to the simple-type and K-quant ports.
func QuantizeAny(t DType, x []float32) ([]byte, bool) {
	switch t {
	case DQ6_K:
		if len(x)%256 == 0 {
			return quantQ6K(x), true
		}
	case DQ4_K:
		if len(x)%256 == 0 {
			return quantQ4K(x), true
		}
	}
	return Quantize(t, x)
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
