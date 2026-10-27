// Package ggufz3 is the Z3-29 strict, bounded GGUF (v2/v3) structure parser: stdlib only,
// ReaderAt plus size, no allocation driven by a hostile count. It finds the exact byte
// spans of the header (magic .. alignment padding) and of every tensor; it never
// interprets tensor payloads (a quantised tensor is an opaque exact block sequence) and
// never dequantises. Malformed input means "no GGUF representation", never an invalid
// object. Native names, types and shapes are hints; SHA-256 of the exact bytes is identity.
//
// Layout parsed (little-endian):
//
//	"GGUF" | version u32 (2 or 3) | tensor_count u64 | kv_count u64
//	kv_count x ( key string | value type u32 | value )
//	tensor_count x ( name string | n_dims u32 | dims u64[n_dims] | ggml_type u32 | offset u64 )
//	padding to the alignment (general.alignment, default 32) | tensor data
//
// A string is u64 length + bytes; an array is element type u32 + count u64 + elements.
package ggufz3

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"sort"
)

var (
	ErrFormat      = errors.New("ggufz3: not a valid GGUF file")
	ErrUnsupported = errors.New("ggufz3: unsupported version or tensor type")
	ErrLimit       = errors.New("ggufz3: limit exceeded")
)

// Limits bound every parse.
type Limits struct {
	MaxMeta    int64 // bytes from the start of the file through the last tensor info (default 64 MiB)
	MaxTensors int64 // default 1<<20
	MaxKV      int64 // default 1<<20
	MaxString  int64 // default 1<<20 (keys are capped at 1<<12)
	MaxDepth   int   // array nesting (default 2)
}

func (l Limits) norm() Limits {
	if l.MaxMeta <= 0 {
		l.MaxMeta = 64 << 20
	}
	if l.MaxTensors <= 0 {
		l.MaxTensors = 1 << 20
	}
	if l.MaxKV <= 0 {
		l.MaxKV = 1 << 20
	}
	if l.MaxString <= 0 {
		l.MaxString = 1 << 20
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = 2
	}
	return l
}

// Value types of the key/value section.
const (
	TUint8, TInt8, TUint16, TInt16, TUint32, TInt32, TFloat32, TBool, TString, TArray, TUint64, TInt64, TFloat64 = 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12
)

var scalarSize = map[uint32]int64{TUint8: 1, TInt8: 1, TUint16: 2, TInt16: 2, TUint32: 4, TInt32: 4, TFloat32: 4, TBool: 1, TUint64: 8, TInt64: 8, TFloat64: 8}

// KV is one metadata entry. Off/End span the whole entry (key through value).
type KV struct {
	Key      string
	Type     uint32
	ElemType uint32 // arrays
	Count    uint64 // arrays: element count
	Uint     uint64 // integer/bool scalars (sign-extension left to the caller)
	Str      string // strings up to 256 bytes (longer ones are only spanned)
	Off, End int64
}

// Tensor is one directory entry with its absolute byte span in the file.
type Tensor struct {
	Index      int
	Name       string
	Dims       []uint64
	Type       uint32
	RelOffset  uint64
	Begin, End int64 // absolute span; End-Begin = nbytes from the type's block geometry
}

// Size is the tensor payload length.
func (t *Tensor) Size() int64 { return t.End - t.Begin }

// File is a parsed GGUF structure.
type File struct {
	Version   uint32
	Size      int64
	Alignment int64
	InfoEnd   int64 // end of the last tensor info
	DataStart int64 // InfoEnd rounded up to Alignment: first byte of the tensor data area
	KVs       []KV
	Tensors   []Tensor // directory order
	Order     []int    // indexes of Tensors by ascending Begin
	DataEnd   int64    // end of the last tensor (== DataStart when there are none)
}

// Lookup returns the KV with the key, if any.
func (f *File) Lookup(key string) (*KV, bool) {
	for i := range f.KVs {
		if f.KVs[i].Key == key {
			return &f.KVs[i], true
		}
	}
	return nil, false
}

// TypeGeom is the block geometry of a ggml tensor type: elements per block and bytes per block.
type TypeGeom struct {
	Name  string
	Elems int64
	Bytes int64
}

// Geom is the finite, explicit table (ggml.h / ggml-common.h block sizes). An absent type is
// refused with ErrUnsupported, never guessed. Q4_2, Q4_3, Q4_0_4_4/4_8/8_8, IQ4_NL_x_y were
// removed upstream and are not supported.
var Geom = map[uint32]TypeGeom{
	0: {"F32", 1, 4}, 1: {"F16", 1, 2}, 2: {"Q4_0", 32, 18}, 3: {"Q4_1", 32, 20}, 6: {"Q5_0", 32, 22}, 7: {"Q5_1", 32, 24},
	8: {"Q8_0", 32, 34}, 9: {"Q8_1", 32, 40}, 10: {"Q2_K", 256, 84}, 11: {"Q3_K", 256, 110}, 12: {"Q4_K", 256, 144},
	13: {"Q5_K", 256, 176}, 14: {"Q6_K", 256, 210}, 15: {"Q8_K", 256, 292}, 16: {"IQ2_XXS", 256, 66}, 17: {"IQ2_XS", 256, 74},
	18: {"IQ3_XXS", 256, 98}, 19: {"IQ1_S", 256, 50}, 20: {"IQ4_NL", 32, 18}, 21: {"IQ3_S", 256, 110}, 22: {"IQ2_S", 256, 82},
	23: {"IQ4_XS", 256, 136}, 24: {"I8", 1, 1}, 25: {"I16", 1, 2}, 26: {"I32", 1, 4}, 27: {"I64", 1, 8}, 28: {"F64", 1, 8},
	29: {"IQ1_M", 256, 56}, 30: {"BF16", 1, 2}, 34: {"TQ1_0", 256, 54}, 35: {"TQ2_0", 256, 66}, 39: {"MXFP4", 32, 17},
}

// NBytes is the exact byte size of a tensor of the type with the dims: the element count must
// be a whole number of blocks, and every product is overflow-checked.
func NBytes(typ uint32, dims []uint64) (int64, error) {
	g, ok := Geom[typ]
	if !ok {
		return 0, fmt.Errorf("%w: ggml type %d", ErrUnsupported, typ)
	}
	if len(dims) == 0 {
		return 0, fmt.Errorf("%w: no dimensions", ErrFormat)
	}
	n := uint64(1)
	for _, d := range dims {
		hi, lo := bits.Mul64(n, d)
		if hi != 0 || lo > math.MaxInt64 {
			return 0, fmt.Errorf("%w: element count overflows", ErrFormat)
		}
		n = lo
	}
	if n == 0 {
		return 0, nil
	}
	// ggml requires the innermost dimension to hold whole blocks; ne0 % blck == 0
	if dims[0]%uint64(g.Elems) != 0 {
		return 0, fmt.Errorf("%w: dim0 %d is not a multiple of the %s block (%d)", ErrFormat, dims[0], g.Name, g.Elems)
	}
	blocks := n / uint64(g.Elems)
	hi, lo := bits.Mul64(blocks, uint64(g.Bytes))
	if hi != 0 || lo > math.MaxInt64 {
		return 0, fmt.Errorf("%w: byte size overflows", ErrFormat)
	}
	return int64(lo), nil
}

// reader is a bounded sequential reader over a ReaderAt, refilled in 1 MiB pieces and never
// past limit; every request is checked against limit before it allocates or reads.
type reader struct {
	r     io.ReaderAt
	pos   int64
	limit int64
	buf   []byte
	bpos  int64 // file offset of buf[0]
}

func (b *reader) need(n int64) ([]byte, error) {
	if n < 0 || n > b.limit-b.pos {
		return nil, fmt.Errorf("%w: read of %d bytes at %d runs past the %d-byte bound", ErrFormat, n, b.pos, b.limit)
	}
	if b.pos < b.bpos || b.pos+n > b.bpos+int64(len(b.buf)) {
		want := max(n, 1<<20)
		want = min(want, b.limit-b.pos)
		nb := make([]byte, want)
		got, err := b.r.ReadAt(nb, b.pos)
		if int64(got) < n {
			if err == nil || err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("%w: %v", ErrFormat, err)
		}
		b.buf, b.bpos = nb[:got], b.pos
	}
	s := b.buf[b.pos-b.bpos : b.pos-b.bpos+n]
	b.pos += n
	return s, nil
}

func (b *reader) u32() (uint32, error) {
	s, err := b.need(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(s), nil
}

func (b *reader) u64() (uint64, error) {
	s, err := b.need(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(s), nil
}

func (b *reader) str(max int64) (string, error) {
	n, err := b.u64()
	if err != nil {
		return "", err
	}
	if n > uint64(max) {
		return "", fmt.Errorf("%w: string of %d bytes", ErrLimit, n)
	}
	s, err := b.need(int64(n))
	return string(s), err
}

// skipString advances over a string and returns its length, keeping up to keep bytes.
func (b *reader) skipString(lim Limits, keep int) (string, error) {
	n, err := b.u64()
	if err != nil {
		return "", err
	}
	if n > uint64(lim.MaxString) {
		return "", fmt.Errorf("%w: string of %d bytes", ErrLimit, n)
	}
	if int64(n) > b.limit-b.pos {
		return "", fmt.Errorf("%w: string of %d bytes past the bound", ErrFormat, n)
	}
	if int(n) <= keep {
		s, err := b.need(int64(n))
		return string(s), err
	}
	b.pos += int64(n)
	return "", nil
}

// value consumes one value of type t; scalars fill kv.Uint, strings kv.Str, arrays kv.ElemType/Count.
func (b *reader) value(t uint32, kv *KV, lim Limits, depth int) error {
	if n, ok := scalarSize[t]; ok {
		s, err := b.need(n)
		if err != nil {
			return err
		}
		if depth == 0 {
			var buf [8]byte
			copy(buf[:], s)
			kv.Uint = binary.LittleEndian.Uint64(buf[:])
		}
		return nil
	}
	switch t {
	case TString:
		s, err := b.skipString(lim, 256)
		if depth == 0 {
			kv.Str = s
		}
		return err
	case TArray:
		if depth >= lim.MaxDepth {
			return fmt.Errorf("%w: array nesting", ErrLimit)
		}
		et, err := b.u32()
		if err != nil {
			return err
		}
		cnt, err := b.u64()
		if err != nil {
			return err
		}
		if depth == 0 {
			kv.ElemType, kv.Count = et, cnt
		}
		if n, ok := scalarSize[et]; ok {
			// the count must fit the remaining bound before anything is walked or allocated
			if cnt > uint64(math.MaxInt64)/uint64(n) || int64(cnt)*n > b.limit-b.pos {
				return fmt.Errorf("%w: array of %d x %d bytes past the bound", ErrFormat, cnt, n)
			}
			b.pos += int64(cnt) * n
			return nil
		}
		if et != TString && et != TArray {
			return fmt.Errorf("%w: array element type %d", ErrFormat, et)
		}
		// every string/array element costs at least 8 bytes
		if cnt > uint64(b.limit-b.pos)/8 {
			return fmt.Errorf("%w: array of %d elements past the bound", ErrFormat, cnt)
		}
		var scratch KV
		for i := uint64(0); i < cnt; i++ {
			if err := b.value(et, &scratch, lim, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("%w: value type %d", ErrFormat, t)
}

// Parse reads the structure of a GGUF file of the given size. It reads only the header
// (bounded by Limits.MaxMeta), never tensor payloads.
func Parse(r io.ReaderAt, size int64, lim Limits) (*File, error) {
	lim = lim.norm()
	if size < 24 {
		return nil, fmt.Errorf("%w: %d bytes", ErrFormat, size)
	}
	b := &reader{r: r, limit: min(size, lim.MaxMeta)}
	m, err := b.need(4)
	if err != nil || string(m) != "GGUF" {
		return nil, fmt.Errorf("%w: bad magic", ErrFormat)
	}
	f := &File{Size: size, Alignment: 32}
	if f.Version, err = b.u32(); err != nil {
		return nil, err
	}
	if f.Version != 2 && f.Version != 3 {
		return nil, fmt.Errorf("%w: GGUF version %d", ErrUnsupported, f.Version)
	}
	nt, err := b.u64()
	if err != nil {
		return nil, err
	}
	nkv, err := b.u64()
	if err != nil {
		return nil, err
	}
	if nt > uint64(lim.MaxTensors) || nkv > uint64(lim.MaxKV) {
		return nil, fmt.Errorf("%w: %d tensors, %d key/values", ErrLimit, nt, nkv)
	}
	// every entry costs at least 12 bytes, so the counts are checked against the bound
	// before anything is allocated
	if nkv > uint64(b.limit-b.pos)/12 || nt > uint64(b.limit-b.pos)/24 {
		return nil, fmt.Errorf("%w: counts exceed the header bound", ErrFormat)
	}
	f.KVs = make([]KV, 0, nkv)
	seen := make(map[string]bool, nkv)
	for i := uint64(0); i < nkv; i++ {
		var kv KV
		kv.Off = b.pos
		if kv.Key, err = b.str(1 << 12); err != nil {
			return nil, err
		}
		if kv.Key == "" || seen[kv.Key] {
			return nil, fmt.Errorf("%w: empty or duplicate key %q", ErrFormat, kv.Key)
		}
		seen[kv.Key] = true
		if kv.Type, err = b.u32(); err != nil {
			return nil, err
		}
		if err = b.value(kv.Type, &kv, lim, 0); err != nil {
			return nil, err
		}
		kv.End = b.pos
		f.KVs = append(f.KVs, kv)
	}
	if kv, ok := f.Lookup("general.alignment"); ok {
		if kv.Type != TUint32 || kv.Uint == 0 || kv.Uint&(kv.Uint-1) != 0 || kv.Uint > 1<<20 {
			return nil, fmt.Errorf("%w: general.alignment %d", ErrFormat, kv.Uint)
		}
		f.Alignment = int64(kv.Uint)
	}
	f.Tensors = make([]Tensor, 0, nt)
	names := make(map[string]bool, nt)
	for i := uint64(0); i < nt; i++ {
		t := Tensor{Index: int(i)}
		if t.Name, err = b.str(1 << 12); err != nil {
			return nil, err
		}
		if names[t.Name] {
			return nil, fmt.Errorf("%w: duplicate tensor name %q", ErrFormat, t.Name)
		}
		names[t.Name] = true
		nd, err := b.u32()
		if err != nil {
			return nil, err
		}
		if nd == 0 || nd > 4 {
			return nil, fmt.Errorf("%w: %d dimensions", ErrFormat, nd)
		}
		t.Dims = make([]uint64, nd)
		for j := range t.Dims {
			if t.Dims[j], err = b.u64(); err != nil {
				return nil, err
			}
		}
		if t.Type, err = b.u32(); err != nil {
			return nil, err
		}
		if t.RelOffset, err = b.u64(); err != nil {
			return nil, err
		}
		f.Tensors = append(f.Tensors, t)
	}
	f.InfoEnd = b.pos
	f.DataStart = (f.InfoEnd + f.Alignment - 1) / f.Alignment * f.Alignment
	if f.DataStart > size || f.DataStart < f.InfoEnd {
		return nil, fmt.Errorf("%w: data area starts at %d, file has %d bytes", ErrFormat, f.DataStart, size)
	}
	f.DataEnd = f.DataStart
	for i := range f.Tensors {
		t := &f.Tensors[i]
		n, err := NBytes(t.Type, t.Dims)
		if err != nil {
			return nil, fmt.Errorf("tensor %q: %w", t.Name, err)
		}
		if t.RelOffset%uint64(f.Alignment) != 0 || t.RelOffset > uint64(size-f.DataStart) || uint64(n) > uint64(size-f.DataStart)-t.RelOffset {
			return nil, fmt.Errorf("%w: tensor %q [%d,+%d) is unaligned or outside the %d-byte data area", ErrFormat, t.Name, t.RelOffset, n, size-f.DataStart)
		}
		t.Begin = f.DataStart + int64(t.RelOffset)
		t.End = t.Begin + n
		f.DataEnd = max(f.DataEnd, t.End)
	}
	f.Order = make([]int, len(f.Tensors))
	for i := range f.Order {
		f.Order[i] = i
	}
	sort.SliceStable(f.Order, func(a, c int) bool { return f.Tensors[f.Order[a]].Begin < f.Tensors[f.Order[c]].Begin })
	for k := 1; k < len(f.Order); k++ {
		p, c := &f.Tensors[f.Order[k-1]], &f.Tensors[f.Order[k]]
		if p.Size() > 0 && c.Size() > 0 && p.End > c.Begin {
			return nil, fmt.Errorf("%w: tensors %q and %q overlap", ErrFormat, p.Name, c.Name)
		}
	}
	return f, nil
}

// Segment is one piece of the exact file layout, in file order.
type Segment struct {
	Begin, End int64
	Tensor     int // index into File.Tensors, or -1 for header / alignment gap / tail
	Kind       string
}

// Segments tiles [0,Size) exactly: the header (magic through the padding that precedes the
// data area), each tensor, the gaps between tensors and the tail after the last one.
func (f *File) Segments() []Segment {
	segs := []Segment{{0, f.DataStart, -1, "header"}}
	pos := f.DataStart
	for _, i := range f.Order {
		t := &f.Tensors[i]
		if t.Size() == 0 {
			continue
		}
		if t.Begin > pos {
			segs = append(segs, Segment{pos, t.Begin, -1, "gap"})
		}
		segs = append(segs, Segment{t.Begin, t.End, i, "tensor"})
		pos = t.End
	}
	if pos < f.Size {
		segs = append(segs, Segment{pos, f.Size, -1, "tail"})
	}
	return segs
}
