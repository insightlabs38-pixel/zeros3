package idxref

import (
	"bufio"
	"io"
	"os"
	"sort"
)

type sortRef struct {
	pos, fileOff, dataOff int64
	n                     uint32
	stream, class         uint8
}

// SortedRefSpool stages bytes with a compact bounded index. Sorting preserves the
// legacy (payload position, file offset, original emission order) reference order.
type SortedRefSpool struct {
	f           *os.File
	w           *bufio.Writer
	index       []sortRef
	limit, size int64
	max         int
	sealed      bool
}

func NewSortedRefSpool(dir string, max int, limit int64) (*SortedRefSpool, error) {
	if max < 0 || max > 2<<20 || limit < 0 {
		return nil, bad("sorted reference limits")
	}
	f, e := os.CreateTemp(dir, "zeros3-sorted-refs-*")
	if e != nil {
		return nil, e
	}
	return &SortedRefSpool{f: f, w: bufio.NewWriterSize(f, 64<<10), index: make([]sortRef, 0, max), max: max, limit: limit}, nil
}
func (s *SortedRefSpool) Close() error {
	name := s.f.Name()
	e := s.f.Close()
	r := os.Remove(name)
	if e != nil {
		return e
	}
	return r
}
func (s *SortedRefSpool) Append(pos, fileOff int64, stream, class uint8, b []byte) error {
	if s.sealed || pos < 0 || fileOff < 0 || len(s.index) >= s.max || len(b) > 16<<20 || s.size > s.limit || int64(len(b)) > s.limit-s.size {
		return bad("sorted reference limits")
	}
	n, e := s.w.Write(b)
	if e != nil {
		return e
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	s.index = append(s.index, sortRef{pos, fileOff, s.size, uint32(len(b)), stream, class})
	s.size += int64(len(b))
	return nil
}
func (s *SortedRefSpool) CopySorted(dst *RefSpool) error {
	if s.sealed {
		return bad("reference spool already sealed")
	}
	s.sealed = true
	if e := s.w.Flush(); e != nil {
		return e
	}
	sort.SliceStable(s.index, func(i, j int) bool {
		a, b := s.index[i], s.index[j]
		return a.pos < b.pos || a.pos == b.pos && a.fileOff < b.fileOff
	})
	var buf []byte
	for _, x := range s.index {
		if cap(buf) < int(x.n) {
			buf = make([]byte, x.n)
		} else {
			buf = buf[:x.n]
		}
		n, e := s.f.ReadAt(buf, x.dataOff)
		if n != len(buf) {
			return io.ErrUnexpectedEOF
		}
		if e != nil && e != io.EOF {
			return e
		}
		if e := dst.Append(x.pos, x.stream, x.class, buf); e != nil {
			return e
		}
	}
	s.index = nil
	return nil
}
