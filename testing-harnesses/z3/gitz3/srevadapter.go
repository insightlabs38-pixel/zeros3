package gitz3

import (
	"encoding/binary"
	"io"

	"zeros3-z3/lab"
	"zeros3-z3/srev"
)

// RevisionOf expresses an exact recipe in the Z3-15F shared shape: every piece
// is a Component (SHA-256 identity, optionally the Git object id as its Native
// hint), pieces the set table already names are `Via` members, and the rest of the
// recipe (permutation, per-entry meta, inline bytes) is the Residual. The Z3-16
// review asks whether anything beyond those ingredients was needed: it was not -
// Roots and Bases stay empty, because a Git pack's roots (refs) are not stored and
// its delta bases are descriptor content, not separate revisions.
func RevisionOf(pl *lab.Plan) *srev.Revision {
	r := &srev.Revision{Notes: map[string]any{}}
	natives, _ := pl.Detail["natives"].(map[lab.ID][]byte)
	var inline int64
	for _, pc := range pl.Pieces {
		if pc.Comp == nil {
			// the Plan layouts charge a length varint plus the bytes
			inline += int64(len(pc.Inline)) + int64(binary.PutUvarint(make([]byte, 10), uint64(len(pc.Inline))<<1|1))
			continue
		}
		c := pc.Comp
		comp := srev.Component{SHA: c.ID, Size: c.Size, Data: c.Data, Class: c.Class, Native: natives[c.ID]}
		r.Members = append(r.Members, srev.Member{C: comp, Via: pc.Via})
	}
	r.Residual = pl.MetaBytes + inline
	r.Index = pl.IndexBytes
	if pl.Rebuild != nil {
		r.Rebuild = func(w io.Writer, get func([32]byte) ([]byte, error)) error {
			return pl.Rebuild(w, func(id lab.ID) ([]byte, error) { return get(id) })
		}
	}
	return r
}
