package qcore

import (
	"sort"
	"sync"
)

// IQ3_S (ggml quantize_iq3_s, block 32, D4 lattice grid of 512 points). The grid, the 12-bit index
// map and the neighbour lists are rebuilt from the 512 grid seeds exactly as iq3xs_init_impl does.

var kgrid512 = [512]uint16{
	0, 1, 2, 5, 7, 8, 9, 10, 12, 14, 16, 17, 21, 27, 32, 34,
	37, 39, 41, 43, 48, 50, 57, 60, 63, 64, 65, 66, 68, 72, 73, 77,
	80, 83, 87, 89, 93, 100, 113, 117, 122, 128, 129, 133, 135, 136, 139, 142,
	145, 149, 152, 156, 162, 165, 167, 169, 171, 184, 187, 195, 201, 205, 208, 210,
	217, 219, 222, 228, 232, 234, 247, 249, 253, 256, 267, 271, 273, 276, 282, 288,
	291, 297, 312, 322, 324, 336, 338, 342, 347, 353, 357, 359, 374, 379, 390, 393,
	395, 409, 426, 441, 448, 450, 452, 464, 466, 470, 475, 488, 492, 512, 513, 514,
	516, 520, 521, 523, 525, 527, 528, 530, 537, 540, 542, 556, 558, 561, 570, 576,
	577, 579, 582, 584, 588, 593, 600, 603, 609, 616, 618, 632, 638, 640, 650, 653,
	655, 656, 660, 666, 672, 675, 685, 688, 698, 705, 708, 711, 712, 715, 721, 727,
	728, 732, 737, 754, 760, 771, 773, 778, 780, 793, 795, 802, 806, 808, 812, 833,
	840, 843, 849, 856, 858, 873, 912, 916, 919, 932, 934, 961, 963, 968, 970, 977,
	989, 993, 1010, 1016, 1024, 1025, 1027, 1029, 1031, 1032, 1034, 1036, 1038, 1041, 1043, 1047,
	1048, 1050, 1057, 1059, 1061, 1064, 1066, 1079, 1080, 1083, 1085, 1088, 1090, 1096, 1099, 1103,
	1106, 1109, 1113, 1116, 1122, 1129, 1153, 1156, 1159, 1169, 1171, 1176, 1183, 1185, 1195, 1199,
	1209, 1212, 1216, 1218, 1221, 1225, 1234, 1236, 1241, 1243, 1250, 1256, 1270, 1281, 1287, 1296,
	1299, 1306, 1309, 1313, 1338, 1341, 1348, 1353, 1362, 1375, 1376, 1387, 1400, 1408, 1410, 1415,
	1425, 1453, 1457, 1477, 1481, 1494, 1496, 1507, 1512, 1538, 1545, 1547, 1549, 1551, 1554, 1561,
	1563, 1565, 1570, 1572, 1575, 1577, 1587, 1593, 1601, 1603, 1605, 1612, 1617, 1619, 1632, 1648,
	1658, 1662, 1664, 1674, 1680, 1690, 1692, 1704, 1729, 1736, 1740, 1745, 1747, 1751, 1752, 1761,
	1763, 1767, 1773, 1787, 1795, 1801, 1806, 1810, 1817, 1834, 1840, 1844, 1857, 1864, 1866, 1877,
	1882, 1892, 1902, 1915, 1934, 1953, 1985, 1987, 2000, 2002, 2013, 2048, 2052, 2058, 2064, 2068,
	2071, 2074, 2081, 2088, 2104, 2114, 2119, 2121, 2123, 2130, 2136, 2141, 2147, 2153, 2157, 2177,
	2179, 2184, 2189, 2193, 2203, 2208, 2223, 2226, 2232, 2244, 2249, 2251, 2256, 2258, 2265, 2269,
	2304, 2306, 2324, 2335, 2336, 2361, 2373, 2375, 2385, 2418, 2443, 2460, 2480, 2504, 2509, 2520,
	2531, 2537, 2562, 2568, 2572, 2578, 2592, 2596, 2599, 2602, 2614, 2620, 2625, 2627, 2629, 2634,
	2641, 2650, 2682, 2688, 2697, 2707, 2712, 2718, 2731, 2754, 2759, 2760, 2775, 2788, 2793, 2805,
	2811, 2817, 2820, 2832, 2842, 2854, 2890, 2902, 2921, 2923, 2978, 3010, 3012, 3026, 3081, 3083,
	3085, 3097, 3099, 3120, 3136, 3152, 3159, 3188, 3210, 3228, 3234, 3245, 3250, 3256, 3264, 3276,
	3281, 3296, 3349, 3363, 3378, 3392, 3395, 3420, 3440, 3461, 3488, 3529, 3531, 3584, 3588, 3591,
	3600, 3602, 3614, 3616, 3628, 3634, 3650, 3657, 3668, 3683, 3685, 3713, 3716, 3720, 3726, 3729,
	3736, 3753, 3778, 3802, 3805, 3819, 3841, 3845, 3851, 3856, 3880, 3922, 3938, 3970, 3993, 4032,
}

type iq3Tables struct {
	gq    [512][4]float32 // grid points as floats
	grid  [512]uint32
	kmap  [4096]int32
	neigh []uint16
}

var (
	iq3Once sync.Once
	iq3Tab  iq3Tables
)

func iq3Init() *iq3Tables {
	iq3Once.Do(func() {
		t := &iq3Tab
		for k := 0; k < 512; k++ {
			var g uint32
			for i := 0; i < 4; i++ {
				l := uint32(kgrid512[k]>>(3*uint(i))) & 7
				g |= (2*l + 1) << (8 * uint(i))
			}
			t.grid[k] = g
			for i := 0; i < 4; i++ {
				t.gq[k][i] = float32(int8(g >> (8 * uint(i))))
			}
		}
		for i := range t.kmap {
			t.kmap[i] = -1
		}
		for i := 0; i < 512; i++ {
			t.kmap[kgrid512[i]] = int32(i)
		}
		type dj struct{ d, j int32 }
		order := func(pos *[4]int32) []dj {
			ds := make([]dj, 512)
			for j := 0; j < 512; j++ {
				var d2 int32
				for k := 0; k < 4; k++ {
					pg := int32(int8(t.grid[j] >> (8 * uint(k))))
					d2 += (pg - pos[k]) * (pg - pos[k])
				}
				ds[j] = dj{d2, int32(j)}
			}
			sort.Slice(ds, func(a, b int) bool { return ds[a].d < ds[b].d || ds[a].d == ds[b].d && ds[a].j < ds[b].j })
			return ds
		}
		const nwant = 3
		for i := 0; i < 4096; i++ {
			if t.kmap[i] >= 0 {
				continue
			}
			var pos [4]int32
			for k := 0; k < 4; k++ {
				pos[k] = 2*int32(i>>(3*uint(k))&7) + 1
			}
			ds := order(&pos)
			t.kmap[i] = -int32(len(t.neigh) + 1)
			start := len(t.neigh)
			t.neigh = append(t.neigh, 0)
			d2, nhave, n := ds[0].d, 1, 0
			for j := 0; j < 512; j++ {
				if ds[j].d > d2 {
					if nhave == nwant {
						break
					}
					d2 = ds[j].d
					nhave++
				}
				t.neigh = append(t.neigh, uint16(ds[j].j))
				n++
			}
			t.neigh[start] = uint16(n)
		}
	})
	return &iq3Tab
}

// Contraction sites of the IQ3_S quantizer (see rowIQ3S): a set bit means the site is a fused multiply-add.
const (
	fx3Sumx2   = 1 << iota // sumx2 += x*x
	fx3Sigma               // sigma2 + x*x
	fx3Scale               // 15 + is*0.2f
	fx3Round               // id*x - 1 inside nearest_int(0.5f*(...))
	fx3Dist                // scale*q - x in the neighbour search
	fx3DistAcc             // d2 += (w*diff)*diff
	fx3Qx                  // sumqx += (w*x)*q
	fx3Q2                  // sumq2 += (w*q)*q
	fx3All     = 1<<iota - 1
)

type iq3Scratch struct { // the C arrays live across rows and blocks: stale L is observable
	L [32]int8
}

func iq3Find(t *iq3Tables, start int32, xval, waux []float32, scale float32, L []int8, fx uint32) int {
	nb := t.neigh[start:]
	n := int(nb[0])
	best := float32(3.4028234663852886e+38)
	gi := -1
	for j := 1; j <= n; j++ {
		var d2 float32
		g := &t.gq[nb[j]]
		for i := 0; i < 4; i++ {
			q := g[i]
			var diff float32
			if fx&fx3Dist != 0 {
				diff = fma32(scale, q, -xval[i])
			} else {
				diff = mul(scale, q) - xval[i]
			}
			if fx&fx3DistAcc != 0 {
				d2 = fma32(mul(waux[i], diff), diff, d2)
			} else {
				d2 += mul(mul(waux[i], diff), diff)
			}
		}
		if d2 < best {
			best, gi = d2, int(nb[j])
		}
	}
	for i := 0; i < 4; i++ {
		L[i] = (int8(t.grid[gi]>>(8*uint(i))) - 1) / 2
	}
	return gi
}

// iq3Ni is nearest_int(0.5f*(id*x-1)).
func iq3Ni(fx uint32, id, x float32) int32 {
	var t float32
	if fx&fx3Round != 0 {
		t = fma32(id, x, -1)
	} else {
		t = mul(id, x) - 1
	}
	return nearestInt(mul(0.5, t))
}

func rowIQ3S(st *iq3Scratch, fx uint32, x []float32, y []byte, qw []float32) {
	const bsz, bs4, bs8, kMaxQ = 32, 8, 4, 8
	t := iq3Init()
	L := &st.L
	var scales [8]float32
	var weight, xval, waux [32]float32
	var Laux [32]int8
	var onGrid, onGridAux [8]bool
	var blockSigns [4]uint8
	for ibl := 0; ibl*256 < len(x); ibl++ {
		xbl, yb := x[ibl*256:(ibl+1)*256], y[ibl*110:(ibl+1)*110]
		clear(yb)
		qs, qh, signs, scl := yb[2:66], yb[66:74], yb[74:106], yb[106:110]
		qo, so := 0, 0
		var maxScale, sumx2 float32
		for i := 0; i < 256; i++ {
			sumx2 = madd(fx&fx3Sumx2 != 0, xbl[i], xbl[i], sumx2)
		}
		sigma2 := mul(2, sumx2) / 256
		for ib := 0; ib < 8; ib++ {
			xb := xbl[ib*bsz : (ib+1)*bsz]
			if qw != nil {
				q := qw[ibl*256+ib*bsz:]
				for i := 0; i < bsz; i++ {
					weight[i] = mul(q[i], sqrt(madd(fx&fx3Sigma != 0, xb[i], xb[i], sigma2)))
				}
			} else {
				for i := 0; i < bsz; i++ {
					weight[i] = mul(xb[i], xb[i])
				}
			}
			for i := 0; i < bsz; i++ {
				waux[i] = sqrt(weight[i])
			}
			for k := 0; k < bs8; k++ {
				var s uint8
				for i := 0; i < 8; i++ {
					if xb[8*k+i] >= 0 {
						xval[8*k+i] = xb[8*k+i]
					} else {
						xval[8*k+i] = -xb[8*k+i]
						s |= 1 << uint(i)
					}
				}
				blockSigns[k] = s
			}
			max := xval[0]
			for i := 1; i < bsz; i++ {
				if xval[i] > max {
					max = xval[i]
				}
			}
			if max == 0 {
				scales[ib] = 0
				continue // the C code skips the qs/signs advance here as well
			}
			var best float32
			scale := max / (2*kMaxQ - 1)
			for k := 0; k < bs4; k++ {
				onGrid[k] = false
			}
			for is := -9; is <= 9; is++ {
				id := madd(fx&fx3Scale != 0, float32(is), 0.2, 2*kMaxQ-1) / max
				thisScale := 1 / id
				for k := 0; k < bs4; k++ {
					for i := 0; i < 4; i++ {
						Laux[4*k+i] = int8(clamp(iq3Ni(fx, id, xval[4*k+i]), 0, kMaxQ-1))
					}
					var u uint16
					for i := 0; i < 4; i++ {
						u |= uint16(Laux[4*k+i]) << (3 * uint(i))
					}
					onGridAux[k] = true
					if gi := t.kmap[u]; gi < 0 {
						onGridAux[k] = false
						iq3Find(t, -gi-1, xval[4*k:], waux[4*k:], thisScale, Laux[4*k:], fx)
					}
				}
				var sumqx, sumq2 float32
				for i := 0; i < bsz; i++ {
					w := weight[i]
					q := float32(2*int32(Laux[i]) + 1)
					sumqx = madd(fx&fx3Qx != 0, mul(w, xval[i]), q, sumqx)
					sumq2 = madd(fx&fx3Q2 != 0, mul(w, q), q, sumq2)
				}
				if sumq2 > 0 && mul(sumqx, sumqx) > mul(best, sumq2) {
					scale = sumqx / sumq2
					best = mul(scale, sumqx)
					copy(L[:], Laux[:])
					onGrid = onGridAux
				}
			}
			notOn := 0
			for k := 0; k < bs4; k++ {
				if !onGrid[k] {
					notOn++
				}
			}
			if notOn > 0 && scale > 0 {
				id := 1 / scale
				for k := 0; k < bs4; k++ {
					var u uint16
					for i := 0; i < 4; i++ {
						l := clamp(iq3Ni(fx, id, xval[4*k+i]), 0, kMaxQ-1)
						u |= uint16(l) << (3 * uint(i))
					}
					gi := t.kmap[u]
					var idx int
					if gi < 0 {
						idx = iq3Find(t, -gi-1, xval[4*k:], waux[4*k:], scale, L[4*k:], fx)
					} else {
						idx = int(gi)
					}
					for i := 0; i < 4; i++ {
						L[4*k+i] = (int8(t.grid[idx]>>(8*uint(i))) - 1) / 2
					}
				}
				var sumqx, sumq2 float32
				for i := 0; i < bsz; i++ {
					w := weight[i]
					q := float32(2*int32(L[i]) + 1)
					sumqx = madd(fx&fx3Qx != 0, mul(w, xval[i]), q, sumqx)
					sumq2 = madd(fx&fx3Q2 != 0, mul(w, q), q, sumq2)
				}
				if sumq2 > 0 {
					scale = sumqx / sumq2
				}
			}
			if scale < 0 {
				scale = -scale
				for k := 0; k < bs8; k++ {
					blockSigns[k] = ^blockSigns[k]
				}
			}
			for k := 0; k < bs4; k++ {
				var u uint16
				for i := 0; i < 4; i++ {
					u |= uint16(L[4*k+i]) << (3 * uint(i))
				}
				gi := t.kmap[u]
				if gi < 0 {
					panic("qcore: IQ3_S point not on grid") // GGML_ABORT in the producer
				}
				qs[qo+k] = byte(gi)
				qh[(ib*bs4+k)/8] |= byte(gi>>8) << uint((ib*bs4+k)%8)
			}
			qo += bs4
			copy(signs[so:], blockSigns[:])
			so += bs8
			scales[ib] = scale
			if scale > maxScale {
				maxScale = scale
			}
		}
		if maxScale == 0 {
			continue
		}
		d := maxScale / 31
		put16(yb, F16(mul(d, 1.033)))
		id := 1 / d
		for ib := 0; ib < 8; ib += 2 {
			l1 := clamp(iq3Ni(fx, id, scales[ib]), 0, 15)
			l2 := clamp(iq3Ni(fx, id, scales[ib+1]), 0, 15)
			scl[ib/2] = byte(l1 | l2<<4)
		}
	}
}

// iq3FMA is the set of contracted sites of the GCC -O3 -mfma build (derived against the compiled producer).
var iq3FMA uint32 = fx3All &^ fx3Sumx2

func iq3Mask(fm bool) uint32 {
	if fm {
		return iq3FMA
	}
	return 0
}
