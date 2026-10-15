package repz3

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"
)

// Source serves SHA-256-addressed objects (components, representation objects and
// index components) by bounded range. It is the only access a decoder has to
// stored bytes. Size must be cheap and must not read the object; ReadAt has
// io.ReaderAt semantics for the one object (a short read returns an error).
type Source interface {
	Size(id SHA) (int64, error)
	ReadAt(id SHA, p []byte, off int64) (int, error)
}

// NativeResolver is the optional global native-identity lookup a Source may offer
// (the production native-ID index would implement it). A representation's own
// index sections are consulted first.
type NativeResolver interface {
	Resolve(native []byte) (SHA, bool)
}

func readFull(src Source, id SHA, p []byte, off int64) error {
	n, err := src.ReadAt(id, p, off)
	if n == len(p) {
		return nil
	}
	if err != nil && err != io.EOF {
		return fmt.Errorf("repz3: reading %x: %w", id[:6], err)
	}
	return fmt.Errorf("%w: %x returned %d of %d bytes at %d", ErrShort, id[:6], n, len(p), off)
}

// ReadObject returns the whole object after checking its length against the
// store, an optional declared length (-1 = none) and max, before allocating. The
// bytes are hashed against id.
func ReadObject(src Source, id SHA, declared, max int64) ([]byte, error) {
	size, err := src.Size(id)
	if err != nil {
		return nil, err
	}
	if size < 0 || (declared >= 0 && size != declared) {
		return nil, fmt.Errorf("%w: %x is %d bytes, recipe says %d", ErrMismatch, id[:6], size, declared)
	}
	if size > max {
		return nil, fmt.Errorf("%w: %x is %d bytes (cap %d)", ErrLimit, id[:6], size, max)
	}
	b := make([]byte, size)
	if err := readFull(src, id, b, 0); err != nil {
		return nil, err
	}
	if sha256.Sum256(b) != id {
		return nil, fmt.Errorf("%w: %x bytes do not hash to their address", ErrMismatch, id[:6])
	}
	return b, nil
}

// MemStore is the laboratory source: a map with read accounting, a native index
// and hooks to damage it. It is not a store implementation.
type MemStore struct {
	objs    map[SHA][]byte
	nat     map[string]SHA
	touched map[SHA]int64
	reads   int
}

func NewMemStore() *MemStore {
	return &MemStore{objs: map[SHA][]byte{}, nat: map[string]SHA{}, touched: map[SHA]int64{}}
}

// Put stores b under its SHA-256 and reports whether it was new.
func (s *MemStore) Put(b []byte) (SHA, bool) {
	id := sha256.Sum256(b)
	if _, ok := s.objs[id]; ok {
		return id, false
	}
	s.objs[id] = append([]byte(nil), b...)
	return id, true
}

// Set stores b under an arbitrary id (tests: corrupt or substituted objects).
func (s *MemStore) Set(id SHA, b []byte) { s.objs[id] = append([]byte(nil), b...) }

func (s *MemStore) Has(id SHA) bool { _, ok := s.objs[id]; return ok }
func (s *MemStore) Delete(id SHA)   { delete(s.objs, id) }
func (s *MemStore) Len() int        { return len(s.objs) }

// Bytes returns the stored bytes without accounting (tests).
func (s *MemStore) Bytes(id SHA) []byte { return s.objs[id] }

func (s *MemStore) Size(id SHA) (int64, error) {
	b, ok := s.objs[id]
	if !ok {
		return 0, fmt.Errorf("%w: %x", ErrMissing, id[:6])
	}
	return int64(len(b)), nil
}

func (s *MemStore) ReadAt(id SHA, p []byte, off int64) (int, error) {
	b, ok := s.objs[id]
	if !ok {
		return 0, fmt.Errorf("%w: %x", ErrMissing, id[:6])
	}
	if off < 0 || off > int64(len(b)) {
		return 0, fmt.Errorf("repz3: offset %d outside %x", off, id[:6])
	}
	n := copy(p, b[off:])
	s.reads++
	s.touched[id] += int64(n)
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// RegisterNative records native -> id in the global native index.
func (s *MemStore) RegisterNative(native []byte, id SHA) { s.nat[string(native)] = id }

func (s *MemStore) Resolve(native []byte) (SHA, bool) { id, ok := s.nat[string(native)]; return id, ok }

// ResetReads clears the read accounting.
func (s *MemStore) ResetReads() { s.reads = 0; s.touched = map[SHA]int64{} }

// Touched returns the objects read since ResetReads, with the bytes read from each.
func (s *MemStore) Touched() (reads int, bytes map[SHA]int64) {
	out := make(map[SHA]int64, len(s.touched))
	for k, v := range s.touched {
		out[k] = v
	}
	return s.reads, out
}

// IngestNative registers the top-level native index sections of the stored
// representation id in the global native index (what a real store would do when
// the representation is put).
func (s *MemStore) IngestNative(id SHA) error {
	e := &Env{Src: s}
	rep, err := e.LoadRep(id)
	if err != nil {
		return err
	}
	for _, x := range rep.Indexes {
		if sz, err := s.Size(x.Ref); err != nil || sz != x.Bytes() {
			return fmt.Errorf("%w: index component is %d bytes, recipe says %d (%v)", ErrMismatch, sz, x.Bytes(), err)
		}
		recs := make([]byte, x.Bytes())
		if err := readFull(s, x.Ref, recs, 0); err != nil {
			return err
		}
		w := x.KeyWidth + 32
		for i := int64(0); i < x.Count; i++ {
			r := recs[i*int64(w) : (i+1)*int64(w)]
			var v SHA
			copy(v[:], r[x.KeyWidth:])
			s.RegisterNative(r[:x.KeyWidth], v)
		}
	}
	return nil
}

// BuildIndex serialises native index records (key -> component SHA) sorted by key.
// All keys must have width w; duplicate keys with different values are an error.
func BuildIndex(w int, pairs map[string]SHA) ([]byte, int64, error) {
	keys := make([]string, 0, len(pairs))
	for k := range pairs {
		if len(k) != w {
			return nil, 0, fmt.Errorf("repz3: native key of %d bytes in a %d-byte index", len(k), w)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]byte, 0, len(keys)*(w+32))
	for _, k := range keys {
		v := pairs[k]
		out = append(out, k...)
		out = append(out, v[:]...)
	}
	return out, int64(len(keys)), nil
}

// Clone returns a store that shares the (immutable) object bytes: a scratch copy
// for dry-run costing.
func (s *MemStore) Clone() *MemStore {
	c := NewMemStore()
	for k, v := range s.objs {
		c.objs[k] = v
	}
	for k, v := range s.nat {
		c.nat[k] = v
	}
	return c
}

// IDs lists the stored objects (tests and reports).
func (s *MemStore) IDs() []SHA {
	out := make([]SHA, 0, len(s.objs))
	for k := range s.objs {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
	return out
}
