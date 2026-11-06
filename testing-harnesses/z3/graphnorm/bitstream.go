package graphnorm

import (
	"errors"
	"fmt"
)

// LLVM bitstream container: an exact, reversible parse tree. Parsing then writing the
// tree must reproduce the input bit for bit; ParseBitstream verifies this and rejects
// anything non-canonical (non-minimal VBR, nonzero padding, wrong block length words).

const (
	encFixed = 1
	encVBR   = 2
	encArray = 3
	encChar6 = 4
	encBlob  = 5
)

type AbbrevOp struct {
	Lit   bool
	Val   uint64 // literal value
	Enc   uint8
	Width uint64
}
type Abbrev []AbbrevOp

type Record struct {
	AbbrevID uint64
	Code     uint64
	Ops      []uint64 // all values after the code, literals included, arrays flattened
	Blob     []byte
}

const (
	ItemRec = iota
	ItemBlock
	ItemDef
)

type Item struct {
	Kind uint8
	Rec  *Record
	Blk  *Block
	Def  Abbrev
}

type Block struct {
	ID, Width uint64
	Items     []Item
}

// Bitcode is a whole file: magic, top-level blocks, and any trailing bytes.
type Bitcode struct {
	Blocks []*Block
	Tail   []byte
}

const (
	MaxBitDepth   = 32
	MaxBitRecords = 4_000_000
	MaxBitOps     = 64_000_000
)

var errBit = errors.New("graphnorm: invalid or non-canonical bitstream")

type bitReader struct {
	d    []byte
	pos  uint64
	end  uint64
	err  error
	recs int
	ops  int
}

func (r *bitReader) fail(f string, a ...any) {
	if r.err == nil {
		r.err = fmt.Errorf("%w: "+f, append([]any{errBit}, a...)...)
	}
}
func (r *bitReader) bits(n uint64) uint64 {
	if r.err != nil {
		return 0
	}
	if n > 64 || n > r.end-r.pos {
		r.fail("read past end at bit %d", r.pos)
		return 0
	}
	var v, sh uint64
	for n > 0 {
		take := min(n, 8-r.pos%8)
		v |= (uint64(r.d[r.pos/8]) >> (r.pos % 8) & (1<<take - 1)) << sh
		r.pos += take
		sh += take
		n -= take
	}
	return v
}
func (r *bitReader) vbr(w uint64) uint64 {
	if w < 2 || w > 32 {
		r.fail("vbr width %d", w)
		return 0
	}
	var v, sh uint64
	for i := 0; ; i++ {
		x := r.bits(w)
		if r.err != nil {
			return 0
		}
		part := x & (1<<(w-1) - 1)
		if part != 0 && (sh >= 64 || (part<<sh)>>sh != part) {
			r.fail("vbr overflow")
			return 0
		}
		v |= part << sh
		if x>>(w-1) == 0 {
			// canonical: the last chunk may be zero only for a single-chunk value
			if i > 0 && part == 0 {
				r.fail("non-minimal vbr")
			}
			return v
		}
		sh += w - 1
		if i > 70 {
			r.fail("vbr too long")
			return 0
		}
	}
}
func (r *bitReader) align() {
	n := (32 - r.pos%32) % 32
	if r.bits(n) != 0 {
		r.fail("nonzero alignment padding")
	}
}

// ParseBitstream parses a raw LLVM bitcode file (magic 'BC' 0xC0DE).
func ParseBitstream(d []byte) (*Bitcode, error) {
	if len(d) < 8 || d[0] != 'B' || d[1] != 'C' || d[2] != 0xC0 || d[3] != 0xDE || len(d)%4 != 0 {
		return nil, fmt.Errorf("%w: header", errBit)
	}
	r := &bitReader{d: d, pos: 32, end: uint64(len(d)) * 8}
	bc := &Bitcode{}
	info := map[uint64][]Abbrev{}
	for r.pos < r.end && r.err == nil {
		start := r.pos
		id := r.bits(2)
		if id != 1 {
			// unparseable top level: keep exact tail bytes
			r.pos = start
			bc.Tail = append([]byte(nil), d[start/8:]...)
			break
		}
		b := r.block(0, 2, info)
		if r.err != nil {
			return nil, r.err
		}
		bc.Blocks = append(bc.Blocks, b)
	}
	if r.err != nil {
		return nil, r.err
	}
	// exactness proof
	out := bc.Bytes()
	if len(out) != len(d) || string(out) != string(d) {
		return nil, fmt.Errorf("%w: re-encode mismatch", errBit)
	}
	return bc, nil
}

func (r *bitReader) block(depth int, outerW uint64, info map[uint64][]Abbrev) *Block {
	if depth > MaxBitDepth {
		r.fail("depth")
		return nil
	}
	b := &Block{ID: r.vbr(8), Width: r.vbr(4)}
	r.align()
	words := r.bits(32)
	if r.err != nil {
		return nil
	}
	if b.Width < 2 || b.Width > 32 || words*32 > r.end-r.pos {
		r.fail("block header")
		return nil
	}
	bodyEnd := r.pos + words*32
	abbrevs := append([]Abbrev(nil), info[b.ID]...)
	curBID := ^uint64(0)
	for r.err == nil {
		if r.pos >= bodyEnd {
			r.fail("block overrun")
			return nil
		}
		id := r.bits(b.Width)
		switch id {
		case 0:
			r.align()
			if r.pos != bodyEnd {
				r.fail("block length word mismatch")
			}
			return b
		case 1:
			sub := r.block(depth+1, b.Width, info)
			if r.err != nil {
				return nil
			}
			b.Items = append(b.Items, Item{Kind: ItemBlock, Blk: sub})
		case 2:
			a := r.definition()
			if r.err != nil {
				return nil
			}
			b.Items = append(b.Items, Item{Kind: ItemDef, Def: a})
			if b.ID == 0 {
				if curBID == ^uint64(0) {
					r.fail("blockinfo abbrev before SETBID")
					return nil
				}
				info[curBID] = append(info[curBID], a)
			} else {
				abbrevs = append(abbrevs, a)
			}
		default:
			rec := r.record(id, abbrevs)
			if r.err != nil {
				return nil
			}
			if b.ID == 0 && rec.Code == 1 {
				if len(rec.Ops) < 1 {
					r.fail("SETBID")
					return nil
				}
				curBID = rec.Ops[0]
			}
			b.Items = append(b.Items, Item{Kind: ItemRec, Rec: rec})
		}
	}
	return nil
}

func (r *bitReader) definition() Abbrev {
	n := r.vbr(5)
	if r.err != nil || n == 0 || n > 512 {
		r.fail("abbrev size")
		return nil
	}
	a := make(Abbrev, 0, n)
	for i := uint64(0); i < n && r.err == nil; i++ {
		var o AbbrevOp
		if r.bits(1) == 1 {
			o.Lit = true
			o.Val = r.vbr(8)
		} else {
			o.Enc = uint8(r.bits(3))
			switch o.Enc {
			case encFixed, encVBR:
				o.Width = r.vbr(5)
				if o.Width > 32 || (o.Enc == encVBR && o.Width < 2 && o.Width != 0) {
					r.fail("abbrev width")
				}
			case encArray:
				if i != n-2 {
					r.fail("array position")
				}
			case encChar6:
			case encBlob:
				if i != n-1 {
					r.fail("blob position")
				}
			default:
				r.fail("abbrev encoding")
			}
		}
		a = append(a, o)
	}
	if r.err == nil && n >= 2 && a[n-2].Enc == encArray && !a[n-2].Lit {
		last := a[n-1]
		if last.Lit || last.Enc == encArray || last.Enc == encBlob {
			r.fail("array element")
		}
	}
	return a
}

func (r *bitReader) scalar(o AbbrevOp) uint64 {
	if o.Lit {
		return o.Val
	}
	switch o.Enc {
	case encFixed:
		return r.bits(o.Width)
	case encVBR:
		if o.Width == 0 {
			return 0
		}
		return r.vbr(o.Width)
	case encChar6:
		return uint64(char6Dec[r.bits(6)])
	}
	r.fail("scalar encoding")
	return 0
}

func (r *bitReader) record(id uint64, abbrevs []Abbrev) *Record {
	r.recs++
	if r.recs > MaxBitRecords {
		r.fail("too many records")
		return nil
	}
	rec := &Record{AbbrevID: id}
	if id == 3 {
		rec.Code = r.vbr(6)
		n := r.vbr(6)
		if r.err != nil || n > uint64(r.end-r.pos) {
			r.fail("unabbrev op count")
			return nil
		}
		r.ops += int(n)
		if r.ops > MaxBitOps {
			r.fail("ops")
			return nil
		}
		rec.Ops = make([]uint64, n)
		for i := range rec.Ops {
			rec.Ops[i] = r.vbr(6)
		}
		return rec
	}
	if id-4 >= uint64(len(abbrevs)) {
		r.fail("unknown abbrev id %d", id)
		return nil
	}
	a := abbrevs[id-4]
	if a[0].Enc == encArray || a[0].Enc == encBlob {
		r.fail("abbrev code op is array/blob")
		return nil
	}
	rec.Code = r.scalar(a[0])
	for i := 1; i < len(a) && r.err == nil; i++ {
		o := a[i]
		switch {
		case !o.Lit && o.Enc == encArray:
			n := r.vbr(6)
			elt := a[i+1]
			if r.err != nil || n > r.end-r.pos+1 {
				r.fail("array length")
				return nil
			}
			if elt.Enc == encFixed && elt.Width == 0 || elt.Enc == encVBR && elt.Width == 0 {
				if n > 1<<24 {
					r.fail("zero-width array")
					return nil
				}
			}
			r.ops += int(n)
			if r.ops > MaxBitOps {
				r.fail("ops")
				return nil
			}
			for j := uint64(0); j < n && r.err == nil; j++ {
				rec.Ops = append(rec.Ops, r.scalar(elt))
			}
			return rec
		case !o.Lit && o.Enc == encBlob:
			n := r.vbr(6)
			r.align()
			if r.err != nil || n*8 > r.end-r.pos {
				r.fail("blob length")
				return nil
			}
			rec.Blob = append([]byte(nil), r.d[r.pos/8:r.pos/8+n]...)
			r.pos += n * 8
			r.align()
			return rec
		default:
			rec.Ops = append(rec.Ops, r.scalar(o))
		}
	}
	return rec
}

const char6Chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._"

var char6Dec [64]byte
var char6Enc [256]int16

func init() {
	for i := range char6Enc {
		char6Enc[i] = -1
	}
	for i := 0; i < 64; i++ {
		char6Dec[i] = char6Chars[i]
		char6Enc[char6Chars[i]] = int16(i)
	}
}

// ---- writer ----

type BitWriter struct {
	b   []byte
	cur uint64
	n   uint64 // bits in cur
}

func (w *BitWriter) Bits(v, n uint64) {
	for n > 0 {
		take := min(n, 8-w.n)
		w.cur |= (v & (1<<take - 1)) << w.n
		w.n += take
		v >>= take
		n -= take
		if w.n == 8 {
			w.b = append(w.b, byte(w.cur))
			w.cur, w.n = 0, 0
		}
	}
}
func (w *BitWriter) VBR(v, width uint64) {
	hi := uint64(1) << (width - 1)
	for v >= hi {
		w.Bits(v&(hi-1)|hi, width)
		v >>= width - 1
	}
	w.Bits(v, width)
}
func (w *BitWriter) BitPos() uint64 { return uint64(len(w.b))*8 + w.n }
func (w *BitWriter) Align() {
	if p := w.BitPos() % 32; p != 0 {
		w.Bits(0, 32-p)
	}
}
func (w *BitWriter) Bytes() []byte { return w.b }

// Patch32 overwrites an already-flushed aligned 32-bit word at byte offset off.
func (w *BitWriter) Patch32(off int, v uint32) {
	w.b[off], w.b[off+1], w.b[off+2], w.b[off+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// Bytes serializes the tree.
func (bc *Bitcode) Bytes() []byte {
	w := &BitWriter{}
	w.Bits(0xDEC04342, 32)
	info := map[uint64][]Abbrev{}
	for _, b := range bc.Blocks {
		w.Bits(1, 2)
		writeBlock(w, b, info)
	}
	w.b = append(w.b, bc.Tail...)
	return w.b
}

func writeBlock(w *BitWriter, b *Block, info map[uint64][]Abbrev) {
	w.VBR(b.ID, 8)
	w.VBR(b.Width, 4)
	w.Align()
	lenOff := len(w.b)
	w.Bits(0, 32)
	start := len(w.b)
	abbrevs := append([]Abbrev(nil), info[b.ID]...)
	curBID := ^uint64(0)
	for _, it := range b.Items {
		switch it.Kind {
		case ItemBlock:
			w.Bits(1, b.Width)
			writeBlock(w, it.Blk, info)
		case ItemDef:
			w.Bits(2, b.Width)
			WriteDefinition(w, it.Def)
			if b.ID == 0 {
				info[curBID] = append(info[curBID], it.Def)
			} else {
				abbrevs = append(abbrevs, it.Def)
			}
		default:
			WriteRecord(w, b.Width, it.Rec, abbrevs)
			if b.ID == 0 && it.Rec.Code == 1 && len(it.Rec.Ops) > 0 {
				curBID = it.Rec.Ops[0]
			}
		}
	}
	w.Bits(0, b.Width)
	w.Align()
	w.Patch32(lenOff, uint32((len(w.b)-start)/4))
}

func WriteDefinition(w *BitWriter, a Abbrev) {
	w.VBR(uint64(len(a)), 5)
	for _, o := range a {
		if o.Lit {
			w.Bits(1, 1)
			w.VBR(o.Val, 8)
			continue
		}
		w.Bits(0, 1)
		w.Bits(uint64(o.Enc), 3)
		if o.Enc == encFixed || o.Enc == encVBR {
			w.VBR(o.Width, 5)
		}
	}
}

func writeScalar(w *BitWriter, o AbbrevOp, v uint64) {
	if o.Lit {
		return
	}
	switch o.Enc {
	case encFixed:
		w.Bits(v, o.Width)
	case encVBR:
		if o.Width != 0 {
			w.VBR(v, o.Width)
		}
	case encChar6:
		w.Bits(uint64(char6Enc[byte(v)]), 6)
	}
}

// WriteRecord emits a record using the abbreviation table in scope. Values that do not fit
// their abbreviation are not detected here; callers prove the result by comparison.
func WriteRecord(w *BitWriter, width uint64, rec *Record, abbrevs []Abbrev) {
	w.Bits(rec.AbbrevID, width)
	if rec.AbbrevID == 3 {
		w.VBR(rec.Code, 6)
		w.VBR(uint64(len(rec.Ops)), 6)
		for _, v := range rec.Ops {
			w.VBR(v, 6)
		}
		return
	}
	a := abbrevs[rec.AbbrevID-4]
	writeScalar(w, a[0], rec.Code)
	k := 0
	for i := 1; i < len(a); i++ {
		o := a[i]
		switch {
		case !o.Lit && o.Enc == encArray:
			n := len(rec.Ops) - k
			w.VBR(uint64(n), 6)
			for ; k < len(rec.Ops); k++ {
				writeScalar(w, a[i+1], rec.Ops[k])
			}
			return
		case !o.Lit && o.Enc == encBlob:
			w.VBR(uint64(len(rec.Blob)), 6)
			w.Align()
			for _, c := range rec.Blob {
				w.Bits(uint64(c), 8)
			}
			w.Align()
			return
		default:
			writeScalar(w, o, rec.Ops[k])
			k++
		}
	}
}

// BlockInfo returns, for a parsed tree, the abbreviations registered per block ID.
func (bc *Bitcode) BlockInfo() map[uint64][]Abbrev {
	info := map[uint64][]Abbrev{}
	var walk func(b *Block)
	walk = func(b *Block) {
		if b.ID == 0 {
			cur := ^uint64(0)
			for _, it := range b.Items {
				if it.Kind == ItemRec && it.Rec.Code == 1 && len(it.Rec.Ops) > 0 {
					cur = it.Rec.Ops[0]
				}
				if it.Kind == ItemDef {
					info[cur] = append(info[cur], it.Def)
				}
			}
		}
		for _, it := range b.Items {
			if it.Kind == ItemBlock {
				walk(it.Blk)
			}
		}
	}
	for _, b := range bc.Blocks {
		walk(b)
	}
	return info
}
