package lz4z3

import (
	"encoding/binary"
)

// Recipe kinds.
const (
	KindExplicit  = 0 // full sequence list
	KindPredicted = 1 // predictor id + acceleration reproduces the block
	KindHybrid    = 2 // predictor reproduces the first K sequences; the rest is explicit
)

// Recipe is the reversible decision record of one LZ4 block.
type Recipe struct {
	Kind   int
	Pred   int
	Accel  int
	Prefix int   // KindHybrid: predicted sequences taken from the predictor
	Seqs   []Seq // KindExplicit: all sequences; KindHybrid: the explicit tail
}

// accelSweep lists the accelerations tried per predictor (producers use small values; the
// lz4 CLI --fast=N and LZ4_ACCELERATION_DEFAULT cover 1..64, wrappers occasionally use larger ones).
var accelSweep = func() []int {
	var a []int
	for i := 1; i <= 64; i++ {
		a = append(a, i)
	}
	return append(a, 96, 128, 192, 256, 512, 1024, 4096, 16384, 65537)
}()

// Analyze returns the cheapest verified recipe for block (decoded to plain with sequences
// seqs). hist must be empty for predictor kinds: predictors model independent blocks.
func Analyze(plain []byte, seqs []Seq, independent bool) Recipe {
	best := Recipe{Kind: KindExplicit, Seqs: seqs}
	if !independent {
		return best
	}
	bestLen := len(best.Marshal())
	bestPrefix, bestP := 0, Recipe{}
	for _, id := range []int{PredDefault, PredContinue, PredDefault5, PredPierrec} {
		for _, ac := range accelSweep {
			if id == PredPierrec && ac != 1 { // the Go producer has no acceleration parameter
				break
			}
			k, ok := predictPrefix(id, plain, ac, seqs)
			if !ok {
				continue
			}
			if k == len(seqs) {
				r := Recipe{Kind: KindPredicted, Pred: id, Accel: ac}
				if n := len(r.Marshal()); n < bestLen {
					best, bestLen = r, n
				}
				return best // exact predictor hits are as cheap as recipes get
			}
			if k > bestPrefix {
				bestPrefix, bestP = k, Recipe{Kind: KindHybrid, Pred: id, Accel: ac, Prefix: k, Seqs: seqs[k:]}
			}
		}
	}
	for lv := 3; lv <= 12; lv++ { // LZ4HC hash-chain levels 3..9 and optimal-parser levels 10..12
		if k, ok := predictPrefix(PredHC, plain, lv, seqs); ok {
			if k == len(seqs) {
				r := Recipe{Kind: KindPredicted, Pred: PredHC, Accel: lv}
				if n := len(r.Marshal()); n < bestLen {
					best = r
				}
				return best
			}
			if k > bestPrefix {
				bestPrefix, bestP = k, Recipe{Kind: KindHybrid, Pred: PredHC, Accel: lv, Prefix: k, Seqs: seqs[k:]}
			}
		}
	}
	if bestPrefix > 0 {
		if n := len(bestP.Marshal()); n < bestLen {
			best = bestP
		}
	}
	return best
}

func commonPrefix(a, b []Seq) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// Marshal serializes the recipe canonically.
func (r Recipe) Marshal() []byte {
	b := []byte{byte(r.Kind)}
	switch r.Kind {
	case KindPredicted:
		b = append(b, byte(r.Pred))
		b = binary.AppendUvarint(b, uint64(r.Accel))
	case KindHybrid:
		b = append(b, byte(r.Pred))
		b = binary.AppendUvarint(b, uint64(r.Accel))
		b = binary.AppendUvarint(b, uint64(r.Prefix))
		b = appendSeqs(b, r.Seqs)
	default:
		b = appendSeqs(b, r.Seqs)
	}
	return b
}

func appendSeqs(b []byte, s []Seq) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	for i, q := range s {
		b = binary.AppendUvarint(b, uint64(q.Lit))
		if q.Match != 0 || i != len(s)-1 {
			b = binary.AppendUvarint(b, uint64(q.Match-minMatch))
			b = binary.AppendUvarint(b, uint64(q.Off))
		}
	}
	return b
}

// Unmarshal parses a serialized recipe; sizes are bounded by lim and the remaining input.
func Unmarshal(b []byte, lim Limits) (Recipe, int, error) {
	lim = lim.norm()
	var r Recipe
	if len(b) < 1 {
		return r, 0, recipeErr("empty recipe")
	}
	r.Kind = int(b[0])
	i := 1
	uv := func() (uint64, bool) {
		v, n := binary.Uvarint(b[i:])
		if n <= 0 {
			return 0, false
		}
		i += n
		return v, true
	}
	switch r.Kind {
	case KindExplicit:
	case KindPredicted, KindHybrid:
		if len(b) < 2 {
			return r, 0, recipeErr("truncated predictor id")
		}
		r.Pred = int(b[1])
		i = 2
		a, ok := uv()
		if !ok || a < 1 || a > 1<<20 {
			return r, 0, recipeErr("acceleration")
		}
		r.Accel = int(a)
		if r.Kind == KindHybrid {
			p, ok := uv()
			if !ok || p > uint64(lim.MaxSeqs) {
				return r, 0, recipeErr("prefix")
			}
			r.Prefix = int(p)
		}
	default:
		return r, 0, recipeErr("kind %d", r.Kind)
	}
	if r.Kind != KindPredicted {
		n, ok := uv()
		if !ok || n > uint64(lim.MaxSeqs) || n > uint64(len(b)-i) {
			return r, 0, recipeErr("sequence count")
		}
		r.Seqs = make([]Seq, n)
		for k := range r.Seqs {
			l, ok := uv()
			if !ok || l > uint64(lim.MaxDecoded) {
				return r, 0, recipeErr("literal run")
			}
			r.Seqs[k].Lit = int(l)
			if k == len(r.Seqs)-1 {
				break // the final sequence is literal-only
			}
			m, ok1 := uv()
			o, ok2 := uv()
			if !ok1 || !ok2 || m > uint64(lim.MaxDecoded) || o < 1 || o > maxOff {
				return r, 0, recipeErr("match fields")
			}
			r.Seqs[k].Match, r.Seqs[k].Off = int(m)+minMatch, int(o)
		}
	}
	return r, i, nil
}

// Replay rebuilds the compressed block from the payload (and optional linked history).
func (r Recipe) Replay(hist, plain []byte, lim Limits) ([]byte, error) {
	lim = lim.norm()
	var seqs []Seq
	switch r.Kind {
	case KindExplicit:
		seqs = r.Seqs
	case KindPredicted, KindHybrid:
		if len(hist) != 0 {
			return nil, recipeErr("predictor on a linked block")
		}
		p, ok := Predict(r.Pred, plain, r.Accel)
		if !ok {
			return nil, recipeErr("unknown predictor %d", r.Pred)
		}
		if r.Kind == KindPredicted {
			seqs = p
		} else {
			if r.Prefix > len(p) {
				return nil, recipeErr("prefix %d beyond %d predicted sequences", r.Prefix, len(p))
			}
			seqs = append(append([]Seq(nil), p[:r.Prefix]...), r.Seqs...)
		}
	default:
		return nil, recipeErr("kind %d", r.Kind)
	}
	if len(seqs) > lim.MaxSeqs || len(plain) > lim.MaxDecoded {
		return nil, ErrLimit
	}
	return BuildH(hist, plain, seqs)
}

// Analyzer is Analyze with memory: producers use one encoder profile per file, so the profile
// that reproduced the previous block is tried first (one encoder run instead of a sweep), and
// after GiveUp consecutive blocks that no predictor reproduces the sweep is skipped: an
// unsupported producer then costs one explicit recipe per block and no encoder runs.
type Analyzer struct {
	GiveUp   int // 0 = never give up
	pred     int
	accel    int
	miss     int
	Hits     int // blocks reproduced by the remembered profile
	Sweeps   int // blocks that needed the full sweep
	Rejected int // blocks handled without any predictor run
}

// Analyze returns the cheapest verified recipe for the block (see the package-level Analyze).
func (a *Analyzer) Analyze(plain []byte, seqs []Seq, independent bool) Recipe {
	if !independent {
		return Recipe{Kind: KindExplicit, Seqs: seqs}
	}
	if a.pred != PredNone {
		if k, ok := predictPrefix(a.pred, plain, a.accel, seqs); ok && k == len(seqs) {
			a.Hits++
			a.miss = 0
			return Recipe{Kind: KindPredicted, Pred: a.pred, Accel: a.accel}
		}
	}
	if a.GiveUp > 0 && a.miss >= a.GiveUp {
		a.Rejected++
		return Recipe{Kind: KindExplicit, Seqs: seqs}
	}
	a.Sweeps++
	r := Analyze(plain, seqs, true)
	if r.Kind == KindPredicted {
		a.pred, a.accel, a.miss = r.Pred, r.Accel, 0
	} else {
		a.miss++
	}
	return r
}
