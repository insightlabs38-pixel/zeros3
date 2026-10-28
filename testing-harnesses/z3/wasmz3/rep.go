package wasmz3

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	"zeros3-z3/idxref"
)

// Representation of one core module (the "indexed-reference" candidate):
//
//	Meta   magic, identity parameters, module size
//	Comps  groups of the *masked payload* (the module without its 8-byte header, every
//	       module-level index slot — type, function, table, memory, global, tag, element, data —
//	       overwritten by a zero LEB128 of the same width), each followed by eight reference
//	       streams, one per index space, holding the references of the slots that start inside the
//	       group as prefixes of the target's semantic identity.
//
// Identities come from native content, never from an ordinal: a type is its structure; an import
// is (module, name, kind, type); a defined function is (type, body with every reference zeroed)
// or, optionally, (type, name-section name); a global / table / memory / tag is its definition
// with references zeroed; a data segment is its mode, offset expression and bytes; an element
// segment adds the identities of the functions it lists. Section sizes, body sizes and vector
// counts are layout fields that stay in the payload (masking never changes a width, so they stay
// valid). Linear-memory address constants (padded i32.const) are not index references and stay
// raw.

const repMagic = "WSM1"

// Config selects the representation parameters.
type Config struct {
	K0       int  // identity prefix bytes, 1..4 (default 3)
	Auto     int  // >0: prefix length per candidate table (idxref.AutoK0 with this collision percentage)
	UseNames bool // a defined function with a name-section name is identified by (type, name)
	Cut      func(payload []byte, secs []idxref.Sec) []int
}

// NumStreams is the number of reference streams: one per index space.
const NumStreams = int(SpAddr) - 1

// Stats are the encoder-side measurements of one representation.
type Stats struct {
	Slots     [NumSpaces]int
	RefBytes  [NumSpaces]int
	Candidate [NumSpaces]int
	Dups      [NumSpaces]int // candidates that share their identity with an earlier one
	Payload   int
	Comps     int
	Opaque    int
}

// Rep is an encoded module.
type Rep struct {
	Meta  []byte
	Comps [][]byte
	Stats Stats
	Cuts  []int
	Secs  []idxref.Sec
	Refs  []idxref.RefRec
}

func spIdx(sp Space) int { return int(sp) - 1 }

type ctx struct {
	m     *Module
	d     []byte // the bytes slots are read from
	id    []byte // d with every slot (address constants included) zeroed: what identities hash
	cfg   Config
	ids   [NumSpaces][]idxref.Identity
	idx   [NumSpaces]*idxref.ClassIndex
	ready [NumSpaces]bool
}

func (c *ctx) k(n int) int {
	if c.cfg.Auto > 0 {
		return idxref.AutoK0(n, c.cfg.Auto)
	}
	return c.cfg.K0
}

func hash(tag byte, parts ...[]byte) idxref.Identity { return idxref.Hash(tag, parts...) }

func lp(b []byte) []byte { return append(binary.AppendUvarint(nil, uint64(len(b))), b...) }

// maskedCopy returns d with every slot (including address constants) zeroed.
func maskedCopy(m *Module, d []byte) []byte {
	out := append([]byte(nil), d...)
	for _, s := range m.Slots {
		maskLEB(out[s.Off : s.Off+int64(s.W)])
	}
	return out
}

func (c *ctx) typeIdentity(i int) idxref.Identity {
	t := &c.m.Types[i]
	return hash(0x71, lp(t.Params), lp(t.Results))
}

// buildTypes fills the type identities (no dependencies).
func (c *ctx) buildTypes() {
	c.ids[SpType] = make([]idxref.Identity, len(c.m.Types))
	for i := range c.m.Types {
		c.ids[SpType][i] = c.typeIdentity(i)
	}
	c.ready[SpType] = true
}

// typeOf is the identity of the type a (restored) type slot at value v names.
func (c *ctx) typeID(v uint32) (idxref.Identity, bool) {
	if int(v) >= len(c.ids[SpType]) {
		return idxref.Identity{}, false
	}
	return c.ids[SpType][v], true
}

// buildDefs fills the function / global / table / memory / tag / data identities. The type slots
// they read (function section, imports, tags) must be restored in c.d.
func (c *ctx) buildDefs() error {
	m := c.m
	var funcs, tables, mems, globals, tags []idxref.Identity
	for _, im := range m.Imports {
		desc := lp(im.Desc)
		switch im.Kind {
		case 0:
			t, ok := c.typeID(im.TypeIdx)
			if !ok {
				return bad("import type %d", im.TypeIdx)
			}
			funcs = append(funcs, hash(0x72, lp([]byte(im.Mod)), lp([]byte(im.Name)), t[:]))
		case 1:
			tables = append(tables, hash(0x73, lp([]byte(im.Mod)), lp([]byte(im.Name)), desc))
		case 2:
			mems = append(mems, hash(0x74, lp([]byte(im.Mod)), lp([]byte(im.Name)), desc))
		case 3:
			globals = append(globals, hash(0x75, lp([]byte(im.Mod)), lp([]byte(im.Name)), desc))
		case 4:
			t, ok := c.typeID(im.TypeIdx)
			if !ok {
				return bad("import tag type %d", im.TypeIdx)
			}
			tags = append(tags, hash(0x76, lp([]byte(im.Mod)), lp([]byte(im.Name)), t[:]))
		}
	}
	nimp := m.NImport[0]
	for j, tv := range m.Funcs {
		t, ok := c.typeID(tv)
		if !ok {
			return bad("function type %d", tv)
		}
		b := m.Bodies[j]
		if c.cfg.UseNames {
			if nm, ok := m.Names[uint32(nimp+j)]; ok {
				funcs = append(funcs, hash(0x77, t[:], lp([]byte(nm))))
				continue
			}
		}
		funcs = append(funcs, hash(0x78, t[:], lp(c.id[b.Start:b.End])))
	}
	for _, r := range m.Tables {
		tables = append(tables, hash(0x79, lp(c.id[r.Off:r.End])))
	}
	for _, r := range m.Mems {
		mems = append(mems, hash(0x7a, lp(c.id[r.Off:r.End])))
	}
	for _, g := range m.Globals {
		globals = append(globals, hash(0x7b, lp(c.id[g.Off:g.End])))
	}
	for _, tv := range m.Tags {
		t, ok := c.typeID(tv)
		if !ok {
			return bad("tag type %d", tv)
		}
		tags = append(tags, hash(0x7c, t[:]))
	}
	var datas []idxref.Identity
	for _, s := range m.Datas {
		datas = append(datas, hash(0x7d, lp(c.id[s.Off:s.DataOff]), lp(c.d[s.DataOff:s.DataOff+s.DataLen])))
	}
	c.ids[SpFunc], c.ids[SpTable], c.ids[SpMem], c.ids[SpGlobal], c.ids[SpTag], c.ids[SpData] = funcs, tables, mems, globals, tags, datas
	for _, sp := range []Space{SpFunc, SpTable, SpMem, SpGlobal, SpTag, SpData} {
		c.ready[sp] = true
	}
	return nil
}

// buildElems fills the element identities; the function slots they list must be restored.
func (c *ctx) buildElems() error {
	m := c.m
	var out []idxref.Identity
	si := 0
	for _, e := range m.Elems {
		for si < len(m.Slots) && m.Slots[si].Off < e.Off {
			si++
		}
		var tgt []byte
		for j := si; j < len(m.Slots) && m.Slots[j].Off < e.End; j++ {
			s := &m.Slots[j]
			if s.Sp == SpFunc || s.Sp == SpGlobal {
				arr := c.ids[s.Sp]
				v := c.val(s)
				if int(v) >= len(arr) {
					return bad("element entry %d", v)
				}
				tgt = append(tgt, arr[v][:]...)
			}
		}
		out = append(out, hash(0x7e, lp(c.id[e.Off:e.End]), tgt))
	}
	c.ids[SpElem] = out
	c.ready[SpElem] = true
	return nil
}

// val reads the number stored at a slot of c.d.
func (c *ctx) val(s *Slot) uint32 {
	v, _, _ := leb(c.d, int(s.Off), int(s.W))
	return uint32(v)
}

func (c *ctx) index(sp Space) (*idxref.ClassIndex, error) {
	if c.idx[sp] != nil {
		return c.idx[sp], nil
	}
	if !c.ready[sp] {
		return nil, unsup("index space %s not ready", SpaceName[sp])
	}
	n := len(c.ids[sp])
	h := make([]int, n)
	for i := range h {
		h[i] = i
	}
	c.idx[sp] = idxref.NewClassIndex(h, c.ids[sp], c.k(n))
	return c.idx[sp], nil
}

// Encode builds the representation of data (one module in memory). The result is verified by a
// full Decode before it is returned; a mismatch is ErrUnsupported.
func Encode(m *Module, data []byte, cfg Config) (*Rep, error) {
	if cfg.K0 == 0 {
		cfg.K0 = 3
	}
	if cfg.K0 < 1 || cfg.K0 > 4 {
		return nil, fmt.Errorf("wasmz3: K0 %d", cfg.K0)
	}
	c := &ctx{m: m, d: data, id: maskedCopy(m, data), cfg: cfg}
	c.buildTypes()
	if err := c.buildDefs(); err != nil {
		return nil, err
	}
	if err := c.buildElems(); err != nil {
		return nil, err
	}
	var st Stats
	st.Opaque = m.Opaque
	for sp := SpType; sp < SpAddr; sp++ {
		ix, err := c.index(sp)
		if err != nil {
			return nil, err
		}
		st.Candidate[sp] = len(c.ids[sp])
		st.Dups[sp] = ix.Duplicates()
	}
	type item struct {
		pos    int
		stream uint8
		b      []byte
		sp     Space
	}
	var items []item
	masked := append([]byte(nil), data...)
	for i := range m.Slots {
		s := &m.Slots[i]
		if s.Sp == SpAddr {
			continue
		}
		ix, _ := c.index(s.Sp)
		if int(s.Val) >= len(c.ids[s.Sp]) {
			return nil, bad("slot at %d names %s %d of %d", s.Off, SpaceName[s.Sp], s.Val, len(c.ids[s.Sp]))
		}
		b, err := ix.Put(nil, int(s.Val))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
		}
		items = append(items, item{int(s.Off) - HeaderSize, uint8(spIdx(s.Sp)), b, s.Sp})
		st.Slots[s.Sp]++
		st.RefBytes[s.Sp] += len(b)
		maskLEB(masked[s.Off : s.Off+int64(s.W)])
	}
	payload := masked[HeaderSize:]
	st.Payload = len(payload)
	secs := payloadSecs(m)
	ri := 0
	for si := range secs {
		sc := &secs[si]
		sc.Hash = make([]uint64, len(sc.Items))
		for k, a := range sc.Items {
			end := sc.End
			if k+1 < len(sc.Items) {
				end = sc.Items[k+1]
			}
			h := idxref.FNV64(idxref.FNVInit, payload[a:end])
			for ri < len(items) && items[ri].pos < a {
				ri++
			}
			for j := ri; j < len(items) && items[j].pos < end; j++ {
				h = idxref.FNV64(h^uint64(items[j].stream), items[j].b)
			}
			sc.Hash[k] = h
		}
	}
	var cuts []int
	if cfg.Cut != nil {
		cuts = cfg.Cut(payload, secs)
	}
	if len(cuts) == 0 || cuts[len(cuts)-1] != len(payload) {
		cuts = append(cuts, len(payload))
	}
	rep := &Rep{Stats: st, Cuts: cuts, Secs: secs}
	ri, start := 0, 0
	for _, end := range cuts {
		if end < start || end > len(payload) {
			return nil, fmt.Errorf("wasmz3: bad cut %d", end)
		}
		var streams [NumStreams][]byte
		for ri < len(items) && items[ri].pos < end {
			streams[items[ri].stream] = append(streams[items[ri].stream], items[ri].b...)
			rep.Refs = append(rep.Refs, idxref.RefRec{Comp: len(rep.Comps), Stream: items[ri].stream, Class: uint8(items[ri].sp), B: items[ri].b})
			ri++
		}
		rep.Comps = append(rep.Comps, idxref.PackComp(payload[start:end], streams[:]))
		start = end
	}
	rep.Stats.Comps = len(rep.Comps)
	var fl byte
	if cfg.UseNames {
		fl = 1
	}
	rep.Meta = binary.AppendUvarint(append([]byte(repMagic), byte(cfg.K0), byte(cfg.Auto), fl), uint64(len(data)))
	got, err := Decode(rep.Meta, rep.Comps)
	if err != nil {
		return nil, fmt.Errorf("%w: self-check decode: %v", ErrUnsupported, err)
	}
	if !bytes.Equal(got, data) {
		return nil, fmt.Errorf("%w: self-check mismatch", ErrUnsupported)
	}
	return rep, nil
}

// payloadSecs describes the payload (module minus its header) for the cutters.
func payloadSecs(m *Module) []idxref.Sec {
	raw := RawSecs(m, nil)
	var out []idxref.Sec
	for _, s := range raw {
		if s.Type == secHeader {
			continue
		}
		s.Off -= HeaderSize
		s.End -= HeaderSize
		for i := range s.Items {
			s.Items[i] -= HeaderSize
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Off < out[j].Off })
	return out
}
