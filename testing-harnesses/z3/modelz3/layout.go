package modelz3

import (
	"fmt"
	"io"

	"zeros3-z3/ggufz3"
	"zeros3-z3/st"
)

// Seg is one exact piece of a model file in file order: framing (meta, gap, tail) or a tensor
// payload. Width is the unit a plane transform separates: the element width of a Safetensors
// tensor, or the quantisation block size of a GGUF tensor (1 for unknown / opaque).
type Seg struct {
	Begin, End int64
	Kind       string // "meta" | "gap" | "tail" | "tensor"
	Name       string
	Type       string // dtype (Safetensors) or ggml type name (GGUF)
	Shape      []int64
	Width      int
	TypeID     uint32 // GGUF ggml_type
}

func (s *Seg) Size() int64 { return s.End - s.Begin }

// Layout tiles [0,Size) exactly.
type Layout struct {
	Format string
	Size   int64
	Segs   []Seg
}

// Tensors returns the tensor segments in file order.
func (l *Layout) Tensors() []*Seg {
	var out []*Seg
	for i := range l.Segs {
		if l.Segs[i].Kind == "tensor" {
			out = append(out, &l.Segs[i])
		}
	}
	return out
}

// FramingBytes is everything that is not a tensor payload.
func (l *Layout) FramingBytes() int64 {
	var n int64
	for _, s := range l.Segs {
		if s.Kind != "tensor" {
			n += s.Size()
		}
	}
	return n
}

// Check verifies the tiling invariant.
func (l *Layout) Check() error {
	var n int64
	for _, s := range l.Segs {
		if s.Begin != n || s.End < s.Begin {
			return fmt.Errorf("modelz3: layout gap at %d", n)
		}
		n = s.End
	}
	if n != l.Size {
		return fmt.Errorf("modelz3: layout ends at %d of %d", n, l.Size)
	}
	return nil
}

// GGUFLayout builds the layout from the strict GGUF parser (one "meta" segment: header, KV
// section, tensor directory and alignment padding).
func GGUFLayout(r io.ReaderAt, size int64) (*Layout, error) {
	f, err := ggufz3.Parse(r, size, ggufz3.Limits{})
	if err != nil {
		return nil, err
	}
	l := &Layout{Format: "gguf", Size: size}
	for _, s := range f.Segments() {
		seg := Seg{Begin: s.Begin, End: s.End, Kind: s.Kind, Width: 1}
		if s.Kind == "header" {
			seg.Kind = "meta"
		}
		if s.Kind == "tensor" {
			t := &f.Tensors[s.Tensor]
			g := ggufz3.Geom[t.Type]
			seg.Name, seg.Type, seg.TypeID, seg.Width = t.Name, g.Name, t.Type, int(g.Bytes)
			for _, d := range t.Dims {
				seg.Shape = append(seg.Shape, int64(d))
			}
		}
		l.Segs = append(l.Segs, seg)
	}
	return l, l.Check()
}

// STLayout builds the layout from the Z3-09 strict Safetensors parser.
func STLayout(r io.ReaderAt, size int64) (*Layout, *st.File, error) {
	f, err := st.Parse(r, size, st.Options{AllowUnknownDType: true})
	if err != nil {
		return nil, nil, err
	}
	l := &Layout{Format: "safetensors", Size: size}
	for _, s := range f.Segments() {
		seg := Seg{Begin: s.Off, End: s.Off + s.Len, Kind: "meta", Width: 1}
		if s.Kind == "tensor" {
			t, _ := f.Lookup(s.Name)
			seg.Kind, seg.Name, seg.Type, seg.Shape = "tensor", t.Name, t.DType, t.Shape
			seg.Width = max(1, t.Bits/8)
		}
		l.Segs = append(l.Segs, seg)
	}
	return l, f, l.Check()
}
