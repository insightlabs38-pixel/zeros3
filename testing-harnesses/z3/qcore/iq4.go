package qcore

var kvaluesIQ4NL = [16]int8{-127, -104, -83, -65, -49, -35, -22, -10, 1, 13, 25, 38, 53, 69, 89, 113}

func bestIndexInt8(val *[16]int8, x float32) int {
	if x <= float32(val[0]) {
		return 0
	}
	if x >= float32(val[15]) {
		return 15
	}
	ml, mu := 0, 15
	for mu-ml > 1 {
		mav := (ml + mu) / 2
		if x < float32(val[mav]) {
			mu = mav
		} else {
			ml = mav
		}
	}
	if x-float32(val[mu-1]) < float32(val[mu])-x {
		return mu - 1
	}
	return mu
}

// quantize_iq4_nl (sbs 32; block: d qs[16]) and quantize_iq4_xs (sbs 256; block: d scales_h
// scales_l[4] qs[128]); ntry = 7 as in ggml_quantize_chunk. L persists across blocks as in C.
func rowIQ4(fm bool, x []float32, y []byte, qw []float32, sbs int) {
	bs := 18
	if sbs == 256 {
		bs = 136
	}
	const ntry = 7
	v := &kvaluesIQ4NL
	var L [256]uint8
	var weight [32]float32
	var scales [8]float32
	nb := sbs / 32
	for b := 0; b*sbs < len(x); b++ {
		xs, yb := x[b*sbs:(b+1)*sbs], y[b*bs:(b+1)*bs]
		var q4 []byte
		if sbs == 256 {
			q4 = yb[8:]
		} else {
			q4 = yb[2:]
		}
		var sigma2 float32
		for j := 0; j < sbs; j++ {
			sigma2 += mul(xs[j], xs[j])
		}
		sigma2 = mul(sigma2, 2/float32(sbs))
		var dh uint16
		var maxScale, amaxScale float32
		for ib := 0; ib < nb; ib++ {
			xb, Lb := xs[ib*32:ib*32+32], L[ib*32:ib*32+32]
			if qw != nil {
				q := qw[b*sbs+ib*32:]
				for j := 0; j < 32; j++ {
					weight[j] = mul(q[j], sqrt(madd(fm, xb[j], xb[j], sigma2)))
				}
			} else {
				for j := 0; j < 32; j++ {
					weight[j] = mul(xb[j], xb[j])
				}
			}
			var amax, max float32
			for j := 0; j < 32; j++ {
				if ax := fabs(xb[j]); ax > amax {
					amax, max = ax, xb[j]
				}
			}
			if amax < groupMaxEps {
				scales[ib] = 0
				continue
			}
			d := -max / float32(v[0])
			id := 1 / d
			var sumqx, sumq2 float32
			for j := 0; j < 32; j++ {
				l := bestIndexInt8(v, mul(id, xb[j]))
				Lb[j] = uint8(l)
				q, w := float32(v[l]), weight[j]
				sumqx = madd(fm, mul(w, q), xb[j], sumqx)
				sumq2 = madd(fm, mul(w, q), q, sumq2)
			}
			d = sumqx / sumq2
			best := mul(d, sumqx)
			for itry := -ntry; itry <= ntry; itry++ {
				id = float32(itry+int(v[0])) / max
				sumqx, sumq2 = 0, 0
				for j := 0; j < 32; j++ {
					q, w := float32(v[bestIndexInt8(v, mul(id, xb[j]))]), weight[j]
					sumqx = madd(fm, mul(w, q), xb[j], sumqx)
					sumq2 = madd(fm, mul(w, q), q, sumq2)
				}
				if sumq2 > 0 && mul(sumqx, sumqx) > mul(best, sumq2) {
					d = sumqx / sumq2
					best = mul(d, sumqx)
				}
			}
			scales[ib] = d
			if a := fabs(d); a > amaxScale {
				amaxScale, maxScale = a, d
			}
		}
		if nb > 1 {
			var sh uint16
			sl := yb[4:8]
			d := -maxScale / 32
			dh = F16(d)
			var id float32
			if d != 0 {
				id = 1 / d
			}
			for ib := 0; ib < nb; ib++ {
				l := clamp(ni(fm, id, scales[ib]), -32, 31)
				dl := mul(d, b2f(l))
				var idl float32
				if dl != 0 {
					idl = 1 / dl
				}
				xb, Lb := xs[ib*32:ib*32+32], L[ib*32:ib*32+32]
				for j := 0; j < 32; j++ {
					Lb[j] = uint8(bestIndexInt8(v, mul(idl, xb[j])))
				}
				l += 32
				ll, lh := uint8(l&0xf), uint16(l>>4)
				if ib%2 == 0 {
					sl[ib/2] = ll
				} else {
					sl[ib/2] |= ll << 4
				}
				sh |= lh << (2 * uint(ib%8))
			}
			put16(yb[2:], sh)
		} else {
			dh = F16(scales[0])
			var id float32
			if scales[0] != 0 {
				id = 1 / scales[0]
			}
			for j := 0; j < sbs; j++ {
				L[j] = uint8(bestIndexInt8(v, mul(id, xs[j])))
			}
		}
		put16(yb, dh)
		for i := 0; i < sbs/32; i++ {
			for j := 0; j < 16; j++ {
				q4[16*i+j] = L[32*i+j] | L[32*i+16+j]<<4
			}
		}
	}
}
