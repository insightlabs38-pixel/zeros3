package semcore

import (
	"bytes"
	"fmt"
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
	Data   []byte
	Segs   []Seg
	Meta   map[string]string
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
	if pos != int64(len(d.Data)) {
		return fmt.Errorf("semcore: %s segments cover %d of %d", d.Format, pos, len(d.Data))
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


// ParseSafetensors tiles a Safetensors file. Unsupported dtypes stay literal bytes.
func ParseSafetensors(name string, data []byte) (*Doc, error) {
	f, err := st.ParseBytes(data, st.Options{AllowUnknownDType: true})
	if err != nil {
		return nil, err
	}
	d := &Doc{Format: "safetensors", Name: name, Data: data, Meta: f.Metadata}
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
	d.Segs = tile(int64(len(data)), spans)
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

// ParseGGUF tiles a little-endian GGUF file; GGUF dims are reversed to row-major shape.
func ParseGGUF(name string, data []byte) (*Doc, error) {
	f, err := ggufz3.Parse(bytes.NewReader(data), int64(len(data)), ggufz3.Limits{})
	if err != nil {
		return nil, err
	}
	d := &Doc{Format: "gguf", Name: name, Data: data, Meta: map[string]string{}}
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
	d.Segs = tile(int64(len(data)), spans)
	return d, d.Check()
}

// ParseTorch tiles a torch.save ZIP and, using a symbolic (non-executing) pickle reader,
// types every storage member that backs exactly one whole tensor.
func ParseTorch(name string, data []byte) (*Doc, error) {
	f, err := torchz3.Parse(bytes.NewReader(data), int64(len(data)), torchz3.Limits{})
	if err != nil {
		return nil, err
	}
	d := &Doc{Format: "pytorch", Name: name, Data: data}
	var pkl []byte
	for i := range f.Members {
		m := &f.Members[i]
		if strings.HasSuffix(m.Name, "/data.pkl") || m.Name == "data.pkl" {
			pkl = data[m.DataBegin:m.DataEnd]
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
	d.Segs = tile(int64(len(data)), spans)
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

// Detect picks a parser by magic bytes and name.
func Detect(name string, data []byte) (*Doc, error) {
	switch {
	case len(data) >= 4 && string(data[:4]) == "GGUF":
		return ParseGGUF(name, data)
	case len(data) >= 4 && string(data[:2]) == "PK":
		return ParseTorch(name, data)
	case strings.HasSuffix(name, ".onnx"):
		return ParseONNX(name, data)
	case len(data) > 9 && data[8] == '{':
		return ParseSafetensors(name, data)
	}
	return nil, fmt.Errorf("semcore: unrecognized format for %s", name)
}
