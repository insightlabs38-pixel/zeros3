package numcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math"
	"sync"

	"zeros3-z3/qcore"
)

// Cand proposes a base tensor for a target tensor.
type Cand struct {
	Slot, TIdx int
	Perm       PermHint
}

// BaseIn is one candidate base object at encode time.
type BaseIn struct {
	ID       ID
	Len      int64
	Src      Source
	Depth    func(ti int) int // dependency depth of that tensor in the base's stored form (nil = 0)
	GapDepth int
}

// AuxIn is an auxiliary object (importance matrix) the encoder may reference.
type AuxIn struct {
	ID   ID
	Data []byte
}

// EncOpts configures the bounded-memory encoder.
type EncOpts struct {
	Window     int64 // float elements per window (default 1<<16)
	Workers    int   // parallel window coders (default 1)
	NoNarrow   bool  // disable exact F32 -> F16/BF16 coding
	Structured bool  // probe for exact affine / low-rank structure
	MaxStruct  int64 // largest tensor (bytes) loaded whole for a structured fit (default 32 MiB)
	Exhaustive bool  // oracle: choose each tensor's base by coding every candidate in full
	NoVerify   bool  // skip the decode-and-compare of every window
	SampleEl   int   // elements sampled per tensor for candidate ranking (default 8192)
}

// EncStats reports the outcome.
type EncStats struct {
	Bytes      int64 // object size
	GapBytes   int64
	DirBytes   int64
	Windows    int64
	ModeBytes  [5]int64
	ModeCount  [5]int64
	Narrowed   int64 // windows coded in a narrower dtype
	Structured int   // tensors with an affine / low-rank predictor
	QuantExact int64 // quant blocks reproduced
	QuantPatch int64 // quant blocks patched
	CandEvals  int64 // sampled candidate evaluations
	FullEvals  int64 // full candidate evaluations (oracle / structured)
	Depth      []int // per tensor dependency depth of the result
	GapDepth   int
	Picks      []int // per tensor chosen candidate index (-1 none)
}

type tplan struct {
	ref    *Ref
	src    Source
	p      *Params
	q      *QParams
	qw     []float32
	win    int64
	depth  int
	opaque bool
}

type encoder struct {
	ctx     context.Context
	tgt     io.ReaderAt
	lay     *Layout
	bases   []BaseIn
	cands   func(ti int) []Cand
	aux     []AuxIn
	im      []*qcore.IMatrix
	o       EncOpts
	st      *EncStats
	auxUsed map[int]int
	auxIDs  []ID
	mu      sync.Mutex
}

func (e *encoder) baseDepth(c Cand) int {
	b := &e.bases[c.Slot]
	if b.Depth == nil {
		return 0
	}
	return b.Depth(c.TIdx)
}

// sampleSpans picks evenly spaced 64-element chunks covering about `want` of n elements.
func sampleSpans(n int64, want int) [][2]int64 {
	const ch = 64
	if n <= int64(want) {
		return [][2]int64{{0, n}}
	}
	k := int64(want / ch)
	out := make([][2]int64, 0, k)
	for i := int64(0); i < k; i++ {
		lo := i * (n - ch) / (k - 1)
		out = append(out, [2]int64{lo, lo + ch})
	}
	return out
}

// fpeProxy estimates FPE payload bytes: exponent entropy plus raw sign/mantissa bits.
func fpeProxy(d DType, words []byte) float64 {
	sh, eb, ok := fpeSplit(d)
	if !ok {
		return float64(len(words))
	}
	w := d.Width()
	n := len(words) / w
	var cnt [256]int
	for i := 0; i < n; i++ {
		cnt[(LE(words, w, i)>>sh)&(1<<eb-1)]++
	}
	h := 0.0
	for _, c := range cnt {
		if c > 0 {
			h -= float64(c) * math.Log2(float64(c)/float64(n))
		}
	}
	return (h + float64(n)*float64(8*uint(w)-eb)) / 8
}

// nrcProxy estimates NRC payload bytes for a residual (shift mode, one context).
func nrcProxy(d DType, resid, pred []byte) float64 {
	w := d.Width()
	n := len(resid) / w
	er, _ := Exps(d, pred)
	var cnt [NRCSyms]int
	raw := 0
	for i := 0; i < n; i++ {
		s := Bitlen(LE(resid, w, i))
		cnt[s+int(er[i])]++
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
	return (h + float64(raw)) / 8
}

func (e *encoder) readTarget(t *Tensor, lo int64, n int) ([]byte, error) {
	w := int64(unitOf(t.DT, false))
	buf := make([]byte, int64(n)*w)
	return buf, readFull(e.tgt, buf, t.Off+lo*w)
}

// planFloat ranks candidates on a sample and returns the plan (nil ref = direct coding).
func (e *encoder) planFloat(ti int, t *Tensor) (*tplan, error) {
	w := int64(t.DT.Width())
	n := t.Len / w
	pl := &tplan{win: e.o.Window}
	cs := e.cands(ti)
	type cv struct {
		c    Cand
		src  Source
		dt   DType
		cost float64
		idx  int
	}
	var ok []cv
	for i, c := range cs {
		if c.Slot < 0 || c.Slot >= len(e.bases) || e.baseDepth(c)+1 > MaxHops7 {
			continue
		}
		dt, size, cols, has := e.bases[c.Slot].Src.Info(c.TIdx)
		if !has || !dt.IsFloat() || size/int64(dt.Width()) != n || size%int64(dt.Width()) != 0 {
			continue
		}
		if c.Perm.Kind != 0 && (cols <= 0 || t.Cols != cols || n%cols != 0) {
			continue
		}
		ok = append(ok, cv{c: c, src: e.bases[c.Slot].Src, dt: dt, idx: i})
	}
	e.st.Picks[ti] = -1
	if len(ok) == 0 || n == 0 {
		return pl, nil
	}
	needRows := false
	for i := range ok {
		needRows = needRows || ok[i].c.Perm.Kind != 0
	}
	evalSample := func(want int, list []cv) (float64, error) {
		spans := sampleSpans(n, want)
		if needRows { // row aligned so permuted bases can be sampled
			spans = rowSpans(n, t.Cols, want)
		}
		var tw []byte
		for _, sp := range spans {
			b, err := e.readTarget(t, sp[0], int(sp[1]-sp[0]))
			if err != nil {
				return 0, err
			}
			tw = append(tw, b...)
		}
		direct := fpeProxy(t.DT, tw)
		for i := range list {
			var pred []byte
			for _, sp := range spans {
				b, err := readRef(e.ctx, list[i].src, &Ref{Slot: uint8(list[i].c.Slot), TIdx: uint32(list[i].c.TIdx), Perm: list[i].c.Perm}, sp[0], int(sp[1]-sp[0]), t.DT, false)
				if err != nil {
					return 0, err
				}
				p, err := predWindow(nil, t.DT, b, sp[0], int(sp[1]-sp[0]), t.Cols)
				if err != nil {
					return 0, err
				}
				pred = append(pred, p...)
			}
			if bytes.Equal(pred, tw) {
				list[i].cost = 0
			} else {
				list[i].cost = nrcProxy(t.DT, MakeResidual(RKey, int(w), tw, pred), pred)
			}
			e.st.CandEvals++
		}
		return direct, nil
	}
	sortCV := func(l []cv) {
		for i := 1; i < len(l); i++ {
			for j := i; j > 0 && l[j].cost < l[j-1].cost; j-- {
				l[j], l[j-1] = l[j-1], l[j]
			}
		}
	}
	var direct float64
	var err error
	if e.o.Exhaustive {
		direct, err = e.fullCost(t, nil, nil)
		if err != nil {
			return nil, err
		}
		for i := range ok {
			r := &Ref{Slot: uint8(ok[i].c.Slot), TIdx: uint32(ok[i].c.TIdx), Perm: ok[i].c.Perm}
			if ok[i].cost, err = e.fullCost(t, r, ok[i].src); err != nil {
				return nil, err
			}
		}
	} else {
		if len(ok) > 3 { // stage 1: small sample over all candidates, keep the best three
			if _, err = evalSample(512, ok); err != nil {
				return nil, err
			}
			sortCV(ok)
			ok = ok[:3]
		}
		if direct, err = evalSample(e.o.SampleEl, ok); err != nil {
			return nil, err
		}
	}
	sortCV(ok)
	best := ok[0]
	if best.cost >= direct*0.98 {
		return pl, nil
	}
	e.st.Picks[ti] = best.idx
	pl.ref = &Ref{Slot: uint8(best.c.Slot), TIdx: uint32(best.c.TIdx), Perm: best.c.Perm}
	pl.src = best.src
	pl.depth = e.baseDepth(best.c) + 1
	if best.c.Perm.Kind != 0 { // permuted rows: windows are whole rows
		pl.win = (pl.win + t.Cols - 1) / t.Cols * t.Cols
	}
	if e.o.Structured && best.c.Perm.Kind == 0 && t.Cols > 1 && n/t.Cols > 1 && n%t.Cols == 0 && t.Len <= e.o.MaxStruct && best.cost > 0 {
		if err := e.planStructured(t, pl, best.dt); err != nil {
			return nil, err
		}
	}
	return pl, nil
}

// rowSpans samples whole rows when a row length is known (cols > 0), else element chunks.
func rowSpans(n, cols int64, want int) [][2]int64 {
	if cols <= 0 || n%cols != 0 || cols > int64(want) {
		if cols > 0 && n%cols == 0 { // long rows: a few whole rows
			rows := n / cols
			k := int64(2)
			if rows < k {
				k = rows
			}
			out := make([][2]int64, 0, k)
			for i := int64(0); i < k; i++ {
				r := i * (rows - 1) / maxI64(k-1, 1)
				out = append(out, [2]int64{r * cols, (r + 1) * cols})
			}
			return out
		}
		return sampleSpans(n, want)
	}
	rows := n / cols
	k := int64(want) / cols
	if k >= rows {
		return [][2]int64{{0, n}}
	}
	out := make([][2]int64, 0, k)
	for i := int64(0); i < k; i++ {
		r := i * (rows - 1) / maxI64(k-1, 1)
		out = append(out, [2]int64{r * cols, (r + 1) * cols})
	}
	return out
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// fullCost codes the whole tensor against ref (nil = direct) and returns the payload bytes.
func (e *encoder) fullCost(t *Tensor, ref *Ref, src Source) (float64, error) {
	td := &TDir{DT: t.DT, Off: t.Off, Len: t.Len, Cols: t.Cols, Win: e.o.Window, Ref: ref}
	if ref != nil && ref.Perm.Kind != 0 {
		td.Win = (td.Win + t.Cols - 1) / t.Cols * t.Cols
	}
	nw, _ := numWindows(t.Len, td.Win, td.unit())
	total := 0.0
	pl := &tplan{ref: ref, src: src, win: td.Win}
	for k := int64(0); k < nw; k++ {
		_, n := td.winBytes(k)
		buf := make([]byte, n)
		if err := readFull(e.tgt, buf, t.Off+k*td.Win*int64(td.unit())); err != nil {
			return 0, err
		}
		wd, payload, err := e.codeWindow(td, pl, k, buf, true)
		if err != nil {
			return 0, err
		}
		_ = wd
		total += float64(len(payload))
	}
	e.st.FullEvals++
	return total, nil
}

// planStructured loads one bounded tensor pair and tries exact affine / low-rank predictors when
// cheap probes say the update is structured. It keeps the identity predictor otherwise.
func (e *encoder) planStructured(t *Tensor, pl *tplan, bdt DType) error {
	w := t.DT.Width()
	n := int(t.Len) / w
	cols := int(t.Cols)
	rows := n / cols
	tw, err := e.readTarget(t, 0, n)
	if err != nil {
		return err
	}
	b, err := readRef(e.ctx, pl.src, pl.ref, 0, n, t.DT, false)
	if err != nil {
		return err
	}
	lowOK, affOK := probeLowRank(t.DT, b, tw, rows, cols), probeScale(t.DT, b, tw, n)
	if !lowOK && !affOK {
		return nil
	}
	e.st.FullEvals++
	shape := []int64{int64(rows), int64(cols)}
	cost := func(p *Params) float64 {
		pred, err := predWindow(p, t.DT, b, 0, n, t.Cols)
		if err != nil {
			return math.Inf(1)
		}
		c := nrcProxy(t.DT, MakeResidual(RKey, w, tw, pred), pred)
		if p != nil {
			c += float64(len(p.Marshal()))
		}
		return c
	}
	best, bestP := cost(nil), (*Params)(nil)
	try := func(p *Params) {
		if p == nil {
			return
		}
		if c := cost(p); c < best*0.97 {
			best, bestP = c, p
		}
	}
	if affOK {
		try(FitAffineRobust(t.DT, shape, GTensor, 0, 4, b, tw))
	}
	if lowOK {
		for _, r := range []int{1, 2, 4, 8, 16} {
			if r >= rows || r >= cols {
				break
			}
			try(LowRankFit(t.DT, shape, r, 4, b, tw, 2, 1))
		}
	}
	if bestP != nil {
		pl.p = bestP
		e.st.Structured++
	}
	return nil
}

// probeLowRank: a 48x48 sample of the float delta has most of its energy in a few directions.
// Natural optimizer noise has a flat spectrum and fails the test without any full-tensor work.
func probeLowRank(dt DType, b Base, tw []byte, rows, cols int) bool {
	const s = 48
	if rows < s || cols < s {
		return false
	}
	tb := Base{DT: dt, Data: tw}
	A := make([]float64, s*s)
	var e2 float64
	for i := 0; i < s; i++ {
		r := i * (rows - 1) / (s - 1)
		for j := 0; j < s; j++ {
			c := j * (cols - 1) / (s - 1)
			d := tb.at(r*cols+c) - b.at(r*cols+c)
			if !Finite(d) {
				return false
			}
			A[i*s+j] = d
			e2 += d * d
		}
	}
	if e2 == 0 {
		return false
	}
	// Gram matrix and its eigenvalues (squared singular values)
	G := make([]float64, s*s)
	for i := 0; i < s; i++ {
		for j := i; j < s; j++ {
			var v float64
			for k := 0; k < s; k++ {
				v += A[i*s+k] * A[j*s+k]
			}
			G[i*s+j], G[j*s+i] = v, v
		}
	}
	ev, _ := jacobiEig(G, s)
	for i := 1; i < len(ev); i++ { // descending
		for j := i; j > 0 && ev[j] > ev[j-1]; j-- {
			ev[j], ev[j-1] = ev[j-1], ev[j]
		}
	}
	top := 0.0
	for i := 0; i < 16 && i < len(ev); i++ {
		top += ev[i]
	}
	return top >= 0.9*e2
}

// probeScale: the target is a common multiple of the base on a sample (within two key steps).
func probeScale(dt DType, b Base, tw []byte, n int) bool {
	tb := Base{DT: dt, Data: tw}
	const s = 512
	if n < s {
		return false
	}
	ratios := make([]float64, 0, s)
	for i := 0; i < s; i++ {
		k := i * (n - 1) / (s - 1)
		bv, tv := b.at(k), tb.at(k)
		if bv != 0 && Finite(bv) && Finite(tv) {
			ratios = append(ratios, tv/bv)
		}
	}
	if len(ratios) < s/2 {
		return false
	}
	for i := 1; i < len(ratios); i++ {
		for j := i; j > 0 && ratios[j] < ratios[j-1]; j-- {
			ratios[j], ratios[j-1] = ratios[j-1], ratios[j]
		}
	}
	med := ratios[len(ratios)/2]
	if med == 1 || !Finite(med) {
		return false
	}
	w := dt.Width()
	hit := 0
	for i := 0; i < s; i++ {
		k := i * (n - 1) / (s - 1)
		pv := FromF64(dt, med*b.at(k))
		d := int64(Key(pv, w)) - int64(Key(LE(tw, w, k), w))
		if d >= -2 && d <= 2 {
			hit++
		}
	}
	return hit >= s*8/10
}

// planQuant finds the producer profile that reproduces a quantized tensor from a float base.
func (e *encoder) planQuant(ti int, t *Tensor) (*tplan, error) {
	q := mustQ(t.DT)
	el, by := qBlock(q)
	nb := t.Len / int64(by)
	n := nb * int64(el)
	cols := t.Cols
	if cols <= 0 {
		cols = n
	}
	if cols%int64(el) != 0 || n%cols != 0 || n == 0 {
		return nil, nil
	}
	rows := n / cols
	srows := maxI64(1, int64(8192)/cols)
	if srows > rows {
		srows = rows
	}
	sb := srows * cols / int64(el) // sample blocks
	want := make([]byte, sb*int64(by))
	if err := readFull(e.tgt, want, t.Off); err != nil {
		return nil, err
	}
	type variant struct {
		q   QParams
		qw  []float32
		aux int
	}
	bestExact, bestIdx := int64(-1), -1
	var bestV variant
	var bestC Cand
	cs := e.cands(ti)
	for ci, c := range cs {
		if c.Slot < 0 || c.Slot >= len(e.bases) || e.baseDepth(c)+1 > MaxHops7 {
			continue
		}
		src := e.bases[c.Slot].Src
		dt, size, bcols, has := src.Info(c.TIdx)
		if !has || !dt.IsFloat() || size/int64(dt.Width()) != n {
			continue
		}
		if c.Perm.Kind != 0 && bcols != cols {
			continue
		}
		b, err := readRef(e.ctx, src, &Ref{Slot: uint8(c.Slot), TIdx: uint32(c.TIdx), Perm: c.Perm}, 0, int(srows*cols), t.DT, true)
		if err != nil {
			return nil, err
		}
		vs := []variant{{q: QParams{Aux: -1}, aux: -1}}
		if dt != DF16 {
			vs = append(vs, variant{q: QParams{Aux: -1, ViaF16: true}, aux: -1})
		}
		for ai, im := range e.im {
			if w := im.For(t.Name, int(cols)); w != nil {
				for _, ar := range []qcore.Arith{qcore.Strict, qcore.GCCFMA} {
					vs = append(vs, variant{q: QParams{Arith: uint8(ar), Name: t.Name}, qw: w, aux: ai})
					if dt != DF16 {
						vs = append(vs, variant{q: QParams{Arith: uint8(ar), Name: t.Name, ViaF16: true}, qw: w, aux: ai})
					}
				}
			}
		}
		for _, v := range vs {
			got := make([]byte, len(want))
			if qcore.Quantize(got, q, floatsOf(b, v.q.ViaF16), int(cols), v.qw, qcore.Arith(v.q.Arith)) != nil {
				continue
			}
			ex := int64(0)
			for i := int64(0); i < sb; i++ {
				if bytes.Equal(got[i*int64(by):(i+1)*int64(by)], want[i*int64(by):(i+1)*int64(by)]) {
					ex++
				}
			}
			e.st.CandEvals++
			if ex > bestExact {
				bestExact, bestV, bestC, bestIdx = ex, v, c, ci
			}
			if ex == sb {
				break
			}
		}
		if bestExact == sb {
			break
		}
	}
	if bestIdx < 0 || bestExact*2 < sb {
		return nil, nil
	}
	pl := &tplan{src: e.bases[bestC.Slot].Src, depth: e.baseDepth(bestC) + 1}
	pl.ref = &Ref{Slot: uint8(bestC.Slot), TIdx: uint32(bestC.TIdx), Perm: bestC.Perm}
	qp := bestV.q
	if bestV.aux >= 0 {
		qp.Aux = e.useAux(bestV.aux)
		pl.qw = bestV.qw
	}
	pl.q = &qp
	rpw := maxI64(1, e.o.Window/cols) // rows per window
	pl.win = rpw * cols / int64(el)
	e.st.Picks[ti] = bestIdx
	return pl, nil
}

func (e *encoder) useAux(ai int) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if k, ok := e.auxUsed[ai]; ok {
		return k
	}
	k := len(e.auxIDs)
	e.auxUsed[ai] = k
	e.auxIDs = append(e.auxIDs, e.aux[ai].ID)
	return k
}

// planOpaque: identical bytes in a same-typed base tensor become copies; else stored/DEFLATE.
func (e *encoder) planOpaque(ti int, t *Tensor) (*tplan, error) {
	pl := &tplan{win: 4 << 20, opaque: true}
	e.st.Picks[ti] = -1
	if t.Len == 0 {
		return pl, nil
	}
	n := int64(4096)
	if n > t.Len {
		n = t.Len
	}
	want := make([]byte, n)
	if err := readFull(e.tgt, want, t.Off+(t.Len-n)/2); err != nil {
		return nil, err
	}
	for ci, c := range e.cands(ti) {
		if c.Slot < 0 || c.Slot >= len(e.bases) || c.Perm.Kind != 0 || e.baseDepth(c)+1 > MaxHops7 {
			continue
		}
		src := e.bases[c.Slot].Src
		dt, size, _, has := src.Info(c.TIdx)
		if !has || dt != t.DT || size != t.Len {
			continue
		}
		got := make([]byte, n)
		if err := src.ReadTensor(e.ctx, c.TIdx, (t.Len-n)/2, got); err != nil {
			return nil, err
		}
		if bytes.Equal(got, want) {
			pl.ref = &Ref{Slot: uint8(c.Slot), TIdx: uint32(c.TIdx)}
			pl.src = src
			pl.depth = e.baseDepth(c) + 1
			e.st.Picks[ti] = ci
			break
		}
	}
	return pl, nil
}

// codeWindow chooses and builds the payload of one window. dry skips verification (cost probes).
func (e *encoder) codeWindow(t *TDir, pl *tplan, k int64, tw []byte, dry bool) (WDir, []byte, error) {
	wd := WDir{Sum: sum16(tw)}
	var payload []byte
	var baseData Base
	haveBase := false
	base := func(ctx context.Context, lo int64, n int) (Base, error) {
		if haveBase {
			return baseData, nil
		}
		b, err := readRef(ctx, pl.src, t.Ref, lo, n, t.DT, t.Q != nil)
		if err == nil {
			baseData, haveBase = b, true
		}
		return b, err
	}
	switch {
	case t.Q != nil:
		_, by := qBlock(mustQ(t.DT))
		got, err := quantWindow(e.ctx, t, k, len(tw), base, pl.qw)
		if err != nil {
			return wd, nil, err
		}
		var np int
		payload, wd.Coder, np = encodePatches(tw, got, by)
		wd.Mode = MQuant
		if !dry {
			e.mu.Lock()
			e.st.QuantPatch += int64(np)
			e.st.QuantExact += int64(len(tw)/by - np)
			e.mu.Unlock()
		}
	case !t.DT.IsFloat():
		if t.Ref != nil {
			b, err := base(e.ctx, k*t.Win, len(tw))
			if err != nil {
				return wd, nil, err
			}
			if bytes.Equal(b.Data, tw) {
				wd.Mode = MCopy
				break
			}
		}
		wd.Mode, wd.Coder, payload = MRaw, CStore, tw
		if z := deflateBytes(tw); len(z) < len(tw) {
			wd.Coder, payload = CFlate, z
		}
	default:
		cd, data := t.DT, tw
		if t.DT == DF32 && !e.o.NoNarrow {
			if d, ok := Narrow(tw, DBF16); ok {
				cd, data, wd.Narrow = DBF16, d, DBF16
			} else if d, ok := Narrow(tw, DF16); ok {
				cd, data, wd.Narrow = DF16, d, DF16
			}
		}
		w := cd.Width()
		n := len(data) / w
		direct := func() {
			wd.Mode = MDirect
			if p, ok := FPEEncode(cd, data); ok && len(p) < len(data) {
				wd.Coder, payload = CFPE, p
			} else {
				wd.Coder, payload = CStore, data
			}
		}
		if t.Ref == nil {
			direct()
			break
		}
		b, err := base(e.ctx, k*t.Win, n)
		if err != nil {
			return wd, nil, err
		}
		pred, err := predWindow(t.P, cd, b, k*t.Win, n, t.Cols)
		if err != nil {
			return wd, nil, err
		}
		if bytes.Equal(pred, data) {
			wd.Mode = MCopy
			wd.Narrow = 0
			if cd != t.DT { // copy windows carry no narrow marker: re-predict in the tensor dtype
				p2, err := predWindow(t.P, t.DT, b, k*t.Win, n, t.Cols)
				if err != nil {
					return wd, nil, err
				}
				if !bytes.Equal(p2, tw) {
					wd.Mode, wd.Narrow = MPred, cd
					payload, wd.Coder = NRCEncode(cd, MakeResidual(RKey, w, data, pred), pred), CNRC
				}
			}
			break
		}
		resid := MakeResidual(RKey, w, data, pred)
		wd.Mode, wd.Coder, payload = MPred, CNRC, NRCEncode(cd, resid, pred)
		if float64(len(payload)) > 0.9*fpeProxy(cd, data) { // weak prediction: compare with first-sight coding
			keepP, keepC := payload, wd.Coder
			direct()
			if len(keepP) <= len(payload) {
				wd.Mode, wd.Coder, payload = MPred, keepC, keepP
			}
		}
	}
	wd.PLen = int64(len(payload))
	if !dry && !e.o.NoVerify {
		out := make([]byte, len(tw))
		aux := func(ctx context.Context, idx int, name string, cols int) ([]float32, error) { return pl.qw, nil }
		if err := rebuild(e.ctx, t, &wd, k, payload, out, base, aux); err != nil || !bytes.Equal(out, tw) {
			return wd, nil, ErrVerify7
		}
	}
	return wd, payload, nil
}

// ErrVerify7 means a freshly coded window did not decode to its input (encoder defect guard).
var ErrVerify7 = errVerify{}

type errVerify struct{}

func (errVerify) Error() string { return "numcore: encoder self-check failed" }

// Encode writes the ZN7 object of target to w in one sequential pass over the target with
// memory bounded by O(Workers x window) plus one structured-fit tensor (EncOpts.MaxStruct).
func Encode(ctx context.Context, w io.Writer, target io.ReaderAt, lay *Layout, bases []BaseIn, cands func(ti int) []Cand, aux []AuxIn, o EncOpts) (*EncStats, error) {
	if err := lay.Validate(); err != nil {
		return nil, err
	}
	if len(bases) > MaxBases7 || len(aux) > MaxBases7 {
		return nil, ErrLayout7
	}
	if o.Window <= 0 {
		o.Window = 1 << 16
	}
	if o.Window > 1<<24 {
		o.Window = 1 << 24
	}
	if o.Workers < 1 {
		o.Workers = 1
	}
	if o.MaxStruct <= 0 {
		o.MaxStruct = 32 << 20
	}
	if o.SampleEl <= 0 {
		o.SampleEl = 8192
	}
	if cands == nil {
		cands = func(int) []Cand { return nil }
	}
	st := &EncStats{Depth: make([]int, len(lay.T)), Picks: make([]int, len(lay.T))}
	e := &encoder{ctx: ctx, tgt: target, lay: lay, bases: bases, cands: cands, aux: aux, o: o, st: st, auxUsed: map[int]int{}}
	for _, a := range aux {
		im, err := qcore.ParseIMatrix(a.Data)
		if err != nil {
			return nil, err
		}
		e.im = append(e.im, im)
	}
	obj := &Object{FileLen: lay.Size, T: make([]TDir, len(lay.T))}
	for _, b := range bases {
		obj.Bases = append(obj.Bases, BaseInfo{b.ID, b.Len})
	}
	cw := &countW{w: w}
	if _, err := cw.Write([]byte(zMagic)); err != nil {
		return nil, err
	}
	// gap section (header and padding bytes), hashed in file position below
	gl := lay.GapLen()
	if gl > MaxGap7 {
		return nil, ErrLayout7
	}
	gaps, err := RawSource{target, lay}.Gaps(ctx)
	if err != nil {
		return nil, err
	}
	gp := deflateBytes(gaps)
	for si, b := range bases {
		if b.GapDepth+1 > MaxHops7 || b.Src == nil {
			continue
		}
		bg, err := b.Src.Gaps(ctx)
		if err != nil {
			return nil, err
		}
		if d, ok := gapDelta(gaps, bg); ok && len(d) < len(gp) {
			gp, obj.GapMode, obj.GapSlot, st.GapDepth = d, 1, uint8(si), b.GapDepth+1
		}
	}
	obj.GapLen, obj.GapPLen, obj.GapSum = gl, int64(len(gp)), sum16(gaps)
	if _, err := cw.Write(gp); err != nil {
		return nil, err
	}
	st.GapBytes = int64(len(gp))

	// window pipeline: the dispatcher reads the target sequentially (and hashes it in file order),
	// workers code windows, the writer emits payloads in order.
	type job struct {
		td  *TDir
		pl  *tplan
		k   int64
		buf []byte
		wd  WDir
		pay []byte
		err error
		wg  sync.WaitGroup
	}
	jobs := make(chan *job, o.Workers)
	done := make(chan *job, 2*o.Workers)
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	e.ctx = ctx2
	var wg sync.WaitGroup
	for i := 0; i < o.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx2.Err() != nil {
					j.err = ctx2.Err()
				} else {
					j.wd, j.pay, j.err = e.codeWindow(j.td, j.pl, j.k, j.buf, false)
				}
				j.buf = nil
				j.wg.Done()
			}
		}()
	}
	h := sha256.New()
	var dispErr error
	go func() {
		defer close(done)
		defer close(jobs)
		gpos, prev := int64(0), int64(0)
		for ti := range lay.T {
			t := &lay.T[ti]
			h.Write(gaps[gpos : gpos+t.Off-prev])
			gpos += t.Off - prev
			prev = t.Off + t.Len
			var pl *tplan
			var err error
			switch {
			case t.DT.IsFloat():
				pl, err = e.planFloat(ti, t)
			case t.DT.IsQuant():
				if pl, err = e.planOpaque(ti, t); err == nil && pl.ref == nil {
					var qp *tplan
					if qp, err = e.planQuant(ti, t); err == nil && qp != nil {
						pl = qp
					}
				}
			default:
				pl, err = e.planOpaque(ti, t)
			}
			if err != nil {
				dispErr = err
				cancel()
				return
			}
			td := &obj.T[ti]
			*td = TDir{DT: t.DT, Off: t.Off, Len: t.Len, Cols: t.Cols, Win: pl.win, Ref: pl.ref, P: pl.p, Q: pl.q}
			if td.Q != nil && td.Cols <= 0 {
				el, _ := qBlock(mustQ(t.DT))
				td.Cols = t.Len / int64(td.unit()) * int64(el)
			}
			st.Depth[ti] = pl.depth
			nw, ok := numWindows(t.Len, td.Win, td.unit())
			if !ok {
				dispErr = ErrLayout7
				cancel()
				return
			}
			td.W = make([]WDir, nw)
			for k := int64(0); k < nw; k++ {
				off, n := td.winBytes(k)
				buf := make([]byte, n)
				if err := readFull(target, buf, t.Off+off); err != nil {
					dispErr = err
					cancel()
					return
				}
				h.Write(buf)
				j := &job{td: td, pl: pl, k: k, buf: buf}
				j.wg.Add(1)
				select {
				case jobs <- j:
				case <-ctx2.Done():
					return
				}
				done <- j
			}
		}
		h.Write(gaps[gpos:])
	}()
	var first error
	for j := range done {
		j.wg.Wait()
		if first != nil {
			continue
		}
		if first = j.err; first == nil {
			j.td.W[j.k] = j.wd
			_, first = cw.Write(j.pay)
			st.Windows++
			st.ModeBytes[j.wd.Mode] += j.wd.PLen
			st.ModeCount[j.wd.Mode]++
			if j.wd.Narrow != 0 {
				st.Narrowed++
			}
		}
		if first != nil {
			cancel()
		}
	}
	wg.Wait()
	if first == nil {
		first = dispErr
	}
	if first == nil {
		first = ctx.Err()
	}
	if first != nil {
		return nil, first
	}
	// a tensor whose windows all fell back to raw/direct keeps no reference
	for ti := range obj.T {
		td := &obj.T[ti]
		if td.Ref == nil || td.Q != nil {
			continue
		}
		used := false
		for k := range td.W {
			if m := td.W[k].Mode; m == MPred || m == MCopy {
				used = true
				break
			}
		}
		if !used {
			td.Ref, td.P = nil, nil
			st.Depth[ti] = 0
		}
	}
	h.Sum(obj.FileSHA[:0])
	obj.Aux = e.auxIDs
	dir := obj.marshalDir()
	dirOff := cw.n
	var tr [zTrailLen]byte
	binary.LittleEndian.PutUint64(tr[0:], uint64(dirOff))
	binary.LittleEndian.PutUint64(tr[8:], uint64(len(dir)))
	copy(tr[16:], zTrailer)
	if _, err := cw.Write(append(dir, tr[:]...)); err != nil {
		return nil, err
	}
	st.DirBytes = int64(len(dir)) + zTrailLen + 4
	st.Bytes = cw.n
	return st, nil
}

type countW struct {
	w io.Writer
	n int64
}

func (c *countW) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}
