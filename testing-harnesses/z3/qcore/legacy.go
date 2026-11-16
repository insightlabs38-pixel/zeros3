package qcore

import "encoding/binary"

// quantize_row_q4_0_ref / q5_0_ref.
func rowQ40Q50Ref(t Type, x []float32, y []byte) {
	bs, div, off := 18, float32(-8), float32(8.5)
	if t == Q5_0 {
		bs, div, off = 22, -16, 16.5
	}
	for b := 0; b*32 < len(x); b++ {
		xb, yb := x[b*32:b*32+32], y[b*bs:(b+1)*bs]
		var amax, max float32
		for _, v := range xb {
			if a := fabs(v); amax < a {
				amax, max = a, v
			}
		}
		d := max / div
		var id float32
		if d != 0 {
			id = 1 / d
		}
		put16(yb, F16(d))
		if t == Q4_0 {
			for j := 0; j < 16; j++ {
				xi0 := minU8(15, int8(cvt(mul(xb[j], id)+off)))
				xi1 := minU8(15, int8(cvt(mul(xb[16+j], id)+off)))
				yb[2+j] = xi0 | xi1<<4
			}
			continue
		}
		var qh uint32
		for j := 0; j < 16; j++ {
			xi0 := minU8(31, int8(cvt(mul(xb[j], id)+off)))
			xi1 := minU8(31, int8(cvt(mul(xb[16+j], id)+off)))
			yb[6+j] = xi0&0xf | (xi1&0xf)<<4
			qh |= uint32(xi0&0x10) >> 4 << uint(j)
			qh |= uint32(xi1&0x10) >> 4 << uint(j+16)
		}
		binary.LittleEndian.PutUint32(yb[2:], qh)
	}
}

// minU8 is C's `uint8_t v = MIN(lim, (int8_t)expr)`: signed compare, then truncation to a byte.
func minU8(lim int8, v int8) byte {
	if v < lim {
		return byte(v)
	}
	return byte(lim)
}

// quantize_row_q4_1_ref / q5_1_ref.
func rowQ41Q51Ref(t Type, x []float32, y []byte) {
	bs, levels, lim := 20, float32(15), int8(15)
	if t == Q5_1 {
		bs, levels, lim = 24, 31, 31
	}
	for b := 0; b*32 < len(x); b++ {
		xb, yb := x[b*32:b*32+32], y[b*bs:(b+1)*bs]
		min, max := float32(3.4028234663852886e38), float32(-3.4028234663852886e38)
		for _, v := range xb {
			if v < min {
				min = v
			}
			if v > max {
				max = v
			}
		}
		d := (max - min) / levels
		var id float32
		if d != 0 {
			id = 1 / d
		}
		put16(yb, F16(d))
		put16(yb[2:], F16(min))
		if t == Q4_1 {
			for j := 0; j < 16; j++ {
				xi0 := minU8(lim, int8(cvt(mul(xb[j]-min, id)+0.5)))
				xi1 := minU8(lim, int8(cvt(mul(xb[16+j]-min, id)+0.5)))
				yb[4+j] = xi0 | xi1<<4
			}
			continue
		}
		var qh uint32
		for j := 0; j < 16; j++ {
			// q5_1_ref casts to uint8_t and does not clamp
			xi0 := byte(cvt(mul(xb[j]-min, id) + 0.5))
			xi1 := byte(cvt(mul(xb[16+j]-min, id) + 0.5))
			yb[8+j] = xi0&0xf | (xi1&0xf)<<4
			qh |= uint32(xi0&0x10) >> 4 << uint(j)
			qh |= uint32(xi1&0x10) >> 4 << uint(j+16)
		}
		binary.LittleEndian.PutUint32(yb[4:], qh)
	}
}

// quantize_row_q8_0_ref.
func rowQ80(x []float32, y []byte) {
	for b := 0; b*32 < len(x); b++ {
		xb, yb := x[b*32:b*32+32], y[b*34:(b+1)*34]
		var amax float32
		for _, v := range xb {
			if a := fabs(v); !(amax > a) { // amax = MAX(amax, fabsf(v)), NaN-faithful
				amax = a
			}
		}
		d := amax / 127
		var id float32
		if d != 0 {
			id = 1 / d
		}
		put16(yb, F16(d))
		for j, v := range xb {
			yb[2+j] = byte(int8(cvt(roundf(mul(v, id)))))
		}
	}
}

func rowSigma2(x []float32) float32 {
	var s float32
	for _, v := range x {
		s += mul(v, v)
	}
	return s / float32(len(x))
}

// quantize_row_q4_0_impl / q5_0_impl (importance-weighted).
func rowQ40Q50Impl(fm bool, t Type, x []float32, y []byte, qw []float32) {
	bs, nmax := 18, 8
	if t == Q5_0 {
		bs, nmax = 22, 16
	}
	sigma2 := rowSigma2(x)
	var weight [32]float32
	var L [32]int8
	for b := 0; b*32 < len(x); b++ {
		xb, yb := x[b*32:b*32+32], y[b*bs:(b+1)*bs]
		for j := range weight {
			weight[j] = mul(qw[b*32+j], sqrt(madd(fm, xb[j], xb[j], sigma2)))
		}
		d := makeQxQuants(fm, 32, nmax, xb, L[:], 1, weight[:])
		put16(yb, F16(d))
		if t == Q4_0 {
			for j := 0; j < 16; j++ {
				yb[2+j] = byte(L[j]) | byte(L[j+16])<<4
			}
			continue
		}
		var qh uint32
		for j := 0; j < 16; j++ {
			xi0, xi1 := byte(L[j]), byte(L[j+16])
			yb[6+j] = xi0&0xf | (xi1&0xf)<<4
			qh |= uint32(xi0&0x10) >> 4 << uint(j)
			qh |= uint32(xi1&0x10) >> 4 << uint(j+16)
		}
		binary.LittleEndian.PutUint32(yb[2:], qh)
	}
}

// quantize_row_q4_1_impl / q5_1_impl.
func rowQ41Q51Impl(fm bool, t Type, x []float32, y []byte, qw []float32) {
	bs, nmax := 20, 15
	if t == Q5_1 {
		bs, nmax = 24, 31
	}
	sigma2 := rowSigma2(x)
	var weight [32]float32
	var L, Laux [32]uint8
	for b := 0; b*32 < len(x); b++ {
		xb, yb := x[b*32:b*32+32], y[b*bs:(b+1)*bs]
		for j := range weight {
			weight[j] = mul(qw[b*32+j], sqrt(madd(fm, xb[j], xb[j], sigma2)))
		}
		d, min := makeQkx3Quants(fm, false, 32, nmax, xb, weight[:], L[:], Laux[:], -0.9, 0.05, 36, false)
		put16(yb, F16(d))
		put16(yb[2:], F16(-min))
		if t == Q4_1 {
			for j := 0; j < 16; j++ {
				yb[4+j] = L[j] | L[j+16]<<4
			}
			continue
		}
		var qh uint32
		for j := 0; j < 16; j++ {
			xi0, xi1 := L[j], L[j+16]
			yb[8+j] = xi0&0xf | (xi1&0xf)<<4
			qh |= uint32(xi0&0x10) >> 4 << uint(j)
			qh |= uint32(xi1&0x10) >> 4 << uint(j+16)
		}
		binary.LittleEndian.PutUint32(yb[4:], qh)
	}
}
