package dcore

import "sort"

// Candidate is one possible set of delta bases for a target.
type Candidate struct {
	ID    int
	Bases [][]byte
}

// SelectOpts configures candidate selection.
type SelectOpts struct {
	Params    Params
	FrameSpan int64
	// Finalists caps exact encodings in staged mode (0 = unlimited, bounded by
	// the stop rule).
	Finalists int
	// Slack widens the stop rule: a candidate is exact-encoded while its
	// Lower <= best exact * (1+Slack).
	Slack float64
	Est   EstimateOpts
	// Exhaustive exact-encodes every candidate (the oracle).
	Exhaustive bool
	// MinSaving rejects a delta unless exact <= direct * (1-MinSaving).
	MinSaving float64
}

// Selection is the outcome of choosing among candidates.
type Selection struct {
	Best          int // index into the candidate slice, -1 = keep the prior form
	Size          int64
	Ops           []Op
	Direct        int64 // exact size of the target stored without bases
	Matched       int   // candidates matched
	ExactEncoded  int   // exact (deflating) encodings executed
	RejectedEarly int   // candidates never exactly encoded
	Estimates     []Estimate
	Exact         []int64 // per candidate; 0 where not computed
}

func directSize(t []byte, fs int64) int64 {
	return EnvelopeSize([]Op{{N: int64(len(t)), Base: -1, Lit: t}}, fs)
}

// Select chooses the delta candidate with the smallest exact envelope, using
// either exhaustive exact costing or the staged estimator. In both modes the
// returned program is exact: only the ranking differs, so a wrong estimate can
// reject a better candidate (regret) but never yields an invalid delta.
func Select(target []byte, cands []Candidate, o SelectOpts) (Selection, error) {
	if o.Params.Window == 0 {
		o.Params = DefaultParams()
	}
	sel := Selection{Best: -1, Estimates: make([]Estimate, len(cands)), Exact: make([]int64, len(cands))}
	ops := make([][]Op, len(cands))
	for i, c := range cands {
		x, err := BuildIndex(c.Bases, o.Params)
		if err != nil {
			continue // an unsupported base can never win; the prior form remains
		}
		ops[i] = x.Match(target)
		sel.Matched++
		sel.Estimates[i] = EstimateProgram(ops[i], o.Est)
	}
	order := make([]int, 0, len(cands))
	for i := range cands {
		if ops[i] != nil {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool { return sel.Estimates[order[a]].Est < sel.Estimates[order[b]].Est })
	sel.Direct = directSize(target, o.FrameSpan)
	bestExact := int64(-1)
	for rank, i := range order {
		if !o.Exhaustive && bestExact >= 0 {
			limit := float64(bestExact) * (1 + o.Slack)
			if float64(sel.Estimates[i].Lower) > limit || (o.Finalists > 0 && rank >= o.Finalists) {
				sel.RejectedEarly++
				continue
			}
		}
		n := EnvelopeSize(ops[i], o.FrameSpan)
		sel.Exact[i] = n
		sel.ExactEncoded++
		if bestExact < 0 || n < bestExact {
			bestExact = n
			sel.Best, sel.Size, sel.Ops = i, n, ops[i]
		}
	}
	if sel.Best >= 0 && float64(sel.Size) > float64(sel.Direct)*(1-o.MinSaving) {
		sel.Best, sel.Ops = -1, nil
	}
	return sel, nil
}
