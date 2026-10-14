package carz3

import (
	"bufio"
	"bytes"
	"io"
)

// Limits bound every allocation and count the parser makes.
type Limits struct {
	MaxHeader int64 // header bytes
	MaxRoots  int
	MaxBlocks int64
	MaxBlock  int64 // block payload bytes
	MaxTotal  int64 // bytes of the CARv1 stream
}

// DefaultLimits suit laboratory corpora (blocks are 1 MiB at most in practice).
func DefaultLimits() Limits {
	return Limits{MaxHeader: 1 << 20, MaxRoots: 1 << 16, MaxBlocks: 1 << 28, MaxBlock: 64 << 20, MaxTotal: 1 << 40}
}

func (l Limits) maxSection() int64 { return l.MaxBlock + 2 + 4*maxVarintLen + MaxDigest }

// Header is the parsed CARv1 header. Raw is the exact DAG-CBOR bytes (without
// the length prefix) and is the only thing reconstruction needs.
type Header struct {
	LenPrefix  int // bytes of the (canonical) length varint
	Raw        []byte
	Version    uint64
	Roots      []CID
	RootsFirst bool // key order in the map: "roots" before "version"
}

// Size is the encoded size of the header section (prefix + body).
func (h *Header) Size() int64 { return int64(h.LenPrefix + len(h.Raw)) }

// cbor reads one item head: major type and argument, requiring the shortest
// argument encoding and a definite length.
func cborHead(b []byte) (major byte, val uint64, n int, err error) {
	if len(b) == 0 {
		return 0, 0, 0, malformed("truncated CBOR")
	}
	major, ai := b[0]>>5, b[0]&0x1f
	switch {
	case ai < 24:
		return major, uint64(ai), 1, nil
	case ai >= 28:
		return 0, 0, 0, unsupported("indefinite / reserved CBOR argument")
	}
	size := 1 << (ai - 24)
	if len(b) < 1+size {
		return 0, 0, 0, malformed("truncated CBOR argument")
	}
	for i := 0; i < size; i++ {
		val = val<<8 | uint64(b[1+i])
	}
	min := [...]uint64{24, 1 << 8, 1 << 16, 1 << 32}[ai-24]
	if val < min {
		return 0, 0, 0, unsupported("non-minimal CBOR argument")
	}
	return major, val, 1 + size, nil
}

// ParseHeader parses a CARv1 header body (the bytes after the length varint):
// the one DAG-CBOR shape the format defines, {"roots": [CID...], "version": 1}
// in either key order. Anything else is reported as unsupported.
func ParseHeader(raw []byte, lim Limits) (*Header, error) {
	h := &Header{Raw: raw}
	p := 0
	if len(raw) == 0 || raw[0] != 0xa2 {
		return nil, unsupported("header is not a 2-entry CBOR map")
	}
	p++
	seen := map[string]bool{}
	for k := 0; k < 2; k++ {
		maj, l, n, err := cborHead(raw[p:])
		if err != nil {
			return nil, err
		}
		if maj != 3 || l > 7 || uint64(len(raw)-p-n) < l {
			return nil, unsupported("header key")
		}
		key := string(raw[p+n : p+n+int(l)])
		p += n + int(l)
		if seen[key] {
			return nil, unsupported("duplicate header key")
		}
		seen[key] = true
		switch key {
		case "version":
			maj, v, n, err := cborHead(raw[p:])
			if err != nil {
				return nil, err
			}
			if maj != 0 || v != 1 {
				return nil, unsupported("header version %d", v)
			}
			h.Version = v
			p += n
		case "roots":
			if k == 0 {
				h.RootsFirst = true
			}
			maj, cnt, n, err := cborHead(raw[p:])
			if err != nil {
				return nil, err
			}
			if maj != 4 || cnt > uint64(lim.MaxRoots) || cnt > uint64(len(raw)) {
				return nil, malformed("roots array")
			}
			p += n
			for i := uint64(0); i < cnt; i++ {
				if len(raw)-p < 2 || raw[p] != 0xd8 || raw[p+1] != 0x2a {
					return nil, unsupported("root is not a tag-42 link")
				}
				p += 2
				maj, bl, n, err := cborHead(raw[p:])
				if err != nil {
					return nil, err
				}
				if maj != 2 || bl < 1 || uint64(len(raw)-p-n) < bl || raw[p+n] != 0x00 {
					return nil, malformed("root link bytes")
				}
				c, cn, err := ParseCID(raw[p+n+1 : p+n+int(bl)])
				if err != nil {
					return nil, err
				}
				if uint64(cn) != bl-1 {
					return nil, malformed("root link has trailing bytes")
				}
				h.Roots = append(h.Roots, c)
				p += n + int(bl)
			}
		default:
			return nil, unsupported("header key %q", key)
		}
	}
	if p != len(raw) {
		return nil, malformed("trailing bytes after header map")
	}
	return h, nil
}

// Entry is one CARv1 section. Data is valid only until the next call to Next;
// CID is detached and stays valid.
type Entry struct {
	Off     int64 // offset of the section (its length varint) in the CARv1 stream
	Prefix  int   // length varint bytes
	CID     CID
	Data    []byte
	DataOff int64
}

// SectionLen is the encoded section length value (CID + data).
func (e *Entry) SectionLen() int64 { return int64(len(e.CID.Raw)) + int64(len(e.Data)) }

// End is the stream offset after the section.
func (e *Entry) End() int64 { return e.Off + int64(e.Prefix) + e.SectionLen() }

// Reader streams a CARv1: header first, then one section at a time. Memory is
// bounded by the largest block seen, not by the archive size.
type Reader struct {
	br     *bufio.Reader
	lim    Limits
	pos    int64
	blocks int64
	hdr    *Header
	buf    []byte
	big    bytes.Buffer
}

// NewReader reads the header of the CARv1 stream r.
func NewReader(r io.Reader, lim Limits) (*Reader, error) {
	rd := &Reader{br: bufio.NewReaderSize(r, 1<<16), lim: lim}
	hl, n, err := rd.uvarint(false)
	if err != nil {
		return nil, err
	}
	if hl == 0 || int64(hl) > lim.MaxHeader {
		return nil, malformed("header length %d", hl)
	}
	raw := make([]byte, hl)
	if _, err := io.ReadFull(rd.br, raw); err != nil {
		return nil, malformed("truncated header")
	}
	rd.pos += int64(hl)
	h, err := ParseHeader(raw, lim)
	if err != nil {
		return nil, err
	}
	h.LenPrefix = n
	rd.hdr = h
	return rd, nil
}

func (r *Reader) Header() *Header { return r.hdr }

// Pos is the number of stream bytes consumed so far.
func (r *Reader) Pos() int64 { return r.pos }

// uvarint reads a canonical varint; at a clean end of stream with eofOK it
// returns io.EOF.
func (r *Reader) uvarint(eofOK bool) (uint64, int, error) {
	var tmp [maxVarintLen]byte
	for i := 0; i < maxVarintLen; i++ {
		c, err := r.br.ReadByte()
		if err != nil {
			if i == 0 && eofOK && err == io.EOF {
				return 0, 0, io.EOF
			}
			return 0, 0, malformed("truncated varint")
		}
		tmp[i] = c
		if c&0x80 == 0 {
			v, n, err := Uvarint(tmp[:i+1])
			if err != nil {
				return 0, 0, err
			}
			r.pos += int64(n)
			return v, n, nil
		}
	}
	return 0, 0, malformed("varint longer than %d bytes", maxVarintLen)
}

// Next returns the next section, or io.EOF after the last one.
func (r *Reader) Next() (*Entry, error) {
	off := r.pos
	sl, n, err := r.uvarint(true)
	if err != nil {
		return nil, err
	}
	if sl == 0 {
		return nil, unsupported("zero-length section")
	}
	if sl > uint64(r.lim.maxSection()) {
		return nil, malformed("section length %d", sl)
	}
	if r.blocks >= r.lim.MaxBlocks {
		return nil, malformed("more than %d blocks", r.lim.MaxBlocks)
	}
	if r.pos+int64(sl) > r.lim.MaxTotal {
		return nil, malformed("stream exceeds %d bytes", r.lim.MaxTotal)
	}
	head, _ := r.br.Peek(int(min(sl, 3+3*maxVarintLen+MaxDigest)))
	cid, cl, err := ParseCID(head)
	if err != nil {
		return nil, err
	}
	if uint64(cl) > sl {
		return nil, malformed("CID longer than its section")
	}
	cid = cid.Clone()
	r.br.Discard(cl)
	dl := int(sl) - cl
	if int64(dl) > r.lim.MaxBlock {
		return nil, malformed("block of %d bytes", dl)
	}
	var data []byte
	if dl <= 1<<20 {
		if cap(r.buf) < dl {
			r.buf = make([]byte, dl)
		}
		data = r.buf[:dl]
		if _, err := io.ReadFull(r.br, data); err != nil {
			return nil, malformed("truncated block")
		}
	} else { // grow with the data actually present: a hostile length costs nothing
		r.big.Reset()
		if n, _ := io.CopyN(&r.big, r.br, int64(dl)); n != int64(dl) {
			return nil, malformed("truncated block")
		}
		data = r.big.Bytes()
	}
	r.pos += int64(sl)
	r.blocks++
	return &Entry{Off: off, Prefix: n, CID: cid, Data: data, DataOff: off + int64(n) + int64(cl)}, nil
}
