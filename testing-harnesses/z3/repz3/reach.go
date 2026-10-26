package repz3

import "fmt"

// DepRole classifies an object a representation needs to stay alive.
type DepRole uint8

const (
	DepRep    DepRole = iota + 1 // a representation object (the top one or a child)
	DepTable                     // a STORED member of a format kind: a structural table or node, not payload
	DepStored                    // a STORED member of a concat representation: payload
	DepVia                       // a component named only inside a stored component (the kind's Expand)
	DepOwned                     // a record target kept alive by an owning section
	DepIndex                     // a record section component (native index)
)

func (r DepRole) String() string {
	return [...]string{"?", "rep", "table", "stored", "via", "owned", "index"}[min(r, 6)]
}

// Reach is the transitive SHA-256 dependency set of a representation.
type Reach struct {
	Role    map[SHA]DepRole
	Order   []SHA // first-seen order: deterministic for one source
	Derived int   // derived members traversed (not objects: their bytes are never stored)
	// Height is, per representation, the nesting depth of its own subtree (a representation
	// with no derived child is 1). A representation shared by several parents is walked
	// once, so each further use is checked against this instead of being re-walked.
	Height map[SHA]int
	// Via lists, per representation, the components its kind's Expand named (what an
	// explicit dependency sidecar would have to hold); recorded for the policy study.
	Via map[SHA][]SHA
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
// remaining components (VIA) and the record sections. VIA is the targets of the
// representation's owning sections when it has any, otherwise whatever the
// kind's Expand finds by reading stored components. It derives the set from
// serialized bytes alone, applies the same depth, cycle and size limits as
// reconstruction, and never writes output.
func (e *Env) Reachable(id SHA) (*Reach, error) { return e.ReachableAll([]SHA{id}) }

// ReachableAll is the mark phase of a mock collector: the exact object set kept
// alive by a set of live top-level representations. Representations shared by
// several roots are walked once.
func (e *Env) ReachableAll(ids []SHA) (*Reach, error) {
	r := &Reach{Role: map[SHA]DepRole{}, Via: map[SHA][]SHA{}, Height: map[SHA]int{}}
	for _, id := range ids {
		if err := e.walk(r, id, 1, nil); err != nil {
			return nil, err
		}
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
		// Shared descendant: its objects are already counted, but this use may sit deeper
		// than the first one did, and reconstruction through it must stay within the limit.
		if h := r.Height[id]; depth+h-1 > e.Limits.norm().MaxDepth {
			return fmt.Errorf("%w: %x reaches depth %d (maximum %d)", ErrDepth, id[:6], depth+h-1, e.Limits.norm().MaxDepth)
		}
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
	member := DepStored
	if rep.Kind != KindConcat {
		member = DepTable
	}
	height := 1
	for i := range rep.Members {
		m := &rep.Members[i]
		if m.Role == Derived {
			r.Derived++
			if err := e.walk(r, m.Child, depth+1, c.path); err != nil {
				return fmt.Errorf("child %x: %w", m.Child[:6], err)
			}
			if h := 1 + r.Height[m.Child]; h > height {
				height = h
			}
			continue
		}
		if err := r.add(m.SHA, member, max); err != nil {
			return err
		}
	}
	owning := false
	for _, x := range rep.Indexes {
		if err := r.add(x.Ref, DepIndex, max); err != nil {
			return err
		}
		if !x.Owns || e.ExpandOnly {
			continue
		}
		owning = true
		var addErr error
		if err := ScanIndex(e.Src, x, func(_ []byte, v SHA) error {
			addErr = r.add(v, DepOwned, max)
			return addErr
		}); err != nil {
			return err
		}
	}
	if rep.Kind != KindConcat && !owning {
		d, err := e.decoder(rep.Kind)
		if err != nil {
			return err
		}
		var addErr error
		seenVia := map[SHA]bool{}
		if err := d.Expand(c, func(s SHA) {
			if !seenVia[s] {
				seenVia[s] = true
				r.Via[id] = append(r.Via[id], s)
			}
			if addErr == nil {
				addErr = r.add(s, DepVia, max)
			}
		}); err != nil {
			return classify(rep.Kind, err)
		}
		if addErr != nil {
			return addErr
		}
	}
	r.Height[id] = height
	return nil
}

// Cost is the accounting of one persisted revision, derived from the objects that
// were actually put: nothing is estimated and nothing is charged twice.
type Cost struct {
	Objects                            int // new objects of every class
	PayloadObjs, RecipeObjs, IndexObjs int
	TableObjs                          int
	Payload                            int64 // new component bytes (stored payload, via, owned)
	Recipe                             int64 // new representation objects (parent and children)
	Index                              int64 // new record sections (native index; owning sections double as the VIA list)
	Table                              int64 // new format structural tables and nodes (STORED members of format kinds)
}

// Bytes is the persisted byte total.
func (c Cost) Bytes() int64 { return c.Payload + c.Recipe + c.Index + c.Table }

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
		case DepTable:
			c.TableObjs++
			c.Table += sz
		default:
			c.PayloadObjs++
			c.Payload += sz
		}
	}
	return c, nil
}
