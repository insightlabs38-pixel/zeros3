package dcore

import (
	"encoding/binary"
	"hash/crc32"
	"io"
	"sort"
	"sync"
)

// RunStore persists immutable runs. Put must make the run durable before it
// returns the reader (a filesystem implementation writes, fsyncs and renames).
type RunStore interface {
	Put(gen uint64, write func(io.Writer) error) (io.ReaderAt, int64, error)
	Drop(gen uint64) error
}

// MaxPostings bounds the ids retained per feature (newest win), as in B1.
const MaxPostings = 64

type openRun struct {
	run *Run
}

// RunSet is an LSM-style similarity index: a bounded in-memory table plus
// immutable sorted runs, tombstones for deleted ids and compaction. It is
// derived state: Query returns candidates only; callers verify content.
type RunSet struct {
	mu       sync.RWMutex
	store    RunStore
	mem      map[uint64][]uint32
	memCount int
	MemLimit int
	runs     []openRun // newest first
	dead     map[uint32]struct{}
	nextGen  uint64
	Stats    RunSetStats
}

// RunSetStats are monotonically increasing work counters.
type RunSetStats struct {
	Flushes, Compactions, Lookups, RunReads, Corrupt int64
}

func NewRunSet(store RunStore, memLimit int) *RunSet {
	return &RunSet{store: store, mem: map[uint64][]uint32{}, MemLimit: memLimit, dead: map[uint32]struct{}{}, nextGen: 1}
}

// Add records the features of object id. ids must increase over time: the
// retention policy treats larger ids as newer.
func (s *RunSet) Add(id uint32, features []uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[uint64]struct{}{}
	for _, f := range features {
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		p := s.mem[f]
		if n := len(p); n > 0 && p[n-1] == id {
			continue
		}
		if len(p) == MaxPostings {
			copy(p, p[1:])
			p = p[:MaxPostings-1]
		}
		s.mem[f] = append(p, id)
		s.memCount++
	}
	if s.memCount >= s.MemLimit {
		return s.flushLocked()
	}
	return nil
}

// Delete tombstones an id immediately; space is reclaimed by Compact.
func (s *RunSet) Delete(id uint32) {
	s.mu.Lock()
	s.dead[id] = struct{}{}
	s.mu.Unlock()
}

func (s *RunSet) Flush() error { s.mu.Lock(); defer s.mu.Unlock(); return s.flushLocked() }

func (s *RunSet) flushLocked() error {
	if len(s.mem) == 0 {
		return nil
	}
	ps := make([]Posting, 0, s.memCount)
	for f, ids := range s.mem {
		for _, id := range ids {
			ps = append(ps, Posting{f, id})
		}
	}
	ps = SortBuckets(ps, 4)
	gen := s.nextGen
	rd, size, err := s.store.Put(gen, func(w io.Writer) error { _, e := WriteRun(w, ps, gen); return e })
	if err != nil {
		return err
	}
	r, err := OpenRun(rd, size, false)
	if err != nil {
		return err
	}
	s.nextGen++
	s.runs = append([]openRun{{r}}, s.runs...)
	s.mem = map[uint64][]uint32{}
	s.memCount = 0
	s.Stats.Flushes++
	return nil
}

// Candidates for one feature: newest MaxPostings live ids across mem and runs.
func (s *RunSet) lookup(f uint64) []uint32 {
	var ids []uint32
	ids = append(ids, s.mem[f]...)
	for _, r := range s.runs {
		got, err := r.run.Lookup(f, MaxPostings)
		s.Stats.RunReads++
		if err != nil {
			s.Stats.Corrupt++ // a bad run lowers recall only; it is never trusted
			continue
		}
		ids = append(ids, got...)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := ids[:0]
	var last uint32
	for i, id := range ids {
		if i > 0 && id == last {
			continue
		}
		last = id
		if _, d := s.dead[id]; !d {
			out = append(out, id)
		}
	}
	if len(out) > MaxPostings {
		out = out[len(out)-MaxPostings:]
	}
	return out
}

// Query ranks candidates by shared features (hits desc, id desc) like B1.
func (s *RunSet) Query(features []uint64, k int) []Candidate2 {
	s.mu.Lock() // counters
	defer s.mu.Unlock()
	if k <= 0 || k > 64 || len(features) > 64 {
		return nil
	}
	scores := map[uint32]int{}
	seen := map[uint64]struct{}{}
	for _, f := range features {
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		s.Stats.Lookups++
		for _, id := range s.lookup(f) {
			scores[id]++
		}
	}
	out := make([]Candidate2, 0, len(scores))
	for id, n := range scores {
		out = append(out, Candidate2{id, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

// Candidate2 is a ranked similarity candidate.
type Candidate2 struct {
	ID   uint32
	Hits int
}

// Compact merges every run into one, dropping tombstoned ids and keeping the
// newest MaxPostings ids per feature, then drops the inputs. The output run is
// durable and open before any input is dropped; a failure leaves all inputs.
func (s *RunSet) Compact() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.flushLocked(); err != nil {
		return err
	}
	if len(s.runs) < 2 && len(s.dead) == 0 {
		return nil
	}
	stream := func() func() (Posting, bool) {
		iters := make([]*RunIter, len(s.runs))
		ok := make([]bool, len(s.runs))
		for i, r := range s.runs {
			iters[i] = r.run.Iter()
			ok[i] = iters[i].Next()
		}
		var window []uint32 // ids of the current feature, ascending, deduped
		var curF uint64
		var emitted int
		var pending []Posting
		var pi int
		gather := func() bool {
			// find smallest feature across heads
			best := -1
			for i := range iters {
				if ok[i] && (best < 0 || iters[i].Cur.Feature < iters[best].Cur.Feature) {
					best = i
				}
			}
			if best < 0 {
				return false
			}
			curF = iters[best].Cur.Feature
			window = window[:0]
			for i := range iters {
				for ok[i] && iters[i].Cur.Feature == curF {
					window = append(window, iters[i].Cur.ID)
					ok[i] = iters[i].Next()
				}
			}
			sort.Slice(window, func(a, b int) bool { return window[a] < window[b] })
			live := window[:0]
			for j, id := range window {
				if j > 0 && id == window[j-1] {
					continue
				}
				if _, d := s.dead[id]; !d {
					live = append(live, id)
				}
			}
			if len(live) > MaxPostings {
				live = live[len(live)-MaxPostings:]
			}
			pending = pending[:0]
			for _, id := range live {
				pending = append(pending, Posting{curF, id})
			}
			pi, emitted = 0, emitted+len(pending)
			return true
		}
		return func() (Posting, bool) {
			for pi >= len(pending) {
				if !gather() {
					return Posting{}, false
				}
			}
			pi++
			return pending[pi-1], true
		}
	}
	// pass 1: count and per-bucket directory
	var counts [65536]uint32
	var total int64
	next := stream()
	for {
		p, ok := next()
		if !ok {
			break
		}
		counts[p.Feature>>48]++
		total++
	}
	gen := s.nextGen
	next = stream()
	rd, size, err := s.store.Put(gen, func(w io.Writer) error {
		_, e := WriteRunStream(w, total, &counts, gen, next)
		return e
	})
	if err != nil {
		return err
	}
	nr, err := OpenRun(rd, size, true)
	if err != nil {
		_ = s.store.Drop(gen)
		return err
	}
	old := s.runs
	s.nextGen++
	s.runs = []openRun{{nr}}
	for _, o := range old {
		_ = s.store.Drop(o.run.Gen)
	}
	s.dead = map[uint32]struct{}{}
	s.Stats.Compactions++
	return nil
}

// Tombstone list ("ZST1"): magic | count u32 | ids u32... | crc32c.
func EncodeTombstones(ids []uint32) []byte {
	b := []byte("ZST1")
	b = binary.LittleEndian.AppendUint32(b, uint32(len(ids)))
	for _, id := range ids {
		b = binary.LittleEndian.AppendUint32(b, id)
	}
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, crcTab))
}

func DecodeTombstones(b []byte) ([]uint32, error) {
	if len(b) < 12 || string(b[:4]) != "ZST1" {
		return nil, ErrRunCorrupt
	}
	n := binary.LittleEndian.Uint32(b[4:])
	if uint64(len(b)) != 12+4*uint64(n) || crc32.Checksum(b[:len(b)-4], crcTab) != binary.LittleEndian.Uint32(b[len(b)-4:]) {
		return nil, ErrRunCorrupt
	}
	ids := make([]uint32, n)
	for i := range ids {
		ids[i] = binary.LittleEndian.Uint32(b[8+4*i:])
	}
	return ids, nil
}

// Runs returns the number of immutable runs (for tests and reports).
func (s *RunSet) Runs() int { s.mu.RLock(); defer s.mu.RUnlock(); return len(s.runs) }
