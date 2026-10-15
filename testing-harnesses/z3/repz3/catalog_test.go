package repz3

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// stub is a decoder with no format: the output is its members in order; Expand
// names a fixed list and counts its calls.
type stub struct {
	via   []SHA
	calls *int
}

func (s stub) Decode(c *Call) error {
	for i := range c.Rep.Members {
		if err := c.WriteMember(i, c.W); err != nil {
			return err
		}
	}
	return nil
}

func (s stub) Expand(c *Call, emit func(SHA)) error {
	*s.calls++
	for _, v := range s.via {
		emit(v)
	}
	return nil
}

func stubEnv(st *MemStore, via []SHA) (*Env, *int) {
	n := new(int)
	return &Env{Src: st, Table: func(k Kind) (Decoder, bool) {
		if k == KindCAR {
			return stub{via, n}, true
		}
		return nil, false
	}}, n
}

func sum(s string) SHA { return sha256.Sum256([]byte(s)) }

// section stores a 4-byte-key record section for pairs and returns its reference.
func section(t *testing.T, st *MemStore, owns bool, pairs map[string]SHA) Index {
	t.Helper()
	recs, n, err := BuildIndex(4, pairs)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := st.Put(recs)
	return Index{KeyWidth: 4, Count: n, Ref: ref, Owns: owns}
}

// repWith stores a CAR-kind (stub) representation with one stored member and the sections.
func repWith(t *testing.T, st *MemStore, tag string, xs ...Index) SHA {
	t.Helper()
	body := []byte("table:" + tag)
	st.Put(body)
	r := &Rep{Kind: KindCAR, Size: int64(len(body)), Members: []Member{{Role: Stored, SHA: sha256.Sum256(body), Size: int64(len(body))}}, Indexes: xs}
	sort.Slice(r.Indexes, func(i, j int) bool { return bytes.Compare(r.Indexes[i].Ref[:], r.Indexes[j].Ref[:]) < 0 })
	return put(st, r)
}

func TestOwningSectionIsTheViaList(t *testing.T) {
	st := NewMemStore()
	a, b := sum("node-a"), sum("node-b")
	st.Put([]byte("node-a"))
	st.Put([]byte("node-b"))
	x := section(t, st, true, map[string]SHA{"k001": a, "k002": b})
	id := repWith(t, st, "owning", x)
	env, calls := stubEnv(st, []SHA{sum("expand-only")})
	r, err := env.Reachable(id)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 0 {
		t.Fatalf("Expand called %d times for a representation with an owning section", *calls)
	}
	if r.Role[a] != DepOwned || r.Role[b] != DepOwned || r.Role[x.Ref] != DepIndex || len(r.Role) != 5 {
		t.Fatalf("roles %v", r.Role)
	}
	// the same section without Owns is a lookup structure only: Expand names the VIA list
	y := section(t, st, false, map[string]SHA{"k001": a})
	id2 := repWith(t, st, "plain", y)
	r2, err := env.Reachable(id2)
	if err != nil || *calls != 1 || r2.Role[sum("expand-only")] != DepVia || r2.Role[a] != 0 {
		t.Fatalf("non-owning section: calls=%d err=%v roles=%v", *calls, err, r2.Role)
	}
	// the study knob walks Expand even when a section owns
	env.ExpandOnly = true
	if r3, err := env.Reachable(id); err != nil || *calls != 2 || r3.Role[a] != 0 {
		t.Fatalf("ExpandOnly: calls=%d err=%v", *calls, err)
	}
}

func TestMalformedOwningSectionFailsBoundedly(t *testing.T) {
	st := NewMemStore()
	t1 := sum("t1")
	good := section(t, st, true, map[string]SHA{"k001": t1, "k002": sum("t2")})
	raw := append([]byte(nil), st.Bytes(good.Ref)...)
	cases := map[string]struct {
		x    Index
		put  []byte
		want error
	}{
		"bytes flipped":  {good, flip(raw, 40), ErrMismatch},
		"truncated":      {good, raw[:len(raw)-1], ErrMismatch},
		"missing":        {Index{KeyWidth: 4, Count: 2, Ref: sum("nowhere"), Owns: true}, nil, ErrMissing},
		"claims more":    {Index{KeyWidth: 4, Count: 3, Ref: good.Ref, Owns: true}, raw, ErrMismatch},
		"keys descend":   {Index{}, descending(raw), ErrCorrupt},
		"duplicate keys": {Index{}, bytes.Join([][]byte{raw[:36], raw[:36]}, nil), ErrCorrupt},
	}
	for name, c := range cases {
		s2 := NewMemStore()
		x := c.x
		if c.put != nil {
			ref, _ := s2.Put(c.put)
			switch {
			case x.Count == 0:
				x = Index{KeyWidth: 4, Count: int64(len(c.put) / 36), Ref: ref, Owns: true}
			case x.Ref == good.Ref && ref == good.Ref: // the true bytes under a lying length
			default:
				s2.Set(x.Ref, c.put) // damaged bytes under the recorded address
			}
		}
		id := repWith(t, s2, name, x)
		env, _ := stubEnv(s2, nil)
		if _, err := env.Reachable(id); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

func flip(b []byte, i int) []byte { c := append([]byte(nil), b...); c[i] ^= 1; return c }

// descending swaps the two records so the keys are out of order (the digest is then
// that of the swapped bytes: only the order check can catch it).
func descending(b []byte) []byte {
	return append(append([]byte(nil), b[36:72]...), b[:36]...)
}

func TestResolveIsLocalAndNeverPicksBetweenConflicts(t *testing.T) {
	st := NewMemStore()
	a, b := sum("a"), sum("b")
	x1 := section(t, st, false, map[string]SHA{"k001": a, "k002": b})
	x2 := section(t, st, false, map[string]SHA{"k001": a, "k003": b}) // agrees on k001
	x3 := section(t, st, false, map[string]SHA{"k002": a})            // disagrees on k002
	env, _ := stubEnv(st, nil)
	open := func(xs ...Index) *Call {
		c, err := env.Open(repWith(t, st, fmt.Sprint(len(xs), xs[0].Ref[0]), xs...))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := open(x1, x2)
	if id, ok, err := c.Resolve([]byte("k001")); err != nil || !ok || id != a {
		t.Fatalf("agreeing owners: %v %v %v", id == a, ok, err)
	}
	if id, ok, err := c.Resolve([]byte("k003")); err != nil || !ok || id != b {
		t.Fatalf("second section only: %v", err)
	}
	if _, ok, err := c.Resolve([]byte("k404")); ok || err != nil {
		t.Fatalf("unknown key: %v %v", ok, err)
	}
	if _, ok, err := c.Resolve([]byte("k0")); ok || err != nil {
		t.Fatalf("wrong width: %v %v", ok, err)
	}
	c = open(x1, x3)
	if _, ok, err := c.Resolve([]byte("k002")); ok || !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("conflicting owners: found=%v err=%v", ok, err)
	}
	if id, ok, err := c.Resolve([]byte("k001")); err != nil || !ok || id != a {
		t.Fatalf("the unambiguous key still resolves: %v", err)
	}
}

func snapshot(c *Catalog) map[string][]SHA { return c.Mappings() }

func TestCatalogOwnershipLifetime(t *testing.T) {
	st := NewMemStore()
	a, b, c3 := sum("a"), sum("b"), sum("c")
	shared := section(t, st, false, map[string]SHA{"k001": a, "k002": b})
	own1 := section(t, st, false, map[string]SHA{"k010": c3})
	own2 := section(t, st, false, map[string]SHA{"k020": c3})
	r1 := repWith(t, st, "one", shared, own1)
	r2 := repWith(t, st, "two", shared, own2)
	cat := NewCatalog(st, nil)
	dom := DefaultDomain(&Rep{Kind: KindCAR}, shared)
	for _, r := range []SHA{r1, r2} {
		if err := cat.Ingest(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := cat.Ingest(r1); err != nil { // idempotent
		t.Fatal(err)
	}
	if cat.Entries() != 4 || cat.NSections() != 3 {
		t.Fatalf("entries=%d sections=%d", cat.Entries(), cat.NSections())
	}
	owners := cat.Owners(dom, []byte("k001"))
	if len(owners) != 1 || len(owners[0].Reps) != 2 {
		t.Fatalf("a mapping owned through one section by two representations: %+v", owners)
	}
	both := snapshot(cat)

	// restart: discard the accelerator, keep only roots and their stored objects
	re, err := Rebuild(st, nil, []SHA{r2, r1})
	if err != nil || !reflect.DeepEqual(both, snapshot(re)) {
		t.Fatalf("rebuild differs from the incrementally built catalog: %v", err)
	}
	// removing one root drops only what it alone owned
	cat.Remove(r1)
	if _, ok, _ := cat.Resolve(dom, []byte("k010")); ok {
		t.Fatal("a mapping owned only by the removed representation survived")
	}
	if id, ok, err := cat.Resolve(dom, []byte("k001")); !ok || err != nil || id != a {
		t.Fatalf("a shared mapping disappeared: %v %v", ok, err)
	}
	if id, ok, _ := cat.Resolve(dom, []byte("k020")); !ok || id != c3 {
		t.Fatal("the surviving representation lost its own mapping")
	}
	re2, _ := Rebuild(st, nil, []SHA{r2})
	if !reflect.DeepEqual(snapshot(cat), snapshot(re2)) {
		t.Fatal("remove then rebuild disagree")
	}
	cat.Remove(r2)
	cat.Remove(r2) // removing twice is harmless
	if cat.Entries() != 0 || cat.NSections() != 0 || cat.Records() != 0 {
		t.Fatalf("a catalog with no roots holds %d entries, %d sections", cat.Entries(), cat.NSections())
	}
}

func TestCatalogConflictIsReportedNotResolved(t *testing.T) {
	st := NewMemStore()
	a, b := sum("a"), sum("b")
	x1 := section(t, st, false, map[string]SHA{"k001": a})
	x2 := section(t, st, false, map[string]SHA{"k001": b})
	x3 := section(t, st, false, map[string]SHA{"k001": a, "k002": b})
	r1, r2, r3 := repWith(t, st, "1", x1), repWith(t, st, "2", x2), repWith(t, st, "3", x3)
	cat := NewCatalog(st, nil)
	dom := DefaultDomain(&Rep{Kind: KindCAR}, x1)
	for _, r := range []SHA{r1, r3} { // same key, same component: two valid owners
		if err := cat.Ingest(r); err != nil {
			t.Fatal(err)
		}
	}
	if id, ok, err := cat.Resolve(dom, []byte("k001")); err != nil || !ok || id != a {
		t.Fatalf("agreeing owners: %v %v", ok, err)
	}
	if err := cat.Ingest(r2); err != nil { // the conflicting owner is ingested: both facts are kept
		t.Fatal(err)
	}
	if _, ok, err := cat.Resolve(dom, []byte("k001")); ok || !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("conflict: found=%v err=%v", ok, err)
	}
	if got := len(cat.Owners(dom, []byte("k001"))); got != 3 {
		t.Fatalf("%d owners listed", got)
	}
	if got := cat.Mappings()[dom+"\x00k001"]; len(got) != 2 {
		t.Fatalf("both candidates must be visible: %v", got)
	}
	if id, ok, err := cat.Resolve(dom, []byte("k002")); err != nil || !ok || id != b {
		t.Fatalf("a non-conflicting key still resolves: %v", err)
	}
	cat.Remove(r2) // the conflict ends with the conflicting owner
	if id, ok, err := cat.Resolve(dom, []byte("k001")); err != nil || !ok || id != a {
		t.Fatalf("after the conflicting owner left: %v %v", ok, err)
	}
	// a domain keeps unrelated native identities apart
	other := NewCatalog(st, func(*Rep, Index) string { return "other" })
	other.Ingest(r1)
	if _, ok, _ := other.Resolve(dom, []byte("k001")); ok {
		t.Fatal("a key leaked across domains")
	}
}

func TestCatalogRefusesDamagedSectionsAtomically(t *testing.T) {
	st := NewMemStore()
	good := section(t, st, false, map[string]SHA{"k001": sum("a")})
	other := section(t, st, false, map[string]SHA{"k005": sum("b")})
	ok1 := repWith(t, st, "ok", good)
	cat := NewCatalog(st, nil)
	if err := cat.Ingest(ok1); err != nil {
		t.Fatal(err)
	}
	before := snapshot(cat)
	// a representation listing a missing and a damaged section, plus the good one
	missing := Index{KeyWidth: 4, Count: 1, Ref: sum("not stored")}
	badBytes := flip(st.Bytes(other.Ref), 5)
	st.Set(other.Ref, badBytes)
	for name, x := range map[string]Index{"missing": missing, "damaged": other} {
		r := repWith(t, st, name, x, good)
		err := cat.Ingest(r)
		if !errors.Is(err, ErrMissing) && !errors.Is(err, ErrMismatch) {
			t.Fatalf("%s section: %v", name, err)
		}
		if !reflect.DeepEqual(before, snapshot(cat)) || cat.NSections() != 1 {
			t.Fatalf("%s section: the catalog changed although the ingest failed", name)
		}
	}
	// a damaged root object is refused as well
	st.Set(ok1, []byte("garbage"))
	if _, err := Rebuild(st, nil, []SHA{ok1}); err == nil {
		t.Fatal("rebuild accepted a damaged representation")
	}
}

func TestCatalogFollowsDerivedChildren(t *testing.T) {
	st := NewMemStore()
	x := section(t, st, false, map[string]SHA{"k001": sum("child record")})
	child := repWith(t, st, "child", x)
	payload := []byte("table:child")
	mkParent := func(tag string) SHA {
		p := &Rep{Kind: KindConcat, Size: int64(len(payload)), Members: []Member{{Role: Derived, SHA: sha256.Sum256(payload), Size: int64(len(payload)), Child: child}}}
		_ = tag
		return put(st, p)
	}
	p1 := mkParent("p1")
	other := &Rep{Kind: KindConcat, Size: int64(len(payload)) + 1, Members: []Member{{Role: Derived, SHA: sha256.Sum256(append([]byte("!"), payload...)), Size: int64(len(payload)) + 1, Child: child, Frame: Frame{Prefix: []byte("!")}}}}
	p2 := put(st, other)
	cat := NewCatalog(st, nil)
	dom := DefaultDomain(&Rep{Kind: KindCAR}, x)
	cat.Ingest(p1)
	cat.Ingest(p2)
	if !cat.Known(dom, []byte("k001")) {
		t.Fatal("a child's section is part of its root's tree")
	}
	cat.Remove(p1)
	if !cat.Known(dom, []byte("k001")) {
		t.Fatal("the shared child lost ownership while a second root still holds it")
	}
	cat.Remove(p2)
	if cat.Known(dom, []byte("k001")) {
		t.Fatal("the child's mapping outlived both roots")
	}
}
