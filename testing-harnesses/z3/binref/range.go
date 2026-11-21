package binref

import (
	"encoding/binary"
	"errors"
	"math"
)

// StreamAt reads a portion of one normalized stream. n == -1 requests all
// bytes, and missing optional reference streams return an empty slice.
type StreamAt func(kind string, region int, off, n int64) ([]byte, error)

type sliceRegion struct {
	off, size int64
	address   uint64
	mapped    bool
}

// ReconstructRange reads matching payload slices and only the reference
// streams for intersecting regions. The caller authenticates each fetched stream.
func ReconstructRange(read StreamAt, off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || n > 64<<20 || off > math.MaxInt64-n {
		return nil, errors.New("invalid executable range")
	}
	head, err := read("regions", 0, 0, -1)
	if err != nil {
		return nil, err
	}
	r := &rd{b: head}
	base, base2, count := r.uv(), r.uv(), r.uv()
	if r.err != nil || count > uint64(len(r.b)/2) {
		return nil, errCorrupt
	}
	regions := make([]sliceRegion, count)
	var total int64
	for i := range regions {
		size, flags := r.uv(), r.take(1)[0]
		if r.err != nil || size > uint64(math.MaxInt64-total) || flags&^byte(3) != 0 {
			return nil, errCorrupt
		}
		regions[i] = sliceRegion{off: total, size: int64(size), mapped: flags&1 != 0}
		if regions[i].mapped {
			regions[i].address = r.uv()
		}
		if flags&2 != 0 {
			r.uv()
		}
		total += int64(size)
	}
	if r.err != nil || len(r.b) != 0 || off+n > total {
		return nil, errCorrupt
	}
	out := make([]byte, n)
	var units map[uint32]uint64
	for i, region := range regions {
		first := max(region.off, off)
		last := min(region.off+region.size, off+n)
		if first >= last {
			continue
		}
		p, err := read("payload", i, first-region.off, last-first)
		if err != nil {
			return nil, err
		}
		if int64(len(p)) != last-first {
			return nil, errCorrupt
		}
		copy(out[first-off:last-off], p)
		ref, err := read("refs", i, 0, -1)
		if err != nil {
			return nil, err
		}
		if len(ref) == 0 {
			continue
		}
		if units == nil {
			raw, err := read("units", 0, 0, -1)
			if err != nil {
				return nil, err
			}
			u := &rd{b: raw}
			length := u.uv()
			if u.err != nil || length > uint64(len(u.b)/5) {
				return nil, errCorrupt
			}
			units = make(map[uint32]uint64, length)
			var address uint64
			for range length {
				id := binary.LittleEndian.Uint32(u.take(4))
				step := u.uv()
				if u.err != nil || step > math.MaxUint64-address {
					return nil, errCorrupt
				}
				address += step
				units[id] = address
			}
			if len(u.b) != 0 {
				return nil, errCorrupt
			}
		}
		d := &rd{b: ref}
		count := d.uv()
		if d.err != nil || count > uint64(len(d.b)/4) {
			return nil, errCorrupt
		}
		var previous int64
		for range count {
			step := d.uv()
			flags := d.take(1)[0]
			enc, bias := Enc(flags&7), (flags>>3)&7
			if d.err != nil || enc < EncRel32 || enc > EncOff32 || step > uint64(region.size-previous) {
				return nil, errCorrupt
			}
			pos := previous + int64(step)
			width := int64(enc.width())
			if width > region.size-pos {
				return nil, errCorrupt
			}
			previous = pos + width
			var target, delta uint64
			if flags&0x80 != 0 {
				next := d.uv()
				if next >= uint64(len(regions)) {
					return nil, errCorrupt
				}
				target, delta = regions[next].address, d.uv()
			} else {
				id := binary.LittleEndian.Uint32(d.take(4))
				var exists bool
				target, exists = units[id]
				if !exists {
					return nil, errCorrupt
				}
				delta = d.uv()
			}
			if d.err != nil || delta > math.MaxUint64-target || !region.mapped && enc == EncRel32 {
				return nil, errCorrupt
			}
			a, b := region.off+pos, region.off+previous
			if a >= last {
				break
			}
			if b <= first {
				continue
			}
			addr := uint64(0)
			if region.mapped {
				addr = region.address + uint64(pos)
			}
			var word [8]byte
			putLE(word[:], int(width), encodeSlot(enc, bias, addr, base, base2, target+delta))
			z, end := max(a, first), min(b, last)
			copy(out[z-off:end-off], word[z-a:end-a])
		}
	}
	return out, nil
}
