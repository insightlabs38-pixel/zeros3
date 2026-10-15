package kinds

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"zeros3-z3/merklez3"
	"zeros3-z3/repz3"
)

// Merkle is the Z3-15 hash-guided snapshot: the exact archive is the magic, the
// root's native identity and every reachable node once in depth-first order, which
// is a function of the graph. The representation lists only the root node (STORED);
// every other node is VIA (named by its parent's payload). The opaque recipe is
//
//	hash tag (1 B) | uvarint(len root) | root native identity
//
// Z3-21 native ownership. Every revision carries what it needs to rebuild itself:
// its record sections map the native identity of every node of its tree to the
// node component. A section lists the nodes its revision added; a revision that
// reuses a subtree lists the sections of the revision that owns that subtree, so
// the chain is closed under reuse (bounded by merkleMaxSections, then compacted
// into one full section). The sections own their targets (Owns): they are the
// complete VIA list, so garbage collection reads index bytes (64 B per node)
// instead of the graph. No global state is consulted to rebuild; the Catalog only
// finds reuse candidates for the analyzer. A native hash is only a key: the
// decoder recomputes it over every fetched node. A SHA-256 native hash needs no
// section (the identity is the component identity) and keeps Expand.

// Native hash functions the decoder knows (a finite list, selected by tag).
var merkleHashes = map[byte]merklez3.HashFn{
	1: merklez3.HashSHA3_256,
	2: merklez3.HashSHA512_256,
	3: merklez3.HashSHA256,
	4: merklez3.HashShort16,
}

func hashTag(h merklez3.HashFn) (byte, bool) {
	for t, f := range merkleHashes {
		if f.Name == h.Name {
			return t, true
		}
	}
	return 0, false
}

// MerkleStats describes a Merkle analysis.
type MerkleStats struct {
	Nodes, NewNodes, ReusedSubtrees int
	IndexRecords                    int
	Sections                        int  // sections listed by the representation
	Compacted, Major                bool // the chain was compacted (Major: into one full section)
}

// merkleMaxSections bounds the section chain a revision lists (the contract allows 16).
const merkleMaxSections = 8

// MerkleDomain is the catalog namespace of a Merkle section: nodes hashed with
// different native functions never conflict even at the same key width.
func MerkleDomain(tag byte, width int) string { return fmt.Sprintf("merkle/%d/w%d", tag, width) }

// Domain is the catalog key namespace for every kind in the kind table.
func Domain(rep *repz3.Rep, x repz3.Index) string {
	if rep.Kind == repz3.KindMerkle && len(rep.Recipe) > 0 {
		return MerkleDomain(rep.Recipe[0], x.KeyWidth)
	}
	return repz3.DefaultDomain(rep, x)
}

// NewCatalog returns an empty native accelerator over src using the kind table's domains.
func NewCatalog(src repz3.Source) *repz3.Catalog { return repz3.NewCatalog(src, Domain) }

// EncodeMerkle analyses a snapshot archive against the nodes the store (SHA-256
// native hash) or the catalog (any other native hash) already knows. cat may be
// nil: nothing is then reused.
func EncodeMerkle(snapshot []byte, h merklez3.HashFn, st *repz3.MemStore, cat *repz3.Catalog) (*Bundle, *MerkleStats, error) {
	tag, ok := hashTag(h)
	if !ok {
		return nil, nil, fmt.Errorf("kinds: unknown native hash %q", h.Name)
	}
	root, nodes, err := merklez3.ParseSnapshot(snapshot, h)
	if err != nil {
		return nil, nil, err
	}
	src := merklez3.NewMapStore()
	sums := map[merklez3.ID]repz3.SHA{}
	for _, n := range nodes {
		src.Put(n)
		sums[n.ID] = sha256.Sum256(n.Payload)
	}
	direct := h.Name == "sha256" && len(root) == 32
	dom := MerkleDomain(tag, len(root))
	known := func(id merklez3.ID) bool {
		switch {
		case direct:
			return st != nil && st.Has(repz3.SHA([]byte(id)))
		case cat == nil:
			return false
		}
		t, found, err := cat.Resolve(dom, []byte(id))
		return err == nil && found && t == sums[id]
	}
	ev := &collect{}
	stats, err := merklez3.Discover(src, root, known, ev, merklez3.VerifyWith(h))
	if err != nil {
		return nil, nil, err
	}
	rn, ok := src.M[root]
	if !ok {
		return nil, nil, fmt.Errorf("kinds: root not in the snapshot")
	}
	rsha := sums[root]
	out := &repz3.Rep{Kind: repz3.KindMerkle, Size: int64(len(snapshot)),
		Members: []repz3.Member{{Role: repz3.Stored, SHA: rsha, Size: int64(len(rn.Payload))}}}
	out.Recipe = binary.AppendUvarint([]byte{tag}, uint64(len(root)))
	out.Recipe = append(out.Recipe, root...)
	objs := [][]byte{rn.Payload}
	for _, n := range ev.fresh {
		if n.ID != root {
			objs = append(objs, n.Payload)
		}
	}
	ms := &MerkleStats{Nodes: len(nodes), NewNodes: len(ev.fresh), ReusedSubtrees: stats.Reused}
	if direct {
		b, err := newBundle(out, objs)
		return b, ms, err
	}
	if len(root) > 64 {
		return nil, nil, fmt.Errorf("kinds: native identity of %d bytes", len(root))
	}
	section := func(ids func(yield func(merklez3.ID))) (repz3.Index, []byte, error) {
		pairs := map[string]repz3.SHA{}
		ids(func(id merklez3.ID) { pairs[string(id)] = sums[id] })
		recs, n, err := repz3.BuildIndex(len(root), pairs)
		if err != nil {
			return repz3.Index{}, nil, err
		}
		return repz3.Index{KeyWidth: len(root), Count: n, Ref: sha256.Sum256(recs), Owns: true}, recs, nil
	}
	chain := map[repz3.SHA]repz3.Index{}
	var newSecs [][]byte
	if len(ev.fresh) > 0 {
		x, recs, err := section(func(y func(merklez3.ID)) {
			for _, n := range ev.fresh {
				y(n.ID)
			}
		})
		if err != nil {
			return nil, nil, err
		}
		chain[x.Ref] = x
		newSecs = append(newSecs, recs)
		ms.IndexRecords = int(x.Count)
	}
	for _, id := range ev.reused {
		os := cat.Owners(dom, []byte(id))
		var best []repz3.Index
		var bestRep repz3.SHA
		for _, o := range os {
			if o.Target != sums[id] {
				continue
			}
			for _, r := range o.Reps {
				s := cat.Sections(r)
				if best == nil || len(s) < len(best) || (len(s) == len(best) && bytes.Compare(r[:], bestRep[:]) < 0) {
					best, bestRep = s, r
				}
			}
		}
		if best == nil {
			return nil, nil, fmt.Errorf("kinds: reused node %x has no owning representation", string(id[:min(len(id), 6)]))
		}
		for _, x := range best {
			chain[x.Ref] = x
		}
	}
	if len(chain) > merkleMaxSections {
		// Leveled compaction. The largest inherited section is the base and stays;
		// every node it does not cover (the small sections' live nodes and this
		// revision's own) is rewritten into one delta section, which also drops
		// the replaced nodes those small sections held. When the base covers less
		// than half of its records of the current tree, everything is rewritten
		// into one full section (major compaction).
		var base repz3.Index
		for _, x := range chain {
			if x.Count > base.Count || (x.Count == base.Count && bytes.Compare(x.Ref[:], base.Ref[:]) < 0) {
				base = x
			}
		}
		covered := map[merklez3.ID]bool{}
		if cat != nil {
			for _, n := range nodes {
				if cat.InSection(dom, []byte(n.ID), base.Ref) {
					covered[n.ID] = true
				}
			}
		}
		if int64(len(covered))*2 < base.Count {
			x, recs, err := section(func(y func(merklez3.ID)) {
				for _, n := range nodes {
					y(n.ID)
				}
			})
			if err != nil {
				return nil, nil, err
			}
			chain = map[repz3.SHA]repz3.Index{x.Ref: x}
			newSecs = [][]byte{recs}
			ms.IndexRecords, ms.Compacted, ms.Major = int(x.Count), true, true
		} else {
			x, recs, err := section(func(y func(merklez3.ID)) {
				for _, n := range nodes {
					if !covered[n.ID] {
						y(n.ID)
					}
				}
			})
			if err != nil {
				return nil, nil, err
			}
			chain = map[repz3.SHA]repz3.Index{base.Ref: base}
			newSecs = nil
			if x.Count > 0 {
				chain[x.Ref] = x
				newSecs = [][]byte{recs}
			}
			ms.IndexRecords, ms.Compacted = int(x.Count), true
		}
	}
	for _, x := range chain {
		out.Indexes = append(out.Indexes, x)
	}
	sort.Slice(out.Indexes, func(i, j int) bool { return bytes.Compare(out.Indexes[i].Ref[:], out.Indexes[j].Ref[:]) < 0 })
	ms.Sections = len(out.Indexes)
	objs = append(objs, newSecs...)
	b, err := newBundle(out, objs)
	return b, ms, err
}

type collect struct {
	fresh  []merklez3.Node
	reused []merklez3.ID
}

func (c *collect) New(n merklez3.Node, _ int) error { c.fresh = append(c.fresh, n); return nil }
func (c *collect) Reuse(id merklez3.ID, _ int)      { c.reused = append(c.reused, id) }

type merkleDecoder struct{}

type mstore struct {
	c       *repz3.Call
	h       merklez3.HashFn
	root    merklez3.ID
	rootSHA repz3.SHA
	nodes   map[merklez3.ID]repz3.SHA // every section record of the representation
	sha     map[merklez3.ID]repz3.SHA
}

func newMStore(c *repz3.Call) (*mstore, error) {
	r := c.Rep.Recipe
	h, ok := merkleHashes[0]
	if len(r) < 2 {
		return nil, fmt.Errorf("%w: Merkle recipe", repz3.ErrCorrupt)
	}
	if h, ok = merkleHashes[r[0]]; !ok {
		return nil, fmt.Errorf("%w: Merkle native hash tag %d", repz3.ErrUnsupported, r[0])
	}
	l, n := binary.Uvarint(r[1:])
	if n <= 0 || l > 64 || uint64(len(r)-1-n) != l || len(c.Rep.Members) < 1 || c.Rep.Members[0].Role != repz3.Stored {
		return nil, fmt.Errorf("%w: Merkle recipe", repz3.ErrCorrupt)
	}
	s := &mstore{c: c, h: h, root: merklez3.ID(r[1+n:]), rootSHA: c.Rep.Members[0].SHA, sha: map[merklez3.ID]repz3.SHA{}, nodes: map[merklez3.ID]repz3.SHA{}}
	// the representation's own sections are the whole native map: no global state
	for _, x := range c.Rep.Indexes {
		if x.KeyWidth != int(l) {
			return nil, fmt.Errorf("%w: Merkle section key width %d, root identity %d", repz3.ErrCorrupt, x.KeyWidth, l)
		}
		err := repz3.ScanIndex(c.Source(), x, func(k []byte, t repz3.SHA) error {
			id := merklez3.ID(k)
			if prev, dup := s.nodes[id]; dup && prev != t {
				return fmt.Errorf("%w: node %x", repz3.ErrAmbiguous, k[:min(len(k), 6)])
			}
			s.nodes[id] = t
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *mstore) Get(id merklez3.ID) (merklez3.Node, error) {
	var sum repz3.SHA
	if t, ok := s.nodes[id]; ok {
		sum = t
	} else if id == s.root {
		sum = s.rootSHA
	} else if s.h.Name == "sha256" && len(id) == 32 { // the native identity is the component identity
		copy(sum[:], id)
	} else {
		return merklez3.Node{}, fmt.Errorf("%w: native node %x", repz3.ErrMissing, string(id[:min(len(id), 6)]))
	}
	p, err := s.c.Get(sum)
	if err != nil {
		return merklez3.Node{}, err
	}
	if string(s.h.Sum(p)) != string(id) {
		return merklez3.Node{}, fmt.Errorf("%w: node %x does not hash to its native identity", repz3.ErrMismatch, string(id[:min(len(id), 6)]))
	}
	kids, err := merklez3.DecodeKids(p)
	if err != nil {
		return merklez3.Node{}, fmt.Errorf("%w: %v", repz3.ErrCorrupt, err)
	}
	s.sha[id] = sum
	return merklez3.Node{ID: id, Payload: p, Kids: kids}, nil
}

func (merkleDecoder) Decode(c *repz3.Call) error {
	s, err := newMStore(c)
	if err != nil {
		return err
	}
	_, err = merklez3.Snapshot(s, s.root, c.W)
	return err
}

func (merkleDecoder) Expand(c *repz3.Call, emit func(repz3.SHA)) error {
	s, err := newMStore(c)
	if err != nil {
		return err
	}
	_, err = merklez3.Walk(s, s.root, func(n merklez3.Node, _ int) error { emit(s.sha[n.ID]); return nil })
	return err
}
