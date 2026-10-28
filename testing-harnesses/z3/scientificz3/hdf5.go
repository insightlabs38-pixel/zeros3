package scientificz3

import (
	"bytes"
	"encoding/binary"
	"math/bits"
)

type message struct {
	kind  byte
	flags byte
	data  []byte
}

func messages(b []byte, addr uint64) ([]message, [2]uint64, error) {
	var bounds [2]uint64
	head, e := span(b, addr, 6)
	if e != nil {
		return nil, bounds, e
	}
	if string(head[:4]) != "OHDR" || head[4] != 2 {
		return nil, bounds, ErrUnsupported
	}
	flags := head[5]
	if flags&0xc0 != 0 {
		return nil, bounds, ErrUnsupported
	}
	p := addr + 6
	if flags&0x20 != 0 {
		p += 4
	}
	if flags&0x10 != 0 {
		p += 16
	}
	w := 1 << uint(flags&3)
	raw, e := span(b, p, uint64(w))
	if e != nil {
		return nil, bounds, e
	}
	n, e := u(raw, w)
	if e != nil {
		return nil, bounds, e
	}
	if n > MaxRegion {
		return nil, bounds, ErrLimit
	}
	p += uint64(w)
	all, e := span(b, addr, p-addr+n+4)
	if e != nil {
		return nil, bounds, e
	}
	if !checksum(all) {
		return nil, bounds, bad("object checksum")
	}
	bounds = [2]uint64{addr, addr + uint64(len(all))}
	end := p + n
	var out []message
	for p < end {
		hs := uint64(4)
		if flags&4 != 0 {
			hs += 2
		}
		if end-p < hs {
			tail, _ := span(b, p, end-p)
			if !bytes.Equal(tail, make([]byte, len(tail))) {
				return nil, bounds, bad("object padding")
			}
			break
		}
		h, _ := span(b, p, hs)
		sz := uint64(binary.LittleEndian.Uint16(h[1:3]))
		if sz > end-p-hs {
			return nil, bounds, bad("message size")
		}
		data, _ := span(b, p+hs, sz)
		out = append(out, message{h[0], h[3], data})
		if len(out) > 4096 {
			return nil, bounds, ErrLimit
		}
		p += hs + sz
	}
	return out, bounds, nil
}

// ParseHDF5 supports superblock3, compact hard links, object-header2 and
// unpaged fixed-array chunk indexes. Other indexes/versions remain explicit CDC.
// Filtered chunks are opaque; raw chunks may use the existing exact planes op.
func ParseHDF5(b []byte) (*Plan, error) {
	if len(b) > MaxSource {
		return nil, ErrLimit
	}
	if len(b) < 48 || string(b[:8]) != "\x89HDF\r\n\x1a\n" {
		return nil, bad("HDF5 signature")
	}
	if b[8] != 3 || b[9] != 8 || b[10] != 8 || b[11] != 0 {
		return nil, ErrUnsupported
	}
	if !checksum(b[:48]) {
		return nil, bad("superblock checksum")
	}
	if binary.LittleEndian.Uint64(b[12:20]) != 0 || binary.LittleEndian.Uint64(b[20:28]) != ^uint64(0) || binary.LittleEndian.Uint64(b[28:36]) != uint64(len(b)) {
		return nil, ErrUnsupported
	}
	root := binary.LittleEndian.Uint64(b[36:44])
	ms, rb, e := messages(b, root)
	if e != nil {
		return nil, e
	}
	var target uint64
	links := 0
	for _, m := range ms {
		if m.kind != 6 {
			continue
		}
		links++
		if links > 1 || m.flags&2 != 0 {
			return nil, ErrUnsupported
		}
		d := m.data
		if len(d) < 2 || d[0] != 1 {
			return nil, ErrUnsupported
		}
		f := d[1]
		if f&0xe0 != 0 {
			return nil, ErrUnsupported
		}
		p := 2
		if f&4 != 0 {
			if len(d) <= p || d[p] != 0 {
				return nil, ErrUnsupported
			}
			p++
		}
		if f&8 != 0 {
			p += 8
		}
		if f&16 != 0 {
			p++
		}
		w := 1 << uint(f&3)
		if p > len(d) {
			return nil, bad("link")
		}
		n, e := u(d[p:], w)
		if e != nil {
			return nil, e
		}
		p += w
		if n > 4096 || n > uint64(len(d)-p) || len(d)-p-int(n) != 8 {
			return nil, bad("hard link")
		}
		p += int(n)
		target = binary.LittleEndian.Uint64(d[p:])
	}
	if links != 1 {
		return nil, ErrUnsupported
	}
	ms, db, e := messages(b, target)
	if e != nil {
		return nil, e
	}
	var layout []byte
	for _, m := range ms {
		if m.kind == 8 {
			if layout != nil || m.flags&2 != 0 {
				return nil, ErrUnsupported
			}
			layout = m.data
		}
	}
	if len(layout) < 6 || layout[0] != 4 || layout[1] != 2 || layout[2] != 0 {
		return nil, ErrUnsupported
	}
	nd, w := int(layout[3]), int(layout[4])
	if nd < 2 || nd > 8 || w < 1 || w > 8 {
		return nil, ErrUnsupported
	}
	p := 5
	dims := make([]uint64, nd)
	raw := uint64(1)
	width := uint64(1)
	for i := range dims {
		if p > len(layout) {
			return nil, bad("chunk dimensions")
		}
		dims[i], e = u(layout[p:], w)
		if e != nil {
			return nil, e
		}
		p += w
		if dims[i] == 0 || dims[i] > MaxRegion || raw > MaxRegion/dims[i] {
			return nil, ErrLimit
		}
		raw *= dims[i]
		if i > 0 {
			width *= dims[i]
		}
	}
	if len(layout)-p != 10 || layout[p] != 3 {
		return nil, ErrUnsupported
	}
	pagebits := layout[p+1]
	if pagebits > 20 {
		return nil, ErrLimit
	}
	addr := binary.LittleEndian.Uint64(layout[p+2:])
	h, e := span(b, addr, 28)
	if e != nil {
		return nil, e
	}
	if string(h[:4]) != "FAHD" || h[4] != 0 || !checksum(h) {
		return nil, bad("fixed array header")
	}
	client, es := h[5], uint64(h[6])
	if h[7] != pagebits || !((client == 0 && es == 8) || (client == 1 && es == uint64(13+(bits.Len64(raw)+7)/8))) {
		return nil, ErrUnsupported
	}
	n := binary.LittleEndian.Uint64(h[8:16])
	if n == 0 || n > 1<<uint(pagebits) || n > MaxRegions {
		return nil, ErrUnsupported
	}
	dataAddr := binary.LittleEndian.Uint64(h[16:24])
	data, e := span(b, dataAddr, 14+n*es+4)
	if e != nil {
		return nil, e
	}
	if string(data[:4]) != "FADB" || data[4] != 0 || data[5] != client || binary.LittleEndian.Uint64(data[6:14]) != addr || !checksum(data) {
		return nil, bad("fixed array data")
	}
	plan := &Plan{Format: "hdf5-fixed-array"}
	meta := [][2]uint64{{0, 48}, rb, db, {addr, addr + 28}, {dataAddr, dataAddr + uint64(len(data))}}
	for i := uint64(0); i < n; i++ {
		v := data[14+i*es : 14+(i+1)*es]
		off := binary.LittleEndian.Uint64(v)
		if off == ^uint64(0) {
			continue
		}
		size := raw
		codec := "raw"
		if client == 1 {
			size, e = u(v[8:], int(es)-12)
			if e != nil {
				return nil, e
			}
			codec = "opaque-filter"
		}
		if size == 0 || size > MaxRegion {
			return nil, ErrLimit
		}
		if _, e = span(b, off, size); e != nil {
			return nil, e
		}
		for _, m := range meta {
			if off < m[1] && m[0] < off+size {
				return nil, bad("data/metadata overlap")
			}
		}
		wid := 0
		if codec == "raw" && width <= 4096 {
			wid = int(width)
		}
		plan.Regions = append(plan.Regions, Region{int64(off), int64(size), codec, wid, int64(raw)})
	}
	if e = plan.validate(len(b)); e != nil {
		return nil, e
	}
	return plan, nil
}
