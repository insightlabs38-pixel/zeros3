package semcas

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
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
}

func DefaultOptions() Options {
	return Options{Semantic: true, Convert: true, MaxCandidates: 6, AcceptFrac: 0.5, MinBytes: 4096}
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
	pbytes int64
	// exclude, when set, hides components from parent search (used while rebasing).
	exclude func(ID) bool
	rebase  bool
	// only/dry support edge-cost evaluation: restrict the parent and write nothing.
	only    *Record
	dry     bool
	acceptF float64
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
	if b, ok := p.pcache[id]; ok {
		return b, nil
	}
	b, err := p.S.Payload(id)
	if err != nil {
		return nil, err
	}
	if p.pbytes+int64(len(b)) > 768<<20 {
		p.pcache, p.pbytes = map[ID][]byte{}, 0
	}
	p.pcache[id], p.pbytes = b, p.pbytes+int64(len(b))
	return b, nil
}

func (p *Planner) sigOf(r *Record) (Sig, bool) {
	if s, ok := p.sigs[r.CID]; ok {
		return s, true
	}
	b, err := p.parentPayload(r.CID)
	if err != nil {
		return Sig{}, false
	}
	s, ok := SigOf(int(r.Desc.DType), b)
	if ok {
		p.sigs[r.CID] = s
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

func rowHash(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64()
}

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
		if (pr.Derived() && p.only == nil) || (p.exclude != nil && p.exclude(id)) {
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
	if p.only != nil {
		cs = []cand{{p.only, 0}}
	}
	rawCost := float64(len(payload)) + 96
	rb := rowBytes(d)
	var best *Record
	frac := p.Opt.AcceptFrac
	if p.acceptF > 0 {
		frac = p.acceptF
	}
	bestCost := rawCost * frac
	bestHow := ""
	var trows map[uint64][]int
	if rb > 0 && len(payload)%rb == 0 {
		trows = make(map[uint64][]int, len(payload)/rb)
		for i := 0; i+rb <= len(payload); i += rb {
			h := rowHash(payload[i : i+rb])
			trows[h] = append(trows[h], i/rb)
		}
	}
	for _, c := range cs {
		pdt := int(c.r.Desc.DType)
		pre, ok := convOps(pdt, int(d.DType))
		if !ok {
			continue
		}
		src, err := p.parentPayload(c.r.CID)
		if err != nil {
			continue
		}
		pc, err := applyOps(src, pre)
		if err != nil || len(pc) != len(payload) {
			continue
		}
		type attempt struct {
			ops []Op
			out []byte
			how string
		}
		var attempts []attempt
		attempts = append(attempts, attempt{pre, pc, "convert"})
		if p.Opt.Semantic && rb > 0 && len(pc)%rb == 0 && len(d.Shape) >= 2 {
			bases := []struct {
				b   []byte
				ops []Op
				tag string
			}{{pc, pre, ""}}
			if len(d.Shape) == 2 {
				if w := ElemBytes(int(d.DType)); w > 0 {
					tr := Op{Kind: OpPerm, Elem: uint32(w), Dims: []uint64{d.Shape[1], d.Shape[0]}, Axes: []uint8{1, 0}}
					if g, err := tr.Apply([][]byte{pc}); err == nil {
						ops := append(append([]Op(nil), pre...), tr)
						attempts = append(attempts, attempt{ops, g, "transpose"})
						bases = append(bases, struct {
							b   []byte
							ops []Op
							tag string
						}{g, ops, "transpose+"})
					}
				}
			}
			for _, bs := range bases {
				if idx := rowGatherIndex(bs.b, payload, rb); idx != nil {
					gop := structuredRows(idx, rb)
					if gop == nil {
						gop = &Op{Kind: OpGather, Elem: uint32(rb), Idx: idx}
					}
					if g, err := gop.Apply([][]byte{bs.b}); err == nil {
						attempts = append(attempts, attempt{append(append([]Op(nil), bs.ops...), *gop), g, bs.tag + "gather"})
					}
				}
			}
		}
		for _, a := range attempts {
			patches := DiffPatches(a.out, payload, 8)
			var pb int
			for _, pt := range patches {
				pb += len(pt.Data) + 6
			}
			cost := float64(pb) + 64 + float64(len(a.ops))*16
			for i := range a.ops {
				cost += float64(len(a.ops[i].Idx)) * 2
			}
			if cost >= bestCost {
				continue
			}
			r, err := p.S.PutDerivedProbe(*d, []ID{c.r.CID}, a.ops, patches, payload)
			if err != nil {
				continue
			}
			rc := float64(len(r.Marshal()))
			if rc < bestCost {
				bestCost = rc
				best = r
				bestHow = a.how
			}
		}
	}
	if best == nil {
		return nil, ""
	}
	if p.dry {
		if len(best.Patches) > 0 {
			bestHow += "+residual"
		}
		return best, bestHow
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

// RegroupStats summarizes the offline group-aware re-rooting pass.
type RegroupStats struct {
	Clusters, Members, Rerooted, Derived int
	RawBefore, RawAfter                  int64
}

// Regroup is the offline oracle for collections: tensors whose payloads are related
// (same element count, close signature) form a cluster; for each cluster every member is
// tried as the single raw root with all other members derived from it (star, depth 1),
// and the cheapest root is installed. This undoes first-seen ordering effects.
func (p *Planner) Regroup() (RegroupStats, error) {
	var st RegroupStats
	byN := map[uint64][]*Record{}
	for _, r := range p.S.All() {
		if r.Desc.Kind != KindTensor || int(r.PayloadLen) < p.Opt.MinBytes {
			continue
		}
		if n, err := r.Desc.Elems(); err == nil {
			byN[n] = append(byN[n], r)
		}
	}
	seen := map[ID]bool{}
	for _, group := range byN {
		for _, seed := range group {
			if seen[seed.CID] {
				continue
			}
			ss, ok := p.sigOf(seed)
			if !ok {
				continue
			}
			cl := []*Record{seed}
			seen[seed.CID] = true
			for _, o := range group {
				if seen[o.CID] {
					continue
				}
				if os, ok := p.sigOf(o); ok && SigDist(ss, os) < 0.025 {
					cl = append(cl, o)
					seen[o.CID] = true
				}
			}
			if len(cl) < 2 || len(cl) > 16 {
				continue
			}
			st.Clusters++
			st.Members += len(cl)
			if err := p.regroupCluster(cl, &st); err != nil {
				return st, err
			}
		}
	}
	return st, nil
}

func (p *Planner) regroupCluster(cl []*Record, st *RegroupStats) error {
	n := len(cl)
	raw := make([]float64, n)
	for i, r := range cl {
		raw[i] = float64(r.PayloadLen) + 96
	}
	edge := make([][]float64, n)
	save := p.dry
	p.dry, p.exclude, p.acceptF = true, nil, 0.9
	defer func() { p.dry, p.only, p.acceptF = save, nil, 0 }()
	for i := range edge {
		edge[i] = make([]float64, n)
		for j := range edge[i] {
			edge[i][j] = raw[j]
			if i == j {
				continue
			}
			pay, err := p.parentPayload(cl[j].CID)
			if err != nil {
				return err
			}
			d := cl[j].Desc
			// parent is the unmodified raw-equivalent payload of i
			p.only = cl[i]
			if rec, _ := p.tryLayoutOnly(&d, pay, cl[i]); rec != nil {
				edge[i][j] = float64(len(rec.Marshal()))
				continue
			}
			if rec, _ := p.tryRelation(&d, &TInfo{DType: int(d.DType), Shape: d.Shape}, pay); rec != nil {
				if c := float64(len(rec.Marshal())); c < raw[j] {
					edge[i][j] = c
				}
			}
		}
	}
	best, bestCost := -1, 0.0
	cur := 0.0
	for j, r := range cl {
		if r.Derived() {
			cur += float64(len(r.Marshal()))
		} else {
			cur += raw[j]
		}
	}
	for i := range cl {
		c := 0.0
		for j := range cl {
			if i == j {
				c += raw[j]
			} else {
				c += edge[i][j]
			}
		}
		if best < 0 || c < bestCost {
			best, bestCost = i, c
		}
	}
	if bestCost >= cur*0.995 {
		return nil
	}
	// apply: root raw first, then every other member from the root (or raw)
	p.dry, p.only = false, nil
	root := cl[best]
	if root.Derived() {
		if err := p.S.Materialize(root.CID); err != nil {
			return err
		}
		st.Rerooted++
	}
	p.rebase = true
	defer func() { p.rebase = false }()
	for j, r := range cl {
		if j == best {
			continue
		}
		pay, err := p.parentPayload(r.CID)
		if err != nil {
			return err
		}
		d := r.Desc
		p.only = p.S.Rec(root.CID)
		p.acceptF = 0.9
		nr, _ := p.tryLayoutOnly(&d, pay, root)
		if nr == nil {
			nr, _ = p.tryRelation(&d, &TInfo{DType: int(d.DType), Shape: d.Shape}, pay)
		}
		p.only = nil
		if nr != nil {
			st.Derived++
		} else if r.Derived() {
			if err := p.S.Materialize(r.CID); err != nil {
				return err
			}
		}
	}
	return nil
}

// tryLayoutOnly is tryLayout restricted to one parent record.
func (p *Planner) tryLayoutOnly(d *Desc, payload []byte, parent *Record) (*Record, string) {
	if parent.Desc.DType != d.DType {
		return nil, ""
	}
	if w := ElemBytes(int(d.DType)); w > 0 {
		for _, c := range layoutCandidates(d, w) {
			c := c
			out, err := c.Apply([][]byte{payload})
			if err != nil || PayloadSHA(out) != parent.Payload {
				continue
			}
			inv := InvertPerm(c)
			if p.dry {
				r := &Record{Desc: *d, PayloadLen: int64(len(payload)), Payload: PayloadSHA(payload), Parents: []ID{parent.CID}, Ops: []Op{inv}}
				r.CID = CID(&r.Desc, r.PayloadLen, r.Payload)
				return r, "layout"
			}
			if r, err := p.put(*d, []ID{parent.CID}, []Op{inv}, nil, payload); err == nil {
				return r, "layout"
			}
		}
	}
	if PayloadSHA(payload) == parent.Payload && !p.dry {
		return nil, ""
	}
	return nil, ""
}

// rowGatherIndex finds, for every row of target, an unused identical row of parent; it
// returns nil when the rows are already in order or fewer than half match.
func rowGatherIndex(parent, target []byte, rb int) []uint32 {
	rows := len(parent) / rb
	prh := make(map[uint64][]int, rows)
	for i := 0; i < rows; i++ {
		h := rowHash(parent[i*rb : (i+1)*rb])
		prh[h] = append(prh[h], i)
	}
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
