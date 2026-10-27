package parquetz3

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
)

// ID is the SHA-256 identity of one component.
type ID = [32]byte

// Emit receives every component Encode produces, once per occurrence (the caller
// deduplicates by id). class is "val", "lvl", "node", "dict", "crec", "frame" or "opq".
// The bytes are only valid during the call.
type Emit func(class string, id ID, data []byte) error

// Getter fetches one component by digest (whole component, as the generic layer does).
type Getter func(id ID) ([]byte, error)

// CutMode selects how a column's record stream is cut into leaf groups.
type CutMode uint8

const (
	// CutContent cuts at record boundaries where a rolling hash of the preceding bytes
	// fires, never before Target/4 or after 4*Target bytes. It depends on content only,
	// so an inserted or removed value moves no boundary far from the edit, and it does
	// not know where pages or row groups end.
	CutContent CutMode = iota
	CutFixed           // every FixedCount records (position dependent: the negative reference)
	CutPage            // one group per page (the physical-page baseline over the logical stream)
	CutRecord          // one group per record (the object-count extreme; an oracle, not a design)
)

// Z3-30's sstz3.HashCutter decides a cut from a hash of the record's user key. A Parquet
// value has no key, and hashing the value alone is wrong for low-cardinality columns (a
// value either always or never cuts). The signal here is the same idea over a window: a
// 64-bit gear hash of the bytes just consumed, tested at every byte so the expected
// group size does not depend on how wide a record is, snapped to the end of the record.

var gear = func() (t [256]uint64) {
	x := uint64(0x9e3779b97f4a7c15)
	for i := range t {
		x += 0x9e3779b97f4a7c15
		z := x
		z = (z ^ z>>30) * 0xbf58476d1ce4e5b9
		z = (z ^ z>>27) * 0x94d049bb133111eb
		t[i] = z ^ z>>31
	}
	return
}()

type cutter struct {
	mode        CutMode
	min, max, p uint64
	fixed       int
	h           uint64
	cur         uint64
	n           int
}

func newCutter(o *Options) cutter {
	t := uint64(o.Target)
	return cutter{mode: o.Cut, min: t / 4, max: 4 * t, p: max(t*3/4, 1), fixed: max(o.FixedCount, 1)}
}

// feed is called once per record, after it was appended; it reports whether the group
// closes after this record.
func (c *cutter) feed(rec []byte) bool {
	switch c.mode {
	case CutRecord:
		return true
	case CutFixed:
		c.n++
		if c.n >= c.fixed {
			c.n = 0
			return true
		}
		return false
	case CutPage:
		return false
	}
	pending := false
	pos := c.cur
	for _, b := range rec {
		c.h = c.h<<1 + gear[b]
		pos++
		if pos >= c.min && uint64(uint32(c.h>>32))%c.p == 0 {
			pending = true
		}
	}
	c.cur = pos
	if pending || c.cur >= c.max {
		c.cur = 0
		return true
	}
	return false
}

// Node fan-out: a node lists the digests of consecutive leaf groups and is itself cut by
// the digest of the last group it holds, so a changed group changes one node and shifts
// no other.
const (
	nodeMin = 8
	nodeAvg = 64
	nodeMax = 256
)

type entry struct {
	id    ID
	items uint64
}

type nodeRef struct {
	id    ID
	items uint64
}

// streamWriter builds one record stream: leaf groups, nodes over them, and the list of
// nodes the recipe references.
type streamWriter struct {
	class string
	cut   cutter
	buf   []byte
	items uint64
	ents  []entry
	nodes []nodeRef
	emit  Emit
	// accounting
	Groups, Records int64
	GroupBytes      int64
}

func newStream(class string, o *Options, emit Emit) *streamWriter {
	return &streamWriter{class: class, cut: newCutter(o), emit: emit}
}

func (s *streamWriter) add(rec []byte, items int) error {
	s.buf = append(s.buf, rec...)
	s.items += uint64(items)
	s.Records++
	if s.cut.feed(rec) {
		return s.closeGroup()
	}
	return nil
}

// endPage closes the group at a page boundary in CutPage mode.
func (s *streamWriter) endPage() error {
	if s.cut.mode == CutPage {
		return s.closeGroup()
	}
	return nil
}

func (s *streamWriter) closeGroup() error {
	if len(s.buf) == 0 {
		return nil
	}
	id := ID(sha256.Sum256(s.buf))
	if err := s.emit(s.class, id, s.buf); err != nil {
		return err
	}
	s.Groups++
	s.GroupBytes += int64(len(s.buf))
	s.ents = append(s.ents, entry{id, s.items})
	s.buf, s.items = s.buf[:0], 0
	if len(s.ents) >= nodeMax || len(s.ents) >= nodeMin && id[0]&(nodeAvg-1) == 0 {
		return s.closeNode()
	}
	return nil
}

func (s *streamWriter) closeNode() error {
	if len(s.ents) == 0 {
		return nil
	}
	b := binary.AppendUvarint(nil, uint64(len(s.ents)))
	var total uint64
	for _, e := range s.ents {
		b = append(b, e.id[:]...)
		b = binary.AppendUvarint(b, e.items)
		total += e.items
	}
	id := ID(sha256.Sum256(b))
	if err := s.emit("node", id, b); err != nil {
		return err
	}
	s.nodes = append(s.nodes, nodeRef{id, total})
	s.ents = s.ents[:0]
	return nil
}

func (s *streamWriter) finish() error {
	if err := s.closeGroup(); err != nil {
		return err
	}
	return s.closeNode()
}

// parseNode decodes a node component into its entries.
func parseNode(b []byte, maxEnt int) ([]entry, error) {
	n, k := readUv(b)
	if k <= 0 || n > uint64(maxEnt) || n > uint64(len(b)) {
		return nil, recipeErr("node entry count")
	}
	pos := k
	out := make([]entry, 0, n)
	for i := uint64(0); i < n; i++ {
		if len(b)-pos < 33 {
			return nil, recipeErr("node entry %d is cut short", i)
		}
		var e entry
		copy(e.id[:], b[pos:])
		pos += 32
		it, k := readUv(b[pos:])
		if k <= 0 {
			return nil, recipeErr("node entry %d item count", i)
		}
		pos += k
		e.items = it
		out = append(out, e)
	}
	if pos != len(b) {
		return nil, recipeErr("%d bytes after the node entries", len(b)-pos)
	}
	return out, nil
}

// ---- reading a stream ----------------------------------------------------------------

// RangeStats counts what a read fetched.
type RangeStats struct {
	Components int   // components fetched (nodes, groups, frames, payloads)
	Bytes      int64 // their sizes
	Nodes      int
	Pages      int // pages reconstructed
}

type streamRef struct {
	nodes []nodeRef // digests resolved through the member table by the recipe parser
	total uint64
}

// parseRec returns the byte length and item count of the record at the start of b.
type parseRec func(b []byte) (n int, items uint64, ok bool)

func parseFixed(w int) parseRec {
	return func(b []byte) (int, uint64, bool) { return w, 1, len(b) >= w }
}

func parseVarlen(b []byte) (int, uint64, bool) {
	if len(b) < 4 {
		return 0, 0, false
	}
	n := 4 + uint64(binary.LittleEndian.Uint32(b))
	return int(n), 1, n <= uint64(len(b))
}

func parseRun(b []byte) (int, uint64, bool) {
	v, k := readUv(b)
	return k, v >> 4, k > 0
}

func (c colSpec) valueParser() parseRec {
	if c.Phys == TByteArr {
		return parseVarlen
	}
	return parseFixed(c.width())
}

// cursor reads a stream record by record, fetching nodes and groups on demand.
type cursor struct {
	get   Getter
	ref   *streamRef
	parse parseRec
	stats *RangeStats
	ni    int // current node
	ents  []entry
	ei    int // current group of the node
	grp   []byte
	gp    int
	have  bool // a node is loaded
	eof   bool
}

func newCursor(get Getter, ref *streamRef, parse parseRec, st *RangeStats) *cursor {
	return &cursor{get: get, ref: ref, parse: parse, stats: st}
}

func (c *cursor) fetch(id ID) ([]byte, error) {
	b, err := c.get(id)
	if err != nil {
		return nil, err
	}
	if c.stats != nil {
		c.stats.Components++
		c.stats.Bytes += int64(len(b))
	}
	return b, nil
}

func (c *cursor) loadNode(n int) error {
	b, err := c.fetch(c.ref.nodes[n].id)
	if err != nil {
		return err
	}
	if c.stats != nil {
		c.stats.Nodes++
	}
	ents, err := parseNode(b, nodeMax*4)
	if err != nil {
		return err
	}
	var sum uint64
	for _, e := range ents {
		sum += e.items
	}
	if sum != c.ref.nodes[n].items {
		return recipeErr("node %d holds %d items, the recipe says %d", n, sum, c.ref.nodes[n].items)
	}
	c.ni, c.ents, c.have = n, ents, true
	return nil
}

func (c *cursor) loadGroup(i int) error {
	b, err := c.fetch(c.ents[i].id)
	if err != nil {
		return err
	}
	c.ei, c.grp, c.gp = i, b, 0
	return nil
}

// next returns the next record (valid until the next call) and its item count; io.EOF at
// the end of the stream.
func (c *cursor) next() ([]byte, uint64, error) {
	for {
		if c.gp < len(c.grp) {
			n, it, ok := c.parse(c.grp[c.gp:])
			if !ok || n <= 0 {
				return nil, 0, recipeErr("record of a group is cut short")
			}
			rec := c.grp[c.gp : c.gp+n]
			c.gp += n
			return rec, it, nil
		}
		if c.eof {
			return nil, 0, io.EOF
		}
		var err error
		switch {
		case !c.have:
			if len(c.ref.nodes) == 0 {
				c.eof = true
				continue
			}
			if err = c.loadNode(0); err == nil && len(c.ents) > 0 {
				err = c.loadGroup(0)
			}
		case c.ei+1 < len(c.ents):
			err = c.loadGroup(c.ei + 1)
		case c.ni+1 < len(c.ref.nodes):
			if err = c.loadNode(c.ni + 1); err == nil && len(c.ents) > 0 {
				err = c.loadGroup(0)
			}
		default:
			c.eof, c.grp, c.gp = true, nil, 0
		}
		if err != nil {
			return nil, 0, err
		}
	}
}

// seek positions the cursor so that next returns the record holding item number item
// (0-based); skip is how many items of that record lie before it (a level run may be
// entered in the middle). Seeking to the end of the stream leaves next at io.EOF.
func (c *cursor) seek(item uint64) (skip uint64, err error) {
	c.have, c.eof, c.grp, c.gp = false, false, nil, 0
	var base uint64
	for n := range c.ref.nodes {
		if base+c.ref.nodes[n].items <= item {
			base += c.ref.nodes[n].items
			continue
		}
		if err := c.loadNode(n); err != nil {
			return 0, err
		}
		for i := range c.ents {
			if base+c.ents[i].items <= item {
				base += c.ents[i].items
				continue
			}
			if err := c.loadGroup(i); err != nil {
				return 0, err
			}
			for c.gp < len(c.grp) {
				n, it, ok := c.parse(c.grp[c.gp:])
				if !ok || n <= 0 {
					return 0, recipeErr("record of a group is cut short")
				}
				if base+it > item {
					return item - base, nil
				}
				base += it
				c.gp += n
			}
		}
		return 0, recipeErr("node holds fewer items than the recipe says")
	}
	if base != item {
		return 0, recipeErr("seek to item %d beyond the stream's %d", item, base)
	}
	c.have, c.eof = true, true
	return 0, nil
}
