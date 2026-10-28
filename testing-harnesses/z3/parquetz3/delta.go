package parquetz3

import "encoding/binary"

// deltaLengths decodes the DELTA_BINARY_PACKED length prefix. It retains the
// original prefix for replay, so block geometry, widths and padding stay exact.
// It deliberately bounds one page to 1M values and 64K-value blocks.
func deltaLengths(b []byte, count int) ([]int, int, error) {
	if count < 0 || count > 1<<20 {
		return nil, 0, unsupported("delta length count")
	}
	pos := 0
	uv := func() (uint64, bool) {
		if pos >= len(b) {
			return 0, false
		}
		v, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return 0, false
		}
		pos += n
		return v, true
	}
	sv := func() (int64, bool) {
		if pos >= len(b) {
			return 0, false
		}
		v, n := binary.Varint(b[pos:])
		if n <= 0 {
			return 0, false
		}
		pos += n
		return v, true
	}
	block, ok := uv()
	if !ok || block == 0 || block > 65536 || block%128 != 0 {
		return nil, 0, corrupt("delta block size")
	}
	minis, ok := uv()
	if !ok || minis == 0 || minis > block || block%minis != 0 || (block/minis)%32 != 0 {
		return nil, 0, corrupt("delta miniblocks")
	}
	total, ok := uv()
	if !ok || total != uint64(count) {
		return nil, 0, corrupt("delta value count")
	}
	if count == 0 {
		return nil, pos, nil
	}
	first, ok := sv()
	if !ok || first < 0 || first > 1<<31-1 {
		return nil, 0, corrupt("delta first length")
	}
	out := make([]int, 0, count)
	out = append(out, int(first))
	last := first
	per := int(block / minis)
	for len(out) < count {
		mind, ok := sv()
		if !ok || mind < -1<<31 || mind > 1<<31-1 {
			return nil, 0, corrupt("delta minimum")
		}
		if int(minis) > len(b)-pos {
			return nil, 0, corrupt("delta widths")
		}
		widths := b[pos : pos+int(minis)]
		pos += int(minis)
		for _, width := range widths {
			if width > 32 {
				return nil, 0, corrupt("delta length bit width")
			}
			size := per * int(width) / 8
			if size > len(b)-pos {
				return nil, 0, corrupt("delta packed values")
			}
			packed := b[pos : pos+size]
			pos += size
			for j := 0; j < per && len(out) < count; j++ {
				var v uint64
				for bit := 0; bit < int(width); bit++ {
					at := j*int(width) + bit
					v |= uint64(packed[at/8]>>uint(at%8)&1) << uint(bit)
				}
				// Parquet INT32 delta arithmetic wraps modulo 2^32. Lengths themselves
				// must remain nonnegative; signed overflow cannot allocate a huge record.
				last = int64(int32(uint32(last) + uint32(mind) + uint32(v)))
				if last < 0 {
					return nil, 0, corrupt("negative delta length")
				}
				out = append(out, int(last))
			}
		}
	}
	return out, pos, nil
}
func decodeDeltaLength(b []byte, count int, lim Limits) (recs, []byte, error) {
	lim = lim.norm()
	r := newRecs(colSpec{Phys: TByteArr})
	lengths, used, e := deltaLengths(b, count)
	if e != nil {
		return r, nil, e
	}
	pos := used
	for _, n := range lengths {
		if n > lim.MaxValue || n > len(b)-pos {
			return r, nil, corrupt("delta string length")
		}
		r.buf = binary.LittleEndian.AppendUint32(r.buf, uint32(n))
		r.buf = append(r.buf, b[pos:pos+n]...)
		pos += n
		r.ends = append(r.ends, len(r.buf))
		r.n++
	}
	if pos != len(b) {
		return r, nil, corrupt("delta trailing bytes")
	}
	return r, append([]byte(nil), b[:used]...), nil
}
func encodeDeltaLength(r *recs, prefix []byte) ([]byte, bool) {
	lengths, used, e := deltaLengths(prefix, r.n)
	if e != nil || used != len(prefix) {
		return nil, false
	}
	out := append([]byte(nil), prefix...)
	for i, n := range lengths {
		value := r.at(i)
		if len(value) < 4 || int(binary.LittleEndian.Uint32(value)) != n || len(value) != n+4 {
			return nil, false
		}
		out = append(out, value[4:]...)
	}
	return out, true
}
