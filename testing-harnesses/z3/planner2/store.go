package planner2

import (
	"fmt"
	"sort"
)

type compRec struct {
	bytes int64
	refs  int
	owner int // first object that wrote the component
}

// Obj is one stored logical object. Primary reconstructs it; Side (optional) is
// an additive sidecar kept next to a CDC primary.
type Obj struct {
	ID, Lineage, Stage int
	Key                string
	Size               int64
	Day                int
	Live               bool // present in the namespace
	Attached           bool // closure currently referenced in the store
	Primary            Cand
	Side               *Cand
	Tier               string
	Reads, RangeBytes  int64 // observed reads (causal state)
	LastRead           int
}

// Store is the shared-component physical model: set-union reachability, real
// ownership, dead bytes that persist until GC, and a migration peak.
type Store struct {
	comp       map[CompID]*compRec
	objs       map[int]*Obj
	deps       map[int]int // fanout: stored objects whose Primary.Base == key
	live, dead int64
	Peak       int64
	Overhead   int64 // per newly created component (Z3-36: 64)
	Capacity   int64 // 0 = unlimited
}

func NewStore(overhead, capacity int64) *Store {
	return &Store{comp: map[CompID]*compRec{}, objs: map[int]*Obj{}, deps: map[int]int{}, Overhead: overhead, Capacity: capacity}
}

func (s *Store) Phys() int64 { return s.live + s.dead }
func (s *Store) Live() int64 { return s.live }
func (s *Store) Dead() int64 { return s.dead }

// Marginal is the exact extra physical bytes needed to add c now: components
// already referenced or still on disk (dead, not yet collected) are free.
func (s *Store) Marginal(c Cand) int64 {
	var n int64
	for _, r := range c.Comps {
		if _, ok := s.comp[r.ID]; !ok {
			n += r.Bytes + s.Overhead
		}
	}
	return n
}

// Check rejects a size mismatch with a previously seen component of the same id.
func (s *Store) Check(c Cand) error {
	if err := c.Validate(); err != nil {
		return err
	}
	for _, r := range c.Comps {
		if p, ok := s.comp[r.ID]; ok && p.bytes != r.Bytes {
			return fmt.Errorf("planner2: component %q size %d conflicts with stored %d", r.ID, r.Bytes, p.bytes)
		}
	}
	return nil
}

// Attach references c's closure for obj, creating missing components. It returns
// the components created (for abort) and bytes written. Capacity is enforced.
func (s *Store) Attach(obj int, c Cand) ([]CompID, int64, error) {
	if err := s.Check(c); err != nil {
		return nil, 0, err
	}
	need := s.Marginal(c)
	if s.Capacity > 0 && s.Phys()+need > s.Capacity {
		return nil, 0, fmt.Errorf("planner2: capacity exceeded (%d + %d > %d)", s.Phys(), need, s.Capacity)
	}
	var created []CompID
	for _, r := range c.Comps {
		p, ok := s.comp[r.ID]
		if !ok {
			p = &compRec{bytes: r.Bytes, owner: obj}
			s.comp[r.ID] = p
			s.live += r.Bytes + s.Overhead
			created = append(created, r.ID)
		} else if p.refs == 0 {
			s.dead -= p.bytes + s.Overhead
			s.live += p.bytes + s.Overhead
		}
		p.refs++
	}
	if c.Base >= 0 {
		s.deps[c.Base]++
	}
	if s.Phys() > s.Peak {
		s.Peak = s.Phys()
	}
	return created, need, nil
}

// Detach drops one reference per closure member; unreferenced components become
// dead bytes (not reclaimed until GC).
func (s *Store) Detach(c Cand) error {
	for _, r := range c.Comps {
		p, ok := s.comp[r.ID]
		if !ok || p.refs <= 0 {
			return fmt.Errorf("planner2: detach of unreferenced component %q", r.ID)
		}
	}
	for _, r := range c.Comps {
		p := s.comp[r.ID]
		p.refs--
		if p.refs == 0 {
			s.live -= p.bytes + s.Overhead
			s.dead += p.bytes + s.Overhead
		}
	}
	if c.Base >= 0 {
		s.deps[c.Base]--
	}
	return nil
}

// Collect reclaims dead components; efficiency<1 leaves a pinned fraction (the
// largest components stay, modelling pack pinning) until a later collection.
func (s *Store) Collect(efficiency float64) int64 {
	var ids []CompID
	for id, p := range s.comp {
		if p.refs == 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var freed int64
	budget := int64(float64(s.dead) * efficiency)
	for _, id := range ids {
		p := s.comp[id]
		sz := p.bytes + s.Overhead
		if efficiency < 1 && freed+sz > budget {
			continue
		}
		delete(s.comp, id)
		s.dead -= sz
		freed += sz
	}
	return freed
}

// AddObj registers a stored object. Obj must be new.
func (s *Store) AddObj(o *Obj) error {
	if _, dup := s.objs[o.ID]; dup {
		return fmt.Errorf("planner2: duplicate object %d", o.ID)
	}
	s.objs[o.ID] = o
	return nil
}

func (s *Store) Obj(id int) (*Obj, bool) { o, ok := s.objs[id]; return o, ok }
func (s *Store) Fanout(id int) int       { return s.deps[id] }

// ObjIDs returns stored object ids in ascending order (deterministic).
func (s *Store) ObjIDs() []int {
	ids := make([]int, 0, len(s.objs))
	for id := range s.objs {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	return ids
}

// PinnedDeadOwner bytes: components still referenced whose first owner left the
// namespace (dependency pins after deletion).
func (s *Store) PinnedBytes() int64 {
	var n int64
	for _, p := range s.comp {
		if p.refs > 0 {
			if o, ok := s.objs[p.owner]; ok && !o.Live {
				n += p.bytes + s.Overhead
			}
		}
	}
	return n
}

// Dependents counts stored live objects whose closure contains id's own components
// (blast radius of losing that object's data).
func (s *Store) BlastRadius(id int) int {
	o, ok := s.objs[id]
	if !ok {
		return 0
	}
	own := map[CompID]bool{}
	for _, r := range o.Primary.Comps {
		if p := s.comp[r.ID]; p != nil && p.owner == id {
			own[r.ID] = true
		}
	}
	n := 0
	for _, x := range s.objs {
		if !x.Live || x.ID == id {
			continue
		}
		for _, r := range x.Primary.Comps {
			if own[r.ID] {
				n++
				break
			}
		}
	}
	return n
}

// Validate cross-checks counters against the component table; used by tests.
func (s *Store) Validate() error {
	var live, dead int64
	refs := map[CompID]int{}
	for _, o := range s.objs {
		if !o.Attached {
			continue
		}
		for _, r := range o.Primary.Comps {
			refs[r.ID]++
		}
		if o.Side != nil {
			for _, r := range o.Side.Comps {
				refs[r.ID]++
			}
		}
	}
	for id, p := range s.comp {
		if p.bytes < 0 || p.refs < 0 {
			return fmt.Errorf("planner2: negative component state %q", id)
		}
		if p.refs > 0 {
			live += p.bytes + s.Overhead
		} else {
			dead += p.bytes + s.Overhead
		}
	}
	if live != s.live || dead != s.dead {
		return fmt.Errorf("planner2: byte counters live %d/%d dead %d/%d", s.live, live, s.dead, dead)
	}
	for id, p := range s.comp {
		if p.refs != refs[id] {
			return fmt.Errorf("planner2: component %q refs %d != attached closure refs %d", id, p.refs, refs[id])
		}
	}
	return nil
}

// Clone deep-copies the physical state for side-effect-free what-if evaluation.
func (s *Store) Clone() *Store {
	c := &Store{comp: make(map[CompID]*compRec, len(s.comp)), objs: make(map[int]*Obj, len(s.objs)), deps: make(map[int]int, len(s.deps)),
		live: s.live, dead: s.dead, Peak: s.Peak, Overhead: s.Overhead, Capacity: s.Capacity}
	for k, v := range s.comp {
		x := *v
		c.comp[k] = &x
	}
	for k, v := range s.objs {
		x := *v
		if v.Side != nil {
			sd := *v.Side
			x.Side = &sd
		}
		c.objs[k] = &x
	}
	for k, v := range s.deps {
		c.deps[k] = v
	}
	return c
}

// Readable reports whether every component of an attached object's closure is
// physically present and referenced (the logical object can be reconstructed).
func (s *Store) Readable(id int) bool {
	o, ok := s.objs[id]
	if !ok || !o.Attached {
		return false
	}
	for _, r := range o.Primary.Comps {
		if p := s.comp[r.ID]; p == nil || p.refs <= 0 || p.bytes != r.Bytes {
			return false
		}
	}
	return true
}

// ReadableAll audits all live objects; the first unreadable id is returned.
func (s *Store) ReadableAll() (int, bool) {
	for _, id := range s.ObjIDs() {
		if o := s.objs[id]; o.Live && !s.Readable(id) {
			return id, false
		}
	}
	return -1, true
}
