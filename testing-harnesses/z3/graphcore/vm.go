package graphcore

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/adler32"
	"hash/crc32"
)

// The transformation VM is a restricted, deterministic, bounded writer. It has no loops,
// no jumps, no callbacks and no unbounded allocation: a program is a flat op sequence whose
// length bounds its work, and every output byte is accounted against MaxOut. The final
// artifact SHA-256 and length are verified by the caller (Recipe replay), never trusted.
//
// Op families: byte (LIT, COPY, GATHER, U, LEB, CINT, LEN, CKSUM, MARK, PATCH, ZERO) and the
// bitstream family (ENTER, END, DEFABBR, REC, BLOB, SETINST) used by LLVM-style containers.

const (
	OpLit     = 0x10 // n, bytes
	OpCopy    = 0x11 // blobIndex, off, len
	OpGather  = 0x12 // blobIndex, n, (off,len)*
	OpU       = 0x13 // width(1..8), endian(0 LE,1 BE), token
	OpLEB     = 0x14 // signed(0/1), minBytes, token  (minBytes preserves non-minimal widths)
	OpCInt    = 0x15 // ECMA-335 compressed unsigned, token
	OpLenBeg  = 0x16 // width, endian, flags(bit0: include the length field itself)
	OpLenEnd  = 0x17
	OpCksum   = 0x18 // kind(0 crc32,1 adler32,2 sha256[:4]), width, endian ; over [last MARK, now)
	OpMark    = 0x19
	OpPatch   = 0x1A // backOffset(uvarint from end), n, bytes
	OpZero    = 0x1B // n
	OpAlign   = 0x1C // alignment (bytes), fill byte
	OpDerive  = 0x1F // kind(0 adler32 LE,1 crc32 LE,2 sha1,3 sha256,4 adler32 BE,5 crc32 BE), patchOff, fromOff: patch checksum of out[fromOff:] at patchOff
	OpStream  = 0x1D // blobIndex, off: position the column-stream cursor
	OpTable   = 0x1E // nrows, ncols, (width, predictor)*: expand column-major varint streams to fixed-width rows
	OpEnter   = 0x02 // blockID, width
	OpEnd     = 0x03
	OpDefAbb  = 0x04 // nops, (flags|enc, value/width)*
	OpSetInst = 0x05 // nargs
	OpExt     = 0x20 // per table: n, n keys : declares the extern lists for following tokens
	// 0x80..0xFF: record (bit6 = defines a value, low 6 bits = abbrev id, 63 = explicit)
)

// Token kinds (low 3 bits of a uvarint).
const (
	TokLit  = 0 // payload = small literal
	TokXRel = 1 // payload = ext idx: InstReg - lookup
	TokXAbs = 2 // lookup
	TokXAb1 = 3 // lookup + 1
	TokLAbs = 4 // payload = k: TabLen(0) + k  (function-local absolute value id)
	TokXRlS = 5 // (InstReg - lookup) << 1  (signed-VBR relative)
	TokLMD  = 6 // payload = k<<1|plus1: TabLen(3) + k + plus1
	TokEsc  = 7 // next uvarint is the full literal
)

// Tables: 0 values, 1 types, 2 attribute lists, 3 metadata.
const NumTabs = 4

// KeyLen is the width of a reference key; collisions are detected and fall back to literals.
const KeyLen = 4

type Key [KeyLen]byte

type Table struct {
	Keys []Key
	rev  map[Key]int
	dup  map[Key]bool
}

func NewTable(keys []Key) *Table {
	t := &Table{Keys: keys, rev: make(map[Key]int, len(keys)), dup: map[Key]bool{}}
	for i, k := range keys {
		if _, ok := t.rev[k]; ok {
			t.dup[k] = true
			continue
		}
		t.rev[k] = i
	}
	return t
}

// Lookup resolves a key; duplicate or missing keys are errors (never silently aliased).
func (t *Table) Lookup(k Key) (int, error) {
	if t == nil {
		return 0, errors.New("vm: table missing")
	}
	if t.dup[k] {
		return 0, errors.New("vm: ambiguous key")
	}
	i, ok := t.rev[k]
	if !ok {
		return 0, errors.New("vm: unresolved key (stale dependency)")
	}
	return i, nil
}

const (
	MaxOut     = 1 << 30
	MaxOps     = 64_000_000
	MaxVMDepth = 64
	MaxExt     = 1 << 20
)

type frame struct {
	id, width uint64
	lenOff    int
	start     int
	abbrevs   []Abbrev
	curBID    uint64
}

type lenFrame struct {
	pos, width int
	big, self  bool
}

// Machine executes programs. Blobs are the resolvable COPY sources (checked by index).
type Machine struct {
	W      BitWriter
	Tabs   [NumTabs]*Table
	Blobs  [][]byte
	Inst   uint64
	info   map[uint64][]Abbrev
	stack  []frame
	ext    [NumTabs][]Key
	lens   []lenFrame
	mark   int
	ops    int
	topAbb uint64 // abbrev width at top level (2)
	sblob  []byte
	spos   int
	Limit  int // maximum output bytes (0 = MaxOut)
}

func (m *Machine) limit() int {
	if m.Limit > 0 && m.Limit < MaxOut {
		return m.Limit
	}
	return MaxOut
}

type extEnt struct {
	tab int
	key Key
}

func NewMachine() *Machine {
	m := &Machine{info: map[uint64][]Abbrev{}, topAbb: 2}
	m.W.Bits(0xDEC04342, 32)
	return m
}

// NewByteMachine has no bitcode magic: for byte-level (non-bitstream) programs.
func NewByteMachine() *Machine { return &Machine{info: map[uint64][]Abbrev{}, topAbb: 2} }

func (m *Machine) Output() []byte { return m.W.Bytes() }

func (m *Machine) Finish() error {
	if len(m.stack) != 0 || len(m.lens) != 0 {
		return errors.New("vm: unterminated block or length scope")
	}
	if m.W.n != 0 {
		return errors.New("vm: output not byte aligned")
	}
	return nil
}

type opReader struct {
	b   []byte
	pos int
	err error
}

func (r *opReader) uv() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 {
		r.err = errors.New("vm: bad varint")
		return 0
	}
	r.pos += n
	return v
}
func (r *opReader) byte() byte {
	if r.err != nil || r.pos >= len(r.b) {
		r.err = errors.New("vm: truncated program")
		return 0
	}
	c := r.b[r.pos]
	r.pos++
	return c
}
func (r *opReader) bytes(n uint64) []byte {
	if r.err != nil || n > uint64(len(r.b)-r.pos) {
		r.err = errors.New("vm: truncated program")
		return nil
	}
	s := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return s
}

func (m *Machine) token(r *opReader) (uint64, error) {
	t := r.uv()
	if r.err != nil {
		return 0, r.err
	}
	p := t >> 3
	switch t & 7 {
	case TokLit:
		return p, nil
	case TokEsc:
		return r.uv(), r.err
	case TokLAbs:
		if m.Tabs[0] == nil {
			return 0, errors.New("vm: value table missing")
		}
		return uint64(len(m.Tabs[0].Keys)) + p, nil
	case TokLMD:
		if m.Tabs[3] == nil {
			return 0, errors.New("vm: metadata table missing")
		}
		return uint64(len(m.Tabs[3].Keys)) + p>>1 + p&1, nil
	}
	tab, ei := int(p&3), p>>2
	if ei >= uint64(len(m.ext[tab])) {
		return 0, errors.New("vm: ext index out of range")
	}
	v, err := m.Tabs[tab].Lookup(m.ext[tab][ei])
	if err != nil {
		return 0, err
	}
	switch t & 7 {
	case TokXRel:
		if uint64(v) > m.Inst {
			return 0, errors.New("vm: relative reference underflow")
		}
		return m.Inst - uint64(v), nil
	case TokXAbs:
		return uint64(v), nil
	case TokXAb1:
		return uint64(v) + 1, nil
	case TokXRlS:
		if uint64(v) > m.Inst {
			return 0, errors.New("vm: relative reference underflow")
		}
		return (m.Inst - uint64(v)) << 1, nil
	}
	return 0, errors.New("vm: bad token")
}

func (m *Machine) cur() *frame {
	if len(m.stack) == 0 {
		return nil
	}
	return &m.stack[len(m.stack)-1]
}

func (m *Machine) curWidth() uint64 {
	if f := m.cur(); f != nil {
		return f.width
	}
	return m.topAbb
}

// Exec runs one program fragment. Fragments share machine state, so a file is produced
// by executing its pieces in order.
func (m *Machine) Exec(prog []byte) error {
	r := &opReader{b: prog}
	err := m.exec(r, prog)
	if err != nil {
		return fmt.Errorf("%w (program offset %d of %d)", err, r.pos, len(prog))
	}
	return nil
}

func (m *Machine) exec(r *opReader, prog []byte) error {
	for r.pos < len(prog) && r.err == nil {
		m.ops++
		if m.ops > MaxOps || len(m.W.b) > m.limit() {
			return errors.New("vm: work limit")
		}
		op := r.byte()
		if op >= 0x80 {
			if err := m.rec(r, op); err != nil {
				return err
			}
			continue
		}
		switch op {
		case OpExt:
			for t := 0; t < NumTabs && r.err == nil; t++ {
				n := r.uv()
				if n > MaxExt || n*KeyLen > uint64(len(prog)) {
					return errors.New("vm: ext too large")
				}
				m.ext[t] = m.ext[t][:0]
				for i := uint64(0); i < n && r.err == nil; i++ {
					var k Key
					copy(k[:], r.bytes(KeyLen))
					m.ext[t] = append(m.ext[t], k)
				}
			}
		case OpSetInst:
			n := r.uv()
			if m.Tabs[0] == nil {
				return errors.New("vm: value table missing")
			}
			m.Inst = uint64(len(m.Tabs[0].Keys)) + n
		case OpEnter:
			id, w := r.uv(), r.uv()
			if r.err != nil {
				break
			}
			if w < 2 || w > 32 || len(m.stack) >= MaxVMDepth {
				return errors.New("vm: bad block")
			}
			m.W.Bits(1, m.curWidth())
			m.W.VBR(id, 8)
			m.W.VBR(w, 4)
			m.W.Align()
			lenOff := len(m.W.b)
			m.W.Bits(0, 32)
			ab := append([]Abbrev(nil), m.info[id]...)
			m.stack = append(m.stack, frame{id: id, width: w, lenOff: lenOff, start: len(m.W.b), abbrevs: ab, curBID: ^uint64(0)})
		case OpEnd:
			f := m.cur()
			if f == nil {
				return errors.New("vm: END outside block")
			}
			m.W.Bits(0, f.width)
			m.W.Align()
			m.W.Patch32(f.lenOff, uint32((len(m.W.b)-f.start)/4))
			m.stack = m.stack[:len(m.stack)-1]
		case OpDefAbb:
			f := m.cur()
			if f == nil {
				return errors.New("vm: DEFABBR outside block")
			}
			n := r.uv()
			if n == 0 || n > 512 {
				return errors.New("vm: bad abbrev")
			}
			a := make(Abbrev, 0, n)
			for i := uint64(0); i < n && r.err == nil; i++ {
				fl := r.byte()
				v := r.uv()
				if fl == 0 {
					a = append(a, AbbrevOp{Lit: true, Val: v})
				} else {
					a = append(a, AbbrevOp{Enc: fl, Width: v})
				}
			}
			if r.err != nil {
				break
			}
			if err := checkAbbrev(a); err != nil {
				return err
			}
			m.W.Bits(2, f.width)
			WriteDefinition(&m.W, a)
			if f.id == 0 {
				if f.curBID == ^uint64(0) {
					return errors.New("vm: blockinfo abbrev before SETBID")
				}
				m.info[f.curBID] = append(m.info[f.curBID], a)
			} else {
				f.abbrevs = append(f.abbrevs, a)
			}
		case OpLit:
			n := r.uv()
			b := r.bytes(n)
			if r.err != nil {
				break
			}
			if err := m.bytesOK(); err != nil {
				return err
			}
			m.W.b = append(m.W.b, b...)
		case OpCopy:
			bi, off, n := r.uv(), r.uv(), r.uv()
			if r.err != nil {
				break
			}
			if err := m.copyRange(bi, off, n); err != nil {
				return err
			}
		case OpGather:
			bi, cnt := r.uv(), r.uv()
			if cnt > uint64(len(prog)) {
				return errors.New("vm: gather count")
			}
			for i := uint64(0); i < cnt && r.err == nil; i++ {
				off, n := r.uv(), r.uv()
				if r.err != nil {
					break
				}
				if err := m.copyRange(bi, off, n); err != nil {
					return err
				}
			}
		case OpU:
			w, end := r.byte(), r.byte()
			v, err := m.token(r)
			if err != nil {
				return err
			}
			if w < 1 || w > 8 || end > 1 || (w < 8 && v>>(8*uint(w)) != 0) {
				return errors.New("vm: U width/overflow")
			}
			if err := m.bytesOK(); err != nil {
				return err
			}
			var b [8]byte
			if end == 0 {
				binary.LittleEndian.PutUint64(b[:], v)
				m.W.b = append(m.W.b, b[:w]...)
			} else {
				binary.BigEndian.PutUint64(b[:], v)
				m.W.b = append(m.W.b, b[8-w:]...)
			}
		case OpLEB:
			signed, minb := r.byte(), r.byte()
			v, err := m.token(r)
			if err != nil {
				return err
			}
			if err := m.bytesOK(); err != nil {
				return err
			}
			m.W.b = appendLEB(m.W.b, v, signed == 1, int(minb))
		case OpCInt:
			v, err := m.token(r)
			if err != nil {
				return err
			}
			switch {
			case v < 0x80:
				m.W.b = append(m.W.b, byte(v))
			case v < 0x4000:
				m.W.b = append(m.W.b, byte(v>>8)|0x80, byte(v))
			case v < 0x20000000:
				m.W.b = append(m.W.b, byte(v>>24)|0xC0, byte(v>>16), byte(v>>8), byte(v))
			default:
				return errors.New("vm: compressed int overflow")
			}
		case OpLenBeg:
			w, end, fl := r.byte(), r.byte(), r.byte()
			if r.err != nil {
				break
			}
			if w < 1 || w > 8 || end > 1 || len(m.lens) >= MaxVMDepth {
				return errors.New("vm: bad length scope")
			}
			m.lens = append(m.lens, lenFrame{pos: len(m.W.b), width: int(w), big: end == 1, self: fl&1 != 0})
			m.W.b = append(m.W.b, make([]byte, w)...)
		case OpLenEnd:
			if len(m.lens) == 0 {
				return errors.New("vm: LEN_END without scope")
			}
			lf := m.lens[len(m.lens)-1]
			m.lens = m.lens[:len(m.lens)-1]
			n := uint64(len(m.W.b) - lf.pos - lf.width)
			if lf.self {
				n += uint64(lf.width)
			}
			if lf.width < 8 && n>>(8*uint(lf.width)) != 0 {
				return errors.New("vm: length overflow")
			}
			var b [8]byte
			if lf.big {
				binary.BigEndian.PutUint64(b[:], n)
				copy(m.W.b[lf.pos:], b[8-lf.width:])
			} else {
				binary.LittleEndian.PutUint64(b[:], n)
				copy(m.W.b[lf.pos:], b[:lf.width])
			}
		case OpMark:
			m.mark = len(m.W.b)
		case OpCksum:
			kind, w, end := r.byte(), r.byte(), r.byte()
			if r.err != nil {
				break
			}
			data := m.W.b[m.mark:]
			var v uint32
			switch kind {
			case 0:
				v = crc32.ChecksumIEEE(data)
			case 1:
				v = adler32.Checksum(data)
			case 2:
				s := sha256.Sum256(data)
				v = binary.BigEndian.Uint32(s[:4])
			default:
				return errors.New("vm: checksum kind")
			}
			if w < 1 || w > 4 || end > 1 {
				return errors.New("vm: checksum width")
			}
			var b [4]byte
			if end == 0 {
				binary.LittleEndian.PutUint32(b[:], v)
				m.W.b = append(m.W.b, b[:w]...)
			} else {
				binary.BigEndian.PutUint32(b[:], v)
				m.W.b = append(m.W.b, b[4-w:]...)
			}
		case OpPatch:
			back, n := r.uv(), r.uv()
			b := r.bytes(n)
			if r.err != nil {
				break
			}
			if back > uint64(len(m.W.b)) || n > back {
				return errors.New("vm: patch out of range")
			}
			copy(m.W.b[uint64(len(m.W.b))-back:], b)
		case OpZero:
			n := r.uv()
			if n > uint64(m.limit()) || len(m.W.b)+int(n) > m.limit() {
				return errors.New("vm: zero run too large")
			}
			m.W.b = append(m.W.b, make([]byte, n)...)
		case OpAlign:
			a, fill := r.uv(), r.byte()
			if a == 0 || a > 1<<20 {
				return errors.New("vm: alignment")
			}
			for uint64(len(m.W.b))%a != 0 {
				m.W.b = append(m.W.b, fill)
				if len(m.W.b) > m.limit() {
					return errors.New("vm: out limit")
				}
			}
		case OpDerive:
			kind := r.byte()
			patch, from := r.uv(), r.uv()
			if r.err != nil {
				break
			}
			if err := m.bytesOK(); err != nil {
				return err
			}
			if from > uint64(len(m.W.b)) {
				return errors.New("vm: derive range")
			}
			var sum []byte
			data := m.W.b[from:]
			switch kind {
			case 0, 4: // adler32: little-endian (DEX) / big-endian (zlib)
				var b [4]byte
				if kind == 0 {
					binary.LittleEndian.PutUint32(b[:], adler32.Checksum(data))
				} else {
					binary.BigEndian.PutUint32(b[:], adler32.Checksum(data))
				}
				sum = b[:]
			case 1, 5: // crc32 little-endian / big-endian
				var b [4]byte
				if kind == 1 {
					binary.LittleEndian.PutUint32(b[:], crc32.ChecksumIEEE(data))
				} else {
					binary.BigEndian.PutUint32(b[:], crc32.ChecksumIEEE(data))
				}
				sum = b[:]
			case 2:
				h := sha1.Sum(data)
				sum = h[:]
			case 3:
				h := sha256.Sum256(data)
				sum = h[:]
			default:
				return errors.New("vm: derive kind")
			}
			if patch > uint64(len(m.W.b)) || uint64(len(sum)) > uint64(len(m.W.b))-patch {
				return errors.New("vm: derive patch range")
			}
			copy(m.W.b[patch:], sum)
		case OpStream:
			bi, off := r.uv(), r.uv()
			if r.err != nil {
				break
			}
			if bi >= uint64(len(m.Blobs)) || off > uint64(len(m.Blobs[bi])) {
				return errors.New("vm: stream position")
			}
			m.sblob, m.spos = m.Blobs[bi], int(off)
		case OpTable:
			nrows, ncols := r.uv(), r.uv()
			if r.err != nil {
				break
			}
			if err := m.table(r, nrows, ncols); err != nil {
				return err
			}
		default:
			return fmt.Errorf("vm: unknown op 0x%02x", op)
		}
	}
	return r.err
}

func (m *Machine) bytesOK() error {
	if m.W.n != 0 {
		return errors.New("vm: byte op at unaligned bit position")
	}
	return nil
}

func (m *Machine) copyRange(bi, off, n uint64) error {
	if bi >= uint64(len(m.Blobs)) {
		return errors.New("vm: blob index")
	}
	b := m.Blobs[bi]
	if off > uint64(len(b)) || n > uint64(len(b))-off {
		return errors.New("vm: copy range")
	}
	if err := m.bytesOK(); err != nil {
		return err
	}
	if uint64(len(m.W.b))+n > uint64(m.limit()) {
		return errors.New("vm: out limit")
	}
	m.W.b = append(m.W.b, b[off:off+n]...)
	return nil
}

func checkAbbrev(a Abbrev) error {
	n := len(a)
	for i, o := range a {
		if o.Lit {
			continue
		}
		switch o.Enc {
		case encFixed, encVBR:
			if o.Width > 32 || (o.Enc == encVBR && o.Width == 1) {
				return errors.New("vm: abbrev width")
			}
		case encArray:
			if i != n-2 {
				return errors.New("vm: array position")
			}
			l := a[n-1]
			if l.Lit || l.Enc == encArray || l.Enc == encBlob {
				return errors.New("vm: array element")
			}
		case encChar6:
		case encBlob:
			if i != n-1 {
				return errors.New("vm: blob position")
			}
		default:
			return errors.New("vm: abbrev encoding")
		}
	}
	if a[0].Enc == encArray || a[0].Enc == encBlob {
		return errors.New("vm: abbrev code op")
	}
	return nil
}

func (m *Machine) rec(r *opReader, hb byte) error {
	f := m.cur()
	if f == nil {
		return errors.New("vm: record outside block")
	}
	def := hb&0x40 != 0
	abbrev := uint64(hb & 63)
	if abbrev == 63 {
		abbrev = r.uv()
	}
	if abbrev < 3 || r.err != nil {
		return errors.New("vm: bad abbrev id")
	}
	rec := &Record{AbbrevID: abbrev}
	if abbrev == 3 {
		rec.Code = r.uv()
		n := r.uv()
		if n > uint64(len(r.b)-r.pos) {
			return errors.New("vm: op count exceeds program")
		}
		rec.Ops = make([]uint64, n)
		for i := range rec.Ops {
			v, err := m.token(r)
			if err != nil {
				return err
			}
			rec.Ops[i] = v
		}
	} else {
		if abbrev-4 >= uint64(len(f.abbrevs)) {
			return errors.New("vm: abbrev id out of range")
		}
		a := f.abbrevs[abbrev-4]
		if a[0].Lit {
			rec.Code = a[0].Val
		} else {
			rec.Code = r.uv()
		}
		for i := 1; i < len(a) && r.err == nil; i++ {
			o := a[i]
			switch {
			case !o.Lit && o.Enc == encArray:
				n := r.uv()
				if n > uint64(len(r.b)-r.pos) {
					return errors.New("vm: array length exceeds program")
				}
				for j := uint64(0); j < n; j++ {
					v, err := m.token(r)
					if err != nil {
						return err
					}
					rec.Ops = append(rec.Ops, v)
				}
				i = len(a) // the element type op is not an operand
			case !o.Lit && o.Enc == encBlob:
				n := r.uv()
				rec.Blob = append([]byte(nil), r.bytes(n)...)
			case o.Lit:
				rec.Ops = append(rec.Ops, o.Val)
			default:
				v, err := m.token(r)
				if err != nil {
					return err
				}
				rec.Ops = append(rec.Ops, v)
			}
		}
		if r.err == nil {
			if err := validateAgainst(a, rec, rec.Blob != nil); err != nil {
				return err
			}
		}
	}
	if r.err != nil {
		return r.err
	}
	if abbrev >= 1<<f.width {
		return errors.New("vm: abbrev id wider than block")
	}
	WriteRecord(&m.W, f.width, rec, f.abbrevs)
	if f.id == 0 && rec.Code == 1 && len(rec.Ops) > 0 {
		f.curBID = rec.Ops[0]
	}
	if def {
		m.Inst++
	}
	return nil
}

// validateAgainst proves the record can be emitted exactly: literals match, scalars fit.
func validateAgainst(a Abbrev, rec *Record, blob bool) error {
	fits := func(o AbbrevOp, v uint64) bool {
		if o.Lit {
			return v == o.Val
		}
		switch o.Enc {
		case encFixed:
			return o.Width >= 64 || v>>o.Width == 0
		case encVBR:
			return o.Width != 0 || v == 0
		case encChar6:
			return v < 256 && char6Enc[v] >= 0
		}
		return false
	}
	if !fits(a[0], rec.Code) {
		return errors.New("vm: code does not fit abbrev")
	}
	k := 0
	for i := 1; i < len(a); i++ {
		o := a[i]
		switch {
		case !o.Lit && o.Enc == encArray:
			for ; k < len(rec.Ops); k++ {
				if !fits(a[i+1], rec.Ops[k]) {
					return errors.New("vm: array value does not fit abbrev")
				}
			}
			if blob {
				return errors.New("vm: blob with array abbrev")
			}
			return nil
		case !o.Lit && o.Enc == encBlob:
			if !blob || k != len(rec.Ops) {
				return errors.New("vm: blob mismatch")
			}
			return nil
		default:
			if k >= len(rec.Ops) || !fits(o, rec.Ops[k]) {
				return errors.New("vm: scalar does not fit abbrev")
			}
			k++
		}
	}
	if k != len(rec.Ops) || blob {
		return errors.New("vm: operand count mismatch")
	}
	return nil
}

func appendLEB(b []byte, v uint64, signed bool, minBytes int) []byte {
	start := len(b)
	for {
		c := byte(v & 0x7F)
		var more bool
		if signed {
			sv := int64(v) >> 7
			v = uint64(sv)
			more = !((sv == 0 && c&0x40 == 0) || (sv == -1 && c&0x40 != 0))
		} else {
			v >>= 7
			more = v != 0
		}
		if more {
			b = append(b, c|0x80)
			continue
		}
		if len(b)-start+1 >= minBytes {
			return append(b, c)
		}
		pad := byte(0)
		if signed && c&0x40 != 0 {
			pad = 0x7F
		}
		b = append(b, c|0x80)
		for len(b)-start+1 < minBytes {
			b = append(b, pad|0x80)
		}
		return append(b, pad)
	}
}

// Column predictors for OpTable.
const (
	PredRaw   = 0 // stored value is the field
	PredDelta = 1 // stored value is zigzag(field - previous field in the column)
)

func unzig(v uint64) int64 { return int64(v>>1) ^ -int64(v&1) }
func Zig(d int64) uint64   { return uint64(d<<1) ^ uint64(d>>63) }

func (m *Machine) table(r *opReader, nrows, ncols uint64) error {
	if ncols == 0 || ncols > 32 || nrows > uint64(m.limit())/ncols {
		return errors.New("vm: table shape")
	}
	type col struct{ w, pred int }
	cols := make([]col, ncols)
	rowW := 0
	for i := range cols {
		w, p := int(r.byte()), int(r.byte())
		if w < 1 || w > 8 || p > PredDelta {
			return errors.New("vm: table column")
		}
		cols[i] = col{w, p}
		rowW += w
	}
	if r.err != nil {
		return r.err
	}
	if uint64(len(m.W.b))+nrows*uint64(rowW) > uint64(m.limit()) {
		return errors.New("vm: out limit")
	}
	if err := m.bytesOK(); err != nil {
		return err
	}
	if nrows*ncols > uint64(len(m.sblob)-m.spos) {
		return errors.New("vm: column stream too short")
	}
	vals := make([][]uint64, ncols)
	for c := range vals {
		vals[c] = make([]uint64, nrows)
		var prev uint64
		for i := uint64(0); i < nrows; i++ {
			v, n := binary.Uvarint(m.sblob[m.spos:])
			if n <= 0 {
				return errors.New("vm: column stream varint")
			}
			m.spos += n
			if cols[c].pred == PredDelta {
				v = prev + uint64(unzig(v))
				prev = v
			}
			if cols[c].w < 8 && v>>(8*uint(cols[c].w)) != 0 {
				return errors.New("vm: column value overflows width")
			}
			vals[c][i] = v
		}
	}
	var tmp [8]byte
	for i := uint64(0); i < nrows; i++ {
		for c := range cols {
			binary.LittleEndian.PutUint64(tmp[:], vals[c][i])
			m.W.b = append(m.W.b, tmp[:cols[c].w]...)
		}
	}
	return nil
}
