package kinds

import (
	"fmt"

	"zeros3-z3/gitz3"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
)

// Git is the Z3-16 gp-zlib tier (gitz3.SeqPlan, mode "zlib"): every pack entry's
// exact zlib stream is a component, listed in pack order as the representation's
// STORED members; the pack header, trailer, OFS distances, entry headers and
// delta bases are derived or kept in the opaque Recipe (the gitz3 meta bytes,
// unchanged). Tiny entries are not inlined (InlineMax 1): the contract has no
// inline piece, so every component is a member.
//
// The native index maps each object id (20 or 32 bytes, per the repository's
// object format) to the first component of the entry that holds the object. The
// .idx file is not represented here (gitz3.IdxPlan derives it from the pack).

// GitStats describes a Git analysis.
type GitStats struct {
	Entries, Deltas, Members int
	IndexRecords             int
}

// EncodeGit analyses a pack file.
func EncodeGit(pack []byte, algo gitz3.Algo) (*Bundle, *GitStats, error) {
	p, err := gitz3.ParsePack(pack, algo, gitz3.Limits{})
	if err != nil {
		return nil, nil, err
	}
	if _, err := p.Resolve(); err != nil {
		return nil, nil, err
	}
	an, err := gitz3.AnalyzePack(p)
	if err != nil {
		return nil, nil, err
	}
	pl, ps := an.SeqPlan(gitz3.RepOpts{Mode: "zlib", InlineMax: 1})
	meta, _ := pl.Detail["meta"].([]byte)
	out := &repz3.Rep{Kind: repz3.KindGit, Size: p.Size, Recipe: meta}
	var objs [][]byte
	for i := range pl.Pieces {
		c := pl.Pieces[i].Comp
		if c == nil {
			return nil, nil, fmt.Errorf("kinds: inline piece %d (the contract has none)", i)
		}
		b, err := c.Data()
		if err != nil {
			return nil, nil, err
		}
		out.Members = append(out.Members, repz3.Member{Role: repz3.Stored, SHA: c.ID, Size: c.Size})
		objs = append(objs, b)
	}
	pairs := map[string]repz3.SHA{}
	for i := range p.Entries {
		if fp := ps.FirstPiece[i]; fp < len(out.Members) {
			pairs[string(p.Entries[i].ID)] = out.Members[fp].SHA
		}
	}
	recs, n, err := repz3.BuildIndex(algo.Size(), pairs)
	if err != nil {
		return nil, nil, err
	}
	xid := sha(recs)
	objs = append(objs, recs)
	out.Indexes = []repz3.Index{{Type: repz3.IndexNative, KeyWidth: algo.Size(), Count: n, Ref: xid}}
	b, err := newBundle(out, objs)
	return b, &GitStats{Entries: len(p.Entries), Deltas: ps.Deltas, Members: len(out.Members), IndexRecords: int(n)}, err
}

type gitDecoder struct{}

func (gitDecoder) Decode(c *repz3.Call) error {
	pieces := make([]lab.Piece, len(c.Rep.Members))
	for i := range c.Rep.Members {
		m := &c.Rep.Members[i]
		if m.Role != repz3.Stored {
			return fmt.Errorf("%w: Git member %d is not a stored component", repz3.ErrCorrupt, i)
		}
		pieces[i] = lab.Piece{Comp: &lab.Comp{ID: lab.ID(m.SHA), Size: m.Size}}
	}
	fetched := 0
	err := gitz3.RebuildSeq(c.Rep.Recipe, pieces, func(id lab.ID) ([]byte, error) {
		fetched++
		return c.Get(repz3.SHA(id))
	}, c.W)
	if err == nil && fetched != len(pieces) {
		return fmt.Errorf("%w: Git recipe consumed %d of %d members", repz3.ErrCorrupt, fetched, len(pieces))
	}
	return err
}

// Expand: every Git component is a listed member, so nothing is VIA.
func (gitDecoder) Expand(*repz3.Call, func(repz3.SHA)) error { return nil }
