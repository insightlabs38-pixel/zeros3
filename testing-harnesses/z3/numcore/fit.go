package numcore

import (
	"math"
	"math/rand"
)

// sums accumulates least-squares moments over finite pairs.
type sums struct{ n, sb, st, sbb, sbt float64 }

func (s *sums) add(b, t float64) {
	if !Finite(b) || !Finite(t) {
		return
	}
	s.n++
	s.sb += b
	s.st += t
	s.sbb += b * b
	s.sbt += b * t
}

// solve returns (a, c) minimizing sum (t - a*b - c)^2 subject to the mode.
func (s *sums) solve(mode int) (a, c float64) {
	if s.n == 0 {
		return 1, 0
	}
	switch mode {
	case 1: // scale only
		if s.sbb == 0 {
			return 1, 0
		}
		return s.sbt / s.sbb, 0
	case 2: // offset only
		return 1, (s.st - s.sb) / s.n
	}
	den := s.n*s.sbb - s.sb*s.sb
	if den <= 1e-300 {
		return 1, (s.st - s.sb) / s.n
	}
	a = (s.n*s.sbt - s.sb*s.st) / den
	c = (s.st - a*s.sb) / s.n
	return
}

// FitAffine fits per-tensor / block / row affine coefficients (mode 0 a+c, 1 scale only, 2 offset only).
func FitAffine(tdt DType, shape []int64, g Gran, block int, mode int, prec uint8, base Base, target []byte) *Params {
	w := tdt.Width()
	n := len(target) / w
	rows, cols := 1, n
	if len(shape) > 0 && shape[len(shape)-1] > 0 {
		cols = int(shape[len(shape)-1])
		rows = n / cols
	}
	_ = rows
	p := &Params{Kind: KAff, Res: RKey, Gran: g, Block: uint32(block), NB: 1, Prec: prec}
	blk := affBlock(p, rows, cols, n)
	nb := (n + blk - 1) / blk
	p.Coef = make([]float32, 2*nb)
	for k := 0; k < nb; k++ {
		var s sums
		hi := (k + 1) * blk
		if hi > n {
			hi = n
		}
		for i := k * blk; i < hi; i++ {
			s.add(base.at(i), ToF64(tdt, LE(target, w, i)))
		}
		a, c := s.solve(mode)
		p.Coef[2*k], p.Coef[2*k+1] = roundCoef(a, prec), roundCoef(c, prec)
	}
	return p
}

// FitLin fits t ~ sum a_k b_k + c (per tensor, or per row) by normal equations.
func FitLin(tdt DType, shape []int64, g Gran, prec uint8, bases []Base, target []byte) *Params {
	w := tdt.Width()
	n := len(target) / w
	m := len(bases)
	rows, cols := 1, n
	if len(shape) > 0 && shape[len(shape)-1] > 0 {
		cols = int(shape[len(shape)-1])
		rows = n / cols
	}
	ng := 1
	if g == GRow {
		ng = rows
	}
	p := &Params{Kind: KLin, Res: RKey, Gran: g, NB: uint8(m), Prec: prec}
	p.Coef = make([]float32, ng*(m+1))
	d := m + 1
	span := n / ng
	for gi := 0; gi < ng; gi++ {
		A := make([]float64, d*d)
		y := make([]float64, d)
		x := make([]float64, d)
		for i := gi * span; i < (gi+1)*span; i++ {
			ok := true
			for k := 0; k < m; k++ {
				x[k] = bases[k].at(i)
				ok = ok && Finite(x[k])
			}
			x[m] = 1
			t := ToF64(tdt, LE(target, w, i))
			if !ok || !Finite(t) {
				continue
			}
			for r := 0; r < d; r++ {
				y[r] += x[r] * t
				for c := 0; c < d; c++ {
					A[r*d+c] += x[r] * x[c]
				}
			}
		}
		sol := solveSPD(A, y, d)
		for k := 0; k < d; k++ {
			p.Coef[gi*d+k] = roundCoef(sol[k], prec)
		}
	}
	return p
}

// solveSPD solves A x = y with ridge regularization and Gauss elimination (A is d x d).
func solveSPD(A, y []float64, d int) []float64 {
	M := make([]float64, d*(d+1))
	tr := 0.0
	for i := 0; i < d; i++ {
		tr += A[i*d+i]
	}
	ridge := 1e-12*tr/float64(d) + 1e-300
	for i := 0; i < d; i++ {
		for j := 0; j < d; j++ {
			M[i*(d+1)+j] = A[i*d+j]
		}
		M[i*(d+1)+i] += ridge
		M[i*(d+1)+d] = y[i]
	}
	for c := 0; c < d; c++ {
		piv := c
		for r := c + 1; r < d; r++ {
			if math.Abs(M[r*(d+1)+c]) > math.Abs(M[piv*(d+1)+c]) {
				piv = r
			}
		}
		if M[piv*(d+1)+c] == 0 {
			continue
		}
		for j := 0; j <= d; j++ {
			M[c*(d+1)+j], M[piv*(d+1)+j] = M[piv*(d+1)+j], M[c*(d+1)+j]
		}
		for r := 0; r < d; r++ {
			if r == c {
				continue
			}
			f := M[r*(d+1)+c] / M[c*(d+1)+c]
			for j := c; j <= d; j++ {
				M[r*(d+1)+j] -= f * M[c*(d+1)+j]
			}
		}
	}
	x := make([]float64, d)
	for i := 0; i < d; i++ {
		if M[i*(d+1)+i] != 0 {
			x[i] = M[i*(d+1)+d] / M[i*(d+1)+i]
		}
	}
	// fall back to identity-like coefficients if the solve produced garbage
	for _, v := range x {
		if !Finite(v) {
			for i := range x {
				x[i] = 0
			}
			x[0] = 1
			break
		}
	}
	return x
}

// LowRankFit returns a rank-r approximation D ~ U V^T of the float delta (target - base) using
// randomized subspace iteration; factors are rounded to prec. rows*cols must fit memory.
func LowRankFit(tdt DType, shape []int64, r int, prec uint8, base Base, target []byte, iters int, seed int64) *Params {
	w := tdt.Width()
	n := len(target) / w
	cols := int(shape[len(shape)-1])
	rows := n / cols
	D := make([]float32, n)
	for i := 0; i < n; i++ {
		b, t := base.at(i), ToF64(tdt, LE(target, w, i))
		d := t - b
		if !Finite(d) {
			d = 0
		}
		D[i] = float32(d)
	}
	if r > rows {
		r = rows
	}
	if r > cols {
		r = cols
	}
	U, V := svdTrunc(D, rows, cols, r, iters, seed)
	p := &Params{Kind: KLow, Res: RKey, NB: 1, Rank: uint16(r), Prec: prec, Coef: make([]float32, (rows+cols)*r)}
	for i := 0; i < rows*r; i++ {
		p.Coef[i] = roundCoef(U[i], prec)
	}
	for i := 0; i < cols*r; i++ {
		p.Coef[rows*r+i] = roundCoef(V[i], prec)
	}
	return p
}

// svdTrunc: D (rows x cols) ~ U V^T with U (rows x r, includes sqrt singular values), V (cols x r).
func svdTrunc(D []float32, rows, cols, r, iters int, seed int64) (U, V []float64) {
	rng := rand.New(rand.NewSource(seed))
	k := r + 4
	if k > cols {
		k = cols
	}
	if k > rows {
		k = rows
	}
	// Y = D * Omega (rows x k)
	Q := make([]float64, rows*k)
	om := make([]float64, cols*k)
	for i := range om {
		om[i] = rng.NormFloat64()
	}
	mulD := func(Xc []float64, kk int, out []float64) { // out(rows x kk) = D * Xc(cols x kk)
		for i := range out {
			out[i] = 0
		}
		for i := 0; i < rows; i++ {
			row := D[i*cols : (i+1)*cols]
			o := out[i*kk : (i+1)*kk]
			for j, dv := range row {
				if dv == 0 {
					continue
				}
				d := float64(dv)
				x := Xc[j*kk : (j+1)*kk]
				for c := 0; c < kk; c++ {
					o[c] += d * x[c]
				}
			}
		}
	}
	mulDT := func(Xr []float64, kk int, out []float64) { // out(cols x kk) = D^T * Xr(rows x kk)
		for i := range out {
			out[i] = 0
		}
		for i := 0; i < rows; i++ {
			row := D[i*cols : (i+1)*cols]
			x := Xr[i*kk : (i+1)*kk]
			for j, dv := range row {
				if dv == 0 {
					continue
				}
				d := float64(dv)
				o := out[j*kk : (j+1)*kk]
				for c := 0; c < kk; c++ {
					o[c] += d * x[c]
				}
			}
		}
	}
	mulD(om, k, Q)
	orth(Q, rows, k)
	W := make([]float64, cols*k)
	for it := 0; it < iters; it++ {
		mulDT(Q, k, W)
		orth(W, cols, k)
		mulD(W, k, Q)
		orth(Q, rows, k)
	}
	// B = Q^T D  (k x cols) = (D^T Q)^T ; Z = D^T Q (cols x k)
	Z := make([]float64, cols*k)
	mulDT(Q, k, Z)
	// small SVD of B via eigen-decomposition of Z^T Z (k x k): B B^T = Z^T Z
	G := make([]float64, k*k)
	for j := 0; j < cols; j++ {
		z := Z[j*k : (j+1)*k]
		for a := 0; a < k; a++ {
			for b := 0; b < k; b++ {
				G[a*k+b] += z[a] * z[b]
			}
		}
	}
	ev, evec := jacobiEig(G, k) // B B^T = Ub S^2 Ub^T
	idx := make([]int, k)
	for i := range idx {
		idx[i] = i
	}
	for i := 0; i < k; i++ {
		for j := i + 1; j < k; j++ {
			if ev[idx[j]] > ev[idx[i]] {
				idx[i], idx[j] = idx[j], idx[i]
			}
		}
	}
	U = make([]float64, rows*r)
	V = make([]float64, cols*r)
	for c := 0; c < r; c++ {
		e := idx[c]
		s := math.Sqrt(math.Max(ev[e], 0))
		if s == 0 {
			continue
		}
		sq := math.Sqrt(s)
		// u = Q * Ub[:,e];  v = B^T Ub[:,e] / s = Z * Ub[:,e] / s
		for i := 0; i < rows; i++ {
			var acc float64
			for a := 0; a < k; a++ {
				acc += Q[i*k+a] * evec[a*k+e]
			}
			U[i*r+c] = acc * sq
		}
		for j := 0; j < cols; j++ {
			var acc float64
			for a := 0; a < k; a++ {
				acc += Z[j*k+a] * evec[a*k+e]
			}
			V[j*r+c] = acc / s * sq
		}
	}
	return
}

// orth orthonormalizes the k columns of X (m x k, row-major) by modified Gram-Schmidt.
func orth(X []float64, m, k int) {
	for c := 0; c < k; c++ {
		for p := 0; p < c; p++ {
			var d float64
			for i := 0; i < m; i++ {
				d += X[i*k+c] * X[i*k+p]
			}
			for i := 0; i < m; i++ {
				X[i*k+c] -= d * X[i*k+p]
			}
		}
		var nn float64
		for i := 0; i < m; i++ {
			nn += X[i*k+c] * X[i*k+c]
		}
		nn = math.Sqrt(nn)
		if nn < 1e-30 {
			for i := 0; i < m; i++ {
				X[i*k+c] = 0
			}
			continue
		}
		for i := 0; i < m; i++ {
			X[i*k+c] /= nn
		}
	}
}

// jacobiEig diagonalizes the symmetric k x k matrix A; returns eigenvalues and column eigenvectors.
func jacobiEig(A []float64, k int) ([]float64, []float64) {
	a := append([]float64(nil), A...)
	v := make([]float64, k*k)
	for i := 0; i < k; i++ {
		v[i*k+i] = 1
	}
	for sweep := 0; sweep < 60; sweep++ {
		off := 0.0
		for i := 0; i < k; i++ {
			for j := i + 1; j < k; j++ {
				off += a[i*k+j] * a[i*k+j]
			}
		}
		if off < 1e-24 {
			break
		}
		for p := 0; p < k; p++ {
			for q := p + 1; q < k; q++ {
				if math.Abs(a[p*k+q]) < 1e-300 {
					continue
				}
				th := (a[q*k+q] - a[p*k+p]) / (2 * a[p*k+q])
				t := 1 / (math.Abs(th) + math.Sqrt(th*th+1))
				if th < 0 {
					t = -t
				}
				c := 1 / math.Sqrt(t*t+1)
				s := t * c
				for i := 0; i < k; i++ {
					aip, aiq := a[i*k+p], a[i*k+q]
					a[i*k+p], a[i*k+q] = c*aip-s*aiq, s*aip+c*aiq
				}
				for i := 0; i < k; i++ {
					api, aqi := a[p*k+i], a[q*k+i]
					a[p*k+i], a[q*k+i] = c*api-s*aqi, s*api+c*aqi
				}
				for i := 0; i < k; i++ {
					vip, viq := v[i*k+p], v[i*k+q]
					v[i*k+p], v[i*k+q] = c*vip-s*viq, s*vip+c*viq
				}
			}
		}
	}
	ev := make([]float64, k)
	for i := range ev {
		ev[i] = a[i*k+i]
	}
	return ev, v
}

// groupCost is the sum of residual bit lengths of predicting target elements [lo,hi) (stride s)
// with pred = a*base + c: a cheap monotone proxy for coded size.
func groupCost(tdt DType, base Base, target []byte, lo, hi, stride int, a, c float32) int {
	w := tdt.Width()
	cost := 0
	for i := lo; i < hi; i += stride {
		b := base.at(i)
		var pv uint32
		if x := b; Finite(x) {
			y := float64(float64(a)*x) + float64(c)
			if Finite(y) {
				pv = FromF64(tdt, y)
			} else {
				pv = idPred(tdt, base, i)
			}
		} else {
			pv = idPred(tdt, base, i)
		}
		cost += Bitlen(Zigzag((Key(LE(target, w, i), w)-Key(pv, w))&Mask(w), w))
	}
	return cost
}

// FitAffineRobust chooses per group among identity, least-squares, scale-only and offset-only
// coefficients by the residual bit-length proxy instead of squared error (squared error is
// dominated by the few large movers and loses to identity on the typical element).
func FitAffineRobust(tdt DType, shape []int64, g Gran, block int, prec uint8, base Base, target []byte) *Params {
	w := tdt.Width()
	n := len(target) / w
	rows, cols := 1, n
	if len(shape) > 0 && shape[len(shape)-1] > 0 {
		cols = int(shape[len(shape)-1])
		rows = n / cols
	}
	p := &Params{Kind: KAff, Res: RKey, Gran: g, Block: uint32(block), NB: 1, Prec: prec}
	blk := affBlock(p, rows, cols, n)
	nb := (n + blk - 1) / blk
	p.Coef = make([]float32, 2*nb)
	for k := 0; k < nb; k++ {
		lo, hi := k*blk, (k+1)*blk
		if hi > n {
			hi = n
		}
		stride := 1
		if hi-lo > 1<<15 {
			stride = (hi - lo) >> 15
		}
		var s sums
		for i := lo; i < hi; i += stride {
			s.add(base.at(i), ToF64(tdt, LE(target, w, i)))
		}
		type cand struct{ a, c float32 }
		a0, c0 := s.solve(0)
		as, _ := s.solve(1)
		_, co := s.solve(2)
		cands := []cand{{1, 0}, {roundCoef(a0, prec), roundCoef(c0, prec)}, {roundCoef(as, prec), 0}, {1, roundCoef(co, prec)}}
		best, bc := cands[0], groupCost(tdt, base, target, lo, hi, stride, 1, 0)
		for _, c := range cands[1:] {
			if cc := groupCost(tdt, base, target, lo, hi, stride, c.a, c.c); cc < bc {
				best, bc = c, cc
			}
		}
		p.Coef[2*k], p.Coef[2*k+1] = best.a, best.c
	}
	return p
}
