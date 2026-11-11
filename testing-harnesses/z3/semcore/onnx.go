package semcore

import (
	"errors"
)

// ONNX TensorProto data_type -> semcas dtype.
var onnxDType = map[uint64]int{1: DF32, 2: DU8, 3: DI8, 4: DU16, 5: DI16, 6: DI32, 7: DI64, 9: DBool,
	10: DF16, 11: DF64, 12: DU32, 13: DU64, 16: DBF16}

var errProto = errors.New("semcore: malformed protobuf")

type pbField struct {
	num, wire uint64
	val       uint64 // varint value
	off, end  int    // payload span for length-delimited / fixed
}

func pbVarint(b []byte, pos *int, end int) (uint64, bool) {
	var v uint64
	for s := uint(0); s < 70; s += 7 {
		if *pos >= end {
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

func pbFields(b []byte, start, end int, visit func(pbField) error) error {
	for pos := start; pos < end; {
		tag, ok := pbVarint(b, &pos, end)
		if !ok || tag>>3 == 0 {
			return errProto
		}
		f := pbField{num: tag >> 3, wire: tag & 7}
		switch f.wire {
		case 0:
			if f.val, ok = pbVarint(b, &pos, end); !ok {
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
			l, ok := pbVarint(b, &pos, end)
			if !ok || l > uint64(end-pos) {
				return errProto
			}
			f.off, f.end = pos, pos+int(l)
			pos += int(l)
		default:
			return errProto
		}
		if err := visit(f); err != nil {
			return err
		}
	}
	return nil
}

// ParseONNX tiles a ModelProto: every dense initializer whose bytes are stored as
// raw_data (or packed float_data/double_data, which are the same little-endian bytes)
// becomes a tensor span; all other protobuf bytes remain literal. External-data
// initializers are not resolved and stay opaque.
func ParseONNX(name string, data []byte) (*Doc, error) {
	d := &Doc{Format: "onnx", Name: name, Data: data}
	var spans []Seg
	count := 0
	err := pbFields(data, 0, len(data), func(f pbField) error {
		if f.num != 7 || f.wire != 2 { // ModelProto.graph
			return nil
		}
		return pbFields(data, f.off, f.end, func(g pbField) error {
			if g.num != 5 || g.wire != 2 { // GraphProto.initializer
				return nil
			}
			if count++; count > 1<<20 {
				return errProto
			}
			var dims []uint64
			var dtype uint64
			var nm string
			rawOff, rawEnd := -1, -1
			external := false
			err := pbFields(data, g.off, g.end, func(t pbField) error {
				switch {
				case t.num == 1 && t.wire == 0:
					dims = append(dims, t.val)
				case t.num == 1 && t.wire == 2: // packed dims
					for p := t.off; p < t.end; {
						v, ok := pbVarint(data, &p, t.end)
						if !ok {
							return errProto
						}
						dims = append(dims, v)
					}
				case t.num == 2 && t.wire == 0:
					dtype = t.val
				case t.num == 8 && t.wire == 2:
					nm = string(data[t.off:t.end])
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
			spans = append(spans, Seg{Off: int64(rawOff), Len: int64(rawEnd - rawOff), T: &TInfo{Name: nm, DType: dt, Shape: dims}})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	d.Segs = tile(int64(len(data)), spans)
	return d, d.Check()
}
