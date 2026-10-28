// Package onnxz3 extracts bounded dense raw initializer regions, without generic
// protobuf normalization, graph rewriting, or external tensor-file resolution.
package onnxz3

import (
	"encoding/binary"
	"fmt"
	"unicode/utf8"
)

const MaxSource = 128 << 20
const MaxTensor = 16 << 20
const MaxFields = 1000000
const MaxTensors = 65536

var ErrUnsupported = fmt.Errorf("unsupported or invalid ONNX initializer framing")

type Tensor struct {
	Name     string
	Type     int
	Dims     []uint64
	Off, Len int64
	Width    int
}
type Plan struct {
	Tensors          []Tensor
	External, Opaque int
}
type field struct {
	number, wire uint64
	value        uint64
	off, end     int
}
type parser struct {
	data   []byte
	fields int
}

func (p *parser) varint(pos *int, end int) (uint64, error) {
	var v uint64
	for shift := uint(0); shift < 70; shift += 7 {
		if *pos >= end {
			return 0, ErrUnsupported
		}
		b := p.data[*pos]
		*pos++
		if shift == 63 && b > 1 {
			return 0, ErrUnsupported
		}
		v |= uint64(b&127) << shift
		if b < 128 {
			return v, nil
		}
	}
	return 0, ErrUnsupported
}
func (p *parser) fieldsIn(start, end int, visit func(field) error) error {
	for pos := start; pos < end; {
		p.fields++
		if p.fields > MaxFields {
			return ErrUnsupported
		}
		key, e := p.varint(&pos, end)
		if e != nil || key>>3 == 0 || key>>3 > 0x1fffffff {
			return ErrUnsupported
		}
		f := field{number: key >> 3, wire: key & 7}
		switch f.wire {
		case 0:
			f.value, e = p.varint(&pos, end)
		case 1:
			if end-pos < 8 {
				return ErrUnsupported
			}
			f.value = binary.LittleEndian.Uint64(p.data[pos:])
			pos += 8
		case 2:
			n, err := p.varint(&pos, end)
			if err != nil || n > uint64(end-pos) {
				return ErrUnsupported
			}
			f.off = pos
			pos += int(n)
			f.end = pos
		case 5:
			if end-pos < 4 {
				return ErrUnsupported
			}
			f.value = uint64(binary.LittleEndian.Uint32(p.data[pos:]))
			pos += 4
		default:
			return ErrUnsupported
		}
		if e != nil {
			return e
		}
		if e = visit(f); e != nil {
			return e
		}
	}
	return nil
}
func width(dtype int) int {
	switch dtype {
	case 1, 6, 12:
		return 4
	case 2, 3, 9, 17, 18, 19, 20:
		return 1
	case 4, 5, 10, 16:
		return 2
	case 7, 11, 13, 14:
		return 8
	case 15:
		return 16
	}
	return 0
}
func (p *parser) tensor(start, end int) (Tensor, bool, bool, error) {
	t := Tensor{}
	raw := -1
	rawEnd := 0
	external := false
	typed := false
	seen := map[uint64]bool{}
	e := p.fieldsIn(start, end, func(f field) error {
		switch f.number {
		case 1:
			if len(t.Dims) >= 32 {
				return ErrUnsupported
			}
			if f.wire == 0 {
				t.Dims = append(t.Dims, f.value)
			} else if f.wire == 2 {
				for pos := f.off; pos < f.end; {
					v, e := p.varint(&pos, f.end)
					if e != nil || len(t.Dims) >= 32 {
						return ErrUnsupported
					}
					t.Dims = append(t.Dims, v)
				}
			} else {
				return ErrUnsupported
			}
		case 2:
			if f.wire != 0 || f.value > 256 || seen[2] {
				return ErrUnsupported
			}
			t.Type = int(f.value)
			seen[2] = true
		case 8:
			if f.wire != 2 || f.end-f.off > 4096 || seen[8] || !utf8.Valid(p.data[f.off:f.end]) {
				return ErrUnsupported
			}
			t.Name = string(p.data[f.off:f.end])
			seen[8] = true
		case 9:
			if f.wire != 2 || seen[9] {
				return ErrUnsupported
			}
			raw, rawEnd = f.off, f.end
			seen[9] = true
		case 13:
			if f.wire != 2 {
				return ErrUnsupported
			}
			external = true
		case 14:
			if f.wire != 0 || f.value > 1 || seen[14] {
				return ErrUnsupported
			}
			external = external || f.value == 1
			seen[14] = true
		case 4, 5, 6, 7, 10, 11:
			typed = true
		}
		return nil
	})
	if e != nil {
		return t, false, false, e
	}
	if external {
		if raw >= 0 {
			return t, false, false, ErrUnsupported
		}
		return t, false, true, nil
	}
	t.Width = width(t.Type)
	if raw < 0 || t.Width == 0 || typed || t.Name == "" {
		return t, false, false, nil
	}
	elements := uint64(1)
	for _, d := range t.Dims {
		if d > MaxTensor || d != 0 && elements > MaxTensor/d {
			return t, false, false, ErrUnsupported
		}
		elements *= d
	}
	if elements > MaxTensor/uint64(t.Width) || uint64(rawEnd-raw) != elements*uint64(t.Width) {
		return t, false, false, ErrUnsupported
	}
	t.Off, t.Len = int64(raw), int64(rawEnd-raw)
	if t.Len == 0 {
		return t, false, false, nil
	}
	return t, true, false, nil
}

// Parse inspects only Model.graph and Graph.initializer. Other fields, sparse
// initializers, nested attributes and typed/external tensors remain opaque bytes.
func Parse(data []byte) (*Plan, error) {
	if len(data) == 0 || len(data) > MaxSource {
		return nil, ErrUnsupported
	}
	p := parser{data: data}
	plan := &Plan{}
	graphs := 0
	names := map[string]bool{}
	tensors := 0
	e := p.fieldsIn(0, len(data), func(f field) error {
		if f.number != 7 {
			return nil
		}
		graphs++
		if graphs != 1 || f.wire != 2 {
			return ErrUnsupported
		}
		return p.fieldsIn(f.off, f.end, func(f field) error {
			if f.number != 5 {
				return nil
			}
			if f.wire != 2 {
				return ErrUnsupported
			}
			tensors++
			if tensors > MaxTensors {
				return ErrUnsupported
			}
			t, ok, external, e := p.tensor(f.off, f.end)
			if e != nil {
				return e
			}
			if t.Name != "" {
				if names[t.Name] {
					return ErrUnsupported
				}
				names[t.Name] = true
			}
			if external {
				plan.External++
			} else if ok {
				plan.Tensors = append(plan.Tensors, t)
			} else {
				plan.Opaque++
			}
			return nil
		})
	})
	if e != nil {
		return nil, e
	}
	if graphs != 1 {
		return nil, ErrUnsupported
	}
	return plan, nil
}
