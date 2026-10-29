package idxref

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
)

// RefSpool stages sorted references on disk. Only one bounded record is retained
// per iterator. Call Close on every exit; callers must not publish partial output.
type RefSpool struct {
	writer      *bufio.Writer
	reader      *bufio.Reader
	reading     bool
	f           *os.File
	size, limit int64
	last        int64
	count       int
	buf         []byte
}
type SpoolRef struct {
	Pos           int64
	Stream, Class uint8
	Bytes         []byte
}

func NewRefSpool(dir string, limit int64) (*RefSpool, error) {
	if limit < 0 {
		return nil, bad("reference spool limit")
	}
	f, e := os.CreateTemp(dir, "zeros3-refs-*")
	if e != nil {
		return nil, e
	}
	return &RefSpool{f: f, limit: limit, last: -1, writer: bufio.NewWriterSize(f, 64<<10), reader: bufio.NewReaderSize(f, 64<<10)}, nil
}
func (s *RefSpool) Close() error {
	name := s.f.Name()
	e := s.f.Close()
	r := os.Remove(name)
	if e != nil {
		return e
	}
	return r
}
func (s *RefSpool) Append(pos int64, stream, class uint8, b []byte) error {
	if s.reading || pos < 0 || pos < s.last || len(b) > 16<<20 || s.size > s.limit || int64(len(b))+14 > s.limit-s.size {
		return bad("reference spool limits/order")
	}
	var h [14]byte
	binary.LittleEndian.PutUint64(h[:], uint64(pos))
	h[8], h[9] = stream, class
	binary.LittleEndian.PutUint32(h[10:], uint32(len(b)))
	for _, p := range [][]byte{h[:], b} {
		n, e := s.writer.Write(p)
		if e != nil {
			return e
		}
		if n != len(p) {
			return io.ErrShortWrite
		}
	}
	s.size += int64(len(b)) + 14
	s.last = pos
	s.count++
	return nil
}
func (s *RefSpool) Rewind() error {
	if e := s.writer.Flush(); e != nil {
		return e
	}
	s.reading = true
	_, e := s.f.Seek(0, io.SeekStart)
	s.reader.Reset(s.f)
	return e
}
func (s *RefSpool) Next() (SpoolRef, error) {
	if !s.reading {
		return SpoolRef{}, bad("reference spool not rewound")
	}
	var h [14]byte
	if _, e := io.ReadFull(s.reader, h[:]); e != nil {
		return SpoolRef{}, e
	}
	n := int(binary.LittleEndian.Uint32(h[10:]))
	if n > 16<<20 {
		return SpoolRef{}, bad("reference record limit")
	}
	if cap(s.buf) < n {
		s.buf = make([]byte, n)
	} else {
		s.buf = s.buf[:n]
	}
	if _, e := io.ReadFull(s.reader, s.buf); e != nil {
		return SpoolRef{}, e
	}
	return SpoolRef{int64(binary.LittleEndian.Uint64(h[:])), h[8], h[9], s.buf}, nil
}
