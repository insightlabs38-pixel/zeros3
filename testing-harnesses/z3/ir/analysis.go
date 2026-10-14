package ir

import (
	"math"
	"math/bits"

	"zeros3-z3/deflate"
)

// Entropy holds static (non-adaptive) empirical statistics of a token
// stream's reconstruction fields. Literal values are excluded: they live in
// the semantic plaintext.
type Entropy struct {
	Tokens, Matches, Literals, Runs int
	LenSym                          [29]int
	DistCode                        [30]int
	LenExtraBits, DistExtraBits     int64
	LitRunBits                      [24]int // histogram of bit length of (run+1)
	LitRunMantBits                  int64
	// recent-distance caches: hits per depth (index = MTF position), misses
	RepHits   [16]int
	RepMisses [4]int // by cache depth 1,4,8,16
	// CondLenSym[b][s]: length symbol given the literal-run bucket before it
	CondLenSym  [4][29]int
	CondDist    [3][30]int
	NearbyDelta int // matches whose distance equals previous distance +-1..16
}

func entropyOf(h []int) (bitsTotal float64, n int) {
	for _, c := range h {
		n += c
	}
	for _, c := range h {
		if c > 0 {
			bitsTotal -= float64(c) * math.Log2(float64(c)/float64(n))
		}
	}
	return
}

// Analyze scans a parsed stream.
func Analyze(s *deflate.Stream) *Entropy {
	e := &Entropy{}
	var cache [16]int
	lastDist := 0
	lits := 0
	flush := func() {
		x := lits + 1
		nb := bits.Len(uint(x)) - 1
		e.LitRunBits[min(nb, 23)]++
		e.LitRunMantBits += int64(nb)
		e.Runs++
	}
	for bi := range s.Blocks {
		for _, t := range s.Blocks[bi].Tokens {
			e.Tokens++
			if t.IsLiteral() {
				lits++
				e.Literals++
				continue
			}
			e.Matches++
			flush()
			ls := t.Sym() - 257
			e.LenSym[ls]++
			e.LenExtraBits += int64(deflate.LengthExtraBits(t.Sym()))
			e.CondLenSym[litBucket(lits)][ls]++
			dc := t.DistCode()
			d := t.Distance()
			e.DistCode[dc]++
			e.DistExtraBits += int64(deflate.DistExtraBits(dc))
			e.CondDist[(&e2model{}).distCtx(t.Length())][dc]++
			hit := -1
			for i, c := range cache {
				if c == d {
					hit = i
					break
				}
			}
			if hit >= 0 {
				e.RepHits[hit]++
			}
			for k, depth := range []int{1, 4, 8, 16} {
				if hit < 0 || hit >= depth {
					e.RepMisses[k]++
				}
			}
			if lastDist != 0 && d != lastDist && abs(d-lastDist) <= 16 {
				e.NearbyDelta++
			}
			lastDist = d
			if hit < 0 {
				hit = len(cache) - 1
			}
			copy(cache[1:hit+1], cache[:hit])
			cache[0] = d
			lits = 0
		}
	}
	if lits > 0 {
		flush()
	}
	return e
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Bits reports ideal static code lengths (bits) under several models.
type Bits struct {
	LitRun, LenSym, LenExtra, DistSym, DistExtra float64
	Order0Total                                  float64 // sum of order-0 field entropies
	Cond                                         float64 // with literal-run / length-bucket conditioning
	RepModel                                     map[int]float64
}

// StaticBits computes the entropies.
func (e *Entropy) StaticBits() Bits {
	var b Bits
	lr, _ := entropyOf(e.LitRunBits[:])
	b.LitRun = lr + float64(e.LitRunMantBits)
	b.LenSym, _ = entropyOf(e.LenSym[:])
	b.LenExtra = float64(e.LenExtraBits)
	b.DistSym, _ = entropyOf(e.DistCode[:])
	b.DistExtra = float64(e.DistExtraBits)
	b.Order0Total = b.LitRun + b.LenSym + b.LenExtra + b.DistSym + b.DistExtra
	cond := b.LitRun + b.LenExtra + b.DistExtra
	for i := range e.CondLenSym {
		x, _ := entropyOf(e.CondLenSym[i][:])
		cond += x
	}
	for i := range e.CondDist {
		x, _ := entropyOf(e.CondDist[i][:])
		cond += x
	}
	b.Cond = cond
	b.RepModel = map[int]float64{}
	for k, depth := range []int{1, 4, 8, 16} {
		miss := e.RepMisses[k]
		hits := e.Matches - miss
		// cost of the hit/miss flag plus index among hits plus the miss payload
		flag, _ := entropyOf([]int{hits, miss})
		idx := make([]int, 0, depth)
		for i := 0; i < depth && i < len(e.RepHits); i++ {
			idx = append(idx, e.RepHits[i])
		}
		ib, _ := entropyOf(idx)
		missFrac := 0.0
		if e.Matches > 0 {
			missFrac = float64(miss) / float64(e.Matches)
		}
		b.RepModel[depth] = b.LitRun + b.LenSym + b.LenExtra + flag + ib + missFrac*(b.DistSym+b.DistExtra)
		_ = ib
	}
	return b
}
