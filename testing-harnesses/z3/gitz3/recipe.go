package gitz3

import (
	"encoding/binary"

	"zeros3-z3/lab"
)

// MarshalPieces serialises a plan's piece list in the lab's "table" layout, the
// layout every headline cost uses: a digest table (32-byte SHA-256 and a length
// varint per distinct component, in first-use order), then one varint per piece
// (table index << 1) or, for an inline piece, varint(len<<1|1) and its bytes.
// Pieces marked Via are reachable through other content and are not listed. The
// bytes returned are exactly what the cost accounting charges (plus a piece
// count), and UnmarshalPieces + Rebuild reproduce the object from them.
func MarshalPieces(ps []lab.Piece) []byte {
	idx := map[lab.ID]int{}
	var table, body []byte
	n := 0
	for _, p := range ps {
		if p.Comp == nil {
			body = binary.AppendUvarint(body, uint64(len(p.Inline))<<1|1)
			body = append(body, p.Inline...)
			n++
			continue
		}
		if p.Via {
			continue
		}
		i, ok := idx[p.Comp.ID]
		if !ok {
			i = len(idx)
			idx[p.Comp.ID] = i
			table = append(table, p.Comp.ID[:]...)
			table = binary.AppendUvarint(table, uint64(p.Comp.Size))
		}
		body = binary.AppendUvarint(body, uint64(i)<<1)
		n++
	}
	out := binary.AppendUvarint(nil, uint64(len(idx)))
	out = append(out, table...)
	out = binary.AppendUvarint(out, uint64(n))
	return append(out, body...)
}

// UnmarshalPieces is the inverse of MarshalPieces. Component pieces carry only
// identity and size; payloads come from the store.
func UnmarshalPieces(b []byte) ([]lab.Piece, error) {
	m := &mreader{b: b}
	nt := int(m.uv(1 << 24))
	type ent struct {
		id   lab.ID
		size int64
	}
	if m.err != nil || nt > len(b) {
		return nil, corrupt("piece table")
	}
	tab := make([]ent, 0, nt)
	for i := 0; i < nt; i++ {
		var e ent
		copy(e.id[:], m.take(32))
		e.size = int64(m.uv(1 << 40))
		tab = append(tab, e)
	}
	n := int(m.uv(1 << 24))
	if m.err != nil || n > len(b) {
		return nil, corrupt("piece count")
	}
	ps := make([]lab.Piece, 0, n)
	for i := 0; i < n; i++ {
		v := m.uv(1 << 40)
		if m.err != nil {
			return nil, m.err
		}
		if v&1 == 1 {
			in := m.take(int(v >> 1))
			ps = append(ps, lab.Piece{Inline: append([]byte(nil), in...), Len: int64(len(in))})
			continue
		}
		if int(v>>1) >= len(tab) {
			return nil, corrupt("piece index")
		}
		e := tab[v>>1]
		ps = append(ps, lab.Piece{Comp: &lab.Comp{ID: e.id, Size: e.size}, Len: e.size})
	}
	if m.err != nil {
		return nil, m.err
	}
	return ps, nil
}

func uvLen(v uint64) int64 { return int64(binary.PutUvarint(make([]byte, binary.MaxVarintLen64), v)) }

// CountListed is the number of pieces MarshalPieces writes.
func CountListed(ps []lab.Piece) int {
	n := 0
	for _, p := range ps {
		if p.Comp == nil || !p.Via {
			n++
		}
	}
	return n
}
