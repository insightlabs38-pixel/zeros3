package ir

import (
	"errors"

	"zeros3-z3/deflate"
)

// The predictive codec (Codec Pred) arrives with Z3-07; this placeholder keeps
// the codec identifier and the Package diagnostics stable and makes every use
// of the codec fail cleanly instead of silently.

var errNoPredictor = errors.New("ir: predictive codec not available")

// Profile parameterises candidate generation (see Z3-07).
type Profile struct {
	ID   uint8
	Name string
}

// Profiles is the predictor profile set (empty until Z3-07).
var Profiles = []Profile{}

// PredStats are diagnostics gathered while encoding with the predictor.
type PredStats struct {
	Tokens, Matches, Escapes int
	IdxHist                  [8]int
	Work                     int64
}

type predictor struct{ Stats PredStats }

func newPredictor(plain []byte, profile uint8) *predictor { return &predictor{} }

func (p *predictor) encodeUnit(runs []Run, start, plainLen int, cost *[NumCats]float64) ([]byte, error) {
	return nil, errNoPredictor
}

func (p *predictor) decodeUnit(data []byte, start, plainLen int) ([]Run, error) {
	return nil, errNoPredictor
}

// ChooseProfile picks a predictor profile for a stream.
func ChooseProfile(s *deflate.Stream, pol Policy) uint8 { return 0 }

func lengthCode(l int) (sym int, extra uint32) {
	lo, hi := 0, 28
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if deflate.LengthBase(257+mid) <= l {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if l == 258 {
		return 285, 0
	}
	return 257 + lo, uint32(l - deflate.LengthBase(257+lo))
}

// canonical reports whether t is the canonical encoding of its length/distance.
func canonical(t deflate.Token) bool {
	s, e := lengthCode(t.Length())
	return s == t.Sym() && e == t.LenExtra()
}
