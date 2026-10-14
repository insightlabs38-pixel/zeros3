package merklez3

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// DiskStore is a read-only store whose resident memory does not depend on the
// node count: a node log plus a sorted fixed-width index searched with ReadAt.
// It exists to show that the traversals above need no resident graph.
type DiskStore struct {
	log, idx *os.File
	idLen    int
	rec      int
	n        int
	Decode   func([]byte) ([]ID, error)
	Reads    int64 // log reads, for accounting
}

// DiskBuilder collects (id, offset, length) records and writes the node log as
// nodes arrive; Finish sorts the records and writes the index.
type DiskBuilder struct {
	dir   string
	log   *os.File
	w     *bufioWriter
	off   uint64
	recs  []byte
	idLen int
	n     int
}

type bufioWriter struct {
	f   *os.File
	buf []byte
}

func (b *bufioWriter) Write(p []byte) {
	b.buf = append(b.buf, p...)
	if len(b.buf) > 1<<20 {
		b.Flush()
	}
}

func (b *bufioWriter) Flush() {
	if len(b.buf) > 0 {
		b.f.Write(b.buf)
		b.buf = b.buf[:0]
	}
}

func NewDiskBuilder(dir string, idLen int) (*DiskBuilder, error) {
	f, err := os.Create(filepath.Join(dir, "nodes.log"))
	if err != nil {
		return nil, err
	}
	return &DiskBuilder{dir: dir, log: f, w: &bufioWriter{f: f}, idLen: idLen}, nil
}

// Put appends a node; duplicate identities are stored once (last write wins is
// not needed: equal identity means equal bytes).
func (b *DiskBuilder) Put(n Node) error {
	if len(n.ID) != b.idLen {
		return fmt.Errorf("merklez3: id length %d, store is %d", len(n.ID), b.idLen)
	}
	b.recs = append(b.recs, n.ID...)
	b.recs = binary.LittleEndian.AppendUint64(b.recs, b.off)
	b.recs = binary.LittleEndian.AppendUint32(b.recs, uint32(len(n.Payload)))
	b.w.Write(n.Payload)
	b.off += uint64(len(n.Payload))
	b.n++
	return nil
}

func (b *DiskBuilder) Finish(decode func([]byte) ([]ID, error)) (*DiskStore, error) {
	b.w.Flush()
	rec := b.idLen + 12
	idx := make([]int, b.n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(i, j int) bool {
		return bytes.Compare(b.recs[idx[i]*rec:idx[i]*rec+b.idLen], b.recs[idx[j]*rec:idx[j]*rec+b.idLen]) < 0
	})
	f, err := os.Create(filepath.Join(b.dir, "nodes.idx"))
	if err != nil {
		return nil, err
	}
	var out []byte
	var last []byte
	n := 0
	for _, i := range idx {
		r := b.recs[i*rec : (i+1)*rec]
		if last != nil && bytes.Equal(last, r[:b.idLen]) {
			continue
		}
		last = r[:b.idLen]
		out = append(out, r...)
		n++
		if len(out) > 1<<20 {
			f.Write(out)
			out = out[:0]
		}
	}
	f.Write(out)
	f.Close()
	b.recs = nil
	b.log.Close()
	return OpenDiskStore(b.dir, b.idLen, n, decode)
}

func OpenDiskStore(dir string, idLen, n int, decode func([]byte) ([]ID, error)) (*DiskStore, error) {
	lg, err := os.Open(filepath.Join(dir, "nodes.log"))
	if err != nil {
		return nil, err
	}
	ix, err := os.Open(filepath.Join(dir, "nodes.idx"))
	if err != nil {
		return nil, err
	}
	return &DiskStore{log: lg, idx: ix, idLen: idLen, rec: idLen + 12, n: n, Decode: decode}, nil
}

func (s *DiskStore) Len() int { return s.n }

func (s *DiskStore) Close() { s.log.Close(); s.idx.Close() }

func (s *DiskStore) lookup(id ID) (off uint64, ln uint32, ok bool) {
	if len(id) != s.idLen {
		return 0, 0, false
	}
	lo, hi := 0, s.n
	buf := make([]byte, s.rec)
	for lo < hi {
		mid := (lo + hi) / 2
		if _, err := s.idx.ReadAt(buf, int64(mid)*int64(s.rec)); err != nil && err != io.EOF {
			return 0, 0, false
		}
		switch c := bytes.Compare(buf[:s.idLen], []byte(id)); {
		case c == 0:
			return binary.LittleEndian.Uint64(buf[s.idLen:]), binary.LittleEndian.Uint32(buf[s.idLen+8:]), true
		case c < 0:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return 0, 0, false
}

// Has reports membership without reading the node.
func (s *DiskStore) Has(id ID) bool { _, _, ok := s.lookup(id); return ok }

func (s *DiskStore) Get(id ID) (Node, error) {
	off, ln, ok := s.lookup(id)
	if !ok {
		return Node{}, fmt.Errorf("%w: %x", ErrMissing, string(id[:min(len(id), 6)]))
	}
	p := make([]byte, ln)
	if _, err := s.log.ReadAt(p, int64(off)); err != nil && err != io.EOF {
		return Node{}, err
	}
	s.Reads++
	kids, err := s.Decode(p)
	if err != nil {
		return Node{}, err
	}
	return Node{ID: id, Payload: p, Kids: kids}, nil
}
