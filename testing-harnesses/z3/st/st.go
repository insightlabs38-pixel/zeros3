// Package st is a small, strict, stdlib-only Safetensors parser for the Z3
// research module (Z3-09A/B). It is structural: it never rewrites the header
// or the tensor bytes, so the exact file bytes stay authoritative. Parsing
// needs only an io.ReaderAt: the 8-byte length prefix and the header are read,
// tensor payloads are not touched until asked for.
//
// Format (https://huggingface.co/docs/safetensors/): an 8-byte little-endian
// header length N, N bytes of JSON (UTF-8, trailing space padding allowed),
// then the raw tensor bytes. The JSON object maps tensor names to
// {"dtype", "shape", "data_offsets": [begin, end]} (offsets relative to the end
// of the header) and may carry "__metadata__": {string: string}. Validation
// mirrors the reference implementation: offsets sorted and contiguous from 0
// to the end of the file, byte size equal to dtype x shape, no duplicates.
package st

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"sort"
	"strconv"
)

var (
	ErrMalformed  = errors.New("safetensors: malformed file")
	ErrLimit      = errors.New("safetensors: limit exceeded")
	ErrUnsupDType = errors.New("safetensors: unsupported dtype")
)

// DTypeBits is the element width of every dtype the format defines.
var DTypeBits = map[string]int{
	"BOOL": 8, "U8": 8, "I8": 8, "F8_E5M2": 8, "F8_E4M3": 8, "F8_E8M0": 8,
	"I16": 16, "U16": 16, "F16": 16, "BF16": 16,
	"I32": 32, "U32": 32, "F32": 32,
	"C64": 64, "F64": 64, "I64": 64, "U64": 64,
	"F4": 4, "F6_E2M3": 6, "F6_E3M2": 6,
}

// Options bound the work done on untrusted input.
type Options struct {
	MaxHeader         int64 // header bytes accepted (default 100,000,000, the reference limit)
	MaxTensors        int   // tensors accepted (default 1<<20)
	MaxDims           int   // dimensions per tensor (default 32)
	AllowUnknownDType bool  // keep tensors of unknown dtype as opaque byte ranges
}

func (o Options) norm() Options {
	if o.MaxHeader <= 0 {
		o.MaxHeader = 100_000_000
	}
	if o.MaxTensors <= 0 {
		o.MaxTensors = 1 << 20
	}
	if o.MaxDims <= 0 {
		o.MaxDims = 32
	}
	return o
}

// Tensor is one entry of the header. Begin/End are relative to File.DataStart.
type Tensor struct {
	Name       string
	DType      string
	Shape      []int64
	Begin, End int64
	Elems      int64
	Bits       int // element width; 0 for an unknown dtype
}

// Size is the payload length in bytes.
func (t *Tensor) Size() int64 { return t.End - t.Begin }

// File is the structure of one Safetensors file. Header holds the exact header
// bytes (including any padding), so File can be re-serialised byte for byte.
type File struct {
	Size      int64
	HeaderLen int64
	Header    []byte
	DataStart int64 // 8 + HeaderLen
	DataLen   int64
	PadLen    int // trailing JSON whitespace bytes in the header
	Metadata  map[string]string
	MetaKeys  []string // __metadata__ keys in header order
	Tensors   []Tensor // sorted by data offset (file order)
	JSONOrder []string // tensor names in header (JSON) order
	byName    map[string]int
}

// Lookup returns the tensor named name.
func (f *File) Lookup(name string) (*Tensor, bool) {
	i, ok := f.byName[name]
	if !ok {
		return nil, false
	}
	return &f.Tensors[i], true
}

// Parse reads the prefix and header of a Safetensors file of the given size.
func Parse(r io.ReaderAt, size int64, opt Options) (*File, error) {
	opt = opt.norm()
	if size < 8 {
		return nil, fmt.Errorf("%w: %d bytes is shorter than the length prefix", ErrMalformed, size)
	}
	var pre [8]byte
	if _, err := r.ReadAt(pre[:], 0); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint64(pre[:])
	if n > uint64(opt.MaxHeader) {
		return nil, fmt.Errorf("%w: header of %d bytes (limit %d)", ErrLimit, n, opt.MaxHeader)
	}
	if n > uint64(size-8) {
		return nil, fmt.Errorf("%w: header length %d exceeds the %d byte file", ErrMalformed, n, size)
	}
	if n < 2 {
		return nil, fmt.Errorf("%w: header of %d bytes cannot hold a JSON object", ErrMalformed, n)
	}
	hdr := make([]byte, n)
	if _, err := r.ReadAt(hdr, 8); err != nil && !(err == io.EOF && n == uint64(len(hdr))) {
		return nil, err
	}
	f := &File{Size: size, HeaderLen: int64(n), Header: hdr, DataStart: 8 + int64(n)}
	f.DataLen = size - f.DataStart
	if err := f.parseHeader(opt); err != nil {
		return nil, err
	}
	return f, nil
}

// ParseBytes parses an in-memory file.
func ParseBytes(b []byte, opt Options) (*File, error) {
	return Parse(bytes.NewReader(b), int64(len(b)), opt)
}

func isJSONSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func (f *File) parseHeader(opt Options) error {
	hdr := f.Header
	if hdr[0] != '{' {
		return fmt.Errorf("%w: header does not start with '{'", ErrMalformed)
	}
	for i := len(hdr) - 1; i >= 0 && isJSONSpace(hdr[i]); i-- {
		f.PadLen++
	}
	dec := json.NewDecoder(bytes.NewReader(hdr))
	dec.UseNumber()
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return fmt.Errorf("%w: header is not a JSON object", ErrMalformed)
	}
	f.byName = map[string]int{}
	seen := map[string]bool{}
	var tensors []Tensor
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		key, ok := kt.(string)
		if !ok {
			return fmt.Errorf("%w: non-string key", ErrMalformed)
		}
		if seen[key] {
			return fmt.Errorf("%w: duplicate key %q", ErrMalformed, key)
		}
		seen[key] = true
		if key == "__metadata__" {
			if err := f.parseMetadata(dec, opt); err != nil {
				return err
			}
			continue
		}
		if len(tensors) >= opt.MaxTensors {
			return fmt.Errorf("%w: more than %d tensors", ErrLimit, opt.MaxTensors)
		}
		t, err := parseTensor(dec, key, opt)
		if err != nil {
			return err
		}
		tensors = append(tensors, t)
		f.JSONOrder = append(f.JSONOrder, key)
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return fmt.Errorf("%w: header object not closed", ErrMalformed)
	}
	if int(dec.InputOffset()) > len(hdr) {
		return fmt.Errorf("%w: header offset", ErrMalformed)
	}
	for _, c := range hdr[dec.InputOffset():] {
		if !isJSONSpace(c) {
			return fmt.Errorf("%w: data after the header object", ErrMalformed)
		}
	}
	return f.validate(tensors, opt)
}

func (f *File) parseMetadata(dec *json.Decoder, opt Options) error {
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return fmt.Errorf("%w: __metadata__ is not an object", ErrMalformed)
	}
	f.Metadata = map[string]string{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		k, _ := kt.(string)
		vt, err := dec.Token()
		if err != nil {
			return fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		v, ok := vt.(string)
		if !ok {
			return fmt.Errorf("%w: __metadata__ value for %q is not a string", ErrMalformed, k)
		}
		if _, dup := f.Metadata[k]; dup {
			return fmt.Errorf("%w: duplicate __metadata__ key %q", ErrMalformed, k)
		}
		f.Metadata[k] = v
		f.MetaKeys = append(f.MetaKeys, k)
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') {
		return fmt.Errorf("%w: __metadata__ not closed", ErrMalformed)
	}
	return nil
}

func nonNeg(num json.Number) (int64, error) {
	s := string(num)
	if s == "" || len(s) > 19 {
		return 0, fmt.Errorf("%w: number %q", ErrMalformed, s)
	}
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%w: number %q is not a non-negative integer", ErrMalformed, s)
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: number %q", ErrMalformed, s)
	}
	return v, nil
}

func parseTensor(dec *json.Decoder, name string, opt Options) (Tensor, error) {
	t := Tensor{Name: name, Begin: -1}
	if tk, err := dec.Token(); err != nil || tk != json.Delim('{') {
		return t, fmt.Errorf("%w: tensor %q is not an object", ErrMalformed, name)
	}
	var haveD, haveS, haveO bool
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return t, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		switch k, _ := kt.(string); k {
		case "dtype":
			if haveD {
				return t, fmt.Errorf("%w: tensor %q repeats dtype", ErrMalformed, name)
			}
			haveD = true
			vt, err := dec.Token()
			s, ok := vt.(string)
			if err != nil || !ok {
				return t, fmt.Errorf("%w: tensor %q dtype is not a string", ErrMalformed, name)
			}
			t.DType = s
		case "shape":
			if haveS {
				return t, fmt.Errorf("%w: tensor %q repeats shape", ErrMalformed, name)
			}
			haveS = true
			if tk, err := dec.Token(); err != nil || tk != json.Delim('[') {
				return t, fmt.Errorf("%w: tensor %q shape is not an array", ErrMalformed, name)
			}
			t.Shape = []int64{}
			for dec.More() {
				if len(t.Shape) >= opt.MaxDims {
					return t, fmt.Errorf("%w: tensor %q has more than %d dimensions", ErrLimit, name, opt.MaxDims)
				}
				vt, err := dec.Token()
				num, ok := vt.(json.Number)
				if err != nil || !ok {
					return t, fmt.Errorf("%w: tensor %q shape entry", ErrMalformed, name)
				}
				d, err := nonNeg(num)
				if err != nil {
					return t, err
				}
				t.Shape = append(t.Shape, d)
			}
			if tk, err := dec.Token(); err != nil || tk != json.Delim(']') {
				return t, fmt.Errorf("%w: tensor %q shape not closed", ErrMalformed, name)
			}
		case "data_offsets":
			if haveO {
				return t, fmt.Errorf("%w: tensor %q repeats data_offsets", ErrMalformed, name)
			}
			haveO = true
			if tk, err := dec.Token(); err != nil || tk != json.Delim('[') {
				return t, fmt.Errorf("%w: tensor %q data_offsets is not an array", ErrMalformed, name)
			}
			var o [2]int64
			for i := 0; i < 2; i++ {
				vt, err := dec.Token()
				num, ok := vt.(json.Number)
				if err != nil || !ok {
					return t, fmt.Errorf("%w: tensor %q data_offsets must hold two integers", ErrMalformed, name)
				}
				if o[i], err = nonNeg(num); err != nil {
					return t, err
				}
			}
			if tk, err := dec.Token(); err != nil || tk != json.Delim(']') {
				return t, fmt.Errorf("%w: tensor %q data_offsets must hold exactly two integers", ErrMalformed, name)
			}
			t.Begin, t.End = o[0], o[1]
		default:
			return t, fmt.Errorf("%w: tensor %q has unexpected field %q", ErrMalformed, name, k)
		}
	}
	if tk, err := dec.Token(); err != nil || tk != json.Delim('}') {
		return t, fmt.Errorf("%w: tensor %q not closed", ErrMalformed, name)
	}
	if !haveD || !haveS || !haveO {
		return t, fmt.Errorf("%w: tensor %q lacks dtype, shape or data_offsets", ErrMalformed, name)
	}
	return t, nil
}

// elems multiplies a shape with overflow detection.
func elems(shape []int64) (int64, bool) {
	n := uint64(1)
	for _, d := range shape {
		hi, lo := bits.Mul64(n, uint64(d))
		if hi != 0 || lo > 1<<62 {
			return 0, false
		}
		n = lo
	}
	return int64(n), true
}

func (f *File) validate(ts []Tensor, opt Options) error {
	for i := range ts {
		t := &ts[i]
		if t.Begin > t.End {
			return fmt.Errorf("%w: tensor %q has inverted offsets [%d,%d]", ErrMalformed, t.Name, t.Begin, t.End)
		}
		if t.End > f.DataLen {
			return fmt.Errorf("%w: tensor %q ends at %d beyond the %d data bytes", ErrMalformed, t.Name, t.End, f.DataLen)
		}
		n, ok := elems(t.Shape)
		if !ok {
			return fmt.Errorf("%w: tensor %q shape overflows", ErrMalformed, t.Name)
		}
		t.Elems = n
		b, known := DTypeBits[t.DType]
		switch {
		case known:
			t.Bits = b
			hi, lo := bits.Mul64(uint64(n), uint64(b))
			if hi != 0 || lo%8 != 0 || int64(lo/8) != t.End-t.Begin {
				return fmt.Errorf("%w: tensor %q: %d elements of %s need %d bits, data_offsets give %d bytes", ErrMalformed, t.Name, n, t.DType, lo, t.End-t.Begin)
			}
		case opt.AllowUnknownDType:
		default:
			return fmt.Errorf("%w: tensor %q has dtype %q", ErrUnsupDType, t.Name, t.DType)
		}
	}
	sort.SliceStable(ts, func(i, j int) bool {
		if ts[i].Begin != ts[j].Begin {
			return ts[i].Begin < ts[j].Begin
		}
		if ts[i].End != ts[j].End {
			return ts[i].End < ts[j].End
		}
		return ts[i].Name < ts[j].Name
	})
	var next int64
	for i := range ts {
		if ts[i].Begin != next {
			if ts[i].Begin < next {
				return fmt.Errorf("%w: tensor %q [%d,%d) overlaps the previous tensor (ends at %d)", ErrMalformed, ts[i].Name, ts[i].Begin, ts[i].End, next)
			}
			return fmt.Errorf("%w: hole of %d bytes before tensor %q", ErrMalformed, ts[i].Begin-next, ts[i].Name)
		}
		next = ts[i].End
	}
	if next != f.DataLen {
		return fmt.Errorf("%w: tensors cover %d of %d data bytes", ErrMalformed, next, f.DataLen)
	}
	f.Tensors = ts
	for i := range ts {
		f.byName[ts[i].Name] = i
	}
	return nil
}

// ReadTensor reads n bytes of t starting at offset off within the tensor.
func (f *File) ReadTensor(r io.ReaderAt, t *Tensor, off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || off > t.Size() || n > t.Size()-off {
		return nil, fmt.Errorf("%w: read [%d,%d) outside tensor %q of %d bytes", ErrMalformed, off, off+n, t.Name, t.Size())
	}
	b := make([]byte, n)
	if _, err := r.ReadAt(b, f.DataStart+t.Begin+off); err != nil && !(err == io.EOF && int64(len(b)) == n) {
		return nil, err
	}
	return b, nil
}

// Segment is a contiguous byte range of the file.
type Segment struct {
	Kind     string // "prefix" | "header" | "tensor"
	Name     string
	Off, Len int64
}

// Segments partitions the file exactly: prefix, header, then every tensor in file order.
func (f *File) Segments() []Segment {
	segs := []Segment{{"prefix", "", 0, 8}, {"header", "", 8, f.HeaderLen}}
	for i := range f.Tensors {
		t := &f.Tensors[i]
		segs = append(segs, Segment{"tensor", t.Name, f.DataStart + t.Begin, t.Size()})
	}
	return segs
}

// Rebuild writes the file back from its structure: the exact header bytes and
// the tensor payloads read from src. The result equals the original bytes.
func (f *File) Rebuild(w io.Writer, src io.ReaderAt) error {
	var pre [8]byte
	binary.LittleEndian.PutUint64(pre[:], uint64(f.HeaderLen))
	if _, err := w.Write(pre[:]); err != nil {
		return err
	}
	if _, err := w.Write(f.Header); err != nil {
		return err
	}
	for i := range f.Tensors {
		t := &f.Tensors[i]
		if _, err := io.Copy(w, io.NewSectionReader(src, f.DataStart+t.Begin, t.Size())); err != nil {
			return err
		}
	}
	return nil
}

// CountingReaderAt measures the I/O a parse or a range read really performs.
type CountingReaderAt struct {
	R     io.ReaderAt
	Reads int
	Bytes int64
}

func (c *CountingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.Reads++
	n, err := c.R.ReadAt(p, off)
	c.Bytes += int64(n)
	return n, err
}
