package clrz3

import (
	"encoding/binary"
	"io"
)

// clrSource retains a 64 KiB field window; larger single records are bounded at
// 16 MiB. Span views expire on the next source read. Owned metadata heaps use
// separate checked buffers, never a sparse full-image allocation.
type clrSource struct {
	bodyWork int64
	bodyRefs int64
	r        io.ReaderAt
	size, lo int64
	buf      []byte
	err      error
}

func (s *clrSource) ReadAt(b []byte, off int64) (int, error) { return s.r.ReadAt(b, off) }
func (s *clrSource) span(off, n int64) []byte {
	if off < 0 || n < 0 || n > 16<<20 || off > s.size || n > s.size-off {
		if s.err == nil {
			s.err = limit("source span %d+%d", off, n)
		}
		return make([]byte, 4)
	}
	if n == 0 {
		return nil
	}
	if off >= s.lo && off+n <= s.lo+int64(len(s.buf)) {
		return s.buf[off-s.lo : off-s.lo+n]
	}
	if n > 64<<10 {
		b := make([]byte, n)
		s.read(b, off)
		return b
	}
	lo := off &^ int64((64<<10)-1)
	length := int64(64 << 10)
	if length > s.size-lo {
		length = s.size - lo
	}
	if off+n > lo+length {
		lo = off
		length = int64(64 << 10)
		if length > s.size-lo {
			length = s.size - lo
		}
	}
	if cap(s.buf) < int(length) {
		s.buf = make([]byte, length)
	} else {
		s.buf = s.buf[:length]
	}
	s.lo = lo
	s.read(s.buf, lo)
	return s.buf[off-lo : off-lo+n]
}
func (s *clrSource) read(b []byte, off int64) {
	n, e := s.r.ReadAt(b, off)
	if n != len(b) || e != nil && e != io.EOF {
		if s.err == nil {
			if e != nil {
				s.err = e
			} else {
				s.err = io.ErrUnexpectedEOF
			}
		}
		clear(b[n:])
	}
}
func (s *clrSource) owned(off, n int64) ([]byte, error) {
	if off < 0 || n < 0 || n > 16<<20 || off > s.size || n > s.size-off {
		return nil, limit("owned heap geometry")
	}
	b := make([]byte, n)
	s.read(b, off)
	return b, s.err
}
func (s *clrSource) col(f *File, t int, r uint32, c int) uint32 {
	cl := f.Tables[t].Cols[c]
	p := s.span(f.RowOff(t, r)+int64(cl.Off), int64(cl.W))
	if cl.W == 2 {
		return uint32(binary.LittleEndian.Uint16(p))
	}
	return binary.LittleEndian.Uint32(p)
}

// parseBodyAt first establishes a bounded record extent, then delegates all CIL,
// token, EH and malformed-byte validation to the unchanged canonical ParseBody.
func parseBodyAt(s *clrSource, off int64, strict bool) (Body, bool, error) {
	if off < 0 || off >= s.size {
		return Body{}, false, nil
	}
	available := s.size - off
	take := int64(12)
	if take > available {
		take = available
	}
	head := s.span(off, take)
	if s.err != nil {
		return Body{}, false, s.err
	}
	extent := take
	if head[0]&3 == 2 {
		extent = 1 + int64(head[0]>>2)
	} else if head[0]&3 == 3 && take >= 12 {
		flags := binary.LittleEndian.Uint16(head)
		code := int64(binary.LittleEndian.Uint32(head[4:]))
		if flags>>12 == 3 && code <= maxCode {
			if code > 1<<20 {
				return Body{}, false, limit("method code window")
			}
			extent = 12 + code
			if extent > 16<<20 {
				return Body{}, false, limit("method body window")
			}
			if extent <= available && flags&8 != 0 {
				p := (extent + 3) &^ 3
				for sections := 0; ; sections++ {
					if sections >= 1024 || p > 16<<20 {
						return Body{}, false, limit("method sections")
					}
					if p > available || available-p < 4 {
						extent = available
						break
					}
					h := s.span(off+p, 4)
					if s.err != nil {
						return Body{}, false, s.err
					}
					kind := h[0]
					n := int64(h[1])
					if kind&0x40 != 0 {
						n |= int64(h[2])<<8 | int64(h[3])<<16
					}
					extent = p + 4
					if n < 4 {
						break
					}
					if n > available-p {
						extent = available
						break
					}
					extent = p + n
					if extent > 16<<20 {
						return Body{}, false, limit("method section window")
					}
					if kind&0x80 == 0 {
						break
					}
					p = (extent + 3) &^ 3
				}
			}
		}
	}
	if extent > available {
		extent = available
	}
	if extent > 16<<20 {
		return Body{}, false, limit("method window")
	}
	if extent > s.size*16+(64<<20)-s.bodyWork {
		return Body{}, false, limit("aggregate method inspection")
	}
	s.bodyWork += extent
	raw := s.span(off, extent)
	if s.err != nil {
		return Body{}, false, s.err
	}
	b, ok := ParseBody(raw, 0, strict)
	refs := int64(len(b.Toks) + len(b.Clauses))
	if refs > (1<<20)-s.bodyRefs {
		return Body{}, false, limit("aggregate method references")
	}
	s.bodyRefs += refs
	b.Off = off
	if b.CodeOff != 0 {
		b.CodeOff += off
	}
	if b.End != 0 {
		b.End += off
	}
	if b.LocalOff != 0 {
		b.LocalOff += off
	}
	if b.SecOff > 0 {
		b.SecOff += off
	}
	if b.SecEnd != 0 {
		b.SecEnd += off
	}
	for i := range b.Toks {
		b.Toks[i].Off += off
	}
	for i := range b.Clauses {
		b.Clauses[i].Off += off
		if b.Clauses[i].TokOff != 0 {
			b.Clauses[i].TokOff += off
		}
	}
	return b, ok, nil
}
