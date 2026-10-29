package dexz3

import (
	"fmt"
	"io"
)

// walkSource retains one 64 KiB window. Larger individual instruction arrays are
// bounded by the existing MaxCodeUnit parser limit and released by the caller.
// A read failure is latched and returned by WalkAt; zero padding never authorizes output.
type walkSource struct {
	r    io.ReaderAt
	size int64
	lo   int64
	buf  []byte
	err  error
}

func (s *walkSource) span(off, n int64) []byte {
	if n < 0 || n > 2<<20 || off < 0 || off > s.size || n > s.size-off {
		if s.err == nil {
			s.err = fmt.Errorf("%w: source span %d+%d", ErrLimit, off, n)
		}
		// Parser call sites request fixed-width values or already-bounded instructions.
		// Invalid sizes cannot reach a slicing caller through the checked walker.
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
	size := int64(64 << 10)
	if size > s.size-lo {
		size = s.size - lo
	}
	if off+n > lo+size {
		lo = off
		size = int64(64 << 10)
		if size > s.size-lo {
			size = s.size - lo
		}
	}
	if cap(s.buf) < int(size) {
		s.buf = make([]byte, size)
	} else {
		s.buf = s.buf[:size]
	}
	s.lo = lo
	s.read(s.buf, lo)
	return s.buf[off-lo : off-lo+n]
}
func (s *walkSource) read(b []byte, off int64) {
	n, err := s.r.ReadAt(b, off)
	if n != len(b) || err != nil && err != io.EOF {
		clear(b[n:])
		if s.err == nil {
			s.err = fmt.Errorf("dexz3: source read at %d: %w", off, io.ErrUnexpectedEOF)
			if err != nil {
				s.err = fmt.Errorf("dexz3: source read at %d: %w", off, err)
			}
		}
	}
}
