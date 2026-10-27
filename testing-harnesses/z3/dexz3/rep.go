package dexz3

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/adler32"
)

// Representation of one DEX (the "indexed-reference" candidate):
//
//	Meta   exact header (112 B) and map list, parameters         explicit recipe
//	Comps  groups of the *masked payload* (the file without header and map, every index/offset
//	       slot overwritten by a length-preserving mask), each followed by the reference bytes of
//	       the slots that start inside it:
//	         five index streams  prefix of the target's semantic Identity (3 bytes + implied extra)
//	         one offset stream   zigzag varint of the target item's ordinal minus the previous
//	                             target ordinal of the same offset kind
//
// Decode needs only Meta and the component bytes. It re-walks the masked payload (structure and
// instruction widths do not depend on masked values), resolves offsets from the item layout,
// then the index spaces in dependency order string < type < proto < field/method, and writes
// every slot back with the length the mask recorded.

const (
	repMagic  = "DXN1"
	numStream = 6 // string, type, proto, field, method, offset
	streamOff = 5
	flagAdler = 1 // the original header checksum was valid and is re-verified by Decode
)

// Config selects the representation parameters.
type Config struct {
	K0 int // identity prefix bytes, 1..4 (default 3)
	// Cut returns the exclusive end offsets of the payload groups; secs describe the sections of
	// the payload (payload coordinates) and the item boundaries a record-aware cutter may use.
	// nil: one group.
	Cut func(payload []byte, secs []Sec) []int
}

// Stats are the encoder-side measurements of one representation.
type Stats struct {
	Slots      [numSpaces]int // slots per space (offset = SpOffset)
	StreamLen  [numStream]int // total reference bytes per stream
	Collisions [numSpaces]int // table entries whose K0 prefix is shared
	Payload    int
	Comps      int
}

// Rep is an encoded DEX.
type Rep struct {
	Meta  []byte
	Comps [][]byte
	Stats Stats
	Cuts  []int // payload offset where each component ends (analysis)
	Secs  []Sec // sections in payload coordinates (analysis)
}

func fnv64(h uint64, b []byte) uint64 {
	for _, c := range b {
		h = (h ^ uint64(c)) * 1099511628211
	}
	return h
}

func zigzag(v int64) uint64   { return uint64(v<<1) ^ uint64(v>>63) }
func unzigzag(u uint64) int64 { return int64(u>>1) ^ -int64(u&1) }

// offItems collects, per target section type, the item start offsets in file order.
func offItems(w *Walked) map[uint16][]int64 {
	m := map[uint16][]int64{}
	for _, it := range w.Items {
		m[it.Type] = append(m[it.Type], it.Off)
	}
	ss := make([]int64, len(w.StrOff))
	for i, o := range w.StrOff {
		ss[i] = int64(o)
	}
	m[TypeStringData] = ss
	return m
}

func spaceOf(s Space) int { return int(s) - 1 } // SpString..SpMethod -> 0..4

func (f *File) mapSpan() (int64, int64) {
	return int64(f.Hdr.MapOff), 4 + 12*int64(len(f.Map))
}

// Encode builds the representation of data (one DEX in memory). The result is verified by a full
// Decode before it is returned; any mismatch is ErrUnsupported (the caller falls back to CDC).
func Encode(f *File, data []byte, cfg Config) (*Rep, error) {
	k0 := cfg.K0
	if k0 == 0 {
		k0 = 3
	}
	if k0 < 1 || k0 > 4 {
		return nil, fmt.Errorf("dexz3: K0 %d", k0)
	}
	w, err := Walk(f, data, WalkOpts{Offsets: true, Debug: true})
	if err != nil {
		return nil, err
	}
	// identities from the original
	var tb Tables
	if tb.ID[SpString], err = identStrings(data, f, w); err != nil {
		return nil, err
	}
	if tb.ID[SpType], err = identTypes(data, f, tb.ID[SpString]); err != nil {
		return nil, err
	}
	if tb.ID[SpProto], err = identProtos(data, f, tb.ID[SpType]); err != nil {
		return nil, err
	}
	if tb.ID[SpField], err = identFields(data, f, tb.ID[SpType], tb.ID[SpString]); err != nil {
		return nil, err
	}
	if tb.ID[SpMethod], err = identMethods(data, f, tb.ID[SpType], tb.ID[SpProto], tb.ID[SpString]); err != nil {
		return nil, err
	}
	for sp := SpString; sp <= SpMethod; sp++ {
		if err := checkUnique(sp, tb.ID[sp]); err != nil {
			return nil, err
		}
	}
	var pi [numSpaces]*prefixIndex
	var st Stats
	for sp := SpString; sp <= SpMethod; sp++ {
		pi[sp] = newPrefixIndex(tb.ID[sp], k0)
		for _, c := range pi[sp].multi {
			st.Collisions[sp] += len(c)
		}
	}
	// masked payload and per-slot reference bytes
	masked := append([]byte(nil), data...)
	mOff, mLen := f.mapSpan()
	for i := range w.Slots {
		MaskSlot(masked, &w.Slots[i])
	}
	oc := newOffCoder(f, w, masked, k0)
	refs := make([]struct {
		pos int
		st  int
		b   []byte
	}, 0, len(w.Slots))
	for i := range w.Slots {
		s := &w.Slots[i]
		pos := int(s.Off) - HeaderSize
		if s.Off >= mOff+mLen {
			pos -= int(mLen)
		} else if s.Off+int64(s.Len) > mOff {
			return nil, bad("slot overlaps the map list")
		}
		st.Slots[s.Space]++
		var b []byte
		var stream int
		if s.Space == SpOffset {
			stream = streamOff
			if b, err = oc.put(s.Kind, int64(s.Val)); err != nil {
				return nil, err
			}
		} else {
			stream = spaceOf(s.Space)
			if b, err = pi[s.Space].put(nil, s.Val); err != nil {
				return nil, err
			}
		}
		st.StreamLen[stream] += len(b)
		refs = append(refs, struct {
			pos int
			st  int
			b   []byte
		}{pos, stream, b})
	}
	// payload: file without header and map list
	payload := make([]byte, 0, len(data))
	payload = append(payload, masked[HeaderSize:mOff]...)
	payload = append(payload, masked[mOff+mLen:]...)
	st.Payload = len(payload)
	secs := f.Sections(w, true)
	ri := 0
	for si := range secs {
		sc := &secs[si]
		sc.Hash = make([]uint64, len(sc.Items))
		for i, st := range sc.Items {
			end := sc.End
			if i+1 < len(sc.Items) {
				end = sc.Items[i+1]
			}
			h := fnv64(14695981039346656037, payload[st:end])
			for ri < len(refs) && refs[ri].pos < st {
				ri++
			}
			for j := ri; j < len(refs) && refs[j].pos < end; j++ {
				h = fnv64(h^uint64(refs[j].st), refs[j].b)
			}
			sc.Hash[i] = h
		}
	}
	var cuts []int
	if cfg.Cut != nil {
		cuts = cfg.Cut(payload, secs)
	}
	if len(cuts) == 0 || cuts[len(cuts)-1] != len(payload) {
		cuts = append(cuts, len(payload))
	}
	// components
	rep := &Rep{Stats: st, Cuts: cuts, Secs: secs}
	ri, start := 0, 0
	for _, end := range cuts {
		if end < start || end > len(payload) {
			return nil, fmt.Errorf("dexz3: bad cut %d", end)
		}
		var streams [numStream][]byte
		for ri < len(refs) && refs[ri].pos < end {
			streams[refs[ri].st] = append(streams[refs[ri].st], refs[ri].b...)
			ri++
		}
		c := binary.AppendUvarint(nil, uint64(end-start))
		for _, s := range streams {
			c = binary.AppendUvarint(c, uint64(len(s)))
		}
		c = append(c, payload[start:end]...)
		for _, s := range streams {
			c = append(c, s...)
		}
		rep.Comps = append(rep.Comps, c)
		start = end
	}
	rep.Stats.Comps = len(rep.Comps)
	// meta
	var flags byte
	if adler32.Checksum(data[12:]) == f.Hdr.Checksum {
		flags |= flagAdler
	}
	m := append([]byte(repMagic), flags, byte(k0))
	m = append(m, f.Raw[:]...)
	mapb := make([]byte, mLen)
	copy(mapb, data[mOff:mOff+mLen])
	m = append(m, mapb...)
	rep.Meta = m
	// self-check
	got, err := Decode(rep.Meta, rep.Comps)
	if err != nil {
		return nil, fmt.Errorf("%w: self-check decode: %v", ErrUnsupported, err)
	}
	if !bytes.Equal(got, data) {
		return nil, fmt.Errorf("%w: self-check mismatch", ErrUnsupported)
	}
	return rep, nil
}

// SplitComp splits one component into its payload and reference streams.
func SplitComp(c []byte) (payload []byte, streams [numStream][]byte, err error) {
	n, k := binary.Uvarint(c)
	if k <= 0 {
		return nil, streams, bad("component header")
	}
	c = c[k:]
	var ls [numStream]uint64
	for i := range ls {
		if ls[i], k = binary.Uvarint(c); k <= 0 {
			return nil, streams, bad("component header")
		}
		c = c[k:]
	}
	tot := n
	for _, l := range ls {
		if l > uint64(len(c)) {
			return nil, streams, bad("component stream length")
		}
		tot += l
		if tot > uint64(len(c)) {
			return nil, streams, bad("component length")
		}
	}
	if tot != uint64(len(c)) {
		return nil, streams, bad("component has %d trailing bytes", uint64(len(c))-tot)
	}
	payload, c = c[:n], c[n:]
	for i, l := range ls {
		streams[i], c = c[:l], c[l:]
	}
	return payload, streams, nil
}

// Decode rebuilds the DEX from Meta and the component bytes (in order).
func Decode(meta []byte, comps [][]byte) ([]byte, error) {
	if len(meta) < len(repMagic)+2+HeaderSize || string(meta[:4]) != repMagic {
		return nil, bad("representation header")
	}
	flags, k0 := meta[4], int(meta[5])
	if k0 < 1 || k0 > 4 {
		return nil, bad("K0 %d", k0)
	}
	var raw [HeaderSize]byte
	copy(raw[:], meta[6:6+HeaderSize])
	mapb := meta[6+HeaderSize:]
	size := int64(le32(raw[32:]))
	mOff := int64(le32(raw[52:]))
	if size < HeaderSize+int64(len(mapb)) || mOff < HeaderSize || mOff+int64(len(mapb)) > size || len(mapb) < 16 {
		return nil, bad("header/map geometry")
	}
	if size > 1<<31 {
		return nil, fmt.Errorf("%w: file size", ErrLimit)
	}
	buf := make([]byte, size)
	copy(buf, raw[:])
	copy(buf[mOff:], mapb)
	var streams [numStream][]byte
	pos := int64(HeaderSize)
	fill := func(b []byte) error {
		for len(b) > 0 {
			if pos == mOff {
				pos += int64(len(mapb))
			}
			lim := size
			if pos < mOff {
				lim = mOff
			}
			if pos >= lim {
				return bad("payload longer than the file")
			}
			n := copy(buf[pos:lim], b)
			pos += int64(n)
			b = b[n:]
		}
		return nil
	}
	for _, c := range comps {
		p, ss, err := SplitComp(c)
		if err != nil {
			return nil, err
		}
		if err := fill(p); err != nil {
			return nil, err
		}
		for i := range ss {
			streams[i] = append(streams[i], ss[i]...)
		}
	}
	if pos == mOff {
		pos += int64(len(mapb))
	}
	if pos != size {
		return nil, bad("payload shorter than the file (%d of %d)", pos, size)
	}
	f, err := Parse(bytes.NewReader(buf), size, Limits{})
	if err != nil {
		return nil, err
	}
	w, err := Walk(f, buf, WalkOpts{Masked: true, Offsets: true, Debug: true})
	if err != nil {
		return nil, err
	}
	// 1. offsets (item classes come from the masked bytes, so they are built before any write)
	oc := newOffCoder(f, w, buf, k0)
	osr := streams[streamOff]
	for i := range w.Slots {
		s := &w.Slots[i]
		if s.Space != SpOffset {
			continue
		}
		var pos int64
		var err error
		if pos, osr, err = oc.get(s.Kind, osr); err != nil {
			return nil, err
		}
		s.Val = uint32(pos)
		if err := putSlot(buf, s, s.Val); err != nil {
			return nil, err
		}
	}
	if len(osr) != 0 {
		return nil, bad("trailing offset bytes")
	}
	// 2. index spaces in dependency order
	var tb Tables
	var pi [numSpaces]*prefixIndex
	var sr [numStream][]byte
	copy(sr[:], streams[:])
	resolve := func(sp Space) error {
		pi[sp] = newPrefixIndex(tb.ID[sp], k0)
		b := sr[spaceOf(sp)]
		for i := range w.Slots {
			s := &w.Slots[i]
			if s.Space != sp {
				continue
			}
			v, rest, err := pi[sp].get(b)
			if err != nil {
				return err
			}
			b = rest
			s.Val = v
			if s.Enc != EncDelta {
				if err := putSlot(buf, s, v); err != nil {
					return err
				}
			}
		}
		if len(b) != 0 {
			return bad("trailing %s reference bytes", sp)
		}
		return nil
	}
	if tb.ID[SpString], err = identStrings(buf, f, w); err != nil {
		return nil, err
	}
	if err := resolve(SpString); err != nil {
		return nil, err
	}
	if tb.ID[SpType], err = identTypes(buf, f, tb.ID[SpString]); err != nil {
		return nil, err
	}
	if err := resolve(SpType); err != nil {
		return nil, err
	}
	if tb.ID[SpProto], err = identProtos(buf, f, tb.ID[SpType]); err != nil {
		return nil, err
	}
	if err := resolve(SpProto); err != nil {
		return nil, err
	}
	if tb.ID[SpField], err = identFields(buf, f, tb.ID[SpType], tb.ID[SpString]); err != nil {
		return nil, err
	}
	if tb.ID[SpMethod], err = identMethods(buf, f, tb.ID[SpType], tb.ID[SpProto], tb.ID[SpString]); err != nil {
		return nil, err
	}
	if err := resolve(SpField); err != nil {
		return nil, err
	}
	if err := resolve(SpMethod); err != nil {
		return nil, err
	}
	// 3. delta-coded members
	for i := range w.Slots {
		s := &w.Slots[i]
		if s.Enc != EncDelta {
			continue
		}
		var prev uint32
		if s.Prev >= 0 {
			prev = w.Slots[s.Prev].Val
			if s.Val <= prev {
				return nil, bad("class_data members not increasing")
			}
		}
		if err := putSlot(buf, s, s.Val-prev); err != nil {
			return nil, err
		}
	}
	if flags&flagAdler != 0 && adler32.Checksum(buf[12:]) != le32(buf[8:]) {
		return nil, bad("header checksum does not match the rebuilt file")
	}
	return buf, nil
}

// putSlot writes value v into the slot's bytes using the slot's own length.
func putSlot(b []byte, s *Slot, v uint32) error {
	p := b[s.Off : s.Off+int64(s.Len)]
	switch s.Enc {
	case EncU16:
		if v > 0xFFFF {
			return bad("value %d does not fit 16 bits", v)
		}
		binary.LittleEndian.PutUint16(p, uint16(v))
	case EncU32:
		binary.LittleEndian.PutUint32(p, v)
	case EncVal:
		if len(p) < 4 && v>>(8*uint(len(p))) != 0 {
			return bad("value %d does not fit %d bytes", v, len(p))
		}
		for i := range p {
			p[i] = byte(v >> (8 * uint(i)))
		}
	case EncP1, EncULEB, EncDelta:
		if s.Enc == EncP1 {
			if v == NoIndex {
				return bad("index overflow")
			}
			v++
		}
		n := len(p)
		if n < 5 && uint64(v)>>(7*uint(n)) != 0 {
			return bad("value %d does not fit ULEB128 of %d bytes", v, n)
		}
		for i := 0; i < n; i++ {
			c := byte(v>>(7*uint(i))) & 0x7f
			if i < n-1 {
				c |= 0x80
			}
			p[i] = c
		}
	}
	return nil
}

// Sec is one section of the file (or of the payload) with the start offsets of its items.
type Sec struct {
	Type     uint16
	Off, End int
	Items    []int // item (or array row) starts inside [Off, End), ascending; nil for gaps
	// Hash is, for each item, a digest of its payload bytes and of the reference bytes of the
	// slots inside it (set by Encode; nil for raw-file sections).
	Hash []uint64
}

// Sections lists the spans of the file with their item boundaries. With payload set, coordinates
// are those of the payload (file without header and map list) and those two spans are omitted.
func (f *File) Sections(w *Walked, payload bool) []Sec {
	mOff, mLen := f.mapSpan()
	conv := func(o int64) int {
		if !payload {
			return int(o)
		}
		o -= HeaderSize
		if o+HeaderSize >= mOff+mLen {
			o -= mLen
		}
		return int(o)
	}
	items := map[uint16][]int{}
	for _, it := range w.Items {
		items[it.Type] = append(items[it.Type], conv(it.Off))
	}
	for _, o := range w.StrOff {
		items[TypeStringData] = append(items[TypeStringData], conv(int64(o)))
	}
	var out []Sec
	for _, sp := range f.Spans {
		if sp.Len == 0 || payload && (sp.Type == TypeHeader || sp.Type == TypeMapList) {
			continue
		}
		sc := Sec{Type: sp.Type, Off: conv(sp.Off), End: conv(sp.Off+sp.Len-1) + 1, Items: items[sp.Type]}
		if rw := fixedSize(sp.Type); rw > 0 && sp.Type != TypeMapList {
			sc.Items = nil
			for o := int64(0); o < sp.Len; o += rw {
				sc.Items = append(sc.Items, conv(sp.Off+o))
			}
		}
		out = append(out, sc)
	}
	return out
}

// offCoder codes offset slots. The first reference to an item is the zigzag delta of its ordinal
// to the previous first-referenced ordinal of the same kind (shifted left, low bit 0); a later
// reference to a shared item is a 1, then the item's content class (a prefix of the hash of its
// masked bytes) and, when several items share that content, its occurrence number. Both sides
// build the classes from the masked payload, before any offset is resolved.
type offCoder struct {
	kinds [numOffKinds]*offKind
}

type offKind struct {
	list    []int64 // item starts of the target section, file order
	end     []int64 // item ends
	ord     map[int64]int
	seen    map[int]bool
	prevNew int
	cls     *prefixIndex
	clsOf   []int   // ordinal -> class number
	clsOrd  [][]int // class -> ordinals
}

func newOffCoder(f *File, w *Walked, masked []byte, k0 int) *offCoder {
	oc := &offCoder{}
	type tab struct {
		list, end []int64
		ord       map[int64]int
		cls       *prefixIndex
		clsOf     []int
		clsOrd    [][]int
	}
	byType := map[uint16]*tab{}
	for k := OffKind(1); k < numOffKinds; k++ {
		t := OffTarget[k]
		tb := byType[t]
		if tb == nil {
			tb = &tab{}
			for _, it := range w.Items {
				if it.Type == t {
					tb.list = append(tb.list, it.Off)
					tb.end = append(tb.end, it.Off+it.Len)
				}
			}
			if t == TypeStringData {
				for i, o := range w.StrOff {
					tb.list = append(tb.list, int64(o))
					tb.end = append(tb.end, int64(w.StrEnd[i]))
				}
			}
			tb.ord = make(map[int64]int, len(tb.list))
			for i, o := range tb.list {
				tb.ord[o] = i
			}
			var ids []Identity
			idx := map[Identity]int{}
			tb.clsOf = make([]int, len(tb.list))
			for i := range tb.list {
				id := hash('O', []byte{byte(t), byte(t >> 8)}, masked[tb.list[i]:tb.end[i]])
				c, ok := idx[id]
				if !ok {
					c = len(ids)
					idx[id] = c
					ids = append(ids, id)
					tb.clsOrd = append(tb.clsOrd, nil)
				}
				tb.clsOf[i] = c
				tb.clsOrd[c] = append(tb.clsOrd[c], i)
			}
			tb.cls = newPrefixIndex(ids, k0)
			byType[t] = tb
		}
		oc.kinds[k] = &offKind{list: tb.list, end: tb.end, ord: tb.ord, seen: map[int]bool{}, prevNew: -1, cls: tb.cls, clsOf: tb.clsOf, clsOrd: tb.clsOrd}
	}
	return oc
}

func (oc *offCoder) put(kind OffKind, pos int64) ([]byte, error) {
	ko := oc.kinds[kind]
	o, ok := ko.ord[pos]
	if !ok {
		return nil, bad("offset %d is not the start of a %#x item", pos, OffTarget[kind])
	}
	if !ko.seen[o] {
		ko.seen[o] = true
		d := o - ko.prevNew
		ko.prevNew = o
		return binary.AppendUvarint(nil, zigzag(int64(d))<<1), nil
	}
	b := binary.AppendUvarint(nil, 1)
	c := ko.clsOf[o]
	b, err := ko.cls.put(b, uint32(c))
	if err != nil {
		return nil, err
	}
	if n := len(ko.clsOrd[c]); n > 1 {
		for j, x := range ko.clsOrd[c] {
			if x == o {
				b = binary.AppendUvarint(b, uint64(j))
			}
		}
	}
	return b, nil
}

func (oc *offCoder) get(kind OffKind, b []byte) (int64, []byte, error) {
	ko := oc.kinds[kind]
	u, k := binary.Uvarint(b)
	if k <= 0 {
		return 0, nil, bad("offset stream truncated")
	}
	b = b[k:]
	var o int
	if u&1 == 0 {
		o = ko.prevNew + int(unzigzag(u>>1))
		if o < 0 || o >= len(ko.list) {
			return 0, nil, bad("offset ordinal %d outside %d items", o, len(ko.list))
		}
		ko.prevNew = o
		ko.seen[o] = true
	} else {
		if u != 1 || ko.cls == nil {
			return 0, nil, bad("offset stream symbol")
		}
		c, rest, err := ko.cls.get(b)
		if err != nil {
			return 0, nil, err
		}
		b = rest
		j := uint64(0)
		if len(ko.clsOrd[c]) > 1 {
			if j, k = binary.Uvarint(b); k <= 0 || j >= uint64(len(ko.clsOrd[c])) {
				return 0, nil, bad("offset occurrence")
			}
			b = b[k:]
		}
		o = ko.clsOrd[c][j]
	}
	return ko.list[o], b, nil
}
