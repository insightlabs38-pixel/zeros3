package gitz3

import (
	"fmt"

	"zeros3-z3/lab"
)

// Admission is the laboratory's per-pack chooser. It never rejects a pack: the
// answer is a layout or "cdc" (ordinary production-shaped CDC of the original bytes),
// which is also the answer for every parse, resolve, checksum, limit or
// reconstruction failure.
type Admission struct {
	Layout string  // "cdc" | "seq-zlib" | "seq-auto" | "set-auto" | "set-plain"
	Reason string  // why
	QStar  float64 // break-even probability of a churn event for the chosen layout (premium / expected saving), -1 if n/a
}

// Premiums are first-ingest costs, relative to CDC, from a dry run on an empty store.
type Premiums struct {
	CDC                       int64
	SeqZlib, SeqAuto          int64
	SetAuto, SetPlain         int64
	EntriesMedianZlib         int
	Entries                   int
	ExactDEFLATEOK, Eligibles int
}

// DryRun costs the layouts on an empty store (production pack rule).
// idxFile (optional) is the pack's index: raw CDC pays for it, the exact layouts regenerate it
// (one flag byte) when it is byte-identical.
func DryRun(an *PackAnalysis, packFile, idxFile []byte) (Premiums, error) {
	var pr Premiums
	pr.Entries = len(an.Entries)
	cost := func(pl *lab.Plan) (int64, error) {
		ac := lab.NewAcct(96, true)
		a, err := ac.Add(pl)
		if err != nil {
			return 0, err
		}
		return a.PackedNew + a.Layouts["table"] + a.PlanMetaBytes + a.IndexBytes, nil
	}
	cdc, err := baseline("cdc", File{Name: "x.pack", Kind: "pack", Data: packFile})
	if err != nil {
		return pr, err
	}
	if pr.CDC, err = cost(cdc); err != nil {
		return pr, err
	}
	var idxExact int64
	if len(idxFile) > 0 {
		ic, err := baseline("cdc", File{Name: "x.idx", Kind: "idx", Data: idxFile})
		if err != nil {
			return pr, err
		}
		c, err := cost(ic)
		if err != nil {
			return pr, err
		}
		pr.CDC += c
		if bi, err := BuildIdx(an.P); err == nil && string(bi) == string(idxFile) {
			idxExact = 1
		} else {
			idxExact = c
		}
	}
	o := RepOpts{}
	for _, v := range []struct {
		dst  *int64
		mode string
		set  bool
	}{{&pr.SeqZlib, "zlib", false}, {&pr.SeqAuto, "auto", false}, {&pr.SetAuto, "auto", true}, {&pr.SetPlain, "plain", true}} {
		o.Mode = v.mode
		var pl *lab.Plan
		if v.set {
			pl, _ = an.SetPlan(o)
		} else {
			pl, _ = an.SeqPlan(o)
		}
		if *v.dst, err = cost(pl); err != nil {
			return pr, err
		}
		*v.dst += idxExact
	}
	sizes := make([]int, 0, len(an.Entries))
	for i := range an.Entries {
		sizes = append(sizes, len(an.Entries[i].Z))
		if an.Entries[i].IROK {
			pr.ExactDEFLATEOK++
		}
	}
	pr.EntriesMedianZlib = median(sizes)
	return pr, nil
}

func median(v []int) int {
	if len(v) == 0 {
		return 0
	}
	c := append([]int(nil), v...)
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j] < c[j-1]; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
	return c[len(c)/2]
}

// ExpectedSaving is the measured fraction of a pack's CDC cost that an exact
// layout avoids when the pack is rewritten with the same objects (reordered,
// rebased, repacked after a small revision): 0.75-0.99 across the Z3-16
// measurements; 0.7 is used as a conservative planning value.
const ExpectedSaving = 0.7

// Admit chooses a layout for a pack given q, the probability that the stored pack
// will later be superseded by a rewrite of mostly the same objects. The choice is
// the layout with the least expected cost, premium + (1-q) * 0 - q * saving, and
// is CDC when no layout's break-even probability is below q. There is no fixed
// size threshold: the crossover is the break-even probability reported with each
// choice.
func Admit(pack, idx []byte, a Algo, q float64) (Admission, error) {
	p, err := ParsePack(pack, a, Limits{})
	if err != nil {
		return Admission{"cdc", "fallback: " + FallbackReason(err), -1}, nil
	}
	if _, err := p.Resolve(); err != nil {
		return Admission{"cdc", "fallback: " + FallbackReason(err), -1}, nil
	}
	an, err := AnalyzePack(p)
	if err != nil {
		return Admission{"cdc", "fallback: analysis failed", -1}, nil
	}
	pr, err := DryRun(an, pack, idx)
	if err != nil {
		return Admission{"cdc", "fallback: dry run failed", -1}, nil
	}
	best := Admission{"cdc", "no exact layout pays at q", -1}
	bestCost := 0.0
	for _, c := range []struct {
		name string
		cost int64
	}{{"seq-zlib", pr.SeqZlib}, {"seq-auto", pr.SeqAuto}, {"set-auto", pr.SetAuto}, {"set-plain", pr.SetPlain}} {
		prem := float64(c.cost - pr.CDC)
		save := ExpectedSaving * float64(pr.CDC)
		// expected cost relative to CDC: premium paid always, saving only with probability q
		exp := prem - q*save
		if exp < bestCost {
			bestCost = exp
			best = Admission{c.name, fmt.Sprintf("premium %+.1f%% of CDC, expected net %+.1f%%", 100*prem/float64(pr.CDC), 100*exp/float64(pr.CDC)), prem / save}
		}
	}
	return best, nil
}
