package repz3

import "fmt"

// DepRole classifies an object a representation needs to stay alive.
type DepRole uint8

const (
	DepRep    DepRole = iota + 1 // a representation object (the top one or a child)
	DepStored                    // a component the representation names (STORED member)
	DepVia                       // a component named only inside a stored component
	DepIndex                     // a native index component (lookup only, not needed to rebuild)
)

func (r DepRole) String() string {
	return [...]string{"?", "rep", "stored", "via", "index"}[min(r, 4)]
}

// Reach is the transitive SHA-256 dependency set of a representation.
type Reach struct {
	Role    map[SHA]DepRole
	Order   []SHA // first-seen order: deterministic for one source
	Derived int   // derived members traversed (not objects: their bytes are never stored)
}

func (r *Reach) add(id SHA, role DepRole, max int) error {
	if _, ok := r.Role[id]; ok {
		return nil
	}
	if len(r.Role) >= max {
		return fmt.Errorf("%w: more than %d objects", ErrLimit, max)
	}
	r.Role[id] = role
	r.Order = append(r.Order, id)
	return nil
}

// Count returns how many reachable objects have the role.
func (r *Reach) Count(role DepRole) (n int) {
	for _, v := range r.Role {
		if v == role {
			n++
		}
	}
	return n
}

// Reachable returns every object needed to rebuild representation id: the
// representation objects of the whole child tree, their stored members, the
// components their kinds name through stored components (VIA, found by the
// kind's Expand reading only stored components) and the native index components.
// It derives the set from serialized bytes alone, applies the same depth, cycle
// and size limits as reconstruction, and never writes output.
func (e *Env) Reachable(id SHA) (*Reach, error) {
	r := &Reach{Role: map[SHA]DepRole{}}
	if err := e.walk(r, id, 1, nil); err != nil {
		return nil, err
	}
	return r, nil
}

func (e *Env) walk(r *Reach, id SHA, depth int, path []SHA) error {
	max := e.Limits.norm().MaxObjects
	for _, p := range path {
		if p == id {
			return fmt.Errorf("%w: %x", ErrCycle, id[:6])
		}
	}
	if _, seen := r.Role[id]; seen {
		return nil
	}
	rep, err := e.load(id, depth, path)
	if err != nil {
		return err
	}
	if err := r.add(id, DepRep, max); err != nil {
		return err
	}
	c := e.call(id, rep, depth, path, nil)
	for i := range rep.Members {
		m := &rep.Members[i]
		if m.Role == Derived {
			r.Derived++
			if err := e.walk(r, m.Child, depth+1, c.path); err != nil {
				return fmt.Errorf("child %x: %w", m.Child[:6], err)
			}
			continue
		}
		if err := r.add(m.SHA, DepStored, max); err != nil {
			return err
		}
	}
	if rep.Kind != KindConcat {
		d, err := e.decoder(rep.Kind)
		if err != nil {
			return err
		}
		var addErr error
		if err := d.Expand(c, func(s SHA) {
			if addErr == nil {
				addErr = r.add(s, DepVia, max)
			}
		}); err != nil {
			return err
		}
		if addErr != nil {
			return addErr
		}
	}
	for _, x := range rep.Indexes {
		if err := r.add(x.Ref, DepIndex, max); err != nil {
			return err
		}
	}
	return nil
}

// Cost is the accounting of one persisted revision, derived from the objects that
// were actually put: nothing is estimated and nothing is charged twice.
type Cost struct {
	Objects                            int // new objects of every class
	PayloadObjs, RecipeObjs, IndexObjs int
	Payload                            int64 // new component bytes (stored and via)
	Recipe                             int64 // new representation objects (parent and children)
	Index                              int64 // new native index components
}

// Bytes is the persisted byte total.
func (c Cost) Bytes() int64 { return c.Payload + c.Recipe + c.Index }

// Total adds a physical overhead of perObject bytes to every newly stored object.
func (c Cost) Total(perObject int64) int64 { return c.Bytes() + perObject*int64(c.Objects) }

// Account classifies the objects a revision newly stored (added) by their role in
// the representation rooted at root. An added object that the representation does
// not reach is hidden storage and an error.
func (e *Env) Account(root SHA, added []SHA) (Cost, error) {
	r, err := e.Reachable(root)
	if err != nil {
		return Cost{}, err
	}
	var c Cost
	for _, id := range added {
		role, ok := r.Role[id]
		if !ok {
			return Cost{}, fmt.Errorf("repz3: %x was stored but is not reachable from the representation", id[:6])
		}
		sz, err := e.Src.Size(id)
		if err != nil {
			return Cost{}, err
		}
		c.Objects++
		switch role {
		case DepRep:
			c.RecipeObjs++
			c.Recipe += sz
		case DepIndex:
			c.IndexObjs++
			c.Index += sz
		default:
			c.PayloadObjs++
			c.Payload += sz
		}
	}
	return c, nil
}
