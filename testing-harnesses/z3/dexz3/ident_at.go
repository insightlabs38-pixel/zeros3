package dexz3

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
func typeListAt(data *walkSource, off uint32, T []Identity) ([][]byte, error) {
	if off == 0 {
		return nil, nil
	}
	if uint64(off)+4 > uint64(data.size) {
		return nil, bad("type_list offset %d", off)
	}
	n := le32(data.span(int64(off), 4))
	if uint64(n)*2 > uint64(data.size)-uint64(off)-4 {
		return nil, bad("type_list size %d", n)
	}
	out := make([][]byte, n)
	for j := uint32(0); j < n; j++ {
		v := le16(data.span(int64(off)+4+2*int64(j), 2))
		if uint64(v) >= uint64(len(T)) {
			return nil, bad("type_list entry %d out of range", v)
		}
		out[j] = T[v][:]
	}
	return out, nil
}

func identProtosAt(data *walkSource, f *File, T []Identity) ([]Identity, error) {
	off, n := idEntries(f, TypeProtoID, 12)
	out := make([]Identity, n)
	for i := int64(0); i < n; i++ {
		e := data.span(off+12*i, 12)
		ret := le32(e[4:])
		if uint64(ret) >= uint64(len(T)) {
			return nil, bad("proto %d return type %d", i, ret)
		}
		ps, err := typeListAt(data, le32(e[8:]), T)
		if err != nil {
			return nil, err
		}
		parts := [][]byte{T[ret][:], u32le(uint32(len(ps)))}
		parts = append(parts, ps...)
		out[i] = hash('P', parts...)
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
