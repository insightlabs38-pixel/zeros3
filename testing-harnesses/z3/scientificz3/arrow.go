package scientificz3

import (
	"encoding/binary"
	"fmt"
)

// FlatBuffers accessors validate vtable/object/field/vector bounds before use.
// They never allocate from an unvalidated count.
type flat struct {
	b              []byte
	p, vt, obj, vs int
}

func table(b []byte, p int) (flat, error) {
	t := flat{b: b, p: p}
	if p < 0 || p > len(b)-4 {
		return t, bad("FlatBuffers table")
	}
	back := int64(int32(binary.LittleEndian.Uint32(b[p:])))
	v := int64(p) - back
	if v < 0 || v > int64(len(b)-4) {
		return t, bad("FlatBuffers vtable")
	}
	t.vt = int(v)
	t.vs = int(binary.LittleEndian.Uint16(b[t.vt:]))
	t.obj = int(binary.LittleEndian.Uint16(b[t.vt+2:]))
	if t.vs < 4 || t.vs&1 != 0 || t.vs > len(b)-t.vt || t.obj < 4 || t.obj > len(b)-p {
		return t, bad("FlatBuffers sizes")
	}
	return t, nil
}
func rootTable(b []byte) (flat, error) {
	if len(b) < 4 {
		return flat{}, bad("FlatBuffers root")
	}
	p := uint64(binary.LittleEndian.Uint32(b))
	if p > uint64(len(b)) {
		return flat{}, bad("FlatBuffers root offset")
	}
	return table(b, int(p))
}
func (t flat) field(slot, n int) (int, error) {
	v := 4 + slot*2
	if v >= t.vs {
		return 0, nil
	}
	off := int(binary.LittleEndian.Uint16(t.b[t.vt+v:]))
	if off == 0 {
		return 0, nil
	}
	if off < 4 || n > t.obj-off {
		return 0, bad("FlatBuffers field")
	}
	return t.p + off, nil
}
func (t flat) number(slot, n int) (uint64, error) {
	p, e := t.field(slot, n)
	if e != nil || p == 0 {
		return 0, e
	}
	return u(t.b[p:], n)
}
func (t flat) target(slot int) (int, error) {
	p, e := t.field(slot, 4)
	if e != nil || p == 0 {
		return 0, e
	}
	v := uint64(binary.LittleEndian.Uint32(t.b[p:]))
	if v < 4 || v > uint64(len(t.b)-p) {
		return 0, bad("FlatBuffers offset")
	}
	return p + int(v), nil
}
func (t flat) child(slot int) (flat, bool, error) {
	p, e := t.target(slot)
	if e != nil || p == 0 {
		return flat{}, false, e
	}
	v, e := table(t.b, p)
	return v, true, e
}
func (t flat) vector(slot, width, max int) ([]byte, int, error) {
	p, e := t.target(slot)
	if e != nil || p == 0 {
		return nil, 0, e
	}
	if p > len(t.b)-4 {
		return nil, 0, bad("FlatBuffers vector")
	}
	n := uint64(binary.LittleEndian.Uint32(t.b[p:]))
	if n > uint64(max) {
		return nil, 0, ErrLimit
	}
	p += 4
	if n > uint64((len(t.b)-p)/width) {
		return nil, 0, bad("FlatBuffers vector span")
	}
	return t.b[p : p+int(n)*width], int(n), nil
}

type arrowBlock struct{ off, meta, body int64 }

func footerBlocks(t flat, slot int, want []arrowBlock) error {
	b, n, e := t.vector(slot, 24, MaxRegions)
	if e != nil {
		return e
	}
	if n != len(want) {
		return bad("Arrow footer block count")
	}
	for i, v := range want {
		p := b[i*24:]
		off := int64(binary.LittleEndian.Uint64(p))
		meta := int64(int32(binary.LittleEndian.Uint32(p[8:])))
		body := int64(binary.LittleEndian.Uint64(p[16:]))
		if off != v.off || meta != v.meta || body != v.body {
			return bad("Arrow footer block identity")
		}
	}
	return nil
}

// ParseArrow recognizes modern IPC files and EOS-terminated streams. It reads
// buffer framing, not column semantics. Unknown compression stays opaque.
func ParseArrow(b []byte) (*Plan, error) {
	if len(b) > MaxSource {
		return nil, ErrLimit
	}
	if len(b) < 8 {
		return nil, bad("Arrow size")
	}
	file := string(b[:6]) == "ARROW1"
	p, end := 0, len(b)
	var footer flat
	if file {
		if len(b) < 18 || string(b[len(b)-6:]) != "ARROW1" {
			return nil, bad("Arrow file magic")
		}
		n := uint64(binary.LittleEndian.Uint32(b[len(b)-10:]))
		if n > MaxRegion || n > uint64(len(b)-18) {
			return nil, ErrLimit
		}
		end = len(b) - 10 - int(n)
		var e error
		footer, e = rootTable(b[end : len(b)-10])
		if e != nil {
			return nil, e
		}
		p = 8
	}
	plan := &Plan{Format: "arrow-ipc"}
	var records, dicts []arrowBlock
	messages := 0
	eos := false
	for p < end {
		if end-p < 4 {
			return nil, bad("IPC prefix")
		}
		start := p
		n := uint64(binary.LittleEndian.Uint32(b[p:]))
		p += 4
		if n == 0xffffffff {
			if end-p < 4 {
				return nil, bad("IPC continuation")
			}
			n = uint64(binary.LittleEndian.Uint32(b[p:]))
			p += 4
		}
		if n == 0 {
			if p != end {
				return nil, bad("IPC EOS tail")
			}
			eos = true
			break
		}
		if messages++; messages > MaxRegions {
			return nil, ErrLimit
		}
		if n > MaxRegion || n > uint64(end-p) {
			return nil, ErrLimit
		}
		meta := b[p : p+int(n)]
		m, e := rootTable(meta)
		if e != nil {
			return nil, e
		}
		ver, e := m.number(0, 2)
		if e != nil {
			return nil, e
		}
		if ver < 3 || ver > 4 {
			return nil, ErrUnsupported
		}
		kind, e := m.number(1, 1)
		if e != nil {
			return nil, e
		}
		body, e := m.number(3, 8)
		if e != nil {
			return nil, e
		}
		p += int(n)
		if body > uint64(end-p) || body > MaxSource {
			return nil, bad("IPC body bounds")
		}
		bodyStart := p
		p += int(body)
		header, ok, e := m.child(2)
		if e != nil {
			return nil, e
		}
		if !ok {
			return nil, bad("IPC union header")
		}
		if kind == 1 {
			if body != 0 {
				return nil, bad("schema body")
			}
			continue
		}
		block := arrowBlock{int64(start), int64(bodyStart - start), int64(body)}
		if kind == 2 {
			dicts = append(dicts, block)
			header, ok, e = header.child(1)
			if e != nil {
				return nil, e
			}
			if !ok {
				return nil, bad("dictionary batch")
			}
		} else if kind == 3 {
			records = append(records, block)
		} else {
			if body > 0 {
				plan.Regions = append(plan.Regions, Region{int64(bodyStart), int64(body), "opaque", 0, int64(body)})
			}
			continue
		}
		length, e := header.number(0, 8)
		if e != nil {
			return nil, e
		}
		if length > 1<<40 {
			return nil, ErrLimit
		}
		nodes, nn, e := header.vector(1, 16, MaxRegions)
		if e != nil {
			return nil, e
		}
		for i := 0; i < nn; i++ {
			v := nodes[i*16:]
			count := binary.LittleEndian.Uint64(v)
			nulls := binary.LittleEndian.Uint64(v[8:])
			if count > 1<<40 || nulls > count {
				return nil, bad("Arrow field nodes")
			}
		}
		codec := "raw"
		compression, compressed, e := header.child(3)
		if e != nil {
			return nil, e
		}
		if compressed {
			method, e := compression.number(1, 1)
			if e != nil {
				return nil, e
			}
			if method != 0 {
				return nil, ErrUnsupported
			}
			v, e := compression.number(0, 1)
			if e != nil {
				return nil, e
			}
			switch v {
			case 0:
				codec = "opaque-lz4"
			case 1:
				codec = "zstd"
			default:
				return nil, ErrUnsupported
			}
		}
		buffers, nb, e := header.vector(2, 16, MaxRegions)
		if e != nil {
			return nil, e
		}
		for i := 0; i < nb; i++ {
			v := buffers[i*16:]
			off := binary.LittleEndian.Uint64(v)
			n := binary.LittleEndian.Uint64(v[8:])
			if off > body || n > body-off {
				return nil, bad("Arrow buffer span")
			}
			if n == 0 {
				continue
			}
			if n > MaxRegion {
				return nil, ErrLimit
			}
			pos := uint64(bodyStart) + off
			decoded := int64(n)
			c := codec
			if compressed {
				if n < 8 {
					return nil, bad("compressed buffer prefix")
				}
				decl := int64(binary.LittleEndian.Uint64(b[pos:]))
				pos += 8
				n -= 8
				if decl == -1 {
					c = "raw"
					decoded = int64(n)
				} else {
					if decl < 0 || decl > MaxRegion {
						return nil, ErrLimit
					}
					decoded = decl
				}
			}
			if n == 0 {
				continue
			}
			w := 0
			if c == "raw" && n%8 == 0 || c == "zstd" && decoded%8 == 0 {
				w = 8
			}
			plan.Regions = append(plan.Regions, Region{int64(pos), int64(n), c, w, decoded})
			if len(plan.Regions) > MaxRegions {
				return nil, ErrLimit
			}
		}
	}
	if !eos {
		return nil, bad("missing IPC EOS")
	}
	if file {
		ver, e := footer.number(0, 2)
		if e != nil {
			return nil, e
		}
		if ver < 3 || ver > 4 {
			return nil, ErrUnsupported
		}
		if e = footerBlocks(footer, 2, dicts); e != nil {
			return nil, e
		}
		if e = footerBlocks(footer, 3, records); e != nil {
			return nil, e
		}
	}
	if e := plan.validate(len(b)); e != nil {
		return nil, fmt.Errorf("Arrow: %w", e)
	}
	return plan, nil
}
