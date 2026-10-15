package kinds

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

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
// and the native index section maps native node identities to the components of
// the nodes this revision adds. Reused subtrees resolve through the source's
// global native index: reconstruction needs it (repz3.NativeResolver), which is
// the one piece of store wiring this kind requires. A native hash is only a key:
// the decoder recomputes it over every fetched node.

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
}

// EncodeMerkle analyses a snapshot archive against the nodes the store's native
// index already knows.
func EncodeMerkle(snapshot []byte, h merklez3.HashFn, st *repz3.MemStore) (*Bundle, *MerkleStats, error) {
	tag, ok := hashTag(h)
	if !ok {
		return nil, nil, fmt.Errorf("kinds: unknown native hash %q", h.Name)
	}
	root, nodes, err := merklez3.ParseSnapshot(snapshot, h)
	if err != nil {
		return nil, nil, err
	}
	src := merklez3.NewMapStore()
	for _, n := range nodes {
		src.Put(n)
	}
	var fresh []merklez3.Node
	stats, err := merklez3.Discover(src, root, func(id merklez3.ID) bool { _, ok := st.Resolve([]byte(id)); return ok },
		collect{&fresh}, merklez3.VerifyWith(h))
	if err != nil {
		return nil, nil, err
	}
	rn, ok := src.M[root]
	if !ok {
		return nil, nil, fmt.Errorf("kinds: root not in the snapshot")
	}
	rsha := sha256.Sum256(rn.Payload)
	out := &repz3.Rep{Kind: repz3.KindMerkle, Size: int64(len(snapshot)),
		Members: []repz3.Member{{Role: repz3.Stored, SHA: rsha, Size: int64(len(rn.Payload))}}}
	out.Recipe = binary.AppendUvarint([]byte{tag}, uint64(len(root)))
	out.Recipe = append(out.Recipe, root...)
	objs := [][]byte{rn.Payload}
	pairs := map[string]repz3.SHA{}
	for _, n := range fresh {
		if n.ID != root {
			objs = append(objs, n.Payload)
		}
		// a native identity equal to the node's SHA-256 resolves without a record
		if sum := sha256.Sum256(n.Payload); string(sum[:]) != string(n.ID) {
			pairs[string(n.ID)] = sum
		}
	}
	ms := &MerkleStats{Nodes: len(nodes), NewNodes: len(fresh), ReusedSubtrees: stats.Reused}
	if len(pairs) > 0 {
		if len(root) > 64 {
			return nil, nil, fmt.Errorf("kinds: native identity of %d bytes", len(root))
		}
		recs, n, err := repz3.BuildIndex(len(root), pairs)
		if err != nil {
			return nil, nil, err
		}
		objs = append(objs, recs)
		out.Indexes = []repz3.Index{{Type: repz3.IndexNative, KeyWidth: len(root), Count: n, Ref: sha256.Sum256(recs)}}
		ms.IndexRecords = int(n)
	}
	b, err := newBundle(out, objs)
	return b, ms, err
}

type collect struct{ nodes *[]merklez3.Node }

func (c collect) New(n merklez3.Node, _ int) error { *c.nodes = append(*c.nodes, n); return nil }
func (collect) Reuse(merklez3.ID, int)             {}

type merkleDecoder struct{}

type mstore struct {
	c       *repz3.Call
	h       merklez3.HashFn
	root    merklez3.ID
	rootSHA repz3.SHA
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
	return &mstore{c: c, h: h, root: merklez3.ID(r[1+n:]), rootSHA: c.Rep.Members[0].SHA, sha: map[merklez3.ID]repz3.SHA{}}, nil
}

func (s *mstore) Get(id merklez3.ID) (merklez3.Node, error) {
	var sum repz3.SHA
	switch {
	case id == s.root:
		sum = s.rootSHA
	default:
		var found bool
		var err error
		if sum, found, err = s.c.Resolve([]byte(id)); err != nil {
			return merklez3.Node{}, err
		}
		if !found && s.h.Name == "sha256" && len(id) == 32 { // the native identity is the component identity
			copy(sum[:], id)
			found = true
		}
		if !found {
			return merklez3.Node{}, fmt.Errorf("%w: native node %x", repz3.ErrMissing, string(id[:min(len(id), 6)]))
		}
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
