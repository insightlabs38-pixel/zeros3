// Package epoch is an isolated model of online pack publication.
package epoch

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

type digest [32]byte

type location struct {
	pack *pack
	off  int64
	size int
}

type pack struct {
	path   string
	leases int
}

type store struct {
	dir    string
	serial atomic.Uint64
	mu     sync.Mutex
	roots  map[string]digest
	locs   map[digest]location
	retire map[*pack]bool
	repack *plan
}

type lease struct {
	s    *store
	root digest
	loc  location
}

type plan struct {
	s     *store
	marks map[digest]location
	pins  map[*pack]bool
}

func newStore(dir string) *store {
	return &store{
		dir: dir, roots: make(map[string]digest), locs: make(map[digest]location), retire: make(map[*pack]bool),
	}
}

// Pack bytes are immutable after publication. A locator is exposed only
// after the complete file and its directory entry have been synced.
func (s *store) createPack(contents map[digest][]byte) (map[digest]location, error) {
	n := s.serial.Add(1)
	destination := filepath.Join(s.dir, fmt.Sprintf("pack-%d", n))
	f, err := os.CreateTemp(s.dir, "staged-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	p := &pack{path: destination}
	out := make(map[digest]location, len(contents))
	var offset int64
	for sum, payload := range contents {
		if sha256.Sum256(payload) != sum {
			return nil, errors.New("pack payload does not match its locator")
		}
		if _, err := f.Write(payload); err != nil {
			return nil, err
		}
		out[sum] = location{pack: p, off: offset, size: len(payload)}
		offset += int64(len(payload))
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(f.Name(), destination); err != nil {
		return nil, err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *store) put(key string, data []byte) error {
	sum := sha256.Sum256(data)
	s.mu.Lock()
	if _, exists := s.locs[sum]; exists {
		s.roots[key] = sum
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	written, err := s.createPack(map[digest][]byte{sum: data})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.locs[sum]; exists {
		s.retire[written[sum].pack] = true
	} else {
		s.locs[sum] = written[sum]
	}
	s.roots[key] = sum
	return s.reclaimLocked()
}

func (s *store) delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.roots, key)
}

func (s *store) acquire(key string) (*lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, exists := s.roots[key]
	if !exists {
		return nil, os.ErrNotExist
	}
	loc, exists := s.locs[root]
	if !exists {
		return nil, errors.New("live root lost its locator")
	}
	loc.pack.leases++
	return &lease{s: s, root: root, loc: loc}, nil
}

func readLocation(loc location, sum digest) ([]byte, error) {
	f, err := os.Open(loc.pack.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make([]byte, loc.size)
	if _, err := io.ReadFull(io.NewSectionReader(f, loc.off, int64(loc.size)), data); err != nil {
		return nil, err
	}
	if sha256.Sum256(data) != sum {
		return nil, errors.New("read returned incorrect bytes")
	}
	return data, nil
}

func (l *lease) read() ([]byte, error) { return readLocation(l.loc, l.root) }

func (l *lease) release() error {
	l.s.mu.Lock()
	defer l.s.mu.Unlock()
	l.loc.pack.leases--
	return l.s.reclaimLocked()
}

func (s *store) begin() (*plan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repack != nil {
		return nil, errors.New("another maintenance plan is active")
	}
	p := &plan{s: s, marks: make(map[digest]location), pins: make(map[*pack]bool)}
	for _, root := range s.roots {
		loc, exists := s.locs[root]
		if !exists {
			return nil, errors.New("live root lost its locator")
		}
		p.marks[root] = loc
		p.pins[loc.pack] = true
	}
	for old := range p.pins {
		old.leases++
	}
	s.repack = p
	return p, nil
}

func (p *plan) stage() (map[digest]location, error) {
	contents := make(map[digest][]byte, len(p.marks))
	for root, old := range p.marks {
		data, err := readLocation(old, root)
		if err != nil {
			return nil, err
		}
		contents[root] = data
	}
	if len(contents) == 0 {
		return nil, nil
	}
	return p.s.createPack(contents)
}

// Reconciliation takes the live roots again. A write the initial mark missed
// retains its durable source pack if the candidate did not copy its digest.
func (p *plan) publish(rebuilt map[digest]location) error {
	s := p.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repack != p {
		return errors.New("maintenance plan is no longer current")
	}
	defer p.releaseLocked()
	next := make(map[digest]location, len(s.roots))
	retained := make(map[*pack]bool)
	for _, root := range s.roots {
		loc, ok := rebuilt[root]
		if !ok {
			loc, ok = s.locs[root]
		}
		if !ok {
			return errors.New("reconciliation would drop live data")
		}
		next[root], retained[loc.pack] = loc, true
	}
	for _, previous := range []map[digest]location{s.locs, rebuilt} {
		for _, l := range previous {
			if !retained[l.pack] {
				s.retire[l.pack] = true
			}
		}
	}
	s.locs = next
	// The caller's release of the mark pins below may make packs reclaimable.
	return nil
}

func (p *plan) releaseLocked() {
	for x := range p.pins {
		x.leases--
	}
	p.s.repack = nil
}

func (p *plan) abort() {
	p.s.mu.Lock()
	defer p.s.mu.Unlock()
	if p.s.repack == p {
		p.releaseLocked()
	}
}

func (s *store) reclaim() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reclaimLocked()
}

func (s *store) reclaimLocked() error {
	for p := range s.retire {
		if p.leases == 0 {
			if err := os.Remove(p.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			delete(s.retire, p)
		}
	}
	return nil
}
