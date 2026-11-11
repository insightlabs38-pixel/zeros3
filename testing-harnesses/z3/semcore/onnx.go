package semcore

import (
	"bytes"
	"errors"
	"io"
)

// ONNX TensorProto data_type -> semcas dtype.
var onnxDType = map[uint64]int{1: DF32, 2: DU8, 3: DI8, 4: DU16, 5: DI16, 6: DI32, 7: DI64, 9: DBool,
	10: DF16, 11: DF64, 12: DU32, 13: DU64, 16: DBF16}

var errProto = errors.New("semcore: malformed protobuf")

type pbField struct {
	num, wire uint64
	val       uint64 // varint value
	off, end  int64  // payload span for length-delimited / fixed
}

// pbReader is a bounded window over a ReaderAt: tags and lengths are read through a small
// buffer, payloads are never read unless the visitor asks for a small one via Small.
type pbReader struct {
	r         io.ReaderAt
	buf       [4096]byte
	base, n   int64 // buffer covers [base, base+n)
	readErr   error
	BytesRead int64
}

// pbSmall bounds a payload a visitor may materialize (names, packed dims).
const pbSmall = 1 << 16

func (p *pbReader) fill(pos, end int64) {
	n := int64(len(p.buf))
	if end-pos < n {
		n = end - pos
	}
	k, err := p.r.ReadAt(p.buf[:n], pos)
	p.BytesRead += int64(k)
	p.base, p.n = pos, int64(k)
	if int64(k) < n && err != nil {
		p.readErr = err
	}
}

func (p *pbReader) byteAt(pos, end int64) (byte, bool) {
	if pos < p.base || pos >= p.base+p.n {
		p.fill(pos, end)
		if p.n == 0 {
			return 0, false
		}
	}
	return p.buf[pos-p.base], true
}

func (p *pbReader) varint(pos *int64, end int64) (uint64, bool) {
	var v uint64
	for s := uint(0); s < 70; s += 7 {
		if *pos >= end {
			return 0, false
		}
		c, ok := p.byteAt(*pos, end)
		if !ok {
			return 0, false
		}
		*pos++
		if s == 63 && c > 1 {
			return 0, false
		}
		v |= uint64(c&127) << s
		if c < 128 {
			return v, true
		}
	}
	return 0, false
}

// Small returns the payload [off,end) when it is at most pbSmall bytes.
func (p *pbReader) Small(off, end int64) ([]byte, bool) {
	if end < off || end-off > pbSmall {
		return nil, false
	}
	b := make([]byte, end-off)
	k, err := p.r.ReadAt(b, off)
	p.BytesRead += int64(k)
	if k < len(b) {
		_ = err
		return nil, false
	}
	return b, true
}

func (p *pbReader) fields(start, end int64, visit func(pbField) error) error {
	for pos := start; pos < end; {
		tag, ok := p.varint(&pos, end)
		if !ok || tag>>3 == 0 {
			return errProto
		}
		f := pbField{num: tag >> 3, wire: tag & 7}
		switch f.wire {
		case 0:
			if f.val, ok = p.varint(&pos, end); !ok {
				return errProto
			}
		case 1:
			if pos+8 > end {
				return errProto
			}
			f.off, f.end = pos, pos+8
			pos += 8
		case 5:
			if pos+4 > end {
				return errProto
			}
			f.off, f.end = pos, pos+4
			pos += 4
		case 2:
			l, ok := p.varint(&pos, end)
			if !ok || l > uint64(end-pos) {
				return errProto
			}
			f.off, f.end = pos, pos+int64(l)
			pos += int64(l)
		default:
			return errProto
		}
		if err := visit(f); err != nil {
			return err
		}
	}
	return nil
}

// ParseONNX tiles a ModelProto held in memory.
func ParseONNX(name string, data []byte) (*Doc, error) {
	d, err := ParseONNXAt(name, bytes.NewReader(data), int64(len(data)))
	if d != nil {
		d.Data = data
	}
	return d, err
}

// ParseONNXAt tiles a ModelProto read through a ReaderAt without reading tensor payloads:
// every dense initializer whose bytes are stored as raw_data (or packed float_data/
// double_data, which are the same little-endian bytes) becomes a tensor span; all other
// protobuf bytes remain literal. External-data initializers are not resolved and stay opaque.
func ParseONNXAt(name string, r io.ReaderAt, size int64) (*Doc, error) {
	d := &Doc{Format: "onnx", Name: name, R: r, Size: size}
	pb := &pbReader{r: r, base: -1}
	var spans []Seg
	count := 0
	err := pb.fields(0, size, func(f pbField) error {
		if f.num != 7 || f.wire != 2 { // ModelProto.graph
			return nil
		}
		return pb.fields(f.off, f.end, func(g pbField) error {
			if g.num != 5 || g.wire != 2 { // GraphProto.initializer
				return nil
			}
			if count++; count > 1<<20 {
				return errProto
			}
			var dims []uint64
			var dtype uint64
			var nm string
			rawOff, rawEnd := int64(-1), int64(-1)
			external := false
			err := pb.fields(g.off, g.end, func(t pbField) error {
				switch {
				case t.num == 1 && t.wire == 0:
					dims = append(dims, t.val)
				case t.num == 1 && t.wire == 2: // packed dims
					b, ok := pb.Small(t.off, t.end)
					if !ok {
						return errProto
					}
					for p := 0; p < len(b); {
						v, ok := pbVarintBytes(b, &p)
						if !ok {
							return errProto
						}
						dims = append(dims, v)
					}
				case t.num == 2 && t.wire == 0:
					dtype = t.val
				case t.num == 8 && t.wire == 2:
					b, ok := pb.Small(t.off, t.end)
					if !ok {
						return errProto
					}
					nm = string(b)
				case (t.num == 9 || t.num == 4 || t.num == 10) && t.wire == 2:
					if rawOff >= 0 {
						rawOff = -2 // two data fields: ambiguous, leave literal
					} else {
						rawOff, rawEnd = t.off, t.end
					}
					if t.num == 4 && dtype != 1 {
						rawOff = -2
					}
				case t.num == 13 || (t.num == 14 && t.val == 1):
					external = true
				case t.num == 5 || t.num == 7 || t.num == 3 || t.num == 11 || t.num == 12:
					rawOff = -2 // typed repeated fields with different byte forms: not canonicalised here
				}
				return nil
			})
			if err != nil {
				return err
			}
			dt, ok := onnxDType[dtype]
			if !ok || external || rawOff < 0 || rawEnd == rawOff {
				return nil
			}
			n := uint64(1)
			for _, x := range dims {
				if x != 0 && n > 1<<50/x {
					return nil
				}
				n *= x
			}
			if w := ElemBytes(dt); w == 0 || n*uint64(w) != uint64(rawEnd-rawOff) {
				return nil
			}
			spans = append(spans, Seg{Off: rawOff, Len: rawEnd - rawOff, T: &TInfo{Name: nm, DType: dt, Shape: dims}})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	d.Segs = tile(size, spans)
	return d, d.Check()
}

func pbVarintBytes(b []byte, pos *int) (uint64, bool) {
	var v uint64
	for s := uint(0); s < 70; s += 7 {
		if *pos >= len(b) {
			return 0, false
		}
		c := b[*pos]
		*pos++
		if s == 63 && c > 1 {
			return 0, false
		}
		v |= uint64(c&127) << s
		if c < 128 {
			return v, true
		}
	}
	return 0, false
}
