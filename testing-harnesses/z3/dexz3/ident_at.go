package dexz3

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Mechanically adapted identity functions; canonical hashes are unchanged.
func identStringsAt(data *walkSource, f *File, w *Walked) ([]Identity, error) {
	if uint32(len(w.StrOff)) != f.Hdr.StringIDs {
		return nil, bad("string_data items %d != string_ids %d", len(w.StrOff), f.Hdr.StringIDs)
	}
	out := make([]Identity, len(w.StrOff))
	for i := range out {
		out[i] = hash('S', data.span(int64(w.StrOff[i]), int64(w.StrEnd[i]-w.StrOff[i])))
	}
	return out, nil
}

func identTypesAt(data *walkSource, f *File, S []Identity) ([]Identity, error) {
	off, n := idEntries(f, TypeTypeID, 4)
	out := make([]Identity, n)
	for i := int64(0); i < n; i++ {
		v := le32(data.span(off+4*i, 4))
		if uint64(v) >= uint64(len(S)) {
			return nil, bad("type %d descriptor %d out of range", i, v)
		}
		out[i] = hash('T', S[v][:])
	}
	return out, nil
}

// typeListIdent returns the parameter identities of the type_list at off (0 = empty).
// Prototype descriptions are hashed directly; a shared long parameter list
// never allocates a slice of identity slices, nor bypasses the aggregate budget.
func identProtosAt(data *walkSource, f *File, T []Identity) ([]Identity, error) {
	params := &walkSource{r: data.r, size: data.size, lo: -1}
	off, n := idEntries(f, TypeProtoID, 12)
	out := make([]Identity, n)
	for i := int64(0); i < n; i++ {
		e := data.span(off+12*i, 12)
		ret, list := le32(e[4:]), le32(e[8:])
		if data.err != nil {
			return nil, data.err
		}
		if uint64(ret) >= uint64(len(T)) {
			return nil, bad("proto %d return type %d", i, ret)
		}
		count := uint32(0)
		if list != 0 {
			if uint64(list)+4 > uint64(data.size) {
				return nil, bad("type_list offset %d", list)
			}
			count = le32(params.span(int64(list), 4))
			if params.err != nil {
				return nil, params.err
			}
			if uint64(count)*2 > uint64(data.size)-uint64(list)-4 {
				return nil, bad("type_list size %d", count)
			}
		}
		cost := int64(count)*32 + 37
		if cost > data.size*16+(64<<20)-data.identityWork {
			return nil, fmt.Errorf("%w: aggregate prototype identity bytes", ErrLimit)
		}
		data.identityWork += cost
		h := sha256.New()
		h.Write([]byte{'P'})
		h.Write(T[ret][:])
		var length [4]byte
		binary.LittleEndian.PutUint32(length[:], count)
		h.Write(length[:])
		for j := uint32(0); j < count; j++ {
			v := le16(params.span(int64(list)+4+2*int64(j), 2))
			if params.err != nil {
				return nil, params.err
			}
			if uint64(v) >= uint64(len(T)) {
				return nil, bad("type_list entry %d out of range", v)
			}
			h.Write(T[v][:])
		}
		h.Sum(out[i][:0])
	}
	return out, nil
}

func identFieldsAt(data *walkSource, f *File, T, S []Identity) ([]Identity, error) {
	off, n := idEntries(f, TypeFieldID, 8)
	out := make([]Identity, n)
	for i := int64(0); i < n; i++ {
		e := data.span(off+8*i, 8)
		c, ty, nm := le16(e), le16(e[2:]), le32(e[4:])
		if uint64(c) >= uint64(len(T)) || uint64(ty) >= uint64(len(T)) || uint64(nm) >= uint64(len(S)) {
			return nil, bad("field %d references out of range", i)
		}
		out[i] = hash('F', T[c][:], T[ty][:], S[nm][:])
	}
	return out, nil
}

func identMethodsAt(data *walkSource, f *File, T, P, S []Identity) ([]Identity, error) {
	off, n := idEntries(f, TypeMethodID, 8)
	out := make([]Identity, n)
	for i := int64(0); i < n; i++ {
		e := data.span(off+8*i, 8)
		c, pr, nm := le16(e), le16(e[2:]), le32(e[4:])
		if uint64(c) >= uint64(len(T)) || uint64(pr) >= uint64(len(P)) || uint64(nm) >= uint64(len(S)) {
			return nil, bad("method %d references out of range", i)
		}
		out[i] = hash('M', T[c][:], P[pr][:], S[nm][:])
	}
	return out, nil
}
