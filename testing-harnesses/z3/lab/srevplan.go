package lab

import (
	"encoding/binary"
	"io"

	"zeros3-z3/srev"
)

// PlanFromRevision turns an adapter's Revision into a lab Plan: members become
// pieces (Via members cost no reference), residual and index bytes are charged
// as plan meta and index bytes.
func PlanFromRevision(r *srev.Revision) *Plan {
	p := &Plan{MetaBytes: r.Residual, IndexBytes: r.Index, Detail: r.Notes}
	for _, m := range r.Members {
		c := &Comp{ID: ID(m.C.SHA), Size: m.C.Size, Data: m.C.Data, Class: m.C.Class}
		p.Pieces = append(p.Pieces, Piece{Comp: c, Len: m.C.Size, Via: m.Via, Derived: m.Derived})
	}
	if r.Rebuild != nil {
		p.Rebuild = func(w io.Writer, get func(ID) ([]byte, error)) error {
			return r.Rebuild(w, func(s [32]byte) ([]byte, error) { return get(ID(s)) })
		}
	}
	return p
}

// Has reports whether the accounting store already holds the component.
func (a *Acct) Has(id ID) bool { _, ok := a.store[id]; return ok }

// RevisionOfPlan expresses an exact recipe in the shared shape: every piece is a
// Component (SHA-256 identity, optionally a native id taken from
// Detail["natives"]), pieces the plan reaches through another piece are `Via`
// members, and the rest of the recipe (permutation, per-entry meta, inline bytes)
// is the Residual. Roots and Bases stay empty.
func RevisionOfPlan(pl *Plan) *srev.Revision {
	r := &srev.Revision{Notes: map[string]any{}}
	natives, _ := pl.Detail["natives"].(map[ID][]byte)
	var inline int64
	for _, pc := range pl.Pieces {
		if pc.Comp == nil {
			// the Plan layouts charge a length varint plus the bytes
			inline += int64(len(pc.Inline)) + int64(binary.PutUvarint(make([]byte, 10), uint64(len(pc.Inline))<<1|1))
			continue
		}
		c := pc.Comp
		comp := srev.Component{SHA: c.ID, Size: c.Size, Data: c.Data, Class: c.Class, Native: natives[c.ID]}
		r.Members = append(r.Members, srev.Member{C: comp, Via: pc.Via, Derived: pc.Derived})
	}
	r.Residual = pl.MetaBytes + inline
	r.Index = pl.IndexBytes
	if pl.Rebuild != nil {
		r.Rebuild = func(w io.Writer, get func([32]byte) ([]byte, error)) error {
			return pl.Rebuild(w, func(id ID) ([]byte, error) { return get(id) })
		}
	}
	return r
}
