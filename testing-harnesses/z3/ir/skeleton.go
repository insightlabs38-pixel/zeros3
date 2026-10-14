package ir

import (
	"encoding/binary"
	"fmt"
	"math/bits"

	"zeros3-z3/deflate"
)

const (
	skelVersion = 2
	maxBlocks   = 1 << 18
	maxUnits    = 1 << 20
)

// bitw writes LSB-first like DEFLATE.
type bitw struct {
	out  []byte
	acc  uint64
	nacc uint
}

func (w *bitw) put(v uint32, n uint) {
	w.acc |= uint64(v) << w.nacc
	w.nacc += n
	for w.nacc >= 8 {
		w.out = append(w.out, byte(w.acc))
		w.acc >>= 8
		w.nacc -= 8
	}
}

// putCode writes a canonical Huffman code MSB first.
func (w *bitw) putCode(code uint16, n uint8) {
	for i := int(n) - 1; i >= 0; i-- {
		w.put(uint32(code>>uint(i))&1, 1)
	}
}

func (w *bitw) flush() []byte {
	if w.nacc > 0 {
		w.out = append(w.out, byte(w.acc))
		w.acc, w.nacc = 0, 0
	}
	return w.out
}

type bitr struct {
	in   []byte
	pos  int // bit position
	over bool
}

func (r *bitr) get(n uint) uint32 {
	var v uint32
	for i := uint(0); i < n; i++ {
		by := r.pos >> 3
		if by >= len(r.in) {
			r.over = true
			return 0
		}
		v |= uint32(r.in[by]>>(uint(r.pos)&7)&1) << i
		r.pos++
	}
	return v
}

func (r *bitr) alignedPos() int { return (r.pos + 7) >> 3 }

// canonCodes assigns canonical codes to lens (RFC1951 3.2.2).
func canonCodes(lens []uint8) (codes []uint16) {
	var count [16]int
	for _, l := range lens {
		count[l]++
	}
	count[0] = 0
	var next [16]int
	code := 0
	for l := 1; l < 16; l++ {
		code = (code + count[l-1]) << 1
		next[l] = code
	}
	codes = make([]uint16, len(lens))
	for s, l := range lens {
		if l != 0 {
			codes[s] = uint16(next[l])
			next[l]++
		}
	}
	return
}

// canon is a canonical Huffman decoder built once per code (puff style).
type canon struct {
	count [16]int
	syms  []int // symbols ordered by (length, symbol)
}

func newCanon(lens []uint8) *canon {
	c := &canon{}
	for _, l := range lens {
		c.count[l]++
	}
	c.count[0] = 0
	for l := 1; l < 16; l++ {
		for s, ln := range lens {
			if int(ln) == l {
				c.syms = append(c.syms, s)
			}
		}
	}
	return c
}

func (c *canon) decode(r *bitr) (int, bool) {
	code, first, index := 0, 0, 0
	for l := 1; l < 16; l++ {
		code |= int(r.get(1))
		if r.over {
			return 0, false
		}
		n := c.count[l]
		if code-n < first {
			i := index + (code - first)
			if i < len(c.syms) {
				return c.syms[i], true
			}
			return 0, false
		}
		index += n
		first = (first + n) << 1
		code <<= 1
	}
	return 0, false
}

func uvarint(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

func padMarker(bits uint8, n uint8) byte { return byte(1<<n | bits) }

func parsePadMarker(m byte) (bits, n uint8, ok bool) {
	if m == 0 {
		return 0, 0, false
	}
	n = uint8(bits32len(uint32(m)) - 1)
	if n > 7 {
		return 0, 0, false
	}
	return m & (1<<n - 1), n, true
}

func bits32len(x uint32) int { return bits.Len32(x) }

// Block entry header byte:
//
//	bit 0     BFINAL
//	bits 1-2  BTYPE (stored, fixed, dynamic)
//	bit 3     dynamic: header predicted from the tokens (Pred); stored: zero padding (AutoPad)
//	bit 4     fixed/dynamic: NTokens equals the previous compressed block's; stored: StoredLen is 0
//
// followed by what the flags do not imply: NTokens (varint) for fixed/dynamic,
// the pad marker for stored without AutoPad, StoredLen for a non-empty stored
// block, the exact header bits for a dynamic block without Pred. An explicit
// NTokens or StoredLen never repeats what a flag could say (canonical form).
const (
	hFinal    = 1
	hKindShft = 1
	hFlag3    = 8
	hFlag4    = 16
)

// encodeBlockMeta appends the exact encoding of one block; prevN is the token
// count of the previous fixed/dynamic block (0 before the first).
func encodeBlockMeta(dst []byte, m *BlockMeta, prevN int) []byte {
	h := byte(m.Kind) << hKindShft
	if m.Final {
		h |= hFinal
	}
	switch m.Kind {
	case deflate.Stored:
		if m.AutoPad {
			h |= hFlag3
		}
		if m.StoredLen == 0 {
			h |= hFlag4
		}
		dst = append(dst, h)
		if !m.AutoPad {
			dst = append(dst, padMarker(m.PadBits, m.PadLen))
		}
		if m.StoredLen != 0 {
			dst = uvarint(dst, uint64(m.StoredLen))
		}
	case deflate.Fixed:
		if m.NTokens == prevN {
			h |= hFlag4
		}
		dst = append(dst, h)
		if m.NTokens != prevN {
			dst = uvarint(dst, uint64(m.NTokens))
		}
	case deflate.Dynamic:
		if m.Pred {
			h |= hFlag3
		}
		if m.NTokens == prevN {
			h |= hFlag4
		}
		dst = append(dst, h)
		if m.NTokens != prevN {
			dst = uvarint(dst, uint64(m.NTokens))
		}
		if !m.Pred {
			dst = append(dst, dynHeaderBits(m)...)
		}
	}
	return dst
}

var clOrder = deflate.CLOrder()

// dynHeaderBits writes the dynamic header exactly as DEFLATE transmits it
// (after BTYPE): HLIT HDIST HCLEN, code-length code lengths, then the
// Huffman-coded code-length symbol sequence.
func dynHeaderBits(m *BlockMeta) []byte {
	var w bitw
	w.put(uint32(m.HLIT)|uint32(m.HDIST)<<5|uint32(m.HCLEN)<<10, 14)
	n := int(m.HCLEN) + 4
	for i := 0; i < n; i++ {
		w.put(uint32(m.CLLens[clOrder[i]]), 3)
	}
	codes := canonCodes(m.CLLens[:])
	for _, cs := range m.CLSyms {
		w.putCode(codes[cs.Sym], m.CLLens[cs.Sym])
		switch {
		case cs.Sym == 16:
			w.put(uint32(cs.Extra), 2)
		case cs.Sym == 17:
			w.put(uint32(cs.Extra), 3)
		case cs.Sym == 18:
			w.put(uint32(cs.Extra), 7)
		}
	}
	return w.flush()
}

// Marshal encodes the skeleton.
func (s *Skeleton) Marshal() []byte {
	b := []byte{skelVersion, byte(s.Codec), s.Profile}
	b = uvarint(b, uint64(len(s.Blocks)))
	prevN := 0
	for i := range s.Blocks {
		b = encodeBlockMeta(b, &s.Blocks[i], prevN)
		if s.Blocks[i].Kind != deflate.Stored {
			prevN = s.Blocks[i].NTokens
		}
	}
	b = append(b, padMarker(s.TailPadBits, s.TailPadLen))
	b = uvarint(b, uint64(len(s.Units)))
	for _, u := range s.Units {
		b = uvarint(b, uint64(u.PlainLen))
		b = uvarint(b, uint64(u.NTokens))
	}
	b = uvarint(b, uint64(s.StreamLen))
	b = binary.LittleEndian.AppendUint32(b, s.CRC)
	return b
}

type sreader struct {
	b   []byte
	pos int
	err error
}

func (r *sreader) byte() byte {
	if r.pos >= len(r.b) {
		r.err = corrupt("skeleton truncated")
		return 0
	}
	v := r.b[r.pos]
	r.pos++
	return v
}

func (r *sreader) uvarint(limit uint64) uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 || (n > 1 && r.b[r.pos+n-1] == 0) { // overflow, truncation or non-minimal
		r.err = corrupt("skeleton varint")
		return 0
	}
	r.pos += n
	if v > limit {
		r.err = fmt.Errorf("%w: skeleton value %d over %d", ErrLimit, v, limit)
		return 0
	}
	return v
}

// ParseSkeleton decodes and validates a skeleton. maxPlain bounds every length.
func ParseSkeleton(b []byte, maxPlain int) (*Skeleton, error) {
	r := &sreader{b: b}
	if v := r.byte(); r.err == nil && v != skelVersion {
		return nil, corrupt("skeleton version %d", v)
	}
	s := &Skeleton{Codec: Codec(r.byte()), Profile: r.byte()}
	nb := r.uvarint(maxBlocks)
	if r.err != nil {
		return nil, r.err
	}
	s.Blocks = make([]BlockMeta, 0, min(int(nb), 1024))
	totalTok, prevN := 0, 0
	for i := 0; i < int(nb); i++ {
		h := r.byte()
		if r.err != nil {
			return nil, r.err
		}
		if h > hFlag4|hFlag3|2<<hKindShft|hFinal {
			return nil, corrupt("block %d header byte %#x", i, h)
		}
		m := BlockMeta{Final: h&hFinal == 1, Kind: deflate.BlockKind(h >> hKindShft & 3)}
		f3, f4 := h&hFlag3 != 0, h&hFlag4 != 0
		if m.Final != (i == int(nb)-1) {
			return nil, corrupt("block %d final flag", i)
		}
		switch m.Kind {
		case deflate.Stored:
			m.AutoPad = f3
			if !f3 {
				pb, pn, ok := parsePadMarker(r.byte())
				if !ok {
					return nil, corrupt("block %d pad marker", i)
				}
				m.PadBits, m.PadLen = pb, pn
			}
			if !f4 {
				m.StoredLen = int(r.uvarint(65535))
				if m.StoredLen == 0 && r.err == nil {
					return nil, corrupt("block %d non-canonical empty stored block", i)
				}
			}
		case deflate.Fixed, deflate.Dynamic:
			if m.Kind == deflate.Fixed && f3 {
				return nil, corrupt("block %d fixed block with header flag", i)
			}
			if f4 {
				m.NTokens = prevN
			} else {
				m.NTokens = int(r.uvarint(uint64(maxPlain)))
				if m.NTokens == prevN && r.err == nil {
					return nil, corrupt("block %d non-canonical token count", i)
				}
			}
			prevN = m.NTokens
			if m.Kind == deflate.Dynamic {
				m.Pred = f3
				if m.Pred && m.NTokens > maxPredictTokens {
					return nil, corrupt("block %d predicted header over %d tokens", i, maxPredictTokens)
				}
				if r.err != nil {
					return nil, r.err
				}
				if !m.Pred {
					if err := parseDynHeader(r, &m); err != nil {
						return nil, fmt.Errorf("block %d: %w", i, err)
					}
				}
			}
		default:
			return nil, corrupt("block %d kind %d", i, m.Kind)
		}
		if r.err != nil {
			return nil, r.err
		}
		totalTok += m.NTokens
		if totalTok > maxPlain {
			return nil, fmt.Errorf("%w: more tokens than plaintext bytes", ErrLimit)
		}
		s.Blocks = append(s.Blocks, m)
	}
	tb, tn, ok := parsePadMarker(r.byte())
	if !ok {
		return nil, corrupt("tail pad marker")
	}
	s.TailPadBits, s.TailPadLen = tb, tn
	nu := r.uvarint(maxUnits)
	if r.err != nil {
		return nil, r.err
	}
	s.Units = make([]UnitIdx, 0, min(int(nu), 1024))
	sumTok, sumPlain := 0, 0
	for i := 0; i < int(nu); i++ {
		u := UnitIdx{PlainLen: int(r.uvarint(uint64(maxPlain))), NTokens: int(r.uvarint(uint64(maxPlain)))}
		if r.err != nil {
			return nil, r.err
		}
		if u.PlainLen == 0 || u.NTokens == 0 || u.NTokens > u.PlainLen {
			return nil, corrupt("unit %d index %+v", i, u)
		}
		sumTok += u.NTokens
		sumPlain += u.PlainLen
		if sumTok > totalTok || sumPlain > maxPlain {
			return nil, fmt.Errorf("%w: unit index exceeds block table", ErrLimit)
		}
		s.Units = append(s.Units, u)
	}
	if sumTok != totalTok {
		return nil, corrupt("units hold %d tokens, blocks need %d", sumTok, totalTok)
	}
	s.StreamLen = int(r.uvarint(1 << 40))
	if r.pos+4 > len(r.b) {
		return nil, corrupt("skeleton truncated at crc")
	}
	s.CRC = binary.LittleEndian.Uint32(r.b[r.pos:])
	r.pos += 4
	if r.err != nil {
		return nil, r.err
	}
	if r.pos != len(b) {
		return nil, corrupt("skeleton has %d trailing bytes", len(b)-r.pos)
	}
	return s, nil
}

func parseDynHeader(r *sreader, m *BlockMeta) error {
	br := &bitr{in: r.b[r.pos:]}
	f := br.get(14)
	m.HLIT, m.HDIST, m.HCLEN = uint8(f&31), uint8(f>>5&31), uint8(f>>10&15)
	nlit, ndist, nclen := int(m.HLIT)+257, int(m.HDIST)+1, int(m.HCLEN)+4
	if nlit > 286 || ndist > 30 {
		return corrupt("too many length or distance symbols")
	}
	for i := 0; i < nclen; i++ {
		m.CLLens[clOrder[i]] = uint8(br.get(3))
	}
	if br.over {
		return corrupt("dynamic header truncated")
	}
	if !deflate.ValidLengths(m.CLLens[:], false) {
		return corrupt("invalid code-length code")
	}
	total := 0
	cd := newCanon(m.CLLens[:])
	for total < nlit+ndist {
		sym, ok := cd.decode(br)
		if !ok {
			return corrupt("code-length symbol")
		}
		cs := deflate.CLSym{Sym: uint8(sym)}
		rep := 1
		switch {
		case sym < 16:
		case sym == 16:
			if total == 0 {
				return corrupt("repeat with no previous length")
			}
			cs.Extra = uint8(br.get(2))
			rep = 3 + int(cs.Extra)
		case sym == 17:
			cs.Extra = uint8(br.get(3))
			rep = 3 + int(cs.Extra)
		default:
			cs.Extra = uint8(br.get(7))
			rep = 11 + int(cs.Extra)
		}
		if br.over {
			return corrupt("dynamic header truncated")
		}
		if total+rep > nlit+ndist {
			return corrupt("repeat overruns code lengths")
		}
		total += rep
		m.CLSyms = append(m.CLSyms, cs)
	}
	if pad := br.alignedPos()*8 - br.pos; pad > 0 && br.in[br.alignedPos()-1]>>(8-uint(pad)) != 0 {
		return corrupt("non-zero header padding") // keeps the skeleton canonical
	}
	r.pos += br.alignedPos()
	lit, dist := ExpandLengths(m)
	if lit == nil || lit[256] == 0 || !deflate.ValidLengths(lit, false) || !deflate.ValidLengths(dist, true) {
		return corrupt("invalid literal/length or distance code")
	}
	return nil
}

// ExpandLengths expands the code-length symbol sequence into literal/length
// and distance code lengths; nil if the sequence is inconsistent.
func ExpandLengths(m *BlockMeta) (lit, dist []uint8) {
	nlit, ndist := int(m.HLIT)+257, int(m.HDIST)+1
	lens := make([]uint8, 0, nlit+ndist)
	for _, cs := range m.CLSyms {
		switch {
		case cs.Sym < 16:
			lens = append(lens, cs.Sym)
		case cs.Sym == 16:
			if len(lens) == 0 {
				return nil, nil
			}
			p := lens[len(lens)-1]
			for j := 0; j < 3+int(cs.Extra); j++ {
				lens = append(lens, p)
			}
		case cs.Sym == 17:
			for j := 0; j < 3+int(cs.Extra); j++ {
				lens = append(lens, 0)
			}
		case cs.Sym == 18:
			for j := 0; j < 11+int(cs.Extra); j++ {
				lens = append(lens, 0)
			}
		default:
			return nil, nil
		}
		if len(lens) > nlit+ndist {
			return nil, nil
		}
	}
	if len(lens) != nlit+ndist {
		return nil, nil
	}
	return lens[:nlit:nlit], lens[nlit:]
}
