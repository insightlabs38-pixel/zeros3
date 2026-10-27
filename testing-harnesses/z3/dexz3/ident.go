package dexz3

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Identity is the DEX-native semantic identity of an indexed entity: a SHA-256 over a canonical
// description built from the target's own content, never from a numeric table index.
//
//	string  H("S" | string_data_item bytes)                        exact MUTF-8 incl. its length prefix
//	type    H("T" | string identity of the descriptor)
//	proto   H("P" | return type | n | parameter types...)         shorty is derived, not identity
//	field   H("F" | class type | field type | name string)
//	method  H("M" | class type | proto | name string)
type Identity = [32]byte

func hash(tag byte, parts ...[]byte) Identity {
	h := sha256.New()
	h.Write([]byte{tag})
	for _, p := range parts {
		h.Write(p)
	}
	var out Identity
	h.Sum(out[:0])
	return out
}

func u32le(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

// Tables holds the identity of every entry of the five index spaces.
type Tables struct {
	ID [numSpaces][]Identity
}

func (t *Tables) lookup(sp Space, i uint32) (Identity, error) {
	if int(sp) >= len(t.ID) || uint64(i) >= uint64(len(t.ID[sp])) {
		return Identity{}, bad("%s index %d out of range", sp, i)
	}
	return t.ID[sp][i], nil
}

func (f *File) idSpan(t uint16) (Span, bool) { return f.Section(t) }

// identStrings computes string identities from the string_data items.
func identStrings(data []byte, f *File, w *Walked) ([]Identity, error) {
	if uint32(len(w.StrOff)) != f.Hdr.StringIDs {
		return nil, bad("string_data items %d != string_ids %d", len(w.StrOff), f.Hdr.StringIDs)
	}
	out := make([]Identity, len(w.StrOff))
	for i := range out {
		out[i] = hash('S', data[w.StrOff[i]:w.StrEnd[i]])
	}
	return out, nil
}

func idEntries(f *File, t uint16, w int64) (int64, int64) {
	s, ok := f.idSpan(t)
	if !ok {
		return 0, 0
	}
	return s.Off, int64(s.Count)
}

func identTypes(data []byte, f *File, S []Identity) ([]Identity, error) {
	off, n := idEntries(f, TypeTypeID, 4)
	out := make([]Identity, n)
	for i := int64(0); i < n; i++ {
		v := le32(data[off+4*i:])
		if uint64(v) >= uint64(len(S)) {
			return nil, bad("type %d descriptor %d out of range", i, v)
		}
		out[i] = hash('T', S[v][:])
	}
	return out, nil
}

// typeListIdent returns the parameter identities of the type_list at off (0 = empty).
func typeList(data []byte, off uint32, T []Identity) ([][]byte, error) {
	if off == 0 {
		return nil, nil
	}
	if uint64(off)+4 > uint64(len(data)) {
		return nil, bad("type_list offset %d", off)
	}
	n := le32(data[off:])
	if uint64(n)*2 > uint64(len(data))-uint64(off)-4 {
		return nil, bad("type_list size %d", n)
	}
	out := make([][]byte, n)
	for j := uint32(0); j < n; j++ {
		v := le16(data[uint64(off)+4+2*uint64(j):])
		if uint64(v) >= uint64(len(T)) {
			return nil, bad("type_list entry %d out of range", v)
		}
		out[j] = T[v][:]
	}
	return out, nil
}

func identProtos(data []byte, f *File, T []Identity) ([]Identity, error) {
	off, n := idEntries(f, TypeProtoID, 12)
	out := make([]Identity, n)
	for i := int64(0); i < n; i++ {
		e := data[off+12*i:]
		ret := le32(e[4:])
		if uint64(ret) >= uint64(len(T)) {
			return nil, bad("proto %d return type %d", i, ret)
		}
		ps, err := typeList(data, le32(e[8:]), T)
		if err != nil {
			return nil, err
		}
		parts := [][]byte{T[ret][:], u32le(uint32(len(ps)))}
		parts = append(parts, ps...)
		out[i] = hash('P', parts...)
	}
	return out, nil
}

func identFields(data []byte, f *File, T, S []Identity) ([]Identity, error) {
	off, n := idEntries(f, TypeFieldID, 8)
	out := make([]Identity, n)
	for i := int64(0); i < n; i++ {
		e := data[off+8*i:]
		c, ty, nm := le16(e), le16(e[2:]), le32(e[4:])
		if uint64(c) >= uint64(len(T)) || uint64(ty) >= uint64(len(T)) || uint64(nm) >= uint64(len(S)) {
			return nil, bad("field %d references out of range", i)
		}
		out[i] = hash('F', T[c][:], T[ty][:], S[nm][:])
	}
	return out, nil
}

func identMethods(data []byte, f *File, T, P, S []Identity) ([]Identity, error) {
	off, n := idEntries(f, TypeMethodID, 8)
	out := make([]Identity, n)
	for i := int64(0); i < n; i++ {
		e := data[off+8*i:]
		c, pr, nm := le16(e), le16(e[2:]), le32(e[4:])
		if uint64(c) >= uint64(len(T)) || uint64(pr) >= uint64(len(P)) || uint64(nm) >= uint64(len(S)) {
			return nil, bad("method %d references out of range", i)
		}
		out[i] = hash('M', T[c][:], P[pr][:], S[nm][:])
	}
	return out, nil
}

// checkUnique rejects a table holding two entries with one identity (ambiguous: nothing may be
// normalized against it).
func checkUnique(sp Space, ids []Identity) error {
	seen := make(map[Identity]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			return fmt.Errorf("%w: duplicate %s identity (ambiguous)", ErrUnsupported, sp)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// prefixIndex codes a reference to table entry i as the first K0 bytes of its identity, plus the
// fewest extra bytes that make the prefix unique among the entries sharing those K0 bytes. The
// decoder owns the same table, so the extra length is implied and no escape marker exists.
type prefixIndex struct {
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

func newPrefixIndex(ids []Identity, k0 int) *prefixIndex {
	p := &prefixIndex{k0: k0, ids: ids, uniq: make(map[uint32]uint32, len(ids)), multi: map[uint32][]uint32{}}
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

func (p *prefixIndex) put(dst []byte, i uint32) ([]byte, error) {
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

// get reads one reference from b and returns the table index and the remaining bytes.
func (p *prefixIndex) get(b []byte) (uint32, []byte, error) {
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
