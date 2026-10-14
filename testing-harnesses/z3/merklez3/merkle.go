// Package merklez3 is the Z3-15 laboratory for hash-guided revision discovery.
// Nodes carry an opaque native identity (a hash the engine never computes or
// interprets) and an ordered list of child identities. Equal identity proves
// an entire subtree unchanged, so a diff can skip it without reading it.
//
// Nothing here knows Keccak, SHA-256, Blake or CID: hashing belongs to the
// adapter that builds or verifies nodes. ZeroS3's own SHA-256 of the exact
// node bytes is recorded separately by the callers that account storage.
package merklez3

import (
	"errors"
	"fmt"
)

// ID is an opaque native identity (the adapter's hash bytes).
type ID string

// Node is one component: its native identity, exact bytes and positional
// children ("" marks an empty slot; a leaf has no children).
type Node struct {
	ID      ID
	Payload []byte
	Kids    []ID
}

// Store serves nodes by native identity. Implementations may be in memory or
// disk backed; the algorithms never assume the whole graph is resident.
type Store interface {
	Get(id ID) (Node, error)
}

// ErrMissing is returned by a Store for an unknown identity.
var ErrMissing = errors.New("merklez3: node not in store")

// MapStore is a resident store with read accounting.
type MapStore struct {
	M map[ID]Node
}

func NewMapStore() *MapStore { return &MapStore{M: map[ID]Node{}} }

func (s *MapStore) Put(n Node) { s.M[n.ID] = n }

func (s *MapStore) Get(id ID) (Node, error) {
	n, ok := s.M[id]
	if !ok {
		return Node{}, fmt.Errorf("%w: %x", ErrMissing, string(id[:min(len(id), 6)]))
	}
	return n, nil
}

// Stats counts the work and the outcome of one traversal.
type Stats struct {
	NodesRead    int   // nodes whose bytes were fetched and decoded
	BytesRead    int64 // payload bytes of those nodes
	HashCompares int   // identity comparisons / membership tests
	Descended    int   // nodes whose children were examined
	Reused       int   // subtrees skipped because their identity was equal / known
	NewNodes     int
	NewBytes     int64
	MaxDepth     int
	MaxFrontier  int // peak explicit stack size (working memory of the traversal)
}

// Event receives the outcome of a traversal.
type Event interface {
	// New is called for every node that is not known to the base; its
	// children are examined next.
	New(n Node, depth int) error
	// Reuse is called for a subtree root proven identical to a known one.
	Reuse(id ID, depth int)
}

// Verify, when set on a traversal, checks a node's payload against its native
// identity using the adapter's hash. A mismatch aborts the traversal: a native
// hash is a hint, and a node that disagrees with it is not trusted.
type Verify func(n Node) error

// Discover walks the DAG from root and reports every node for which have(id)
// is false. A known identity prunes its whole subtree: this is the
// closure invariant of a content-addressed store (a node is only stored with
// all its descendants). Work is proportional to the new nodes plus their child
// edges, not to the size of the graph. have is the only access to existing
// content; it is a single membership test.
func Discover(s Store, root ID, have func(ID) bool, ev Event, ver Verify) (Stats, error) {
	var st Stats
	type frame struct {
		id    ID
		depth int
	}
	stack := []frame{{root, 0}}
	seen := map[ID]bool{} // new nodes already emitted (DAGs share)
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		st.HashCompares++
		if f.id == "" {
			continue
		}
		if have(f.id) || seen[f.id] {
			st.Reused++
			if ev != nil {
				ev.Reuse(f.id, f.depth)
			}
			continue
		}
		n, err := s.Get(f.id)
		if err != nil {
			return st, err
		}
		st.NodesRead++
		st.BytesRead += int64(len(n.Payload))
		if ver != nil {
			if err := ver(n); err != nil {
				return st, err
			}
		}
		seen[f.id] = true
		st.NewNodes++
		st.NewBytes += int64(len(n.Payload))
		st.Descended++
		st.MaxDepth = max(st.MaxDepth, f.depth)
		if ev != nil {
			if err := ev.New(n, f.depth); err != nil {
				return st, err
			}
		}
		for i := len(n.Kids) - 1; i >= 0; i-- {
			stack = append(stack, frame{n.Kids[i], f.depth + 1})
		}
		st.MaxFrontier = max(st.MaxFrontier, len(stack))
	}
	return st, nil
}

// DiffAligned compares two revisions that share a positional layout (slot i of
// a node means the same thing in both): equal child identities at a slot prove
// that subtree unchanged; unequal ones are read from both sides and compared
// recursively. This is the classic Merkle-tree diff. Nodes only the target has
// are reported New; their whole subtree is new relative to this base path.
func DiffAligned(base, target Store, bRoot, tRoot ID, ev Event, ver Verify) (Stats, error) {
	var st Stats
	type pair struct {
		b, t  ID
		depth int
	}
	stack := []pair{{bRoot, tRoot, 0}}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		st.HashCompares++
		if p.t == "" {
			continue
		}
		if p.b == p.t {
			st.Reused++
			if ev != nil {
				ev.Reuse(p.t, p.depth)
			}
			continue
		}
		tn, err := target.Get(p.t)
		if err != nil {
			return st, err
		}
		st.NodesRead++
		st.BytesRead += int64(len(tn.Payload))
		if ver != nil {
			if err := ver(tn); err != nil {
				return st, err
			}
		}
		st.NewNodes++
		st.NewBytes += int64(len(tn.Payload))
		st.Descended++
		st.MaxDepth = max(st.MaxDepth, p.depth)
		if ev != nil {
			if err := ev.New(tn, p.depth); err != nil {
				return st, err
			}
		}
		var bkids []ID
		if p.b != "" {
			bn, err := base.Get(p.b)
			if err != nil {
				return st, err
			}
			st.NodesRead++
			st.BytesRead += int64(len(bn.Payload))
			bkids = bn.Kids
		}
		for i := len(tn.Kids) - 1; i >= 0; i-- {
			var b ID
			if i < len(bkids) {
				b = bkids[i]
			}
			stack = append(stack, pair{b, tn.Kids[i], p.depth + 1})
		}
		st.MaxFrontier = max(st.MaxFrontier, len(stack))
	}
	return st, nil
}

// Walk visits every node reachable from root exactly once (a DAG's shared
// subtrees once), reading each. It is the "full graph scan" baseline and is
// how a component-by-component comparison has to enumerate a snapshot.
func Walk(s Store, root ID, fn func(Node, int) error) (Stats, error) {
	var st Stats
	type frame struct {
		id    ID
		depth int
	}
	stack := []frame{{root, 0}}
	seen := map[ID]bool{}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if f.id == "" || seen[f.id] {
			continue
		}
		seen[f.id] = true
		n, err := s.Get(f.id)
		if err != nil {
			return st, err
		}
		st.NodesRead++
		st.BytesRead += int64(len(n.Payload))
		st.Descended++
		st.MaxDepth = max(st.MaxDepth, f.depth)
		if fn != nil {
			if err := fn(n, f.depth); err != nil {
				return st, err
			}
		}
		for i := len(n.Kids) - 1; i >= 0; i-- {
			stack = append(stack, frame{n.Kids[i], f.depth + 1})
		}
		st.MaxFrontier = max(st.MaxFrontier, len(stack))
	}
	return st, nil
}
