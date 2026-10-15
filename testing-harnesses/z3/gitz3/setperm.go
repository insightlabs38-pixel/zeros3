package gitz3

import (
	"bytes"
	"encoding/binary"
	"hash/adler32"
	"io"
	"math/bits"
	"sort"

	"zeros3-z3/lab"
)

// Set+permutation layout. Entries are described order-independently - by object
// id, with delta bases named by object id - and the descriptors are sorted by id
// into one byte string S (the "set table"), cut with production CDC into
// components. A pack is then (S chunks, permutation). Two packs of the same
// object set in different order share S entirely and differ only in the
// permutation; packs that differ in some entries share the unchanged chunks of S.
// The descriptor table also carries the object ids, so lookup by native id needs
// no separate index. The pieces the descriptors name are `Via` members: reachable
// through S, so the recipe carries no reference to them.

const setMagic = 0xA2

type setDesc struct {
	id     []byte
	body   []byte // everything after the id
	pieces []lab.Piece
}

// appendPieceList writes a piece list. A piece whose component SHA-256 equals
// selfID (a SHA-256 repository's object id of a single-component object) is
// written as the tag 2 with no digest: the native id already is the identity.
func appendPieceList(dst []byte, pcs []lab.Piece, via *[]lab.Piece, selfID []byte, self *int) []byte {
	dst = appendUv(dst, uint64(len(pcs)))
	for _, pc := range pcs {
		if pc.Comp == nil {
			dst = appendUv(dst, uint64(len(pc.Inline))<<1|1)
			dst = append(dst, pc.Inline...)
			continue
		}
		if selfID != nil && len(pcs) == 1 && string(pc.Comp.ID[:]) == string(selfID) {
			dst = appendUv(dst, 2)
			*self++
		} else {
			dst = appendUv(dst, 0)
			dst = append(dst, pc.Comp.ID[:]...)
		}
		*via = append(*via, lab.Piece{Comp: pc.Comp, Len: pc.Len, Via: true})
	}
	return dst
}

// SetStats describe a set-layout plan.
type SetStats struct {
	PackStats
	TableBytes, PermBytes int64
	ReconBytes            int64 // bytes of the reconstruction table (separate from the stable table)
	TableChunks           int
	SelfDigest            int     // plain components whose digest is the native id (SHA-256 repositories)
	SectionOf             []int   // pack entry index -> table section
	SectionBytes          []int64 // bytes of each table section
}

// SetPlan builds the set+permutation recipe of a pack.
func (an *PackAnalysis) SetPlan(o RepOpts) (*lab.Plan, *SetStats) {
	p := an.P
	n := len(an.Entries)
	st := &SetStats{}
	st.Entries = n
	order := make([]int, n) // sorted position -> pack index
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return bytes.Compare(p.Entries[order[a]].ID, p.Entries[order[b]].ID) < 0 })
	rank := make([]int, n) // pack index -> sorted position
	for r, i := range order {
		rank[i] = r
	}
	// Two tables, both sorted by object id and cut at the same id-chosen
	// boundaries: the stable table (identity: ids, kinds, bases, component
	// digests) and the reconstruction table (zlib header code, DEFLATE skeleton and
	// unit references of ModeP entries). A recompression event rewrites only the
	// second.
	var table, rtab []byte
	var via []lab.Piece
	var tp, rp []lab.Piece
	natives := map[lab.ID][]byte{} // component -> Git object id, for single-component canonical objects
	secStart, rStart := 0, 0
	st.SectionOf = make([]int, n)
	cut := func() {
		if len(table) > secStart {
			tp = append(tp, mkPieces(table[secStart:], o.inline(), "settable")...)
			st.SectionBytes = append(st.SectionBytes, int64(len(table)-secStart))
			secStart = len(table)
		}
		if len(rtab) > rStart {
			rp = append(rp, mkPieces(rtab[rStart:], o.inline(), "recontable")...)
			rStart = len(rtab)
		}
	}
	for _, i := range order {
		ep := &an.Entries[i]
		e := &p.Entries[i]
		mode := o.chooseMode(ep)
		pcs, nPlain, _ := entryPieces(ep, mode, o)
		zc, explicit := zhdrCode(ep.ZHdr[:])
		st.SectionOf[i] = len(st.SectionBytes)
		table = append(table, e.ID...)
		table = append(table, ep.Kind|byte(mode)<<3)
		if e.IsDelta() {
			table = append(table, p.Entries[e.Base].ID...)
			st.Deltas++
		}
		var selfID []byte
		if p.Algo == SHA256 && ep.Kind <= 4 {
			selfID = e.ID
		}
		if mode == ModeZ {
			table = appendUv(table, uint64(ep.Size))
			table = appendPieceList(table, pcs, &via, nil, &st.SelfDigest)
			st.ModeZ++
			st.ZlibBytes += int64(len(ep.Z))
		} else {
			table = appendPieceList(table, pcs[:nPlain], &via, selfID, &st.SelfDigest)
			rtab = append(rtab, zc)
			if explicit {
				rtab = append(rtab, ep.ZHdr[:]...)
			}
			rtab = appendUv(rtab, uint64(len(ep.Skel)))
			rtab = append(rtab, ep.Skel...)
			rtab = appendPieceList(rtab, pcs[nPlain:], &via, nil, &st.SelfDigest)
			st.ModeP++
			st.PlainBytes += int64(len(ep.Comp))
			st.SkelBytes += int64(len(ep.Skel))
		}
		if mode == ModeP && ep.Kind <= 4 && nPlain == 1 && pcs[0].Comp != nil {
			natives[pcs[0].Comp.ID] = e.ID
		}
		if !ep.IROK {
			st.NoIR++
		}
		// section boundary chosen by the object id (uniform), so adding or
		// removing an object only changes the section it falls in
		if binary.BigEndian.Uint16(e.ID[:2])%uint16(o.sectionK()) == 0 {
			cut()
		}
	}
	cut()
	// permutation: pack position -> sorted position, ceil(log2 n) bits each
	w := bits.Len(uint(max(n-1, 1)))
	perm := packBits(n, w, func(pos int) uint64 { return uint64(rank[pos]) })
	var meta []byte
	meta = append(meta, setMagic, byte(p.Algo))
	meta = appendUv(meta, uint64(p.Version))
	meta = appendUv(meta, uint64(n))
	meta = appendUv(meta, uint64(len(tp)))
	meta = appendUv(meta, uint64(len(rp)))
	meta = append(meta, byte(w))
	meta = append(meta, perm...)
	st.TableBytes, st.PermBytes, st.ReconBytes = int64(len(table)), int64(len(perm)), int64(len(rtab))
	st.TableChunks = len(tp)
	st.MetaBytes = int64(len(meta))
	pl := &lab.Plan{MetaBytes: int64(len(meta)), Detail: map[string]any{"meta": meta, "natives": natives}}
	pl.Pieces = append(pl.Pieces, tp...)
	pl.Pieces = append(pl.Pieces, rp...)
	pl.Pieces = append(pl.Pieces, via...)
	pl.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		return RebuildSet(meta, pl.Pieces, get, w)
	}
	return pl, st
}

func (o RepOpts) sectionK() int {
	if o.SectionK <= 0 {
		return 8
	}
	return o.SectionK
}

func packBits(n, w int, at func(int) uint64) []byte {
	out := make([]byte, (n*w+7)/8)
	bit := 0
	for i := 0; i < n; i++ {
		v := at(i)
		for b := 0; b < w; b++ {
			if v>>uint(b)&1 != 0 {
				out[bit>>3] |= 1 << uint(bit&7)
			}
			bit++
		}
	}
	return out
}

func unpackBit(b []byte, bit, w int) uint64 {
	var v uint64
	for k := 0; k < w; k++ {
		if b[(bit+k)>>3]>>uint((bit+k)&7)&1 != 0 {
			v |= 1 << uint(k)
		}
	}
	return v
}

// RebuildSet is the reconstruction path of SetPlan: meta -> set table (from its
// chunk pieces) -> descriptors -> permutation order -> exact pack.
func RebuildSet(meta []byte, pieces []lab.Piece, get func(lab.ID) ([]byte, error), out io.Writer) error {
	m := &mreader{b: meta}
	if m.byte() != setMagic {
		return corrupt("recipe magic")
	}
	a := Algo(m.byte())
	if a != SHA1 && a != SHA256 {
		return corrupt("recipe algo")
	}
	ver := m.uv(3)
	n := int(m.uv(1 << 32))
	nt := int(m.uv(1 << 20))
	nr := int(m.uv(1 << 20))
	w := int(m.byte())
	if m.err != nil || nt+nr > len(pieces) || w > 32 {
		return corrupt("recipe header")
	}
	perm := m.take((n*w + 7) / 8)
	if m.err != nil {
		return m.err
	}
	var table, rtab []byte
	for _, pc := range pieces[:nt] {
		b, err := pieceBytes(pc, get)
		if err != nil {
			return err
		}
		table = append(table, b...)
	}
	for _, pc := range pieces[nt : nt+nr] {
		b, err := pieceBytes(pc, get)
		if err != nil {
			return err
		}
		rtab = append(rtab, b...)
	}
	hs := a.Size()
	if int64(n)*int64(hs+2) > int64(len(table)) {
		return corrupt("table too short for %d entries", n)
	}
	t := &mreader{b: table}
	rt := &mreader{b: rtab}
	type desc struct {
		id             []byte
		kind, mode, zc byte
		zh             [2]byte
		baseID         []byte
		size           int64
		z              []byte
		skel           []byte
		plain, units   [][]byte // component bytes, resolved lazily
		plainP, unitP  []lab.ID
		inlP, inlU     map[int][]byte
	}
	readList := func(t *mreader, self []byte) (ids []lab.ID, inl map[int][]byte, err error) {
		cnt := int(t.uv(1 << 20))
		inl = map[int][]byte{}
		for k := 0; k < cnt; k++ {
			v := t.uv(1 << 40)
			if t.err != nil {
				return nil, nil, t.err
			}
			if v&1 == 1 {
				inl[k] = t.take(int(v >> 1))
				ids = append(ids, lab.ID{})
				continue
			}
			var id lab.ID
			switch v {
			case 0:
				copy(id[:], t.take(32))
			case 2:
				if len(self) != 32 {
					return nil, nil, corrupt("self digest in a non-SHA-256 table")
				}
				copy(id[:], self)
			default:
				return nil, nil, corrupt("piece tag %d", v)
			}
			ids = append(ids, id)
		}
		return ids, inl, t.err
	}
	fetch := func(ids []lab.ID, inl map[int][]byte) ([][]byte, error) {
		var res [][]byte
		for k, id := range ids {
			if b, ok := inl[k]; ok {
				res = append(res, b)
				continue
			}
			b, err := get(id)
			if err != nil {
				return nil, err
			}
			res = append(res, b)
		}
		return res, nil
	}
	descs := make([]desc, n)
	for i := 0; i < n; i++ {
		d := &descs[i]
		d.id = t.take(hs)
		b0 := t.byte()
		d.kind, d.mode = b0&7, (b0>>3)&1
		if b0>>4 != 0 {
			return corrupt("table flags")
		}
		if d.kind == ofsDelta || d.kind == refDelta {
			d.baseID = t.take(hs)
		} else if d.kind < 1 || d.kind > 4 {
			return corrupt("entry kind")
		}
		if d.mode == ModeZ {
			d.size = int64(t.uv(1 << 40))
			ids, inl, err := readList(t, nil)
			if err != nil {
				return err
			}
			parts, err := fetch(ids, inl)
			if err != nil {
				return err
			}
			d.z = bytes.Join(parts, nil)
		} else {
			pids, pinl, err := readList(t, d.id)
			if err != nil {
				return err
			}
			d.zc = rt.byte()
			switch {
			case d.zc < 4:
				d.zh = knownZHdr[d.zc]
			case d.zc == 7:
				copy(d.zh[:], rt.take(2))
			default:
				return corrupt("zlib header code")
			}
			d.skel = rt.take(int(rt.uv(1 << 30)))
			uids, uinl, err := readList(rt, nil)
			if err != nil {
				return err
			}
			d.plainP, d.inlP, d.unitP, d.inlU = pids, pinl, uids, uinl
		}
		if rt.err != nil {
			return rt.err
		}
		if t.err != nil {
			return t.err
		}
	}
	h := a.New()
	mw := io.MultiWriter(out, h)
	var hdr [12]byte
	copy(hdr[:], "PACK")
	binary.BigEndian.PutUint32(hdr[4:], uint32(ver))
	binary.BigEndian.PutUint32(hdr[8:], uint32(n))
	mw.Write(hdr[:])
	off := int64(12)
	offOf := map[string]int64{}
	for pos := 0; pos < n; pos++ {
		r := int(unpackBit(perm, pos*w, w))
		if r >= n {
			return corrupt("permutation rank %d", r)
		}
		d := &descs[r]
		var baseEnc []byte
		switch d.kind {
		case ofsDelta:
			bo, ok := offOf[string(d.baseID)]
			if !ok {
				return corrupt("OFS base not yet written")
			}
			baseEnc = encodeOfs(nil, uint64(off-bo))
		case refDelta:
			baseEnc = d.baseID
		}
		size, z := d.size, d.z
		if d.mode == ModeP {
			pb, err := fetch(d.plainP, d.inlP)
			if err != nil {
				return err
			}
			ub, err := fetch(d.unitP, d.inlU)
			if err != nil {
				return err
			}
			comp := bytes.Join(pb, nil)
			plain := comp
			if d.kind <= 4 {
				ty, sz, hl, err := ParseCanonHeader(comp)
				if err != nil || uint8(ty) != d.kind || sz != int64(len(comp)-hl) {
					return corrupt("canonical object header in component")
				}
				plain = comp[hl:]
			}
			size = int64(len(plain))
			def, err := replayDeflate(d.skel, ub, plain)
			if err != nil {
				return err
			}
			z = append(append([]byte(nil), d.zh[:]...), def...)
			z = binary.BigEndian.AppendUint32(z, adler32.Checksum(plain))
		}
		offOf[string(d.id)] = off
		eh := encodeEntryHeader(nil, d.kind, size)
		eh = append(eh, baseEnc...)
		mw.Write(eh)
		mw.Write(z)
		off += int64(len(eh) + len(z))
	}
	_, err := out.Write(h.Sum(nil))
	return err
}
