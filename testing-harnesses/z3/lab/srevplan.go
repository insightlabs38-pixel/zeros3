package lab

import (
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
		p.Pieces = append(p.Pieces, Piece{Comp: c, Len: m.C.Size, Via: m.Via})
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
