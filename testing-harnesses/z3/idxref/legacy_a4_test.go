package idxref

import "encoding/binary"

// Mechanically preserved pre-A4 implementation: immutable wire/error oracle.
type legacyPrefixIndex struct {
	k0    int
	ids   []Identity
	uniq  map[uint32]uint32
	multi map[uint32][]uint32
}

func legacyKey(id *Identity, k0 int) uint32 {
	var k uint32
	for i := 0; i < k0; i++ {
		k = k<<8 | uint32(id[i])
	}
	return k
}

// legacyNewPrefixIndex builds the coder over ids (K0 in 1..4).
func legacyNewPrefixIndex(ids []Identity, k0 int) *legacyPrefixIndex {
	p := &legacyPrefixIndex{k0: k0, ids: ids, uniq: make(map[uint32]uint32, len(ids)), multi: map[uint32][]uint32{}}
	for i := range ids {
		k := legacyKey(&ids[i], k0)
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
func (p *legacyPrefixIndex) Collisions() int {
	n := 0
	for _, c := range p.multi {
		n += len(c)
	}
	return n
}

// Len is the table size.
func (p *legacyPrefixIndex) Len() int { return len(p.ids) }

// Put appends the reference to entry i.
func (p *legacyPrefixIndex) Put(dst []byte, i uint32) ([]byte, error) {
	if uint64(i) >= uint64(len(p.ids)) {
		return dst, bad("reference %d outside table of %d", i, len(p.ids))
	}
	id := &p.ids[i]
	k := legacyKey(id, p.k0)
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
func (p *legacyPrefixIndex) Get(b []byte) (uint32, []byte, error) {
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

type legacyClassIndex struct {
	pi      *legacyPrefixIndex
	members [][]int
	classOf map[int]uint32
}

// legacyNewClassIndex groups handles by identity; ids[i] is the identity of handles[i].
func legacyNewClassIndex(handles []int, ids []Identity, k0 int) *legacyClassIndex {
	c := &legacyClassIndex{classOf: make(map[int]uint32, len(handles))}
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
	c.pi = legacyNewPrefixIndex(cls, k0)
	return c
}

// Collisions counts classes whose prefix is shared with another class.
func (c *legacyClassIndex) Collisions() int { return c.pi.Collisions() }

// Duplicates counts handles that share their identity with an earlier handle.
func (c *legacyClassIndex) Duplicates() int { return len(c.classOf) - len(c.members) }

// Put appends the reference to handle h.
func (c *legacyClassIndex) Put(dst []byte, h int) ([]byte, error) {
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
func (c *legacyClassIndex) Get(b []byte) (int, []byte, error) {
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
