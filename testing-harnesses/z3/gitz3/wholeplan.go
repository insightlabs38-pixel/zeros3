package gitz3

import (
	"io"

	"zeros3-z3/lab"
)

// EntryWholePlan is the pack-entry structural split without any zlib or delta
// knowledge: header (12 B) and trailer inline, each entry's exact bytes
// (header + base encoding + zlib stream) one component. Concat plan, so range
// reads work generically.
func EntryWholePlan(p *Pack, o RepOpts) *lab.Plan {
	pl := &lab.Plan{Concat: true}
	pl.Pieces = append(pl.Pieces, lab.Piece{Inline: append([]byte(nil), p.Data[:12]...), Len: 12})
	for i := range p.Entries {
		e := &p.Entries[i]
		b := p.Data[e.Off:e.End]
		for _, pc := range mkPieces(b, o.inline(), "entry") {
			pl.Pieces = append(pl.Pieces, pc)
		}
	}
	hs := p.Algo.Size()
	pl.Pieces = append(pl.Pieces, lab.Piece{Inline: append([]byte(nil), p.Data[len(p.Data)-hs:]...), Len: int64(hs)})
	pl.Rebuild = lab.ConcatRebuild(pl)
	_ = io.EOF
	return pl
}

// NativeObjPlan is the oracle floor: the pack's unique objects as canonical
// components (header || payload), without any pack structure. It is NOT an exact
// representation of the pack (no order, delta choice, compression) and is marked
// Oracle. IndexBytes charges the native-ID -> ZeroS3-ID mapping for objects not
// already stored: SHA-1 repositories need 20+32 B per object; SHA-256
// repositories need nothing for single-component objects (the Git object id
// already is the component's SHA-256).
func NativeObjPlan(p *Pack, o RepOpts) (*lab.Plan, error) {
	pl := &lab.Plan{Oracle: true}
	cache := map[int][]byte{}
	var idx int64
	for i := range p.Entries {
		e := &p.Entries[i]
		pay, err := p.Object(i, cache)
		if err != nil {
			return nil, err
		}
		canon := Canonical(e.Type, pay)
		pcs := mkPieces(canon, 1, "native")
		newObj := false
		for _, pc := range pcs {
			if pc.Comp != nil && (o.Has == nil || !o.Has(pc.Comp.ID)) {
				newObj = true
			}
		}
		if newObj && len(pcs) > 0 && pcs[0].Comp != nil {
			if p.Algo == SHA1 {
				idx += 20 + 32
			} else if len(pcs) > 1 {
				idx += 32
			}
		}
		for _, pc := range pcs {
			pc.Via = true // the floor charges no recipe: only new object bytes and the id mapping
			pl.Pieces = append(pl.Pieces, pc)
		}
	}
	pl.IndexBytes = idx
	return pl, nil
}
