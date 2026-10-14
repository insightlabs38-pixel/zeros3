package ir

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"

	"zeros3-z3/deflate"
)

// ---- E1: compact structural, byte oriented -------------------------------
//
//	unit  := codec(1) item*
//	item  := ctl(u16 LE) [litrun uvarint] [distance extra: ceil(bits/8) bytes]
//	ctl   := lenSym-257 (5 bits) | distCode (5) <<5 | lenExtra (5) <<10 | haveLits <<15
//
// lenSym field 31 marks a trailing literal-only item (litrun always present).
// Literal byte values are never stored; they come from the semantic plaintext.

func distExtraBytes(dc int) int { return (deflate.DistExtraBits(dc) + 7) / 8 }

func encodeE1(runs []Run) []byte {
	out := make([]byte, 1, 64)
	out[0] = byte(E1)
	for _, r := range runs {
		if r.M == 0 {
			out = binary.LittleEndian.AppendUint16(out, 31|1<<15)
			out = uvarint(out, uint64(r.Lits))
			continue
		}
		ctl := uint16(r.M.Sym()-257) | uint16(r.M.DistCode())<<5 | uint16(r.M.LenExtra())<<10
		if r.Lits > 0 {
			ctl |= 1 << 15
		}
		out = binary.LittleEndian.AppendUint16(out, ctl)
		if r.Lits > 0 {
			out = uvarint(out, uint64(r.Lits))
		}
		x, n := r.M.DistExtra(), distExtraBytes(r.M.DistCode())
		for i := 0; i < n; i++ {
			out = append(out, byte(x>>(8*i)))
		}
	}
	return out
}

func decodeE1(data []byte, plainLen int) ([]Run, error) {
	if len(data) < 1 || Codec(data[0]) != E1 {
		return nil, corrupt("E1 unit header")
	}
	b := data[1:]
	var runs []Run
	pos := 0
	for pos < plainLen {
		if len(b) < 2 {
			return nil, corrupt("E1 unit truncated")
		}
		ctl := binary.LittleEndian.Uint16(b)
		b = b[2:]
		ls := int(ctl & 31)
		var lits uint64
		if ctl>>15 == 1 {
			v, n := binary.Uvarint(b)
			if n <= 0 || (n > 1 && b[n-1] == 0) || v > uint64(plainLen-pos) {
				return nil, corrupt("E1 literal run")
			}
			lits, b = v, b[n:]
		}
		if ls == 31 {
			if lits == 0 || int(lits) != plainLen-pos {
				return nil, corrupt("E1 trailing literal run")
			}
			runs = append(runs, Run{Lits: uint32(lits)})
			pos += int(lits)
			continue
		}
		if ls > 28 {
			return nil, corrupt("E1 length symbol %d", ls)
		}
		dc := int(ctl >> 5 & 31)
		if dc > 29 {
			return nil, corrupt("E1 distance code %d", dc)
		}
		le := uint32(ctl >> 10 & 31)
		if le >= 1<<uint(deflate.LengthExtraBits(257+ls)) && !(le == 0) {
			return nil, corrupt("E1 length extra %d", le)
		}
		n := distExtraBytes(dc)
		if len(b) < n {
			return nil, corrupt("E1 unit truncated")
		}
		var de uint32
		for i := 0; i < n; i++ {
			de |= uint32(b[i]) << (8 * i)
		}
		b = b[n:]
		if de >= 1<<uint(deflate.DistExtraBits(dc)) && !(de == 0) {
			return nil, corrupt("E1 distance extra %d", de)
		}
		t := deflate.Match(257+ls, le, dc, de)
		adv := int(lits) + t.Length()
		if adv > plainLen-pos {
			return nil, corrupt("E1 unit overruns its plaintext")
		}
		pos += adv
		runs = append(runs, Run{Lits: uint32(lits), M: t})
	}
	if len(b) != 0 {
		return nil, corrupt("E1 unit has %d trailing bytes", len(b))
	}
	return runs, nil
}

func encodeE1Flate(runs []Run) []byte {
	raw := encodeE1(runs)
	var buf bytes.Buffer
	buf.WriteByte(byte(E1Flate))
	w, _ := flate.NewWriter(&buf, flate.BestCompression)
	w.Write(raw)
	w.Close()
	return buf.Bytes()
}

func decodeE1Flate(data []byte, plainLen int) ([]Run, error) {
	if len(data) < 1 || Codec(data[0]) != E1Flate {
		return nil, corrupt("E1+flate unit header")
	}
	// E1 spends at least 2 bytes per >= 3 plaintext bytes; bound the inflated size.
	limit := int64(plainLen)*4 + 64
	r := flate.NewReader(bytes.NewReader(data[1:]))
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, corrupt("E1+flate payload")
	}
	return decodeE1(raw, plainLen)
}

// ---- E2: range-coded structural -----------------------------------------

type e2model struct {
	lrUn    [4][24]bitModel
	lrMant  [24][4]bitModel
	lsym    [4][32]bitModel
	lext    [29][5]bitModel
	dsym    [3][32]bitModel
	dext    [30][14]bitModel
	prevLen int // previous match length
}

func newE2() *e2model {
	m := &e2model{}
	for i := range m.lrUn {
		for j := range m.lrUn[i] {
			m.lrUn[i][j].init()
		}
	}
	for i := range m.lrMant {
		for j := range m.lrMant[i] {
			m.lrMant[i][j].init()
		}
	}
	for i := range m.lsym {
		for j := range m.lsym[i] {
			m.lsym[i][j].init()
		}
	}
	for i := range m.lext {
		for j := range m.lext[i] {
			m.lext[i][j].init()
		}
	}
	for i := range m.dsym {
		for j := range m.dsym[i] {
			m.dsym[i][j].init()
		}
	}
	for i := range m.dext {
		for j := range m.dext[i] {
			m.dext[i][j].init()
		}
	}
	return m
}

func lenBucket(l int) int {
	switch {
	case l <= 4:
		return 0
	case l <= 8:
		return 1
	case l <= 16:
		return 2
	}
	return 3
}

func litBucket(n int) int {
	switch {
	case n == 0:
		return 0
	case n == 1:
		return 1
	case n <= 4:
		return 2
	}
	return 3
}

// litRun codes a literal-run length (the number of literal tokens before the
// next match, or at the end of the unit).
func (m *e2model) litRun(io *rcio, n int) int {
	io.cat = CatLitRun
	return io.gamma(m.lrUn[lenBucket(m.prevLen)][:], m.lrMant[:], n)
}

func (m *e2model) distCtx(length int) int {
	switch {
	case length <= 4:
		return 0
	case length <= 8:
		return 1
	}
	return 2
}

// distCodeOf returns the (code, extra) of distance d, 1..32768.
func distCodeOf(d int) (int, uint32) {
	lo, hi := 0, 29
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if deflate.DistBase(mid) <= d {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo, uint32(d - deflate.DistBase(lo))
}

// match codes a match token after a literal run of lastLits literals.
// When decoding, t is ignored. ok=false means an invalid symbol was decoded.
func (m *e2model) match(io *rcio, lastLits int, t deflate.Token) (deflate.Token, bool) {
	enc := io.dec == nil
	ls, le := 0, uint32(0)
	if enc {
		ls, le = t.Sym()-257, t.LenExtra()
	}
	io.cat = CatLenSym
	ls = io.tree(m.lsym[litBucket(lastLits)][:], 5, ls)
	if ls > 28 {
		return 0, false
	}
	io.cat = CatLenExtra
	nb := deflate.LengthExtraBits(257 + ls)
	var x uint32
	for i := nb - 1; i >= 0; i-- {
		x = x<<1 | uint32(io.bit(&m.lext[ls][i], int(le>>uint(i))&1))
	}
	length := deflate.LengthBase(257+ls) + int(x)
	lb := m.distCtx(length)
	// The recent-distance cache of Z3-06 was removed in Z3-08E: it saved
	// <=0.2% on periodic data and cost 3% on binary structures.
	var dc int
	var de uint32
	if enc {
		dc, de = t.DistCode(), t.DistExtra()
	}
	io.cat = CatDistSym
	dc = io.tree(m.dsym[lb][:], 5, dc)
	if dc > 29 {
		return 0, false
	}
	io.cat = CatDistExtra
	nb = deflate.DistExtraBits(dc)
	var y uint32
	for i := nb - 1; i >= 0; i-- {
		y = y<<1 | uint32(io.bit(&m.dext[dc][min(nb-1-i, 13)], int(de>>uint(i))&1))
	}
	de = y
	m.prevLen = length
	return deflate.Match(257+ls, x, dc, de), true
}

func encodeE2(runs []Run, cost *[NumCats]float64) []byte {
	e := newEncoder()
	io := &rcio{enc: e, cost: cost}
	m := newE2()
	for _, r := range runs {
		m.litRun(io, int(r.Lits))
		if r.M != 0 {
			m.match(io, int(r.Lits), r.M)
		}
	}
	return append([]byte{byte(E2)}, e.finish()...)
}

func decodeE2(data []byte, plainLen int) ([]Run, error) {
	if len(data) < 1 || Codec(data[0]) != E2 {
		return nil, corrupt("E2 unit header")
	}
	io := &rcio{dec: newDecoder(data[1:])}
	m := newE2()
	var runs []Run
	pos := 0
	for pos < plainLen {
		n := m.litRun(io, 0)
		if n < 0 || n > plainLen-pos {
			return nil, corrupt("E2 literal run %d", n)
		}
		pos += n
		if pos == plainLen {
			if n > 0 {
				runs = append(runs, Run{Lits: uint32(n)})
			}
			break
		}
		t, ok := m.match(io, n, 0)
		if !ok || t.Length() > plainLen-pos {
			return nil, corrupt("E2 match")
		}
		pos += t.Length()
		runs = append(runs, Run{Lits: uint32(n), M: t})
	}
	return runs, nil
}
