package dagz3

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sort"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// Exact reconstructs root through the serialized objects only and compares length,
// SHA-256 and bytes with want.
func Exact(env *repz3.Env, root repz3.SHA, want []byte) error {
	var out bytes.Buffer
	n, sum, err := env.Reconstruct(root, &out)
	if err != nil {
		return err
	}
	if n != int64(len(want)) || int64(out.Len()) != n || sum != sha256.Sum256(want) || !bytes.Equal(out.Bytes(), want) {
		return errors.New("dagz3: reconstruction differs from the original")
	}
	return nil
}

// Graph is the representation DAG (DERIVED.Child links) below a set of roots, loaded from
// the serialized objects only.
type Graph struct {
	Reps   map[repz3.SHA]*repz3.Rep
	Parent map[repz3.SHA]int // incoming DERIVED member edges
	Order  []repz3.SHA
}

// LoadGraph walks the child links below roots once per distinct representation.
func LoadGraph(env *repz3.Env, roots ...repz3.SHA) (*Graph, error) {
	g := &Graph{Reps: map[repz3.SHA]*repz3.Rep{}, Parent: map[repz3.SHA]int{}}
	var visit func(id repz3.SHA) error
	visit = func(id repz3.SHA) error {
		if _, ok := g.Reps[id]; ok {
			return nil
		}
		rep, err := env.LoadRep(id)
		if err != nil {
			return err
		}
		g.Reps[id] = rep
		g.Order = append(g.Order, id)
		for _, m := range rep.Members {
			if m.Role == repz3.Derived {
				g.Parent[m.Child]++
				if err := visit(m.Child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, r := range roots {
		if err := visit(r); err != nil {
			return nil, err
		}
	}
	return g, nil
}

// Edges is the number of DERIVED member references among distinct representations.
func (g *Graph) Edges() (n int) {
	for _, c := range g.Parent {
		n += c
	}
	return n
}

// Shared lists representations with more than one incoming DERIVED edge.
func (g *Graph) Shared() (out []repz3.SHA) {
	for id, c := range g.Parent {
		if c > 1 {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
	return out
}

// ReachStats compares the true union of transitive reachability with the sum of the
// per-root walks: the difference is the graph sharing.
type ReachStats struct {
	Union, Sum           int
	UnionBytes, SumBytes int64
}

// Union walks roots together and one by one.
func Union(st *repz3.MemStore, env *repz3.Env, roots []repz3.SHA) (*repz3.Reach, ReachStats, error) {
	var rs ReachStats
	all, err := env.ReachableAll(roots)
	if err != nil {
		return nil, rs, err
	}
	size := func(id repz3.SHA) int64 { n, _ := st.Size(id); return n }
	for _, id := range all.Order {
		rs.Union++
		rs.UnionBytes += size(id)
	}
	for _, r := range roots {
		one, err := env.Reachable(r)
		if err != nil {
			return nil, rs, err
		}
		for _, id := range one.Order {
			rs.Sum++
			rs.SumBytes += size(id)
		}
	}
	return all, rs, nil
}

// ReadSet reconstructs every root (discarding output, checking SHA-256 against want when
// given), and reports the objects read, the bytes read, and any object read that the
// reachability union does not contain (a GC bug).
func ReadSet(st *repz3.MemStore, env *repz3.Env, roots []repz3.SHA, want [][]byte) (read map[repz3.SHA]int64, unexplained []repz3.SHA, err error) {
	reach, err := env.ReachableAll(roots)
	if err != nil {
		return nil, nil, err
	}
	st.ResetReads()
	for i, r := range roots {
		if want != nil {
			if err := Exact(env, r, want[i]); err != nil {
				return nil, nil, fmt.Errorf("root %x: %w", r[:6], err)
			}
			continue
		}
		if _, _, err := env.Reconstruct(r, io.Discard); err != nil {
			return nil, nil, err
		}
	}
	_, read = st.Touched()
	for id := range read {
		if _, ok := reach.Role[id]; !ok {
			unexplained = append(unexplained, id)
		}
	}
	sort.Slice(unexplained, func(i, j int) bool { return bytes.Compare(unexplained[i][:], unexplained[j][:]) < 0 })
	return read, unexplained, nil
}

// Roots is the lab root set: store/namespace lifecycle, deliberately not part of any
// representation. Liveness is whatever repz3 reachability says about the named roots.
type Roots struct {
	St    *repz3.MemStore
	Env   *repz3.Env
	names map[string]repz3.SHA
	want  map[string][]byte
}

func NewRoots(st *repz3.MemStore) *Roots {
	return &Roots{St: st, Env: kinds.NewEnvLimits(st, Lim), names: map[string]repz3.SHA{}, want: map[string][]byte{}}
}

// Add persists b and names it a live root; want is the exact object it must rebuild.
func (r *Roots) Add(name string, b *kinds.Bundle, want []byte) (repz3.SHA, error) {
	id, _, err := b.Persist(r.St)
	if err != nil {
		return id, err
	}
	r.names[name], r.want[name] = id, want
	return id, nil
}

func (r *Roots) Remove(name string) { delete(r.names, name); delete(r.want, name) }

func (r *Roots) IDs() []repz3.SHA {
	var ns []string
	for n := range r.names {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	out := make([]repz3.SHA, len(ns))
	for i, n := range ns {
		out[i] = r.names[n]
	}
	return out
}

// Live is the mark phase: every object the live roots keep alive.
func (r *Roots) Live() (*repz3.Reach, error) { return r.Env.ReachableAll(r.IDs()) }

// Sweep deletes every stored object the live roots do not reach and reports what it freed.
func (r *Roots) Sweep() (freed []repz3.SHA, bytes int64, err error) {
	live, err := r.Live()
	if err != nil {
		return nil, 0, err
	}
	for _, id := range r.St.IDs() {
		if _, ok := live.Role[id]; !ok {
			n, _ := r.St.Size(id)
			bytes += n
			freed = append(freed, id)
			r.St.Delete(id)
		}
	}
	return freed, bytes, nil
}

// Verify rebuilds every live root exactly from what remains in the store.
func (r *Roots) Verify() error {
	for n, id := range r.names {
		if err := Exact(r.Env, id, r.want[n]); err != nil {
			return fmt.Errorf("root %q: %w", n, err)
		}
	}
	return nil
}

// Bounded reports whether err is one of the contract's bounded failure classes (never a
// panic, hang or silent success with other bytes).
func Bounded(err error) bool {
	for _, e := range []error{repz3.ErrMissing, repz3.ErrMismatch, repz3.ErrCorrupt, repz3.ErrShort, repz3.ErrLimit, repz3.ErrUnsupported} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// Deletion is the outcome of deleting each reachable object of one root in turn.
type Deletion struct {
	Total, Needed int
	Unbounded     int // failures that are not a bounded contract error
	WrongBytes    int // reconstructions that "succeeded" with other bytes (must be zero)
}

// DeleteEach removes every object reachable from root, one at a time, and classifies the
// effect on reconstruction by the object's dependency role. An object the reconstruction
// does not need (a lookup-only native index) is counted, not hidden.
func DeleteEach(st *repz3.MemStore, env *repz3.Env, root repz3.SHA, want []byte) (map[repz3.DepRole]*Deletion, error) {
	reach, err := env.Reachable(root)
	if err != nil {
		return nil, err
	}
	out := map[repz3.DepRole]*Deletion{}
	for _, id := range reach.Order {
		role := reach.Role[id]
		d := out[role]
		if d == nil {
			d = &Deletion{}
			out[role] = d
		}
		d.Total++
		cl := st.Clone()
		cl.Delete(id)
		e := &repz3.Env{Src: cl, Table: env.Table, Limits: env.Limits}
		err := Exact(e, root, want)
		switch {
		case err == nil:
		case Bounded(err):
			d.Needed++
		case err.Error() == "dagz3: reconstruction differs from the original":
			d.WrongBytes++
		default:
			d.Unbounded++
		}
	}
	return out, nil
}

// Wrap is the smallest genuine parent: a concat representation holding the literal head
// parts as STORED members followed by one DERIVED member whose child is a representation
// of childOut and whose frame is f. It returns the parent bundle and its exact output.
func Wrap(child *kinds.Bundle, childOut []byte, f repz3.Frame, head ...[]byte) (*kinds.Bundle, []byte, error) {
	rep := &repz3.Rep{Kind: repz3.KindConcat}
	var objs [][]byte
	var out []byte
	for _, h := range head {
		rep.Members = append(rep.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(h), Size: int64(len(h))})
		objs = append(objs, append([]byte(nil), h...))
		out = append(out, h...)
	}
	framed := append(append(append([]byte(nil), f.Prefix...), childOut...), make([]byte, f.Zeros)...)
	rep.Members = append(rep.Members, repz3.Member{Role: repz3.Derived, SHA: sha256.Sum256(framed), Size: int64(len(framed)), Child: child.ID, Frame: f})
	out = append(out, framed...)
	rep.Size = int64(len(out))
	b, err := kinds.NewBundle(rep, objs, child)
	return b, out, err
}
