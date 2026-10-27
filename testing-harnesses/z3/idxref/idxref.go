// Package idxref is the format-independent part of the Z3-32 indexed-reference representations
// (dexz3, jvmz3): semantic identities, the prefix coder that names a table entry by a few bytes of
// its identity (with a deterministic, table-implied disambiguation), component packaging of a masked
// payload with its reference streams, and the section/record description grouping cutters use.
// It knows nothing about DEX or class files and adds nothing to repz3: every format keeps its own
// slot semantics, and the representation is still just components plus a recipe. Stdlib only.
package idxref

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// ErrFormat reports a malformed reference stream or component.
var ErrFormat = errors.New("idxref: malformed representation")

func bad(f string, a ...any) error { return fmt.Errorf("%w: "+f, append([]any{ErrFormat}, a...)...) }

// Identity is the semantic identity of a table entry: SHA-256 of a tagged, canonical description
// built from the entry's own content (never from a numeric index).
type Identity = [32]byte

// Hash computes the identity of tag | parts...
func Hash(tag byte, parts ...[]byte) Identity {
	h := sha256.New()
	h.Write([]byte{tag})
	for _, p := range parts {
		h.Write(p)
	}
	var out Identity
	h.Sum(out[:0])
	return out
}

// CheckUnique rejects a table with two entries of one identity (ambiguous: nothing may be
// normalized against it).
func CheckUnique(what string, ids []Identity) error {
	seen := make(map[Identity]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			return fmt.Errorf("duplicate %s identity (ambiguous)", what)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// PrefixIndex codes a reference to table entry i as the first K0 bytes of its identity plus the
// fewest extra bytes that make the prefix unique among the entries sharing those K0 bytes. The
// decoder owns the same table, so the extra length is implied and no escape marker exists.
type PrefixIndex struct {
	k0    int
	ids   []Identity
	uniq  map[uint32]uint32
	multi map[uint32][]uint32
}

func key(id *Identity, k0 int) uint32 {
	var k uint32
	for i := 0; i < k0; i++ {
		k = k<<8 | uint32(id[i])
	}
	return k
}

// NewPrefixIndex builds the coder over ids (K0 in 1..4).
func NewPrefixIndex(ids []Identity, k0 int) *PrefixIndex {
	p := &PrefixIndex{k0: k0, ids: ids, uniq: make(map[uint32]uint32, len(ids)), multi: map[uint32][]uint32{}}
	for i := range ids {
		k := key(&ids[i], k0)
		if m, ok := p.multi[k]; ok {
			p.multi[k] = append(m, uint32(i))
		} else if j, ok := p.uniq[k]; ok {
			delete(p.uniq, k)
			p.multi[k] = []uint32{j, uint32(i)}
		} else {
			p.uniq[k] = uint32(i)
		}
	}
	return p
}

// Collisions is the number of entries whose K0 prefix is shared with another entry.
func (p *PrefixIndex) Collisions() int {
	n := 0
	for _, c := range p.multi {
		n += len(c)
	}
	return n
}

// Len is the table size.
func (p *PrefixIndex) Len() int { return len(p.ids) }

// Put appends the reference to entry i.
func (p *PrefixIndex) Put(dst []byte, i uint32) ([]byte, error) {
	if uint64(i) >= uint64(len(p.ids)) {
		return dst, bad("reference %d outside table of %d", i, len(p.ids))
	}
	id := &p.ids[i]
	k := key(id, p.k0)
	dst = append(dst, id[:p.k0]...)
	cands, ok := p.multi[k]
	if !ok {
		return dst, nil
	}
	for e := p.k0; e < 32; e++ {
		dst = append(dst, id[e])
		n := 0
		for _, c := range cands {
			if string(p.ids[c][:e+1]) == string(id[:e+1]) {
				n++
			}
		}
		if n == 1 {
			return dst, nil
		}
	}
	return dst, bad("identity not unique")
}

// Get reads one reference from b and returns the entry index and the remaining bytes.
func (p *PrefixIndex) Get(b []byte) (uint32, []byte, error) {
	if len(b) < p.k0 {
		return 0, nil, bad("reference stream truncated")
	}
	var k uint32
	for i := 0; i < p.k0; i++ {
		k = k<<8 | uint32(b[i])
	}
	if j, ok := p.uniq[k]; ok {
		return j, b[p.k0:], nil
	}
	cands, ok := p.multi[k]
	if !ok {
		return 0, nil, bad("reference to an unknown identity")
	}
	cur := append([]uint32(nil), cands...)
	for e := p.k0; e < 32 && len(cur) > 1; e++ {
		if e >= len(b) {
			return 0, nil, bad("reference stream truncated")
		}
		n := cur[:0]
		for _, c := range cur {
			if p.ids[c][e] == b[e] {
				n = append(n, c)
			}
		}
		cur = n
		if len(cur) == 1 {
			return cur[0], b[e+1:], nil
		}
	}
	return 0, nil, bad("reference to an unknown identity")
}

// Zigzag maps a signed delta to an unsigned varint value and back.
func Zigzag(v int64) uint64   { return uint64(v<<1) ^ uint64(v>>63) }
func Unzigzag(u uint64) int64 { return int64(u>>1) ^ -int64(u&1) }

// FNV64 continues a 64-bit FNV-1a hash (grouping decisions only, never identity).
func FNV64(h uint64, b []byte) uint64 {
	for _, c := range b {
		h = (h ^ uint64(c)) * 1099511628211
	}
	return h
}

// FNVInit is the FNV-1a offset basis.
const FNVInit = 14695981039346656037

// PackComp builds one component: uvarint(len payload), uvarint(len stream i)..., payload, streams.
func PackComp(payload []byte, streams [][]byte) []byte {
	c := binary.AppendUvarint(nil, uint64(len(payload)))
	for _, s := range streams {
		c = binary.AppendUvarint(c, uint64(len(s)))
	}
	c = append(c, payload...)
	for _, s := range streams {
		c = append(c, s...)
	}
	return c
}

// SplitComp is the inverse of PackComp for n streams; the component must be consumed exactly.
func SplitComp(c []byte, n int) (payload []byte, streams [][]byte, err error) {
	pl, k := binary.Uvarint(c)
	if k <= 0 {
		return nil, nil, bad("component header")
	}
	c = c[k:]
	ls := make([]uint64, n)
	for i := range ls {
		if ls[i], k = binary.Uvarint(c); k <= 0 {
			return nil, nil, bad("component header")
		}
		c = c[k:]
	}
	tot := pl
	for _, l := range ls {
		if l > uint64(len(c)) {
			return nil, nil, bad("component stream length")
		}
		tot += l
		if tot > uint64(len(c)) {
			return nil, nil, bad("component length")
		}
	}
	if tot != uint64(len(c)) {
		return nil, nil, bad("component has %d trailing bytes", uint64(len(c))-tot)
	}
	payload, c = c[:pl], c[pl:]
	streams = make([][]byte, n)
	for i, l := range ls {
		streams[i], c = c[:l], c[l:]
	}
	return payload, streams, nil
}

// Sec is one section of a masked payload with the start offsets of its records (items or array
// rows) and, when known, a digest of each record's bytes and reference bytes. Cutters group
// records into components at content-defined boundaries.
type Sec struct {
	Type     uint16
	Off, End int
	Items    []int
	Hash     []uint64
}

// ClassIndex names entries (opaque integer handles) whose identities may repeat: the entries with
// one identity form a class, a reference is the class's prefix (as PrefixIndex) plus, when the class
// has several members, the member's occurrence number in handle order. A format that never meets
// duplicates pays nothing; one that does (JVM constant pools from some producers, shared DEX items)
// still resolves every reference exactly.
type ClassIndex struct {
	pi      *PrefixIndex
	members [][]int
	classOf map[int]uint32
}

// NewClassIndex groups handles by identity; ids[i] is the identity of handles[i].
func NewClassIndex(handles []int, ids []Identity, k0 int) *ClassIndex {
	c := &ClassIndex{classOf: make(map[int]uint32, len(handles))}
	idx := make(map[Identity]uint32, len(ids))
	var cls []Identity
	for i, id := range ids {
		n, ok := idx[id]
		if !ok {
			n = uint32(len(cls))
			idx[id] = n
			cls = append(cls, id)
			c.members = append(c.members, nil)
		}
		c.members[n] = append(c.members[n], handles[i])
		c.classOf[handles[i]] = n
	}
	c.pi = NewPrefixIndex(cls, k0)
	return c
}

// Collisions counts classes whose prefix is shared with another class.
func (c *ClassIndex) Collisions() int { return c.pi.Collisions() }

// Duplicates counts handles that share their identity with an earlier handle.
func (c *ClassIndex) Duplicates() int { return len(c.classOf) - len(c.members) }

// Put appends the reference to handle h.
func (c *ClassIndex) Put(dst []byte, h int) ([]byte, error) {
	n, ok := c.classOf[h]
	if !ok {
		return dst, bad("handle %d is not in the table", h)
	}
	dst, err := c.pi.Put(dst, n)
	if err != nil {
		return dst, err
	}
	if m := c.members[n]; len(m) > 1 {
		for j, x := range m {
			if x == h {
				return binary.AppendUvarint(dst, uint64(j)), nil
			}
		}
	}
	return dst, nil
}

// Get reads one reference and returns the handle.
func (c *ClassIndex) Get(b []byte) (int, []byte, error) {
	n, rest, err := c.pi.Get(b)
	if err != nil {
		return 0, nil, err
	}
	m := c.members[n]
	if len(m) == 1 {
		return m[0], rest, nil
	}
	j, k := binary.Uvarint(rest)
	if k <= 0 || j >= uint64(len(m)) {
		return 0, nil, bad("occurrence number")
	}
	return m[j], rest[k:], nil
}
