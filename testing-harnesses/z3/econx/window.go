package econx

import (
	"fmt"
	"io"
	"math/bits"
	"zeros3-z3/idxref"
	"zeros3-z3/lab"
)

// UnitCutAt is UnitCutBytes over an immutable windowed payload. Raw regions read
// at most 4*target bytes; record regions use the identical precomputed hashes.
func UnitCutAt(r io.ReaderAt, secs []idxref.Sec, t int) ([]int, error) {
	if t < 4 || t > 1<<20 || t&(t-1) != 0 {
		return nil, fmt.Errorf("invalid unit target %d", t)
	}
	var out []int
	scratch := make([]byte, 4*t)
	for _, s := range secs {
		if s.Off < 0 || s.End < s.Off {
			return nil, fmt.Errorf("invalid section")
		}
		if len(s.Items) < 2 {
			for off := s.Off; off < s.End; {
				n := s.End - off
				if n > len(scratch) {
					n = len(scratch)
				}
				got, err := r.ReadAt(scratch[:n], int64(off))
				if got != n {
					return nil, io.ErrUnexpectedEOF
				}
				if err != nil && err != io.EOF {
					return nil, err
				}
				n = len(lab.ChunkScaled(scratch[:n], t/4, t, 4*t)[0])
				off += n
				out = append(out, off)
			}
			continue
		}
		if s.End == s.Off || s.Hash != nil && len(s.Hash) != len(s.Items) {
			return nil, fmt.Errorf("invalid record section")
		}
		m := uint64(t) * uint64(len(s.Items)) / uint64(s.End-s.Off)
		if m < 1 {
			m = 1
		}
		m = 1 << uint(bits.Len64(m*3/2)-1)
		start := s.Off
		for i, st := range s.Items {
			end := s.End
			if i+1 < len(s.Items) {
				end = s.Items[i+1]
			}
			if st < s.Off || end < st || end > s.End {
				return nil, fmt.Errorf("invalid record")
			}
			var h uint64
			if s.Hash != nil {
				h = s.Hash[i]
			} else {
				var err error
				h, err = idxref.FNVRange(r, int64(st), int64(end), scratch)
				if err != nil {
					return nil, err
				}
			}
			if h%m == 0 || end-start >= 4*t {
				out = append(out, end)
				start = end
			}
		}
		if start < s.End {
			out = append(out, s.End)
		}
	}
	return out, nil
}
