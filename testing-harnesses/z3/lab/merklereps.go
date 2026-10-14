package lab

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"zeros3-z3/merklez3"
	"zeros3-z3/srev"
)

// Z3-15: representations of a synthetic Merkle state snapshot. The input is
// the exact snapshot archive (merklez3.Snapshot); every representation must
// rebuild those bytes.
//
//	merkle-flat    every node a component, listed in DFS order in the recipe
//	               (the generic component CAS: needs every node read and hashed)
//	merkle-guided  hash-guided discovery against the nodes already stored; new
//	               nodes hang off the root (Via), the recipe is the root only,
//	               plus a native -> SHA-256 index for the nodes it adds
//
// raw / cdc / fixed come from Reps(). The "source" of a guided rep is a
// resident map built from the archive: the lab counts the work the algorithm
// would do against a random-access source, it does not claim that reading a
// flat archive file is avoided. cmd/z3merkle scale uses a disk-backed source
// for the real random-access measurement.

// MerkleReps returns baselines and the two Merkle representations, with fresh
// state (the native index) for one family.
func MerkleReps(h merklez3.HashFn) []Rep {
	var out []Rep
	for _, r := range Reps() {
		switch r.Name {
		case "raw", "cdc", "fixed-64k":
			out = append(out, r)
		}
	}
	// each representation owns its native index: they store different components
	flat := &merkleState{native: map[merklez3.ID][32]byte{}}
	guided := &merkleState{native: map[merklez3.ID][32]byte{}}
	auto := &merkleState{native: map[merklez3.ID][32]byte{}}
	out = append(out,
		Rep{"merkle-flat", func(in *Input) (*Plan, error) { return flat.flat(in, h) }},
		Rep{"merkle-guided", func(in *Input) (*Plan, error) { return guided.guided(in, h, 1.1) }},
		Rep{"merkle-auto", func(in *Input) (*Plan, error) { return auto.auto(in, h) }},
	)
	return out
}

type merkleState struct {
	native map[merklez3.ID][32]byte // native identity -> SHA-256 of the stored node
}

func parseSnap(in *Input, h merklez3.HashFn) (merklez3.ID, []merklez3.Node, []byte, error) {
	b, err := in.Src.Span(0, in.Src.Size)
	if err != nil {
		return "", nil, nil, err
	}
	root, nodes, err := merklez3.ParseSnapshot(b, h)
	if err != nil {
		return "", nil, nil, ErrNotApplicable
	}
	return root, nodes, b, nil
}

func snapHeader(root merklez3.ID) []byte {
	return append(append([]byte("Z3SNAP1\x00"), binary.AppendUvarint(nil, uint64(len(root)))...), root...)
}

func nodeComp(n merklez3.Node, class string) srev.Component {
	p := n.Payload
	return srev.Component{Native: []byte(n.ID), SHA: sha256.Sum256(p), Size: int64(len(p)), Data: func() ([]byte, error) { return p, nil }, Class: class}
}

func (st *merkleState) flat(in *Input, h merklez3.HashFn) (*Plan, error) {
	root, nodes, _, err := parseSnap(in, h)
	if err != nil {
		return nil, err
	}
	hdr := snapHeader(root)
	rev := &srev.Revision{Roots: [][]byte{[]byte(root)}, Residual: int64(len(hdr))}
	var total int64
	for _, n := range nodes {
		c := nodeComp(n, "node")
		rev.Members = append(rev.Members, srev.Member{C: c})
		total += c.Size
	}
	rev.Rebuild = func(w io.Writer, get func([32]byte) ([]byte, error)) error {
		if _, err := w.Write(hdr); err != nil {
			return err
		}
		for _, m := range rev.Members {
			b, err := get(m.C.SHA)
			if err != nil {
				return err
			}
			if _, err := w.Write(append(binary.AppendUvarint(nil, uint64(len(b))), b...)); err != nil {
				return err
			}
		}
		return nil
	}
	rev.Notes = map[string]any{"scan_nodes": len(nodes), "scan_bytes": total, "total_nodes": len(nodes), "total_bytes": total}
	return PlanFromRevision(rev), nil
}

type snapSource struct {
	m map[merklez3.ID]merklez3.Node
}

func (s snapSource) Get(id merklez3.ID) (merklez3.Node, error) {
	n, ok := s.m[id]
	if !ok {
		return merklez3.Node{}, merklez3.ErrMissing
	}
	return n, nil
}

type collectNew struct{ nodes []merklez3.Node }

func (c *collectNew) New(n merklez3.Node, _ int) error { c.nodes = append(c.nodes, n); return nil }
func (c *collectNew) Reuse(merklez3.ID, int)           {}

// MerkleAutoMaxNew is the new-node fraction at which merkle-auto stops using
// node components and stores the archive with production CDC instead.
var MerkleAutoMaxNew = 0.30

// errDense means the hash-guided path found too little to reuse.
var errDense = fmt.Errorf("merkle: too few reusable subtrees")

// auto is the admission rule: hash-guided components when verification passes
// and enough of the graph is already known, production CDC otherwise. A node
// whose payload disagrees with its native hash fails the transform; the
// archive is still stored, as bytes.
func (st *merkleState) auto(in *Input, h merklez3.HashFn) (*Plan, error) {
	p, err := st.guided(in, h, MerkleAutoMaxNew)
	if err == nil {
		p.Detail["auto"] = "merkle-guided"
		return p, nil
	}
	if err == ErrNotApplicable {
		return nil, err
	}
	cp, cerr := analyzeChunked(in, cdcSplit)
	if cerr != nil {
		return nil, cerr
	}
	cp.Detail = map[string]any{"auto": "cdc", "why": err.Error()}
	return cp, nil
}

func (st *merkleState) guided(in *Input, h merklez3.HashFn, maxNew float64) (*Plan, error) {
	root, nodes, _, err := parseSnap(in, h)
	if err != nil {
		return nil, err
	}
	src := snapSource{m: map[merklez3.ID]merklez3.Node{}}
	var total int64
	for _, n := range nodes {
		src.m[n.ID] = n
		total += int64(len(n.Payload))
	}
	col := &collectNew{}
	stats, err := merklez3.Discover(src, root, func(id merklez3.ID) bool { _, ok := st.native[id]; return ok }, col, merklez3.VerifyWith(h))
	if err != nil {
		return nil, err // a node disagreeing with its native hash fails the transform
	}
	if len(st.native) > 0 && len(nodes) > 0 && float64(len(col.nodes))/float64(len(nodes)) >= maxNew { // a first revision has no history to be judged against
		return nil, errDense
	}
	rn, ok := src.m[root]
	if !ok {
		return nil, fmt.Errorf("merkle: root %x not in snapshot", string(root))
	}
	hdr := snapHeader(root)
	rev := &srev.Revision{Roots: [][]byte{[]byte(root)}, Residual: int64(len(hdr))}
	rev.Members = append(rev.Members, srev.Member{C: nodeComp(rn, "root")})
	for _, n := range col.nodes {
		if n.ID != root {
			rev.Members = append(rev.Members, srev.Member{C: nodeComp(n, "node"), Via: true})
		}
	}
	for _, n := range col.nodes {
		sha := sha256.Sum256(n.Payload)
		st.native[n.ID] = sha
		if string(sha[:]) != string(n.ID) { // a native id equal to the SHA-256 needs no lookup entry
			rev.Index += int64(len(n.ID)) + 32
		}
	}
	st.native[root] = sha256.Sum256(rn.Payload)
	rev.Rebuild = func(w io.Writer, get func([32]byte) ([]byte, error)) error {
		_, err := merklez3.Snapshot(nativeStore{st, get, h}, root, w)
		return err
	}
	rev.Notes = map[string]any{"discover": stats, "total_nodes": len(nodes), "total_bytes": total, "new_nodes": len(col.nodes), "reused_subtrees": stats.Reused}
	return PlanFromRevision(rev), nil
}

// nativeStore serves nodes from the accounting store through the native index.
type nativeStore struct {
	st  *merkleState
	get func([32]byte) ([]byte, error)
	h   merklez3.HashFn
}

func (s nativeStore) Get(id merklez3.ID) (merklez3.Node, error) {
	sha, ok := s.st.native[id]
	if !ok {
		return merklez3.Node{}, merklez3.ErrMissing
	}
	p, err := s.get(sha)
	if err != nil {
		return merklez3.Node{}, err
	}
	kids, err := merklez3.DecodeKids(p)
	if err != nil {
		return merklez3.Node{}, err
	}
	return merklez3.Node{ID: id, Payload: p, Kids: kids}, nil
}
