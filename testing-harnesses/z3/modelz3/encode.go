package modelz3

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sync"

	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// BaseTensor is an earlier file's tensor as a descendant can reference it: exact bytes named by
// their SHA-256, held either as a STORED component (Raw) or reproduced by a one-op child
// representation (the tensor was itself coded as a residual). Src/Off are analysis-side only; the
// serialized representation never contains them.
type BaseTensor struct {
	Name  string
	Type  string
	Shape []int64
	SHA   SHA
	Size  int64
	Src   *Src
	Off   int64
	Width int
	Hops  int // delta hops to a stored component (0 = Raw)
	Raw   bool
	spec  *childSpec // !Raw: how to rebuild the bytes
	child *kinds.Bundle
}

type childSpec struct {
	base *BaseTensor // nil: standalone planes
	res  []byte
	w, e int
}

// Policy selects the exact per-tensor candidates the encoder may use.
type Policy struct {
	Delta  bool // XOR-planes against the same name/type/size base tensor
	Planes bool // standalone byte planes
	Chunk  int  // target bytes per residual chunk (default 256 KiB)
	// MaxHops bounds the chain of residual operations a tensor may sit at: a stored component is
	// hop 0, a planes residual or an XOR against a hop-h base is hop h+1. Decoding a tensor at hop h
	// nests h representations (the file's, then h-1 children), so the contract's depth limit of 4
	// allows MaxHops <= 4; the default is 3.
	MaxHops int
}

// EncStats counts what the encoder chose, in tensor bytes.
type EncStats struct {
	ResIDs                          map[SHA]bool // residual components this file's operations use
	Copy, CopyBase, XOR, Planes     int64        // tensor bytes by chosen op
	NCopy, NCopyBase, NXOR, NPlanes int
	Framing                         int64
}

func chunkElems(w, target int) int { return max(1, target/max(1, w)) }

// childBundle builds (once) the one-op representation that reproduces this tensor.
func (b *BaseTensor) childBundle(has func(SHA) bool) (*kinds.Bundle, error) {
	if b.child != nil {
		return b.child, nil
	}
	if b.Raw {
		return nil, fmt.Errorf("modelz3: %s is a stored component", b.Name)
	}
	s := b.spec
	rep := &repz3.Rep{Kind: repz3.KindModel, Size: b.Size}
	var objs [][]byte
	var kids []*kinds.Bundle
	op := kinds.ModelOp{Tag: kinds.ModelPlanes, W: s.w, E: s.e}
	if s.base != nil {
		op.Tag = kinds.ModelXORPlanes
		m, kid, extra, err := s.base.member(has)
		if err != nil {
			return nil, err
		}
		rep.Members = append(rep.Members, m)
		objs = append(objs, extra...)
		if kid != nil {
			kids = append(kids, kid)
		}
		op.Base = 0
	}
	rid := sha256.Sum256(s.res)
	rep.Members = append(rep.Members, repz3.Member{Role: repz3.Stored, SHA: rid, Size: int64(len(s.res))})
	op.Res = len(rep.Members) - 1
	objs = append(objs, s.res)
	rep.Recipe = kinds.BuildModelRecipe([]kinds.ModelOp{op})
	bd, err := kinds.NewBundle(rep, objs, kids...)
	b.child = bd
	return bd, err
}

// member is how a parent representation lists this tensor's bytes. A stored base that the store
// does not hold yet (the base file was kept as CDC chunks only) travels with the parent as an
// extra object: the descendant owns what it depends on.
func (b *BaseTensor) member(has func(SHA) bool) (repz3.Member, *kinds.Bundle, [][]byte, error) {
	if b.Raw {
		m := repz3.Member{Role: repz3.Stored, SHA: b.SHA, Size: b.Size}
		if has != nil && has(b.SHA) {
			return m, nil, nil, nil
		}
		raw := make([]byte, b.Size)
		if err := readFull(b.Src.R, raw, b.Off); err != nil {
			return m, nil, nil, err
		}
		return m, nil, [][]byte{raw}, nil
	}
	c, err := b.childBundle(has)
	if err != nil {
		return repz3.Member{}, nil, nil, err
	}
	return repz3.Member{Role: repz3.Derived, SHA: b.SHA, Size: b.Size, Child: c.ID}, c, nil, nil
}

type cand struct {
	tsha SHA
	tb   []byte // the tensor bytes (held only for Raw / child specs that need them)
	kind int    // 0 raw, 1 planes, 2 xor, 3 same-as-base
	res  []byte
	cost [3]int64
	base *BaseTensor
	err  error
}

// EncodeModel codes one file as a KindModel representation. bases are candidate base files as tensor
// name maps (each tensor takes its cheapest exact candidate over all of them; none = standalone); has reports components already stored (a stored component costs nothing
// to reference). It returns the bundle and, for every tensor of this file, how a later file can
// name it as a base.
func EncodeModel(src *Src, lay *Layout, bases []map[string]*BaseTensor, has func(SHA) bool, pol Policy) (*kinds.Bundle, map[string]*BaseTensor, *EncStats, error) {
	if pol.Chunk == 0 {
		pol.Chunk = 256 << 10
	}
	if pol.MaxHops == 0 {
		pol.MaxHops = 3
	}
	tens := lay.Tensors()
	cands := make([]*cand, len(tens))
	var wg sync.WaitGroup
	sem := newWsem(1 << 30)
	for i, t := range tens {
		wg.Add(1)
		go func(i int, t *Seg) {
			defer wg.Done()
			n := t.Size()
			sem.acquire(4 * n)
			defer sem.release(4 * n)
			c := &cand{}
			cands[i] = c
			tb := make([]byte, n)
			if err := readFull(src.R, tb, t.Begin); err != nil {
				c.err = err
				return
			}
			c.tb = tb
			c.tsha = sha256.Sum256(tb)
			var cs []*BaseTensor
			for _, m := range bases {
				if bt := m[t.Name]; bt != nil && bt.Type == t.Type && bt.Size == n && sameShape(bt.Shape, t.Shape) {
					cs = append(cs, bt)
				}
			}
			for _, bt := range cs {
				if bt.SHA == c.tsha {
					c.kind, c.base = 3, bt
					return
				}
			}
			if has != nil && has(c.tsha) {
				c.kind = 0
				return
			}
			e := chunkElems(t.Width, pol.Chunk)
			c.cost[0] = lab.PackedLen(tb)
			best := c.cost[0]
			var pr, xr []byte
			if pol.Planes {
				pr = kinds.PlaneChunks(tb, t.Width, e)
				c.cost[1] = lab.PackedLen(pr)
				if c.cost[1] < best {
					best, c.kind = c.cost[1], 1
				}
			}
			if pol.Delta {
				for _, bt := range cs {
					if bt.Hops >= pol.MaxHops {
						continue
					}
					bb := make([]byte, n)
					if err := readFull(bt.Src.R, bb, bt.Off); err != nil {
						c.err = err
						return
					}
					r := kinds.PlaneChunks(XOR(nil, tb, bb), t.Width, e)
					cost := lab.PackedLen(r)
					if bt.Raw && has != nil && !has(bt.SHA) {
						cost += lab.PackedLen(bb) // the base component has to be stored for this file to stand
					}
					if cost < best {
						best, c.kind, xr, c.base = cost, 2, r, bt
						c.cost[2] = cost
					}
				}
			}
			switch c.kind {
			case 1:
				c.res = pr
			case 2:
				c.res = xr
			}
		}(i, t)
	}
	wg.Wait()
	for _, c := range cands {
		if c.err != nil {
			return nil, nil, nil, c.err
		}
	}
	rep := &repz3.Rep{Kind: repz3.KindModel, Size: lay.Size}
	var ops []kinds.ModelOp
	var objs [][]byte
	var kids []*kinds.Bundle
	kidSeen := map[SHA]bool{}
	idx := map[[2]SHA]int{}
	addMember := func(m repz3.Member) int {
		k := [2]SHA{m.SHA, m.Child}
		if i, ok := idx[k]; ok {
			return i
		}
		rep.Members = append(rep.Members, m)
		idx[k] = len(rep.Members) - 1
		return len(rep.Members) - 1
	}
	addKid := func(b *kinds.Bundle) {
		if b != nil && !kidSeen[b.ID] {
			kidSeen[b.ID] = true
			kids = append(kids, b)
		}
	}
	stats := &EncStats{ResIDs: map[SHA]bool{}}
	out := map[string]*BaseTensor{}
	ti := 0
	for _, s := range lay.Segs {
		if s.Kind != "tensor" {
			b := make([]byte, s.Size())
			if err := readFull(src.R, b, s.Begin); err != nil {
				return nil, nil, nil, err
			}
			// framing is cut with production CDC: a GGUF header is megabytes of tokenizer, and a
			// small metadata edit must not re-store all of it
			if err := lab.ChunkCDC(bytes.NewReader(b), func(_ int64, c []byte) error {
				cp := append([]byte(nil), c...)
				ops = append(ops, kinds.ModelOp{Tag: kinds.ModelCopy, M: addMember(repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(cp), Size: int64(len(cp))})})
				objs = append(objs, cp)
				return nil
			}); err != nil {
				return nil, nil, nil, err
			}
			stats.Framing += s.Size()
			continue
		}
		c, t := cands[ti], tens[ti]
		ti++
		n := t.Size()
		bt := &BaseTensor{Name: t.Name, Type: t.Type, Shape: t.Shape, SHA: c.tsha, Size: n, Src: src, Off: t.Begin, Width: t.Width}
		e := chunkElems(t.Width, pol.Chunk)
		op := kinds.ModelOp{Name: t.Name}
		switch c.kind {
		case 3: // identical to the base tensor: name it, store nothing
			m, kid, extra, err := c.base.member(has)
			if err != nil {
				return nil, nil, nil, err
			}
			op.Tag, op.M = kinds.ModelCopy, addMember(m)
			objs = append(objs, extra...)
			addKid(kid)
			bt.Raw, bt.spec, bt.child, bt.Hops = c.base.Raw, c.base.spec, c.base.child, c.base.Hops
			stats.CopyBase += n
			stats.NCopyBase++
		case 0:
			op.Tag = kinds.ModelCopy
			op.M = addMember(repz3.Member{Role: repz3.Stored, SHA: c.tsha, Size: n})
			objs = append(objs, c.tb)
			bt.Raw = true
			stats.Copy += n
			stats.NCopy++
		case 1, 2:
			rid := sha256.Sum256(c.res)
			ri := addMember(repz3.Member{Role: repz3.Stored, SHA: rid, Size: n})
			stats.ResIDs[rid] = true
			objs = append(objs, c.res)
			op.Res, op.W, op.E = ri, t.Width, e
			spec := &childSpec{res: c.res, w: t.Width, e: e}
			if c.kind == 2 {
				m, kid, extra, err := c.base.member(has)
				if err != nil {
					return nil, nil, nil, err
				}
				op.Tag, op.Base = kinds.ModelXORPlanes, addMember(m)
				objs = append(objs, extra...)
				addKid(kid)
				spec.base = c.base
				bt.Hops = c.base.Hops + 1
				stats.XOR += n
				stats.NXOR++
			} else {
				op.Tag = kinds.ModelPlanes
				bt.Hops = 1
				stats.Planes += n
				stats.NPlanes++
			}
			bt.spec = spec
		}
		out[t.Name] = bt
		ops = append(ops, op)
		c.tb = nil
	}
	rep.Recipe = kinds.BuildModelRecipe(ops)
	bd, err := kinds.NewBundle(rep, objs, kids...)
	if err != nil {
		return nil, nil, nil, err
	}
	return bd, out, stats, nil
}

func repIDs(b *kinds.Bundle, into map[SHA]bool) {
	into[b.ID] = true
	for _, c := range b.Children {
		repIDs(c, into)
	}
}

// Commit persists the bundle into st and returns its digest and the physical cost of what was new:
// component bytes raw and under the pack rule, representation objects, object count.
func Commit(st *repz3.MemStore, b *kinds.Bundle) (repz3.SHA, Cost, error) {
	id, added, err := b.Persist(st)
	if err != nil {
		return id, Cost{}, err
	}
	reps := map[SHA]bool{}
	repIDs(b, reps)
	var c Cost
	for _, a := range added {
		o := st.Bytes(a)
		c.Objects++
		if reps[a] {
			c.Recipe += int64(len(o))
			continue
		}
		c.Raw += int64(len(o))
		c.Packed += lab.PackedLen(o)
	}
	return id, c, nil
}

// Prove reconstructs id from st through the serialized representations only and compares length,
// SHA-256 and bytes with the file.
func Prove(st *repz3.MemStore, id repz3.SHA, src *Src) error {
	env := kinds.NewEnvLimits(st, repz3.Limits{MaxDepth: repz3.HardMaxDepth, MaxWhole: 1 << 30})
	var buf bytes.Buffer
	buf.Grow(int(src.Size))
	n, sum, err := env.Reconstruct(id, &buf)
	if err != nil {
		return err
	}
	want := make([]byte, src.Size)
	if err := readFull(src.R, want, 0); err != nil {
		return err
	}
	if n != src.Size || sum != sha256.Sum256(want) || !bytes.Equal(buf.Bytes(), want) {
		return fmt.Errorf("modelz3: reconstruction of %s differs", src.Name)
	}
	return nil
}

func bundleObjects(b *kinds.Bundle, reps map[SHA]bool, objs map[SHA][]byte) {
	reps[b.ID] = true
	objs[b.ID] = b.Raw
	for _, o := range b.Objects {
		objs[sha256.Sum256(o)] = o
	}
	for _, c := range b.Children {
		bundleObjects(c, reps, objs)
	}
}

// DryCost prices persisting b into st without changing st.
func DryCost(st *repz3.MemStore, b *kinds.Bundle, pk *Packer) Cost {
	reps := map[SHA]bool{}
	objs := map[SHA][]byte{}
	bundleObjects(b, reps, objs)
	var c Cost
	for id, o := range objs {
		if st.Has(id) {
			continue
		}
		c.Objects++
		if reps[id] {
			c.Recipe += int64(len(o))
			continue
		}
		c.Raw += int64(len(o))
		c.Packed += pk.PutPacked(id, o)
	}
	return c
}

// Retained prices the objects a representation needs on its own: everything Reachable names,
// raw and under the pack rule. It is the cost of keeping the artifact if nothing else were stored.
func Retained(st *repz3.MemStore, id repz3.SHA, pk *Packer) (Cost, error) {
	env := kinds.NewEnvLimits(st, repz3.Limits{MaxDepth: repz3.HardMaxDepth})
	r, err := env.Reachable(id)
	if err != nil {
		return Cost{}, err
	}
	var c Cost
	for obj, role := range r.Role {
		o := st.Bytes(obj)
		c.Objects++
		if role == repz3.DepRep {
			c.Recipe += int64(len(o))
			continue
		}
		c.Raw += int64(len(o))
		c.Packed += pk.PutPacked(obj, o)
	}
	return c, nil
}
