// Package mdelta2 is the Z4-B5 laboratory for exact model-family storage: tensor discovery,
// numerical predictors with exact residuals, residual coders and a self-describing container.
// Research code only; production is untouched.
package mdelta2

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"zeros3-z3/ggufz3"
	"zeros3-z3/st"
)

// DType is the element encoding of a tensor payload.
// Tensor is one named payload span of a model file.
type Tensor struct {
	Name       string
	DType      DType
	Shape      []int64
	Begin, End int64 // absolute byte span in the file
	GGMLType   uint32
}

func (t *Tensor) Size() int64 { return t.End - t.Begin }
func (t *Tensor) Elems() int64 {
	n := int64(1)
	for _, s := range t.Shape {
		n *= s
	}
	return n
}

// Rows and Cols view the tensor as a matrix (last dimension = Cols); 1-D tensors are 1 row.
func (t *Tensor) RowsCols() (int64, int64) {
	if len(t.Shape) == 0 {
		return 1, 1
	}
	c := t.Shape[len(t.Shape)-1]
	if c <= 0 {
		return 1, 1
	}
	return t.Elems() / c, c
}

// Model is an opened Safetensors or GGUF file with on-demand tensor reads.
type Model struct {
	Path    string
	Name    string
	Size    int64
	Format  string // "st" or "gguf"
	Header  []byte // exact bytes before the tensor data area
	Tensors []Tensor
	f       *os.File
	byName  map[string]int
	// GGUF only: the data area start and the bytes between tensors.
	DataStart int64
}

func (m *Model) Close() error { return m.f.Close() }

// Index returns the tensor position by name.
func (m *Model) Index(name string) (int, bool) { i, ok := m.byName[name]; return i, ok }

func (m *Model) Lookup(name string) (*Tensor, bool) {
	i, ok := m.byName[name]
	if !ok {
		return nil, false
	}
	return &m.Tensors[i], true
}

// Read returns the payload of tensor i.
func (m *Model) Read(i int) ([]byte, error) {
	t := &m.Tensors[i]
	b := make([]byte, t.Size())
	if _, err := m.f.ReadAt(b, t.Begin); err != nil && !(err == io.EOF && int64(len(b)) == t.Size()) {
		return nil, err
	}
	return b, nil
}

func (m *Model) ReadAt(p []byte, off int64) (int, error) { return m.f.ReadAt(p, off) }

var stDT = map[string]DType{"F32": DF32, "F16": DF16, "BF16": DBF16}

// Open parses a Safetensors or GGUF file.
func Open(path string) (*Model, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, _ := f.Stat()
	m := &Model{Path: path, Name: path, Size: fi.Size(), f: f, byName: map[string]int{}}
	var magic [4]byte
	f.ReadAt(magic[:], 0)
	if string(magic[:]) == "GGUF" {
		g, err := ggufz3.Parse(f, m.Size, ggufz3.Limits{})
		if err != nil {
			f.Close()
			return nil, err
		}
		m.Format, m.DataStart = "gguf", g.DataStart
		m.Header = make([]byte, g.DataStart)
		f.ReadAt(m.Header, 0)
		for _, t := range g.Tensors {
			dt, ok := ggmlDT[t.Type]
			if !ok {
				dt = DQOther
				if geom, has := ggufz3.Geom[t.Type]; has && geom.Elems == 1 {
					dt = DRaw
				}
			}
			shape := make([]int64, len(t.Dims))
			for i, d := range t.Dims { // GGUF stores the fastest dimension first
				shape[len(t.Dims)-1-i] = int64(d)
			}
			m.Tensors = append(m.Tensors, Tensor{Name: t.Name, DType: dt, Shape: shape, Begin: t.Begin, End: t.End, GGMLType: t.Type})
		}
	} else {
		s, err := st.Parse(f, m.Size, st.Options{AllowUnknownDType: true})
		if err != nil {
			f.Close()
			return nil, err
		}
		m.Format, m.DataStart = "st", s.DataStart
		m.Header = s.Header[:0:0]
		m.Header = make([]byte, s.DataStart)
		f.ReadAt(m.Header, 0)
		for _, t := range s.Tensors {
			dt, ok := stDT[t.DType]
			if !ok {
				dt = DRaw
			}
			m.Tensors = append(m.Tensors, Tensor{Name: t.Name, DType: dt, Shape: append([]int64(nil), t.Shape...), Begin: s.DataStart + t.Begin, End: s.DataStart + t.End})
		}
	}
	sort.SliceStable(m.Tensors, func(i, j int) bool { return m.Tensors[i].Begin < m.Tensors[j].Begin }) // file order = container index order
	for i := range m.Tensors {
		m.byName[m.Tensors[i].Name] = i
	}
	if len(m.Tensors) == 0 {
		f.Close()
		return nil, errors.New("mdelta2: no tensors")
	}
	return m, nil
}

var ggmlDT = map[uint32]DType{0: DF32, 1: DF16, 30: DBF16, 2: DQ4_0, 3: DQ4_1, 6: DQ5_0, 7: DQ5_1, 8: DQ8_0, 12: DQ4_K, 13: DQ5_K, 14: DQ6_K}

// Gaps returns the file bytes outside tensor payloads (header, padding, tail), concatenated in
// file order, plus the spans so a decoder can interleave them. The file must be tiled by header,
// payloads in offset order and padding.
func (m *Model) Gaps() (gaps []byte, spans [][2]int64, err error) {
	order := make([]int, len(m.Tensors))
	for i := range order {
		order[i] = i
	}
	sortBy(order, func(a, b int) bool { return m.Tensors[a].Begin < m.Tensors[b].Begin })
	pos := int64(0)
	for _, i := range order {
		t := &m.Tensors[i]
		if t.Begin < pos {
			return nil, nil, fmt.Errorf("mdelta2: overlapping tensors")
		}
		if t.Begin > pos {
			spans = append(spans, [2]int64{pos, t.Begin})
		}
		pos = t.End
	}
	if pos < m.Size {
		spans = append(spans, [2]int64{pos, m.Size})
	}
	for _, s := range spans {
		b := make([]byte, s[1]-s[0])
		if _, err := m.f.ReadAt(b, s[0]); err != nil {
			return nil, nil, err
		}
		gaps = append(gaps, b...)
	}
	return gaps, spans, nil
}
