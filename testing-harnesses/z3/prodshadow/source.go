package prodshadow

import (
	"container/list"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"

	"zeros3-z3/repz3"
)

// emptySHA is the logical identity of a zero-length component. The production
// CAS cannot hold an empty record (pack records are 1..256 KiB), so the mapping
// gives it no physical object at all.
var emptySHA = sha256.Sum256(nil)

// Source is the production-backed repz3.Source. A logical object id resolves to
//
//	direct   one production chunk whose digest IS the id (size 1..MaxChunk), or
//	virtual  a Descriptor: an ordered list of ordinary production chunks.
//
// Reads go through the real casRead (so they cross the locator, loose files,
// packs, compression and tiers) and are bounded to the chunks the range needs.
type Source struct {
	br *Bridge

	mu    sync.Mutex
	descs map[[32]byte]*Descriptor
	sizes map[[32]byte]int64
	lru   *list.List // chunk cache, most recent first
	cache map[[32]byte]*list.Element
	cap   int
	trace map[[32]byte]int64 // chunk -> bytes served from it (cache or store)

	ReadAts      atomic.Int64 // repz3 ReadAt calls
	ReqBytes     atomic.Int64 // bytes the decoder asked for
	ChunkFetches atomic.Int64 // production chunk reads (cache misses)
	FetchBytes   atomic.Int64 // logical bytes of those chunks
	CacheHits    atomic.Int64
}

type cached struct {
	sha  [32]byte
	data []byte
}

// NewSource returns a Source over br with a small verified-chunk cache (chunks).
func NewSource(br *Bridge, cacheChunks int) *Source {
	return &Source{br: br, descs: map[[32]byte]*Descriptor{}, sizes: map[[32]byte]int64{}, lru: list.New(),
		cache: map[[32]byte]*list.Element{}, cap: cacheChunks, trace: map[[32]byte]int64{}}
}

// AddDescriptors registers virtual-component descriptors (from a published or in-flight root).
func (s *Source) AddDescriptors(m map[[32]byte]*Descriptor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, d := range m {
		s.descs[id] = d
	}
}

// Reset drops descriptors, sizes and cached chunks (a "restart" of the shadow layer).
func (s *Source) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.descs = map[[32]byte]*Descriptor{}
	s.sizes = map[[32]byte]int64{}
	s.lru.Init()
	s.cache = map[[32]byte]*list.Element{}
	s.trace = map[[32]byte]int64{}
}

// ResetStats zeroes the read accounting and the touched-chunk trace.
func (s *Source) ResetStats() {
	s.mu.Lock()
	s.trace = map[[32]byte]int64{}
	s.mu.Unlock()
	s.ReadAts.Store(0)
	s.ReqBytes.Store(0)
	s.ChunkFetches.Store(0)
	s.FetchBytes.Store(0)
	s.CacheHits.Store(0)
}

// Touched returns the production chunks the reads since ResetStats needed.
func (s *Source) Touched() [][32]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][32]byte, 0, len(s.trace))
	for id := range s.trace {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i][:]) < string(out[j][:]) })
	return out
}

func (s *Source) desc(id [32]byte) *Descriptor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.descs[id]
}

// Size is cheap: a descriptor lookup or one CAS stat; it reads no payload.
func (s *Source) Size(id repz3.SHA) (int64, error) {
	if id == emptySHA {
		return 0, nil
	}
	if d := s.desc(id); d != nil {
		return d.Size, nil
	}
	s.mu.Lock()
	n, ok := s.sizes[id]
	s.mu.Unlock()
	if ok {
		return n, nil
	}
	w, err := s.br.Stat(id)
	if errors.Is(err, ErrNotFound) {
		return 0, fmt.Errorf("%w: %x", repz3.ErrMissing, id[:6])
	}
	if err != nil {
		return 0, err
	}
	if w.Size < 1 || w.Size > MaxChunk {
		return 0, fmt.Errorf("%w: direct object %x has size %d", repz3.ErrMismatch, id[:6], w.Size)
	}
	s.mu.Lock()
	s.sizes[id] = w.Size
	s.mu.Unlock()
	return w.Size, nil
}

// chunk returns one verified production chunk (production casRead verifies the digest).
func (s *Source) chunk(sha [32]byte, want int64) ([]byte, error) {
	s.mu.Lock()
	if e, ok := s.cache[sha]; ok {
		s.lru.MoveToFront(e)
		data := e.Value.(*cached).data
		s.mu.Unlock()
		s.CacheHits.Add(1)
		return data, nil
	}
	s.mu.Unlock()
	data, err := s.br.Read(sha)
	if errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("%w: chunk %x", repz3.ErrMissing, sha[:6])
	}
	if errors.Is(err, ErrCorrupt) {
		return nil, fmt.Errorf("%w: %v", repz3.ErrMismatch, err)
	}
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != want {
		return nil, fmt.Errorf("%w: chunk %x is %d bytes, descriptor says %d", repz3.ErrMismatch, sha[:6], len(data), want)
	}
	s.ChunkFetches.Add(1)
	s.FetchBytes.Add(int64(len(data)))
	s.mu.Lock()
	if s.cap > 0 {
		if _, ok := s.cache[sha]; !ok {
			s.cache[sha] = s.lru.PushFront(&cached{sha, data})
			for s.lru.Len() > s.cap {
				old := s.lru.Back()
				s.lru.Remove(old)
				delete(s.cache, old.Value.(*cached).sha)
			}
		}
	}
	s.mu.Unlock()
	return data, nil
}

func (s *Source) touch(sha [32]byte, n int64) {
	s.mu.Lock()
	s.trace[sha] += n
	s.mu.Unlock()
}

// ReadAt has io.ReaderAt semantics for one logical object.
func (s *Source) ReadAt(id repz3.SHA, p []byte, off int64) (int, error) {
	s.ReadAts.Add(1)
	s.ReqBytes.Add(int64(len(p)))
	size, err := s.Size(id)
	if err != nil {
		return 0, err
	}
	if off < 0 || off > size {
		return 0, fmt.Errorf("prodshadow: offset %d outside %x", off, id[:6])
	}
	want := int64(len(p))
	if off+want > size {
		want = size - off
	}
	if want == 0 {
		return 0, shortEOF(want, len(p))
	}
	d := s.desc(id)
	if d == nil { // direct: the chunk is the object
		data, err := s.chunk(id, size)
		if err != nil {
			return 0, err
		}
		s.touch(id, want)
		copy(p, data[off:off+want])
		return int(want), shortEOF(want, len(p))
	}
	offs := d.offsets()
	i := sort.Search(len(d.Refs), func(i int) bool { return offs[i+1] > off })
	var done int64
	for done < want && i < len(d.Refs) {
		r := d.Refs[i]
		data, err := s.chunk(r.SHA, int64(r.Len))
		if err != nil {
			return int(done), err
		}
		from := off + done - offs[i]
		n := copy(p[done:want], data[from:])
		s.touch(r.SHA, int64(n))
		done += int64(n)
		i++
	}
	return int(done), shortEOF(done, len(p))
}

func shortEOF(n int64, plen int) error {
	if n < int64(plen) {
		return io.EOF
	}
	return nil
}

// VerifyComponent reads every chunk of a logical object and checks its length and
// SHA-256: the integrity route behind range reads. Reordered, substituted (same
// length), truncated or missing chunks all fail here.
func (s *Source) VerifyComponent(id repz3.SHA) error {
	size, err := s.Size(id)
	if err != nil {
		return err
	}
	if size == 0 {
		return nil
	}
	h := sha256.New()
	var buf [MaxChunk]byte
	for off := int64(0); off < size; {
		n := int64(len(buf))
		if size-off < n {
			n = size - off
		}
		got, err := s.ReadAt(id, buf[:n], off)
		if int64(got) != n {
			return fmt.Errorf("prodshadow: verify %x: %d of %d at %d: %v", id[:6], got, n, off, err)
		}
		h.Write(buf[:n])
		off += n
	}
	var sum [32]byte
	h.Sum(sum[:0])
	if sum != id {
		return fmt.Errorf("%w: %x reassembles to a different digest", repz3.ErrMismatch, id[:6])
	}
	return nil
}

// Chunks lists the production chunks a logical object depends on, in order.
func (s *Source) Chunks(id repz3.SHA) [][32]byte {
	if id == emptySHA {
		return nil
	}
	if d := s.desc(id); d != nil {
		out := make([][32]byte, len(d.Refs))
		for i, r := range d.Refs {
			out[i] = r.SHA
		}
		return out
	}
	return [][32]byte{id}
}

// AllDescs collects the descriptors of a set of roots.
func (s *Source) AllDescs(roots []*Root) map[[32]byte]*Descriptor {
	out := map[[32]byte]*Descriptor{}
	for _, r := range roots {
		for id, d := range r.Descs {
			out[id] = d
		}
	}
	return out
}
