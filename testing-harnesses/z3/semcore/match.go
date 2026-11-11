package semcore

import (
	"bytes"
	"fmt"
	"hash/maphash"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Options tune the matcher; every acceptance is still proven by SHA-256.
type Options struct {
	// Semantic enables layout (permute/gather) relations between same-typed tensors.
	Semantic bool
	// Convert enables cross-dtype relations (float convert, Q8_0/Q4_0 quantize) with residuals.
	Convert bool
	// MaxCandidates bounds value-relation trials per target tensor.
	MaxCandidates int
	// AcceptFrac: accept a derivation only if its stored bytes are below this fraction of
	// the packed raw payload.
	AcceptFrac float64
	// MinBytes: tensors below this size are stored raw without search.
	MinBytes int
	// MaxTensor: tensors larger than this are stored as fixed-size Bytes blocks (0: default).
	MaxTensor int
	// CacheBytes bounds the decoded parent-payload cache (0: 256 MiB).
	CacheBytes int64
	// WorkBytes bounds transient candidate buffers (0: 512 MiB); Parallel caps concurrent
	// candidate evaluations (0: NumCPU, 1: sequential).
	WorkBytes int64
	Parallel  int
}

func DefaultOptions() Options {
	return Options{Semantic: true, Convert: true, MaxCandidates: 6, AcceptFrac: 0.5, MinBytes: 4096}
}

// ProbeStats are counters for the staged candidate probes.
type ProbeStats struct {
	GatherSkipped, GatherTried, DiffAborted int
}

type rtKey struct {
	cid   ID
	dtype int
	rb    int
	base  int
}

// rowTableFor returns the (cached, bounded) row hash table of a candidate layout.
func (p *Planner) rowTableFor(cid ID, dtype, rb, base int, layout []byte) *rowTable {
	k := rtKey{cid, dtype, rb, base}
	p.mu.Lock()
	if t, ok := p.rtabs[k]; ok {
		p.mu.Unlock()
		return t
	}
	p.mu.Unlock()
	t := newRowTable(layout, rb)
	p.mu.Lock()
	if p.rtabs == nil || len(p.rtabs) >= 64 {
		p.rtabs = map[rtKey]*rowTable{}
	}
	p.rtabs[k] = t
	p.mu.Unlock()
	return t
}

// Placement reports how one tensor was stored.
type Placement struct {
	CID                ID
	How                string
	RecBytes, NewBytes int64 // serialized record bytes and newly stored blob bytes
	PatchBytes         int64
	Parent             ID
	Ops                string
	Search             time.Duration
}

// Planner places tensors into a store using exact identity first, then proven relations.
type Planner struct {
	S      *Store
	Opt    Options
	Hist   map[string]int
	sigs   map[ID]Sig
	pcache map[ID][]byte
	porder []ID // insertion order of pcache (oldest first): bounded FIFO eviction
	pbytes int64
	// PeakCache is the largest decoded-payload cache residency observed (bytes).
	PeakCache int64
	// exclude, when set, hides components from parent search (used while rebasing).
	exclude func(ID) bool
	rebase  bool
	mu      sync.Mutex // guards the caches below and Probe (candidate evaluation is concurrent)
	// Probe counts the work the cheap probes avoided or spent (measurement only).
	Probe ProbeStats
	rtabs map[rtKey]*rowTable
	// Chunk is the adapter's content-defined chunker for literal regions (nil: no chunking).
	Chunk func([]byte) [][]byte
}

func NewPlanner(s *Store, o Options) *Planner {
	return &Planner{S: s, Opt: o, Hist: map[string]int{}, sigs: map[ID]Sig{}, pcache: map[ID][]byte{}}
}

func tensorDesc(t *TInfo) Desc {
	return Desc{Kind: KindTensor, Version: 1, DType: uint16(t.DType), Shape: append([]uint64(nil), t.Shape...), Ordered: true}
}

func rowBytes(d *Desc) int {
	if len(d.Shape) < 2 {
		return 0
	}
	var n uint64 = 1
	for _, x := range d.Shape[1:] {
		n *= x
	}
	rows := d.Shape[0]
	total := d.PayloadLen()
	if total < 0 {
		if g, ok := ggmlGeom(int(d.DType)); ok {
			e, _ := d.Elems()
			total = int64(e / uint64(g[0]) * uint64(g[1]))
		} else {
			return 0
		}
	}
	if rows == 0 || uint64(total)%rows != 0 {
		return 0
	}
	_ = n
	return int(uint64(total) / rows)
}

func ggmlGeom(d int) ([2]int, bool) {
	if g, ok := blockGeom(d); ok {
		return g, true
	}
	return [2]int{}, false
}

// Place stores a tensor payload, preferring (1) identical typed component, (2) shared bytes,
// (3) an exactly proven layout relation, (4) a proven value relation with exact residual,
// (5) a new raw component.
func (p *Planner) Place(t *TInfo, payload []byte) (Placement, error) {
	start := time.Now()
	d := tensorDesc(t)
	sha := PayloadSHA(payload)
	cid := CID(&d, int64(len(payload)), sha)
	pl := Placement{CID: cid}
	if p.S.Has(cid) {
		pl.How = "identity"
		return pl, nil
	}
	finish := func(how string, r *Record, newBlob int64) (Placement, error) {
		pl.How, pl.CID = how, r.CID
		pl.RecBytes = int64(len(r.Marshal()))
		pl.NewBytes = newBlob
		pl.Search = time.Since(start)
		p.Hist[how]++
		return pl, nil
	}
	if same := p.S.ByPayload(sha); len(same) > 0 {
		r, err := p.S.PutRaw(d, payload)
		if err != nil {
			return pl, err
		}
		return finish("bytes-shared", r, 0)
	}
	if len(payload) >= p.Opt.MinBytes && (p.Opt.Semantic || p.Opt.Convert) {
		if r, ops, rec := p.tryLayout(&d, payload); r != nil {
			pl.Parent = r.Parents[0]
			pl.Ops = ops
			return finish("layout", r, 0)
		} else {
			_ = rec
		}
		if p.Opt.Convert || p.Opt.Semantic {
			if r, how := p.tryRelation(&d, t, payload); r != nil {
				pl.Parent = r.Parents[0]
				pl.Ops = opsString(r.Ops)
				for _, pt := range r.Patches {
					pl.PatchBytes += int64(len(pt.Data)) + 4
				}
				return finish(how, r, 0)
			}
		}
	}
	r, err := p.S.PutRaw(d, payload)
	if err != nil {
		return pl, err
	}
	if sg, ok := SigOf(int(d.DType), payload); ok {
		p.sigs[r.CID] = sg
	}
	return finish("new", r, int64(len(payload)))
}

// parentPayload returns a stored payload through a bounded cache.
func (p *Planner) parentPayload(id ID) ([]byte, error) {
	p.mu.Lock()
	if b, ok := p.pcache[id]; ok {
		p.mu.Unlock()
		return b, nil
	}
	p.mu.Unlock()
	b, err := p.S.Payload(id)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if ob, ok := p.pcache[id]; ok {
		return ob, nil
	}
	lim := p.Opt.CacheBytes
	if lim <= 0 {
		lim = 256 << 20
	}
	for p.pbytes+int64(len(b)) > lim && len(p.porder) > 0 {
		old := p.porder[0]
		p.porder = p.porder[1:]
		if ob, ok := p.pcache[old]; ok {
			p.pbytes -= int64(len(ob))
			delete(p.pcache, old)
		}
	}
	if int64(len(b)) <= lim {
		p.pcache[id], p.pbytes = b, p.pbytes+int64(len(b))
		p.porder = append(p.porder, id)
	}
	if p.pbytes > p.PeakCache {
		p.PeakCache = p.pbytes
	}
	return b, nil
}

func (p *Planner) sigOf(r *Record) (Sig, bool) {
	p.mu.Lock()
	s, ok := p.sigs[r.CID]
	p.mu.Unlock()
	if ok {
		return s, true
	}
	b, err := p.parentPayload(r.CID)
	if err != nil {
		return Sig{}, false
	}
	s, ok = SigOf(int(r.Desc.DType), b)
	if ok {
		p.mu.Lock()
		p.sigs[r.CID] = s
		p.mu.Unlock()
	}
	return s, ok
}

func opsString(ops []Op) string {
	var s []string
	for i := range ops {
		s = append(s, ops[i].String())
	}
	return strings.Join(s, ">")
}

// layoutCandidates are transforms applied to the target to look for an identical stored
// component. Perm(Dims, Axes) of the target = parent, so target = Invert(Perm)(parent).
func layoutCandidates(d *Desc, w int) []Op {
	r := len(d.Shape)
	if r < 2 || r > 4 {
		return nil
	}
	var out []Op
	axes := make([]uint8, r)
	var gen func(k int, used uint)
	gen = func(k int, used uint) {
		if k == r {
			id := true
			for i, a := range axes {
				if int(a) != i {
					id = false
				}
			}
			if !id {
				out = append(out, Op{Kind: OpPerm, Elem: uint32(w), Dims: append([]uint64(nil), d.Shape...), Axes: append([]uint8(nil), axes...)})
			}
			return
		}
		for a := 0; a < r; a++ {
			if used&(1<<a) == 0 {
				axes[k] = uint8(a)
				gen(k+1, used|1<<a)
			}
		}
	}
	gen(0, 0)
	return out
}

func (p *Planner) tryLayout(d *Desc, payload []byte) (*Record, string, bool) {
	w := ElemBytes(int(d.DType))
	if w == 0 {
		return nil, "", false
	}
	for _, c := range layoutCandidates(d, w) {
		c := c
		out, err := c.Apply([][]byte{payload})
		if err != nil {
			continue
		}
		for _, id := range p.S.ByPayload(PayloadSHA(out)) {
			pr := p.S.Rec(id)
			if pr.Desc.Kind != KindTensor || pr.Desc.DType != d.DType || (p.exclude != nil && p.exclude(id)) {
				continue
			}
			inv := InvertPerm(c)
			r, err := p.put(*d, []ID{id}, []Op{inv}, nil, payload)
			if err == nil {
				return r, opsString([]Op{inv}), true
			}
		}
	}
	return nil, "", false
}

var rowSeed = maphash.MakeSeed()

// rowHash buckets rows for candidate matching only; equality is always checked bytewise.
func rowHash(b []byte) uint64 { return maphash.Bytes(rowSeed, b) }

// convOps builds the pipeline converting parent dtype `from` payload into dtype `to`.
func convOps(from, to int) ([]Op, bool) {
	if from == to {
		return nil, true
	}
	switch {
	case IsFloat(from) && IsFloat(to):
		return []Op{{Kind: OpConvert, From: uint16(from), To: uint16(to)}}, true
	case IsFloat(from) && to == DQBase+8:
		ops := []Op{}
		if from != DF32 {
			ops = append(ops, Op{Kind: OpConvert, From: uint16(from), To: DF32})
		}
		return append(ops, Op{Kind: OpQ8_0}), true
	case IsFloat(from) && to == DQBase+2:
		ops := []Op{}
		if from != DF32 {
			ops = append(ops, Op{Kind: OpConvert, From: uint16(from), To: DF32})
		}
		return append(ops, Op{Kind: OpQ4_0}), true
	}
	return nil, false
}

func applyOps(b []byte, ops []Op) ([]byte, error) {
	cur := b
	for i := range ops {
		o, err := ops[i].Apply([][]byte{cur})
		if err != nil {
			return nil, err
		}
		cur = o
	}
	return cur, nil
}

// tryRelation searches same-element-count stored tensors for a derivation of the target
// (convert/quantize, optional row gather or transpose) plus an exact residual.
func (p *Planner) tryRelation(d *Desc, t *TInfo, payload []byte) (*Record, string) {
	n, err := d.Elems()
	if err != nil {
		return nil, ""
	}
	cands := p.S.ByElems(n)
	if len(cands) == 0 {
		return nil, ""
	}
	// rank: same-name first, then closer dtype width, then stable by CID
	type cand struct {
		r     *Record
		score float64
	}
	tsig, haveSig := SigOf(int(d.DType), payload)
	var cs []cand
	for _, id := range cands {
		pr := p.S.Rec(id)
		if pr.Derived() || (p.exclude != nil && p.exclude(id)) {
			continue // bases are raw components only: keeps depth 1 and avoids chains through residuals
		}
		pd := int(pr.Desc.DType)
		_, ok := convOps(pd, int(d.DType))
		same := pd == int(d.DType)
		if !ok && !same {
			continue
		}
		if !p.Opt.Convert && !same {
			continue
		}
		sc := 0.0
		if haveSig {
			ps, ok := p.sigOf(pr)
			if !ok {
				continue
			}
			sc = SigDist(tsig, ps)
			if sc > 0.08 { // quantization noise is ~1e-2; unrelated tensors differ far more
				continue
			}
		} else if !same {
			continue
		}
		cs = append(cs, cand{pr, sc})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].score < cs[j].score })
	if len(cs) > p.Opt.MaxCandidates {
		cs = cs[:p.Opt.MaxCandidates]
	}
	rawCost := float64(len(payload)) + 96
	rb := rowBytes(d)
	limit := rawCost * p.Opt.AcceptFrac
	type result struct {
		r    *Record
		cost float64
		how  string
	}
	res := make([]result, len(cs))
	workers := p.workers(len(payload))
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i := range cs {
		i := i
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			r, cost, how := p.evalCandidate(d, cs[i].r, payload, rb, limit)
			res[i] = result{r, cost, how}
		}()
	}
	wg.Wait()
	// deterministic reduction: the sequential planner kept the first candidate that was
	// strictly cheaper than everything before it, i.e. the first minimum in candidate order.
	var best *Record
	bestCost := limit
	bestHow := ""
	for _, x := range res {
		if x.r != nil && x.cost < bestCost {
			best, bestCost, bestHow = x.r, x.cost, x.how
		}
	}
	if best == nil {
		return nil, ""
	}
	// persist the winner (probe records were not written)
	r, err := p.put(*d, best.Parents, best.Ops, best.Patches, payload)
	if err != nil {
		return nil, ""
	}
	if len(best.Patches) > 0 {
		bestHow += "+residual"
	}
	return r, bestHow
}

// structuredRows recognizes the llama.cpp head-interleave row permutation (and inverse)
// and returns an equivalent compact Perm over row-sized cells.
func structuredRows(idx []uint32, rb int) *Op {
	rows := len(idx)
	for h := 1; h <= rows; h++ {
		if rows%h != 0 || (rows/h)%2 != 0 || h > 1024 {
			continue
		}
		hd := rows / h
		for dir := 0; dir < 2; dir++ {
			ok := true
			for hh := 0; hh < h && ok; hh++ {
				for k := 0; k < hd/2 && ok; k++ {
					for a := 0; a < 2; a++ {
						// forward: out[h][k][a] = in[h][a][k]; inverse: out[h][a][k] = in[h][k][a]
						var o, in int
						if dir == 0 {
							o, in = hh*hd+k*2+a, hh*hd+a*(hd/2)+k
						} else {
							o, in = hh*hd+a*(hd/2)+k, hh*hd+k*2+a
						}
						if int(idx[o]) != in {
							ok = false
							break
						}
					}
				}
			}
			if ok {
				if dir == 0 {
					return &Op{Kind: OpPerm, Elem: uint32(rb), Dims: []uint64{uint64(h), 2, uint64(hd / 2)}, Axes: []uint8{0, 2, 1}}
				}
				return &Op{Kind: OpPerm, Elem: uint32(rb), Dims: []uint64{uint64(h), uint64(hd / 2), 2}, Axes: []uint8{0, 2, 1}}
			}
		}
	}
	return nil
}

var _ = fmt.Sprint

func (p *Planner) put(d Desc, parents []ID, ops []Op, patches []Patch, payload []byte) (*Record, error) {
	if !p.rebase {
		return p.S.PutDerived(d, parents, ops, patches, payload)
	}
	r := &Record{Desc: d, PayloadLen: int64(len(payload)), Payload: PayloadSHA(payload), Parents: parents, Ops: ops, Patches: patches}
	r.CID = CID(&r.Desc, r.PayloadLen, r.Payload)
	if err := p.S.Rederive(r); err != nil {
		return nil, err
	}
	return r, nil
}

// RebaseStats summarizes one optimization pass.
type RebaseStats struct {
	Considered, Rebased int
	BlobBytesFreed      int64
}

// Optimize is the offline group-aware admission pass: every raw tensor component, largest
// first, is re-expressed as a derivation of any other component that does not depend on it,
// whenever that is cheaper. Repeats until no component changes (bounded rounds).
func (p *Planner) Optimize(rounds int) (RebaseStats, error) {
	var st RebaseStats
	p.rebase = true
	defer func() { p.rebase, p.exclude = false, nil }()
	for ; rounds > 0; rounds-- {
		changed := false
		recs := p.S.All()
		sort.SliceStable(recs, func(i, j int) bool { return recs[i].PayloadLen > recs[j].PayloadLen })
		for _, r := range recs {
			if r.Derived() || r.Desc.Kind != KindTensor || int(r.PayloadLen) < p.Opt.MinBytes {
				continue
			}
			st.Considered++
			self := r.CID
			p.exclude = func(id ID) bool {
				if id == self {
					return true
				}
				cl, err := p.S.Closure(id)
				if err != nil {
					return true
				}
				for _, x := range cl {
					if x == self {
						return true
					}
				}
				return false
			}
			payload, err := p.parentPayload(r.CID)
			if err != nil {
				return st, err
			}
			t := &TInfo{DType: int(r.Desc.DType), Shape: r.Desc.Shape}
			d := r.Desc
			var nr *Record
			if got, _, ok := p.tryLayout(&d, payload); got != nil && ok {
				nr = got
			} else if got, _ := p.tryRelation(&d, t, payload); got != nil {
				nr = got
			}
			if nr != nil {
				st.Rebased++
				st.BlobBytesFreed += r.PayloadLen
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return st, nil
}

// rowTable indexes the rows of one parent layout by hash (candidate bucketing only).
type rowTable struct {
	rb   int
	rows int
	by   map[uint64][]int
}

func newRowTable(parent []byte, rb int) *rowTable {
	rows := len(parent) / rb
	t := &rowTable{rb: rb, rows: rows, by: make(map[uint64][]int, rows)}
	for i := 0; i < rows; i++ {
		h := rowHash(parent[i*rb : (i+1)*rb])
		t.by[h] = append(t.by[h], i)
	}
	return t
}

// sampleHits counts how many of up to 64 evenly spaced target rows occur in the table.
// It only gates an expensive exact search; a gather needs at least half the rows to match,
// so a sample with fewer than 8 hits out of 64 rejects a true half-match with probability
// below 1e-9 (and a table with fewer rows is searched exhaustively).
func (t *rowTable) sampleHits(target []byte) (hits, n int) {
	rows := len(target) / t.rb
	step := rows / 64
	if step < 1 {
		step = 1
	}
	for i := 0; i < rows; i += step {
		n++
		if _, ok := t.by[rowHash(target[i*t.rb:(i+1)*t.rb])]; ok {
			hits++
		}
	}
	return
}

// rowGatherIndex finds, for every row of target, an unused identical row of parent; it
// returns nil when the rows are already in order or fewer than half match.
func rowGatherIndex(parent, target []byte, rb int) []uint32 {
	return rowGatherIndexT(parent, target, rb, newRowTable(parent, rb))
}

func rowGatherIndexT(parent, target []byte, rb int, tab *rowTable) []uint32 {
	rows := len(parent) / rb
	prh := tab.by
	idx := make([]uint32, rows)
	used := make([]bool, rows)
	matched := make([]bool, rows)
	nm := 0
	for i := 0; i < rows; i++ {
		idx[i] = uint32(i)
		for _, j := range prh[rowHash(target[i*rb:(i+1)*rb])] {
			if !used[j] && bytes.Equal(parent[j*rb:(j+1)*rb], target[i*rb:(i+1)*rb]) {
				idx[i], used[j], matched[i] = uint32(j), true, true
				nm++
				break
			}
		}
	}
	identity := true
	for i, x := range idx {
		if int(x) != i {
			identity = false
			break
		}
	}
	if identity || nm*2 < rows {
		return nil
	}
	// keep a permutation: unmatched targets take the unused parent rows in order
	fi := 0
	for i := 0; i < rows; i++ {
		if matched[i] {
			continue
		}
		for fi < rows && used[fi] {
			fi++
		}
		if fi < rows {
			idx[i], used[fi] = uint32(fi), true
		}
	}
	return idx
}

// workers bounds concurrent candidate evaluations: each one holds a few tensor-sized
// buffers, so the count shrinks as the tensor grows (Options.WorkBytes budget).
func (p *Planner) workers(tensorBytes int) int {
	lim := p.Opt.WorkBytes
	if lim <= 0 {
		lim = 512 << 20
	}
	w := int(lim / (int64(tensorBytes)*5 + 1))
	if w < 1 {
		w = 1
	}
	if m := p.Opt.Parallel; m > 0 && w > m {
		w = m
	} else if m == 0 && w > runtime.NumCPU() {
		w = runtime.NumCPU()
	}
	return w
}

// evalCandidate searches the layouts and conversions of one stored base for the cheapest
// exact derivation of payload below limit (bytes). It returns (nil, 0, "") when none is.
func (p *Planner) evalCandidate(d *Desc, cr *Record, payload []byte, rb int, limit float64) (*Record, float64, string) {
	pdt := int(cr.Desc.DType)
	pre, ok := convOps(pdt, int(d.DType))
	if !ok {
		return nil, 0, ""
	}
	src, err := p.parentPayload(cr.CID)
	if err != nil {
		return nil, 0, ""
	}
	pc, err := applyOps(src, pre)
	if err != nil || len(pc) != len(payload) {
		return nil, 0, ""
	}
	type attempt struct {
		ops []Op
		out []byte
		how string
	}
	var best *Record
	bestCost := limit
	bestHow := ""
	try := func(a attempt) {
		fixed := 64 + float64(len(a.ops))*16
		for i := range a.ops {
			fixed += float64(len(a.ops[i].Idx)) * 2
		}
		if fixed >= bestCost {
			return
		}
		// exact early exit: a patch list this large would be rejected below anyway
		patches, ok := DiffPatchesMax(a.out, payload, 8, int(bestCost-fixed)+1)
		if !ok {
			p.count(&p.Probe.DiffAborted)
			return
		}
		var pb int
		for _, pt := range patches {
			pb += len(pt.Data) + 6
		}
		cost := float64(pb) + fixed
		if cost >= bestCost {
			return
		}
		r, err := p.S.PutDerivedProbe(*d, []ID{cr.CID}, a.ops, patches, payload)
		if err != nil {
			return
		}
		if rc := float64(len(r.Marshal())); rc < bestCost {
			bestCost, best, bestHow = rc, r, a.how
		}
	}
	try(attempt{pre, pc, "convert"})
	// An exact conversion (no residual) costs less than any attempt that adds an operation.
	if best != nil && len(best.Patches) == 0 {
		return best, bestCost, bestHow
	}
	if p.Opt.Semantic && rb > 0 && len(pc)%rb == 0 && len(d.Shape) >= 2 {
		type layout struct {
			b   []byte
			ops []Op
			tag string
		}
		bases := []layout{{pc, pre, ""}}
		if len(d.Shape) == 2 {
			if w := ElemBytes(int(d.DType)); w > 0 {
				tr := Op{Kind: OpPerm, Elem: uint32(w), Dims: []uint64{d.Shape[1], d.Shape[0]}, Axes: []uint8{1, 0}}
				if g, err := tr.Apply([][]byte{pc}); err == nil {
					ops := append(append([]Op(nil), pre...), tr)
					try(attempt{ops, g, "transpose"})
					bases = append(bases, layout{g, ops, "transpose+"})
				}
			}
		}
		for bi, bs := range bases {
			tab := p.rowTableFor(cr.CID, int(d.DType), rb, bi, bs.b)
			if h, n := tab.sampleHits(payload); n >= 16 && h*8 < n { // <12.5% sampled rows present
				p.count(&p.Probe.GatherSkipped)
				continue
			}
			p.count(&p.Probe.GatherTried)
			if idx := rowGatherIndexT(bs.b, payload, rb, tab); idx != nil {
				gop := structuredRows(idx, rb)
				if gop == nil {
					gop = &Op{Kind: OpGather, Elem: uint32(rb), Idx: idx}
				}
				if g, err := gop.Apply([][]byte{bs.b}); err == nil {
					try(attempt{append(append([]Op(nil), bs.ops...), *gop), g, bs.tag + "gather"})
				}
			}
		}
	}
	if best == nil {
		return nil, 0, ""
	}
	return best, bestCost, bestHow
}

func (p *Planner) count(c *int) { p.mu.Lock(); *c++; p.mu.Unlock() }
