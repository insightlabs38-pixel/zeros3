package gitz3

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/adler32"
	"hash/crc32"
	"io"
)

// Entry is one parsed pack entry. Offsets are into Pack.Data.
type Entry struct {
	Off, End   int64  // whole entry [Off,End)
	Kind       uint8  // 1..4 full object, 6 OFS_DELTA, 7 REF_DELTA
	Size       int64  // inflated size of the entry's zlib stream (object payload, or delta program)
	HdrLen     int    // bytes of the type/size varint
	BaseLen    int    // bytes of the OFS offset / REF id encoding (0 for full objects)
	BaseOff    int64  // OFS: absolute offset of the base entry
	BaseRef    []byte // REF: base object id
	ZOff, ZEnd int64  // zlib stream [ZOff,ZEnd) including header and Adler-32
	CRC        uint32 // CRC-32 of [Off,End) (what the v2 index stores)
	// Filled by Resolve.
	Type  ObjType // resolved object type
	RSize int64   // resolved object size
	ID    []byte
	Base  int // index of the base entry, -1 for a full object
	Depth int
}

func (e *Entry) IsDelta() bool { return e.Kind == ofsDelta || e.Kind == refDelta }

// Pack is a parsed pack file held in memory.
type Pack struct {
	Algo     Algo
	Data     []byte
	Version  uint32
	Entries  []Entry
	Trailer  []byte
	Lim      Limits
	byOff    map[int64]int
	ids      map[string]int
	resolved bool
}

// ParsePack parses the pack structure: header, every entry header, every zlib
// stream boundary (each stream is inflated once to find its end, check its
// declared size and its Adler-32) and the trailer checksum. It does not
// resolve deltas; see Resolve. Anything it cannot parse is an error the caller
// answers by treating the pack as opaque bytes.
func ParsePack(data []byte, a Algo, lim Limits) (*Pack, error) {
	lim = lim.norm()
	hs := a.Size()
	if int64(len(data)) > lim.MaxPack {
		return nil, limit("pack %d bytes", len(data))
	}
	if len(data) < 12+hs {
		return nil, corrupt("pack shorter than header+trailer")
	}
	if string(data[:4]) != "PACK" {
		return nil, corrupt("pack magic")
	}
	ver := binary.BigEndian.Uint32(data[4:])
	if ver != 2 && ver != 3 {
		return nil, ErrUnsupported
	}
	n := binary.BigEndian.Uint32(data[8:])
	body := int64(len(data) - hs)
	if int64(n) > int64(lim.MaxObjects) || int64(n) > (body-12)/9 {
		return nil, limit("object count %d", n)
	}
	h := a.New()
	h.Write(data[:body])
	if !bytes.Equal(h.Sum(nil), data[body:]) {
		return nil, ErrChecksum
	}
	p := &Pack{Algo: a, Data: data, Version: ver, Trailer: data[body:], Lim: lim, byOff: make(map[int64]int, n)}
	p.Entries = make([]Entry, 0, n)
	var inflated int64
	off := int64(12)
	for i := uint32(0); i < n; i++ {
		e, err := p.parseEntry(off, body)
		if err != nil {
			return nil, err
		}
		inflated += e.Size
		if inflated > lim.MaxInflated {
			return nil, limit("inflated bytes > %d", lim.MaxInflated)
		}
		e.CRC = crc32.ChecksumIEEE(data[e.Off:e.End])
		p.byOff[e.Off] = len(p.Entries)
		p.Entries = append(p.Entries, e)
		off = e.End
	}
	if off != body {
		return nil, corrupt("%d bytes between last entry and trailer", body-off)
	}
	return p, nil
}

type adlerSink struct {
	h hashAdler
	n int64
}
type hashAdler = interface {
	io.Writer
	Sum32() uint32
}

func (s *adlerSink) Write(b []byte) (int, error) { s.n += int64(len(b)); return s.h.Write(b) }

func (p *Pack) parseEntry(off, body int64) (Entry, error) {
	d := p.Data
	e := Entry{Off: off, Base: -1}
	i := off
	if i >= body {
		return e, corrupt("entry offset %d at/after trailer", i)
	}
	c := d[i]
	i++
	e.Kind = (c >> 4) & 7
	size := uint64(c & 15)
	shift := uint(4)
	for c&0x80 != 0 {
		if i >= body {
			return e, corrupt("entry header truncated")
		}
		if shift > 56 {
			return e, corrupt("entry size varint too long")
		}
		c = d[i]
		i++
		size |= uint64(c&0x7f) << shift
		shift += 7
	}
	if size > uint64(p.Lim.MaxObjectSize) {
		return e, limit("entry size %d", size)
	}
	e.Size = int64(size)
	e.HdrLen = int(i - off)
	switch e.Kind {
	case 1, 2, 3, 4:
	case ofsDelta:
		start := i
		if i >= body {
			return e, corrupt("OFS header truncated")
		}
		c = d[i]
		i++
		ofs := uint64(c & 0x7f)
		for c&0x80 != 0 {
			if i >= body {
				return e, corrupt("OFS header truncated")
			}
			if ofs >= 1<<56 {
				return e, corrupt("OFS offset overflow")
			}
			ofs++
			c = d[i]
			i++
			ofs = ofs<<7 | uint64(c&0x7f)
		}
		if ofs == 0 || int64(ofs) > off-12 {
			return e, corrupt("OFS base offset %d from %d", ofs, off)
		}
		e.BaseOff = off - int64(ofs)
		e.BaseLen = int(i - start)
	case refDelta:
		hs := int64(p.Algo.Size())
		if i+hs > body {
			return e, corrupt("REF header truncated")
		}
		e.BaseRef = d[i : i+hs]
		i += hs
		e.BaseLen = int(hs)
	default:
		return e, corrupt("entry type %d", e.Kind)
	}
	e.ZOff = i
	zl, err := inflateZlib(d[i:body], e.Size, io.Discard)
	if err != nil {
		return e, err
	}
	e.ZEnd = i + zl
	e.End = e.ZEnd
	return e, nil
}

// inflateZlib decodes the zlib stream at the start of data into w, requires it
// to produce exactly want bytes with a valid Adler-32, and returns its length.
// It uses compress/flate over a bytes.Reader (which implements io.ByteReader, so
// the reader consumes no byte beyond the stream) and never allocates from the
// claimed length.
func inflateZlib(data []byte, want int64, w io.Writer) (int64, error) {
	if len(data) < 2+2+4 {
		return 0, corrupt("zlib stream truncated")
	}
	cmf, flg := data[0], data[1]
	if cmf&15 != 8 || cmf>>4 > 7 || (uint(cmf)<<8|uint(flg))%31 != 0 || flg&0x20 != 0 {
		return 0, corrupt("zlib header %02x%02x", cmf, flg)
	}
	br := bytes.NewReader(data[2:])
	fr := flate.NewReader(br)
	defer fr.Close()
	sink := &adlerSink{h: adler32.New()}
	n, err := io.Copy(io.MultiWriter(sink, w), io.LimitReader(fr, want+1))
	if err != nil {
		return 0, corrupt("inflate: %v", err)
	}
	if n != want {
		return 0, corrupt("inflated %d bytes, header says %d", n, want)
	}
	used := int64(len(data)-2) - int64(br.Len())
	if used+2+4 > int64(len(data)) {
		return 0, corrupt("zlib trailer truncated")
	}
	if binary.BigEndian.Uint32(data[2+used:]) != sink.h.Sum32() {
		return 0, corrupt("zlib Adler-32")
	}
	return 2 + used + 4, nil
}

// ZlibBytes is the exact zlib stream of entry i.
func (p *Pack) ZlibBytes(i int) []byte { return p.Data[p.Entries[i].ZOff:p.Entries[i].ZEnd] }

// HeaderBytes is the entry's type/size varint plus its OFS/REF base encoding.
func (p *Pack) HeaderBytes(i int) []byte { return p.Data[p.Entries[i].Off:p.Entries[i].ZOff] }

// Inflate returns the zlib plaintext of entry i: the object payload of a full
// entry, the delta program of a delta entry.
func (p *Pack) Inflate(i int) ([]byte, error) {
	e := &p.Entries[i]
	var buf bytes.Buffer
	buf.Grow(int(min(e.Size, (e.ZEnd-e.ZOff)*1032+64)))
	if _, err := inflateZlib(p.Data[e.ZOff:e.ZEnd], e.Size, &buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ResolveStats summarise a Resolve.
type ResolveStats struct {
	Full, Delta, MaxDepth int
	DeltaOut, Inflated    int64
}

// Resolve resolves every delta chain (depth-first from the full objects, one
// chain of payloads in memory at a time) and fills Type, RSize, ID, Base and
// Depth. Unresolvable deltas (a REF_DELTA base missing from the pack, which
// includes thin packs and REF cycles) return ErrThin; duplicate object ids are
// ErrUnsupported; limits are enforced on depth, per-object size, total
// inflated bytes and total delta output.
func (p *Pack) Resolve() (ResolveStats, error) {
	var st ResolveStats
	lim := p.Lim
	n := len(p.Entries)
	ofsKids := map[int][]int{}
	refWait := map[string][]int{}
	var roots []int
	for i := range p.Entries {
		e := &p.Entries[i]
		switch e.Kind {
		case ofsDelta:
			b, ok := p.byOff[e.BaseOff]
			if !ok {
				return st, corrupt("OFS base offset %d is not an entry boundary", e.BaseOff)
			}
			e.Base = b
			ofsKids[b] = append(ofsKids[b], i)
		case refDelta:
			refWait[string(e.BaseRef)] = append(refWait[string(e.BaseRef)], i)
		default:
			roots = append(roots, i)
		}
	}
	ids := make(map[string]int, n)
	done := 0
	type frame struct {
		idx     int
		payload []byte
		kids    []int
		next    int
	}
	var stack []frame
	register := func(i int, t ObjType, payload []byte, depth int) error {
		e := &p.Entries[i]
		e.Type, e.RSize, e.Depth = t, int64(len(payload)), depth
		e.ID = ObjectID(p.Algo, t, payload)
		if _, dup := ids[string(e.ID)]; dup {
			return ErrUnsupported
		}
		ids[string(e.ID)] = i
		done++
		if depth > st.MaxDepth {
			st.MaxDepth = depth
		}
		kids := append([]int(nil), ofsKids[i]...)
		if w, ok := refWait[string(e.ID)]; ok {
			kids = append(kids, w...)
			delete(refWait, string(e.ID))
		}
		if len(kids) == 0 {
			return nil
		}
		stack = append(stack, frame{idx: i, payload: payload, kids: kids})
		return nil
	}
	for _, r := range roots {
		e := &p.Entries[r]
		pl, err := p.Inflate(r)
		if err != nil {
			return st, err
		}
		st.Inflated += int64(len(pl))
		st.Full++
		if err := register(r, ObjType(e.Kind), pl, 0); err != nil {
			return st, err
		}
		for len(stack) > 0 {
			f := &stack[len(stack)-1]
			if f.next >= len(f.kids) {
				stack = stack[:len(stack)-1]
				continue
			}
			k := f.kids[f.next]
			f.next++
			ke := &p.Entries[k]
			if depth := p.Entries[f.idx].Depth + 1; depth > lim.MaxDeltaDepth {
				return st, limit("delta depth %d", depth)
			}
			d, err := p.Inflate(k)
			if err != nil {
				return st, err
			}
			st.Inflated += int64(len(d))
			if st.Inflated > lim.MaxInflated {
				return st, limit("inflated bytes > %d", lim.MaxInflated)
			}
			_, tgt, _, herr := ParseDeltaHeader(d)
			if herr != nil {
				return st, herr
			}
			if tgt > uint64(lim.MaxObjectSize) {
				return st, limit("delta target %d", tgt)
			}
			if st.DeltaOut += int64(tgt); st.DeltaOut > lim.MaxDeltaOut {
				return st, limit("delta output > %d", lim.MaxDeltaOut)
			}
			out, err := ApplyDelta(f.payload, d, lim.MaxObjectSize)
			if err != nil {
				return st, err
			}
			ke.Base = f.idx
			st.Delta++
			if err := register(k, p.Entries[f.idx].Type, out, p.Entries[f.idx].Depth+1); err != nil {
				return st, err
			}
		}
	}
	if done != n {
		return st, ErrThin
	}
	p.ids = ids
	p.resolved = true
	return st, nil
}

// Object materialises the resolved object of entry i (header-less payload),
// following its delta chain iteratively; cache, if non-nil, memoises payloads
// by entry.
func (p *Pack) Object(i int, cache map[int][]byte) ([]byte, error) {
	if !p.resolved {
		return nil, ErrUnsupported
	}
	var chain []int
	cur := i
	var base []byte
	for {
		if b, ok := cache[cur]; ok {
			base = b
			break
		}
		chain = append(chain, cur)
		if !p.Entries[cur].IsDelta() {
			break
		}
		if len(chain) > p.Lim.MaxDeltaDepth+1 {
			return nil, limit("delta depth")
		}
		cur = p.Entries[cur].Base
	}
	for k := len(chain) - 1; k >= 0; k-- {
		j := chain[k]
		pl, err := p.Inflate(j)
		if err != nil {
			return nil, err
		}
		if p.Entries[j].IsDelta() {
			if pl, err = ApplyDelta(base, pl, p.Lim.MaxObjectSize); err != nil {
				return nil, err
			}
		}
		if cache != nil {
			cache[j] = pl
		}
		base = pl
	}
	return base, nil
}

// IndexOfID returns the entry index of an object id, or -1 (after Resolve).
func (p *Pack) IndexOfID(id []byte) int {
	if i, ok := p.ids[string(id)]; ok {
		return i
	}
	return -1
}
