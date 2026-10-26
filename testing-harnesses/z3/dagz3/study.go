package dagz3

import (
	"bytes"
	"fmt"
	"sort"
	"time"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// Curve is one fixed policy (every version analysed with the same structural cap) applied to
// a chain of versions into one accumulating store: the persisted cost of each version is what
// it adds to a store holding its predecessors.
type Curve struct {
	Cap      int
	Bytes    []int64 // new persisted bytes per version (representations, components, tables, indexes)
	Objects  []int   // new objects per version
	Depth    []int
	Analyze  []time.Duration
	Rejected int
}

// Total is the cumulative cost including a per-object physical overhead.
func (c *Curve) Total(perObject int64) (t int64) {
	for i := range c.Bytes {
		t += c.Bytes[i] + perObject*int64(c.Objects[i])
	}
	return t
}

// persistNew puts b into st and returns the bytes and the number of objects that were new.
func persistNew(st *repz3.MemStore, b *kinds.Bundle) (bytes int64, objs int, err error) {
	_, added, err := b.Persist(st)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range added {
		n, _ := st.Size(id)
		bytes += n
	}
	return bytes, len(added), nil
}

// LevelCurve runs each cap over the versions. Every stored version is proven exact by
// Discover; nothing is estimated.
func LevelCurve(versions [][]byte, caps []int) ([]Curve, error) {
	var out []Curve
	for _, k := range caps {
		st := repz3.NewMemStore()
		c := Curve{Cap: k}
		for _, v := range versions {
			t0 := time.Now()
			bud := DefaultBudget(k)
			b, err := Discover(v, bud)
			if err != nil {
				return nil, err
			}
			c.Analyze = append(c.Analyze, time.Since(t0))
			for _, r := range bud.Rejected {
				c.Rejected += r
			}
			n, objs, err := persistNew(st, b)
			if err != nil {
				return nil, err
			}
			c.Bytes, c.Objects, c.Depth = append(c.Bytes, n), append(c.Objects, objs), append(c.Depth, Depth(b))
		}
		out = append(out, c)
	}
	return out, nil
}

// CandSet is a set of exact candidate representations for one object.
type CandSet struct {
	Cands   []Cand
	Bundles map[string]*kinds.Bundle
}

// Candidates builds the candidate of every cap (identical results are listed once, under the
// shallowest cap that produces them) as proven, serialized representation DAGs.
func Candidates(object string, data []byte, caps []int) (*CandSet, error) {
	cs := &CandSet{Bundles: map[string]*kinds.Bundle{}}
	seen := map[repz3.SHA]bool{}
	for _, k := range caps {
		b, err := Discover(data, DefaultBudget(k))
		if err != nil {
			return nil, err
		}
		if seen[b.ID] {
			continue
		}
		seen[b.ID] = true
		id := fmt.Sprintf("%s/d%d", object, k)
		c, err := CandFromBundle(id, object, b, data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		cs.Cands = append(cs.Cands, c)
		cs.Bundles[id] = b
	}
	return cs, nil
}

// Union merges candidate sets of several objects into one problem over the given store.
func Assemble(sets map[string]*CandSet, order []string, datas map[string][]byte, base *repz3.MemStore, perObject int64) *Problem {
	p := &Problem{PerObject: perObject, Base: map[repz3.SHA]bool{}}
	if base != nil {
		for _, id := range base.IDs() {
			p.Base[id] = true
		}
	}
	for _, o := range order {
		c0 := sets[o].Cands[0]
		p.Objects = append(p.Objects, Obj{Name: o, Out: c0.Out, Len: c0.Len})
		p.Cands = append(p.Cands, sets[o].Cands...)
	}
	return p
}

// Apply persists the plan's chosen bundles into st and returns the bytes and objects that
// were actually new; the plan's prediction must equal them (the accounting gate).
func Apply(st *repz3.MemStore, sets map[string]*CandSet, plan *Plan, order []string) (bytes int64, objs int, err error) {
	for _, o := range order {
		b := sets[o].Bundles[plan.Choice[o]]
		if b == nil {
			return 0, 0, fmt.Errorf("dagz3: plan chooses unknown candidate %q for %q", plan.Choice[o], o)
		}
		n, k, err := persistNew(st, b)
		if err != nil {
			return 0, 0, err
		}
		bytes, objs = bytes+n, objs+k
	}
	return bytes, objs, nil
}

// ReadCost is the objects and bytes a point read fetched, by role.
type ReadCost struct {
	Objects                                      int
	Bytes                                        int64
	RepBytes, TableBytes, PayloadBytes, IdxBytes int64
	Reps                                         int
	Whole                                        bool // no nested access: the whole root was reconstructed
}

func classify(role repz3.DepRole, rc *ReadCost, n int64) {
	switch role {
	case repz3.DepRep:
		rc.RepBytes += n
		rc.Reps++
	case repz3.DepTable:
		rc.TableBytes += n
	case repz3.DepIndex:
		rc.IdxBytes += n
	default:
		rc.PayloadBytes += n
	}
}

func readCost(st *repz3.MemStore, env *repz3.Env, root repz3.SHA) ReadCost {
	_, touched := st.Touched() // before the classification walk, which reads objects itself
	reach, _ := env.Reachable(root)
	var rc ReadCost
	for id, n := range touched {
		rc.Objects++
		rc.Bytes += n
		classify(reach.Role[id], &rc, n)
	}
	return rc
}

// descend finds the first representation below root (depth-first, member order) whose output
// is exactly size bytes and which is not a codec wrapper, and opens it through Child calls:
// only representation objects on the path are read.
func descend(env *repz3.Env, root repz3.SHA, size int64) (*repz3.Call, bool, error) {
	var walk func(c *repz3.Call) (*repz3.Call, error)
	walk = func(c *repz3.Call) (*repz3.Call, error) {
		if c.Rep.Size == size && c.Rep.Kind != repz3.KindGzip && c.Rep.Kind != repz3.KindZstd {
			return c, nil
		}
		for i, m := range c.Rep.Members {
			if m.Role != repz3.Derived {
				continue
			}
			ch, err := c.Child(i)
			if err != nil {
				return nil, err
			}
			if got, err := walk(ch); err != nil || got != nil {
				return got, err
			}
		}
		return nil, nil
	}
	c, err := env.Open(root)
	if err != nil {
		return nil, false, err
	}
	got, err := walk(c)
	return got, got != nil, err
}

// PointReadPage reads page p (4096 bytes) of the database of length dbLen that is nested in
// the representation rooted at root. When the database (or its plaintext, cut by CDC) is
// exposed as a representation of its own the read touches only the representation objects on
// the path plus the node/chunk components that hold the page; otherwise the whole root is
// reconstructed, which is what the structure forces.
func PointReadPage(st *repz3.MemStore, env *repz3.Env, root repz3.SHA, dbLen int64, p int64, want []byte) (ReadCost, error) {
	st.ResetReads()
	c, ok, err := descend(env, root, dbLen)
	if err != nil {
		return ReadCost{}, err
	}
	var page []byte
	switch {
	case ok && c.Rep.Kind == repz3.KindSQLite:
		page, err = kinds.ReadSQLitePage(c, p)
	case ok && c.Rep.Kind == repz3.KindConcat:
		page, err = readRange(c, p*4096, 4096)
	default:
		if _, _, err = env.Reconstruct(root, discard{}); err == nil {
			rc := readCost(st, env, root)
			rc.Whole = true
			return rc, nil
		}
	}
	if err != nil {
		return ReadCost{}, err
	}
	if !bytes.Equal(page, want) {
		return ReadCost{}, fmt.Errorf("dagz3: point read returned other bytes")
	}
	return readCost(st, env, root), nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// readRange reads [off, off+n) of a concat representation by fetching only the members that
// cover it.
func readRange(c *repz3.Call, off, n int64) ([]byte, error) {
	var out []byte
	pos := int64(0)
	for i, m := range c.Rep.Members {
		if pos+m.Size > off && pos < off+n {
			b, err := c.Member(i)
			if err != nil {
				return nil, err
			}
			lo := max(off-pos, 0)
			hi := min(off+n-pos, m.Size)
			out = append(out, b[lo:hi]...)
		}
		pos += m.Size
	}
	if int64(len(out)) != n {
		return nil, fmt.Errorf("dagz3: range [%d,%d) outside the representation", off, off+n)
	}
	return out, nil
}

// Hindsight compares three policies over a chain of objects whose candidate sets are fixed:
// each object on its own against the empty store, each against the accumulated store, and the
// global optimum over the whole chain (a yardstick, not a production policy).
func Hindsight(sets map[string]*CandSet, order []string, datas map[string][]byte, perObject int64) (indep, online, hindsight int64, err error) {
	// independent: each object chosen as if alone in an empty store, then paid for in order
	chosen := map[string]string{}
	for _, o := range order {
		p := Assemble(sets, []string{o}, datas, nil, perObject)
		pl, e := p.Independent()
		if e != nil {
			return 0, 0, 0, e
		}
		chosen[o] = pl.Choice[o]
	}
	st := repz3.NewMemStore()
	b, n, e := Apply(st, sets, &Plan{Choice: chosen}, order)
	if e != nil {
		return 0, 0, 0, e
	}
	indep = b + perObject*int64(n)
	// online: sequential, each against the store so far
	st = repz3.NewMemStore()
	for _, o := range order {
		p := Assemble(sets, []string{o}, datas, st, perObject)
		pl, e := p.Marginal()
		if e != nil {
			return 0, 0, 0, e
		}
		b, n, e := Apply(st, sets, pl, []string{o})
		if e != nil {
			return 0, 0, 0, e
		}
		online += b + perObject*int64(n)
	}
	// hindsight: the exact optimum over all objects together
	p := Assemble(sets, order, datas, nil, perObject)
	pl, e := p.Exact(5_000_000)
	if e != nil {
		return 0, 0, 0, e
	}
	return indep, online, pl.Total, nil
}

// SortedKeys is a small helper for stable reports.
func SortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
