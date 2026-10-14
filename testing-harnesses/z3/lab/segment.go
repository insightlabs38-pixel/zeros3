package lab

import (
	"archive/zip"
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"

	"zeros3-z3/st"
)

// Laboratory segmenters. Each recognises just enough of a container to
// expose its semantic units (members, tensors, blocks, pages, records) with
// byte spans. They are bounded, never trust declared lengths, and are not
// production parsers: an error only means "no structural representation for
// this object", never that the object is invalid.

type Seg struct {
	Key      string
	Off, Len int64
	// Logical returns the segment's decoded content when the container
	// compresses members (ZIP, chunked arrays); nil otherwise.
	Logical func() ([]byte, error)
	// Deflate marks a segment whose bytes are exactly one raw RFC1951 stream.
	Deflate bool
	// DType and Width (element bytes) describe tensor segments (Safetensors).
	DType string
	Width int
}

var (
	ErrNoSegmenter = errors.New("lab: no segmenter for format")
	ErrMalformed   = errors.New("lab: malformed container")
)

const (
	maxHeaderBytes = 64 << 20
	maxExpandBytes = 1 << 30
)

// Segment returns the segments of s in offset order. Segments never overlap
// and always lie inside s.
func Segment(format string, s Source) ([]Seg, error) {
	var fn func(Source) ([]Seg, error)
	switch format {
	case "zip", "pytorch-zip":
		fn = segZip
	case "safetensors":
		fn = segSafetensors
	case "gguf":
		fn = segGGUF
	case "car":
		fn = segCAR
	case "zbin":
		fn = segZBin
	case "pagedb":
		fn = segPageDB
	case "colstore":
		fn = segColstore
	case "ndarray":
		fn = segNDArray
	case "protorec":
		fn = segProtorec
	default:
		return nil, ErrNoSegmenter
	}
	segs, err := fn(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return normalize(s.Size, segs)
}

func normalize(size int64, segs []Seg) ([]Seg, error) {
	out := segs[:0:0]
	for _, g := range segs {
		if g.Len == 0 {
			continue
		}
		if g.Off < 0 || g.Len < 0 || g.Off > size || g.Len > size-g.Off {
			return nil, fmt.Errorf("%w: segment %q [%d,+%d) outside %d bytes", ErrMalformed, g.Key, g.Off, g.Len, size)
		}
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Off < out[j].Off })
	for i := 1; i < len(out); i++ {
		if out[i].Off < out[i-1].Off+out[i-1].Len {
			return nil, fmt.Errorf("%w: segments %q and %q overlap", ErrMalformed, out[i-1].Key, out[i].Key)
		}
	}
	return out, nil
}

func u64span(off, n uint64, size int64) error {
	if off > uint64(size) || n > uint64(size)-off {
		return fmt.Errorf("span [%d,+%d) outside %d bytes", off, n, size)
	}
	return nil
}

func inflateLimited(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxExpandBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxExpandBytes {
		return nil, errors.New("decoded size exceeds limit")
	}
	return b, nil
}

func segZip(s Source) ([]Seg, error) {
	zr, err := zip.NewReader(io.NewSectionReader(s.R, 0, s.Size), s.Size)
	if err != nil {
		return nil, err
	}
	var segs []Seg
	for _, f := range zr.File {
		off, err := f.DataOffset()
		if err != nil {
			return nil, err
		}
		if err := u64span(uint64(off), f.CompressedSize64, s.Size); err != nil {
			return nil, err
		}
		f := f
		segs = append(segs, Seg{Key: f.Name, Off: off, Len: int64(f.CompressedSize64), Deflate: f.Method == zip.Deflate, Logical: func() ([]byte, error) {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return inflateLimited(rc)
		}})
	}
	return segs, nil
}

func segSafetensors(s Source) ([]Seg, error) {
	f, err := st.Parse(s.R, s.Size, st.Options{MaxHeader: maxHeaderBytes})
	if err != nil {
		return nil, err
	}
	var segs []Seg
	for i := range f.Tensors {
		t := &f.Tensors[i]
		w := 0
		if t.Bits%8 == 0 {
			w = t.Bits / 8
		}
		segs = append(segs, Seg{Key: t.Name, Off: f.DataStart + t.Begin, Len: t.Size(), DType: t.DType, Width: w})
	}
	return segs, nil
}

type binReader struct {
	b   []byte
	p   int
	err error
}

func (r *binReader) take(n uint64) []byte {
	if r.err != nil || n > uint64(len(r.b)-r.p) {
		r.err = errors.New("truncated header")
		return nil
	}
	out := r.b[r.p : r.p+int(n)]
	r.p += int(n)
	return out
}
func (r *binReader) u32() uint32 {
	if b := r.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}
func (r *binReader) u64() uint64 {
	if b := r.take(8); b != nil {
		return binary.LittleEndian.Uint64(b)
	}
	return 0
}
func (r *binReader) str() string { return string(r.take(r.u64())) }

func segGGUF(s Source) ([]Seg, error) {
	n := min(s.Size, maxHeaderBytes)
	b, err := s.Span(0, n)
	if err != nil {
		return nil, err
	}
	if len(b) < 24 || string(b[:4]) != "GGUF" || binary.LittleEndian.Uint32(b[4:]) != 3 {
		return nil, errors.New("not GGUF v3")
	}
	r := &binReader{b: b, p: 8}
	nt, nkv := r.u64(), r.u64()
	if nt > uint64(len(b)) || nkv > uint64(len(b)) {
		return nil, errors.New("implausible counts")
	}
	align := uint64(32)
	for i := uint64(0); i < nkv && r.err == nil; i++ {
		key := r.str()
		switch kind := r.u32(); kind {
		case 4, 5, 6, 7:
			v := r.u32()
			if key == "general.alignment" && kind == 4 && v > 0 && v&(v-1) == 0 {
				align = uint64(v)
			}
		case 8:
			r.str()
		default:
			return nil, fmt.Errorf("unsupported metadata value type %d (subset: u32/f32/string)", kind)
		}
	}
	type info struct {
		name     string
		typ      uint32
		elems    uint64
		off      uint64
		blockEls uint64
		blockSz  uint64
	}
	var infos []info
	for i := uint64(0); i < nt && r.err == nil; i++ {
		in := info{name: r.str()}
		if r.u32() != 1 {
			return nil, errors.New("subset supports 1-D tensors")
		}
		in.elems, in.typ, in.off = r.u64(), r.u32(), r.u64()
		switch in.typ {
		case 0:
			in.blockEls, in.blockSz = 1, 4
		case 1:
			in.blockEls, in.blockSz = 1, 2
		case 2:
			in.blockEls, in.blockSz = 32, 18
		case 8:
			in.blockEls, in.blockSz = 32, 34
		default:
			return nil, fmt.Errorf("unsupported tensor type %d", in.typ)
		}
		infos = append(infos, in)
	}
	if r.err != nil {
		return nil, r.err
	}
	dataStart := (uint64(r.p) + align - 1) / align * align
	var segs []Seg
	for _, in := range infos {
		if in.elems%in.blockEls != 0 || in.elems/in.blockEls > uint64(s.Size) {
			return nil, fmt.Errorf("tensor %q size", in.name)
		}
		sz := in.elems / in.blockEls * in.blockSz
		if in.off > uint64(s.Size) || dataStart > uint64(s.Size)-in.off {
			return nil, fmt.Errorf("tensor %q offset", in.name)
		}
		if err := u64span(dataStart+in.off, sz, s.Size); err != nil {
			return nil, err
		}
		segs = append(segs, Seg{Key: in.name, Off: int64(dataStart + in.off), Len: int64(sz)})
	}
	return segs, nil
}

// parseCID returns the byte length of a CIDv1 at the start of b.
func parseCID(b []byte) (int, bool) {
	p := 0
	for k := 0; k < 4; k++ { // version, codec, hash code, digest length
		v, n := binary.Uvarint(b[p:])
		if n <= 0 {
			return 0, false
		}
		p += n
		if k == 0 && v != 1 {
			return 0, false
		}
		if k == 3 {
			if v > 64 || p+int(v) > len(b) {
				return 0, false
			}
			return p + int(v), true
		}
	}
	return 0, false
}

func segCAR(s Source) ([]Seg, error) {
	br := bufio.NewReaderSize(io.NewSectionReader(s.R, 0, s.Size), 1<<16)
	pos := int64(0)
	readUvarint := func() (uint64, int64, error) {
		start := pos
		var v uint64
		for shift := uint(0); shift < 63; shift += 7 {
			c, err := br.ReadByte()
			if err != nil {
				return 0, 0, err
			}
			pos++
			v |= uint64(c&0x7f) << shift
			if c&0x80 == 0 {
				return v, pos - start, nil
			}
		}
		return 0, 0, errors.New("varint too long")
	}
	hl, _, err := readUvarint()
	if err != nil || hl > maxHeaderBytes || hl > uint64(s.Size-pos) {
		return nil, errors.New("bad CAR header length")
	}
	if _, err := br.Discard(int(hl)); err != nil {
		return nil, err
	}
	pos += int64(hl)
	var segs []Seg
	for pos < s.Size {
		start := pos
		sl, _, err := readUvarint()
		if err != nil || sl > uint64(s.Size-pos) {
			return nil, errors.New("bad CAR section length")
		}
		head, _ := br.Peek(int(min(sl, 64)))
		cl, ok := parseCID(head)
		if !ok || uint64(cl) > sl {
			return nil, errors.New("bad CID in CAR section")
		}
		segs = append(segs, Seg{Key: hex.EncodeToString(head[:cl]), Off: start, Len: pos - start + int64(sl)})
		if _, err := br.Discard(int(sl)); err != nil {
			return nil, err
		}
		pos += int64(sl)
	}
	return segs, nil
}

func segZBin(s Source) ([]Seg, error) {
	b, err := s.Span(0, min(s.Size, 4096))
	if err != nil {
		return nil, err
	}
	if len(b) < 32 || string(b[:4]) != "ZBIN" {
		return nil, errors.New("not ZBIN")
	}
	n := binary.LittleEndian.Uint32(b[24:])
	if n > 16 || 32+32*int(n) > len(b) {
		return nil, errors.New("section table")
	}
	var segs []Seg
	for i := 0; i < int(n); i++ {
		e := b[32+32*i:]
		kind := binary.LittleEndian.Uint32(e)
		off, sz := binary.LittleEndian.Uint64(e[8:]), binary.LittleEndian.Uint64(e[16:])
		if err := u64span(off, sz, s.Size); err != nil {
			return nil, err
		}
		key := map[uint32]string{1: "text", 2: "rodata", 3: "ptrtab", 4: "data"}[kind]
		if key == "" {
			key = fmt.Sprintf("kind%d", kind)
		}
		segs = append(segs, Seg{Key: key, Off: int64(off), Len: int64(sz)})
	}
	return segs, nil
}

func segPageDB(s Source) ([]Seg, error) {
	b, err := s.Span(0, min(s.Size, 16))
	if err != nil {
		return nil, err
	}
	if len(b) < 16 || string(b[:4]) != "ZPDB" {
		return nil, errors.New("not ZPDB")
	}
	ps := int64(binary.LittleEndian.Uint32(b[4:]))
	if ps < 512 || ps > 1<<20 || ps&(ps-1) != 0 || s.Size%ps != 0 {
		return nil, fmt.Errorf("page size %d", ps)
	}
	var segs []Seg
	for i := int64(0); i*ps < s.Size; i++ {
		segs = append(segs, Seg{Key: fmt.Sprintf("p%d", i), Off: i * ps, Len: ps})
	}
	return segs, nil
}

func segColstore(s Source) ([]Seg, error) {
	if s.Size < 12 {
		return nil, errors.New("short file")
	}
	t, err := s.Span(s.Size-8, 8)
	if err != nil {
		return nil, err
	}
	fl := int64(binary.LittleEndian.Uint32(t))
	if string(t[4:]) != "ZCL1" || fl < 4 || fl > s.Size-8-4 {
		return nil, errors.New("bad trailer")
	}
	foot, err := s.Span(s.Size-8-fl, fl)
	if err != nil {
		return nil, err
	}
	n := int64(binary.LittleEndian.Uint32(foot))
	if 4+24*n != fl {
		return nil, errors.New("footer length")
	}
	var segs []Seg
	for i := int64(0); i < n; i++ {
		e := foot[4+24*i:]
		rg, col := binary.LittleEndian.Uint32(e), binary.LittleEndian.Uint32(e[4:])
		off, ln := binary.LittleEndian.Uint64(e[8:]), binary.LittleEndian.Uint64(e[16:])
		if err := u64span(off, ln, s.Size-8-fl); err != nil {
			return nil, err
		}
		segs = append(segs, Seg{Key: fmt.Sprintf("rg%d.c%d", rg, col), Off: int64(off), Len: int64(ln)})
	}
	return segs, nil
}

func segNDArray(s Source) ([]Seg, error) {
	b, err := s.Span(0, min(s.Size, 4096))
	if err != nil {
		return nil, err
	}
	if len(b) < 8 || string(b[:4]) != "ZND1" {
		return nil, errors.New("not ZND1")
	}
	nd := int(binary.LittleEndian.Uint32(b[4:]))
	if nd < 1 || nd > 4 || 8+16*nd+4 > len(b) {
		return nil, errors.New("rank")
	}
	nc := int64(binary.LittleEndian.Uint32(b[8+16*nd:]))
	idxOff := int64(8 + 16*nd + 4)
	if nc > s.Size/16 {
		return nil, errors.New("chunk count")
	}
	idx, err := s.Span(idxOff, 16*nc)
	if err != nil {
		return nil, err
	}
	var segs []Seg
	for i := int64(0); i < nc; i++ {
		off, ln := binary.LittleEndian.Uint64(idx[16*i:]), binary.LittleEndian.Uint64(idx[16*i+8:])
		if err := u64span(off, ln, s.Size); err != nil {
			return nil, err
		}
		o, l := int64(off), int64(ln)
		segs = append(segs, Seg{Key: fmt.Sprintf("c%d", i), Off: o, Len: l, Deflate: true, Logical: func() ([]byte, error) {
			raw, err := s.Span(o, l)
			if err != nil {
				return nil, err
			}
			return inflateLimited(flate.NewReader(bytes.NewReader(raw)))
		}})
	}
	return segs, nil
}

func segProtorec(s Source) ([]Seg, error) {
	var segs []Seg
	var pos int64
	for pos < s.Size {
		head, err := s.Span(pos, min(s.Size-pos, 32))
		if err != nil {
			return nil, err
		}
		l, n := binary.Uvarint(head)
		if n <= 0 || l > uint64(s.Size-pos-int64(n)) {
			return nil, errors.New("bad record framing")
		}
		key := fmt.Sprintf("rec@%d", pos)
		if len(head) > n+1 && head[n] == 0x08 {
			if id, m := binary.Uvarint(head[n+1:]); m > 0 {
				key = fmt.Sprintf("id:%d", id)
			}
		}
		segs = append(segs, Seg{Key: key, Off: pos, Len: int64(n) + int64(l)})
		pos += int64(n) + int64(l)
	}
	return segs, nil
}

// Region is a contiguous byte range of an object: either a segment or the
// uncovered gap before/between/after segments.
type Region struct {
	Off, Len int64
	Seg      *Seg // nil for gaps
}

// Regions partitions [0,size) into segments and gaps, in order.
func Regions(size int64, segs []Seg) []Region {
	var out []Region
	pos := int64(0)
	for i := range segs {
		g := &segs[i]
		if g.Off > pos {
			out = append(out, Region{Off: pos, Len: g.Off - pos})
		}
		out = append(out, Region{Off: g.Off, Len: g.Len, Seg: g})
		pos = g.Off + g.Len
	}
	if pos < size {
		out = append(out, Region{Off: pos, Len: size - pos})
	}
	return out
}
