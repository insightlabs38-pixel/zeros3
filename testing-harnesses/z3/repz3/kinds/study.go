package kinds

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"time"

	"zeros3-z3/repz3"
)

// The Z3-21 reachability study (laboratory only). One stored representation is
// walked under the three policies the batch compares:
//
//	A  Expand: the kinds' Expand reads stored structure (no extra storage)
//	B  sidecar: every representation with VIA components gets an exact dependency
//	   component, count | sorted 32-byte SHA-256 ids, and the walk reads that
//	   instead of the structure (modelled here; it is not on the wire)
//	C  the contract as built: Expand, except that a representation with an
//	   owning record section is walked by reading that section
//
// Nothing here deletes anything; the mark phase is repz3.Env.ReachableAll.

// BuildDeps serialises a dependency sidecar: uvarint(count) | ids sorted ascending.
func BuildDeps(ids []repz3.SHA) []byte {
	ids = append([]repz3.SHA(nil), ids...)
	sort.Slice(ids, func(i, j int) bool { return bytes.Compare(ids[i][:], ids[j][:]) < 0 })
	out := binary.AppendUvarint(nil, uint64(len(ids)))
	for _, id := range ids {
		out = append(out, id[:]...)
	}
	return out
}

// Walk is the cost of one mark of one root.
type Walk struct {
	Live      int           // objects kept alive
	ReadBytes int64         // bytes read from objects other than representation objects
	ReadObjs  int           // ... and how many such objects
	RepBytes  int64         // representation objects read (identical under every policy)
	Time      time.Duration // mean over repeats
}

// ReachStudy compares the policies on one root.
type ReachStudy struct {
	Via       int // components found by Expand (VIA), over the whole tree
	Reps      int // representations with at least one VIA component
	A, C, B   Walk
	Sidecars  int   // objects policy B adds
	SidecarB  int64 // bytes policy B adds, sidecar components
	RepDeltaB int64 // bytes policy B adds to the representation objects (one section reference each)
}

func timed(f func()) time.Duration {
	f()
	n := 1
	for {
		t0 := time.Now()
		for i := 0; i < n; i++ {
			f()
		}
		if el := time.Since(t0); el > 150*time.Millisecond || n >= 1<<10 {
			return el / time.Duration(n)
		}
		n *= 4
	}
}

func measure(st *repz3.MemStore, env *repz3.Env, root repz3.SHA) (Walk, *repz3.Reach, error) {
	st.ResetReads()
	r, err := env.Reachable(root)
	if err != nil {
		return Walk{}, nil, err
	}
	_, touched := st.Touched()
	w := Walk{Live: len(r.Role)}
	for id, n := range touched {
		if r.Role[id] == repz3.DepRep {
			w.RepBytes += n
			continue
		}
		w.ReadBytes += n
		w.ReadObjs++
	}
	w.Time = timed(func() { env.Reachable(root) })
	return w, r, nil
}

// StudyReach measures policies A, B and C on root, which must be stored in st.
func StudyReach(st *repz3.MemStore, root repz3.SHA) (*ReachStudy, error) {
	out := &ReachStudy{}
	a := NewEnv(st)
	a.ExpandOnly = true
	wa, ra, err := measure(st, a, root)
	if err != nil {
		return nil, err
	}
	out.A = wa
	wc, _, err := measure(st, NewEnv(st), root)
	if err != nil {
		return nil, err
	}
	out.C = wc
	// policy B: model the sidecars as real objects of a scratch copy
	scratch := st.Clone()
	side := map[repz3.SHA]repz3.SHA{}
	for rid, via := range ra.Via {
		if len(via) == 0 {
			continue
		}
		b := BuildDeps(via)
		id, _ := scratch.Put(b)
		side[rid] = id
		out.Reps++
		out.Via += len(via)
		out.Sidecars++
		out.SidecarB += int64(len(b))
		out.RepDeltaB += 36 // flags | key width | count | 32-byte component id
	}
	markB := func() (int, error) {
		live := map[repz3.SHA]bool{}
		for rid := range ra.Role {
			if ra.Role[rid] != repz3.DepRep {
				continue
			}
			live[rid] = true
			rep, err := a.LoadRep(rid)
			if err != nil {
				return 0, err
			}
			for _, m := range rep.Members {
				if m.Role == repz3.Stored {
					live[m.SHA] = true
				}
			}
			for _, x := range rep.Indexes {
				live[x.Ref] = true
			}
			if sid, ok := side[rid]; ok {
				b, err := repz3.ReadObject(scratch, sid, -1, 1<<30)
				if err != nil {
					return 0, err
				}
				n, k := binary.Uvarint(b)
				if k <= 0 || uint64(len(b)-k) != 32*n {
					return 0, fmt.Errorf("sidecar %x malformed", sid[:6])
				}
				for i := uint64(0); i < n; i++ {
					var id repz3.SHA
					copy(id[:], b[uint64(k)+32*i:])
					live[id] = true
				}
				live[sid] = true
			}
		}
		return len(live), nil
	}
	scratch.ResetReads()
	live, err := markB()
	if err != nil {
		return nil, err
	}
	_, touched := scratch.Touched()
	out.B = Walk{Live: live}
	for id, n := range touched {
		if ra.Role[id] == repz3.DepRep {
			out.B.RepBytes += n
			continue
		}
		out.B.ReadBytes += n
		out.B.ReadObjs++
	}
	out.B.Time = timed(func() { markB() })
	if want := len(ra.Role) + out.Sidecars; live != want {
		return nil, fmt.Errorf("kinds: sidecar mark keeps %d objects, Expand mark %d + %d sidecars", live, len(ra.Role), out.Sidecars)
	}
	return out, nil
}

// PointRead is the bytes one native Git object read touches, by class.
type PointRead struct {
	Rep, Index, Table, Stream int64 // bytes touched in each class
	Objects                   int
	RepObjectBytes            int64 // the whole representation object (always read to check its digest)
	Total                     int64
	Payload                   int
}

// MeasureGitRead opens a stored Git representation, reads the non-delta object
// oid through the native index and reports what was touched.
func MeasureGitRead(st *repz3.MemStore, id repz3.SHA, oid []byte) (PointRead, error) {
	st.ResetReads()
	c, err := NewEnv(st).Open(id)
	if err != nil {
		return PointRead{}, err
	}
	_, payload, err := ReadGitObject(c, oid)
	if err != nil {
		return PointRead{}, err
	}
	_, touched := st.Touched()
	idx := map[repz3.SHA]bool{}
	for _, x := range c.Rep.Indexes {
		idx[x.Ref] = true
	}
	table := map[repz3.SHA]bool{}
	for _, m := range c.Rep.Members {
		table[m.SHA] = true
	}
	var p PointRead
	p.Payload = len(payload)
	for obj, n := range touched {
		p.Objects++
		p.Total += n
		switch {
		case obj == id:
			p.Rep += n
		case idx[obj]:
			p.Index += n
		case table[obj]:
			p.Table += n
		default:
			p.Stream += n
		}
	}
	p.RepObjectBytes, _ = st.Size(id)
	return p, nil
}

// LegacyGitRep rebuilds the Z3-20 shape of a stored Git representation (the gitz3
// meta in the recipe, every stream a generic STORED member) so its size can be
// compared with the table layout. Laboratory comparison only.
func LegacyGitRep(st *repz3.MemStore, id repz3.SHA) (*repz3.Rep, error) {
	c, err := NewEnv(st).Open(id)
	if err != nil {
		return nil, err
	}
	meta, tab, err := gitTable(c)
	if err != nil {
		return nil, err
	}
	old := &repz3.Rep{Kind: repz3.KindGit, Size: c.Rep.Size, Recipe: meta, Indexes: c.Rep.Indexes}
	for _, e := range tab {
		old.Members = append(old.Members, repz3.Member{Role: repz3.Stored, SHA: e.sha, Size: e.size})
	}
	return old, nil
}
