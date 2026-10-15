package kinds

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"

	"zeros3-z3/carz3"
	"zeros3-z3/gitz3"
	"zeros3-z3/merklez3"
	"zeros3-z3/repz3"
)

var (
	workDir    string
	chainsOnce sync.Once
	chainsVal  []*Chain
	chainsErr  error
)

func TestMain(m *testing.M) {
	workDir, _ = os.MkdirTemp("", "z3rep")
	code := m.Run()
	os.RemoveAll(workDir)
	os.Exit(code)
}

func testChains(t *testing.T) []*Chain {
	t.Helper()
	chainsOnce.Do(func() { chainsVal, chainsErr = Chains(Small, workDir) })
	if chainsErr != nil {
		t.Fatal(chainsErr)
	}
	return chainsVal
}

// recSource records every object a reconstruction touches.
type recSource struct {
	repz3.Source
	seen map[repz3.SHA]bool
}

func record(s repz3.Source) *recSource { return &recSource{s, map[repz3.SHA]bool{}} }

func (r *recSource) Size(id repz3.SHA) (int64, error) { r.seen[id] = true; return r.Source.Size(id) }
func (r *recSource) ReadAt(id repz3.SHA, p []byte, off int64) (int, error) {
	r.seen[id] = true
	return r.Source.ReadAt(id, p, off)
}
func (r *recSource) Resolve(n []byte) (repz3.SHA, bool) {
	if nr, ok := r.Source.(repz3.NativeResolver); ok {
		return nr.Resolve(n)
	}
	return repz3.SHA{}, false
}

// noNative hides the source's global native index.
type noNative struct{ repz3.Source }

func rebuild(src repz3.Source, id repz3.SHA) ([]byte, error) {
	var b bytes.Buffer
	_, _, err := NewEnv(src).Reconstruct(id, &b)
	return b.Bytes(), err
}

// persisted analyses and stores one stage and returns only what storage keeps: the
// digest and the cost. The bundle (the analysis structure) is not returned.
func persisted(t *testing.T, st *repz3.MemStore, ch *Chain, s Stage) (repz3.SHA, repz3.Cost) {
	t.Helper()
	cand, err := ch.Encode(st, s.Data)
	if err != nil {
		t.Fatalf("%s/%s: encode: %v", ch.Name, s.Label, err)
	}
	id, cost, err := Commit(st, cand)
	if err != nil {
		t.Fatalf("%s/%s: commit: %v", ch.Name, s.Label, err)
	}
	return id, cost
}

// Every migrated stage rebuilds exactly through the serialized form, the decoder
// table and the component source only; the objects the reconstruction touches are
// exactly the objects the walker lists (the index components excepted); and the
// cost is the persisted bytes.
func TestMigratedWorkloadsAreExactReachableAndAccounted(t *testing.T) {
	for _, ch := range testChains(t) {
		t.Run(ch.Name, func(t *testing.T) {
			st := repz3.NewMemStore()
			var added int64
			for _, s := range ch.Stages {
				before := st.Len()
				id, cost := persisted(t, st, ch, s)
				rec := record(st)
				var out bytes.Buffer
				n, sum, err := NewEnv(rec).Reconstruct(id, &out)
				if err != nil {
					t.Fatalf("%s: %v", s.Label, err)
				}
				if n != int64(len(s.Data)) || sum != sha256.Sum256(s.Data) || !bytes.Equal(out.Bytes(), s.Data) {
					t.Fatalf("%s: reconstruction differs (n=%d of %d)", s.Label, n, len(s.Data))
				}
				r, err := NewEnv(st).Reachable(id)
				if err != nil {
					t.Fatalf("%s: reach: %v", s.Label, err)
				}
				for obj, role := range r.Role {
					if role != repz3.DepIndex && !rec.seen[obj] {
						t.Errorf("%s: %v %x is reachable but reconstruction never read it", s.Label, role, obj[:4])
					}
				}
				for obj := range rec.seen {
					if _, ok := r.Role[obj]; !ok {
						t.Errorf("%s: reconstruction read %x, which the walker does not list", s.Label, obj[:4])
					}
				}
				// accounting == bytes actually persisted; nothing stored is unreachable
				if got := st.Len() - before; got != cost.Objects {
					t.Errorf("%s: %d objects stored, cost counts %d", s.Label, got, cost.Objects)
				}
				added += cost.Bytes()
				if cost.Payload < 0 || cost.Recipe <= 0 {
					t.Errorf("%s: cost %+v", s.Label, cost)
				}
			}
			var total int64
			for _, id := range allIDs(st) {
				sz, _ := st.Size(id)
				total += sz
			}
			if total != added {
				t.Errorf("store holds %d bytes, per-stage costs sum to %d", total, added)
			}
		})
	}
}

func allIDs(st *repz3.MemStore) (ids []repz3.SHA) {
	// the store is small in tests: enumerate through a probe of Touched is not possible, so keep a registry
	return st.IDs()
}

func TestSerializationIsDeterministic(t *testing.T) {
	for _, ch := range testChains(t) {
		s := ch.Stages[0]
		a, err1 := ch.Encode(repz3.NewMemStore(), s.Data)
		b, err2 := ch.Encode(repz3.NewMemStore(), s.Data)
		if err1 != nil || err2 != nil {
			t.Fatal(err1, err2)
		}
		if !bytes.Equal(a.Raw, b.Raw) || a.ID != b.ID {
			t.Errorf("%s: two analyses serialise differently", ch.Name)
		}
		back, err := repz3.Unmarshal(a.Raw, repz3.Limits{})
		if err != nil {
			t.Fatalf("%s: %v", ch.Name, err)
		}
		again, _ := back.Marshal()
		if !bytes.Equal(again, a.Raw) {
			t.Errorf("%s: Unmarshal/Marshal changed the bytes", ch.Name)
		}
	}
}

// CAR needs no stored native index: every CID derives from its block digest.
func TestCARNativeIndexIsDerived(t *testing.T) {
	for _, ch := range testChains(t) {
		if len(ch.Name) < 4 || ch.Name[:4] != "car/" {
			continue
		}
		for _, s := range ch.Stages {
			b, st, err := EncodeCAR(bytes.NewReader(s.Data), int64(len(s.Data)))
			if err != nil {
				t.Fatal(err)
			}
			if len(b.Rep.Indexes) != 0 || st.Derived != st.Sections {
				t.Errorf("%s/%s: %d index sections, %d of %d CIDs derived", ch.Name, s.Label, len(b.Rep.Indexes), st.Derived, st.Sections)
			}
		}
	}
}

// Analysis falls back to CDC; decoding never does.
func TestFallbackIsAnalysisTimeOnly(t *testing.T) {
	ch := SQLiteChain(400)
	st := repz3.NewMemStore()
	var ids []repz3.SHA
	for _, s := range ch.Stages[:3] {
		id, _ := persisted(t, st, ch, s)
		ids = append(ids, id)
	}
	wholesale := ch.Stages[3]
	cand, err := ch.Encode(st, wholesale.Data)
	if err != nil {
		t.Fatal(err)
	}
	for _, ov := range []int64{0, 64, 256} {
		chosen, used, err := Admit(st, cand, wholesale.Data, ov)
		if err != nil || used || chosen.Rep.Kind != repz3.KindConcat {
			t.Errorf("wholesale rewrite at overhead %d: candidate used=%v kind=%d err=%v", ov, used, chosen.Rep.Kind, err)
		}
	}
	sparse, err := ch.Encode(repz3.NewMemStore(), ch.Stages[0].Data) // first sight into an empty store
	if err != nil {
		t.Fatal(err)
	}
	_ = sparse
	// a state that does win is admitted
	st2 := repz3.NewMemStore()
	persisted(t, st2, ch, ch.Stages[0])
	sp, _ := ch.Encode(st2, ch.Stages[1].Data)
	if chosen, used, err := Admit(st2, sp, ch.Stages[1].Data, 256); err != nil || !used || chosen.Rep.Kind != repz3.KindSQLite {
		t.Errorf("sparse update not admitted: used=%v err=%v", used, err)
	}
	// input that is not the format never produces a candidate; Admit answers CDC
	junk := bytes.Repeat([]byte("not a database "), 5000)
	if _, _, err := EncodeSQLite(bytes.NewReader(junk), int64(len(junk))); err == nil {
		t.Fatal("junk analysed as SQLite")
	}
	if chosen, used, err := Admit(st, nil, junk, 0); err != nil || used || chosen.Rep.Kind != repz3.KindConcat {
		t.Errorf("no candidate: %v %v", used, err)
	}

	// decode time: a persisted SQLite representation with a damaged component is an
	// error, not a reinterpretation as CDC bytes
	id := ids[0]
	r, err := NewEnv(st).Reachable(id)
	if err != nil {
		t.Fatal(err)
	}
	for obj, role := range r.Role {
		if role != repz3.DepVia {
			continue
		}
		good := append([]byte(nil), st.Bytes(obj)...)
		bad := append([]byte(nil), good...)
		bad[3] ^= 1
		st.Set(obj, bad)
		if _, err := rebuild(st, id); !errors.Is(err, repz3.ErrMismatch) {
			t.Fatalf("damaged page: %v", err)
		}
		st.Delete(obj)
		if _, err := rebuild(st, id); !errors.Is(err, repz3.ErrMissing) {
			t.Fatalf("missing page: %v", err)
		}
		st.Set(obj, good)
		break
	}
	if got, err := rebuild(st, id); err != nil || !bytes.Equal(got, ch.Stages[0].Data) {
		t.Fatalf("restored: %v", err)
	}
}

func TestNoDecoderMeansUnsupported(t *testing.T) {
	ch := SQLiteChain(200)
	st := repz3.NewMemStore()
	id, _ := persisted(t, st, ch, ch.Stages[0])
	var out bytes.Buffer
	if _, _, err := (&repz3.Env{Src: st}).Reconstruct(id, &out); !errors.Is(err, repz3.ErrUnsupported) {
		t.Fatalf("%v", err)
	}
}

// A representation whose recipe is cut or extended, or whose members are
// reordered, fails with an error for every kind: no panic, no hang, no output
// accepted as exact.
func TestKindRecipesAreHostileSafe(t *testing.T) {
	small := []*Chain{}
	for _, ch := range testChains(t) {
		switch ch.Name {
		case "git/fixture-sha1", "git/fixture-sha256", "merkle/sha3-256", "car/v1-reorder", "sqlite":
			small = append(small, ch)
		}
	}
	for _, ch := range small {
		st := repz3.NewMemStore()
		s := ch.Stages[0]
		cand, err := ch.Encode(st, s.Data)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := Commit(st, cand); err != nil {
			t.Fatal(err)
		}
		try := func(label string, mut func(r repz3.Rep) repz3.Rep) {
			r := mut(*cand.Rep)
			raw, err := r.Marshal()
			if err != nil {
				return // not representable at all
			}
			id, _ := st.Put(raw)
			var out bytes.Buffer
			n, sum, err := NewEnv(st).Reconstruct(id, &out)
			if err == nil && (n != int64(len(s.Data)) || sum != sha256.Sum256(s.Data)) {
				return // output differs and the caller's identity check catches it
			}
			if err == nil && label != "identity" {
				t.Errorf("%s/%s: damaged representation reconstructed exactly", ch.Name, label)
			}
		}
		step := max(1, len(cand.Rep.Recipe)/200)
		for n := 0; n < len(cand.Rep.Recipe); n += step {
			n := n
			try(fmt.Sprintf("recipe cut %d", n), func(r repz3.Rep) repz3.Rep { r.Recipe = r.Recipe[:n]; return r })
		}
		try("recipe extended", func(r repz3.Rep) repz3.Rep { r.Recipe = append(append([]byte(nil), r.Recipe...), 0); return r })
		if len(cand.Rep.Members) > 1 {
			try("member dropped", func(r repz3.Rep) repz3.Rep { r.Members = r.Members[1:]; return r })
			try("members swapped", func(r repz3.Rep) repz3.Rep {
				m := append([]repz3.Member(nil), r.Members...)
				m[0], m[len(m)-1] = m[len(m)-1], m[0]
				r.Members = m
				return r
			})
		}
		try("size changed", func(r repz3.Rep) repz3.Rep { r.Size++; return r })
		try("identity", func(r repz3.Rep) repz3.Rep { return r })
	}
}

// Merkle: a native index entry is a hint. A record that points a native identity at
// a different valid node is caught by the decoder's hash check; the source's global
// native index is required for reused subtrees.
func TestMerkleNativeIndexIsAHint(t *testing.T) {
	ch, err := MerkleChain(merklez3.HashSHA3_256, 1500)
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	id0, _ := persisted(t, st, ch, ch.Stages[0])
	id1, _ := persisted(t, st, ch, ch.Stages[1])
	if got, err := rebuild(noNative{st}, id0); err != nil || !bytes.Equal(got, ch.Stages[0].Data) {
		t.Fatalf("first sight is self-contained: %v", err)
	}
	if _, err := rebuild(noNative{st}, id1); !errors.Is(err, repz3.ErrMissing) {
		t.Fatalf("reused subtrees without the global index: %v", err)
	}
	// point the first two records of the first revision's own index at each other's node
	rep, _ := NewEnv(st).LoadRep(id0)
	x := rep.Indexes[0]
	recs := append([]byte(nil), st.Bytes(x.Ref)...)
	w := x.KeyWidth + 32
	a, b := recs[x.KeyWidth:w], recs[w+x.KeyWidth:2*w]
	for i := range a {
		a[i], b[i] = b[i], a[i]
	}
	st2 := repz3.NewMemStore()
	nref, _ := st2.Put(recs)
	r2 := *rep
	r2.Indexes = []repz3.Index{{Type: x.Type, KeyWidth: x.KeyWidth, Count: x.Count, Ref: nref}}
	raw, _ := r2.Marshal()
	// the tampered store: all original objects plus the swapped index
	for _, o := range allIDs(st) {
		st2.Put(st.Bytes(o))
	}
	rid, _ := st2.Put(raw)
	if _, err := rebuild(noNative{st2}, rid); !errors.Is(err, repz3.ErrMismatch) {
		t.Fatalf("swapped index records: %v", err)
	}
	// a node that does not hash to its native identity
	st3 := repz3.NewMemStore()
	r3, _ := NewEnv(st).LoadRep(id0)
	root := st.Bytes(r3.Members[0].SHA)
	bad := append([]byte(nil), root...)
	bad[len(bad)-1] ^= 1
	bsha, _ := st3.Put(bad)
	r3.Members = []repz3.Member{{Role: repz3.Stored, SHA: bsha, Size: int64(len(bad))}}
	r3.Indexes = nil
	raw3, _ := r3.Marshal()
	id3, _ := st3.Put(raw3)
	if _, err := rebuild(st3, id3); !errors.Is(err, repz3.ErrMismatch) && !errors.Is(err, repz3.ErrCorrupt) {
		t.Fatalf("root that does not hash to its native identity: %v", err)
	}
}

// A SHA-256 native identity is the component identity: no index section is needed.
func TestMerkleSHA256NeedsNoIndex(t *testing.T) {
	ch, err := MerkleChain(merklez3.HashSHA256, 1500)
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	for _, s := range ch.Stages {
		id, cost := persisted(t, st, ch, s)
		if cost.IndexObjs != 0 {
			t.Errorf("%s: %d index objects", s.Label, cost.IndexObjs)
		}
		if got, err := rebuild(noNative{st}, id); err != nil || !bytes.Equal(got, s.Data) {
			t.Errorf("%s: %v", s.Label, err)
		}
	}
}

// ---- compound ----

func compoundFixture(t *testing.T) (*repz3.MemStore, *Chain, repz3.SHA) {
	t.Helper()
	ch := CompoundChain(300)
	st := repz3.NewMemStore()
	id, _ := persisted(t, st, ch, ch.Stages[0])
	return st, ch, id
}

func TestCompoundDerivedStructure(t *testing.T) {
	st, ch, id := compoundFixture(t)
	env := NewEnv(st)
	rep, err := env.LoadRep(id)
	if err != nil {
		t.Fatal(err)
	}
	var derived []int
	for i, m := range rep.Members {
		if m.Role == repz3.Derived {
			derived = append(derived, i)
		}
	}
	if len(derived) != 2 {
		t.Fatalf("derived members %v", derived)
	}
	kindsSeen := map[repz3.Kind]repz3.Member{}
	children := map[repz3.SHA]bool{}
	for _, i := range derived {
		m := rep.Members[i]
		if st.Has(m.SHA) {
			t.Fatal("a derived member's bytes were stored")
		}
		child, err := env.LoadRep(m.Child)
		if err != nil {
			t.Fatal(err)
		}
		kindsSeen[child.Kind] = m
		children[m.Child] = true
	}
	// each child is verified on its own: it rebuilds the inner object, and the parent's
	// framed digest is the digest of exactly prefix || child || zeros, found in the layer
	for _, i := range derived {
		m := rep.Members[i]
		var inner bytes.Buffer
		if n, _, err := env.Reconstruct(m.Child, &inner); err != nil || n != int64(inner.Len()) {
			t.Fatalf("child %x alone: %v", m.Child[:4], err)
		}
		framed := append(append(append([]byte(nil), m.Frame.Prefix...), inner.Bytes()...), make([]byte, m.Frame.Zeros)...)
		if sha256.Sum256(framed) != m.SHA || int64(len(framed)) != m.Size || !bytes.Contains(ch.Stages[0].Data, framed) {
			t.Fatalf("child %x: framed output differs from the member", m.Child[:4])
		}
	}
	if _, ok := kindsSeen[repz3.KindSQLite]; !ok {
		t.Fatal("no SQLite child")
	}
	car, ok := kindsSeen[repz3.KindCAR]
	if !ok || car.Frame.Zeros == 0 || car.Frame.Zeros > 511 || car.Size%512 != 0 {
		t.Fatalf("CAR member %+v: tar padding must be carried as framing", car.Frame)
	}
	r, err := env.Reachable(id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Count(repz3.DepRep) != 3 || r.Derived != 2 {
		t.Fatalf("reps=%d derived=%d", r.Count(repz3.DepRep), r.Derived)
	}
	// the dependency partition: what only the child needs
	parent := map[repz3.SHA]bool{}
	for _, mm := range rep.Members {
		if mm.Role == repz3.Stored {
			parent[mm.SHA] = true
		}
	}
	var childOnly []repz3.SHA
	for obj, role := range r.Role {
		if role == repz3.DepVia || (role == repz3.DepStored && !parent[obj]) || (role == repz3.DepRep && children[obj]) {
			childOnly = append(childOnly, obj)
		}
	}
	if len(childOnly) < 4 {
		t.Fatalf("only %d child-only dependencies", len(childOnly))
	}
	// delete each class of child-only dependency in turn: listed, then reconstruction fails cleanly
	pick := map[repz3.DepRole]repz3.SHA{}
	for _, obj := range childOnly {
		pick[r.Role[obj]] = obj
	}
	if len(pick) != 3 {
		t.Fatalf("child-only classes: %v", pick)
	}
	for role, obj := range pick {
		good := append([]byte(nil), st.Bytes(obj)...)
		st.Delete(obj)
		if _, err := rebuild(st, id); !errors.Is(err, repz3.ErrMissing) {
			t.Errorf("without %v %x: %v", role, obj[:4], err)
		}
		st.Set(obj, good)
	}
	if got, err := rebuild(st, id); err != nil || !bytes.Equal(got, ch.Stages[0].Data) {
		t.Fatalf("restored: %v", err)
	}
	// the revision shares its child-only components with the next
	id2, cost2 := persisted(t, st, ch, ch.Stages[1])
	if cost2.Payload >= int64(len(ch.Stages[1].Data))/10 {
		t.Errorf("second layer stored %d bytes", cost2.Payload)
	}
	if got, err := rebuild(st, id2); err != nil || !bytes.Equal(got, ch.Stages[1].Data) {
		t.Fatalf("second layer: %v", err)
	}
}

// Prefix framing (a Git object header) around a nested SQLite object.
func TestDerivedPrefixFraming(t *testing.T) {
	ch := SQLiteChain(100)
	db := ch.Stages[0].Data
	st := repz3.NewMemStore()
	child, _, err := EncodeSQLite(bytes.NewReader(db), int64(len(db)))
	if err != nil {
		t.Fatal(err)
	}
	prefix := []byte(fmt.Sprintf("blob %d\x00", len(db)))
	framed := append(append([]byte(nil), prefix...), db...)
	parent, err := newBundle(&repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(framed)), Members: []repz3.Member{{
		Role: repz3.Derived, SHA: sha256.Sum256(framed), Size: int64(len(framed)), Child: child.ID, Frame: repz3.Frame{Prefix: prefix}}}}, nil, child)
	if err != nil {
		t.Fatal(err)
	}
	if err := Prove(parent, framed); err != nil {
		t.Fatal(err)
	}
	id, _, err := Commit(st, parent)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := rebuild(st, id); err != nil || !bytes.Equal(got, framed) {
		t.Fatalf("%v", err)
	}
}

func TestCompoundDepthIsBounded(t *testing.T) {
	ch := SQLiteChain(100)
	db := ch.Stages[0].Data
	child, _, _ := EncodeSQLite(bytes.NewReader(db), int64(len(db)))
	mid, err := newBundle(&repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(db)), Members: []repz3.Member{{Role: repz3.Derived, SHA: sha256.Sum256(db), Size: int64(len(db)), Child: child.ID}}}, nil, child)
	if err != nil {
		t.Fatal(err)
	}
	top, err := newBundle(&repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(db)), Members: []repz3.Member{{Role: repz3.Derived, SHA: sha256.Sum256(db), Size: int64(len(db)), Child: mid.ID}}}, nil, mid)
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	id, _, _ := Commit(st, top)
	env := NewEnv(st)
	if _, _, err := env.Reconstruct(id, io.Discard); !errors.Is(err, repz3.ErrDepth) {
		t.Fatalf("depth 3 by default: %v", err)
	}
	if _, err := env.Reachable(id); !errors.Is(err, repz3.ErrDepth) {
		t.Fatalf("walker: %v", err)
	}
	env.Limits.MaxDepth = 3
	var out bytes.Buffer
	if _, _, err := env.Reconstruct(id, &out); err != nil || !bytes.Equal(out.Bytes(), db) {
		t.Fatalf("explicit opt-in: %v", err)
	}
}

// ---- partial reads ----

func TestPartialReadsKeepTheirLocality(t *testing.T) {
	t.Run("sqlite page", func(t *testing.T) {
		ch := SQLiteChain(1500)
		st := repz3.NewMemStore()
		id, _ := persisted(t, st, ch, ch.Stages[0])
		st.ResetReads()
		c, err := NewEnv(st).Open(id)
		if err != nil {
			t.Fatal(err)
		}
		pg, err := ReadSQLitePage(c, 777)
		if err != nil || !bytes.Equal(pg, ch.Stages[0].Data[777*4096:778*4096]) {
			t.Fatalf("page: %v", err)
		}
		reads, tb := st.Touched()
		var comps int
		var cb int64
		for obj, n := range tb {
			if obj != id {
				comps++
				cb += n
			}
		}
		if comps != 2 || cb > 4096+2200 {
			t.Errorf("%d components, %d bytes (%d reads) for one page", comps, cb, reads)
		}
	})
	t.Run("car block", func(t *testing.T) {
		ch := CARChains(carz3.TinyBlocks)[0]
		st := repz3.NewMemStore()
		id, _ := persisted(t, st, ch, ch.Stages[0])
		data := ch.Stages[0].Data
		rd, err := carz3.NewReader(bytes.NewReader(data), carz3.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		var cid, block []byte
		for i := 0; i < 20; i++ {
			e, err := rd.Next()
			if err != nil {
				t.Fatal(err)
			}
			cid, block = e.CID.Raw, append([]byte(nil), e.Data...)
		}
		st.ResetReads()
		c, err := NewEnv(st).Open(id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ReadCARBlock(c, cid)
		if err != nil || !bytes.Equal(got, block) {
			t.Fatalf("block: %v", err)
		}
		_, tb := st.Touched()
		want := 1 + len(c.Rep.Members) + 1 // the representation, the table chunks, the block
		if _, ok := tb[id]; !ok || len(tb) != want {
			t.Errorf("touched %d objects, want %d (representation + %d table chunks + block)", len(tb), want, len(c.Rep.Members))
		}
		var blockBytes int64
		for obj, n := range tb {
			if sz, _ := st.Size(obj); sz == int64(len(block)) && n == sz {
				blockBytes = n
			}
		}
		if blockBytes == 0 {
			t.Errorf("the block was not read once, whole")
		}
	})
	t.Run("git object", func(t *testing.T) {
		ch, err := GitFixtureChain(gitz3.SHA1)
		if err != nil {
			t.Fatal(err)
		}
		st := repz3.NewMemStore()
		id, _ := persisted(t, st, ch, ch.Stages[0])
		p, _ := gitz3.ParsePack(ch.Stages[0].Data, gitz3.SHA1, gitz3.Limits{})
		p.Resolve()
		found := 0
		for i := range p.Entries {
			if p.Entries[i].IsDelta() {
				c, _ := NewEnv(st).Open(id)
				if _, _, err := ReadGitObject(c, p.Entries[i].ID); !errors.Is(err, ErrDeltaEntry) {
					t.Errorf("delta entry %d: %v", i, err)
				}
				continue
			}
			st.ResetReads()
			c, err := NewEnv(st).Open(id)
			if err != nil {
				t.Fatal(err)
			}
			typ, payload, err := ReadGitObject(c, p.Entries[i].ID)
			if err != nil {
				t.Fatalf("entry %d: %v", i, err)
			}
			want, _ := p.Inflate(i)
			if !bytes.Equal(payload, want) || typ == "" {
				t.Fatalf("entry %d payload differs", i)
			}
			found++
		}
		if found == 0 {
			t.Fatal("no full object in the fixture")
		}
	})
	t.Run("compound inner page", func(t *testing.T) {
		st, ch, id := compoundFixture(t)
		data := ch.Stages[0].Data
		env := NewEnv(st)
		c, err := env.Open(id)
		if err != nil {
			t.Fatal(err)
		}
		// locate the database inside the layer to know the expected page
		off := bytes.Index(data, []byte("SQLite format 3\x00"))
		st.ResetReads()
		c, _ = env.Open(id)
		var cc *repz3.Call
		for i, m := range c.Rep.Members {
			if m.Role != repz3.Derived {
				continue
			}
			if k, err := c.Child(i); err != nil {
				t.Fatal(err)
			} else if k.Rep.Kind == repz3.KindSQLite {
				cc = k
			}
		}
		if cc == nil {
			t.Fatal("no SQLite child")
		}
		pg, err := ReadSQLitePage(cc, 100)
		if err != nil || !bytes.Equal(pg, data[off+100*4096:off+101*4096]) {
			t.Fatalf("inner page: %v", err)
		}
	})
}
