package gitz3

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"hash/adler32"
	"hash/crc32"
	"io"
	"sync"
)

// Entry is one parsed pack entry. Offsets are into the pack source.
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

// Pack is a parsed pack: structural metadata plus a bounded random-access source.
// It never holds the pack bytes; every range is read on demand through the source
// (a file, a CAS-backed object, a byte slice), and no read can go past Size.
type Pack struct {
	Algo    Algo
	Version uint32
	Entries []Entry
	Trailer []byte
	Lim     Limits
	Size    int64 // bytes of the pack, trailer included

	src      *io.SectionReader
	byOff    map[int64]int
	ids      map[string]int
	resolved bool
}

// ParsePack parses an in-memory pack; a convenience wrapper over ParsePackAt.
func ParsePack(data []byte, a Algo, lim Limits) (*Pack, error) {
	return ParsePackAt(bytes.NewReader(data), int64(len(data)), a, lim)
}

// ParsePackAt parses the pack structure of the size bytes readable from r:
// header, every entry header, every zlib stream boundary (each stream is inflated
// once to find its end, check its declared size and its Adler-32) and the trailer
// checksum. It reads in bounded pieces (a fixed header window, a fixed streaming
// buffer), never past size, and allocates nothing from a declared length. It does
// not resolve deltas; see Resolve. Anything it cannot parse is an error the caller
// answers by treating the pack as opaque bytes. A source shorter than size is
// ErrCorrupt.
func ParsePackAt(r io.ReaderAt, size int64, a Algo, lim Limits) (*Pack, error) {
	lim = lim.norm()
	hs := int64(a.Size())
	if size < 0 {
		return nil, corrupt("negative pack size")
	}
	if size > lim.MaxPack {
		return nil, limit("pack %d bytes", size)
	}
	if size < 12+hs {
		return nil, corrupt("pack shorter than header+trailer")
	}
	src := io.NewSectionReader(r, 0, size)
	var hdr [12]byte
	if err := readFullAt(src, hdr[:], 0); err != nil {
		return nil, err
	}
	if string(hdr[:4]) != "PACK" {
		return nil, corrupt("pack magic")
	}
	ver := binary.BigEndian.Uint32(hdr[4:])
	if ver != 2 && ver != 3 {
		return nil, ErrUnsupported
	}
	n := binary.BigEndian.Uint32(hdr[8:])
	body := size - hs
	if int64(n) > int64(lim.MaxObjects) || int64(n) > (body-12)/9 {
		return nil, limit("object count %d", n)
	}
	h := a.New()
	if got, err := io.Copy(h, io.NewSectionReader(src, 0, body)); err != nil {
		return nil, err
	} else if got != body {
		return nil, corrupt("source ended at %d of %d bytes", got, body)
	}
	trailer := make([]byte, hs)
	if err := readFullAt(src, trailer, body); err != nil {
		return nil, err
	}
	if !bytes.Equal(h.Sum(nil), trailer) {
		return nil, ErrChecksum
	}
	p := &Pack{Algo: a, Version: ver, Trailer: trailer, Lim: lim, Size: size, src: src, byOff: make(map[int64]int, n)}
	p.Entries = make([]Entry, 0, n)
	zr := getZReader()
	defer putZReader(zr)
	var inflated int64
	off := int64(12)
	for i := uint32(0); i < n; i++ {
		e, err := p.parseEntry(off, body, zr)
		if err != nil {
			return nil, err
		}
		inflated += e.Size
		if inflated > lim.MaxInflated {
			return nil, limit("inflated bytes > %d", lim.MaxInflated)
		}
		p.byOff[e.Off] = len(p.Entries)
		p.Entries = append(p.Entries, e)
		off = e.End
	}
	if off != body {
		return nil, corrupt("%d bytes between last entry and trailer", body-off)
	}
	return p, nil
}

// readFullAt fills b from r at off. A short read is ErrCorrupt (the source is
// shorter than its declared size); other I/O errors pass through.
func readFullAt(r io.ReaderAt, b []byte, off int64) error {
	n, err := r.ReadAt(b, off)
	if n == len(b) {
		return nil
	}
	if err == nil || err == io.EOF {
		return corrupt("source ended at %d, wanted %d bytes at %d", off+int64(n), len(b), off)
	}
	return err
}

// ReadAt reads from the pack source; it never reads past Size.
func (p *Pack) ReadAt(b []byte, off int64) (int, error) { return p.src.ReadAt(b, off) }

// Range is a bounded reader over the pack bytes [off,end).
func (p *Pack) Range(off, end int64) (*io.SectionReader, error) {
	if off < 0 || off > end || end > p.Size {
		return nil, corrupt("range [%d,%d) outside pack of %d bytes", off, end, p.Size)
	}
	return io.NewSectionReader(p.src, off, end-off), nil
}

// Bytes copies the pack bytes [off,end) into memory. For headers, trailers and
// other small ranges; large ranges should go through Range / WriteRange.
func (p *Pack) Bytes(off, end int64) ([]byte, error) {
	if off < 0 || off > end || end > p.Size {
		return nil, corrupt("range [%d,%d) outside pack of %d bytes", off, end, p.Size)
	}
	b := make([]byte, end-off)
	if err := readFullAt(p.src, b, off); err != nil {
		return nil, err
	}
	return b, nil
}

// WriteRange streams the pack bytes [off,end) to w.
func (p *Pack) WriteRange(w io.Writer, off, end int64) error {
	r, err := p.Range(off, end)
	if err != nil {
		return err
	}
	n, err := io.Copy(w, r)
	if err == nil && n != end-off {
		err = corrupt("source ended at %d of %d", off+n, end)
	}
	return err
}

// EntryBytes is the exact bytes of entry i (header, base encoding, zlib stream).
func (p *Pack) EntryBytes(i int) ([]byte, error) { return p.Bytes(p.Entries[i].Off, p.Entries[i].End) }

// ZlibBytes is the exact zlib stream of entry i.
func (p *Pack) ZlibBytes(i int) ([]byte, error) { return p.Bytes(p.Entries[i].ZOff, p.Entries[i].ZEnd) }

// HeaderBytes is the entry's type/size varint plus its OFS/REF base encoding.
func (p *Pack) HeaderBytes(i int) ([]byte, error) {
	return p.Bytes(p.Entries[i].Off, p.Entries[i].ZOff)
}

// maxEntryHdr bounds the type/size varint (10) plus an OFS varint (10) or a REF id (32).
const maxEntryHdr = 64

func (p *Pack) parseEntry(off, body int64, zr *zreader) (Entry, error) {
	e := Entry{Off: off, Base: -1}
	if off >= body {
		return e, corrupt("entry offset %d at/after trailer", off)
	}
	var window [maxEntryHdr]byte
	d := window[:min(int64(maxEntryHdr), body-off)]
	if err := readFullAt(p.src, d, off); err != nil {
		return e, err
	}
	i := int64(0)
	c := d[i]
	i++
	e.Kind = (c >> 4) & 7
	size := uint64(c & 15)
	shift := uint(4)
	for c&0x80 != 0 {
		if i >= int64(len(d)) {
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
	e.HdrLen = int(i)
	switch e.Kind {
	case 1, 2, 3, 4:
	case ofsDelta:
		start := i
		if i >= int64(len(d)) {
			return e, corrupt("OFS header truncated")
		}
		c = d[i]
		i++
		ofs := uint64(c & 0x7f)
		for c&0x80 != 0 {
			if i >= int64(len(d)) {
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
		if i+hs > int64(len(d)) {
			return e, corrupt("REF header truncated")
		}
		e.BaseRef = append([]byte(nil), d[i:i+hs]...)
		i += hs
		e.BaseLen = int(hs)
	default:
		return e, corrupt("entry type %d", e.Kind)
	}
	e.ZOff = off + i
	zr.reset(p.src, e.ZOff, body, crc32.ChecksumIEEE(d[:i]))
	zl, err := inflateZlibFrom(zr, e.Size, io.Discard)
	if err != nil {
		return e, err
	}
	e.ZEnd = e.ZOff + zl
	e.End = e.ZEnd
	e.CRC = zr.crcSum()
	return e, nil
}

// zreader is the byte-exact input of the zlib decoder over a ReaderAt range: it
// reads ahead only inside its own buffer, so the number of bytes the decoder
// consumed (and their CRC-32) is exact; compress/flate uses it directly because it
// implements io.ByteReader.
type zreader struct {
	r    io.ReaderAt
	next int64 // source offset of the next byte to buffer
	end  int64
	buf  [4 << 10]byte
	i, n int
	h    int    // buf[h:i] is consumed but not yet folded into crc
	crc  uint32 // CRC-32 over the header bytes given to reset plus every consumed byte
}

var zpool = sync.Pool{New: func() any { return new(zreader) }}

func getZReader() *zreader  { return zpool.Get().(*zreader) }
func putZReader(z *zreader) { z.r = nil; zpool.Put(z) }

// reset points z at [off,end) of r; crc seeds the running CRC-32.
func (z *zreader) reset(r io.ReaderAt, off, end int64, crc uint32) {
	z.r, z.next, z.end, z.i, z.n, z.h, z.crc = r, off, end, 0, 0, 0, crc
}

func (z *zreader) sync() {
	z.crc = crc32.Update(z.crc, crc32.IEEETable, z.buf[z.h:z.i])
	z.h = z.i
}

func (z *zreader) crcSum() uint32 { z.sync(); return z.crc }

// pos is the source offset of the next unconsumed byte.
func (z *zreader) pos() int64 { return z.next - int64(z.n-z.i) }

func (z *zreader) fill() error {
	z.sync()
	z.i, z.n, z.h = 0, 0, 0
	want := min(int64(len(z.buf)), z.end-z.next)
	if want <= 0 {
		return io.ErrUnexpectedEOF
	}
	if err := readFullAt(z.r, z.buf[:want], z.next); err != nil {
		return err
	}
	z.n = int(want)
	z.next += want
	return nil
}

func (z *zreader) ReadByte() (byte, error) {
	if z.i == z.n {
		if err := z.fill(); err != nil {
			return 0, err
		}
	}
	c := z.buf[z.i]
	z.i++
	return c, nil
}

func (z *zreader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if z.i == z.n {
		if err := z.fill(); err != nil {
			return 0, err
		}
	}
	n := copy(p, z.buf[z.i:z.n])
	z.i += n
	return n, nil
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

// inflateZlibFrom decodes the zlib stream at z's position into w, requires it to
// produce exactly want bytes with a valid Adler-32, and returns its length. It
// consumes no byte beyond the stream and never allocates from the claimed length.
// On error w may already hold a prefix of the plaintext.
func inflateZlibFrom(z *zreader, want int64, w io.Writer) (int64, error) {
	start := z.pos()
	var hd [2]byte
	for k := range hd {
		c, err := z.ReadByte()
		if err != nil {
			return 0, corrupt("zlib stream truncated")
		}
		hd[k] = c
	}
	cmf, flg := hd[0], hd[1]
	if cmf&15 != 8 || cmf>>4 > 7 || (uint(cmf)<<8|uint(flg))%31 != 0 || flg&0x20 != 0 {
		return 0, corrupt("zlib header %02x%02x", cmf, flg)
	}
	fr := flate.NewReader(z)
	defer fr.Close()
	sink := &adlerSink{h: adler32.New()}
	n, err := io.Copy(io.MultiWriter(sink, w), io.LimitReader(fr, want+1))
	if err != nil {
		return 0, corrupt("inflate: %v", err)
	}
	if n != want {
		return 0, corrupt("inflated %d bytes, header says %d", n, want)
	}
	var tr [4]byte
	for k := range tr {
		c, err := z.ReadByte()
		if err != nil {
			return 0, corrupt("zlib trailer truncated")
		}
		tr[k] = c
	}
	if binary.BigEndian.Uint32(tr[:]) != sink.h.Sum32() {
		return 0, corrupt("zlib Adler-32")
	}
	return z.pos() - start, nil
}

// inflateZlib is inflateZlibFrom over an in-memory stream.
func inflateZlib(data []byte, want int64, w io.Writer) (int64, error) {
	z := getZReader()
	defer putZReader(z)
	z.reset(bytes.NewReader(data), 0, int64(len(data)), 0)
	return inflateZlibFrom(z, want, w)
}

// InflateTo streams the zlib plaintext of entry i to w: the object payload of a
// full entry, the delta program of a delta entry. The stream is verified (declared
// size, Adler-32, exact extent) as it is written; on error w may already hold a
// prefix, which the caller must discard.
func (p *Pack) InflateTo(i int, w io.Writer) error {
	if i < 0 || i >= len(p.Entries) {
		return corrupt("entry %d", i)
	}
	e := &p.Entries[i]
	z := getZReader()
	defer putZReader(z)
	z.reset(p.src, e.ZOff, e.ZEnd, 0)
	n, err := inflateZlibFrom(z, e.Size, w)
	if err == nil && n != e.ZEnd-e.ZOff {
		err = corrupt("entry %d: zlib stream is %d bytes, parsed as %d", i, n, e.ZEnd-e.ZOff)
	}
	return err
}

// Inflate is InflateTo into memory: the bounded materialisation helper (an entry's
// inflated size is limited by Limits.MaxObjectSize at parse time). Delta
// resolution and the lab's component builders still need whole payloads; this is
// the one place that materialises them.
func (p *Pack) Inflate(i int) ([]byte, error) {
	e := &p.Entries[i]
	var buf bytes.Buffer
	buf.Grow(int(min(e.Size, (e.ZEnd-e.ZOff)*1032+64)))
	if err := p.InflateTo(i, &buf); err != nil {
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
