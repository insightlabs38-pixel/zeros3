package parquetz3

import (
	"errors"
	"io"
)

// levelReader expands a column's level-run stream into definition levels.
type levelReader struct {
	cur    *cursor
	level  uint8
	remain uint64
	maxDef int
}

func (lr *levelReader) seek(pos uint64) error {
	skip, err := lr.cur.seek(pos)
	if err != nil {
		return err
	}
	lr.remain = 0
	rec, items, err := lr.cur.next()
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	v, _ := readUv(rec)
	lr.level, lr.remain = uint8(v&15), items-skip
	return nil
}

func (lr *levelReader) read(n int) ([]uint8, error) {
	out := make([]uint8, 0, n)
	for len(out) < n {
		if lr.remain == 0 {
			rec, items, err := lr.cur.next()
			if errors.Is(err, io.EOF) {
				return nil, recipeErr("the level stream ends before the page does")
			}
			if err != nil {
				return nil, err
			}
			v, _ := readUv(rec)
			if int(v&15) > lr.maxDef {
				return nil, recipeErr("definition level %d above %d", v&15, lr.maxDef)
			}
			lr.level, lr.remain = uint8(v&15), items
			continue
		}
		take := min(lr.remain, uint64(n-len(out)))
		for i := uint64(0); i < take; i++ {
			out = append(out, lr.level)
		}
		lr.remain -= take
	}
	return out, nil
}

type colState struct {
	spec  colSpec
	v     *cursor
	l     *levelReader
	vnext uint64 // position the value cursor is at (valid when v != nil)
	lnext uint64
	// the dictionary of the chunk being rebuilt; an older one is dropped when the next
	// dictionary page of the column is reached, so memory follows one chunk, not the file
	dictSeg int
	dict    *dict
}

type decoder struct {
	rec   *Recipe
	get   Getter
	lim   Limits
	stats RangeStats
	cols  []colState
	cp    *Compressor
}

func newDecoder(rec *Recipe, get Getter, lim Limits) *decoder {
	d := &decoder{rec: rec, lim: lim.norm(), cp: NewCompressor(CodecNone, lim)}
	d.get = get
	d.cols = make([]colState, len(rec.Cols))
	for i := range rec.Cols {
		c := &rec.Cols[i]
		d.cols[i].spec = colSpec{Phys: c.Phys, TypeLen: c.TypeLen, MaxDef: c.MaxDef}
		d.cols[i].dictSeg = -1
	}
	return d
}

func (d *decoder) fetch(member int, want int64) ([]byte, error) {
	b, err := d.get(d.rec.Digests[member])
	if err != nil {
		return nil, err
	}
	if want >= 0 && int64(len(b)) != want {
		return nil, recipeErr("component %x is %d bytes, expected %d", d.rec.Digests[member][:6], len(b), want)
	}
	d.stats.Components++
	d.stats.Bytes += int64(len(b))
	return b, nil
}

// position makes the column's cursors read from the given item positions.
func (d *decoder) position(ci int, vpos, lpos uint64, needLevels bool) error {
	cs := &d.cols[ci]
	col := &d.rec.Cols[ci]
	if cs.v == nil {
		cs.v = newCursor(d.get, &col.V, cs.spec.valueParser(), &d.stats)
		cs.vnext = 0
		if vpos != 0 {
			cs.vnext = ^uint64(0)
		}
	}
	if cs.vnext != vpos {
		if _, err := cs.v.seek(vpos); err != nil {
			return err
		}
		cs.vnext = vpos
	}
	if needLevels {
		if cs.l == nil {
			cs.l = &levelReader{cur: newCursor(d.get, &col.L, parseRun, &d.stats), maxDef: col.MaxDef}
			cs.lnext = 0
			if lpos != 0 {
				cs.lnext = ^uint64(0)
			}
		}
		if cs.lnext != lpos {
			if err := cs.l.seek(lpos); err != nil {
				return err
			}
			cs.lnext = lpos
		}
	}
	return nil
}

func (d *decoder) readValues(cs *colState, k int) (recs, error) {
	r := newRecs(cs.spec)
	for i := 0; i < k; i++ {
		rec, _, err := cs.v.next()
		if errors.Is(err, io.EOF) {
			return r, recipeErr("the value stream ends before the page does")
		}
		if err != nil {
			return r, err
		}
		if cs.spec.Phys == TBoolean && rec[0] > 1 {
			return r, recipeErr("boolean value %d is not 0 or 1", rec[0])
		}
		r.add(rec)
	}
	cs.vnext += uint64(k)
	return r, nil
}

// dictOf returns the dictionary governing a data page (building it on first use).
func (d *decoder) dictOf(si int) (*dict, error) {
	if si < 0 {
		return nil, nil
	}
	s := &d.rec.Segs[si]
	cs := &d.cols[s.Col]
	if cs.dictSeg == si {
		return cs.dict, nil
	}
	n := int(s.Page.NumValues)
	if n > MaxPageValues {
		return nil, limitErr("dictionary of %d entries", n)
	}
	var vals recs
	if s.DictExplicit {
		b, err := d.fetch(s.Member, -1)
		if err != nil {
			return nil, err
		}
		r, used, err := decodePlain(cs.spec, b, n, d.lim)
		if err != nil || used != len(b) {
			return nil, recipeErr("dictionary component: %v", err)
		}
		vals = r
	} else {
		// the dictionary is the first N distinct values of the chunk's value stream
		cur := newCursor(d.get, &d.rec.Cols[s.Col].V, cs.spec.valueParser(), &d.stats)
		if _, err := cur.seek(s.VPos); err != nil {
			return nil, err
		}
		vals = newRecs(cs.spec)
		seen := make(map[string]struct{}, min(n, 1<<16))
		for vals.n < n {
			rec, _, err := cur.next()
			if errors.Is(err, io.EOF) {
				return nil, recipeErr("the value stream holds fewer than %d distinct values", n)
			}
			if err != nil {
				return nil, err
			}
			if _, ok := seen[string(rec)]; !ok {
				seen[string(rec)] = struct{}{}
				vals.add(rec)
				if int64(len(vals.buf)) > d.lim.MaxDict {
					return nil, limitErr("dictionary above %d bytes", d.lim.MaxDict)
				}
			}
		}
	}
	dd := newDict(vals)
	cs.dictSeg, cs.dict = si, dd
	return dd, nil
}

func (d *decoder) compRec(s *Seg) (CompRec, error) {
	rec := CompRec{ID: s.Comp}
	if s.Comp > compMax {
		return rec, recipeErr("compression id %d", s.Comp)
	}
	if compNeedsMember(s.Comp) {
		b, err := d.fetch(s.CompMember, -1)
		if err != nil {
			return rec, err
		}
		if s.Comp == CompSnapCmds {
			rec.Cmds = b
		} else {
			rec.Zstd = b
		}
	}
	return rec, nil
}

// page rebuilds the bytes of a logical page segment (raw header + payload).
func (d *decoder) page(si int) ([]byte, error) {
	s := &d.rec.Segs[si]
	cs := &d.cols[s.Col]
	pg := s.Page
	rec, err := d.compRec(s)
	if err != nil {
		return nil, err
	}
	var levels, values []byte
	if s.Kind == SegDict {
		if s.DictExplicit {
			if values, err = d.fetch(s.Member, -1); err != nil {
				return nil, err
			}
		} else {
			dd, err := d.dictOf(si)
			if err != nil {
				return nil, err
			}
			values = encodePlain(cs.spec, &dd.vals)
		}
		d.stats.Pages++
	} else {
		n := int(pg.NumValues)
		if n > MaxPageValues {
			return nil, limitErr("page of %d values", n)
		}
		dd, err := d.dictOf(s.DictSeg)
		if err != nil {
			return nil, err
		}
		col := &d.rec.Cols[s.Col]
		if err := d.position(s.Col, s.VPos, s.LPos, col.MaxDef > 0); err != nil {
			return nil, err
		}
		pc := pageContent{n: n, present: s.Present}
		if col.MaxDef > 0 {
			if pc.defs, err = cs.l.read(n); err != nil {
				return nil, err
			}
			cs.lnext += uint64(n)
			pc.present = 0
			for _, v := range pc.defs {
				if int(v) == col.MaxDef {
					pc.present++
				}
			}
			if pc.present != s.Present {
				return nil, recipeErr("page holds %d non-null values, the recipe says %d", pc.present, s.Present)
			}
		}
		if pc.vals, err = d.readValues(cs, pc.present); err != nil {
			return nil, err
		}
		var ok1, ok2 bool
		levels, ok1 = encodeLevels(pg, cs.spec, pc.defs, s.Lay)
		values, ok2 = encodeValues(pg, cs.spec, dd, &pc, s.Lay)
		if !ok1 || !ok2 {
			return nil, recipeErr("page layout does not apply to its values")
		}
		d.stats.Pages++
	}
	var payload []byte
	if pg.Type == PageDataV2 {
		v, err := d.cp.Replay(rec, values)
		if err != nil {
			return nil, err
		}
		payload = append(append(make([]byte, 0, len(levels)+len(v)), levels...), v...)
	} else {
		body := values
		if len(levels) > 0 {
			body = append(append(make([]byte, 0, len(levels)+len(values)), levels...), values...)
		}
		if payload, err = d.cp.Replay(rec, body); err != nil {
			return nil, err
		}
	}
	if int64(len(payload)) != int64(pg.Comp) {
		return nil, recipeErr("page payload is %d bytes, its header says %d", len(payload), pg.Comp)
	}
	return append(append(make([]byte, 0, len(s.Hdr)+len(payload)), s.Hdr...), payload...), nil
}

// seg rebuilds the bytes of one segment.
func (d *decoder) seg(si int) ([]byte, error) {
	s := &d.rec.Segs[si]
	switch s.Kind {
	case SegFrame:
		return s.Frame, nil
	case SegFrameComp:
		return d.fetch(s.Member, s.Len)
	case SegOpaque:
		b, err := d.fetch(s.Member, int64(s.Page.Comp))
		if err != nil {
			return nil, err
		}
		return append(append(make([]byte, 0, len(s.Hdr)+len(b)), s.Hdr...), b...), nil
	}
	return d.page(si)
}

// Decode writes the exact original file to w from the serialized recipe and the
// components it names; nothing else is consulted.
func Decode(w io.Writer, recipe []byte, get Getter, lim Limits) error {
	rec, err := ParseRecipe(recipe, lim)
	if err != nil {
		return err
	}
	_, err = decodeRange(w, rec, get, 0, rec.Size, lim)
	return err
}

// DecodeRange writes bytes [off, off+n) of the file, reconstructing only the segments
// that overlap the range (a page is the unit) and reports what it fetched.
func DecodeRange(w io.Writer, recipe []byte, get Getter, off, n int64, lim Limits) (RangeStats, error) {
	rec, err := ParseRecipe(recipe, lim)
	if err != nil {
		return RangeStats{}, err
	}
	return decodeRange(w, rec, get, off, n, lim)
}

func decodeRange(w io.Writer, rec *Recipe, get Getter, off, n int64, lim Limits) (RangeStats, error) {
	if off < 0 || n < 0 || off > rec.Size || n > rec.Size-off {
		return RangeStats{}, recipeErr("range [%d,+%d) outside the %d byte file", off, n, rec.Size)
	}
	d := newDecoder(rec, get, lim)
	end := off + n
	// first overlapping segment by binary search over the derived offsets
	lo, hi := 0, len(rec.Segs)
	for lo < hi {
		m := (lo + hi) / 2
		if rec.Segs[m].Off+rec.Segs[m].Total <= off {
			lo = m + 1
		} else {
			hi = m
		}
	}
	for si := lo; si < len(rec.Segs) && rec.Segs[si].Off < end; si++ {
		s := &rec.Segs[si]
		if s.Total == 0 {
			continue
		}
		b, err := d.seg(si)
		if err != nil {
			return d.stats, err
		}
		if int64(len(b)) != s.Total {
			return d.stats, recipeErr("segment %d rebuilt as %d bytes, expected %d", si, len(b), s.Total)
		}
		a, z := max(off, s.Off)-s.Off, min(end, s.Off+s.Total)-s.Off
		if _, err := w.Write(b[a:z]); err != nil {
			return d.stats, err
		}
	}
	return d.stats, nil
}

// Components calls emit with the digest of every leaf group named by the stream nodes (the
// components reachable only through a node); get fetches the nodes.
func Components(recipe []byte, get Getter, lim Limits, emit func(ID)) error {
	rec, err := ParseRecipe(recipe, lim)
	if err != nil {
		return err
	}
	for ci := range rec.Cols {
		for _, st := range []*streamRef{&rec.Cols[ci].V, &rec.Cols[ci].L} {
			for _, nr := range st.nodes {
				b, err := get(nr.id)
				if err != nil {
					return err
				}
				ents, err := parseNode(b, nodeMax*4)
				if err != nil {
					return err
				}
				for _, e := range ents {
					emit(e.id)
				}
			}
		}
	}
	return nil
}
