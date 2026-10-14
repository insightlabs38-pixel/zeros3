package merklez3

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha3"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"sort"
)

// HashFn is an adapter's native hash. The engine never calls it; only the
// builder and Verify do.
type HashFn struct {
	Name string
	Sum  func([]byte) []byte
}

var (
	HashSHA3_256   = HashFn{"sha3-256", func(b []byte) []byte { s := sha3.Sum256(b); return s[:] }}
	HashSHA512_256 = HashFn{"sha512-256", func(b []byte) []byte { s := sha512.Sum512_256(b); return s[:] }}
	HashSHA256     = HashFn{"sha256", func(b []byte) []byte { s := sha256.Sum256(b); return s[:] }}
	// HashShort16 is a deliberately different shape: 16-byte truncated SHA-256.
	HashShort16 = HashFn{"sha256/16", func(b []byte) []byte { s := sha256.Sum256(b); return s[:16] }}
)

// Config selects the synthetic trie's layout and native hash. LeafMax is the
// most entries a leaf holds; Layout toggles an encoding variant that changes
// every node's bytes without changing the state (a layout-only rewrite).
type Config struct {
	Hash    HashFn
	LeafMax int
	Layout  byte
}

func DefaultConfig() Config { return Config{Hash: HashSHA3_256, LeafMax: 8} }

// State is a deterministic key/value set: keys are 64-bit, a key's value is a
// pure function of (key, version). Ver holds the keys whose version is not 0.
type State struct {
	Keys []uint64 // sorted, unique
	Ver  map[uint64]uint32
}

func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// NewState builds n keys (key i = mix(seed+i)) at version 0.
func NewState(seed uint64, n int) *State {
	ks := make([]uint64, n)
	for i := range ks {
		ks[i] = mix(seed + uint64(i))
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	out := ks[:0]
	for i, k := range ks {
		if i == 0 || k != ks[i-1] {
			out = append(out, k)
		}
	}
	return &State{Keys: out, Ver: map[uint64]uint32{}}
}

func (s *State) Clone() *State {
	c := &State{Keys: append([]uint64(nil), s.Keys...), Ver: make(map[uint64]uint32, len(s.Ver))}
	for k, v := range s.Ver {
		c.Ver[k] = v
	}
	return c
}

// Value is the deterministic value of key at its current version (24-88 bytes).
func (s *State) Value(key uint64) []byte {
	v := uint64(s.Ver[key])
	h := mix(key ^ mix(v+0x51))
	n := 24 + int(h%65)
	b := make([]byte, n)
	for i := 0; i < n; i += 8 {
		var w [8]byte
		binary.LittleEndian.PutUint64(w[:], mix(h+uint64(i)))
		copy(b[i:], w[:])
	}
	return b
}

func (s *State) index(key uint64) (int, bool) {
	i := sort.Search(len(s.Keys), func(i int) bool { return s.Keys[i] >= key })
	return i, i < len(s.Keys) && s.Keys[i] == key
}

// Mutations. Each returns a new state and the keys it touched (ground truth).

func (s *State) Update(keys []uint64) *State {
	c := s.Clone()
	for _, k := range keys {
		c.Ver[k]++
	}
	return c
}

func (s *State) Insert(keys []uint64) *State {
	c := s.Clone()
	for _, k := range keys {
		if i, ok := c.index(k); !ok {
			c.Keys = append(c.Keys, 0)
			copy(c.Keys[i+1:], c.Keys[i:])
			c.Keys[i] = k
		}
	}
	return c
}

func (s *State) Remove(keys []uint64) *State {
	c := s.Clone()
	rm := map[uint64]bool{}
	for _, k := range keys {
		rm[k] = true
		delete(c.Ver, k)
	}
	out := c.Keys[:0]
	for _, k := range c.Keys {
		if !rm[k] {
			out = append(out, k)
		}
	}
	c.Keys = out
	return c
}

// PickScattered returns n existing keys spread across the key space.
func (s *State) PickScattered(seed uint64, n int) []uint64 {
	var out []uint64
	seen := map[int]bool{}
	for i := 0; len(out) < n && i < n*8; i++ {
		j := int(mix(seed+uint64(i)) % uint64(len(s.Keys)))
		if !seen[j] {
			seen[j] = true
			out = append(out, s.Keys[j])
		}
	}
	return out
}

// PickClustered returns n consecutive existing keys starting near a seeded spot.
func (s *State) PickClustered(seed uint64, n int) []uint64 {
	j := int(mix(seed) % uint64(max(1, len(s.Keys)-n)))
	return append([]uint64(nil), s.Keys[j:min(j+n, len(s.Keys))]...)
}

// KeysUnder returns the keys whose top nibbles equal prefix (nibs high nibbles).
func (s *State) KeysUnder(prefix uint64, nibs int) []uint64 {
	lo := prefix << (64 - 4*uint(nibs))
	hi := lo | (^uint64(0) >> (4 * uint(nibs)))
	i := sort.Search(len(s.Keys), func(i int) bool { return s.Keys[i] >= lo })
	j := sort.Search(len(s.Keys), func(i int) bool { return s.Keys[i] > hi })
	return append([]uint64(nil), s.Keys[i:j]...)
}

// FreshKeys returns n keys not present in s.
func (s *State) FreshKeys(seed uint64, n int) []uint64 {
	var out []uint64
	for i := uint64(0); len(out) < n; i++ {
		k := mix(seed ^ 0xabcdef0000 + i)
		if _, ok := s.index(k); !ok {
			out = append(out, k)
		}
	}
	return out
}

// ---- encoding ---------------------------------------------------------------

// Node payloads:
//
//	leaf   0x01 | layout | uvarint(n) | n x ( key u64 BE | uvarint(vlen) | value )
//	branch 0x02 | layout | u16 bitmap | per present slot: uvarint(len) | native id
//
// Layout byte is part of the exact bytes; changing it rewrites every node.

func appendLeaf(b []byte, s *State, keys []uint64, layout byte) []byte {
	b = append(b, 0x01, layout)
	b = binary.AppendUvarint(b, uint64(len(keys)))
	for _, k := range keys {
		b = binary.BigEndian.AppendUint64(b, k)
		v := s.Value(k)
		b = binary.AppendUvarint(b, uint64(len(v)))
		b = append(b, v...)
	}
	return b
}

func appendBranch(b []byte, layout byte, kids []ID) []byte {
	var bm uint16
	for i, k := range kids {
		if k != "" {
			bm |= 1 << uint(i)
		}
	}
	b = append(b, 0x02, layout)
	b = binary.BigEndian.AppendUint16(b, bm)
	for _, k := range kids {
		if k != "" {
			b = binary.AppendUvarint(b, uint64(len(k)))
			b = append(b, k...)
		}
	}
	return b
}

// DecodeKids extracts the positional child identities (length 16 for a branch,
// nil for a leaf) from a synthetic node payload.
func DecodeKids(p []byte) ([]ID, error) {
	if len(p) < 2 {
		return nil, fmt.Errorf("merklez3: short node")
	}
	switch p[0] {
	case 0x01:
		return nil, nil
	case 0x02:
		if len(p) < 4 {
			return nil, fmt.Errorf("merklez3: short branch")
		}
		bm := binary.BigEndian.Uint16(p[2:])
		q := 4
		kids := make([]ID, 16)
		for i := 0; i < 16; i++ {
			if bm&(1<<uint(i)) == 0 {
				continue
			}
			l, n := binary.Uvarint(p[q:])
			if n <= 0 || l > 64 || uint64(len(p)-q-n) < l {
				return nil, fmt.Errorf("merklez3: bad child id")
			}
			kids[i] = ID(p[q+n : q+n+int(l)])
			q += n + int(l)
		}
		if q != len(p) {
			return nil, fmt.Errorf("merklez3: trailing bytes in branch")
		}
		return kids, nil
	}
	return nil, fmt.Errorf("merklez3: unknown node kind %d", p[0])
}

// VerifyWith returns a Verify that recomputes the native hash of every node and
// checks the structural shape.
func VerifyWith(h HashFn) Verify {
	return func(n Node) error {
		if string(h.Sum(n.Payload)) != string(n.ID) {
			return fmt.Errorf("merklez3: node %x: payload does not hash to its native id", string(n.ID[:min(len(n.ID), 6)]))
		}
		k, err := DecodeKids(n.Payload)
		if err != nil {
			return err
		}
		if len(k) != len(n.Kids) {
			return fmt.Errorf("merklez3: node %x: child list disagrees with payload", string(n.ID[:min(len(n.ID), 6)]))
		}
		for i := range k {
			if k[i] != n.Kids[i] {
				return fmt.Errorf("merklez3: node %x: child %d disagrees with payload", string(n.ID[:min(len(n.ID), 6)]), i)
			}
		}
		return nil
	}
}

// BuildStats describes one build.
type BuildStats struct {
	Nodes, Leaves, Branches int
	Bytes                   int64
	Depth                   int
}

// Build constructs the canonical trie of the state and emits every node
// children-first. The shape is a function of the key set alone: 16-ary on key
// nibbles, a leaf as soon as at most LeafMax keys share the prefix.
func (s *State) Build(cfg Config, emit func(Node) error) (ID, BuildStats, error) {
	var st BuildStats
	var rec func(keys []uint64, depth int) (ID, error)
	rec = func(keys []uint64, depth int) (ID, error) {
		st.Depth = max(st.Depth, depth)
		if len(keys) == 0 {
			return "", nil
		}
		if len(keys) <= cfg.LeafMax || depth == 16 {
			p := appendLeaf(nil, s, keys, cfg.Layout)
			n := Node{ID: ID(cfg.Hash.Sum(p)), Payload: p}
			st.Nodes++
			st.Leaves++
			st.Bytes += int64(len(p))
			return n.ID, emit(n)
		}
		kids := make([]ID, 16)
		shift := 60 - 4*uint(depth)
		lo := 0
		for nib := 0; nib < 16; nib++ {
			hi := lo
			for hi < len(keys) && int((keys[hi]>>shift)&15) == nib {
				hi++
			}
			id, err := rec(keys[lo:hi], depth+1)
			if err != nil {
				return "", err
			}
			kids[nib] = id
			lo = hi
		}
		p := appendBranch(nil, cfg.Layout, kids)
		n := Node{ID: ID(cfg.Hash.Sum(p)), Payload: p, Kids: kids}
		st.Nodes++
		st.Branches++
		st.Bytes += int64(len(p))
		return n.ID, emit(n)
	}
	root, err := rec(s.Keys, 0)
	return root, st, err
}

// BuildInto builds the state into a MapStore and returns the root.
func (s *State) BuildInto(cfg Config, ms *MapStore) (ID, BuildStats) {
	root, st, _ := s.Build(cfg, func(n Node) error { ms.Put(n); return nil })
	return root, st
}

// MergeRoots makes a new branch whose slots [0,split) come from a and
// [split,16) from b (both roots must be branches): a merge-like DAG revision
// that references subtrees of two lines of history. The new root is emitted
// into ms; no other node is touched.
func MergeRoots(cfg Config, ms *MapStore, a, b ID, split int) (ID, error) {
	an, err := ms.Get(a)
	if err != nil {
		return "", err
	}
	bn, err := ms.Get(b)
	if err != nil {
		return "", err
	}
	if len(an.Kids) != 16 || len(bn.Kids) != 16 {
		return "", fmt.Errorf("merklez3: merge needs branch roots")
	}
	kids := make([]ID, 16)
	for i := range kids {
		if i < split {
			kids[i] = an.Kids[i]
		} else {
			kids[i] = bn.Kids[i]
		}
	}
	p := appendBranch(nil, cfg.Layout, kids)
	n := Node{ID: ID(cfg.Hash.Sum(p)), Payload: p, Kids: kids}
	ms.Put(n)
	return n.ID, nil
}

// ShareRoot makes a branch whose slot 0..n-1 all point at the same child: a
// DAG with a shared subtree (one stored node reachable by many paths).
func ShareRoot(cfg Config, ms *MapStore, child ID, n int) ID {
	kids := make([]ID, 16)
	for i := 0; i < n && i < 16; i++ {
		kids[i] = child
	}
	p := appendBranch(nil, cfg.Layout, kids)
	nd := Node{ID: ID(cfg.Hash.Sum(p)), Payload: p, Kids: kids}
	ms.Put(nd)
	return nd.ID
}

// ---- snapshot serialisation --------------------------------------------------

// Snapshot serialises the graph from root as an exact archive: magic, the
// root identity, then every reachable node once in depth-first pre-order as
// uvarint(len) || payload. This is the "outer exact format" of the synthetic
// state-snapshot workload.
func Snapshot(s Store, root ID, w interface{ Write([]byte) (int, error) }) (int64, error) {
	var n int64
	hdr := append([]byte("Z3SNAP1\x00"), binary.AppendUvarint(nil, uint64(len(root)))...)
	hdr = append(hdr, root...)
	if _, err := w.Write(hdr); err != nil {
		return 0, err
	}
	n += int64(len(hdr))
	_, err := Walk(s, root, func(nd Node, _ int) error {
		rec := binary.AppendUvarint(nil, uint64(len(nd.Payload)))
		rec = append(rec, nd.Payload...)
		m, err := w.Write(rec)
		n += int64(m)
		return err
	})
	return n, err
}

// ParseSnapshot reads a Snapshot back into nodes, verifying nothing: callers
// decide how much of the native hash to trust.
func ParseSnapshot(b []byte, h HashFn) (root ID, nodes []Node, err error) {
	if !bytes.HasPrefix(b, []byte("Z3SNAP1\x00")) {
		return "", nil, fmt.Errorf("merklez3: not a snapshot")
	}
	p := 8
	l, n := binary.Uvarint(b[p:])
	if n <= 0 || uint64(len(b)-p-n) < l {
		return "", nil, fmt.Errorf("merklez3: bad root")
	}
	root = ID(b[p+n : p+n+int(l)])
	p += n + int(l)
	for p < len(b) {
		l, n := binary.Uvarint(b[p:])
		if n <= 0 || uint64(len(b)-p-n) < l {
			return "", nil, fmt.Errorf("merklez3: truncated record")
		}
		pl := b[p+n : p+n+int(l)]
		kids, err := DecodeKids(pl)
		if err != nil {
			return "", nil, err
		}
		nodes = append(nodes, Node{ID: ID(h.Sum(pl)), Payload: pl, Kids: kids})
		p += n + int(l)
	}
	return root, nodes, nil
}
