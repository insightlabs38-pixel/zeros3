package qcore

const groupMaxEps = 1e-15

func b2f(l int32) float32 { return float32(l) }

// make_qx_quants. rmse_type as in ggml (negative = return early); qw may be nil.
func makeQxQuants(fm bool, n, nmax int, x []float32, L []int8, rmse int, qw []float32) float32 {
	var max, amax float32
	for i := 0; i < n; i++ {
		if ax := fabs(x[i]); ax > amax {
			amax, max = ax, x[i]
		}
	}
	if amax < groupMaxEps {
		for i := 0; i < n; i++ {
			L[i] = 0
		}
		return 0
	}
	nm := int32(nmax)
	iscale := float32(-nmax) / max
	if rmse == 0 {
		for i := 0; i < n; i++ {
			L[i] = int8(nm + clamp(ni(fm, iscale, x[i]), -nm, nm-1))
		}
		return 1 / iscale
	}
	early := false
	if rmse < 0 {
		rmse, early = -rmse, true
	}
	wt := func(i int) float32 {
		switch {
		case qw != nil:
			return qw[i]
		case rmse == 1:
			return mul(x[i], x[i])
		case rmse == 2:
			return 1
		case rmse == 3:
			return fabs(x[i])
		}
		return sqrt(fabs(x[i]))
	}
	var sumlx, suml2 float32
	for i := 0; i < n; i++ {
		l := clamp(ni(fm, iscale, x[i]), -nm, nm-1)
		L[i] = int8(l + nm)
		w := wt(i)
		sumlx = madd(fm, mul(w, x[i]), b2f(l), sumlx)
		suml2 = madd(fm, mul(w, b2f(l)), b2f(l), suml2)
	}
	var scale float32
	if suml2 != 0 {
		scale = sumlx / suml2
	}
	if early {
		if suml2 > 0 {
			return mul(0.5, scale+1/iscale)
		}
		return 1 / iscale
	}
	best := mul(scale, sumlx)
	for is := -9; is <= 9; is++ {
		if is == 0 {
			continue
		}
		iscale = -madd(fm, 0.1, float32(is), float32(nmax)) / max
		sumlx, suml2 = 0, 0
		for i := 0; i < n; i++ { // vectorized in-order reduction: never fused
			l := clamp(ni(fm, iscale, x[i]), -nm, nm-1)
			w := wt(i)
			sumlx += mul(mul(w, x[i]), b2f(l))
			suml2 += mul(mul(w, b2f(l)), b2f(l))
		}
		if suml2 > 0 && mul(sumlx, sumlx) > mul(best, suml2) {
			for i := 0; i < n; i++ {
				L[i] = int8(nm + clamp(ni(fm, iscale, x[i]), -nm, nm-1))
			}
			scale = sumlx / suml2
			best = mul(scale, sumlx)
		}
	}
	return scale
}

// make_q3_quants with do_rmse = true (the only use).
func makeQ3Quants(n, nmax int, x []float32, L []int8) float32 {
	var max, amax float32
	for i := 0; i < n; i++ {
		if ax := fabs(x[i]); ax > amax {
			amax, max = ax, x[i]
		}
	}
	if amax < groupMaxEps {
		for i := 0; i < n; i++ {
			L[i] = 0
		}
		return 0
	}
	nm := int32(nmax)
	iscale := float32(-nmax) / max
	var sumlx, suml2 float32
	for i := 0; i < n; i++ {
		l := clamp(nearestInt(mul(iscale, x[i])), -nm, nm-1)
		L[i] = int8(l)
		w := mul(x[i], x[i])
		sumlx += mul(mul(w, x[i]), b2f(l))
		suml2 += mul(mul(w, b2f(l)), b2f(l))
	}
	for itry := 0; itry < 5; itry++ {
		changed := 0
		for i := 0; i < n; i++ {
			w := mul(x[i], x[i])
			li := float32(L[i])
			slx := sumlx - mul(mul(w, x[i]), li)
			if slx > 0 {
				sl2 := suml2 - mul(mul(w, li), li)
				nl := clamp(nearestInt(mul(x[i], sl2)/slx), -nm, nm-1)
				if nl != int32(L[i]) {
					slx += mul(mul(w, x[i]), b2f(nl))
					sl2 += mul(mul(w, b2f(nl)), b2f(nl))
					if sl2 > 0 && mul(mul(slx, slx), suml2) > mul(mul(sumlx, sumlx), sl2) {
						L[i], sumlx, suml2 = int8(nl), slx, sl2
						changed++
					}
				}
			}
		}
		if changed == 0 {
			break
		}
	}
	for i := 0; i < n; i++ {
		L[i] += int8(nmax)
	}
	return sumlx / suml2
}

// make_qkx2_quants. Returns (scale, the_min).
func makeQkx2Quants(n, nmax int, x, weights []float32, L, Laux []uint8, rmin, rdelta float32, nstep int, useMad bool) (float32, float32) {
	const fm = false
	min, max := x[0], x[0]
	sumW := weights[0]
	sumX := mul(sumW, x[0])
	for i := 1; i < n; i++ {
		if x[i] < min {
			min = x[i]
		}
		if x[i] > max {
			max = x[i]
		}
		w := weights[i]
		sumW += w
		sumX += mul(w, x[i])
	}
	if min > 0 {
		min = 0
	}
	if max == min {
		for i := 0; i < n; i++ {
			L[i] = 0
		}
		return 0, -min
	}
	return kxSearch(fm, n, nmax, x, weights, L, Laux, rmin, rdelta, nstep, useMad, min, max, sumW, sumX)
}

// make_qkx3_quants: as qkx2 with optional weights (nil = x^2) and `max <= min`.
// fm selects the contracted build; sumXFused is its call-site dependent choice for `sum_x += w*x`
// (fused where the function was inlined and unrolled, plain in the out-of-line clone).
func makeQkx3Quants(fm, sumXFused bool, n, nmax int, x, weights []float32, L, Laux []uint8, rmin, rdelta float32, nstep int, useMad bool) (float32, float32) {
	var wbuf [32]float32
	if weights == nil {
		for i := 0; i < n; i++ {
			wbuf[i] = mul(x[i], x[i])
		}
		weights = wbuf[:n]
	}
	min, max := x[0], x[0]
	sumW := weights[0]
	sumX := mul(sumW, x[0])
	for i := 1; i < n; i++ {
		if x[i] < min {
			min = x[i]
		}
		if x[i] > max {
			max = x[i]
		}
		w := weights[i]
		sumW += w
		sumX = madd(fm && sumXFused, w, x[i], sumX)
	}
	if min > 0 {
		min = 0
	}
	if max <= min {
		for i := 0; i < n; i++ {
			L[i] = 0
		}
		return 0, -min
	}
	return kxSearch(fm, n, nmax, x, weights, L, Laux, rmin, rdelta, nstep, useMad, min, max, sumW, sumX)
}

// kxSearch is the shared body of make_qkx2_quants / make_qkx3_quants after the degenerate checks.
func kxSearch(fm bool, n, nmax int, x, weights []float32, L, Laux []uint8, rmin, rdelta float32, nstep int, useMad bool, min, max, sumW, sumX float32) (float32, float32) {
	nm := int32(nmax)
	iscale := float32(nmax) / (max - min)
	scale := 1 / iscale
	var bestMad float32
	for i := 0; i < n; i++ {
		L[i] = uint8(clamp(ni(fm, iscale, x[i]-min), 0, nm))
		diff := madd(fm, scale, float32(L[i]), min) - x[i]
		if useMad {
			diff = fabs(diff)
		} else {
			diff = mul(diff, diff)
		}
		bestMad = madd(fm, weights[i], diff, bestMad)
	}
	if nstep < 1 {
		return scale, -min
	}
	for is := 0; is <= nstep; is++ {
		iscale = (madd(fm, rdelta, float32(is), rmin) + float32(nmax)) / (max - min)
		var sumL, sumL2, sumXL float32
		for i := 0; i < n; i++ {
			l := clamp(ni(fm, iscale, x[i]-min), 0, nm)
			Laux[i] = uint8(l)
			wl := mul(weights[i], b2f(l)) // shared by three uses: never fused into sum_l
			sumL += wl
			sumL2 = madd(fm, wl, b2f(l), sumL2)
			sumXL = madd(fm, wl, x[i], sumXL)
		}
		var D float32
		if fm {
			D = fma32(sumW, sumL2, -mul(sumL, sumL))
		} else {
			D = mul(sumW, sumL2) - mul(sumL, sumL)
		}
		if D > 0 {
			var thisScale, thisMin float32
			if fm {
				thisScale = fma32(-sumX, sumL, mul(sumW, sumXL)) / D
				thisMin = fma32(sumL2, sumX, -mul(sumL, sumXL)) / D
			} else {
				thisScale = (mul(sumW, sumXL) - mul(sumX, sumL)) / D
				thisMin = (mul(sumL2, sumX) - mul(sumL, sumXL)) / D
			}
			if thisMin > 0 {
				thisMin = 0
				thisScale = sumXL / sumL2
			}
			var mad float32
			for i := 0; i < n; i++ {
				diff := madd(fm, thisScale, float32(Laux[i]), thisMin) - x[i]
				if useMad {
					diff = fabs(diff)
				} else {
					diff = mul(diff, diff)
				}
				mad = madd(fm, weights[i], diff, mad)
			}
			if mad < bestMad {
				copy(L[:n], Laux[:n])
				bestMad, scale, min = mad, thisScale, thisMin
			}
		}
	}
	return scale, -min
}

// make_qp_quants.
func makeQpQuants(fm bool, n, nmax int, x []float32, L []uint8, qw []float32) float32 {
	var max float32
	for i := 0; i < n; i++ {
		if !(max > x[i]) { // MAX(max, x[i]), NaN-faithful
			max = x[i]
		}
	}
	if max == 0 {
		for i := 0; i < n; i++ {
			L[i] = 0
		}
		return 0
	}
	nm := int32(nmax)
	iscale := float32(nmax) / max
	for i := 0; i < n; i++ {
		L[i] = uint8(ni(fm, iscale, x[i]))
	}
	scale := 1 / iscale
	var bestMse float32
	for i := 0; i < n; i++ {
		diff := madd(fm, -scale, float32(L[i]), x[i])
		bestMse = madd(fm, mul(qw[i], diff), diff, bestMse)
	}
	for is := -4; is <= 4; is++ {
		if is == 0 {
			continue
		}
		iscaleIs := madd(fm, 0.1, float32(is), float32(nmax)) / max
		scaleIs := 1 / iscaleIs
		var mse float32
		for i := 0; i < n; i++ {
			l := ni(fm, iscaleIs, x[i])
			if l > nm {
				l = nm
			}
			diff := madd(fm, -scaleIs, b2f(l), x[i])
			mse = madd(fm, mul(qw[i], diff), diff, mse)
		}
		if mse < bestMse {
			bestMse, iscale = mse, iscaleIs
		}
	}
	var sumlx, suml2 float32
	for i := 0; i < n; i++ {
		l := ni(fm, iscale, x[i])
		if l > nm {
			l = nm
		}
		L[i] = uint8(l)
		w := qw[i]
		sumlx = madd(fm, mul(w, x[i]), b2f(l), sumlx)
		suml2 = madd(fm, mul(w, b2f(l)), b2f(l), suml2)
	}
	for itry := 0; itry < 5; itry++ {
		changed := 0
		for i := 0; i < n; i++ {
			w := qw[i]
			li := float32(L[i])
			slx := madd(fm, -mul(w, x[i]), li, sumlx)
			sl2 := madd(fm, -mul(w, li), li, suml2)
			if slx > 0 && sl2 > 0 {
				nl := nearestInt(mul(x[i], sl2) / slx)
				if nl > nm {
					nl = nm
				}
				if nl != int32(L[i]) {
					slx = madd(fm, mul(w, x[i]), b2f(nl), slx)
					sl2 = madd(fm, mul(w, b2f(nl)), b2f(nl), sl2)
					if mul(mul(slx, slx), suml2) > mul(mul(sumlx, sumlx), sl2) {
						L[i], sumlx, suml2 = uint8(nl), slx, sl2
						changed++
					}
				}
			}
		}
		if changed == 0 {
			break
		}
	}
	return sumlx / suml2
}

func getScaleMinK4(j int, q []byte) (sc, m byte) {
	if j < 4 {
		return q[j] & 63, q[j+4] & 63
	}
	return q[j+4]&0xF | q[j-4]>>6<<4, q[j+4]>>4 | q[j]>>6<<4
}

func pack2(L []uint8, qs []byte) { // 256 two-bit quants -> 64 bytes
	for j := 0; j < 256; j += 128 {
		for l := 0; l < 32; l++ {
			qs[j/4+l] = L[j+l] | L[j+l+32]<<2 | L[j+l+64]<<4 | L[j+l+96]<<6
		}
	}
}

// quantize_row_q2_K_ref. block: scales[16] qs[64] d dmin.
func rowQ2KRef(x []float32, y []byte) {
	var L [256]uint8
	var Laux [16]uint8
	var weights, mins, scales [16]float32
	for b := 0; b*256 < len(x); b++ {
		xb, yb := x[b*256:(b+1)*256], y[b*84:(b+1)*84]
		var maxScale, maxMin float32
		for j := 0; j < 16; j++ {
			for l := 0; l < 16; l++ {
				weights[l] = fabs(xb[16*j+l])
			}
			scales[j], mins[j] = makeQkx2Quants(16, 3, xb[16*j:16*j+16], weights[:], L[16*j:16*j+16], Laux[:], -0.5, 0.1, 15, true)
			if scales[j] > maxScale {
				maxScale = scales[j]
			}
			if mins[j] > maxMin {
				maxMin = mins[j]
			}
		}
		sc := yb[0:16]
		var dh, dmh uint16
		if maxScale > 0 {
			iscale := 15 / maxScale
			for j := 0; j < 16; j++ {
				sc[j] = byte(nearestInt(mul(iscale, scales[j])))
			}
			dh = F16(maxScale / 15)
		} else {
			for j := 0; j < 16; j++ {
				sc[j] = 0
			}
		}
		if maxMin > 0 {
			iscale := 15 / maxMin
			for j := 0; j < 16; j++ {
				sc[j] |= byte(nearestInt(mul(iscale, mins[j])) << 4)
			}
			dmh = F16(maxMin / 15)
		}
		put16(yb[80:], dh)
		put16(yb[82:], dmh)
		q2Requant(false, xb, sc, F16to32(dh), F16to32(dmh), L[:])
		pack2(L[:], yb[16:80])
	}
}

// fm: the contracted q2_K_impl fuses `x + dmin*m` (the product is formed in the unrolled block).
func q2Requant(fm bool, xb []float32, sc []byte, dd, dmin float32, L []uint8) {
	for j := 0; j < 16; j++ {
		d := mul(dd, float32(sc[j]&0xF))
		if d == 0 {
			continue
		}
		for ii := 0; ii < 16; ii++ {
			L[16*j+ii] = uint8(clamp(nearestInt(madd(fm, dmin, float32(sc[j]>>4), xb[16*j+ii])/d), 0, 3))
		}
	}
}

// quantize_row_q2_K_impl (one row, importance weights qw).
func rowQ2KImpl(fm bool, x []float32, y []byte, qw []float32) {
	var L [256]uint8
	var Laux, Ls, Lm [16]uint8
	var mins, scales, sw, weight [16]float32
	for b := 0; b*256 < len(x); b++ {
		xb, yb := x[b*256:(b+1)*256], y[b*84:(b+1)*84]
		sw = [16]float32{}
		var sumx2 float32
		for j := 0; j < 256; j++ {
			sumx2 += mul(xb[j], xb[j])
		}
		sigma2 := sumx2 / 256
		for j := 0; j < 16; j++ {
			q := qw[256*b+16*j:]
			for l := 0; l < 16; l++ {
				weight[l] = mul(q[l], sqrt(madd(fm, xb[16*j+l], xb[16*j+l], sigma2)))
			}
			for l := 0; l < 16; l++ {
				sw[j] += weight[l]
			}
			scales[j], mins[j] = makeQkx3Quants(fm, true, 16, 3, xb[16*j:16*j+16], weight[:], L[16*j:16*j+16], Laux[:], -0.9, 0.05, 36, false)
		}
		dh := F16(makeQpQuants(fm, 16, 15, scales[:], Ls[:], sw[:]))
		dmh := F16(makeQpQuants(fm, 16, 15, mins[:], Lm[:], sw[:]))
		put16(yb[80:], dh)
		put16(yb[82:], dmh)
		sc := yb[0:16]
		for j := 0; j < 16; j++ {
			sc[j] = Ls[j] | Lm[j]<<4
		}
		q2Requant(fm, xb, sc, F16to32(dh), F16to32(dmh), L[:])
		pack2(L[:], yb[16:80])
	}
}

// q3 tail shared by ref and impl: requantize with the stored 6-bit scales, split the high bit.
// block: hmask[32] qs[64] scales[12] d.
func q3Finish(xb []float32, yb []byte, L []int8) {
	sc := yb[96:108]
	dd := F16to32(uint16(yb[108]) | uint16(yb[109])<<8)
	for j := 0; j < 16; j++ {
		var s int8
		if j < 8 {
			s = int8(sc[j] & 0xF)
		} else {
			s = int8(sc[j-8] >> 4)
		}
		s = int8(int32(s)|int32(sc[8+j%4]>>(2*uint(j/4))&3)<<4) - 32
		d := mul(dd, float32(s))
		if d == 0 {
			continue
		}
		for ii := 0; ii < 16; ii++ {
			L[16*j+ii] = int8(clamp(nearestInt(xb[16*j+ii]/d), -4, 3) + 4)
		}
	}
	hm := yb[0:32]
	for i := range hm {
		hm[i] = 0
	}
	m, bit := 0, byte(1)
	var U [256]uint8
	for j := 0; j < 256; j++ {
		if L[j] > 3 {
			hm[m] |= bit
			L[j] -= 4
		}
		U[j] = uint8(L[j])
		if m++; m == 32 {
			m, bit = 0, bit<<1
		}
	}
	pack2(U[:], yb[32:96])
}

func q3PutScale(sc []byte, j int, l int32) {
	if j < 8 {
		sc[j] = byte(l & 0xF)
	} else {
		sc[j-8] |= byte(l&0xF) << 4
	}
	l >>= 4
	sc[j%4+8] |= byte(l << (2 * uint(j/4)))
}

// quantize_row_q3_K_ref.
func rowQ3KRef(x []float32, y []byte) {
	var L [256]int8
	var scales [16]float32
	for b := 0; b*256 < len(x); b++ {
		xb, yb := x[b*256:(b+1)*256], y[b*110:(b+1)*110]
		var maxScale, amax float32
		for j := 0; j < 16; j++ {
			scales[j] = makeQ3Quants(16, 4, xb[16*j:16*j+16], L[16*j:16*j+16])
			if s := fabs(scales[j]); s > amax {
				amax, maxScale = s, scales[j]
			}
		}
		sc := yb[96:108]
		for i := range sc {
			sc[i] = 0
		}
		var dh uint16
		if maxScale != 0 {
			iscale := -32 / maxScale
			for j := 0; j < 16; j++ {
				l := int32(int8(nearestInt(mul(iscale, scales[j]))))
				q3PutScale(sc, j, int32(int8(clamp(l, -32, 31)+32)))
			}
			dh = F16(1 / iscale)
		}
		put16(yb[108:], dh)
		q3Finish(xb, yb, L[:])
	}
}

// quantize_row_q3_K_impl.
func rowQ3KImpl(fm bool, x []float32, y []byte, qw []float32) {
	var L [256]int8
	var Ls [16]int8
	var scales, weight, sw [16]float32
	for b := 0; b*256 < len(x); b++ {
		xb, yb := x[b*256:(b+1)*256], y[b*110:(b+1)*110]
		var sumx2 float32
		for j := 0; j < 256; j++ {
			sumx2 += mul(xb[j], xb[j])
		}
		sigma2 := mul(2, sumx2) / 256
		for j := 0; j < 16; j++ {
			q := qw[256*b+16*j:]
			var sumw float32
			for l := 0; l < 16; l++ {
				weight[l] = mul(q[l], sqrt(madd(fm, xb[16*j+l], xb[16*j+l], sigma2)))
			}
			for l := 0; l < 16; l++ {
				sumw += weight[l]
			}
			sw[j] = sumw
			scales[j] = makeQxQuants(fm, 16, 4, xb[16*j:16*j+16], L[16*j:16*j+16], 1, weight[:])
		}
		sc := yb[96:108]
		for i := range sc {
			sc[i] = 0
		}
		dBlock := makeQxQuants(fm, 16, 32, scales[:], Ls[:], 1, sw[:])
		for j := 0; j < 16; j++ {
			q3PutScale(sc, j, int32(Ls[j]))
		}
		put16(yb[108:], F16(dBlock))
		q3Finish(xb, yb, L[:])
	}
}

// quantize_row_q4_K_{ref,impl} (nmax 15) and q5_K (nmax 31); qw == nil selects the ref path.
// q4_K block: d dmin scales[12] qs[128]; q5_K: d dmin scales[12] qh[32] qs[128].
func rowQ4Q5K(fm bool, x []float32, y []byte, qw []float32, nmax int) {
	bs := 144
	if nmax == 31 {
		bs = 176
	}
	var L [256]uint8
	var Laux [32]uint8
	var Ls, Lm [8]uint8
	var weights [32]float32
	var mins, scales, sw [8]float32
	for b := 0; b*256 < len(x); b++ {
		xb, yb := x[b*256:(b+1)*256], y[b*bs:(b+1)*bs]
		sc := yb[4:16]
		for i := range sc {
			sc[i] = 0
		}
		var dh, dmh uint16
		if qw == nil {
			var maxScale, maxMin float32
			for j := 0; j < 8; j++ {
				var sx2 float32
				for l := 0; l < 32; l++ {
					sx2 += mul(xb[32*j+l], xb[32*j+l])
				}
				avx := sqrt(sx2 / 32)
				for l := 0; l < 32; l++ {
					weights[l] = avx + fabs(xb[32*j+l])
				}
				if nmax == 15 {
					scales[j], mins[j] = makeQkx2Quants(32, 15, xb[32*j:32*j+32], weights[:], L[32*j:32*j+32], Laux[:], -1, 0.1, 20, false)
				} else {
					scales[j], mins[j] = makeQkx2Quants(32, 31, xb[32*j:32*j+32], weights[:], L[32*j:32*j+32], Laux[:], -0.5, 0.1, 15, false)
				}
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
				Ls[j] = minByte(63, byte(nearestInt(mul(invScale, scales[j]))))
				Lm[j] = minByte(63, byte(nearestInt(mul(invMin, mins[j]))))
			}
			dh, dmh = F16(maxScale/63), F16(maxMin/63)
		} else {
			var sumx2 float32
			for l := 0; l < 256; l++ {
				sumx2 += mul(xb[l], xb[l])
			}
			sigma2 := mul(2, sumx2) / 256
			for j := 0; j < 8; j++ {
				q := qw[256*b+32*j:]
				var sumw float32
				for l := 0; l < 32; l++ {
					weights[l] = mul(q[l], sqrt(madd(fm, xb[32*j+l], xb[32*j+l], sigma2)))
				}
				for l := 0; l < 32; l++ {
					sumw += weights[l]
				}
				sw[j] = sumw
				scales[j], mins[j] = makeQkx3Quants(fm, false, 32, nmax, xb[32*j:32*j+32], weights[:], L[32*j:32*j+32], Laux[:], -0.9, 0.05, 36, false)
			}
			dh = F16(makeQpQuants(fm, 8, 63, scales[:], Ls[:], sw[:]))
			dmh = F16(makeQpQuants(fm, 8, 63, mins[:], Lm[:], sw[:]))
			if nmax == 31 { // only q5_K_impl clamps again
				for j := 0; j < 8; j++ {
					Ls[j], Lm[j] = minByte(63, Ls[j]), minByte(63, Lm[j])
				}
			}
		}
		for j := 0; j < 8; j++ {
			ls, lm := Ls[j], Lm[j]
			if j < 4 {
				sc[j], sc[j+4] = ls, lm
			} else {
				sc[j+4] = ls&0xF | lm&0xF<<4
				sc[j-4] |= ls >> 4 << 6
				sc[j] |= lm >> 4 << 6
			}
		}
		put16(yb[0:], dh)
		put16(yb[2:], dmh)
		dd, dmin := F16to32(dh), F16to32(dmh)
		for j := 0; j < 8; j++ {
			s, m := getScaleMinK4(j, sc)
			d := mul(dd, float32(s))
			if d == 0 {
				continue
			}
			dm := mul(dmin, float32(m))
			for ii := 0; ii < 32; ii++ {
				L[32*j+ii] = uint8(clamp(nearestInt((xb[32*j+ii]+dm)/d), 0, int32(nmax)))
			}
		}
		if nmax == 15 {
			q := yb[16:]
			for j := 0; j < 256; j += 64 {
				for l := 0; l < 32; l++ {
					q[l] = L[j+l] | L[j+l+32]<<4
				}
				q = q[32:]
			}
			continue
		}
		qh, ql := yb[16:48], yb[48:]
		for i := range qh {
			qh[i] = 0
		}
		m1, m2 := byte(1), byte(2)
		for n := 0; n < 256; n += 64 {
			for j := 0; j < 32; j++ {
				l1, l2 := L[n+j], L[n+j+32]
				if l1 > 15 {
					l1 -= 16
					qh[j] |= m1
				}
				if l2 > 15 {
					l2 -= 16
					qh[j] |= m2
				}
				ql[j] = l1 | l2<<4
			}
			m1, m2 = m1<<2, m2<<2
			ql = ql[32:]
		}
	}
}

func minByte(a, b byte) byte {
	if a < b {
		return a
	}
	return b
}

// quantize_row_q6_K_{ref,impl}; block: ql[128] qh[64] scales[16] d.
func rowQ6K(fm bool, x []float32, y []byte, qw []float32) {
	var L [256]int8
	var scales [16]float32
	for b := 0; b*256 < len(x); b++ {
		xb, yb := x[b*256:(b+1)*256], y[b*210:(b+1)*210]
		var maxScale, maxAbs float32
		for ib := 0; ib < 16; ib++ {
			var w []float32
			if qw != nil {
				w = qw[256*b+16*ib : 256*b+16*ib+16]
			}
			s := makeQxQuants(fm, 16, 32, xb[16*ib:16*ib+16], L[16*ib:16*ib+16], 1, w)
			scales[ib] = s
			if a := fabs(s); a > maxAbs {
				maxAbs, maxScale = a, s
			}
		}
		if maxAbs < groupMaxEps {
			for i := range yb {
				yb[i] = 0
			}
			continue
		}
		iscale := -128 / maxScale
		dh := F16(1 / iscale)
		put16(yb[208:], dh)
		for ib := 0; ib < 16; ib++ {
			l := ni(fm, iscale, scales[ib])
			if l > 127 {
				l = 127
			}
			yb[192+ib] = byte(int8(l))
		}
		dd := F16to32(dh)
		for j := 0; j < 16; j++ {
			d := mul(dd, float32(int8(yb[192+j])))
			if d == 0 {
				continue
			}
			for ii := 0; ii < 16; ii++ {
				L[16*j+ii] = int8(clamp(nearestInt(xb[16*j+ii]/d), -32, 31) + 32)
			}
		}
		ql, qh := yb[0:128], yb[128:192]
		for j := 0; j < 256; j += 128 {
			for l := 0; l < 32; l++ {
				q1, q2, q3, q4 := byte(L[j+l])&0xF, byte(L[j+l+32])&0xF, byte(L[j+l+64])&0xF, byte(L[j+l+96])&0xF
				ql[l] = q1 | q3<<4
				ql[l+32] = q2 | q4<<4
				qh[l] = byte(L[j+l])>>4 | byte(L[j+l+32])>>4<<2 | byte(L[j+l+64])>>4<<4 | byte(L[j+l+96])>>4<<6
			}
			ql, qh = ql[64:], qh[32:]
		}
	}
}
