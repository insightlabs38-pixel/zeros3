package semcore

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"

	"zeros3-z3/ggufz3"
	"zeros3-z3/st"
	"zeros3-z3/torchz3"
)

// TInfo describes a payload span as the tensor it stores. DType/Shape describe the byte
// array exactly as laid out in the file (C order over Shape); Name and Hint are only
// search hints, never identity.
type TInfo struct {
	Name  string
	DType int
	Shape []uint64
	// Heads lists plausible attention head counts for layout hypotheses (hint only).
	Heads []int
}

// Seg is one tile of a source file: a literal region or a tensor payload span.
type Seg struct {
	Off, Len int64
	T        *TInfo // nil => literal
}

// Doc is a file tiled exactly into literals and tensor spans.
type Doc struct {
	Format string
	Name   string
	// Data holds the whole file for in-memory documents. Streaming documents leave it nil
	// and are read through R (never more than one segment at a time).
	Data []byte
	R    io.ReaderAt
	Size int64
	Segs []Seg
	Meta map[string]string
}

// Len is the file length.
func (d *Doc) Len() int64 {
	if d.Data != nil || d.R == nil {
		return int64(len(d.Data))
	}
	return d.Size
}

// Source returns the random-access view of the file.
func (d *Doc) Source() io.ReaderAt {
	if d.R != nil {
		return d.R
	}
	return bytes.NewReader(d.Data)
}

// ReadSeg reads one segment's bytes into buf (grown as needed) and returns the filled slice.
// An in-memory document returns a view of Data without copying.
func (d *Doc) ReadSeg(s Seg, buf []byte) ([]byte, error) {
	if d.Data != nil || d.R == nil {
		return d.Data[s.Off : s.Off+s.Len], nil
	}
	if s.Len < 0 || s.Len > MaxOutput {
		return nil, ErrCorrupt
	}
	if int64(cap(buf)) < s.Len {
		buf = make([]byte, s.Len)
	}
	buf = buf[:s.Len]
	if n, err := d.R.ReadAt(buf, s.Off); int64(n) < s.Len {
		if err == nil || err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return buf, nil
}

// Check verifies that the segments tile the file exactly.
func (d *Doc) Check() error {
	pos := int64(0)
	for _, s := range d.Segs {
		if s.Off != pos || s.Len < 0 {
			return fmt.Errorf("semcore: %s segments do not tile at %d", d.Format, pos)
		}
		pos += s.Len
	}
	if pos != d.Len() {
		return fmt.Errorf("semcore: %s segments cover %d of %d", d.Format, pos, d.Len())
	}
	return nil
}

func tile(size int64, spans []Seg) []Seg {
	sort.Slice(spans, func(i, j int) bool { return spans[i].Off < spans[j].Off })
	var out []Seg
	pos := int64(0)
	for _, s := range spans {
		if s.Len == 0 {
			continue
		}
		if s.Off > pos {
			out = append(out, Seg{Off: pos, Len: s.Off - pos})
		}
		out = append(out, s)
		pos = s.Off + s.Len
	}
	if pos < size {
		out = append(out, Seg{Off: pos, Len: size - pos})
	}
	return out
}

var stDType = map[string]int{"BOOL": DBool, "U8": DU8, "I8": DI8, "U16": DU16, "I16": DI16, "F16": DF16, "BF16": DBF16,
	"U32": DU32, "I32": DI32, "F32": DF32, "U64": DU64, "I64": DI64, "F64": DF64}

// ParseSafetensors tiles a Safetensors file held in memory. Unsupported dtypes stay literal.
func ParseSafetensors(name string, data []byte) (*Doc, error) {
	d, err := ParseSafetensorsAt(name, bytes.NewReader(data), int64(len(data)))
	if d != nil {
		d.Data = data
	}
	return d, err
}

// ParseSafetensorsAt reads only the header of a Safetensors file.
func ParseSafetensorsAt(name string, r io.ReaderAt, size int64) (*Doc, error) {
	f, err := st.Parse(r, size, st.Options{AllowUnknownDType: true})
	if err != nil {
		return nil, err
	}
	d := &Doc{Format: "safetensors", Name: name, R: r, Size: size, Meta: f.Metadata}
	var spans []Seg
	for i := range f.Tensors {
		t := &f.Tensors[i]
		dt, ok := stDType[t.DType]
		if !ok || t.Size() == 0 {
			continue
		}
		shape := make([]uint64, len(t.Shape))
		for j, x := range t.Shape {
			shape[j] = uint64(x)
		}
		spans = append(spans, Seg{Off: f.DataStart + t.Begin, Len: t.Size(), T: &TInfo{Name: t.Name, DType: dt, Shape: shape}})
	}
	d.Segs = tile(size, spans)
	return d, d.Check()
}

func ggufDType(t uint32) (int, bool) {
	switch t {
	case 0:
		return DF32, true
	case 1:
		return DF16, true
	case 24:
		return DI8, true
	case 25:
		return DI16, true
	case 26:
		return DI32, true
	case 27:
		return DI64, true
	case 28:
		return DF64, true
	case 30:
		return DBF16, true
	}
	if _, ok := ggufz3.Geom[t]; ok {
		return DQBase + int(t), true
	}
	return 0, false
}

// ParseGGUF tiles a little-endian GGUF file held in memory; GGUF dims are reversed to row-major shape.
func ParseGGUF(name string, data []byte) (*Doc, error) {
	d, err := ParseGGUFAt(name, bytes.NewReader(data), int64(len(data)))
	if d != nil {
		d.Data = data
	}
	return d, err
}

// ParseGGUFAt reads only the GGUF header and tensor table.
func ParseGGUFAt(name string, r io.ReaderAt, size int64) (*Doc, error) {
	f, err := ggufz3.Parse(r, size, ggufz3.Limits{})
	if err != nil {
		return nil, err
	}
	d := &Doc{Format: "gguf", Name: name, R: r, Size: size, Meta: map[string]string{}}
	var heads []int
	for _, kv := range f.KVs {
		if strings.HasSuffix(kv.Key, "attention.head_count") || strings.HasSuffix(kv.Key, "attention.head_count_kv") {
			heads = append(heads, int(kv.Uint))
		}
	}
	var spans []Seg
	for i := range f.Tensors {
		t := &f.Tensors[i]
		dt, ok := ggufDType(t.Type)
		if !ok || t.Size() == 0 {
			continue
		}
		shape := make([]uint64, len(t.Dims))
		for j, x := range t.Dims {
			shape[len(t.Dims)-1-j] = x
		}
		spans = append(spans, Seg{Off: t.Begin, Len: t.Size(), T: &TInfo{Name: t.Name, DType: dt, Shape: shape, Heads: heads}})
	}
	d.Segs = tile(size, spans)
	return d, d.Check()
}

// ParseTorch tiles a torch.save ZIP held in memory.
func ParseTorch(name string, data []byte) (*Doc, error) {
	d, err := ParseTorchAt(name, bytes.NewReader(data), int64(len(data)))
	if d != nil {
		d.Data = data
	}
	return d, err
}

// maxPickle bounds the pickle member a streaming parse will read.
const maxPickle = 64 << 20

// ParseTorchAt tiles a torch.save ZIP and, using a symbolic (non-executing) pickle reader,
// types every storage member that backs exactly one whole tensor. Only the ZIP directory
// and the pickle member are read.
func ParseTorchAt(name string, r io.ReaderAt, size int64) (*Doc, error) {
	f, err := torchz3.Parse(r, size, torchz3.Limits{})
	if err != nil {
		return nil, err
	}
	d := &Doc{Format: "pytorch", Name: name, R: r, Size: size}
	var pkl []byte
	for i := range f.Members {
		m := &f.Members[i]
		if strings.HasSuffix(m.Name, "/data.pkl") || m.Name == "data.pkl" {
			if n := m.DataEnd - m.DataBegin; n < 0 || n > maxPickle {
				return nil, ErrCorrupt
			}
			pkl = make([]byte, m.DataEnd-m.DataBegin)
			if k, err := r.ReadAt(pkl, m.DataBegin); k < len(pkl) {
				if err == nil {
					err = io.ErrUnexpectedEOF
				}
				return nil, err
			}
		}
	}
	tensors, _ := ScanTorchPickle(pkl) // failure leaves storages untyped
	byKey := map[string][]TorchTensor{}
	for _, t := range tensors {
		byKey[t.StorageKey] = append(byKey[t.StorageKey], t)
	}
	var spans []Seg
	for i := range f.Members {
		m := &f.Members[i]
		if !strings.Contains(m.Name, "/data/") || m.Size() == 0 {
			continue
		}
		key := m.Name[strings.LastIndex(m.Name, "/")+1:]
		ts := byKey[key]
		if len(ts) != 1 {
			continue
		}
		t := ts[0]
		w := ElemBytes(t.DType)
		if w == 0 || t.Offset != 0 || int64(t.Numel)*int64(w) != m.Size() {
			continue
		}
		spans = append(spans, Seg{Off: m.DataBegin, Len: m.Size(), T: &TInfo{Name: t.Name, DType: t.DType, Shape: storageShape(t)}})
	}
	d.Segs = tile(size, spans)
	return d, d.Check()
}

// storageShape returns the logical shape reordered into storage (stride-descending) order
// when the strides are a permutation of a dense layout, else the logical shape.
func storageShape(t TorchTensor) []uint64 {
	n := len(t.Shape)
	if len(t.Stride) != n || n < 2 {
		return t.Shape
	}
	ord := make([]int, n)
	for i := range ord {
		ord[i] = i
	}
	sort.SliceStable(ord, func(a, b int) bool { return t.Stride[ord[a]] > t.Stride[ord[b]] })
	out := make([]uint64, n)
	exp := uint64(1)
	for i := n - 1; i >= 0; i-- {
		ax := ord[i]
		if uint64(t.Stride[ax]) != exp && t.Shape[ax] != 1 {
			return t.Shape
		}
		out[i] = t.Shape[ax]
		exp *= t.Shape[ax]
	}
	return out
}

// Detect picks a parser by magic bytes and name for an in-memory file.
func Detect(name string, data []byte) (*Doc, error) {
	d, err := DetectAt(name, bytes.NewReader(data), int64(len(data)))
	if d != nil {
		d.Data = data
	}
	return d, err
}

// DetectAt picks a parser by magic bytes and name, reading only the file's first bytes and
// the format's own metadata regions.
func DetectAt(name string, r io.ReaderAt, size int64) (*Doc, error) {
	var head [16]byte
	n, _ := r.ReadAt(head[:], 0)
	h := head[:n]
	switch {
	case len(h) >= 4 && string(h[:4]) == "GGUF":
		return ParseGGUFAt(name, r, size)
	case len(h) >= 4 && string(h[:2]) == "PK":
		return ParseTorchAt(name, r, size)
	case strings.HasSuffix(name, ".onnx"):
		return ParseONNXAt(name, r, size)
	case size > 9 && len(h) > 9 && h[8] == '{':
		return ParseSafetensorsAt(name, r, size)
	}
	return nil, fmt.Errorf("semcore: unrecognized format for %s", name)
}
