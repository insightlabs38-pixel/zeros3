package carz3

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"
)

// Ref is one section of the archive as a representation sees it: the block
// payload is identified by its SHA-256 (the ZeroS3 identity) and carried
// separately; the CID is either derivable from that digest or kept exactly.
type Ref struct {
	Sum    [32]byte // SHA-256 of the block bytes (ZeroS3 identity)
	Len    int64    // block bytes
	CID    []byte   // exact CID bytes when !Derived
	Prefix int      // index into Rep.Prefixes when Derived
	// Derived means CID == Prefixes[Prefix] || Sum (a sha2-256 CID).
	Derived bool
	Inline  []byte // block bytes kept in the recipe (small blocks); nil otherwise

	SrcOff int64 // lab only: absolute offset of the block bytes in the source
	Off    int64 // section offset in the CARv1 payload (lab only; recomputable)
	Ver    Verification
}

// IndexMode says how the CARv2 index is represented.
type IndexMode byte

const (
	IndexNone    IndexMode = iota // no index in the file
	IndexDerived                  // rebuilt from the entries (verified exact at build time)
	IndexExact                    // kept as exact bytes (unknown codec, or not rebuildable)
)

// Rep is the laboratory's exact representation of one CAR file. It is not a
// frozen schema; Marshal exists so that its byte cost is real.
type Rep struct {
	V2         bool
	Header     []byte // exact CARv1 header body
	Prefixes   [][]byte
	Refs       []Ref
	Char       [16]byte // CARv2 characteristics
	HdrExact   []byte   // CARv2 40-byte header kept verbatim when not derivable (else nil)
	PadBefore  []byte
	PadAfter   []byte
	IndexMode  IndexMode
	IndexCodec uint64
	IndexBytes []byte // exact index bytes when IndexMode == IndexExact
	IndexRaw   int    // size of the original index (all modes)
}

// Stats counts what Build saw.
type Stats struct {
	Sections     int
	Verified     int
	NotVerified  int
	Mismatch     int
	Derived      int
	DupCID       int // sections whose exact CID bytes appeared earlier in this file
	DupPayload   int // sections whose SHA-256 appeared earlier under any CID
	DupPayloadDC int // ... under a different native identity (CID Key)
	Bytes        int64
	BlockBytes   int64
	IndexParsed  bool
}

// Options configure Build.
type Options struct {
	Limits
	InlineMax int  // blocks shorter than this stay in the recipe (0 = every block is a component)
	Lenient   bool // keep going on a CID/payload hash mismatch instead of failing
	NoDerive  bool // keep every CID and index verbatim (the naive recipe)
	NoDupStat bool // skip the duplicate-CID / duplicate-payload sets (they cost memory per section)
}

func DefaultOptions() Options { return Options{Limits: DefaultLimits()} }

// Build parses the CAR (v1 or v2) in src and returns its representation. It
// streams: only the refs (one per section), the small CARv2 wrapper and the
// index are retained, never a block payload (except Options.InlineMax).
func Build(src io.ReaderAt, size int64, opt Options) (*Rep, *Stats, error) {
	if opt.MaxTotal == 0 {
		opt.Limits = DefaultLimits()
	}
	rep, st := &Rep{}, &Stats{}
	var inner io.Reader
	var base int64
	var v2 *V2
	head := make([]byte, min(size, V2PragmaLen))
	if _, err := src.ReadAt(head, 0); err != nil && err != io.EOF {
		return nil, nil, malformed("read: %v", err)
	}
	dataSize := size
	if IsV2(head) {
		var err error
		if v2, err = ParseV2(src, size); err != nil {
			return nil, nil, err
		}
		base, dataSize = int64(v2.DataOffset), int64(v2.DataSize)
		rep.V2 = true
	}
	if dataSize > opt.MaxTotal {
		return nil, nil, malformed("archive over %d bytes", opt.MaxTotal)
	}
	inner = io.NewSectionReader(src, base, dataSize)
	opt.MaxTotal = min(opt.MaxTotal, dataSize) // a section cannot claim bytes past the archive
	rd, err := NewReader(inner, opt.Limits)
	if err != nil {
		return nil, nil, err
	}
	rep.Header = append([]byte(nil), rd.Header().Raw...)
	prefixIdx := map[string]int{}
	seenCID := map[string]bool{}
	seenSum := map[[32]byte]string{}
	var recs []IndexRec
	for {
		e, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		st.Sections++
		sum := sha256.Sum256(e.Data)
		r := Ref{Sum: sum, Len: int64(len(e.Data)), SrcOff: base + e.DataOff, Off: e.Off}
		r.Ver = e.CID.Verify(e.Data)
		switch r.Ver {
		case Verified:
			st.Verified++
		case NotVerified:
			st.NotVerified++
		case Mismatch:
			st.Mismatch++
			if !opt.Lenient {
				return nil, nil, malformed("block %d: CID multihash does not match payload", st.Sections-1)
			}
		}
		if !opt.NoDerive && e.CID.DerivableFrom256(sum) {
			p := string(e.CID.Raw[:e.CID.Prefix])
			k, ok := prefixIdx[p]
			if !ok {
				k = len(rep.Prefixes)
				prefixIdx[p] = k
				rep.Prefixes = append(rep.Prefixes, []byte(p))
			}
			r.Derived, r.Prefix = true, k
			st.Derived++
		} else {
			r.CID = append([]byte(nil), e.CID.Raw...)
		}
		if !opt.NoDupStat {
			if seenCID[string(e.CID.Raw)] {
				st.DupCID++
			}
			seenCID[string(e.CID.Raw)] = true
			if k0, ok := seenSum[sum]; ok {
				st.DupPayload++
				if k0 != e.CID.Key() {
					st.DupPayloadDC++
				}
			} else {
				seenSum[sum] = e.CID.Key()
			}
		}
		if int64(len(e.Data)) < int64(opt.InlineMax) {
			r.Inline = append([]byte(nil), e.Data...)
		}
		st.Bytes += e.End() - e.Off
		st.BlockBytes += int64(len(e.Data))
		if v2 != nil && v2.IndexRaw != nil && e.CID.HashCode != MhIdentity { // writers do not index identity CIDs
			recs = append(recs, IndexRec{Mh: e.CID.HashCode, Digest: append([]byte(nil), e.CID.Digest...), Offset: uint64(e.Off)})
		}
		rep.Refs = append(rep.Refs, r)
	}
	if rd.Pos() != dataSize {
		return nil, nil, malformed("payload ends at %d, expected %d", rd.Pos(), dataSize)
	}
	if v2 != nil {
		copy(rep.Char[:], v2.HeaderRaw[:16])
		rep.PadBefore, rep.PadAfter = v2.PadBefore, v2.PadAfter
		hdr := DeriveHeader(rep.Char[:], len(v2.PadBefore), dataSize, len(v2.PadAfter), v2.IndexRaw != nil)
		if hdr != v2.HeaderRaw {
			rep.HdrExact = append([]byte(nil), v2.HeaderRaw[:]...)
		}
		rep.IndexRaw = len(v2.IndexRaw)
		switch {
		case v2.IndexRaw == nil:
			rep.IndexMode = IndexNone
		default:
			idx, ok, err := ParseIndex(v2.IndexRaw)
			if err != nil {
				return nil, nil, err
			}
			st.IndexParsed = ok
			rep.IndexMode = IndexExact
			if ok {
				rep.IndexCodec = idx.Codec
				if !opt.NoDerive && bytes.Equal(BuildIndex(idx.Codec, indexRecsFor(idx.Codec, recs)), v2.IndexRaw) {
					rep.IndexMode = IndexDerived
				}
			}
			if rep.IndexMode == IndexExact {
				rep.IndexBytes = v2.IndexRaw
			}
		}
	}
	return rep, st, nil
}

// indexRecsFor adapts per-section records to the index codec: IndexSorted has
// no multihash-code level.
func indexRecsFor(codec uint64, recs []IndexRec) []IndexRec {
	if codec == MultihashIndexSortedCodec {
		return recs
	}
	out := make([]IndexRec, len(recs))
	for i, r := range recs {
		out[i] = IndexRec{Digest: r.Digest, Offset: r.Offset}
	}
	return out
}

// Getter returns the payload of the block with the given SHA-256.
type Getter func(sum [32]byte) ([]byte, error)

func (r *Ref) cid(rep *Rep) []byte {
	if r.Derived {
		return append(append([]byte(nil), rep.Prefixes[r.Prefix]...), r.Sum[:]...)
	}
	return r.CID
}

func (r *Ref) data(get Getter) ([]byte, error) {
	if r.Inline != nil {
		return r.Inline, nil
	}
	b, err := get(r.Sum)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != r.Len || sha256.Sum256(b) != r.Sum {
		return nil, fmt.Errorf("carz3: component %x failed length/SHA-256 check", r.Sum[:6])
	}
	return b, nil
}

// section appends the encoded section of ref r to b.
func (rep *Rep) section(b []byte, r *Ref, get Getter) ([]byte, error) {
	d, err := r.data(get)
	if err != nil {
		return nil, err
	}
	cid := r.cid(rep)
	b = AppendUvarint(b, uint64(len(cid))+uint64(len(d)))
	b = append(b, cid...)
	return append(b, d...), nil
}

// PayloadSize is the size of the CARv1 stream.
func (rep *Rep) PayloadSize() int64 {
	n := int64(len(AppendUvarint(nil, uint64(len(rep.Header))))) + int64(len(rep.Header))
	for i := range rep.Refs {
		r := &rep.Refs[i]
		cl := int64(len(r.CID))
		if r.Derived {
			cl = int64(len(rep.Prefixes[r.Prefix]) + 32)
		}
		n += int64(len(AppendUvarint(nil, uint64(cl+r.Len)))) + cl + r.Len
	}
	return n
}

// Reconstruct writes the exact original archive bytes.
func (rep *Rep) Reconstruct(w io.Writer, get Getter) error {
	var idx []byte
	if rep.V2 {
		var hdr [V2HeaderLen]byte
		ds := rep.PayloadSize()
		if rep.HdrExact != nil {
			copy(hdr[:], rep.HdrExact)
		} else {
			hdr = DeriveHeader(rep.Char[:], len(rep.PadBefore), ds, len(rep.PadAfter), rep.IndexMode != IndexNone)
		}
		for _, b := range [][]byte{V2Pragma, hdr[:], rep.PadBefore} {
			if _, err := w.Write(b); err != nil {
				return err
			}
		}
		idx = rep.indexBytes()
	}
	buf := AppendUvarint(nil, uint64(len(rep.Header)))
	buf = append(buf, rep.Header...)
	if _, err := w.Write(buf); err != nil {
		return err
	}
	for i := range rep.Refs {
		var err error
		if buf, err = rep.section(buf[:0], &rep.Refs[i], get); err != nil {
			return err
		}
		if _, err := w.Write(buf); err != nil {
			return err
		}
	}
	if rep.V2 {
		if _, err := w.Write(rep.PadAfter); err != nil {
			return err
		}
		if _, err := w.Write(idx); err != nil {
			return err
		}
	}
	return nil
}

// indexBytes returns the index as stored or rebuilt.
func (rep *Rep) indexBytes() []byte {
	switch rep.IndexMode {
	case IndexExact:
		return rep.IndexBytes
	case IndexDerived:
		return BuildIndex(rep.IndexCodec, rep.indexRecs())
	}
	return nil
}

// indexRecs recomputes the index records from the refs alone. Offsets are
// recovered by walking the sections; digests come from the CIDs.
func (rep *Rep) indexRecs() []IndexRec {
	off := int64(len(AppendUvarint(nil, uint64(len(rep.Header))))) + int64(len(rep.Header))
	recs := make([]IndexRec, 0, len(rep.Refs))
	for i := range rep.Refs {
		r := &rep.Refs[i]
		cid := r.cid(rep)
		c, _, _ := ParseCID(cid)
		if c.HashCode != MhIdentity {
			rec := IndexRec{Digest: c.Digest, Offset: uint64(off)}
			if rep.IndexCodec == MultihashIndexSortedCodec {
				rec.Mh = c.HashCode
			}
			recs = append(recs, rec)
		}
		off += int64(len(AppendUvarint(nil, uint64(len(cid))+uint64(r.Len)))) + int64(len(cid)) + r.Len
	}
	return recs
}

// Sizes breaks the encoded recipe into the parts the Z3-14 report names.
type Sizes struct {
	Header    int // CARv1 header body + length
	Wrapper   int // CARv2 characteristics, padding, header exceptions
	Index     int // exact index bytes stored (0 when derived/none)
	Prefix    int // distinct CID prefix table
	CIDExact  int // CIDs that are not derivable, kept verbatim
	BlockTbl  int // distinct block digests + lengths
	Order     int // per-section block index + CID mode
	InlineB   int // block bytes carried inside the recipe
	Total     int
	NBlocks   int // distinct non-inline blocks
	NSections int
}

// Marshal serialises the recipe and reports its parts.
func (rep *Rep) Marshal() ([]byte, Sizes) {
	h, t, e, sz := rep.MarshalParts(false)
	return append(append(h, t...), e...), sz
}

// MarshalParts serialises the recipe as head (header, wrapper, index, CID
// prefix table), the distinct-block table, and tail (the per-section order).
// With sorted the table is ordered by digest, so that two archives holding
// the same set of blocks produce identical table bytes whatever their order.
func (rep *Rep) MarshalParts(sorted bool) (head, table, tail []byte, sz Sizes) {
	var b []byte
	flags := byte(0)
	if rep.V2 {
		flags |= 1
	}
	b = append(b, 1, flags|byte(rep.IndexMode)<<1)
	n0 := len(b)
	b = AppendUvarint(b, uint64(len(rep.Header)))
	b = append(b, rep.Header...)
	sz.Header = len(b) - n0
	n0 = len(b)
	if rep.V2 {
		b = append(b, rep.Char[:]...)
		b = AppendUvarint(b, rep.IndexCodec)
		b = AppendUvarint(b, uint64(len(rep.PadBefore)))
		b = append(b, rep.PadBefore...)
		b = AppendUvarint(b, uint64(len(rep.PadAfter)))
		b = append(b, rep.PadAfter...)
		b = AppendUvarint(b, uint64(len(rep.HdrExact)))
		b = append(b, rep.HdrExact...)
		sz.Wrapper = len(b) - n0
		n0 = len(b)
		if rep.IndexMode == IndexExact {
			b = AppendUvarint(b, uint64(len(rep.IndexBytes)))
			b = append(b, rep.IndexBytes...)
		}
		sz.Index = len(b) - n0
	}
	n0 = len(b)
	b = AppendUvarint(b, uint64(len(rep.Prefixes)))
	for _, p := range rep.Prefixes {
		b = AppendUvarint(b, uint64(len(p)))
		b = append(b, p...)
	}
	sz.Prefix = len(b) - n0
	head = b
	b = nil
	tbl := map[[32]byte]int{}
	var order []*Ref
	for i := range rep.Refs {
		r := &rep.Refs[i]
		if r.Inline == nil {
			if _, ok := tbl[r.Sum]; !ok {
				tbl[r.Sum] = len(order)
				order = append(order, r)
			}
		}
	}
	if sorted {
		sort.Slice(order, func(i, j int) bool { return bytes.Compare(order[i].Sum[:], order[j].Sum[:]) < 0 })
		for i, r := range order {
			tbl[r.Sum] = i
		}
	}
	n0 = len(b)
	b = AppendUvarint(b, uint64(len(order)))
	for _, r := range order {
		b = append(b, r.Sum[:]...)
		b = AppendUvarint(b, uint64(r.Len))
	}
	sz.BlockTbl, sz.NBlocks, sz.NSections = len(b)-n0, len(order), len(rep.Refs)
	table = b
	b = nil
	n0 = len(b)
	b = AppendUvarint(b, uint64(len(rep.Refs)))
	for i := range rep.Refs {
		r := &rep.Refs[i]
		if r.Inline != nil {
			b = AppendUvarint(b, 0)
			b = AppendUvarint(b, uint64(len(r.Inline)))
			b = append(b, r.Inline...)
			sz.InlineB += len(r.Inline)
		} else {
			b = AppendUvarint(b, uint64(tbl[r.Sum])+1)
		}
		if r.Derived {
			b = AppendUvarint(b, uint64(r.Prefix)+1)
		} else {
			b = AppendUvarint(b, 0)
			b = AppendUvarint(b, uint64(len(r.CID)))
			b = append(b, r.CID...)
			sz.CIDExact += len(r.CID)
		}
	}
	sz.Order = len(b) - n0 - sz.CIDExact - sz.InlineB
	sz.Total = len(head) + len(table) + len(b)
	return head, table, b, sz
}

// Unmarshal parses a Marshal output (bounds-checked; used by exactness tests).
func Unmarshal(b []byte) (*Rep, error) {
	p := 0
	u := func() (uint64, error) {
		v, n, err := Uvarint(b[p:])
		if err != nil {
			return 0, err
		}
		if n == 0 {
			return 0, malformed("recipe truncated")
		}
		p += n
		return v, nil
	}
	take := func(n uint64) ([]byte, error) {
		if n > uint64(len(b)-p) {
			return nil, malformed("recipe truncated")
		}
		x := b[p : p+int(n)]
		p += int(n)
		return append([]byte(nil), x...), nil
	}
	if len(b) < 2 || b[0] != 1 {
		return nil, malformed("recipe version")
	}
	rep := &Rep{V2: b[1]&1 != 0, IndexMode: IndexMode(b[1] >> 1)}
	p = 2
	var err error
	var n uint64
	if n, err = u(); err != nil {
		return nil, err
	}
	if rep.Header, err = take(n); err != nil {
		return nil, err
	}
	if rep.V2 {
		c, err := take(16)
		if err != nil {
			return nil, err
		}
		copy(rep.Char[:], c)
		if rep.IndexCodec, err = u(); err != nil {
			return nil, err
		}
		for _, dst := range []*[]byte{&rep.PadBefore, &rep.PadAfter, &rep.HdrExact} {
			if n, err = u(); err != nil {
				return nil, err
			}
			if *dst, err = take(n); err != nil {
				return nil, err
			}
		}
		if len(rep.HdrExact) == 0 {
			rep.HdrExact = nil
		}
		if rep.IndexMode == IndexExact {
			if n, err = u(); err != nil {
				return nil, err
			}
			if rep.IndexBytes, err = take(n); err != nil {
				return nil, err
			}
		}
	}
	if n, err = u(); err != nil {
		return nil, err
	}
	for i := uint64(0); i < n; i++ {
		l, err := u()
		if err != nil {
			return nil, err
		}
		x, err := take(l)
		if err != nil {
			return nil, err
		}
		rep.Prefixes = append(rep.Prefixes, x)
	}
	if n, err = u(); err != nil {
		return nil, err
	}
	type te struct {
		sum [32]byte
		ln  int64
	}
	var tbl []te
	for i := uint64(0); i < n; i++ {
		s, err := take(32)
		if err != nil {
			return nil, err
		}
		l, err := u()
		if err != nil {
			return nil, err
		}
		var t te
		copy(t.sum[:], s)
		t.ln = int64(l)
		tbl = append(tbl, t)
	}
	if n, err = u(); err != nil {
		return nil, err
	}
	for i := uint64(0); i < n; i++ {
		var r Ref
		k, err := u()
		if err != nil {
			return nil, err
		}
		if k == 0 {
			l, err := u()
			if err != nil {
				return nil, err
			}
			if r.Inline, err = take(l); err != nil {
				return nil, err
			}
			r.Len, r.Sum = int64(len(r.Inline)), sha256.Sum256(r.Inline)
		} else {
			if k > uint64(len(tbl)) {
				return nil, malformed("block index out of range")
			}
			r.Sum, r.Len = tbl[k-1].sum, tbl[k-1].ln
		}
		m, err := u()
		if err != nil {
			return nil, err
		}
		if m == 0 {
			l, err := u()
			if err != nil {
				return nil, err
			}
			if r.CID, err = take(l); err != nil {
				return nil, err
			}
		} else {
			if m > uint64(len(rep.Prefixes)) {
				return nil, malformed("prefix index out of range")
			}
			r.Derived, r.Prefix = true, int(m-1)
		}
		rep.Refs = append(rep.Refs, r)
	}
	if p != len(b) {
		return nil, malformed("trailing recipe bytes")
	}
	return rep, nil
}

// ReadRange materialises [off, off+n) of the original archive from the
// recipe. It never touches components whose section lies outside the range
// (CARv2 wrapper, header and index bytes are served from the recipe itself).
// fetched is the component payload bytes requested; scanned is the number of
// recipe entries walked to find the range (offsets are not stored, they are
// recomputed from lengths).
func (rep *Rep) ReadRange(off, n int64, get Getter) (out []byte, fetched int64, scanned int, err error) {
	var pre, mid []byte // bytes before the payload, and the v1 header
	var idx []byte
	if rep.V2 {
		var hdr [V2HeaderLen]byte
		if rep.HdrExact != nil {
			copy(hdr[:], rep.HdrExact)
		} else {
			hdr = DeriveHeader(rep.Char[:], len(rep.PadBefore), rep.PayloadSize(), len(rep.PadAfter), rep.IndexMode != IndexNone)
		}
		pre = append(append(append(pre, V2Pragma...), hdr[:]...), rep.PadBefore...)
		idx = rep.indexBytes()
	}
	mid = append(AppendUvarint(nil, uint64(len(rep.Header))), rep.Header...)
	head := append(pre, mid...)
	pos := int64(0)
	emit := func(b []byte) {
		end := pos + int64(len(b))
		if end > off && pos < off+n {
			out = append(out, b[max(off, pos)-pos:min(off+n, end)-pos]...)
		}
		pos = end
	}
	emit(head)
	for i := range rep.Refs {
		r := &rep.Refs[i]
		cidLen := int64(len(r.CID))
		if r.Derived {
			cidLen = int64(len(rep.Prefixes[r.Prefix]) + 32)
		}
		secLen := int64(len(AppendUvarint(nil, uint64(cidLen+r.Len)))) + cidLen + r.Len
		scanned++
		if pos+secLen <= off || pos >= off+n {
			pos += secLen
			if pos >= off+n {
				break
			}
			continue
		}
		var sec []byte
		if sec, err = rep.section(nil, r, get); err != nil {
			return nil, fetched, scanned, err
		}
		if r.Inline == nil {
			fetched += r.Len
		}
		emit(sec)
		if pos >= off+n {
			break
		}
	}
	if pos < off+n {
		emit(rep.PadAfter)
		emit(idx)
	}
	if off+n > pos {
		return nil, fetched, scanned, fmt.Errorf("carz3: range [%d,+%d) beyond archive of %d bytes", off, n, pos)
	}
	return out, fetched, scanned, nil
}

// IndexBytesOrRebuilt returns the index as stored (exact) or rebuilt (derived).
func (rep *Rep) IndexBytesOrRebuilt() []byte { return rep.indexBytes() }

// CIDBytes returns the exact CID bytes of section i.
func (rep *Rep) CIDBytes(i int) []byte { return rep.Refs[i].cid(rep) }
