package mdelta2

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"zeros3-z3/simdelta"
)

// Pick names one base tensor.
type Pick struct{ Slot, TIdx int }

// EncOpts selects which predictor families the archival encoder may try.
type EncOpts struct {
	Coders   []int // final residual coders to try (default flate+nrc)
	Narrow   bool  // allow exact F32 -> F16/BF16 coding
	Affine   bool
	Low      []int // low-rank ladder (empty = off)
	Lin      bool  // two/three-base linear combinations
	Segments bool  // per-segment (block) base choice
	SegElems int   // segment size in elements (default 1<<20)
	Workers  int
	// Hint supplies the converter's tensor reordering for float->quantized reproduction.
	Hint func(name string) PermHint
	// RangeSeg forces tensors above this many elements into independent segments of that size
	// (bounded range-read amplification at a small coding cost). 0 = off.
	RangeSeg int
}

type BaseSrc struct {
	SHA      SHA
	Len      int64
	M        *Model
	TDepth   []int // base-hop depth of every tensor of this base (nil = all 0)
	GapDepth int   // hop depth of this base's header bytes (delta chain)
}

// MaxHops is the bound on base hops per tensor (StoreResolver refuses deeper chains).
const MaxHops = MaxDepth - 1

func (b BaseSrc) depth(i int) int {
	if b.TDepth == nil || i >= len(b.TDepth) {
		return 0
	}
	return b.TDepth[i]
}

// TStat is the per-tensor outcome.
type TStat struct {
	Name     string
	Mode     string // copy | direct | pred:<kind>
	Coder    int
	Bytes    int // stream+params bytes
	Raw      int64
	Segs     int
	EncMS    int64
	Narrowed bool
	Depth    int
}

type Stats struct {
	T          []TStat
	Total      int
	ModeBytes  map[string]int
	EncodeSecs float64
	TDepth     []int // per tensor (model order)
	GapDepth   int
}

type option struct {
	name   string
	p      *Params
	picks  []Pick
	nbases []Base
	est    float64
}

// nrcProxy estimates NRC payload bytes (shift mode, one context) from a histogram: entropy of the
// shifted bit-length symbol plus raw mantissa bits; no allocation of a stream.
// NRCProxy is the entropy-based NRC size estimate.
func NRCProxy(d DType, resid, pred []byte) float64 { return nrcProxy(d, resid, pred) }

func nrcProxy(d DType, resid, pred []byte) float64 {
	w := d.Width()
	n := len(resid) / w
	var emax int
	for i := 0; i < n; i++ {
		if x := expField(d, le(pred, w, i)); x > emax {
			emax = x
		}
	}
	eb := emax - 63
	if eb < 0 {
		eb = 0
	}
	var cnt [nrcSyms]int
	raw := 0
	for i := 0; i < n; i++ {
		r := le(resid, w, i)
		s := bitlen(r)
		e := expField(d, le(pred, w, i)) - eb
		if e < 0 {
			e = 0
		}
		cnt[s+e]++
		if s > 1 {
			raw += s - 1
		}
	}
	h := 0.0
	for _, c := range cnt {
		if c > 0 {
			h -= float64(c) * math.Log2(float64(c)/float64(n))
		}
	}
	tab := 0
	for _, c := range cnt {
		if c > 0 {
			tab += 3
		}
	}
	return (h+float64(raw))/8 + float64(tab) + 16
}

func segLayout(t *Tensor, nElems int, segElems int) [][2]int {
	_, cols := t.RowsCols()
	c := int(cols)
	if c <= 0 {
		c = 1
	}
	rowsPer := segElems / c
	if rowsPer < 1 {
		rowsPer = 1
	}
	step := rowsPer * c
	var out [][2]int
	for s := 0; s < nElems; s += step {
		e := s + step
		if e > nElems {
			e = nElems
		}
		out = append(out, [2]int{s, e})
	}
	return out
}

type planner struct {
	m     *Model
	bases []BaseSrc
	o     EncOpts
}

func (o *EncOpts) coders() []int {
	if len(o.Coders) == 0 {
		return []int{CFlate, CNRC, CFPE}
	}
	return o.Coders
}

// encodeCoded picks the best coder for (resid, pred) and returns the stream.
func (pl *planner) encodeCoded(d DType, p *Params, resid, pred []byte) (int, []byte) {
	best, bc := []byte(nil), -1
	for _, c := range pl.o.coders() {
		s, ok := EncodeResidual(c, d, p.Res, resid, pred)
		if !ok {
			continue
		}
		if best == nil || len(s) < len(best) {
			best, bc = s, c
		}
	}
	return bc, best
}

// options builds candidate predictors for a (coding-dtype) segment.
func (pl *planner) options(cd DType, shape []int64, data []byte, picks [][]Pick, getBase func(Pick, int, int) Base, lo, hi int) []option {
	var out []option
	w := cd.Width()
	_ = w
	for _, ps := range picks {
		var bs []Base
		for _, p := range ps {
			bs = append(bs, getBase(p, lo, hi))
		}
		if len(ps) == 1 {
			out = append(out, option{name: "id-key", p: &Params{Kind: KID, Res: RKey, NB: 1, Prec: 4}, picks: ps, nbases: bs})
			out = append(out, option{name: "id-xor", p: &Params{Kind: KID, Res: RXor, NB: 1, Prec: 4}, picks: ps, nbases: bs})
			if cd.IsFloat() && pl.o.Affine {
				for _, g := range []Gran{GTensor, GRow} {
					out = append(out, option{name: "aff", p: FitAffineRobust(cd, shape, g, 0, 4, bs[0], data), picks: ps, nbases: bs})
				}
			}
			if cd.IsFloat() && len(pl.o.Low) > 0 && len(shape) == 2 && shape[0] >= 16 && shape[1] >= 16 && int(shape[0]*shape[1]) <= 48<<20 {
				for _, r := range pl.o.Low {
					out = append(out, option{name: fmt.Sprintf("low%d", r), p: LowRankFit(cd, shape, r, 4, bs[0], data, 2, 1), picks: ps, nbases: bs})
				}
			}
		} else if cd.IsFloat() && pl.o.Lin {
			out = append(out, option{name: "lin", p: FitLin(cd, shape, GTensor, 4, bs, data), picks: ps, nbases: bs})
			out = append(out, option{name: "lin-row", p: FitLin(cd, shape, GRow, 4, bs, data), picks: ps, nbases: bs})
		}
	}
	return out
}

// planSeg chooses the cheapest representation of one segment of coding-dtype data.
func (pl *planner) planSeg(cd DType, shape []int64, data []byte, picks [][]Pick, getBase func(Pick, int, int) Base, lo, hi int) (Seg, int) {
	w := cd.Width()
	n := len(data) / w
	// direct baseline
	zero := make([]byte, len(data))
	dp := &Params{Kind: KNone, Res: RXor, Prec: 4}
	dc, ds := pl.encodeCoded(cd, dp, data, zero)
	bestSeg := Seg{Elems: int64(n), Mode: ModeCoded, Params: dp, Coder: uint8(dc), Stream: ds}
	bestCost := len(ds) + len(dp.Marshal()) + 4
	opts := pl.options(cd, shape, data, picks, getBase, lo, hi)
	for i := range opts {
		o := &opts[i]
		pred, err := Predict(o.p, cd, shape, o.nbases)
		if err != nil {
			o.est = math.Inf(1)
			continue
		}
		res := MakeResidual(o.p.Res, w, data, pred)
		if cd.IsFloat() {
			o.est = nrcProxy(cd, res, pred)
		} else {
			o.est = float64(DeflateLen(res))
		}
		o.est += float64(len(o.p.Marshal()))
	}
	sort.SliceStable(opts, func(i, j int) bool { return opts[i].est < opts[j].est })
	for i := 0; i < len(opts) && i < 3; i++ {
		o := opts[i]
		if o.est >= float64(bestCost)*1.05 {
			break
		}
		pred, _ := Predict(o.p, cd, shape, o.nbases)
		res := MakeResidual(o.p.Res, w, data, pred)
		c, s := pl.encodeCoded(cd, o.p, res, pred)
		cost := len(s) + len(o.p.Marshal()) + 6*len(o.picks) + 4
		if cost < bestCost {
			bestCost = cost
			refs := make([]BaseRef, len(o.picks))
			for k, p := range o.picks {
				refs[k] = BaseRef{Slot: uint8(p.Slot), TIdx: uint32(p.TIdx), Off: int64(lo)}
			}
			bestSeg = Seg{Elems: int64(n), Mode: ModeCoded, Params: o.p, Refs: refs, Coder: uint8(c), Stream: s}
			bestSeg.Params.NB = uint8(len(o.picks))
		}
	}
	return bestSeg, bestCost
}

// EncodeModel builds the container of m given bases and per-tensor candidate base picks.
func EncodeModel(m *Model, bases []BaseSrc, cands func(ti int) [][]Pick, o EncOpts) ([]byte, *Stats, error) {
	t0 := time.Now()
	pl := &planner{m: m, bases: bases, o: o}
	if o.SegElems == 0 {
		o.SegElems = 1 << 20
	}
	if o.Workers == 0 {
		o.Workers = 4
	}
	gaps, spans, err := m.Gaps()
	if err != nil {
		return nil, nil, err
	}
	_ = spans
	order := make([]int, len(m.Tensors))
	for i := range order {
		order[i] = i
	}
	sortBy(order, func(a, b int) bool { return m.Tensors[a].Begin < m.Tensors[b].Begin })
	c := &Container{FileLen: m.Size, Gaps: gaps, GapsLen: int64(len(gaps))}
	for _, b := range bases {
		c.Bases = append(c.Bases, BaseInfo{b.SHA, b.Len})
	}
	// header/padding bytes: delta against the best base's own gaps when that is smaller
	bestGap := len(Deflate(gaps))
	gapDepth := 0
	for si, b := range bases {
		if b.GapDepth+1 > MaxHops {
			continue
		}
		bg, _, err := b.M.Gaps()
		if err != nil || len(bg) == 0 || len(gaps) == 0 {
			continue
		}
		if w, err := simdelta.Encode("adaptive", [][]byte{bg}, gaps); err == nil && len(w)+8 < bestGap {
			bestGap, c.GapWire, c.GapSlot = len(w)+8, w, uint8(si)
			gapDepth = b.GapDepth + 1
		}
	}
	// the file digest
	fb := make([]byte, m.Size)
	if _, err := m.ReadAt(fb, 0); err != nil {
		return nil, nil, err
	}
	c.FileSHA = sum(fb)
	fb = nil

	recs := make([]TRec, len(order))
	stats := make([]TStat, len(order))
	var wg sync.WaitGroup
	sem := make(chan struct{}, o.Workers)
	var emu sync.Mutex
	var firstErr error
	for pos, ti := range order {
		wg.Add(1)
		sem <- struct{}{}
		go func(pos, ti int) {
			defer wg.Done()
			defer func() { <-sem }()
			rec, st, err := pl.planTensor(ti, cands(ti))
			if err != nil {
				emu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				emu.Unlock()
				return
			}
			recs[pos], stats[pos] = rec, st
		}(pos, ti)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, nil, firstErr
	}
	prev := int64(0)
	for pos, ti := range order {
		t := &m.Tensors[ti]
		recs[pos].GapBefore = t.Begin - prev
		prev = t.End
		c.T = append(c.T, recs[pos])
	}
	c.TailGap = m.Size - prev
	st := &Stats{T: stats, ModeBytes: map[string]int{}}
	st.GapDepth = gapDepth
	st.TDepth = make([]int, len(stats))
	for i, ts := range stats {
		st.TDepth[order[i]] = ts.Depth
	}
	for _, s := range stats {
		st.Total += s.Bytes
		st.ModeBytes[s.Mode] += s.Bytes
	}
	out := c.Marshal()
	st.EncodeSecs = time.Since(t0).Seconds()
	return out, st, nil
}

// planTensor decides one tensor (all of: copy, narrowing, whole-tensor vs segmented prediction).
func (pl *planner) planTensor(ti int, picks [][]Pick) (TRec, TStat, error) {
	t0 := time.Now()
	picks = pl.depthOK(picks)
	rec, st, err := pl.planTensor0(ti, picks, t0)
	for _, sg := range rec.Segs {
		for _, r := range sg.Refs {
			if d := pl.bases[r.Slot].depth(int(r.TIdx)) + 1; d > st.Depth {
				st.Depth = d
			}
		}
	}
	return rec, st, err
}

// depthOK drops candidates that would exceed the hop bound.
func (pl *planner) depthOK(picks [][]Pick) [][]Pick {
	var out [][]Pick
	for _, ps := range picks {
		ok := true
		for _, p := range ps {
			if pl.bases[p.Slot].depth(p.TIdx)+1 > MaxHops {
				ok = false
			}
		}
		if ok {
			out = append(out, ps)
		}
	}
	return out
}

func (pl *planner) planTensor0(ti int, picks [][]Pick, t0 time.Time) (TRec, TStat, error) {
	t := &pl.m.Tensors[ti]
	data, err := pl.m.Read(ti)
	if err != nil {
		return TRec{}, TStat{}, err
	}
	rec := TRec{DType: t.DType, Shape: t.Shape, Len: int64(len(data))}
	st := TStat{Name: t.Name, Raw: int64(len(data))}
	// exact copy of an identical base tensor
	for _, ps := range picks {
		if len(ps) != 1 {
			continue
		}
		bt := &pl.bases[ps[0].Slot].M.Tensors[ps[0].TIdx]
		if bt.Size() != t.Size() || bt.DType != t.DType {
			continue
		}
		bd, err := pl.bases[ps[0].Slot].M.Read(ps[0].TIdx)
		if err == nil && bytes.Equal(bd, data) {
			rec.Segs = []Seg{{Elems: int64(len(data) / t.DType.Width()), Mode: ModeCopy, Refs: []BaseRef{{Slot: uint8(ps[0].Slot), TIdx: uint32(ps[0].TIdx), Tag: tag(bd)}}}}
			st.Mode, st.Bytes = "copy", 14
			st.EncMS = time.Since(t0).Milliseconds()
			return rec, st, nil
		}
	}
	if t.DType.IsQuant() {
		return pl.planQuant(ti, t, data, picks, rec, st, t0)
	}
	cd := t.DType
	narrowed := DType(0)
	if pl.o.Narrow && cd == DF32 {
		if nd, ok := Narrow(data, DF16); ok {
			narrowed, cd, data = DF16, DF16, nd
		} else if nd, ok := Narrow(data, DBF16); ok {
			narrowed, cd, data = DBF16, DBF16, nd
		}
	}
	// base readers in the coding dtype domain (bases keep their own dtype; Base.at converts)
	cache := map[Pick][]byte{}
	var cmu sync.Mutex
	getBase := func(p Pick, lo, hi int) Base {
		cmu.Lock()
		d, ok := cache[p]
		cmu.Unlock()
		bm := pl.bases[p.Slot].M
		if !ok {
			d, _ = bm.Read(p.TIdx)
			cmu.Lock()
			cache[p] = d
			cmu.Unlock()
		}
		bdt := bm.Tensors[p.TIdx].DType
		bw := bdt.Width()
		return Base{DT: bdt, Data: d[lo*bw : hi*bw]}
	}
	// restrict candidate picks to element-compatible, float-compatible bases
	var ok [][]Pick
	nElems := len(data) / cd.Width()
	for _, ps := range picks {
		good := true
		for _, p := range ps {
			bt := &pl.bases[p.Slot].M.Tensors[p.TIdx]
			if int(bt.Elems()) != nElems || (cd.IsFloat() != bt.DType.IsFloat()) || (!cd.IsFloat() && bt.DType != cd) {
				good = false
			}
		}
		if good {
			ok = append(ok, ps)
		}
	}
	tshape := t.Shape
	whole, wholeCost := pl.planSeg(cd, tshape, data, ok, getBase, 0, nElems)
	best := []Seg{whole}
	bestCost := wholeCost
	if pl.o.RangeSeg > 0 && nElems > pl.o.RangeSeg {
		var segs []Seg
		cost := 0
		for _, r := range segLayout(t, nElems, pl.o.RangeSeg) {
			w := cd.Width()
			s, c := pl.planSeg(cd, segShape(tshape, int64(r[1]-r[0])), data[r[0]*w:r[1]*w], ok, getBase, r[0], r[1])
			segs = append(segs, s)
			cost += c + 3
		}
		best, bestCost = segs, cost
	} else if pl.o.Segments && len(ok) > 1 && nElems >= 2*pl.o.SegElems {
		var segs []Seg
		cost := 0
		for _, r := range segLayout(t, nElems, pl.o.SegElems) {
			w := cd.Width()
			s, c := pl.planSeg(cd, segShape(tshape, int64(r[1]-r[0])), data[r[0]*w:r[1]*w], ok, getBase, r[0], r[1])
			segs = append(segs, s)
			cost += c + 3
		}
		if cost < bestCost {
			best, bestCost = segs, cost
		}
	}
	for i := range best {
		best[i].Narrow = narrowed
		if len(best[i].Refs) > 0 {
			for k := range best[i].Refs {
				r := &best[i].Refs[k]
				bm := pl.bases[r.Slot].M
				bw := int64(bm.Tensors[r.TIdx].DType.Width())
				bd := getBase(Pick{int(r.Slot), int(r.TIdx)}, 0, int(bm.Tensors[r.TIdx].Elems()))
				r.Tag = tag(bd.Data)
				_ = bw
			}
		}
	}
	rec.Segs = best
	st.Mode, st.Bytes, st.Segs = "direct", bestCost, len(best)
	if len(best) == 1 {
		st.Coder = int(best[0].Coder)
	}
	for _, s := range best {
		if s.Params != nil && s.Params.Kind != KNone {
			st.Mode = fmt.Sprintf("pred:%d", s.Params.Kind)
		}
	}
	st.Narrowed = narrowed != 0
	st.EncMS = time.Since(t0).Milliseconds()
	return rec, st, nil
}

// ModelResolver serves base tensors straight from opened files (used for in-encoder proof).
type ModelResolver map[SHA]*Model

func (r ModelResolver) Tensor(base SHA, idx int) (DType, []byte, error) {
	m, ok := r[base]
	if !ok {
		return 0, nil, ErrMissing
	}
	if idx < 0 || idx >= len(m.Tensors) {
		return 0, nil, ErrFormat
	}
	d, err := m.Read(idx)
	return m.Tensors[idx].DType, d, err
}

func (r ModelResolver) Window(base SHA, idx int, lo, hi int64) (DType, []byte, error) {
	m, ok := r[base]
	if !ok {
		return 0, nil, ErrMissing
	}
	if idx < 0 || idx >= len(m.Tensors) || lo < 0 || hi < lo || hi > m.Tensors[idx].Size() {
		return 0, nil, ErrFormat
	}
	b := make([]byte, hi-lo)
	_, err := m.ReadAt(b, m.Tensors[idx].Begin+lo)
	return m.Tensors[idx].DType, b, err
}

func (r ModelResolver) Gaps(base SHA) ([]byte, error) {
	m, ok := r[base]
	if !ok {
		return nil, ErrMissing
	}
	g, _, err := m.Gaps()
	return g, err
}

// planQuant chooses among typed quantized-lane streams for a block-quantized tensor.
func (pl *planner) planQuant(ti int, t *Tensor, data []byte, picks [][]Pick, rec TRec, st TStat, t0 time.Time) (TRec, TStat, error) {
	best := Seg{Elems: int64(len(data)), Mode: ModeQuant, Q: nil}
	var hint PermHint
	if pl.o.Hint != nil {
		hint = pl.o.Hint(t.Name)
	}
	for k, v := range quantCandidates(t.DType, data, nil, hint, t.Shape, nil) {
		_ = k
		best.Q = v
	}
	bestMode := "quant:fields"
	for _, ps := range picks {
		if len(ps) != 1 {
			continue
		}
		bm := pl.bases[ps[0].Slot].M
		bt := &bm.Tensors[ps[0].TIdx]
		bd, err := bm.Read(ps[0].TIdx)
		if err != nil {
			continue
		}
		var fb *Base
		var same []byte
		switch {
		case bt.DType == t.DType:
			same = bd
		case bt.DType.IsFloat():
			fb = &Base{DT: bt.DType, Data: bd}
		default:
			continue
		}
		for name, q := range quantCandidates(t.DType, data, fb, hint, t.Shape, same) {
			if name == "fields" {
				continue
			}
			if len(q)+8 < len(best.Q)+len(best.Refs)*8 {
				best.Q, bestMode = q, "quant:"+name
				best.Refs = []BaseRef{{Slot: uint8(ps[0].Slot), TIdx: uint32(ps[0].TIdx), Tag: tag(bd)}}
			}
		}
	}
	rec.Segs = []Seg{best}
	st.Mode, st.Bytes = bestMode, len(best.Q)+len(best.Refs)*8
	st.EncMS = time.Since(t0).Milliseconds()
	return rec, st, nil
}

// ShiftHist is the histogram of NRC shift-mode symbols (bit length + relative exponent).
func ShiftHist(d DType, resid, pred []byte) []float64 {
	w := d.Width()
	n := len(resid) / w
	er, _ := exps(d, pred)
	h := make([]float64, nrcSyms)
	for i := 0; i < n; i++ {
		h[bitlen(le(resid, w, i))+int(er[i])]++
	}
	return h
}
