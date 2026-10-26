package zstdz3

import (
	"encoding/binary"
	"math/bits"
	"sort"
)

// Compressed-block structure (RFC 8878 section 3.1.1.3): a literals section then a
// sequences section. scanBlock parses both headers and updates the cross-block coding
// state; decodeLiterals and decodeSeqs then run the entropy decoders.

const (
	litRaw     = 0
	litRLE     = 1
	litComp    = 2
	litTreeles = 3

	iLL = 0
	iOF = 1
	iML = 2
)

// codingState is what a compressed block inherits from earlier blocks of its frame.
type codingState struct {
	huff *huffTable
	fse  [3]*fseTable // LL, OF, ML
}

type litHdr struct {
	mode, form uint8
	regen      int
	treeDesc   []byte // verbatim tree description (mode litComp)
	tree       *huffTable
	streams    int
	body       []byte // raw: the literals; RLE: one byte; else jump table + streams
}

type seqHdr struct {
	n        int
	modes    byte
	tabBytes []byte // verbatim table descriptions between the modes byte and the bitstream
	tabs     [3]*fseTable
	stream   []byte
}

type blockHdr struct {
	lit litHdr
	seq seqHdr
}

var (
	llBase = [36]uint32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 18, 20, 22, 24, 28, 32, 40, 48, 64, 128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536}
	llBits = [36]uint8{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 3, 3, 4, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	mlBase = [53]uint32{3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 37, 39, 41, 43, 47, 51, 59, 67, 83, 99, 131, 259, 515, 1027, 2051, 4099, 8195, 16387, 32771, 65539}
	mlBits = [53]uint8{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 3, 3, 4, 4, 5, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}

	maxAcc = [3]int{maxAccLL, maxAccOF, maxAccML}
	maxSym = [3]int{maxSymLL, maxSymOF, maxSymML}
)

func llCode(v uint32) int {
	if v < 16 {
		return int(v)
	}
	return sort.Search(36, func(i int) bool { return llBase[i] > v }) - 1
}

func mlCode(v uint32) int {
	if v < 35 {
		return int(v) - 3
	}
	return sort.Search(53, func(i int) bool { return mlBase[i] > v }) - 1
}

func ofCode(v uint32) int { return 31 - bits.LeadingZeros32(v) }

// seqRec is one sequence: literal length, match length, and the offset value as coded
// (1..3 are repeat-offset codes, larger values are offset+3).
type seqRec struct{ ll, ml, of uint32 }

func scanBlock(st *codingState, c []byte, bmax int) (*blockHdr, error) {
	h := &blockHdr{}
	used, err := scanLiterals(st, c, bmax, &h.lit)
	if err != nil {
		return nil, err
	}
	if err := scanSeqs(st, c[used:], bmax, &h.seq); err != nil {
		return nil, err
	}
	return h, nil
}

func scanLiterals(st *codingState, c []byte, bmax int, l *litHdr) (int, error) {
	if len(c) == 0 {
		return 0, corrupt("empty compressed block")
	}
	b0 := c[0]
	l.mode = b0 & 3
	sf := b0 >> 2 & 3
	need := func(n int) error {
		if len(c) < n {
			return corrupt("truncated literals header")
		}
		return nil
	}
	if l.mode == litRaw || l.mode == litRLE {
		var hdr int
		switch sf {
		case 0, 2:
			hdr, l.form, l.regen = 1, 0, int(b0>>3)
		case 1:
			if err := need(2); err != nil {
				return 0, err
			}
			hdr, l.form, l.regen = 2, 1, int(b0>>4)|int(c[1])<<4
		default:
			if err := need(3); err != nil {
				return 0, err
			}
			hdr, l.form, l.regen = 3, 2, int(b0>>4)|int(c[1])<<4|int(c[2])<<12
		}
		if l.regen > bmax {
			return 0, corrupt("literals of %d bytes in a block of at most %d", l.regen, bmax)
		}
		n := l.regen
		if l.mode == litRLE {
			n = 1
		}
		if len(c)-hdr < n {
			return 0, corrupt("truncated literals")
		}
		l.body = c[hdr : hdr+n]
		return hdr + n, nil
	}
	l.form = sf
	var hdr, regen, comp int
	switch sf {
	case 0, 1:
		if err := need(3); err != nil {
			return 0, err
		}
		v := uint32(c[0]) | uint32(c[1])<<8 | uint32(c[2])<<16
		hdr, regen, comp = 3, int(v>>4&0x3ff), int(v>>14&0x3ff)
	case 2:
		if err := need(4); err != nil {
			return 0, err
		}
		v := binary.LittleEndian.Uint32(c)
		hdr, regen, comp = 4, int(v>>4&0x3fff), int(v>>18&0x3fff)
	default:
		if err := need(5); err != nil {
			return 0, err
		}
		v := uint64(binary.LittleEndian.Uint32(c)) | uint64(c[4])<<32
		hdr, regen, comp = 5, int(v>>4&0x3ffff), int(v>>22&0x3ffff)
	}
	l.streams = 4
	if sf == 0 {
		l.streams = 1
	}
	if regen == 0 || regen > bmax {
		return 0, corrupt("compressed literals of %d bytes in a block of at most %d", regen, bmax)
	}
	if comp == 0 || len(c)-hdr < comp {
		return 0, corrupt("literals section of %d bytes overruns the block", comp)
	}
	l.regen = regen
	sec := c[hdr : hdr+comp]
	if l.mode == litComp {
		t, n, err := readHuffTree(sec)
		if err != nil {
			return 0, err
		}
		l.treeDesc, l.tree, sec = sec[:n], t, sec[n:]
		st.huff = t
	} else {
		if st.huff == nil {
			return 0, corrupt("treeless literals without a previous huffman tree")
		}
		l.tree = st.huff
	}
	if len(sec) == 0 || (l.streams == 4 && len(sec) < 7) {
		return 0, corrupt("literals streams missing")
	}
	l.body = sec
	return hdr + comp, nil
}

// parseSeqTables reads the three table descriptions selected by modes from b and
// resolves repeat modes against prev. It returns the tables and the bytes used.
func parseSeqTables(modes byte, b []byte, prev [3]*fseTable) (tabs [3]*fseTable, used int, err error) {
	for k := 0; k < 3; k++ {
		switch modes >> uint(6-2*k) & 3 {
		case 0:
			tabs[k] = predTables[k]
		case 1:
			if used >= len(b) {
				return tabs, 0, corrupt("truncated rle table")
			}
			if int(b[used]) > maxSym[k] {
				return tabs, 0, corrupt("rle symbol %d", b[used])
			}
			tabs[k] = rleTable(b[used])
			used++
		case 2:
			t, n, err := readFSEDesc(b[used:], maxAcc[k], maxSym[k])
			if err != nil {
				return tabs, 0, err
			}
			t.kind, t.desc = 2, append([]byte(nil), b[used:used+n]...)
			tabs[k] = t
			used += n
		default:
			if prev[k] == nil {
				return tabs, 0, corrupt("repeat table without a previous table")
			}
			tabs[k] = prev[k]
		}
	}
	return tabs, used, nil
}

func scanSeqs(st *codingState, c []byte, bmax int, s *seqHdr) error {
	if len(c) == 0 {
		return corrupt("missing sequences section")
	}
	b0 := int(c[0])
	p := 1
	switch {
	case b0 == 0:
		if len(c) != 1 {
			return corrupt("bytes after an empty sequences section")
		}
		return nil
	case b0 < 128:
		s.n = b0
	case b0 < 255:
		if len(c) < 2 {
			return corrupt("truncated sequence count")
		}
		s.n, p = (b0-128)<<8+int(c[1]), 2
	default:
		if len(c) < 3 {
			return corrupt("truncated sequence count")
		}
		s.n, p = int(c[1])+int(c[2])<<8+0x7f00, 3
	}
	if s.n > bmax/3 {
		return corrupt("%d sequences cannot fit a block of %d", s.n, bmax)
	}
	if len(c) < p+1 {
		return corrupt("truncated sequence modes")
	}
	s.modes = c[p]
	if s.modes&3 != 0 {
		return corrupt("reserved sequence mode bits")
	}
	tabs, used, err := parseSeqTables(s.modes, c[p+1:], st.fse)
	if err != nil {
		return err
	}
	s.tabs, s.tabBytes = tabs, c[p+1:p+1+used]
	s.stream = c[p+1+used:]
	if len(s.stream) == 0 {
		return corrupt("missing sequence bitstream")
	}
	st.fse = tabs
	return nil
}

func fitBuf(b []byte, n int) []byte {
	if cap(b) < n {
		return make([]byte, n, max(n, 2*cap(b)))
	}
	return b[:n]
}

// decodeLiterals returns the regenerated literal bytes (into scratch when possible).
func decodeLiterals(l *litHdr, scratch []byte) ([]byte, error) {
	switch l.mode {
	case litRaw:
		return l.body, nil
	case litRLE:
		out := fitBuf(scratch, l.regen)
		for i := range out {
			out[i] = l.body[0]
		}
		return out, nil
	}
	out := fitBuf(scratch, l.regen)
	if l.streams == 1 {
		return out, l.tree.decodeStream(l.body, out)
	}
	j, body := l.body[:6], l.body[6:]
	s1, s2, s3 := int(binary.LittleEndian.Uint16(j)), int(binary.LittleEndian.Uint16(j[2:])), int(binary.LittleEndian.Uint16(j[4:]))
	s4 := len(body) - s1 - s2 - s3
	seg := (l.regen + 3) / 4
	last := l.regen - 3*seg
	if s4 < 1 || s1 < 1 || s2 < 1 || s3 < 1 || last < 0 {
		return nil, corrupt("bad huffman jump table")
	}
	parts := [4][]byte{body[:s1], body[s1 : s1+s2], body[s1+s2 : s1+s2+s3], body[s1+s2+s3:]}
	for i := 0; i < 4; i++ {
		lo, hi := i*seg, (i+1)*seg
		if i == 3 {
			hi = l.regen
		}
		if err := l.tree.decodeStream(parts[i], out[lo:hi]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// decodeSeqs decodes the sequence bitstream. rank holds, per table, the position of
// the final state among the states that carry its symbol: the one free choice a
// producer makes that the symbols themselves do not determine.
func decodeSeqs(s *seqHdr) (trip []seqRec, rank [3]uint32, err error) {
	r, err := newRevReader(s.stream)
	if err != nil {
		return nil, rank, err
	}
	var st [3]int
	for _, k := range [3]int{iLL, iOF, iML} {
		v, ok := r.read(int(s.tabs[k].acc))
		if !ok {
			return nil, rank, corrupt("truncated sequence states")
		}
		st[k] = int(v)
	}
	trip = make([]seqRec, s.n)
	for i := 0; i < s.n; i++ {
		el, eo, em := s.tabs[iLL].dec[st[iLL]], s.tabs[iOF].dec[st[iOF]], s.tabs[iML].dec[st[iML]]
		ob, ok1 := r.read(int(eo.sym))
		mb, ok2 := r.read(int(mlBits[em.sym]))
		lb, ok3 := r.read(int(llBits[el.sym]))
		if !(ok1 && ok2 && ok3) {
			return nil, rank, corrupt("sequence bitstream underrun")
		}
		trip[i] = seqRec{ll: llBase[el.sym] + lb, ml: mlBase[em.sym] + mb, of: 1<<uint(eo.sym) + ob}
		if i+1 == s.n {
			break
		}
		for _, p := range [3]struct {
			k int
			e fseDec
		}{{iLL, el}, {iML, em}, {iOF, eo}} {
			v, ok := r.read(int(p.e.nb))
			if !ok {
				return nil, rank, corrupt("sequence bitstream underrun")
			}
			st[p.k] = int(p.e.base) + int(v)
		}
	}
	if r.pos != 0 {
		return nil, rank, corrupt("sequence bitstream has %d unused bits", r.pos)
	}
	for k := 0; k < 3; k++ {
		rank[k] = uint32(s.tabs[k].rank(st[k]))
	}
	return trip, rank, nil
}

// history is the sliding window of one frame's decoded output.
type history struct {
	buf    []byte
	window int64
	total  int64 // bytes of the frame decoded before the current block
}

// reserve makes room for a block of up to n bytes, keeping window bytes of history.
// The buffer slides only when full, with slack so that the copy is amortized.
func (h *history) reserve(n int) {
	if len(h.buf)+n <= cap(h.buf) {
		return
	}
	keep := min(int64(len(h.buf)), h.window)
	need := keep + int64(n)
	if int64(cap(h.buf)) >= need {
		copy(h.buf, h.buf[int64(len(h.buf))-keep:])
		h.buf = h.buf[:keep]
		return
	}
	nb := make([]byte, keep, need+min(h.window, 16<<20)+4*maxBlock)
	copy(nb, h.buf[int64(len(h.buf))-keep:])
	h.buf = nb
}

// exec runs the sequences over the literals, appending the block's output to h.buf and
// updating the recent offsets. bmax bounds the block's decoded size.
func (h *history) exec(trip []seqRec, lits []byte, rep *[3]uint32, bmax int) error {
	out := h.buf
	start := len(out)
	lp := 0
	for _, s := range trip {
		ll, ml := int(s.ll), int(s.ml)
		if ll > len(lits)-lp {
			return corrupt("sequence uses more literals than the section holds")
		}
		if len(out)-start+ll+ml > bmax {
			return corrupt("block decodes past %d bytes", bmax)
		}
		out = append(out, lits[lp:lp+ll]...)
		lp += ll
		off, err := resolveOffset(rep, s)
		if err != nil {
			return err
		}
		if int64(off) > h.window || int(off) > len(out) || off == 0 {
			return corrupt("offset %d beyond the %d bytes of history", off, min(int64(len(out)), h.window))
		}
		for rem := ml; rem > 0; {
			n := min(rem, int(off))
			out = append(out, out[len(out)-int(off):len(out)-int(off)+n]...)
			rem -= n
		}
	}
	out = append(out, lits[lp:]...)
	if len(out)-start > bmax {
		return corrupt("block decodes past %d bytes", bmax)
	}
	h.buf = out
	return nil
}
