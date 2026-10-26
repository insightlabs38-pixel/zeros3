package dagz3

import (
	"crypto/sha256"
	"fmt"
	"math/rand"

	"zeros3-z3/repz3"
)

// Synthetic planning fixtures. Objects are named; the SHA of a named object is the digest of
// its name, so two candidates share an object exactly when they use the same name.

func fake(name string) repz3.SHA { return sha256.Sum256([]byte("fake:" + name)) }

// FB builds a Problem from named parts.
type FB struct {
	P   Problem
	out map[string]Obj
}

func NewFB(perObject int64) *FB {
	return &FB{P: Problem{Base: map[repz3.SHA]bool{}, PerObject: perObject}, out: map[string]Obj{}}
}

// Object declares a logical object.
func (f *FB) Object(name string) *FB {
	o := Obj{Name: name, Out: fake("out:" + name), Len: 1000}
	f.out[name] = o
	f.P.Objects = append(f.P.Objects, o)
	return f
}

// Cand adds a candidate for object: root names its representation object (size repSize); kids
// are child representation names (shared when the names coincide, each with its own size
// given in parts); parts are other components name -> size.
func (f *FB) Cand(object, id, root string, repSize int64, kids map[string]int64, parts map[string]int64) *FB {
	o := f.out[object]
	c := Cand{ID: id, Object: object, Out: o.Out, Len: o.Len, Root: fake("rep:" + root),
		Objs: map[repz3.SHA]int64{fake("rep:" + root): repSize}, Reps: map[repz3.SHA][]repz3.SHA{}}
	c.Reps[c.Root] = nil
	for k, n := range kids {
		c.Objs[fake("rep:"+k)] = n
		c.Reps[c.Root] = append(c.Reps[c.Root], fake("rep:"+k))
		c.Reps[fake("rep:"+k)] = nil
	}
	for k, n := range parts {
		c.Objs[fake("part:"+k)] = n
	}
	sortSHAs(c.Reps[c.Root])
	f.P.Cands = append(f.P.Cands, c)
	return f
}

func (f *FB) InBase(names ...string) *FB {
	for _, n := range names {
		f.P.Base[fake("rep:"+n)] = true
		f.P.Base[fake("part:"+n)] = true
	}
	return f
}

func sortSHAs(s []repz3.SHA) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && string(s[j][:]) < string(s[j-1][:]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Fixtures are the deterministic adversarial instances of the planner study.
func Fixtures() map[string]*Problem {
	out := map[string]*Problem{}

	// 1. no sharing anywhere: independent choice is optimal
	f := NewFB(0)
	for _, o := range []string{"A", "B", "C"} {
		f.Object(o)
		f.Cand(o, o+"1", o+"1", 10, nil, map[string]int64{o + "-p1": 100})
		f.Cand(o, o+"2", o+"2", 10, nil, map[string]int64{o + "-p2": 130})
	}
	out["independent-optimal"] = &f.P

	// 2. two parents each locally prefer the private candidate (105 < 110), but together the
	// shared child X pays for itself (100 + 2*10 < 210)
	f = NewFB(0)
	for _, o := range []string{"A", "B"} {
		f.Object(o)
		f.Cand(o, o+"1", o+"1", 5, map[string]int64{"X": 100}, map[string]int64{o + "-s": 5})
		f.Cand(o, o+"2", o+"2", 5, nil, map[string]int64{o + "-p": 100})
	}
	out["shared-child-pays"] = &f.P

	// 3. one expensive shared child (300) unlocks four cheap parents (5 vs 120 each)
	f = NewFB(0)
	for i := 0; i < 4; i++ {
		o := fmt.Sprintf("P%d", i)
		f.Object(o)
		f.Cand(o, o+"-x", o+"-x", 5, map[string]int64{"X": 300}, nil)
		f.Cand(o, o+"-p", o+"-p", 5, nil, map[string]int64{o + "-priv": 115})
	}
	out["expensive-child-unlocks-parents"] = &f.P

	// 4. apparent sharing is fake: the children have the same size but different bytes
	f = NewFB(0)
	for _, o := range []string{"A", "B"} {
		f.Object(o)
		f.Cand(o, o+"1", o+"1", 5, map[string]int64{"X-" + o: 100}, map[string]int64{o + "-s": 5})
		f.Cand(o, o+"2", o+"2", 5, nil, map[string]int64{o + "-p": 100})
	}
	out["fake-sharing"] = &f.P

	// 5. the store already holds X: marginal choice sees it, independent choice too (base-aware)
	f = NewFB(0).InBase("X")
	for _, o := range []string{"A", "B"} {
		f.Object(o)
		f.Cand(o, o+"1", o+"1", 5, map[string]int64{"X": 100}, map[string]int64{o + "-s": 5})
		f.Cand(o, o+"2", o+"2", 5, nil, map[string]int64{o + "-p": 100})
	}
	out["store-holds-child"] = &f.P

	// 6. per-object overhead flips the winner: many tiny nodes lose to one fat object
	f = NewFB(40)
	f.Object("A")
	f.Cand("A", "many", "many", 20, map[string]int64{"n1": 20, "n2": 20, "n3": 20}, map[string]int64{"d1": 30, "d2": 30, "d3": 30})
	f.Cand("A", "one", "one", 20, nil, map[string]int64{"fat": 260})
	out["overhead-flips"] = &f.P
	return out
}

// Synthetic generates n objects with three candidates each: a private one, and two that use
// a child representation drawn from a shared pool. The pool size sets how many objects
// compete for each child. Costs are chosen so that taking a shared child is locally
// unattractive (child cost exceeds the private alternative) but globally attractive when a
// few objects agree.
func Synthetic(n int, seed int64) *Problem {
	r := rand.New(rand.NewSource(seed))
	f := NewFB(0)
	pool := max(2, n/3)
	childCost := make([]int64, pool)
	for i := range childCost {
		childCost[i] = 120 + int64(r.Intn(80))
	}
	for i := 0; i < n; i++ {
		o := fmt.Sprintf("o%03d", i)
		f.Object(o)
		priv := 90 + int64(r.Intn(60))
		f.Cand(o, o+"-p", o+"-p", 8, nil, map[string]int64{o + "-pp": priv})
		for k := 0; k < 2; k++ {
			c := r.Intn(pool)
			name := fmt.Sprintf("c%03d", c)
			f.Cand(o, fmt.Sprintf("%s-s%d", o, k), fmt.Sprintf("%s-s%d", o, k), 8, map[string]int64{name: childCost[c]}, map[string]int64{fmt.Sprintf("%s-q%d", o, k): 4 + int64(r.Intn(12))})
		}
	}
	return &f.P
}
