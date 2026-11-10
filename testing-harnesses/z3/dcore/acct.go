package dcore

import (
	"errors"
	"fmt"
)

// Exact physical accounting for representation/delta authority.
//
// Model: a store holds physical chunk records (loose or inside packs). A
// content (object body identified by SHA-256) is referenced by Refs manifests
// (current, history, snapshots). Under CDC authority a live content keeps its
// chunk list; after migration a content may instead be a Delta (a record
// naming up to MaxBases whole bodies as bases) or a Rep (a record naming
// component chunks). A chunk is physically needed iff some LIVE content in CDC
// or Rep form lists it; a content is live iff Refs > 0 or it is a base of a
// live content (transitively). Hence a CDC chunk shared by several contents is
// reclaimable only when EVERY live content that lists it leaves CDC form, i.e.
// when the surviving references are covered by the migrating set: the union
// rule that the per-object "exclusive bytes" estimate violated (B8).

// Form kinds.
const (
	FormCDC = iota
	FormDelta
	FormRep
)

type Form struct {
	Kind int
	Opt  int // index into Content.Delta or 0 for Rep
}

// DeltaOpt is a delta of a content against whole base contents.
type DeltaOpt struct {
	Bases   []int
	Bytes   int64   // serialized record bytes (envelope + frames)
	ReadAmp float64 // modeled 4 KiB read amplification multiple of CDC
}

// RepOpt is a representation root with component chunks.
type RepOpt struct {
	Comps     []int // chunk ids (not necessarily present under CDC)
	RootBytes int64
	ReadAmp   float64
}

type Content struct {
	Name   string
	Refs   int
	Chunks []int
	Delta  []DeltaOpt
	Rep    *RepOpt
}

// Chunk is one physical chunk record.
type Chunk struct {
	Phys   int64
	Pack   int  // pack id, or -1 for a loose file
	Exists bool // present before migration (CDC chunks); components start absent
}

// Model is a store snapshot plus candidate representations.
type Model struct {
	Chunks         []Chunk
	Contents       []Content
	RecordOverhead int64 // fixed bytes per body record (locator + header)
	MaxDepth       int
	MaxBases       int
}

var ErrInfeasible = errors.New("dcore: infeasible plan")

// Totals are the physical byte counts of one assignment at each reclamation stage.
type Totals struct {
	Logical     int64 // live chunks + records: what is needed
	AfterGC     int64 // dead loose chunks and fully dead packs removed
	AfterRepack int64 // packs whose live fraction <= threshold rewritten
	Peak        int64 // staged: everything pre-existing plus all new bytes, before any deletion
	Records     int64
	NewBytes    int64 // bytes newly written (records + absent components)
	Live        int   // live contents
}

// Eval computes exact totals for assign (one Form per content). It is the
// from-scratch oracle used to validate all incremental planners.
func (m *Model) Eval(assign []Form, repackLive float64) (Totals, error) {
	var t Totals
	n := len(m.Contents)
	if len(assign) != n {
		return t, fmt.Errorf("assignment length %d != %d", len(assign), n)
	}
	live := make([]bool, n)
	var stack []int
	for i := range m.Contents {
		if m.Contents[i].Refs > 0 {
			live[i] = true
			stack = append(stack, i)
		}
	}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if f := assign[i]; f.Kind == FormDelta {
			if f.Opt < 0 || f.Opt >= len(m.Contents[i].Delta) {
				return t, ErrInfeasible
			}
			d := m.Contents[i].Delta[f.Opt]
			if len(d.Bases) == 0 || (m.MaxBases > 0 && len(d.Bases) > m.MaxBases) {
				return t, ErrInfeasible
			}
			for _, b := range d.Bases {
				if b < 0 || b >= n {
					return t, ErrInfeasible
				}
				if !live[b] {
					live[b] = true
					stack = append(stack, b)
				}
			}
		} else if f.Kind == FormRep && m.Contents[i].Rep == nil {
			return t, ErrInfeasible
		}
	}
	// depth / cycle check
	depth := make([]int, n)
	state := make([]int8, n)
	var dfs func(i int) (int, error)
	dfs = func(i int) (int, error) {
		if state[i] == 2 {
			return depth[i], nil
		}
		if state[i] == 1 {
			return 0, ErrInfeasible // cycle
		}
		state[i] = 1
		d := 0
		if assign[i].Kind == FormDelta {
			for _, b := range m.Contents[i].Delta[assign[i].Opt].Bases {
				bd, err := dfs(b)
				if err != nil {
					return 0, err
				}
				d = max(d, bd+1)
			}
		}
		state[i], depth[i] = 2, d
		if m.MaxDepth > 0 && d > m.MaxDepth {
			return 0, ErrInfeasible
		}
		return d, nil
	}
	for i := range live {
		if live[i] {
			if _, err := dfs(i); err != nil {
				return t, err
			}
		}
	}
	// A CDC-form content that is only a base is pinned: needs a record.
	isBase := make([]bool, n)
	for i := range live {
		if live[i] && assign[i].Kind == FormDelta {
			for _, b := range m.Contents[i].Delta[assign[i].Opt].Bases {
				isBase[b] = true
			}
		}
	}
	need := make([]bool, len(m.Chunks))
	for i := range live {
		if !live[i] {
			continue
		}
		t.Live++
		switch assign[i].Kind {
		case FormCDC:
			for _, c := range m.Contents[i].Chunks {
				need[c] = true
			}
			if isBase[i] {
				t.Records += m.RecordOverhead
				t.NewBytes += m.RecordOverhead
			}
		case FormDelta:
			d := m.Contents[i].Delta[assign[i].Opt]
			t.Records += d.Bytes + m.RecordOverhead
			t.NewBytes += d.Bytes + m.RecordOverhead
		case FormRep:
			r := m.Contents[i].Rep
			for _, c := range r.Comps {
				need[c] = true
			}
			t.Records += r.RootBytes + m.RecordOverhead
			t.NewBytes += r.RootBytes + m.RecordOverhead
		}
	}
	packLive := map[int]int64{}
	packAll := map[int]int64{}
	var existing int64
	for c, ch := range m.Chunks {
		if need[c] {
			t.Logical += ch.Phys
			if !ch.Exists {
				t.NewBytes += ch.Phys
			}
		}
		if ch.Exists {
			existing += ch.Phys
			if ch.Pack >= 0 {
				packAll[ch.Pack] += ch.Phys
				if need[c] {
					packLive[ch.Pack] += ch.Phys
				}
			}
		}
	}
	t.Logical += t.Records
	t.Peak = existing + t.NewBytes
	// stage: ordinary GC removes dead loose chunks and fully dead packs.
	t.AfterGC = t.Records
	t.AfterRepack = t.Records
	for c, ch := range m.Chunks {
		if !ch.Exists && !need[c] {
			continue
		}
		if !ch.Exists { // a staged component that is live
			t.AfterGC += ch.Phys
			t.AfterRepack += ch.Phys
			continue
		}
		if ch.Pack < 0 {
			if need[c] {
				t.AfterGC += ch.Phys
				t.AfterRepack += ch.Phys
			}
			continue
		}
		if need[c] {
			t.AfterGC += ch.Phys
			t.AfterRepack += ch.Phys
		}
	}
	for p, all := range packAll {
		lv := packLive[p]
		if lv == 0 {
			continue // removed whole
		}
		dead := all - lv
		t.AfterGC += dead // partially live pack keeps its dead bytes
		if float64(lv) > repackLive*float64(all) {
			t.AfterRepack += dead // above the repack threshold: left alone
		}
	}
	return t, nil
}

// ExclusiveSaving is the defective per-object estimate: CDC bytes referenced
// by this content and by no other live content. It is kept only to reproduce
// the B8 accounting defect in tests and measurements.
func (m *Model) ExclusiveSaving(i int) int64 {
	refs := make([]int, len(m.Chunks))
	for j := range m.Contents {
		if m.Contents[j].Refs > 0 {
			seen := map[int]bool{}
			for _, c := range m.Contents[j].Chunks {
				if !seen[c] {
					seen[c] = true
					refs[c]++
				}
			}
		}
	}
	var s int64
	seen := map[int]bool{}
	for _, c := range m.Contents[i].Chunks {
		if !seen[c] {
			seen[c] = true
			if refs[c] == 1 {
				s += m.Chunks[c].Phys
			}
		}
	}
	return s
}

// GroupReclaimable is the corrected estimate: the physical bytes of chunks all
// of whose live CDC references lie inside group (the union rule).
func (m *Model) GroupReclaimable(group []int) int64 {
	total := make([]int, len(m.Chunks))
	inG := make([]int, len(m.Chunks))
	in := map[int]bool{}
	for _, g := range group {
		in[g] = true
	}
	for j := range m.Contents {
		if m.Contents[j].Refs <= 0 {
			continue
		}
		seen := map[int]bool{}
		for _, c := range m.Contents[j].Chunks {
			if !seen[c] {
				seen[c] = true
				total[c] += m.Contents[j].Refs
				if in[j] {
					inG[c] += m.Contents[j].Refs
				}
			}
		}
	}
	var s int64
	for c := range m.Chunks {
		if total[c] > 0 && total[c] == inG[c] && m.Chunks[c].Exists {
			s += m.Chunks[c].Phys
		}
	}
	return s
}
