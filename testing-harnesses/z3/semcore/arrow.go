package semcore

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// A strict, bounded reader for the few FlatBuffers tables an uncompressed Arrow IPC file
// needs (Message, Schema/Field, RecordBatch). It validates every offset and executes nothing.
type fb struct {
	b   []byte
	pos int
}

var errFB = errors.New("semcore: malformed flatbuffer")

func (t fb) u32(off int) (uint32, bool) {
	if off < 0 || off+4 > len(t.b) {
		return 0, false
	}
	return binary.LittleEndian.Uint32(t.b[off:]), true
}

func fbRoot(b []byte) (fb, error) {
	o, ok := fb{b: b}.u32(0)
	if !ok || int(o) < 4 || int(o) >= len(b) {
		return fb{}, errFB
	}
	return fb{b, int(o)}, nil
}

// field returns the absolute offset of slot's value, or -1 when absent.
func (t fb) field(slot int) (int, error) {
	so, ok := t.u32(t.pos)
	if !ok {
		return -1, errFB
	}
	vt := t.pos - int(int32(so))
	if vt < 0 || vt+4 > len(t.b) {
		return -1, errFB
	}
	vlen := int(binary.LittleEndian.Uint16(t.b[vt:]))
	if vlen < 4 || vt+vlen > len(t.b) {
		return -1, errFB
	}
	if 4+2*slot+2 > vlen {
		return -1, nil
	}
	fo := int(binary.LittleEndian.Uint16(t.b[vt+4+2*slot:]))
	if fo == 0 {
		return -1, nil
	}
	if t.pos+fo >= len(t.b) {
		return -1, errFB
	}
	return t.pos + fo, nil
}

func (t fb) int64f(slot int) (int64, bool, error) {
	o, err := t.field(slot)
	if err != nil || o < 0 {
		return 0, false, err
	}
	if o+8 > len(t.b) {
		return 0, false, errFB
	}
	return int64(binary.LittleEndian.Uint64(t.b[o:])), true, nil
}

func (t fb) small(slot, width int) (uint64, bool, error) {
	o, err := t.field(slot)
	if err != nil || o < 0 {
		return 0, false, err
	}
	if o+width > len(t.b) {
		return 0, false, errFB
	}
	var v uint64
	for i := 0; i < width; i++ {
		v |= uint64(t.b[o+i]) << (8 * i)
	}
	return v, true, nil
}

func (t fb) table(slot int) (fb, bool, error) {
	o, err := t.field(slot)
	if err != nil || o < 0 {
		return fb{}, false, err
	}
	r, ok := t.u32(o)
	if !ok || o+int(r) >= len(t.b) || r == 0 {
		return fb{}, false, errFB
	}
	return fb{t.b, o + int(r)}, true, nil
}

// vector returns the element base offset and count.
func (t fb) vector(slot int) (int, int, bool, error) {
	o, err := t.field(slot)
	if err != nil || o < 0 {
		return 0, 0, false, err
	}
	r, ok := t.u32(o)
	if !ok {
		return 0, 0, false, errFB
	}
	v := o + int(r)
	n, ok := t.u32(v)
	if !ok || n > 1<<20 {
		return 0, 0, false, errFB
	}
	return v + 4, int(n), true, nil
}

func (t fb) str(slot int) (string, error) {
	o, err := t.field(slot)
	if err != nil || o < 0 {
		return "", err
	}
	r, ok := t.u32(o)
	if !ok {
		return "", errFB
	}
	s := o + int(r)
	n, ok := t.u32(s)
	if !ok || s+4+int(n) > len(t.b) {
		return "", errFB
	}
	return string(t.b[s+4 : s+4+int(n)]), nil
}

// ArrowCol describes one top-level field the lane understands.
type ArrowCol struct {
	Name      string
	DType     int // semcas dtype for fixed-width columns; DPlainBytes for utf8/binary
	Width     int
	Supported bool
	Why       string
}

// DPlainBytes is the canonical dtype of a variable-length binary/utf8 column stream:
// u32 little-endian length followed by the value bytes, per value.
const DPlainBytes = 200

type ArrowBuf struct{ Off, Len int64 }

// ArrowBatch is one RecordBatch: absolute body start and per-field buffers.
type ArrowBatch struct {
	Rows    int64
	Bodystr int64 // absolute file offset of the body
	Bufs    []ArrowBuf
	Nulls   []int64
	MsgOff  int64 // absolute offset of the message prefix
}

// ArrowFile is the parsed framing of an uncompressed Arrow IPC file.
type ArrowFile struct {
	Cols    []ArrowCol
	Batches []ArrowBatch
}

// ParseArrowFile reads the schema and record-batch framing. Dictionary batches,
// compression, nested or otherwise unsupported fields leave their columns unsupported but
// keep the file tiled (their bytes simply stay literals).
func ParseArrowFile(b []byte) (*ArrowFile, error) {
	if len(b) < 24 || string(b[:6]) != "ARROW1" || string(b[len(b)-6:]) != "ARROW1" {
		return nil, errors.New("semcore: not an Arrow IPC file")
	}
	af := &ArrowFile{}
	pos := 8
	end := len(b) - 6
	for pos+8 <= end {
		start := pos
		if binary.LittleEndian.Uint32(b[pos:]) != 0xffffffff {
			return nil, errors.New("semcore: arrow continuation marker missing")
		}
		ml := int(int32(binary.LittleEndian.Uint32(b[pos+4:])))
		pos += 8
		if ml == 0 {
			break // end of stream: footer follows
		}
		if ml < 0 || ml%8 != 0 || pos+ml > end {
			return nil, errors.New("semcore: bad arrow message length")
		}
		root, err := fbRoot(b[pos : pos+ml])
		if err != nil {
			return nil, err
		}
		ht, _, err := root.small(1, 1)
		if err != nil {
			return nil, err
		}
		bl, _, err := root.int64f(3)
		if err != nil {
			return nil, err
		}
		body := int64(pos + ml)
		if bl < 0 || body+bl > int64(end) {
			return nil, errors.New("semcore: bad arrow body length")
		}
		hdr, ok, err := root.table(2)
		if err != nil || !ok {
			return nil, errFB
		}
		switch ht {
		case 1: // Schema
			if err := af.readSchema(hdr); err != nil {
				return nil, err
			}
		case 2: // DictionaryBatch: every column becomes unsupported
			for i := range af.Cols {
				af.Cols[i].Supported, af.Cols[i].Why = false, "dictionary batch"
			}
		case 3:
			bt, err := readBatch(hdr, body, bl, int64(start))
			if err != nil {
				return nil, err
			}
			af.Batches = append(af.Batches, bt)
		}
		pos = int(body + bl)
	}
	if len(af.Cols) == 0 {
		return nil, errors.New("semcore: arrow file without schema")
	}
	return af, nil
}

func (af *ArrowFile) readSchema(s fb) error {
	if end, ok, err := s.small(0, 2); err != nil || (ok && end != 0) {
		return fmt.Errorf("semcore: big-endian arrow schema unsupported")
	}
	base, n, ok, err := s.vector(1)
	if err != nil || !ok {
		return errFB
	}
	for i := 0; i < n; i++ {
		fo := base + 4*i
		r, ok := s.u32(fo)
		if !ok {
			return errFB
		}
		f := fb{s.b, fo + int(r)}
		col := ArrowCol{}
		if col.Name, err = f.str(0); err != nil {
			return err
		}
		tt, _, err := f.small(2, 1)
		if err != nil {
			return err
		}
		ty, has, err := f.table(3)
		if err != nil {
			return err
		}
		_, hasDict, _ := f.table(4)
		_, cb, _, _ := f.vector(5)
		switch {
		case hasDict:
			col.Why = "dictionary-encoded"
		case cb > 0:
			col.Why = "nested"
		case tt == 2 && has: // Int
			bw, _, _ := ty.small(0, 4)
			sg, _, _ := ty.small(1, 1)
			col.Width = int(bw / 8)
			switch {
			case bw == 32 && sg == 1:
				col.DType = DI32
			case bw == 64 && sg == 1:
				col.DType = DI64
			case bw == 32:
				col.DType = DU32
			case bw == 64:
				col.DType = DU64
			default:
				col.Why = fmt.Sprintf("int%d", bw) // Parquet widens narrow ints to INT32
			}
		case tt == 3 && has: // FloatingPoint
			pr, _, _ := ty.small(0, 2)
			switch pr {
			case 1:
				col.DType, col.Width = DF32, 4
			case 2:
				col.DType, col.Width = DF64, 8
			default:
				col.Why = "half float"
			}
		case tt == 4 || tt == 5: // Binary, Utf8
			col.DType = DPlainBytes
		default:
			col.Why = fmt.Sprintf("arrow type %d", tt)
		}
		col.Supported = col.Why == ""
		af.Cols = append(af.Cols, col)
	}
	return nil
}

func readBatch(h fb, body, bodyLen, msgOff int64) (ArrowBatch, error) {
	var bt ArrowBatch
	var err error
	if bt.Rows, _, err = h.int64f(0); err != nil || bt.Rows < 0 {
		return bt, errFB
	}
	if _, has, _ := h.table(3); has {
		return bt, errors.New("semcore: compressed arrow batch")
	}
	nb, nn, ok, err := h.vector(1)
	if err != nil || !ok {
		return bt, errFB
	}
	for i := 0; i < nn; i++ {
		if nb+16*i+16 > len(h.b) {
			return bt, errFB
		}
		bt.Nulls = append(bt.Nulls, int64(binary.LittleEndian.Uint64(h.b[nb+16*i+8:])))
	}
	bb, bn, ok, err := h.vector(2)
	if err != nil || !ok {
		return bt, errFB
	}
	for i := 0; i < bn; i++ {
		p := bb + 16*i
		if p+16 > len(h.b) {
			return bt, errFB
		}
		off := int64(binary.LittleEndian.Uint64(h.b[p:]))
		ln := int64(binary.LittleEndian.Uint64(h.b[p+8:]))
		if off < 0 || ln < 0 || off > bodyLen || ln > bodyLen-off {
			return bt, errFB
		}
		bt.Bufs = append(bt.Bufs, ArrowBuf{Off: body + off, Len: ln})
	}
	bt.Bodystr, bt.MsgOff = body, msgOff
	return bt, nil
}
