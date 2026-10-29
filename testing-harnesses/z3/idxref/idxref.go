// Package idxref is the format-independent part of the Z3-32 indexed-reference representations
// (dexz3, jvmz3): semantic identities, the prefix coder that names a table entry by a few bytes of
// its identity (with a deterministic, table-implied disambiguation), component packaging of a masked
// payload with its reference streams, and the section/record description grouping cutters use.
// It knows nothing about DEX or class files and adds nothing to repz3: every format keeps its own
// slot semantics, and the representation is still just components plus a recipe. Stdlib only.
package idxref

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
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
	k0         int
	ids        []Identity
	order      []uint32 // lexicographic identity order; no collision buckets or copied candidate lists
	widths     []uint8  // minimal unique prefix, 33 denotes an ambiguous full identity
	collisions int
}

// NewPrefixIndex builds the coder over ids (K0 in 1..4).
func NewPrefixIndex(ids []Identity, k0 int) *PrefixIndex {
	p := &PrefixIndex{k0: k0, ids: ids, order: make([]uint32, len(ids)), widths: make([]uint8, len(ids))}
	for i := range p.order {
		p.order[i] = uint32(i)
	}
	sort.Slice(p.order, func(i, j int) bool { return bytes.Compare(ids[p.order[i]][:], ids[p.order[j]][:]) < 0 })
	common := func(a, b uint32) int {
		n := 0
		for n < 32 && ids[a][n] == ids[b][n] {
			n++
		}
		return n
	}
	for pos, i := range p.order {
		n := 0
		if pos > 0 {
			n = common(i, p.order[pos-1]) + 1
		}
		if pos+1 < len(p.order) {
			if m := common(i, p.order[pos+1]) + 1; m > n {
				n = m
			}
		}
		if n < k0 {
			n = k0
		}
		p.widths[i] = uint8(n)
		if n > k0 {
			p.collisions++
		}
	}
	return p
}

// Collisions is the number of entries whose K0 prefix is shared with another entry.
func (p *PrefixIndex) Collisions() int { return p.collisions }
func (p *PrefixIndex) Len() int        { return len(p.ids) }

// Put has constant work per reference, even for adversarial colliding prefixes.
func (p *PrefixIndex) Put(dst []byte, i uint32) ([]byte, error) {
	if uint64(i) >= uint64(len(p.ids)) {
		return dst, bad("reference %d outside table of %d", i, len(p.ids))
	}
	n := int(p.widths[i])
	if n > 32 {
		return append(dst, p.ids[i][:]...), bad("identity not unique")
	}
	return append(dst, p.ids[i][:n]...), nil
}

// Get narrows a sorted interval without allocating or scanning a collision class.
func (p *PrefixIndex) Get(b []byte) (uint32, []byte, error) {
	if len(b) < p.k0 {
		return 0, nil, bad("reference stream truncated")
	}
	lo, hi := 0, len(p.order)
	for n := p.k0; n <= 32; n++ {
		if n > len(b) {
			return 0, nil, bad("reference stream truncated")
		}
		base := lo
		l := sort.Search(hi-base, func(j int) bool { return bytes.Compare(p.ids[p.order[base+j]][:n], b[:n]) >= 0 }) + base
		h := sort.Search(hi-l, func(j int) bool { return bytes.Compare(p.ids[p.order[l+j]][:n], b[:n]) > 0 }) + l
		lo, hi = l, h
		if hi-lo == 1 {
			return p.order[lo], b[n:], nil
		}
		if lo == hi {
			break
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
type classHandle struct {
	h              int
	class, ordinal uint32
}

type ClassIndex struct {
	pi      *PrefixIndex
	members []int
	starts  []uint32      // CSR class boundaries, including final sentinel
	handles []classHandle // sorted unique handles, exact legacy last-class/first-occurrence semantics
}

// NewClassIndex groups by identity with sorted compact arrays. Class numbers retain
// first-identity-occurrence order; members retain their original handle order.
func NewClassIndex(handles []int, ids []Identity, k0 int) *ClassIndex {
	c := &ClassIndex{}
	order := make([]uint32, len(ids))
	for i := range order {
		order[i] = uint32(i)
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		n := bytes.Compare(ids[a][:], ids[b][:])
		return n < 0 || n == 0 && a < b
	})
	type group struct {
		lo, hi int
		first  uint32
	}
	groups := make([]group, 0)
	for lo := 0; lo < len(order); {
		hi := lo + 1
		for hi < len(order) && ids[order[lo]] == ids[order[hi]] {
			hi++
		}
		groups = append(groups, group{lo, hi, order[lo]})
		lo = hi
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].first < groups[j].first })
	cls := make([]Identity, len(groups))
	c.starts = make([]uint32, len(groups)+1)
	c.members = make([]int, 0, len(ids))
	c.handles = make([]classHandle, len(ids))
	for n, g := range groups {
		cls[n] = ids[g.first]
		c.starts[n] = uint32(len(c.members))
		for ordinal, i := range order[g.lo:g.hi] {
			c.members = append(c.members, handles[i])
			c.handles[i] = classHandle{handles[i], uint32(n), uint32(ordinal)}
		}
	}
	c.starts[len(groups)] = uint32(len(c.members))
	// Stable ordering retains original occurrence order for repeated handles.
	sort.SliceStable(c.handles, func(i, j int) bool { return c.handles[i].h < c.handles[j].h })
	out := c.handles[:0]
	for lo := 0; lo < len(c.handles); {
		hi := lo + 1
		for hi < len(c.handles) && c.handles[hi].h == c.handles[lo].h {
			hi++
		}
		selected := c.handles[hi-1]
		for _, v := range c.handles[lo:hi] {
			if v.class == selected.class {
				selected = v
				break
			}
		}
		out = append(out, selected)
		lo = hi
	}
	c.handles = out
	c.pi = NewPrefixIndex(cls, k0)
	return c
}

func (c *ClassIndex) Collisions() int { return c.pi.Collisions() }
func (c *ClassIndex) Duplicates() int { return len(c.handles) - (len(c.starts) - 1) }

// Put resolves a handle in O(log N); duplicate-heavy classes require no scan.
func (c *ClassIndex) Put(dst []byte, h int) ([]byte, error) {
	j := sort.Search(len(c.handles), func(i int) bool { return c.handles[i].h >= h })
	if j == len(c.handles) || c.handles[j].h != h {
		return dst, bad("handle %d is not in the table", h)
	}
	v := c.handles[j]
	dst, err := c.pi.Put(dst, v.class)
	if err != nil {
		return dst, err
	}
	if c.starts[v.class+1]-c.starts[v.class] > 1 {
		dst = binary.AppendUvarint(dst, uint64(v.ordinal))
	}
	return dst, nil
}

func (c *ClassIndex) Get(b []byte) (int, []byte, error) {
	n, rest, err := c.pi.Get(b)
	if err != nil {
		return 0, nil, err
	}
	lo, hi := c.starts[n], c.starts[n+1]
	if hi-lo == 1 {
		return c.members[lo], rest, nil
	}
	j, k := binary.Uvarint(rest)
	if k <= 0 || j >= uint64(hi-lo) {
		return 0, nil, bad("occurrence number")
	}
	return c.members[uint64(lo)+j], rest[k:], nil
}
