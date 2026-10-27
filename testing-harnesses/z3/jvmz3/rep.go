package jvmz3

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"zeros3-z3/idxref"
)

// Representation of one class file:
//
//	Meta   magic, K0                                                   explicit recipe
//	Comps  groups of the masked class bytes (every constant-pool index slot overwritten: 0, or 1
//	       when zero means "absent"), each followed by the reference bytes of the slots starting
//	       inside it, one stream per target space: a prefix (K0 bytes + implied extra) of the
//	       semantic identity of the constant-pool entry the slot named
//
// Identity: tag + literal bytes + identities of referenced entries (see entryIdent). Decode walks
// the masked bytes (attribute names are resolved on the fly because they select the parser), then
// resolves spaces in dependency order: leaf literals, Utf8 < {Class, NameAndType, ...} <
// {Fieldref, Methodref, ...} < loadable constants. The constant-pool order itself is exact payload.

const (
	repMagic  = "JVN1"
	numStream = int(numSpaces) - 1 // one stream per space, SpUtf8..SpDouble
)

// Config selects the representation parameters.
type Config struct {
	K0 int // identity prefix bytes, 1..4 (default 2)
	// Cut returns the exclusive end offsets of the component groups; secs[0] is the header and
	// constant pool (records are entries), secs[1] the class body (records are the header, fields,
	// methods and class attributes). nil: one component.
	Cut func(payload []byte, secs []idxref.Sec) []int
}

// Stats are encoder-side measurements.
type Stats struct {
	Slots      [numSpaces]int
	StreamLen  [numStream]int
	Collisions int
	Duplicates int // constant-pool entries identical to an earlier one
	Comps      int
}

// Rep is an encoded class file.
type Rep struct {
	Meta  []byte
	Comps [][]byte
	Stats Stats
	Cuts  []int
	Secs  []idxref.Sec
}

func spIdx(sp Space) int { return int(sp) - 1 }

// entryIdents holds the lazily computed identity of every constant-pool entry.
type entryIdents struct {
	b    []byte
	c    *File
	id   []idxref.Identity
	have []bool
	busy []bool
}

func newIdents(b []byte, c *File) *entryIdents {
	return &entryIdents{b: b, c: c, id: make([]idxref.Identity, c.CPCount), have: make([]bool, c.CPCount), busy: make([]bool, c.CPCount)}
}

func (e *entryIdents) ref(at int64, want Space) (idxref.Identity, error) {
	i := int(binary.BigEndian.Uint16(e.b[at:]))
	if i == 0 || i >= e.c.CPCount || e.c.CP[i].Tag == 0 || !want.accepts(e.c.CP[i].Tag) {
		return idxref.Identity{}, bad("constant pool reference %d at %d is not a %s entry", i, at, want)
	}
	return e.get(i)
}

func (e *entryIdents) get(i int) (idxref.Identity, error) {
	if e.have[i] {
		return e.id[i], nil
	}
	if e.busy[i] {
		return idxref.Identity{}, bad("constant pool cycle at %d", i)
	}
	e.busy[i] = true
	defer func() { e.busy[i] = false }()
	en := e.c.CP[i]
	p := en.Off + 1
	body := e.b[en.Off+1 : en.End]
	var id idxref.Identity
	switch en.Tag {
	case TagUtf8:
		id = idxref.Hash('U', e.b[en.Off+3:en.End])
	case TagInteger:
		id = idxref.Hash('I', body)
	case TagFloat:
		id = idxref.Hash('F', body)
	case TagLong:
		id = idxref.Hash('J', body)
	case TagDouble:
		id = idxref.Hash('D', body)
	case TagClass, TagString, TagMethodType, TagModule, TagPackage:
		a, err := e.ref(p, SpUtf8)
		if err != nil {
			return id, err
		}
		id = idxref.Hash(map[byte]byte{TagClass: 'C', TagString: 'S', TagMethodType: 'T', TagModule: 'M', TagPackage: 'P'}[en.Tag], a[:])
	case TagNameAndType:
		a, err := e.ref(p, SpUtf8)
		if err != nil {
			return id, err
		}
		d, err := e.ref(p+2, SpUtf8)
		if err != nil {
			return id, err
		}
		id = idxref.Hash('N', a[:], d[:])
	case TagFieldref, TagMethodref, TagInterfaceMethodref:
		a, err := e.ref(p, SpClass)
		if err != nil {
			return id, err
		}
		n, err := e.ref(p+2, SpNat)
		if err != nil {
			return id, err
		}
		id = idxref.Hash(map[byte]byte{TagFieldref: 'f', TagMethodref: 'm', TagInterfaceMethodref: 'i'}[en.Tag], a[:], n[:])
	case TagDynamic, TagInvokeDynamic:
		n, err := e.ref(p+2, SpNat)
		if err != nil {
			return id, err
		}
		id = idxref.Hash(map[byte]byte{TagDynamic: 'd', TagInvokeDynamic: 'y'}[en.Tag], e.b[p:p+2], n[:])
	case TagMethodHandle:
		r, err := e.ref(p+1, SpMember)
		if err != nil {
			return id, err
		}
		id = idxref.Hash('h', e.b[p:p+1], r[:])
	default:
		return id, bad("tag %d", en.Tag)
	}
	e.id[i], e.have[i] = id, true
	return id, nil
}

// table builds the reference coder of a space: the entries it can name, in pool order.
func (e *entryIdents) table(sp Space, k0 int) (*idxref.ClassIndex, error) {
	var idx []int
	var ids []idxref.Identity
	for i := 1; i < e.c.CPCount; i++ {
		if e.c.CP[i].Tag != 0 && sp.accepts(e.c.CP[i].Tag) {
			id, err := e.get(i)
			if err != nil {
				return nil, err
			}
			idx = append(idx, i)
			ids = append(ids, id)
		}
	}
	return idxref.NewClassIndex(idx, ids, k0), nil
}

// Encode builds the representation of the class bytes b and verifies it with a full Decode.
func Encode(c *File, b []byte, cfg Config) (*Rep, error) {
	k0 := cfg.K0
	if k0 == 0 {
		k0 = 2
	}
	if k0 < 1 || k0 > 4 {
		return nil, fmt.Errorf("jvmz3: K0 %d", k0)
	}
	ids := newIdents(b, c)
	var st Stats
	var tabs [numSpaces]*idxref.ClassIndex
	for sp := SpUtf8; sp < numSpaces; sp++ {
		if sp == SpAttr {
			tabs[sp] = tabs[SpUtf8]
			continue
		}
		t, err := ids.table(sp, k0)
		if err != nil {
			return nil, err
		}
		st.Collisions += t.Collisions()
		st.Duplicates += t.Duplicates()
		tabs[sp] = t
	}
	// every entry's identity must be computable (this also rejects reference cycles)
	for i := 1; i < c.CPCount; i++ {
		if c.CP[i].Tag != 0 {
			if _, err := ids.get(i); err != nil {
				return nil, err
			}
		}
	}
	masked := append([]byte(nil), b...)
	type ref struct {
		pos int
		st  int
		b   []byte
	}
	refs := make([]ref, 0, len(c.Slots))
	for i := range c.Slots {
		s := &c.Slots[i]
		MaskSlot(masked, s)
		rb, err := tabs[s.Space].Put(nil, int(s.Val))
		if err != nil {
			return nil, err
		}
		st.Slots[s.Space]++
		st.StreamLen[spIdx(s.Space)] += len(rb)
		refs = append(refs, ref{int(s.Off), spIdx(s.Space), rb})
	}
	// sections and record hashes for cutters
	secs := []idxref.Sec{{Type: 1, Off: 0, End: int(c.CPEnd)}, {Type: 2, Off: int(c.CPEnd), End: len(b)}}
	secs[0].Items = append(secs[0].Items, 0)
	for i := 1; i < c.CPCount; i++ {
		if c.CP[i].Tag != 0 && i > 1 {
			secs[0].Items = append(secs[0].Items, int(c.CP[i].Off))
		}
	}
	for _, o := range c.Bounds {
		secs[1].Items = append(secs[1].Items, int(o))
	}
	ri := 0
	for si := range secs {
		sc := &secs[si]
		sc.Hash = make([]uint64, len(sc.Items))
		for i, stt := range sc.Items {
			end := sc.End
			if i+1 < len(sc.Items) {
				end = sc.Items[i+1]
			}
			h := idxref.FNV64(idxref.FNVInit, masked[stt:end])
			for ri < len(refs) && refs[ri].pos < stt {
				ri++
			}
			for j := ri; j < len(refs) && refs[j].pos < end; j++ {
				h = idxref.FNV64(h^uint64(refs[j].st), refs[j].b)
			}
			sc.Hash[i] = h
		}
	}
	var cuts []int
	if cfg.Cut != nil {
		cuts = cfg.Cut(masked, secs)
	}
	if len(cuts) == 0 || cuts[len(cuts)-1] != len(masked) {
		cuts = append(cuts, len(masked))
	}
	rep := &Rep{Stats: st, Cuts: cuts, Secs: secs}
	ri, start := 0, 0
	for _, end := range cuts {
		if end < start || end > len(masked) {
			return nil, fmt.Errorf("jvmz3: bad cut %d", end)
		}
		streams := make([][]byte, numStream)
		for ri < len(refs) && refs[ri].pos < end {
			streams[refs[ri].st] = append(streams[refs[ri].st], refs[ri].b...)
			ri++
		}
		rep.Comps = append(rep.Comps, idxref.PackComp(masked[start:end], streams))
		start = end
	}
	rep.Stats.Comps = len(rep.Comps)
	rep.Meta = append([]byte(repMagic), byte(k0))
	got, err := Decode(rep.Meta, rep.Comps)
	if err != nil {
		return nil, fmt.Errorf("%w: self-check decode: %v", ErrUnsupported, err)
	}
	if !bytes.Equal(got, b) {
		return nil, fmt.Errorf("%w: self-check mismatch", ErrUnsupported)
	}
	return rep, nil
}

func idxErr(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrFormat, err)
}

// Decode rebuilds the class file from Meta and the component bytes (in order).
func Decode(meta []byte, comps [][]byte) ([]byte, error) {
	if len(meta) != len(repMagic)+1 || string(meta[:4]) != repMagic {
		return nil, bad("representation header")
	}
	k0 := int(meta[4])
	if k0 < 1 || k0 > 4 {
		return nil, bad("K0 %d", k0)
	}
	var buf []byte
	streams := make([][]byte, numStream)
	for _, c := range comps {
		p, ss, err := idxref.SplitComp(c, numStream)
		if err != nil {
			return nil, idxErr(err)
		}
		buf = append(buf, p...)
		for i := range ss {
			streams[i] = append(streams[i], ss[i]...)
		}
	}
	if len(buf) < 24 || len(buf) > 1<<28 {
		return nil, bad("payload length %d", len(buf))
	}
	var ids *entryIdents
	var attrTab *idxref.ClassIndex
	attrStream := streams[spIdx(SpAttr)]
	prep := func(c *File) error {
		ids = newIdents(buf, c)
		t, err := ids.table(SpUtf8, k0)
		attrTab = t
		return err
	}
	res := func(s *Slot) error {
		v, rest, err := attrTab.Get(attrStream)
		if err != nil {
			return err
		}
		attrStream = rest
		return putSlot(buf, s, uint32(v))
	}
	c, err := walkWith(buf, Limits{}.norm(), true, res, prep)
	if err != nil {
		return nil, err
	}
	if len(attrStream) != 0 {
		return nil, bad("trailing attribute-name bytes")
	}
	// the attribute-name slots were resolved during the walk; their value is in the buffer
	for i := range c.Slots {
		s := &c.Slots[i]
		if s.Space == SpAttr {
			s.Val = binary.BigEndian.Uint16(buf[s.Off:])
		}
	}
	levels := [][]Space{
		{SpUtf8, SpInt, SpLong, SpFloat, SpDouble},
		{SpClass, SpNat, SpModule, SpPackage},
		{SpField, SpMethod, SpMember, SpInvDyn},
		{SpLoad},
	}
	for _, lv := range levels {
		for _, sp := range lv {
			tab, err := ids.table(sp, k0)
			if err != nil {
				return nil, err
			}
			b := streams[spIdx(sp)]
			for i := range c.Slots {
				s := &c.Slots[i]
				if s.Space != sp {
					continue
				}
				v, rest, err := tab.Get(b)
				if err != nil {
					return nil, idxErr(err)
				}
				b = rest
				if err := putSlot(buf, s, uint32(v)); err != nil {
					return nil, err
				}
			}
			if len(b) != 0 {
				return nil, bad("trailing %s reference bytes", sp)
			}
		}
	}
	return buf, nil
}

func putSlot(b []byte, s *Slot, v uint32) error {
	if s.Len == 1 {
		if v > 0xFF {
			return bad("index %d does not fit ldc", v)
		}
		b[s.Off] = byte(v)
		return nil
	}
	if v > 0xFFFF {
		return bad("index %d", v)
	}
	binary.BigEndian.PutUint16(b[s.Off:], uint16(v))
	return nil
}
