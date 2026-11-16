package brotliz3

import (
	"math"
	"math/bits"
)

// log2Int is a platform-independent, correctly rounded log2 of an integer (double-double
// evaluation of 2*atanh((m-1)/(m+1))/ln 2 after scaling m into [sqrt(1/2), sqrt(2))). The C
// encoder calls libm's log2 here; glibc's result is the correctly rounded one for all but about
// one integer in 10^4, and Go's math.Log2 is neither that nor identical across architectures.
// Every operation below is an exactly specified IEEE operation (math.FMA included).

type dd struct{ hi, lo float64 }

func twoSum(a, b float64) dd {
	s := a + b
	bb := s - a
	return dd{s, (a - (s - bb)) + (b - bb)}
}

func ddAdd(a, b dd) dd {
	s := twoSum(a.hi, b.hi)
	return twoSum(s.hi, s.lo+(a.lo+b.lo))
}

func ddMul(a, b dd) dd {
	p := a.hi * b.hi
	e := math.FMA(a.hi, b.hi, -p)
	e += math.FMA(a.hi, b.lo, a.lo*b.hi)
	return twoSum(p, e)
}

func ddDiv(a, b dd) dd {
	q1 := a.hi / b.hi
	r := ddAdd(a, ddMul(dd{-q1, 0}, b))
	q2 := r.hi / b.hi
	r = ddAdd(r, ddMul(dd{-q2, 0}, b))
	q3 := r.hi / b.hi
	return ddAdd(twoSum(q1, q2), dd{q3, 0})
}

var invLn2 = dd{1.4426950408889634, 2.0355273740931033e-17}

var log2Cache [4096]struct {
	v uint64
	r float64
}

func log2Int(v uint64) float64 {
	c := &log2Cache[v&4095]
	if c.v == v && v != 0 {
		return c.r
	}
	e := bits.Len64(v) - 1
	if v == 1<<uint(e) {
		return float64(e)
	}
	// m = v / 2^k in [sqrt(1/2), sqrt(2)); exact for v < 2^53
	m := math.Ldexp(float64(v), -e)
	if m > math.Sqrt2 {
		m = math.Ldexp(m, -1)
		e++
	}
	s := ddDiv(twoSum(m, -1), twoSum(m, 1))
	s2 := ddMul(s, s)
	term, sum := s, s
	for k := 1; k <= 24; k++ {
		term = ddMul(term, s2)
		sum = ddAdd(sum, ddDiv(term, dd{float64(2*k + 1), 0}))
	}
	l := ddMul(ddAdd(sum, sum), invLn2)
	r := ddAdd(dd{float64(e), 0}, l)
	c.v, c.r = v, r.hi
	return r.hi
}
