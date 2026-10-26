package dagz3

import (
	"crypto/sha256"
	"errors"
	"io"
	"math/rand"
	"testing"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

func putRep(st *repz3.MemStore, r *repz3.Rep) repz3.SHA {
	raw, err := r.Marshal()
	if err != nil {
		panic(err)
	}
	id, _ := st.Put(raw)
	return id
}

// childParent persists a SQLite child and a parent that derives it with frame f; mutate
// adjusts the parent's DERIVED member before it is serialized.
func childParent(t *testing.T, mutate func(m *repz3.Member, child *kinds.Bundle)) (st *repz3.MemStore, env *repz3.Env, parent repz3.SHA, child *kinds.Bundle) {
	db := Join(DB(71, 16))
	child = discover(t, db, 1)
	st = repz3.NewMemStore()
	child.Persist(st)
	m := repz3.Member{Role: repz3.Derived, SHA: sha256.Sum256(db), Size: int64(len(db)), Child: child.ID}
	mutate(&m, child)
	parent = putRep(st, &repz3.Rep{Kind: repz3.KindConcat, Size: m.Size, Members: []repz3.Member{m}})
	return st, kinds.NewEnvLimits(st, Lim), parent, child
}

// TestMalformedDAG: every damage class below the contract fails with a bounded error and
// reconstructs nothing; none panics or hangs.
func TestMalformedDAG(t *testing.T) {
	ident := func(*repz3.Member, *kinds.Bundle) {}
	recon := func(env *repz3.Env, id repz3.SHA) error {
		_, _, err := env.Reconstruct(id, io.Discard)
		return err
	}
	t.Run("missing child representation", func(t *testing.T) {
		st, env, p, c := childParent(t, ident)
		st.Delete(c.ID)
		if err := recon(env, p); !errors.Is(err, repz3.ErrMissing) {
			t.Fatalf("%v", err)
		}
		if _, err := env.Reachable(p); !errors.Is(err, repz3.ErrMissing) {
			t.Fatalf("reach: %v", err)
		}
	})
	t.Run("child hash mismatch", func(t *testing.T) {
		st, env, p, c := childParent(t, ident)
		other := discover(t, Join(DB(72, 16)), 1)
		st.Set(c.ID, other.Raw) // a valid representation stored under the wrong address
		if err := recon(env, p); !errors.Is(err, repz3.ErrMismatch) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("child size mismatch", func(t *testing.T) {
		_, env, p, _ := childParent(t, func(m *repz3.Member, _ *kinds.Bundle) { m.Size++ })
		if err := recon(env, p); !errors.Is(err, repz3.ErrMismatch) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("invalid child framing", func(t *testing.T) {
		_, env, p, _ := childParent(t, func(m *repz3.Member, _ *kinds.Bundle) {
			m.Frame = repz3.Frame{Prefix: []byte("XX")} // claims a prefix the digest was not computed over
			m.Size += 2
		})
		if err := recon(env, p); !errors.Is(err, repz3.ErrMismatch) {
			t.Fatalf("%v", err)
		}
		bad := &repz3.Rep{Kind: repz3.KindConcat, Size: 1 << 20, Members: []repz3.Member{{Role: repz3.Derived, Size: 1 << 20, Child: repz3.SHA{1}, Frame: repz3.Frame{Zeros: 1 << 20}}}}
		if _, err := bad.Marshal(); !errors.Is(err, repz3.ErrCorrupt) {
			t.Fatalf("oversized frame: %v", err)
		}
	})
	t.Run("shared child corruption", func(t *testing.T) {
		db := Join(DB(73, 24))
		child := discover(t, db, 1)
		st := repz3.NewMemStore()
		r := NewRoots(st)
		pa, oa := wrap(t, child, db, repz3.Frame{Zeros: 3}, []byte("a"))
		pb, ob := wrap(t, child, db, repz3.Frame{Zeros: 5}, []byte("b"))
		other := Join(DB(74, 24))
		pc, oc := wrap(t, discover(t, other, 1), other, repz3.Frame{}, []byte("c"))
		ida, idb, idc := must2(t)(r.Add("A", pa, oa)), must2(t)(r.Add("B", pb, ob)), must2(t)(r.Add("C", pc, oc))
		reach, _ := r.Env.Reachable(child.ID)
		var victim repz3.SHA
		for _, id := range reach.Order {
			if reach.Role[id] == repz3.DepVia {
				victim = id
				break
			}
		}
		raw := append([]byte(nil), st.Bytes(victim)...)
		raw[len(raw)/2] ^= 0x40
		st.Set(victim, raw)
		if err := recon(r.Env, ida); !errors.Is(err, repz3.ErrMismatch) {
			t.Fatalf("A: %v", err)
		}
		if err := recon(r.Env, idb); !errors.Is(err, repz3.ErrMismatch) {
			t.Fatalf("B: %v", err)
		}
		if err := Exact(r.Env, idc, oc); err != nil { // an unrelated root is not affected
			t.Fatalf("C: %v", err)
		}
	})
	t.Run("bad index ownership", func(t *testing.T) {
		st := repz3.NewMemStore()
		mk := func(recs []byte, n int64, owns bool) repz3.SHA {
			ref, _ := st.Put(recs)
			return putRep(st, &repz3.Rep{Kind: repz3.KindCAR, Indexes: []repz3.Index{{KeyWidth: 4, Count: n, Ref: ref, Owns: owns}}})
		}
		env := kinds.NewEnvLimits(st, Lim)
		ghost := sha256.Sum256([]byte("a target that was never stored"))
		good, n, _ := repz3.BuildIndex(4, map[string]repz3.SHA{"aaaa": ghost})
		if _, err := env.Reachable(mk(good, n, true)); err != nil && !errors.Is(err, repz3.ErrMissing) {
			t.Fatalf("an owned target that is absent is not a reachability error (the walk only names it): %v", err)
		}
		unsorted := append(append([]byte("bbbb"), ghost[:]...), append([]byte("aaaa"), ghost[:]...)...)
		if _, err := env.Reachable(mk(unsorted, 2, true)); !errors.Is(err, repz3.ErrCorrupt) {
			t.Fatalf("unsorted owning section: %v", err)
		}
		if _, err := env.Reachable(mk(good, 2, true)); !errors.Is(err, repz3.ErrMismatch) {
			t.Fatalf("section length disagrees with the representation: %v", err)
		}
		bent := append([]byte(nil), good...)
		bent[0] ^= 1
		ref, _ := st.Put(bent)
		id := putRep(st, &repz3.Rep{Kind: repz3.KindCAR, Indexes: []repz3.Index{{KeyWidth: 4, Count: n, Ref: ref, Owns: true}}})
		st.Set(ref, good) // address no longer matches the bytes
		if _, err := env.Reachable(id); !errors.Is(err, repz3.ErrMismatch) {
			t.Fatalf("section bytes do not hash to their address: %v", err)
		}
	})
	t.Run("ambiguous native identity", func(t *testing.T) {
		st := repz3.NewMemStore()
		mkSec := func(v repz3.SHA) repz3.Index {
			recs, n, _ := repz3.BuildIndex(6, map[string]repz3.SHA{"NATIVE": v})
			ref, _ := st.Put(recs)
			return repz3.Index{KeyWidth: 6, Count: n, Ref: ref}
		}
		id := putRep(st, &repz3.Rep{Kind: repz3.KindCAR, Indexes: sortedIdx(mkSec(repz3.SHA{1}), mkSec(repz3.SHA{2}))})
		call, err := kinds.NewEnvLimits(st, Lim).Open(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := call.Resolve([]byte("NATIVE")); !errors.Is(err, repz3.ErrAmbiguous) {
			t.Fatalf("%v", err)
		}
	})
}

func sortedIdx(a, b repz3.Index) []repz3.Index {
	if string(a.Ref[:]) > string(b.Ref[:]) {
		a, b = b, a
	}
	return []repz3.Index{a, b}
}

// TestGarbageRecipes: each decoder given random recipe bytes under a valid digest fails
// with a bounded error; never a panic, hang or output.
func TestGarbageRecipes(t *testing.T) {
	r := rand.New(rand.NewSource(28))
	comp := []byte("a component")
	for _, k := range []repz3.Kind{repz3.KindCAR, repz3.KindSQLite, repz3.KindGit, repz3.KindMerkle, repz3.KindGzip, repz3.KindZstd} {
		for i := 0; i < 150; i++ {
			st := repz3.NewMemStore()
			cid, _ := st.Put(comp)
			rec := make([]byte, r.Intn(80))
			r.Read(rec)
			rep := &repz3.Rep{Kind: k, Size: int64(1 + r.Intn(1000)), Recipe: rec, Members: []repz3.Member{{Role: repz3.Stored, SHA: cid, Size: int64(len(comp))}}}
			if i%3 == 0 {
				rep.Members = append(rep.Members, repz3.Member{Role: repz3.Derived, SHA: sha256.Sum256(comp), Size: int64(len(comp)), Child: cid})
			}
			id := putRep(st, rep)
			env := kinds.NewEnvLimits(st, Lim)
			if _, _, err := env.Reconstruct(id, io.Discard); err == nil {
				t.Fatalf("kind %d: garbage recipe %x reconstructed", k, rec)
			} else if !Bounded(err) {
				t.Fatalf("kind %d: unclassified error %v", k, err)
			}
			if _, err := env.Reachable(id); err != nil && !Bounded(err) { // must also return, not hang or panic
				t.Fatalf("kind %d: unclassified walk error %v", k, err)
			}
		}
	}
}
