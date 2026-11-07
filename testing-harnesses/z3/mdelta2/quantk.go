package mdelta2

import (
	"encoding/binary"
	"math"
)

// Ports of ggml's reference K-quant quantizers (Q6_K, Q4_K). float32 throughout; explicit
// conversions prevent fused multiply-add. Producer mismatches surface as residual, not errors.

func nearestInt(f float32) int {
	v := f + 12582912.0
	return int(int32(math.Float32bits(v))&0x007fffff) - 0x00400000
}

func f32abs(x float32) float32 { return float32(math.Abs(float64(x))) }

func makeQxQuants(n, nmax int, x []float32, L []int8) float32 {
	var max, amax float32
	for i := 0; i < n; i++ {
		if ax := f32abs(x[i]); ax > amax {
			amax, max = ax, x[i]
		}
	}
	if amax < 1e-15 {
		for i := 0; i < n; i++ {
			L[i] = 0
		}
		return 0
	}
	iscale := -float32(nmax) / max
	var sumlx, suml2 float32
	for i := 0; i < n; i++ {
		l := nearestInt(iscale * x[i])
		l = maxI(-nmax, minI(nmax-1, l))
		L[i] = int8(l + nmax)
		w := float32(x[i] * x[i])
		sumlx += float32(float32(w*x[i]) * float32(l))
		suml2 += float32(float32(w*float32(l)) * float32(l))
	}
	var scale float32
	if suml2 != 0 {
		scale = sumlx / suml2
	}
	best := float32(scale * sumlx)
	for is := -9; is <= 9; is++ {
		if is == 0 {
			continue
		}
		iscale = -(float32(nmax) + float32(0.1)*float32(is)) / max
		sumlx, suml2 = 0, 0
		for i := 0; i < n; i++ {
			l := nearestInt(iscale * x[i])
			l = maxI(-nmax, minI(nmax-1, l))
			w := float32(x[i] * x[i])
			sumlx += float32(float32(w*x[i]) * float32(l))
			suml2 += float32(float32(w*float32(l)) * float32(l))
		}
		if suml2 > 0 && float32(sumlx*sumlx) > float32(best*suml2) {
			for i := 0; i < n; i++ {
				l := nearestInt(iscale * x[i])
				L[i] = int8(nmax + maxI(-nmax, minI(nmax-1, l)))
			}
			scale = sumlx / suml2
			best = float32(scale * sumlx)
		}
	}
	return scale
}

func maxI(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func quantQ6K(x []float32) []byte {
	nb := len(x) / 256
	out := make([]byte, nb*210)
	var L [256]int8
	var scales [16]float32
	for b := 0; b < nb; b++ {
		xb := x[b*256 : (b+1)*256]
		y := out[b*210 : (b+1)*210]
		var maxScale, maxAbs float32
		for ib := 0; ib < 16; ib++ {
			s := makeQxQuants(16, 32, xb[16*ib:16*ib+16], L[16*ib:16*ib+16])
			scales[ib] = s
			if a := f32abs(s); a > maxAbs {
				maxAbs, maxScale = a, s
			}
		}
		if maxAbs < 1e-15 {
			continue // all-zero block (d = 0)
		}
		iscale := -128 / maxScale
		dh := f16bits(1 / iscale)
		binary.LittleEndian.PutUint16(y[208:], dh)
		for ib := 0; ib < 16; ib++ {
			y[192+ib] = byte(int8(minI(127, nearestInt(iscale*scales[ib]))))
		}
		d16 := f16val(dh)
		for j := 0; j < 16; j++ {
			d := float32(d16 * float32(int8(y[192+j])))
			if d == 0 {
				continue
			}
			for ii := 0; ii < 16; ii++ {
				l := nearestInt(xb[16*j+ii] / d)
				L[16*j+ii] = int8(maxI(-32, minI(31, l)) + 32)
			}
		}
		ql, qh := y[0:128], y[128:192]
		for j := 0; j < 256; j += 128 {
			for l := 0; l < 32; l++ {
				q1, q2, q3, q4 := byte(L[j+l])&0xf, byte(L[j+l+32])&0xf, byte(L[j+l+64])&0xf, byte(L[j+l+96])&0xf
				ql[l] = q1 | q3<<4
				ql[l+32] = q2 | q4<<4
				qh[l] = byte(L[j+l])>>4 | byte(L[j+l+32])>>4<<2 | byte(L[j+l+64])>>4<<4 | byte(L[j+l+96])>>4<<6
			}
			ql, qh = ql[64:], qh[32:]
		}
	}
	return out
}

func makeQkx2Quants(n, nmax int, x, weights []float32, L []uint8, Laux []uint8, rmin, rdelta float32, nstep int) (scale, theMin float32) {
	mn, mx := x[0], x[0]
	sumW := weights[0]
	sumX := float32(sumW * x[0])
	for i := 1; i < n; i++ {
		if x[i] < mn {
			mn = x[i]
		}
		if x[i] > mx {
			mx = x[i]
		}
		sumW += weights[i]
		sumX += float32(weights[i] * x[i])
	}
	if mn > 0 {
		mn = 0
	}
	if mx == mn {
		for i := 0; i < n; i++ {
			L[i] = 0
		}
		return 0, -mn
	}
	iscale := float32(nmax) / (mx - mn)
	scale = 1 / iscale
	var bestMad float32
	for i := 0; i < n; i++ {
		l := nearestInt(iscale * (x[i] - mn))
		L[i] = uint8(maxI(0, minI(nmax, l)))
		diff := float32(float32(scale*float32(L[i])) + mn - x[i])
		bestMad += float32(weights[i] * float32(diff*diff))
	}
	for is := 0; is <= nstep; is++ {
		iscale = (rmin + rdelta*float32(is) + float32(nmax)) / (mx - mn)
		var sumL, sumL2, sumXL float32
		for i := 0; i < n; i++ {
			l := nearestInt(iscale * (x[i] - mn))
			l = maxI(0, minI(nmax, l))
			Laux[i] = uint8(l)
			w := weights[i]
			sumL += float32(w * float32(l))
			sumL2 += float32(float32(w*float32(l)) * float32(l))
			sumXL += float32(float32(w*float32(l)) * x[i])
		}
		D := float32(sumW*sumL2) - float32(sumL*sumL)
		if D > 0 {
			thisScale := (float32(sumW*sumXL) - float32(sumX*sumL)) / D
			thisMin := (float32(sumL2*sumX) - float32(sumL*sumXL)) / D
			if thisMin > 0 {
				thisMin = 0
				thisScale = sumXL / sumL2
			}
			var mad float32
			for i := 0; i < n; i++ {
				diff := float32(float32(thisScale*float32(Laux[i])) + thisMin - x[i])
				mad += float32(weights[i] * float32(diff*diff))
			}
			if mad < bestMad {
				copy(L[:n], Laux[:n])
				bestMad, scale, mn = mad, thisScale, thisMin
			}
		}
	}
	return scale, -mn
}

func getScaleMinK4(j int, q []byte) (sc, m byte) {
	if j < 4 {
		return q[j] & 63, q[j+4] & 63
	}
	return q[j+4]&0xF | q[j-4]>>6<<4, q[j+4]>>4 | q[j]>>6<<4
}

func quantQ4K(x []float32) []byte {
	nb := len(x) / 256
	out := make([]byte, nb*144)
	var L [256]uint8
	var Laux [32]uint8
	var weights [32]float32
	var mins, scales [8]float32
	for b := 0; b < nb; b++ {
		xb := x[b*256 : (b+1)*256]
		y := out[b*144 : (b+1)*144]
		var maxScale, maxMin float32
		for j := 0; j < 8; j++ {
			var sx2 float32
			for l := 0; l < 32; l++ {
				sx2 += float32(xb[32*j+l] * xb[32*j+l])
			}
			avx := float32(math.Sqrt(float64(sx2 / 32)))
			for l := 0; l < 32; l++ {
				weights[l] = avx + f32abs(xb[32*j+l])
			}
			scales[j], mins[j] = makeQkx2Quants(32, 15, xb[32*j:32*j+32], weights[:], L[32*j:32*j+32], Laux[:], -1, 0.1, 20)
			if scales[j] > maxScale {
				maxScale = scales[j]
			}
			if mins[j] > maxMin {
				maxMin = mins[j]
			}
		}
		var invScale, invMin float32
		if maxScale > 0 {
			invScale = 63 / maxScale
		}
		if maxMin > 0 {
			invMin = 63 / maxMin
		}
		for j := 0; j < 8; j++ {
			ls, lm := byte(minI(63, nearestInt(invScale*scales[j]))), byte(minI(63, nearestInt(invMin*mins[j])))
			if j < 4 {
				y[4+j], y[4+j+4] = ls, lm
			} else {
				y[4+j+4] = ls&0xF | lm&0xF<<4
				y[4+j-4] |= ls >> 4 << 6
				y[4+j] |= lm >> 4 << 6
			}
		}
		dh, dmh := f16bits(maxScale/63), f16bits(maxMin/63)
		binary.LittleEndian.PutUint16(y[0:], dh)
		binary.LittleEndian.PutUint16(y[2:], dmh)
		for j := 0; j < 8; j++ {
			sc, m := getScaleMinK4(j, y[4:16])
			d := float32(f16val(dh) * float32(sc))
			if d == 0 {
				continue
			}
			dm := float32(f16val(dmh) * float32(m))
			for ii := 0; ii < 32; ii++ {
				l := nearestInt((xb[32*j+ii] + dm) / d)
				L[32*j+ii] = uint8(maxI(0, minI(15, l)))
			}
		}
		q := y[16:]
		for j := 0; j < 256; j += 64 {
			for l := 0; l < 32; l++ {
				q[l] = L[j+l] | L[j+l+32]<<4
			}
			q = q[32:]
		}
	}
	return out
}
