package parquetz3

import (
	"encoding/binary"
	"math/bits"
)

// MaxPageValues bounds the level records of one page (a hostile num_values never sizes an
// allocation beyond it).
const MaxPageValues = 1 << 24

// colSpec is what page coding needs to know about a flat (non-repeated) column.
type colSpec struct {
	Phys, TypeLen, MaxDef int
}

// width is the byte width of one canonical record for fixed-size types (a BOOLEAN is one
// byte, 0 or 1); 0 for BYTE_ARRAY, whose records are uint32 length + bytes (the PLAIN form).
func (c colSpec) width() int {
	switch c.Phys {
	case TBoolean:
		return 1
	case TInt32, TFloat:
		return 4
	case TInt64, TDouble:
		return 8
	case TInt96:
		return 12
	case TFixedLen:
		return c.TypeLen
	}
	return 0
}

func (c colSpec) levelBW() int { return bits.Len(uint(c.MaxDef)) }

// recs is a list of canonical value records.
type recs struct {
	fixed int
	buf   []byte
	ends  []int // end offset of every record when fixed == 0
	n     int
}

func newRecs(c colSpec) recs { return recs{fixed: c.width()} }

func (r *recs) at(i int) []byte {
	if r.fixed > 0 {
		return r.buf[i*r.fixed : (i+1)*r.fixed]
	}
	s := 0
	if i > 0 {
		s = r.ends[i-1]
	}
	return r.buf[s:r.ends[i]]
}

func (r *recs) add(b []byte) {
	r.buf = append(r.buf, b...)
	if r.fixed == 0 {
		r.ends = append(r.ends, len(r.buf))
	}
	r.n++
}

// decodePlain decodes n PLAIN values from b; used is the bytes consumed.
func decodePlain(c colSpec, b []byte, n int, lim Limits) (r recs, used int, err error) {
	r = newRecs(c)
	switch {
	case c.Phys == TBoolean:
		need := (n + 7) / 8
		if need > len(b) {
			return r, 0, corrupt("boolean page of %d values in %d bytes", n, len(b))
		}
		r.buf = make([]byte, n)
		for i := 0; i < n; i++ {
			r.buf[i] = b[i>>3] >> uint(i&7) & 1
		}
		r.n = n
		return r, need, nil
	case r.fixed > 0:
		if n > len(b)/r.fixed {
			return r, 0, corrupt("%d values of %d bytes in %d bytes", n, r.fixed, len(b))
		}
		r.buf = b[:n*r.fixed]
		r.n = n
		return r, n * r.fixed, nil
	}
	pos := 0
	for i := 0; i < n; i++ {
		if len(b)-pos < 4 {
			return r, 0, corrupt("byte array length")
		}
		l := int(binary.LittleEndian.Uint32(b[pos:]))
		if l < 0 || l > len(b)-pos-4 {
			return r, 0, corrupt("byte array of %d bytes in %d", l, len(b)-pos-4)
		}
		if l > lim.MaxValue {
			return r, 0, unsupported("value of %d bytes exceeds the logical limit %d", l, lim.MaxValue)
		}
		pos += 4 + l
		r.ends = append(r.ends, pos)
	}
	r.buf = b[:pos]
	r.n = n
	return r, pos, nil
}

// encodePlain is the PLAIN form of r.
func encodePlain(c colSpec, r *recs) []byte {
	if c.Phys != TBoolean {
		return r.buf
	}
	out := make([]byte, (r.n+7)/8)
	for i := 0; i < r.n; i++ {
		if r.buf[i] != 0 {
			out[i>>3] |= 1 << uint(i&7)
		}
	}
	return out
}

// dict is a decoded dictionary page.
type dict struct {
	vals recs
	idx  map[string]uint32
	dup  bool // two entries are equal: indices cannot be recomputed from values
}

func newDict(r recs) *dict {
	d := &dict{vals: r, idx: make(map[string]uint32, r.n)}
	for i := 0; i < r.n; i++ {
		k := string(r.at(i))
		if _, ok := d.idx[k]; ok {
			d.dup = true
		}
		d.idx[k] = uint32(i)
	}
	return d
}

func (d *dict) indexWidth() int {
	if d.vals.n <= 1 {
		return 0
	}
	return bits.Len(uint(d.vals.n - 1))
}

// layout is the page-layout recipe of a data page: which hybrid streams follow the
// standard encoder and which carry their stored run headers.
type layout struct {
	LvlExpl bool
	LvlHdrs []uint64
	LvlPad  []uint32
	ValExpl bool
	// ValWidth: the standard run encoder with a stored index width (a writer that sizes the
	// width from the dictionary as it stood when the page was cut, not from the final one).
	ValWidth bool
	ValBW    int
	ValHdrs  []uint64
	ValPad   []uint32
}

// Explicit reports which hybrid streams of the page carry stored run headers (the rest
// follow the standard encoder).
func (l layout) Explicit() (levels, values bool) { return l.LvlExpl, l.ValExpl }

// WidthOnly reports a standard run encoding whose index width is stored (a tiny recipe).
func (l layout) WidthOnly() bool { return l.ValWidth }

// pageContent is the logical content of a data page.
type pageContent struct {
	n       int // level records (the header's num_values)
	defs    []uint8
	present int
	vals    recs
}

// encodingKind classifies how a page encoding stores values.
func dictEnc(e int32) bool { return e == EncPlainDict || e == EncRLEDict }

// pageSupported reports whether a data/dictionary page encoding of a column is handled.
func encodingSupported(c colSpec, enc int32) bool {
	switch enc {
	case EncPlain, EncPlainDict, EncRLEDict:
		return true
	case EncRLE:
		return c.Phys == TBoolean
	case EncByteStrSplt:
		return c.Phys != TBoolean && c.Phys != TByteArr
	}
	return false
}

func defsToU32(d []uint8) []uint32 {
	o := make([]uint32, len(d))
	for i, v := range d {
		o[i] = uint32(v)
	}
	return o
}

// hybridPart encodes values with the layout part (standard unless explicit).
func hybridPart(vals []uint32, bw int, expl bool, hdrs []uint64, pad []uint32) ([]byte, bool) {
	if expl {
		return hybridEncodeRuns(vals, bw, hdrs, pad)
	}
	return hybridEncodeStd(vals, bw), true
}

// encodeLevels returns the definition-level bytes of a page (nil without levels): a v1
// page prefixes the hybrid stream with its uint32 length, a v2 page stores it raw.
func encodeLevels(pg *Page, c colSpec, defs []uint8, lay layout) ([]byte, bool) {
	if c.MaxDef == 0 {
		return nil, true
	}
	h, ok := hybridPart(defsToU32(defs), c.levelBW(), lay.LvlExpl, lay.LvlHdrs, lay.LvlPad)
	if !ok {
		return nil, false
	}
	if pg.Type == PageDataV2 {
		return h, true
	}
	return append(binary.LittleEndian.AppendUint32(make([]byte, 0, 4+len(h)), uint32(len(h))), h...), true
}

// encodeValues returns the value section of a data page.
func encodeValues(pg *Page, c colSpec, d *dict, pc *pageContent, lay layout) ([]byte, bool) {
	switch {
	case pg.Enc == EncPlain:
		return encodePlain(c, &pc.vals), true
	case dictEnc(pg.Enc):
		if d == nil || d.dup {
			return nil, false
		}
		idx := make([]uint32, pc.vals.n)
		for i := range idx {
			v, ok := d.idx[string(pc.vals.at(i))]
			if !ok {
				return nil, false
			}
			idx[i] = v
		}
		bw := d.indexWidth()
		if lay.ValExpl || lay.ValWidth {
			bw = lay.ValBW
		}
		if bw > 32 {
			return nil, false
		}
		h, ok := hybridPart(idx, bw, lay.ValExpl, lay.ValHdrs, lay.ValPad)
		return append([]byte{byte(bw)}, h...), ok
	case pg.Enc == EncRLE && c.Phys == TBoolean:
		idx := make([]uint32, pc.vals.n)
		for i := range idx {
			idx[i] = uint32(pc.vals.buf[i] & 1)
		}
		h, ok := hybridPart(idx, 1, lay.ValExpl, lay.ValHdrs, lay.ValPad)
		return append(binary.LittleEndian.AppendUint32(nil, uint32(len(h))), h...), ok
	case pg.Enc == EncByteStrSplt:
		w := c.width()
		if w == 0 || c.Phys == TBoolean {
			return nil, false
		}
		out := make([]byte, len(pc.vals.buf))
		n := pc.vals.n
		for i := 0; i < n; i++ {
			for j := 0; j < w; j++ {
				out[j*n+i] = pc.vals.buf[i*w+j]
			}
		}
		return out, true
	}
	return nil, false
}

// decodePage decodes the logical content of a data page from its uncompressed level
// bytes and value section (for v1 pages levels holds the prefixed hybrid stream at the
// start of the body and values the rest), recording the layout the page uses.
func decodePage(pg *Page, c colSpec, d *dict, levels, values []byte, lim Limits) (pc pageContent, lay layout, err error) {
	n := int(pg.NumValues)
	if n > MaxPageValues {
		return pc, lay, limitErr("page of %d values", n)
	}
	pc.n = n
	pc.present = n
	if c.MaxDef > 0 {
		var hb []byte
		if pg.Type == PageDataV2 {
			hb = levels
		} else {
			if len(levels) < 4 {
				return pc, lay, corrupt("level length prefix")
			}
			hb = levels[4:]
		}
		dv, hdrs, pad, used, err := hybridDecode(hb, c.levelBW(), n)
		if err != nil {
			return pc, lay, err
		}
		if used != len(hb) {
			return pc, lay, corrupt("%d trailing level bytes", len(hb)-used)
		}
		lay.LvlHdrs, lay.LvlPad = hdrs, pad
		pc.defs = make([]uint8, n)
		pc.present = 0
		for i, v := range dv {
			if int(v) > c.MaxDef {
				return pc, lay, corrupt("definition level %d above %d", v, c.MaxDef)
			}
			pc.defs[i] = uint8(v)
			if int(v) == c.MaxDef {
				pc.present++
			}
		}
	}
	switch {
	case pg.Enc == EncPlain:
		r, used, err := decodePlain(c, values, pc.present, lim)
		if err != nil {
			return pc, lay, err
		}
		if used != len(values) {
			return pc, lay, corrupt("%d trailing value bytes", len(values)-used)
		}
		pc.vals = r
	case dictEnc(pg.Enc):
		if d == nil {
			return pc, lay, corrupt("dictionary-encoded page without a dictionary")
		}
		if len(values) < 1 {
			return pc, lay, corrupt("dictionary index width")
		}
		bw := int(values[0])
		idx, hdrs, pad, used, err := hybridDecode(values[1:], bw, pc.present)
		if err != nil {
			return pc, lay, err
		}
		if used+1 != len(values) {
			return pc, lay, corrupt("%d trailing index bytes", len(values)-1-used)
		}
		lay.ValBW, lay.ValHdrs, lay.ValPad = bw, hdrs, pad
		pc.vals = newRecs(c)
		for _, ix := range idx {
			if int(ix) >= d.vals.n {
				return pc, lay, corrupt("dictionary index %d of %d", ix, d.vals.n)
			}
			pc.vals.add(d.vals.at(int(ix)))
		}
	case pg.Enc == EncRLE && c.Phys == TBoolean:
		if len(values) < 4 {
			return pc, lay, corrupt("RLE boolean length")
		}
		l := int(binary.LittleEndian.Uint32(values))
		if l < 0 || l > len(values)-4 || l != len(values)-4 {
			return pc, lay, corrupt("RLE boolean length %d in %d bytes", l, len(values)-4)
		}
		idx, hdrs, pad, used, err := hybridDecode(values[4:], 1, pc.present)
		if err != nil {
			return pc, lay, err
		}
		if used != l {
			return pc, lay, corrupt("%d trailing RLE boolean bytes", l-used)
		}
		lay.ValBW, lay.ValHdrs, lay.ValPad = 1, hdrs, pad
		pc.vals = newRecs(c)
		pc.vals.buf = make([]byte, len(idx))
		for i, v := range idx {
			pc.vals.buf[i] = byte(v)
		}
		pc.vals.n = len(idx)
	case pg.Enc == EncByteStrSplt:
		w := c.width()
		if w == 0 || c.Phys == TBoolean || len(values) != w*pc.present {
			return pc, lay, corrupt("byte-stream-split page of %d bytes for %d values", len(values), pc.present)
		}
		pc.vals = newRecs(c)
		pc.vals.buf = make([]byte, len(values))
		for i := 0; i < pc.present; i++ {
			for j := 0; j < w; j++ {
				pc.vals.buf[i*w+j] = values[j*pc.present+i]
			}
		}
		pc.vals.n = pc.present
	default:
		return pc, lay, unsupported("page encoding %d for physical type %d", pg.Enc, c.Phys)
	}
	return pc, lay, nil
}

// runState builds the canonical level stream: one record per run of equal definition
// levels, uvarint(run length << 4 | level), so any record can be read without its
// neighbours (MaxDef is capped at 8 for logical columns). A run may span pages, so the
// state lives per column.
type runState struct {
	init       bool
	level, run int
}

func (st *runState) emit(fn func(rec []byte, items int) error) error {
	var tmp [10]byte
	return fn(binary.AppendUvarint(tmp[:0], uint64(st.run)<<4|uint64(st.level)), st.run)
}

// add feeds one definition level; a closed run is passed to fn.
func (st *runState) add(lv int, fn func(rec []byte, items int) error) error {
	if !st.init {
		st.init, st.level, st.run = true, lv, 0
	}
	if lv != st.level {
		if err := st.emit(fn); err != nil {
			return err
		}
		st.level, st.run = lv, 0
	}
	st.run++
	return nil
}

// finish flushes the last run.
func (st *runState) finish(fn func(rec []byte, items int) error) error {
	if !st.init {
		return nil
	}
	st.init = false
	return st.emit(fn)
}
