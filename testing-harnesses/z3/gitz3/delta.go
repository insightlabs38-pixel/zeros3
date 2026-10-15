package gitz3

// Git delta format: varint(source size), varint(target size), then ops:
//   0x80|flags: copy (offset bytes per flags 0x01..0x08, size bytes 0x10..0x40, size 0 = 0x10000)
//   1..0x7f   : insert that many literal bytes
//   0         : reserved (error)

type DeltaStats struct {
	Src, Tgt    uint64
	Copies      int
	Inserts     int
	CopyBytes   int64
	InsertBytes int64
}

func deltaVarint(d []byte, i int) (uint64, int, error) {
	var v uint64
	for shift := uint(0); ; shift += 7 {
		if i >= len(d) {
			return 0, i, corrupt("delta header truncated")
		}
		if shift > 56 {
			return 0, i, corrupt("delta size varint too long")
		}
		c := d[i]
		i++
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return v, i, nil
		}
	}
}

// ParseDeltaHeader returns the source and target sizes and the header length.
func ParseDeltaHeader(d []byte) (src, tgt uint64, n int, err error) {
	src, n, err = deltaVarint(d, 0)
	if err != nil {
		return
	}
	tgt, n, err = deltaVarint(d, n)
	return
}

// nextOp decodes one op at d[i:]. For a copy it returns (off, size, ins=nil); for
// an insert the literal slice.
func nextOp(d []byte, i int) (off, size uint64, ins []byte, next int, err error) {
	c := d[i]
	i++
	switch {
	case c == 0:
		return 0, 0, nil, i, corrupt("delta opcode 0")
	case c&0x80 != 0:
		for b := uint(0); b < 4; b++ {
			if c&(1<<b) != 0 {
				if i >= len(d) {
					return 0, 0, nil, i, corrupt("delta copy truncated")
				}
				off |= uint64(d[i]) << (8 * b)
				i++
			}
		}
		for b := uint(0); b < 3; b++ {
			if c&(0x10<<b) != 0 {
				if i >= len(d) {
					return 0, 0, nil, i, corrupt("delta copy truncated")
				}
				size |= uint64(d[i]) << (8 * b)
				i++
			}
		}
		if size == 0 {
			size = 0x10000
		}
		return off, size, nil, i, nil
	default:
		n := int(c)
		if i+n > len(d) {
			return 0, 0, nil, i, corrupt("delta insert truncated")
		}
		return 0, 0, d[i : i+n], i + n, nil
	}
}

// ScanDelta validates a delta program's structure without a base (copy ranges
// are only checked against the declared source size) and counts its operations.
func ScanDelta(d []byte) (DeltaStats, error) {
	var st DeltaStats
	src, tgt, i, err := ParseDeltaHeader(d)
	if err != nil {
		return st, err
	}
	st.Src, st.Tgt = src, tgt
	var out uint64
	for i < len(d) {
		off, size, ins, nx, err := nextOp(d, i)
		if err != nil {
			return st, err
		}
		i = nx
		if ins != nil {
			st.Inserts++
			st.InsertBytes += int64(len(ins))
			out += uint64(len(ins))
		} else {
			if off > src || size > src-off {
				return st, corrupt("delta copy %d+%d outside source %d", off, size, src)
			}
			st.Copies++
			st.CopyBytes += int64(size)
			out += size
		}
		if out > tgt {
			return st, corrupt("delta output exceeds declared target")
		}
	}
	if out != tgt {
		return st, corrupt("delta produces %d bytes, declared %d", out, tgt)
	}
	return st, nil
}

// ApplyDelta applies a delta to base. maxOut bounds the target size. The
// target size is rejected up front if the delta is too short to produce it
// (one copy op byte yields at most 64 KiB), so a hostile header cannot make us
// allocate more than 64 KiB per delta byte nor more than maxOut.
func ApplyDelta(base, d []byte, maxOut int64) ([]byte, error) {
	src, tgt, i, err := ParseDeltaHeader(d)
	if err != nil {
		return nil, err
	}
	if src != uint64(len(base)) {
		return nil, corrupt("delta source size %d, base %d", src, len(base))
	}
	if maxOut > 0 && tgt > uint64(maxOut) {
		return nil, limit("delta target %d > %d", tgt, maxOut)
	}
	if tgt > uint64(len(d))*0x10000 {
		return nil, corrupt("delta target %d impossible from %d delta bytes", tgt, len(d))
	}
	out := make([]byte, 0, tgt)
	for i < len(d) {
		off, size, ins, nx, err := nextOp(d, i)
		if err != nil {
			return nil, err
		}
		i = nx
		if ins != nil {
			if uint64(len(out))+uint64(len(ins)) > tgt {
				return nil, corrupt("delta output exceeds declared target")
			}
			out = append(out, ins...)
			continue
		}
		if off > src || size > src-off {
			return nil, corrupt("delta copy %d+%d outside base %d", off, size, src)
		}
		if uint64(len(out))+size > tgt {
			return nil, corrupt("delta output exceeds declared target")
		}
		out = append(out, base[off:off+size]...)
	}
	if uint64(len(out)) != tgt {
		return nil, corrupt("delta produced %d bytes, declared %d", len(out), tgt)
	}
	return out, nil
}
