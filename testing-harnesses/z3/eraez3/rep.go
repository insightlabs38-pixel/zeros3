package eraez3

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"sort"
)

// Framing encoders the lab can reproduce. A framed entry is stored as plaintext
// only if one of them regenerates the stored bytes exactly.
const (
	EncRef byte = 0 // the reference buffered Snappy writer
	EncRaw byte = 1 // framing with uncompressed chunks only
)

func encodeWith(enc byte, plain []byte) []byte {
	if enc == EncRaw {
		return FrameEncodeRaw(plain)
	}
	return FrameEncodeRef(plain)
}

// Seg is a run of a repeated type pattern: Types repeated Repeat times.
type Seg struct {
	Types  []uint16
	Repeat int
}

// IndexMode says how the trailing component index is represented.
type IndexMode byte

const (
	IndexAbsent   IndexMode = iota // the file has no trailing index
	IndexDerived                   // rebuilt from entry positions (verified byte-exact at build)
	IndexVerbatim                  // kept as an ordinary verbatim entry
)

// Rep is the exact representation of one EraE file.
type Rep struct {
	Layout     []Seg
	Streams    map[uint16][]byte // plaintext streams: framed-Snappy types (RLP-delimited) and TD (32-byte values)
	Verbatim   [][]byte          // payloads kept as stored (version, accumulator, non-canonical or unknown entries)
	Exceptions []int             // entry indexes whose payload is verbatim although the type has a stream
	Enc        []byte            // framing encoder of each stream-coded framed entry, in entry order (EncRef / EncRaw)
	IndexMode  IndexMode
	IndexStart uint64   // first block number (not derivable from the entries)
	CompTypes  []uint16 // component order of the index
	IndexLen   int      // size of the original index entry payload
	Entries    int      // entries before the index
}

// Stats counts what Build saw.
type Stats struct {
	Entries                   int
	Plain, Fixed, Verbatim    int
	NonCanonical              int // framed entries no known encoder reproduces
	RawFramed                 int // framed entries reproduced by the uncompressed-chunk encoder
	CompressedBytes           int64
	PlainBytes                int64
	IndexBytes                int
	IndexDerived              bool
	Blocks                    int
	PerType                   map[uint16]int
	PerTypeFile, PerTypePlain map[uint16]int64
}

func streamKind(t uint16) (plain, fixed bool) {
	switch t {
	case TypeHeader, TypeBody, TypeReceipts, TypeSlimReceipts, TypeProof:
		return true, false
	case TypeTD:
		return false, true
	}
	return false, false
}

func compressLayout(types []uint16) []Seg {
	var out []Seg
	for i := 0; i < len(types); {
		bestP, bestR := 1, 1
		for p := 1; p <= 8 && i+p <= len(types); p++ {
			r := 1
			for i+(r+1)*p <= len(types) && equalTypes(types[i:i+p], types[i+r*p:i+(r+1)*p]) {
				r++
			}
			if (r >= 2 || p == 1) && r*p > bestP*bestR {
				bestP, bestR = p, r
			}
		}
		out = append(out, Seg{Types: append([]uint16(nil), types[i:i+bestP]...), Repeat: bestR})
		i += bestP * bestR
	}
	return out
}

func equalTypes(a, b []uint16) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (rep *Rep) expand() []uint16 {
	var out []uint16
	for _, s := range rep.Layout {
		for r := 0; r < s.Repeat; r++ {
			out = append(out, s.Types...)
		}
	}
	return out
}

// Build parses src and returns its representation. With verify it
// reconstructs the representation and compares the SHA-256 with the source:
// anything the representation cannot reproduce exactly is refused.
func Build(src io.ReaderAt, size int64, lim Limits, verify bool) (*Rep, *Stats, error) {
	ents, err := Parse(src, size, lim)
	if err != nil {
		return nil, nil, err
	}
	rep := &Rep{Streams: map[uint16][]byte{}}
	st := &Stats{PerType: map[uint16]int{}, PerTypeFile: map[uint16]int64{}, PerTypePlain: map[uint16]int64{}}
	last := ents[len(ents)-1]
	body := ents
	if last.Type == TypeComponentIndex && len(ents) > 1 {
		body = ents[:len(ents)-1]
	}
	rep.Entries = len(body)
	types := make([]uint16, len(body))
	read := func(e Entry) ([]byte, error) {
		b := make([]byte, e.Len)
		if e.Len == 0 {
			return b, nil
		}
		if _, err := src.ReadAt(b, e.Off+8); err != nil && !(err == io.EOF) {
			return nil, malformed("read: %v", err)
		}
		return b, nil
	}
	for i, e := range body {
		types[i] = e.Type
		st.Entries++
		st.PerType[e.Type]++
		st.PerTypeFile[e.Type] += int64(e.Len)
		p, err := read(e)
		if err != nil {
			return nil, nil, err
		}
		isPlain, isFixed := streamKind(e.Type)
		switch {
		case isPlain:
			plain, derr := FrameDecode(p)
			if derr == nil {
				if n, ok := rlpItemLen(plain); ok && n == len(plain) {
					found := false
					for _, enc := range []byte{EncRef, EncRaw} {
						if bytes.Equal(encodeWith(enc, plain), p) {
							rep.Enc = append(rep.Enc, enc)
							rep.Streams[e.Type] = append(rep.Streams[e.Type], plain...)
							st.Plain++
							if enc == EncRaw {
								st.RawFramed++
							}
							st.CompressedBytes += int64(len(p))
							st.PlainBytes += int64(len(plain))
							st.PerTypePlain[e.Type] += int64(len(plain))
							found = true
							break
						}
					}
					if found {
						continue
					}
					st.NonCanonical++
				}
			}
			rep.Exceptions = append(rep.Exceptions, i)
			rep.Verbatim = append(rep.Verbatim, p)
			st.Verbatim++
		case isFixed && len(p) == 32:
			rep.Streams[e.Type] = append(rep.Streams[e.Type], p...)
			st.Fixed++
			st.PerTypePlain[e.Type] += 32
		default:
			if isFixed {
				rep.Exceptions = append(rep.Exceptions, i)
			}
			rep.Verbatim = append(rep.Verbatim, p)
			st.Verbatim++
		}
	}
	rep.Layout = compressLayout(types)
	if len(body) < len(ents) {
		ip, err := read(last)
		if err != nil {
			return nil, nil, err
		}
		rep.IndexLen = len(ip)
		st.IndexBytes = len(ip)
		rep.IndexMode = IndexVerbatim
		if start, comps, blocks, ok := parseComponentIndex(ip); ok {
			ct := componentTypes(types, int(comps))
			if ct != nil {
				rep.IndexStart, rep.CompTypes = start, ct
				st.Blocks = int(blocks)
				pos := entryPositions(rep, body)
				if bytes.Equal(buildComponentIndex(rep, pos, last.Off, int(blocks)), ip) {
					rep.IndexMode = IndexDerived
					st.IndexDerived = true
				}
			}
		}
		if rep.IndexMode == IndexVerbatim {
			rep.Verbatim = append(rep.Verbatim, ip)
		}
	} else {
		rep.IndexMode = IndexAbsent
	}
	if verify {
		h := sha256.New()
		if err := rep.Reconstruct(h, rep.Streams); err != nil {
			return nil, nil, unsupported("reconstruction failed: %v", err)
		}
		want := sha256.New()
		if _, err := io.Copy(want, io.NewSectionReader(src, 0, size)); err != nil {
			return nil, nil, err
		}
		if !bytes.Equal(h.Sum(nil), want.Sum(nil)) {
			return nil, nil, unsupported("representation does not reproduce the file")
		}
	}
	return rep, st, nil
}

func entryPositions(rep *Rep, body []Entry) []int64 {
	pos := make([]int64, len(body))
	for i, e := range body {
		pos[i] = e.Off
	}
	return pos
}

// componentTypes derives the component order (distinct non-framing types in
// first-appearance order) for an index with n components.
func componentTypes(types []uint16, n int) []uint16 {
	var ct []uint16
	seen := map[uint16]bool{}
	for _, t := range types {
		if t == TypeVersion || t == TypeAccumulator || seen[t] {
			continue
		}
		seen[t] = true
		ct = append(ct, t)
	}
	if len(ct) != n {
		return nil
	}
	return ct
}

func parseComponentIndex(p []byte) (start uint64, comps, count uint64, ok bool) {
	if len(p) < 24 {
		return 0, 0, 0, false
	}
	start = binary.LittleEndian.Uint64(p)
	comps = binary.LittleEndian.Uint64(p[len(p)-16:])
	count = binary.LittleEndian.Uint64(p[len(p)-8:])
	if comps == 0 || comps > 16 || count > 1<<24 || uint64(len(p)) != 24+8*comps*count {
		return 0, 0, 0, false
	}
	return start, comps, count, true
}

// buildComponentIndex rebuilds the index payload: start, then for each block
// and component the offset of that block's entry of the component's type,
// relative to the index entry's own position, then component count and count.
func buildComponentIndex(rep *Rep, pos []int64, indexPos int64, blocks int) []byte {
	byType := map[uint16][]int64{}
	types := rep.expand()
	for i, t := range types {
		byType[t] = append(byType[t], pos[i])
	}
	out := binary.LittleEndian.AppendUint64(nil, rep.IndexStart)
	for b := 0; b < blocks; b++ {
		for _, t := range rep.CompTypes {
			l := byType[t]
			var off int64
			if b < len(l) {
				off = l[b] - indexPos
			}
			out = binary.LittleEndian.AppendUint64(out, uint64(off))
		}
	}
	out = binary.LittleEndian.AppendUint64(out, uint64(len(rep.CompTypes)))
	return binary.LittleEndian.AppendUint64(out, uint64(blocks))
}

// Reconstruct writes the exact archive bytes, taking stream bytes from streams.
func (rep *Rep) Reconstruct(w io.Writer, streams map[uint16][]byte) error {
	types := rep.expand()
	cur := map[uint16]int{}
	exc := map[int]bool{}
	for _, i := range rep.Exceptions {
		exc[i] = true
	}
	vi, pi := 0, 0
	var pos int64
	positions := make([]int64, 0, len(types))
	var buf []byte
	for i, t := range types {
		positions = append(positions, pos)
		var payload []byte
		isPlain, isFixed := streamKind(t)
		switch {
		case exc[i] || (!isPlain && !isFixed):
			if vi >= len(rep.Verbatim) {
				return malformed("verbatim list exhausted")
			}
			payload = rep.Verbatim[vi]
			vi++
		case isPlain:
			s := streams[t]
			n, ok := rlpItemLen(s[cur[t]:])
			if !ok {
				return malformed("stream %#x exhausted or corrupt", t)
			}
			if pi >= len(rep.Enc) {
				return malformed("encoder list exhausted")
			}
			payload = encodeWith(rep.Enc[pi], s[cur[t]:cur[t]+n])
			pi++
			cur[t] += n
		default:
			s := streams[t]
			if cur[t]+32 > len(s) {
				return malformed("stream %#x exhausted", t)
			}
			payload = s[cur[t] : cur[t]+32]
			cur[t] += 32
		}
		buf = appendEntry(buf[:0], t, payload)
		if _, err := w.Write(buf); err != nil {
			return err
		}
		pos += int64(len(buf))
	}
	switch rep.IndexMode {
	case IndexDerived:
		blocks := 0
		if len(rep.CompTypes) > 0 {
			for _, t := range types {
				if t == rep.CompTypes[0] {
					blocks++
				}
			}
		}
		buf = appendEntry(buf[:0], TypeComponentIndex, buildComponentIndex(rep, positions, pos, blocks))
		_, err := w.Write(buf)
		return err
	case IndexVerbatim:
		if vi >= len(rep.Verbatim) {
			return malformed("verbatim list exhausted at index")
		}
		buf = appendEntry(buf[:0], TypeComponentIndex, rep.Verbatim[vi])
		_, err := w.Write(buf)
		return err
	}
	return nil
}

// Sizes of the recipe parts (excluding the plaintext streams, which are
// components of their own).
type Sizes struct {
	Layout, Verbatim, Exceptions, Index, Total int
}

// Marshal serialises the recipe: layout, verbatim entries and exceptions, index
// parameters. Streams are not included.
func (rep *Rep) Marshal() ([]byte, Sizes) {
	var sz Sizes
	var b []byte
	b = append(b, 1, byte(rep.IndexMode))
	n0 := len(b)
	b = binary.AppendUvarint(b, uint64(len(rep.Layout)))
	for _, s := range rep.Layout {
		b = binary.AppendUvarint(b, uint64(len(s.Types)))
		for _, t := range s.Types {
			b = binary.AppendUvarint(b, uint64(t))
		}
		b = binary.AppendUvarint(b, uint64(s.Repeat))
	}
	sz.Layout = len(b) - n0
	n0 = len(b)
	b = binary.AppendUvarint(b, uint64(len(rep.Verbatim)))
	for _, v := range rep.Verbatim {
		b = binary.AppendUvarint(b, uint64(len(v)))
		b = append(b, v...)
	}
	sz.Verbatim = len(b) - n0
	n0 = len(b)
	ex := append([]int(nil), rep.Exceptions...)
	sort.Ints(ex)
	b = binary.AppendUvarint(b, uint64(len(ex)))
	prev := 0
	for _, e := range ex {
		b = binary.AppendUvarint(b, uint64(e-prev))
		prev = e
	}
	sz.Exceptions = len(b) - n0
	n0 = len(b)
	// encoder ids, run-length coded
	var runs [][2]int
	for _, e := range rep.Enc {
		if len(runs) > 0 && runs[len(runs)-1][0] == int(e) {
			runs[len(runs)-1][1]++
		} else {
			runs = append(runs, [2]int{int(e), 1})
		}
	}
	b = binary.AppendUvarint(b, uint64(len(runs)))
	for _, r := range runs {
		b = binary.AppendUvarint(b, uint64(r[0]))
		b = binary.AppendUvarint(b, uint64(r[1]))
	}
	sz.Exceptions += len(b) - n0
	n0 = len(b)
	if rep.IndexMode == IndexDerived {
		b = binary.LittleEndian.AppendUint64(b, rep.IndexStart)
		b = binary.AppendUvarint(b, uint64(len(rep.CompTypes)))
		for _, t := range rep.CompTypes {
			b = binary.AppendUvarint(b, uint64(t))
		}
	}
	sz.Index = len(b) - n0
	sz.Total = len(b)
	return b, sz
}

// Unmarshal parses a Marshal output (bounds-checked).
func Unmarshal(b []byte) (*Rep, error) {
	if len(b) < 2 || b[0] != 1 {
		return nil, malformed("recipe version")
	}
	rep := &Rep{IndexMode: IndexMode(b[1]), Streams: map[uint16][]byte{}}
	p := 2
	u := func() (uint64, error) {
		v, n := binary.Uvarint(b[p:])
		if n <= 0 {
			return 0, malformed("recipe truncated")
		}
		p += n
		return v, nil
	}
	n, err := u()
	if err != nil || n > 1<<20 {
		return nil, malformed("layout")
	}
	for i := uint64(0); i < n; i++ {
		var s Seg
		k, err := u()
		if err != nil || k == 0 || k > 8 {
			return nil, malformed("layout pattern")
		}
		for j := uint64(0); j < k; j++ {
			t, err := u()
			if err != nil {
				return nil, err
			}
			s.Types = append(s.Types, uint16(t))
		}
		r, err := u()
		if err != nil || r > 1<<24 {
			return nil, malformed("layout repeat")
		}
		s.Repeat = int(r)
		rep.Layout = append(rep.Layout, s)
	}
	if n, err = u(); err != nil || n > 1<<24 {
		return nil, malformed("verbatim count")
	}
	for i := uint64(0); i < n; i++ {
		l, err := u()
		if err != nil || l > uint64(len(b)-p) {
			return nil, malformed("verbatim entry")
		}
		rep.Verbatim = append(rep.Verbatim, append([]byte(nil), b[p:p+int(l)]...))
		p += int(l)
	}
	if n, err = u(); err != nil || n > 1<<24 {
		return nil, malformed("exception count")
	}
	prev := 0
	for i := uint64(0); i < n; i++ {
		d, err := u()
		if err != nil {
			return nil, err
		}
		prev += int(d)
		rep.Exceptions = append(rep.Exceptions, prev)
	}
	if n, err = u(); err != nil || n > 1<<24 {
		return nil, malformed("encoder runs")
	}
	for i := uint64(0); i < n; i++ {
		e, err := u()
		if err != nil || e > 1 {
			return nil, malformed("encoder id")
		}
		c, err := u()
		if err != nil || c > 1<<24 {
			return nil, malformed("encoder run")
		}
		for j := uint64(0); j < c; j++ {
			rep.Enc = append(rep.Enc, byte(e))
		}
	}
	if rep.IndexMode == IndexDerived {
		if len(b)-p < 8 {
			return nil, malformed("index start")
		}
		rep.IndexStart = binary.LittleEndian.Uint64(b[p:])
		p += 8
		k, err := u()
		if err != nil || k == 0 || k > 16 {
			return nil, malformed("component types")
		}
		for i := uint64(0); i < k; i++ {
			t, err := u()
			if err != nil {
				return nil, err
			}
			rep.CompTypes = append(rep.CompTypes, uint16(t))
		}
	}
	if p != len(b) {
		return nil, malformed("trailing recipe bytes")
	}
	return rep, nil
}
