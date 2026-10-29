package idxref

import (
	"bufio"
	"container/heap"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const sortRunRecords = 4096

type sortRef struct {
	pos, fileOff, dataOff, seq int64
	n                          uint32
	stream, class              uint8
}

// SortedRefSpool uses bounded external runs. Its RAM does not scale with the
// number of references: one 4096-record sorting window, bounded merge cursors,
// and one record buffer replace the former whole-reference sorting index.
type SortedRefSpool struct {
	f                      *os.File
	w                      *bufio.Writer
	index                  []sortRef
	runs                   []string
	limit, size, chunkSize int64
	max, count             int
	sealed                 bool
}

func NewSortedRefSpool(dir string, max int, limit int64) (*SortedRefSpool, error) {
	if max < 0 || max > 2<<20 || limit < 0 {
		return nil, bad("sorted reference limits")
	}
	f, e := os.CreateTemp(dir, "zeros3-sorted-refs-*")
	if e != nil {
		return nil, e
	}
	capRecords := max
	if capRecords > sortRunRecords {
		capRecords = sortRunRecords
	}
	return &SortedRefSpool{f: f, w: bufio.NewWriterSize(f, 64<<10), index: make([]sortRef, 0, capRecords), max: max, limit: limit}, nil
}
func (s *SortedRefSpool) Close() error {
	name := s.f.Name()
	first := s.f.Close()
	for _, name := range append(s.runs, name) {
		if e := os.Remove(name); e != nil && first == nil {
			first = e
		}
	}
	return first
}
func (s *SortedRefSpool) Append(pos, fileOff int64, stream, class uint8, b []byte) error {
	if s.sealed || pos < 0 || fileOff < 0 || s.count >= s.max || len(b) > 16<<20 || s.size > s.limit || int64(len(b)) > s.limit-s.size {
		return bad("sorted reference limits")
	}
	n, e := s.w.Write(b)
	if e != nil {
		return e
	}
	if n != len(b) {
		return io.ErrShortWrite
	}
	s.index = append(s.index, sortRef{pos, fileOff, s.chunkSize, int64(s.count), uint32(len(b)), stream, class})
	s.size += int64(len(b))
	s.chunkSize += int64(len(b))
	s.count++
	if len(s.index) == sortRunRecords {
		return s.flushRun()
	}
	return nil
}
func (s *SortedRefSpool) flushRun() error {
	if len(s.index) == 0 {
		return nil
	}
	if e := s.w.Flush(); e != nil {
		return e
	}
	sort.Slice(s.index, func(i, j int) bool {
		a, b := s.index[i], s.index[j]
		return a.pos < b.pos || a.pos == b.pos && (a.fileOff < b.fileOff || a.fileOff == b.fileOff && a.seq < b.seq)
	})
	run, e := os.CreateTemp(filepath.Dir(s.f.Name()), "zeros3-ref-run-*")
	if e != nil {
		return e
	}
	s.runs = append(s.runs, run.Name())
	defer run.Close()
	w := bufio.NewWriterSize(run, 64<<10)
	var buf []byte
	for _, x := range s.index {
		var h [30]byte
		binary.LittleEndian.PutUint64(h[:], uint64(x.pos))
		binary.LittleEndian.PutUint64(h[8:], uint64(x.fileOff))
		binary.LittleEndian.PutUint64(h[16:], uint64(x.seq))
		binary.LittleEndian.PutUint32(h[24:], x.n)
		h[28], h[29] = x.stream, x.class
		if _, e = w.Write(h[:]); e != nil {
			return e
		}
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
		if _, e = w.Write(buf); e != nil {
			return e
		}
	}
	if e = w.Flush(); e != nil {
		return e
	}
	if e = run.Close(); e != nil {
		return e
	}
	if e = s.f.Truncate(0); e != nil {
		return e
	}
	if _, e = s.f.Seek(0, io.SeekStart); e != nil {
		return e
	}
	s.w.Reset(s.f)
	s.chunkSize = 0
	s.index = s.index[:0]
	return nil
}

type runCursor struct {
	f   *os.File
	r   *bufio.Reader
	ref sortRef
}

func (c *runCursor) next() error {
	var h [30]byte
	if _, e := io.ReadFull(c.r, h[:]); e != nil {
		return e
	}
	c.ref = sortRef{pos: int64(binary.LittleEndian.Uint64(h[:])), fileOff: int64(binary.LittleEndian.Uint64(h[8:])), seq: int64(binary.LittleEndian.Uint64(h[16:])), n: binary.LittleEndian.Uint32(h[24:]), stream: h[28], class: h[29]}
	if c.ref.n > 16<<20 || c.ref.pos < 0 || c.ref.fileOff < 0 || c.ref.seq < 0 {
		return bad("sorted reference run")
	}
	return nil
}

type runHeap []*runCursor

func (h runHeap) Len() int { return len(h) }
func (h runHeap) Less(i, j int) bool {
	a, b := h[i].ref, h[j].ref
	return a.pos < b.pos || a.pos == b.pos && (a.fileOff < b.fileOff || a.fileOff == b.fileOff && a.seq < b.seq)
}
func (h runHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *runHeap) Push(x any)   { *h = append(*h, x.(*runCursor)) }
func (h *runHeap) Pop() any     { old := *h; x := old[len(old)-1]; *h = old[:len(old)-1]; return x }

func (s *SortedRefSpool) CopySorted(dst *RefSpool) error {
	if s.sealed {
		return bad("reference spool already sealed")
	}
	s.sealed = true
	if e := s.flushRun(); e != nil {
		return e
	}
	if len(s.runs) > 512 {
		return bad("reference run count")
	}
	var cursors []*runCursor
	defer func() {
		for _, c := range cursors {
			c.f.Close()
		}
	}()
	var heads runHeap
	for _, name := range s.runs {
		f, e := os.Open(name)
		if e != nil {
			return e
		}
		c := &runCursor{f: f, r: bufio.NewReaderSize(f, 4096)}
		cursors = append(cursors, c)
		if e = c.next(); e != nil {
			return e
		}
		heads = append(heads, c)
	}
	heap.Init(&heads)
	var buf []byte
	for heads.Len() > 0 {
		c := heads[0]
		x := c.ref
		if cap(buf) < int(x.n) {
			buf = make([]byte, x.n)
		} else {
			buf = buf[:x.n]
		}
		if _, e := io.ReadFull(c.r, buf); e != nil {
			return e
		}
		if e := dst.Append(x.pos, x.stream, x.class, buf); e != nil {
			return e
		}
		e := c.next()
		if e == io.EOF {
			heap.Pop(&heads)
		} else if e != nil {
			return e
		} else {
			heap.Fix(&heads, 0)
		}
	}
	s.index = nil
	return nil
}
