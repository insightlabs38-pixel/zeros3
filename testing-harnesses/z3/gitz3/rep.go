package gitz3

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash/adler32"
	"io"
	"sync"

	"zeros3-z3/deflate"
	"zeros3-z3/ir"
	"zeros3-z3/lab"
)

// ---- component helpers (ZeroS3 identity is always SHA-256 of the exact bytes) ----

func cid(b []byte) lab.ID { return lab.ID(sha256.Sum256(b)) }

func mkComp(b []byte) *lab.Comp {
	return &lab.Comp{ID: cid(b), Size: int64(len(b)), Data: func() ([]byte, error) { return b, nil }}
}

const (
	cdcMax        = 256 << 10
	defaultInline = 40
	defaultUnit   = 64 << 10
)

// pieces turns bytes into recipe pieces: inline if short, one component if it
// fits in a CDC maximum chunk, otherwise production CDC chunks.
func mkPieces(b []byte, inlineMax int, class string) []lab.Piece {
	if len(b) < inlineMax {
		return []lab.Piece{{Inline: append([]byte(nil), b...), Len: int64(len(b))}}
	}
	if len(b) <= cdcMax {
		c := mkComp(b)
		c.Class = class
		return []lab.Piece{{Comp: c, Len: int64(len(b))}}
	}
	var out []lab.Piece
	_ = lab.ChunkCDC(bytes.NewReader(b), func(_ int64, ch []byte) error {
		cp := append([]byte(nil), ch...)
		c := mkComp(cp)
		c.Class = class
		out = append(out, lab.Piece{Comp: c, Len: int64(len(cp))})
		return nil
	})
	return out
}

func pieceBytes(pc lab.Piece, get func(lab.ID) ([]byte, error)) ([]byte, error) {
	if pc.Comp == nil {
		return pc.Inline, nil
	}
	return get(pc.Comp.ID)
}

// ---- memoised exact DEFLATE replay ----

// The laboratory rebuilds the same entry under many representations. Replaying a
// DEFLATE skeleton is deterministic in (skeleton, unit bytes, plaintext), so the
// replayed stream is memoised under a SHA-256 of exactly those inputs. Every
// rebuilt pack/loose file is still compared end to end with the original bytes;
// the memo only avoids recomputing identical replays.
var reconMemo = struct {
	sync.Mutex
	m map[[32]byte][]byte
	n int64
}{m: map[[32]byte][]byte{}}

func reconKey(skel []byte, plain []byte, units [][]byte) [32]byte {
	h := sha256.New()
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(skel)))
	h.Write(n[:])
	h.Write(skel)
	for _, u := range units {
		binary.BigEndian.PutUint64(n[:], uint64(len(u)))
		h.Write(n[:])
		h.Write(u)
	}
	h.Write(plain)
	var k [32]byte
	copy(k[:], h.Sum(nil))
	return k
}

// replayDeflate returns the raw DEFLATE stream for (skeleton, units, plaintext).
func replayDeflate(skel []byte, units [][]byte, plain []byte) ([]byte, error) {
	if ReplayMemoOff {
		return replayRaw(skel, units, plain)
	}
	key := reconKey(skel, plain, units)
	reconMemo.Lock()
	if b, ok := reconMemo.m[key]; ok {
		reconMemo.Unlock()
		return b, nil
	}
	reconMemo.Unlock()
	def, err := replayRaw(skel, units, plain)
	if err != nil {
		return nil, err
	}
	reconMemo.Lock()
	if reconMemo.n > 768<<20 {
		reconMemo.m, reconMemo.n = map[[32]byte][]byte{}, 0
	}
	reconMemo.m[key] = def
	reconMemo.n += int64(len(def))
	reconMemo.Unlock()
	return def, nil
}

// ReplayMemoOff disables the memo (throughput measurements replay every stream).
var ReplayMemoOff bool

func replayRaw(skel []byte, units [][]byte, plain []byte) ([]byte, error) {
	var def bytes.Buffer
	err := ir.Reconstruct(&def, skel, func(k int) ([]byte, error) {
		if k >= len(units) {
			return nil, corrupt("unit %d missing", k)
		}
		return units[k], nil
	}, plain, ir.DecodeOptions{})
	if err != nil {
		return nil, err
	}
	return def.Bytes(), nil
}

// ---- per-entry analysis ----

// Entry modes in the exact recipe.
const (
	ModeZ = 0 // the entry's zlib stream is stored as bytes (component or inline)
	ModeP = 1 // plaintext component + exact DEFLATE reconstruction (skeleton + units)
)

var knownZHdr = [][2]byte{{0x78, 0x9c}, {0x78, 0x01}, {0x78, 0x5e}, {0x78, 0xda}}

func zhdrCode(h []byte) (code byte, explicit bool) {
	for i, k := range knownZHdr {
		if h[0] == k[0] && h[1] == k[1] {
			return byte(i), false
		}
	}
	return 7, true
}

// EntryPrep is everything the recipe builders need about one entry.
type EntryPrep struct {
	Kind  uint8
	ZHdr  [2]byte
	Z     []byte // exact zlib stream
	Comp  []byte // plaintext component: canonical object (full entries) or delta program
	Size  int64  // inflated size of the zlib stream
	Skel  []byte
	Units [][]byte
	IROK  bool // exact DEFLATE reconstruction verified
}

// PackAnalysis is a resolved pack plus per-entry exact-DEFLATE analyses.
type PackAnalysis struct {
	P       *Pack
	Entries []EntryPrep
}

// AnalyzePack inflates every entry once, encodes its exact DEFLATE
// reconstruction IR and verifies the reconstruction immediately; an entry whose
// stream does not reconstruct exactly keeps IROK=false and is carried as bytes.
func AnalyzePack(p *Pack) (*PackAnalysis, error) {
	an := &PackAnalysis{P: p, Entries: make([]EntryPrep, len(p.Entries))}
	for i := range p.Entries {
		e := &p.Entries[i]
		ep := &an.Entries[i]
		ep.Kind, ep.Size = e.Kind, e.Size
		var err error
		if ep.Z, err = p.ZlibBytes(i); err != nil {
			return nil, err
		}
		copy(ep.ZHdr[:], ep.Z[:2])
		pl, err := p.Inflate(i)
		if err != nil {
			return nil, err
		}
		if e.IsDelta() {
			ep.Comp = pl
		} else {
			ep.Comp = Canonical(ObjType(e.Kind), pl)
		}
		z, err := deflate.ParseZlib(ep.Z, deflate.Options{MaxOutput: int(min(e.Size+16, 1<<31-1))})
		if err != nil {
			continue
		}
		pol := ir.Policy{Kind: ir.PStream}
		if len(pl) > defaultUnit {
			pol = ir.Policy{Kind: ir.PTokRun, Target: 16 << 10}
		}
		pk, err := ir.Encode(z.Stream, ir.Config{Codec: ir.Pred, Policy: pol, Profile: ir.AutoProfile})
		if err != nil {
			continue
		}
		out, err := replayDeflate(pk.Skeleton, pk.Units, pl)
		if err != nil || !bytes.Equal(out, ep.Z[2:len(ep.Z)-4]) {
			continue
		}
		ep.Skel, ep.Units, ep.IROK = pk.Skeleton, pk.Units, true
	}
	return an, nil
}

// ---- recipe (meta) serialisation ----

// RepOpts selects the exact pack recipe variant.
type RepOpts struct {
	Mode      string // "auto" (ModeP unless it costs more than Z by Slack), "zlib" (all ModeZ), "plain" (ModeP when verified)
	InlineMax int
	SectionK  int               // set layout: mean entries per table section (default 8)
	Slack     float64           // auto: accept ModeP up to (1+Slack) x the ModeZ cost (default 0.10; negative = 0)
	Has       func(lab.ID) bool // components already stored (family accounting); nil = none
}

func (o RepOpts) slack() float64 {
	switch {
	case o.Slack < 0:
		return 0
	case o.Slack == 0:
		return 0.10
	}
	return o.Slack
}

// chooseMode is the per-entry admission: ModeP only when the exact DEFLATE
// reconstruction verified, and (auto) only when the plaintext+reconstruction
// cost is within Slack of just keeping the compressed stream.
func (o RepOpts) chooseMode(ep *EntryPrep) int {
	if !ep.IROK {
		return ModeZ
	}
	switch o.Mode {
	case "plain":
		return ModeP
	case "zlib":
		return ModeZ
	}
	zp, _, _ := entryPieces(ep, ModeZ, o)
	pp, _, _ := entryPieces(ep, ModeP, o)
	cz := float64(o.pieceCost(zp, false))
	cp := float64(o.pieceCost(pp, true) + len(ep.Skel) + 4)
	if cp <= cz*(1+o.slack()) {
		return ModeP
	}
	return ModeZ
}

func (o RepOpts) inline() int {
	if o.InlineMax <= 0 {
		return defaultInline
	}
	return o.InlineMax
}

// PackStats describes one built plan.
type PackStats struct {
	Entries, ModeZ, ModeP, NoIR int
	ZlibBytes, PlainBytes       int64 // bytes of the zlib streams / plaintext components
	SkelBytes, UnitBytes        int64
	MetaBytes                   int64
	Deltas                      int
}

const metaMagic = 0xA1

func appendUv(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

func entryPieces(ep *EntryPrep, mode int, o RepOpts) (pcs []lab.Piece, nPlain, nUnits int) {
	if mode == ModeZ {
		return mkPieces(ep.Z, o.inline(), "zlib"), 0, 0
	}
	cls := "plain"
	if ep.Kind > 4 {
		cls = "delta"
	}
	pp := mkPieces(ep.Comp, o.inline(), cls)
	pcs = append(pcs, pp...)
	for _, u := range ep.Units {
		pcs = append(pcs, mkPieces(u, o.inline(), "unit")...)
	}
	return pcs, len(pp), len(ep.Units)
}

func (o RepOpts) pieceCost(pcs []lab.Piece, pack bool) int {
	n := 0
	for _, pc := range pcs {
		switch {
		case pc.Comp == nil:
			n += len(pc.Inline) + 1
		case o.Has != nil && o.Has(pc.Comp.ID):
			n += 34
		default:
			sz := int(pc.Comp.Size)
			if pack {
				if b, err := pc.Comp.Data(); err == nil {
					sz = int(lab.PackedLen(b))
				}
			}
			n += sz + 34
		}
	}
	return n
}

// SeqPlan builds the exact pack recipe with entries in pack order: per entry a
// meta record (kind, mode, zlib header code, size or skeleton, base distance /
// base id) and its pieces. The pack header and trailer are derived; the
// OFS_DELTA offsets are derived from the rebuilt entry offsets.
func (an *PackAnalysis) SeqPlan(o RepOpts) (*lab.Plan, *PackStats) {
	p := an.P
	st := &PackStats{Entries: len(an.Entries)}
	var meta []byte
	meta = append(meta, metaMagic, byte(p.Algo))
	meta = appendUv(meta, uint64(p.Version))
	meta = appendUv(meta, uint64(len(an.Entries)))
	pl := &lab.Plan{}
	for i := range an.Entries {
		ep := &an.Entries[i]
		e := &p.Entries[i]
		mode := o.chooseMode(ep)
		pcs, nPlain, nUnits := entryPieces(ep, mode, o)
		zc, explicit := zhdrCode(ep.ZHdr[:])
		meta = append(meta, ep.Kind|byte(mode)<<3|zc<<4)
		if explicit {
			meta = append(meta, ep.ZHdr[:]...)
		}
		switch e.Kind {
		case ofsDelta:
			meta = appendUv(meta, uint64(i-e.Base))
			st.Deltas++
		case refDelta:
			meta = append(meta, e.BaseRef...)
			st.Deltas++
		}
		if mode == ModeZ {
			meta = appendUv(meta, uint64(ep.Size))
			meta = appendUv(meta, uint64(len(pcs)))
			st.ModeZ++
			st.ZlibBytes += int64(len(ep.Z))
		} else {
			meta = appendUv(meta, uint64(nPlain))
			meta = appendUv(meta, uint64(len(ep.Skel)))
			meta = append(meta, ep.Skel...)
			meta = appendUv(meta, uint64(nUnits))
			st.ModeP++
			st.PlainBytes += int64(len(ep.Comp))
			st.SkelBytes += int64(len(ep.Skel))
			for _, u := range ep.Units {
				st.UnitBytes += int64(len(u))
			}
		}
		if !ep.IROK {
			st.NoIR++
		}
		pl.Pieces = append(pl.Pieces, pcs...)
	}
	pl.MetaBytes = int64(len(meta))
	st.MetaBytes = pl.MetaBytes
	pl.Detail = map[string]any{"meta": meta}
	pl.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		return RebuildSeq(meta, pl.Pieces, get, w)
	}
	return pl, st
}

func encodeEntryHeader(dst []byte, kind uint8, size int64) []byte {
	c := kind<<4 | byte(size&15)
	size >>= 4
	for size > 0 {
		dst = append(dst, c|0x80)
		c = byte(size & 0x7f)
		size >>= 7
	}
	return append(dst, c)
}

func encodeOfs(dst []byte, ofs uint64) []byte {
	var tmp [16]byte
	pos := len(tmp) - 1
	tmp[pos] = byte(ofs & 127)
	for ofs >>= 7; ofs > 0; ofs >>= 7 {
		ofs--
		pos--
		tmp[pos] = 128 | byte(ofs&127)
	}
	return append(dst, tmp[pos:]...)
}

type mreader struct {
	b   []byte
	pos int
	err error
}

func (m *mreader) byte() byte {
	if m.pos >= len(m.b) {
		m.err = corrupt("recipe truncated")
		return 0
	}
	c := m.b[m.pos]
	m.pos++
	return c
}

func (m *mreader) uv(limit uint64) uint64 {
	v, n := binary.Uvarint(m.b[m.pos:])
	if n <= 0 || v > limit {
		m.err = corrupt("recipe varint")
		return 0
	}
	m.pos += n
	return v
}

func (m *mreader) take(n int) []byte {
	if n < 0 || m.pos+n > len(m.b) {
		m.err = corrupt("recipe truncated")
		return nil
	}
	b := m.b[m.pos : m.pos+n]
	m.pos += n
	return b
}

// RebuildSeq deserialises the meta bytes, takes components from pieces via get
// and writes the exact pack. It is the only reconstruction path for SeqPlan.
func RebuildSeq(meta []byte, pieces []lab.Piece, get func(lab.ID) ([]byte, error), w io.Writer) error {
	m := &mreader{b: meta}
	if m.byte() != metaMagic {
		return corrupt("recipe magic")
	}
	a := Algo(m.byte())
	if a != SHA1 && a != SHA256 {
		return corrupt("recipe algo")
	}
	ver := m.uv(3)
	n := m.uv(1 << 32)
	if m.err != nil {
		return m.err
	}
	h := a.New()
	out := io.MultiWriter(w, h)
	var hdr [12]byte
	copy(hdr[:], "PACK")
	binary.BigEndian.PutUint32(hdr[4:], uint32(ver))
	binary.BigEndian.PutUint32(hdr[8:], uint32(n))
	out.Write(hdr[:])
	off := int64(12)
	offs := make([]int64, 0, min(n, 1<<20))
	pi := 0
	nextPieces := func(k int) ([]lab.Piece, error) {
		if k < 0 || pi+k > len(pieces) {
			return nil, corrupt("recipe pieces exhausted")
		}
		s := pieces[pi : pi+k]
		pi += k
		return s, nil
	}
	for i := uint64(0); i < n; i++ {
		b0 := m.byte()
		kind, mode, zc := b0&7, int(b0>>3)&1, (b0>>4)&7
		var zh [2]byte
		switch {
		case zc < 4:
			zh = knownZHdr[zc]
		case zc == 7:
			copy(zh[:], m.take(2))
		default:
			return corrupt("zlib header code")
		}
		var baseEnc []byte
		switch kind {
		case 1, 2, 3, 4:
		case ofsDelta:
			d := m.uv(i)
			if d == 0 {
				return corrupt("OFS distance")
			}
			baseEnc = encodeOfs(nil, uint64(off-offs[i-d]))
		case refDelta:
			baseEnc = append([]byte(nil), m.take(a.Size())...)
		default:
			return corrupt("entry kind")
		}
		var size int64
		var z []byte
		if mode == ModeZ {
			size = int64(m.uv(1 << 40))
			if m.err != nil {
				return m.err
			}
			nz := int(m.uv(1 << 20))
			if m.err != nil {
				return m.err
			}
			ps, err := nextPieces(nz)
			if err != nil {
				return err
			}
			for _, pc := range ps {
				b, err := pieceBytes(pc, get)
				if err != nil {
					return err
				}
				z = append(z, b...)
			}
		} else {
			nPlain := int(m.uv(1 << 20))
			skelLen := int(m.uv(1 << 30))
			skel := m.take(skelLen)
			nUnits := int(m.uv(1 << 24))
			if m.err != nil {
				return m.err
			}
			pps, err := nextPieces(nPlain)
			if err != nil {
				return err
			}
			ups, err := nextPieces(nUnits)
			if err != nil {
				return err
			}
			var comp []byte
			for _, pc := range pps {
				b, err := pieceBytes(pc, get)
				if err != nil {
					return err
				}
				comp = append(comp, b...)
			}
			plain := comp
			if kind <= 4 {
				t, sz, hl, err := ParseCanonHeader(comp)
				if err != nil || uint8(t) != kind || sz != int64(len(comp)-hl) {
					return corrupt("canonical object header in component")
				}
				plain = comp[hl:]
			}
			size = int64(len(plain))
			ubs := make([][]byte, len(ups))
			for k := range ups {
				if ubs[k], err = pieceBytes(ups[k], get); err != nil {
					return err
				}
			}
			def, err := replayDeflate(skel, ubs, plain)
			if err != nil {
				return err
			}
			z = make([]byte, 0, 6+len(def))
			z = append(z, zh[:]...)
			z = append(z, def...)
			z = binary.BigEndian.AppendUint32(z, adler32.Checksum(plain))
		}
		offs = append(offs, off)
		eh := encodeEntryHeader(nil, kind, size)
		eh = append(eh, baseEnc...)
		out.Write(eh)
		out.Write(z)
		off += int64(len(eh) + len(z))
	}
	if m.err != nil {
		return m.err
	}
	_, err := w.Write(h.Sum(nil))
	return err
}

// IdxPlan represents the pack index: if it is the byte-identical function of
// the pack it costs one flag byte and is regenerated; otherwise the caller must
// use a byte-level fallback (ok=false).
func IdxPlan(p *Pack, idx []byte, packPlan *lab.Plan) (*lab.Plan, bool) {
	bi, err := BuildIdx(p)
	if err != nil || !bytes.Equal(bi, idx) {
		return nil, false
	}
	pl := &lab.Plan{MetaBytes: 1}
	algo := p.Algo
	pl.Rebuild = func(w io.Writer, get func(lab.ID) ([]byte, error)) error {
		var buf bytes.Buffer
		if err := packPlan.Rebuild(&buf, get); err != nil {
			return err
		}
		pp, err := ParsePack(buf.Bytes(), algo, Limits{})
		if err != nil {
			return err
		}
		if _, err := pp.Resolve(); err != nil {
			return err
		}
		out, err := BuildIdx(pp)
		if err != nil {
			return err
		}
		_, err = w.Write(out)
		return err
	}
	return pl, true
}

var _ = fmt.Sprintf

// NativeEq reports, for a resolved pack analysed in exact-plaintext mode, how
// many objects have a ZeroS3 component whose SHA-256 equals the Git object id
// (SHA-256 repositories only: the component is the canonical object bytes held
// as exactly one component) and why the others do not.
type NativeEq struct {
	Objects, Equal    int
	Inline, Chunked   int // canonical bytes shorter than the inline threshold / longer than one CDC chunk
	DeltaEntries      int // delta entries: the stored component is the delta program, not the object
	EqualBytes, Total int64
}

func (an *PackAnalysis) NativeEquality(o RepOpts) NativeEq {
	var ne NativeEq
	p := an.P
	for i := range an.Entries {
		ep := &an.Entries[i]
		ne.Objects++
		ne.Total += int64(len(ep.Comp))
		e := &p.Entries[i]
		switch {
		case e.IsDelta():
			ne.DeltaEntries++
		case len(ep.Comp) < o.inline():
			ne.Inline++
		case len(ep.Comp) > cdcMax:
			ne.Chunked++
		default:
			if p.Algo == SHA256 && string(e.ID) == string(cidBytes(ep.Comp)) {
				ne.Equal++
				ne.EqualBytes += int64(len(ep.Comp))
			}
		}
	}
	return ne
}

func cidBytes(b []byte) []byte { s := sha256.Sum256(b); return s[:] }

// IRSummary describes the exact-DEFLATE side of an analysed pack.
type IRSummary struct {
	Entries, OK              int
	Zlib, Plain, Skel, Units int64 // zlib stream bytes / plaintext bytes / skeleton bytes / unit bytes over the verified entries
}

func (an *PackAnalysis) IRSummary() IRSummary {
	var s IRSummary
	s.Entries = len(an.Entries)
	for i := range an.Entries {
		ep := &an.Entries[i]
		if !ep.IROK {
			continue
		}
		s.OK++
		s.Zlib += int64(len(ep.Z))
		s.Plain += int64(len(ep.Comp))
		s.Skel += int64(len(ep.Skel))
		for _, u := range ep.Units {
			s.Units += int64(len(u))
		}
	}
	return s
}
