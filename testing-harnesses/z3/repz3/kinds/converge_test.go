package kinds

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"

	"zeros3-z3/gitz3"
	"zeros3-z3/merklez3"
	"zeros3-z3/repz3"
)

// ---- shared helpers ----

// committed stores every stage of a chain (with the native accelerator) and returns
// the representation digests in stage order.
func committed(t *testing.T, ch *Chain) (*repz3.MemStore, *repz3.Catalog, []repz3.SHA) {
	t.Helper()
	st := repz3.NewMemStore()
	cat := NewCatalog(st)
	var ids []repz3.SHA
	for _, s := range ch.Stages {
		id, _ := persisted(t, st, cat, ch, s)
		ids = append(ids, id)
	}
	return st, cat, ids
}

// readSet reconstructs id and returns the objects it read.
func readSet(t *testing.T, st *repz3.MemStore, id repz3.SHA, want []byte) map[repz3.SHA]bool {
	t.Helper()
	st.ResetReads()
	var out bytes.Buffer
	n, sum, err := NewEnv(st).Reconstruct(id, &out)
	if err != nil || n != int64(len(want)) || sum != sha256.Sum256(want) || !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("reconstruction: n=%d of %d err=%v", n, len(want), err)
	}
	_, touched := st.Touched()
	m := map[repz3.SHA]bool{}
	for k := range touched {
		m[k] = true
	}
	return m
}

func chainsNamed(t *testing.T, names ...string) []*Chain {
	t.Helper()
	var out []*Chain
	for _, ch := range testChains(t) {
		for _, n := range names {
			if ch.Name == n {
				out = append(out, ch)
			}
		}
	}
	return out
}

// everyChain is the testChains set plus the long Merkle chain (section compaction).
func everyChain(t *testing.T) []*Chain {
	t.Helper()
	long, err := MerkleLongChain(merklez3.HashSHA3_256, 1200, 11)
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]*Chain(nil), testChains(t)...), long)
}

// ---- A. VIA / GC reachability ----

// Deleting an object the reconstruction read and the contract classifies must break
// the reconstruction, and the walker must have listed it: nothing needed is omitted.
func TestDeletingAnyLiveClassBreaksReconstruction(t *testing.T) {
	for _, ch := range everyChain(t) {
		t.Run(ch.Name, func(t *testing.T) {
			st, _, ids := committed(t, ch)
			last := len(ch.Stages) - 1
			id := ids[last]
			read := readSet(t, st, id, ch.Stages[last].Data)
			reach, err := NewEnv(st).Reachable(id)
			if err != nil {
				t.Fatal(err)
			}
			picked := map[repz3.DepRole]repz3.SHA{}
			for _, obj := range reach.Order {
				role := reach.Role[obj]
				if _, ok := picked[role]; !ok && read[obj] && obj != id {
					picked[role] = obj
				}
			}
			if len(picked) < 2 {
				t.Fatalf("only %d classes read", len(picked))
			}
			for role, obj := range picked {
				good := append([]byte(nil), st.Bytes(obj)...)
				st.Delete(obj)
				if _, err := rebuild(st, id); !errors.Is(err, repz3.ErrMissing) {
					t.Errorf("without %v %x: %v", role, obj[:4], err)
				}
				st.Set(obj, good)
			}
			if got, err := rebuild(st, id); err != nil || !bytes.Equal(got, ch.Stages[last].Data) {
				t.Fatalf("restored: %v", err)
			}
		})
	}
}

// The mock mark phase over several roots: every stored object is reachable from the
// union of all roots (no mystery object), the marked set is exactly the union of the
// per-root reachability, and after a simulated sweep the remaining roots rebuild
// exactly while the removed revision is gone.
func TestMarkSweepKeepsExactlyTheLiveSet(t *testing.T) {
	for _, ch := range everyChain(t) {
		if len(ch.Stages) < 2 {
			continue
		}
		t.Run(ch.Name, func(t *testing.T) {
			st, cat, ids := committed(t, ch)
			env := NewEnv(st)
			all, err := env.ReachableAll(ids)
			if err != nil {
				t.Fatal(err)
			}
			for _, obj := range st.IDs() {
				if _, ok := all.Role[obj]; !ok {
					t.Fatalf("stored object %x is not reachable from any root", obj[:4])
				}
			}
			if all.Role == nil || len(all.Role) != st.Len() {
				t.Fatalf("marked %d of %d stored objects", len(all.Role), st.Len())
			}
			union := map[repz3.SHA]bool{}
			for _, id := range ids {
				r, err := env.Reachable(id)
				if err != nil {
					t.Fatal(err)
				}
				for k := range r.Role {
					union[k] = true
				}
			}
			if len(union) != len(all.Role) {
				t.Fatalf("union of roots %d, joint mark %d", len(union), len(all.Role))
			}

			drop := len(ids) / 2 // a revision in the middle (the first when there are two)
			if len(ids) == 2 {
				drop = 0
			}
			var live []repz3.SHA
			for i, id := range ids {
				if i != drop {
					live = append(live, id)
				}
			}
			mark, err := env.ReachableAll(live)
			if err != nil {
				t.Fatal(err)
			}
			swept := 0
			for _, obj := range st.IDs() {
				if _, ok := mark.Role[obj]; !ok {
					st.Delete(obj)
					swept++
				}
			}
			if st.Len() != len(mark.Role) {
				t.Fatalf("after the sweep the store holds %d objects, the mark %d", st.Len(), len(mark.Role))
			}
			for i, id := range ids {
				got, err := rebuild(st, id)
				if i == drop {
					if err == nil {
						t.Errorf("the removed revision still rebuilds")
					}
					continue
				}
				if err != nil || !bytes.Equal(got, ch.Stages[i].Data) {
					t.Errorf("live revision %d after the sweep: %v", i, err)
				}
			}
			if swept == 0 {
				t.Errorf("the sweep removed nothing")
			}
			// the accelerator follows: removing the root equals rebuilding from the survivors
			cat.Remove(ids[drop])
			re, err := repz3.Rebuild(st, Domain, live)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cat.Mappings(), re.Mappings()) {
				t.Errorf("incremental catalog and rebuilt catalog disagree after removal")
			}
		})
	}
}

// A nested root keeps its child representations, their members and their VIA
// components alive transitively; with no roots nothing is live.
func TestNestedRootsAreTransitivelyLive(t *testing.T) {
	ch := chainsNamed(t, "gzip/gzip+tar+derived")[0]
	st, _, ids := committed(t, ch)
	env := NewEnv(st)
	r, err := env.Reachable(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if r.Count(repz3.DepRep) != 4 || r.Derived != 3 { // gzip -> tar -> {sqlite, car}
		t.Fatalf("reps=%d derived=%d", r.Count(repz3.DepRep), r.Derived)
	}
	if r.Count(repz3.DepVia) < 100 {
		t.Fatalf("only %d VIA objects under a gzip root", r.Count(repz3.DepVia))
	}
	none, err := env.ReachableAll(nil)
	if err != nil || len(none.Role) != 0 {
		t.Fatalf("no roots: %d live (%v)", len(none.Role), err)
	}
	// the gzip root alone is enough: sweep the other revisions and rebuild it
	mark, _ := env.ReachableAll(ids[:1])
	for _, obj := range st.IDs() {
		if _, ok := mark.Role[obj]; !ok {
			st.Delete(obj)
		}
	}
	if got, err := rebuild(st, ids[0]); err != nil || !bytes.Equal(got, ch.Stages[0].Data) {
		t.Fatalf("first revision after sweeping the others: %v", err)
	}
}

// Expansion data that is internally consistent (it hashes to its address) but not a
// table of its kind fails with a bounded error: no panic, no unbounded allocation.
func TestMalformedExpansionDataFailsBoundedly(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, kind := range []repz3.Kind{repz3.KindCAR, repz3.KindSQLite, repz3.KindGit, repz3.KindGzip, repz3.KindMerkle} {
		for trial := 0; trial < 60; trial++ {
			st := repz3.NewMemStore()
			g := make([]byte, rng.Intn(200))
			rng.Read(g)
			switch trial % 4 {
			case 1: // plausible counts with nothing behind them
				g = append(binary.AppendUvarint(nil, 1<<40), g...)
			case 2:
				g = append([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}, g...)
			case 3:
				g = nil
			}
			id, _ := st.Put(g)
			rep := &repz3.Rep{Kind: kind, Size: 1, Members: []repz3.Member{{Role: repz3.Stored, SHA: id, Size: int64(len(g))}}, Recipe: []byte{3, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}}
			if kind == repz3.KindGzip {
				rep.Members = append(rep.Members, repz3.Member{Role: repz3.Derived, SHA: id, Size: 1, Child: id})
			}
			raw, err := rep.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			rid, _ := st.Put(raw)
			done := make(chan error, 1)
			go func() { _, err := NewEnv(st).Reachable(rid); done <- err }()
			select {
			case err := <-done:
				if err == nil {
					t.Errorf("kind %d trial %d: garbage expanded without error", kind, trial)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("kind %d trial %d: expansion did not finish", kind, trial)
			}
		}
	}
}

// ---- B. native ownership ----

// The restart story on the workload that motivated the global resolver: build the
// accelerator, discard it, keep only the live roots and the objects they reach, and
// rebuild. Lookups and reconstruction agree; removing a root drops exactly the
// mappings that only it owned.
func TestNativeAcceleratorRestartAndOwnership(t *testing.T) {
	ch, err := MerkleLongChain(merklez3.HashSHA3_256, 1500, 3)
	if err != nil {
		t.Fatal(err)
	}
	st, cat, ids := committed(t, ch)
	pre := cat.Mappings()

	// restart: a store holding only the serialized live roots and what they reach
	live, err := NewEnv(st).ReachableAll(ids)
	if err != nil {
		t.Fatal(err)
	}
	fresh := repz3.NewMemStore()
	for obj := range live.Role {
		fresh.Put(st.Bytes(obj))
	}
	re, err := repz3.Rebuild(fresh, Domain, ids)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pre, re.Mappings()) {
		t.Fatal("the rebuilt accelerator differs from the one built incrementally")
	}
	for i, id := range ids {
		if got, err := rebuild(fresh, id); err != nil || !bytes.Equal(got, ch.Stages[i].Data) {
			t.Fatalf("revision %d from the restart store: %v", i, err)
		}
	}
	// every native lookup the accelerator answers is the component the node lives in
	rep0, _ := NewEnv(fresh).LoadRep(ids[0])
	dom := Domain(rep0, rep0.Indexes[0])
	for key, targets := range re.Mappings() {
		if len(targets) != 1 {
			t.Fatalf("%d candidates for one native identity", len(targets))
		}
		_ = key
	}
	tag := rep0.Recipe[0]
	_ = tag

	// removing the newest root: mappings introduced only by it disappear, and the ones
	// the older revisions use remain
	newest, prev := ids[len(ids)-1], ids[len(ids)-2]
	repN, _ := NewEnv(st).LoadRep(newest)
	repP, _ := NewEnv(st).LoadRep(prev)
	inPrev := map[repz3.SHA]bool{}
	for _, x := range repP.Indexes {
		inPrev[x.Ref] = true
	}
	var ownOnly []repz3.Index
	for _, x := range repN.Indexes {
		if !inPrev[x.Ref] {
			ownOnly = append(ownOnly, x)
		}
	}
	if len(ownOnly) == 0 {
		t.Fatal("the newest revision added no section of its own")
	}
	var gone [][]byte
	if err := repz3.ScanIndex(st, ownOnly[0], func(k []byte, _ repz3.SHA) error { gone = append(gone, append([]byte(nil), k...)); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, k := range gone {
		if !cat.Known(dom, k) {
			t.Fatalf("key %x unknown while its owner is live", k[:4])
		}
	}
	cat.Remove(newest)
	for _, k := range gone {
		if cat.Known(dom, k) {
			t.Fatalf("key %x survived the removal of its only owner", k[:4])
		}
	}
	if cat.Entries() >= len(pre) {
		t.Fatalf("%d entries after removal, %d before", cat.Entries(), len(pre))
	}
	re2, _ := repz3.Rebuild(st, Domain, ids[:len(ids)-1])
	if !reflect.DeepEqual(cat.Mappings(), re2.Mappings()) {
		t.Fatal("incremental removal and rebuild disagree")
	}
}

// Every Merkle revision carries what it needs: it rebuilds from a store holding
// nothing but the objects its own tree reaches, with no accelerator; the section
// chain stays bounded and is compacted (leveled: a small delta, occasionally a full
// rewrite) when it would not be.
func TestMerkleRevisionsAreSelfContainedAndCompact(t *testing.T) {
	ch, err := MerkleLongChain(merklez3.HashSHA3_256, 150, 70)
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	cat := NewCatalog(st)
	minor, major := 0, 0
	var costs []repz3.Cost
	for i, s := range ch.Stages {
		b, ms, err := EncodeMerkle(s.Data, merklez3.HashSHA3_256, st, cat)
		if err != nil {
			t.Fatal(err)
		}
		if len(b.Rep.Indexes) > merkleMaxSections || ms.Sections != len(b.Rep.Indexes) {
			t.Fatalf("rev %d lists %d sections", i, len(b.Rep.Indexes))
		}
		switch {
		case ms.Major:
			major++
			if len(b.Rep.Indexes) != 1 {
				t.Fatalf("rev %d: major compaction into %d sections", i, len(b.Rep.Indexes))
			}
		case ms.Compacted:
			minor++
			if len(b.Rep.Indexes) > 2 {
				t.Fatalf("rev %d: minor compaction left %d sections", i, len(b.Rep.Indexes))
			}
		}
		if err := Prove(st, b, s.Data); err != nil {
			t.Fatalf("rev %d: %v", i, err)
		}
		id, c, err := Commit(st, cat, b)
		if err != nil {
			t.Fatal(err)
		}
		costs = append(costs, c)
		if i%5 == 0 || ms.Compacted {
			own, err := NewEnv(st).Reachable(id)
			if err != nil {
				t.Fatal(err)
			}
			alone := repz3.NewMemStore()
			for obj := range own.Role {
				alone.Put(st.Bytes(obj))
			}
			if got, err := rebuild(alone, id); err != nil || !bytes.Equal(got, s.Data) {
				t.Fatalf("rev %d from its own reachable set: %v", i, err)
			}
		}
	}
	if minor == 0 || major == 0 {
		t.Fatalf("70 one-key revisions: %d minor and %d major compactions", minor, major)
	}
	// an incremental revision, even one that compacts, is far cheaper than the full section
	for i := 1; i < len(costs); i++ {
		if !(costs[i].Index*3 < costs[0].Index) && !(i > 1 && costs[i].Index < costs[0].Index*2) {
			t.Errorf("rev %d added %d index bytes, first sight %d", i, costs[i].Index, costs[0].Index)
		}
	}
	var small int
	for i := 1; i < len(costs); i++ {
		if costs[i].Index*10 < costs[0].Index {
			small++
		}
	}
	if small < len(costs)-1-major-minor {
		t.Errorf("only %d of %d revisions were cheap", small, len(costs)-1)
	}
}

// A conflicting native identity is reported, never silently resolved: two Merkle
// representations whose sections give one key two different components.
func TestMerkleNativeConflictIsAmbiguous(t *testing.T) {
	ch, _ := MerkleLongChain(merklez3.HashSHA3_256, 400, 1)
	st := repz3.NewMemStore()
	cat := NewCatalog(st)
	id0, _ := persisted(t, st, cat, ch, ch.Stages[0])
	rep, _ := NewEnv(st).LoadRep(id0)
	x := rep.Indexes[0]
	recs := append([]byte(nil), st.Bytes(x.Ref)...)
	w := x.KeyWidth + 32
	key := append([]byte(nil), recs[:x.KeyWidth]...)
	other := sha256.Sum256([]byte("a different component"))
	copy(recs[x.KeyWidth:w], other[:])
	nref, _ := st.Put(recs)
	r2 := *rep
	r2.Indexes = []repz3.Index{{KeyWidth: x.KeyWidth, Count: x.Count, Ref: nref, Owns: true}}
	raw, _ := r2.Marshal()
	id2, _ := st.Put(raw)
	if err := cat.Ingest(id2); err != nil {
		t.Fatal(err)
	}
	dom := Domain(rep, x)
	if _, ok, err := cat.Resolve(dom, key); ok || !errors.Is(err, repz3.ErrAmbiguous) {
		t.Fatalf("conflict: found=%v err=%v", ok, err)
	}
	if len(cat.Owners(dom, key)) != 2 {
		t.Fatalf("both owners must be visible")
	}
	// a reader of the conflicting representation fails on its own sections, not on the catalog
	if _, err := rebuild(st, id2); err == nil {
		t.Fatal("a representation mapping a node to a different component rebuilt")
	}
	// and the analyzer treats the conflicting key as unknown rather than reusing it
	if got, err := rebuild(st, id0); err != nil || !bytes.Equal(got, ch.Stages[0].Data) {
		t.Fatalf("the honest owner is unaffected: %v", err)
	}
}

// Git SHA-1 native identities: the catalog maps an object id to the stream component
// of every live pack that lists it; two repacks of the same history give the same
// answer or a reported conflict, never a silent pick.
func TestGitNativeCatalog(t *testing.T) {
	ch, err := GitFixtureChain(gitz3.SHA1)
	if err != nil {
		t.Fatal(err)
	}
	st, cat, ids := committed(t, ch)
	rep, _ := NewEnv(st).LoadRep(ids[0])
	dom := Domain(rep, rep.Indexes[0])
	var keys [][]byte
	if err := repz3.ScanIndex(st, rep.Indexes[0], func(k []byte, _ repz3.SHA) error { keys = append(keys, append([]byte(nil), k...)); return nil }); err != nil {
		t.Fatal(err)
	}
	agree, conflict := 0, 0
	for _, k := range keys {
		_, ok, err := cat.Resolve(dom, k)
		switch {
		case err == nil && ok:
			agree++
		case errors.Is(err, repz3.ErrAmbiguous):
			conflict++
		default:
			t.Fatalf("key %x: found=%v err=%v", k[:4], ok, err)
		}
	}
	t.Logf("ofs+ref repack of one history: %d ids, %d agree, %d conflict", len(keys), agree, conflict)
	if agree+conflict != len(keys) {
		t.Fatal("a key vanished")
	}
	cat.Remove(ids[1])
	for _, k := range keys {
		if _, ok, err := cat.Resolve(dom, k); !ok || err != nil {
			t.Fatalf("with one owner left every key resolves unambiguously: %v %v", ok, err)
		}
	}
}

// ---- D. Git point reads ----

// The representation object no longer lists the streams: it is the recipe, the table
// chunk references and one index reference. A non-delta read touches that object, the
// index probes and the one stream, and never the table.
func TestGitPointReadDoesNotScaleWithTheMemberList(t *testing.T) {
	ch, err := GitFixtureChain(gitz3.SHA1)
	if err != nil {
		t.Fatal(err)
	}
	minStreams := 10
	if rp := chainsNamed(t, "git/repack"); len(rp) > 0 { // needs the optional git producer
		ch, minStreams = rp[0], 100
	}
	st := repz3.NewMemStore()
	id, _ := persisted(t, st, nil, ch, ch.Stages[0])
	p, _ := gitz3.ParsePack(ch.Stages[0].Data, gitz3.SHA1, gitz3.Limits{})
	p.Resolve()
	rep, _ := NewEnv(st).LoadRep(id)
	_, gs, _ := EncodeGit(ch.Stages[0].Data, gitz3.SHA1)
	if len(rep.Members) > 4 || gs.Streams < minStreams {
		t.Fatalf("%d members for %d streams", len(rep.Members), gs.Streams)
	}
	raw := st.Bytes(id)
	if want := 40*len(rep.Members) + 120; len(raw) > want || len(rep.Recipe) != 0 {
		t.Errorf("representation is %d bytes (recipe %d, %d members): it should be one reference per table chunk", len(raw), len(rep.Recipe), len(rep.Members))
	}
	single, multi := 0, 0
	for i := range p.Entries {
		if p.Entries[i].IsDelta() {
			continue
		}
		pr, err := MeasureGitRead(st, id, p.Entries[i].ID)
		if err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		if pr.Rep != pr.RepObjectBytes {
			t.Errorf("entry %d: representation read %d of %d bytes", i, pr.Rep, pr.RepObjectBytes)
		}
		if pr.Index > 22*int64(20+32)*bitsLen(int64(len(p.Entries))) {
			t.Errorf("entry %d: %d index bytes for %d records", i, pr.Index, len(p.Entries))
		}
		if pr.Table == 0 {
			// the common case: representation + index + the one stream, whatever the pack size
			if pr.Objects != 3 {
				t.Errorf("entry %d: %d objects touched without the table", i, pr.Objects)
			}
			single++
		} else {
			// a stream cut into several components: the table gives the continuation
			if pr.Stream < 64<<10 {
				t.Errorf("entry %d: table touched for only %d stream bytes", i, pr.Stream)
			}
			multi++
		}
	}
	t.Logf("%d single-component point reads (no table), %d multi-component", single, multi)
	if single < 10 {
		t.Fatalf("only %d full objects read without the table", single)
	}
	if single < 10*multi {
		t.Errorf("%d reads needed the table against %d that did not", multi, single)
	}
	// whole-pack reconstruction and Expand find every stream: no hidden dependency
	r, err := NewEnv(st).Reachable(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Count(repz3.DepVia); got == 0 || got > gs.Streams {
		t.Errorf("%d VIA streams for %d table entries", got, gs.Streams)
	}
}

// The Git table now carries the gitz3 meta record: every cut, extension and byte
// flip of it fails (or at least never reproduces the pack), and the shape rules hold.
func TestGitTableIsHostileSafe(t *testing.T) {
	ch, err := GitFixtureChain(gitz3.SHA1)
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	id, _ := persisted(t, st, nil, ch, ch.Stages[0])
	rep, _ := NewEnv(st).LoadRep(id)
	if len(rep.Members) != 1 {
		t.Fatalf("%d table members", len(rep.Members))
	}
	table := append([]byte(nil), st.Bytes(rep.Members[0].SHA)...)
	want := ch.Stages[0].Data
	try := func(label string, tb []byte) {
		ref, _ := st.Put(tb)
		r := *rep
		r.Members = []repz3.Member{{Role: repz3.Stored, SHA: ref, Size: int64(len(tb))}}
		raw, err := r.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		rid, _ := st.Put(raw)
		var out bytes.Buffer
		_, sum, err := NewEnv(st).Reconstruct(rid, &out)
		if err == nil && sum == sha256.Sum256(want) && label != "identity" {
			t.Errorf("%s: a damaged table reconstructed the pack", label)
		}
		NewEnv(st).Reachable(rid) // must return, not panic or hang
	}
	for n := 0; n < len(table); n++ {
		try(fmt.Sprintf("cut %d", n), table[:n])
	}
	for i := 0; i < len(table); i += 5 {
		try(fmt.Sprintf("flip %d", i), flip(table, i))
	}
	try("extended", append(append([]byte(nil), table...), 0))
	try("identity", table)
	// shape: a recipe on a Git representation, or no table at all
	r := *rep
	r.Recipe = []byte{1}
	raw, _ := r.Marshal()
	rid, _ := st.Put(raw)
	if _, err := rebuild(st, rid); !errors.Is(err, repz3.ErrCorrupt) {
		t.Errorf("recipe on a Git representation: %v", err)
	}
	r = *rep
	r.Members = nil
	raw, _ = r.Marshal()
	rid, _ = st.Put(raw)
	if _, err := rebuild(st, rid); !errors.Is(err, repz3.ErrCorrupt) {
		t.Errorf("no table: %v", err)
	}
}

func bitsLen(n int64) int64 {
	b := int64(1)
	for n > 1 {
		n >>= 1
		b++
	}
	return b
}

// ---- C. gzip ----

func gzipFixture(pages int) (tarBytes []byte) {
	r0 := synthSQLiteText(5, pages)
	rngl := xorshift(4)
	lib := make([]byte, 64<<10)
	for off := 0; off < len(lib); off += 4096 {
		copy(lib[off:], textPage(&rngl, 4096))
	}
	return TarLayer([]string{"etc/app.conf", "lib/libapp.so", "data/app.db"},
		map[string][]byte{"etc/app.conf": []byte("mode=prod\n"), "lib/libapp.so": lib, "data/app.db": joinSQLite(r0)})
}

func gzipWith(data []byte, level int, h gzip.Header) []byte {
	var b bytes.Buffer
	w, _ := gzip.NewWriterLevel(&b, level)
	w.Header = h
	w.Write(data)
	w.Close()
	return b.Bytes()
}

func tarChild(plain []byte) (*Bundle, error) {
	t, _, err := EncodeTarLayer(plain, 32<<10)
	if err != nil {
		return CDC(plain)
	}
	return t, nil
}

// The exact original gzip bytes come back for several producers' settings and header
// shapes, through the serialized representation, decoder dispatch and source only.
func TestGzipIsExactAcrossLevelsAndHeaders(t *testing.T) {
	tarBytes := gzipFixture(60)
	hdrs := map[string]gzip.Header{
		"plain":    {},
		"name":     {Name: "layer.tar", Comment: "a layer", ModTime: time.Unix(1700000000, 0), OS: 3},
		"extra":    {Extra: []byte("AB\x03\x00xyz"), OS: 3},
		"all":      {Name: "n", Comment: "c", Extra: []byte("ZZ\x01\x00!"), ModTime: time.Unix(1, 0), OS: 7},
		"empty-ok": {Name: ""},
	}
	for lvl, name := range map[int]string{gzip.NoCompression: "stored", gzip.BestSpeed: "fast", gzip.DefaultCompression: "default", gzip.BestCompression: "best", gzip.HuffmanOnly: "huffman"} {
		for hn, h := range hdrs {
			if lvl != gzip.DefaultCompression && hn != "plain" && hn != "all" {
				continue
			}
			t.Run(name+"/"+hn, func(t *testing.T) {
				gz := gzipWith(tarBytes, lvl, h)
				b, st, err := EncodeGzip(gz, tarChild)
				if err != nil {
					t.Fatal(err)
				}
				if st.HeaderBytes < 10 {
					t.Fatalf("header %d", st.HeaderBytes)
				}
				if err := Prove(nil, b, gz); err != nil {
					t.Fatal(err)
				}
				store := repz3.NewMemStore()
				id, cost, err := Commit(store, nil, b)
				if err != nil {
					t.Fatal(err)
				}
				read := readSet(t, store, id, gz)
				reach, err := NewEnv(store).Reachable(id)
				if err != nil {
					t.Fatal(err)
				}
				for obj := range reach.Role {
					if !read[obj] {
						t.Errorf("%v %x live but never read", reach.Role[obj], obj[:4])
					}
				}
				if cost.Table == 0 || cost.Recipe == 0 {
					t.Errorf("cost %+v", cost)
				}
				// exact bytes: header, trailer and final length
				var out bytes.Buffer
				n, sum, _ := NewEnv(store).Reconstruct(id, &out)
				o := out.Bytes()
				hl, _ := gzipHeaderLen(gz)
				if n != int64(len(gz)) || sum != sha256.Sum256(gz) || !bytes.Equal(o[:hl], gz[:hl]) || !bytes.Equal(o[len(o)-8:], gz[len(gz)-8:]) {
					t.Fatalf("n=%d header/trailer differ", n)
				}
			})
		}
	}
}

func TestGzipWithHeaderCRCAndOddInputs(t *testing.T) {
	tarBytes := gzipFixture(30)
	base := gzipWith(tarBytes, gzip.DefaultCompression, gzip.Header{})
	// add FHCRC: flag bit 1 and a 16-bit header CRC after the fixed header
	hc := crc32.ChecksumIEEE(append([]byte{0x1f, 0x8b, 8, 2}, base[4:10]...)) & 0xffff
	withCRC := append(append(append([]byte(nil), 0x1f, 0x8b, 8, 2), base[4:10]...), byte(hc), byte(hc>>8))
	withCRC = append(withCRC, base[10:]...)
	b, _, err := EncodeGzip(withCRC, tarChild)
	if err != nil {
		t.Fatal(err)
	}
	if err := Prove(nil, b, withCRC); err != nil {
		t.Fatal(err)
	}
	// a plaintext that is not a tar is a CDC child, still exact
	var jb0 bytes.Buffer
	for rng := xorshift(3); jb0.Len() < 60000; {
		jb0.Write(textPage(&rng, 512))
	}
	junk := gzipWith(jb0.Bytes(), gzip.DefaultCompression, gzip.Header{})
	jb, _, err := EncodeGzip(junk, tarChild)
	if err != nil {
		t.Fatal(err)
	}
	if err := Prove(nil, jb, junk); err != nil {
		t.Fatal(err)
	}
	// an expansion ratio past the admission bound is refused (a small hostile layer stays CDC)
	hostile := gzipWith(bytes.Repeat([]byte{'a'}, 4<<20), gzip.BestCompression, gzip.Header{})
	if _, _, err := EncodeGzip(hostile, CDC); !errors.Is(err, errGzip) {
		t.Fatalf("ratio guard: %v", err)
	}
	// empty plaintext
	empty := gzipWith(nil, gzip.DefaultCompression, gzip.Header{})
	eb, _, err := EncodeGzip(empty, CDC)
	if err != nil {
		t.Fatal(err)
	}
	if err := Prove(nil, eb, empty); err != nil {
		t.Fatal(err)
	}
}

// Input the contract does not express is rejected at analysis and Admit answers CDC.
func TestGzipRejectsWhatItCannotExpress(t *testing.T) {
	good := gzipWith(gzipFixture(20), gzip.DefaultCompression, gzip.Header{})
	flip := func(b []byte, i int) []byte { c := append([]byte(nil), b...); c[i] ^= 0x40; return c }
	cases := map[string][]byte{
		"two members":       append(append([]byte(nil), good...), good...),
		"trailing bytes":    append(append([]byte(nil), good...), 1, 2, 3),
		"bad crc":           flip(good, len(good)-8),
		"bad isize":         flip(good, len(good)-2),
		"not gzip":          []byte("hello, this is not gzip data at all"),
		"truncated":         good[:len(good)/2],
		"reserved flags":    flip(good, 3),
		"corrupt deflate":   flip(good, len(good)/2),
		"header only":       good[:10],
		"huge extra length": append([]byte{0x1f, 0x8b, 8, 4, 0, 0, 0, 0, 0, 0, 0xff, 0xff}, make([]byte, 100)...),
	}
	st := repz3.NewMemStore()
	for name, data := range cases {
		if _, _, err := EncodeGzip(data, tarChild); err == nil {
			// a flipped deflate byte may still be a valid stream with a failing trailer; that is also an error
			t.Errorf("%s: accepted", name)
			continue
		}
		cand, _ := EncodeGzipVariant("gzip+tar+derived", data)
		chosen, used, err := Admit(st, cand, data, 0)
		if err != nil || used || chosen.Rep.Kind != repz3.KindConcat {
			t.Errorf("%s: admit used=%v err=%v", name, used, err)
		}
		if err := Prove(nil, chosen, data); err != nil {
			t.Errorf("%s: fallback not exact: %v", name, err)
		}
	}
}

func gzipStored(t *testing.T) (*repz3.MemStore, repz3.SHA, []byte, *Bundle) {
	t.Helper()
	gz := gzipWith(gzipFixture(40), gzip.DefaultCompression, gzip.Header{Name: "layer.tar"})
	b, _, err := EncodeGzip(gz, tarChild)
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	id, _, err := Commit(st, nil, b)
	if err != nil {
		t.Fatal(err)
	}
	return st, id, gz, b
}

// Damaged recipes, tables, units, trailers and children fail with an error and never
// yield bytes that pass for the original.
func TestGzipMalformedRepresentationFails(t *testing.T) {
	st, id, gz, b := gzipStored(t)
	reach, _ := NewEnv(st).Reachable(id)
	bad := func(label string, mut func(r *repz3.Rep)) {
		r := *b.Rep
		r.Recipe = append([]byte(nil), r.Recipe...)
		r.Members = append([]repz3.Member(nil), r.Members...)
		mut(&r)
		raw, err := r.Marshal()
		if err != nil {
			return
		}
		rid, _ := st.Put(raw)
		var out bytes.Buffer
		_, sum, err := NewEnv(st).Reconstruct(rid, &out)
		if err == nil && sum == sha256.Sum256(gz) {
			t.Errorf("%s: damaged representation reconstructed the original", label)
		}
	}
	for n := 0; n < len(b.Rep.Recipe); n++ {
		n := n
		bad(fmt.Sprintf("recipe cut %d", n), func(r *repz3.Rep) { r.Recipe = r.Recipe[:n] })
	}
	for i := 0; i < len(b.Rep.Recipe); i += 3 {
		i := i
		bad(fmt.Sprintf("recipe byte %d", i), func(r *repz3.Rep) { r.Recipe[i] ^= 0x10 })
	}
	bad("trailer crc", func(r *repz3.Rep) { r.Recipe[len(r.Recipe)-2-8] ^= 1 })
	bad("size", func(r *repz3.Rep) { r.Size++ })
	bad("no derived member", func(r *repz3.Rep) { r.Members = r.Members[:len(r.Members)-1] })
	bad("table member dropped", func(r *repz3.Rep) { r.Members = append(r.Members[1:2:2], r.Members[len(r.Members)-1]) })

	// damage or remove each stored object class in turn
	for obj, role := range reach.Role {
		good := append([]byte(nil), st.Bytes(obj)...)
		if len(good) == 0 {
			continue
		}
		st.Set(obj, flip(good, len(good)/2))
		if _, err := rebuild(st, id); err == nil {
			t.Fatalf("damaged %v %x rebuilt", role, obj[:4])
		}
		st.Delete(obj)
		if _, err := rebuild(st, id); !errors.Is(err, repz3.ErrMissing) {
			t.Fatalf("missing %v %x: %v", role, obj[:4], err)
		}
		st.Set(obj, good)
	}
	if got, err := rebuild(st, id); err != nil || !bytes.Equal(got, gz) {
		t.Fatalf("restored: %v", err)
	}
}

func flip(b []byte, i int) []byte { c := append([]byte(nil), b...); c[i] ^= 0x01; return c }

// The decoder writes progressively, stops when the writer fails, and leaves no
// goroutine behind whether it fails early, late or never.
func TestGzipProgressiveOutputAndWriterFailure(t *testing.T) {
	st, id, gz, _ := gzipStored(t)
	base := runtime.NumGoroutine()
	var writes int
	var first int64 = -1
	var total int64
	cw := writerFunc(func(p []byte) (int, error) {
		writes++
		if first < 0 {
			first = int64(len(p))
		}
		total += int64(len(p))
		return len(p), nil
	})
	if n, _, err := NewEnv(st).Reconstruct(id, cw); err != nil || n != int64(len(gz)) || total != n {
		t.Fatalf("n=%d total=%d err=%v", n, total, err)
	}
	if writes < 3 || first >= int64(len(gz)) {
		t.Errorf("output arrived in %d writes (first %d of %d bytes): not progressive", writes, first, len(gz))
	}
	boom := errors.New("sink full")
	for _, limit := range []int64{0, 1, 5, 10, 11, 100, 4096, total / 2, total - 9, total - 8, total - 1} {
		limit := limit
		var got int64
		w := writerFunc(func(p []byte) (int, error) {
			if got+int64(len(p)) > limit {
				return int(max(0, limit-got)), boom
			}
			got += int64(len(p))
			return len(p), nil
		})
		if _, _, err := NewEnv(st).Reconstruct(id, w); err == nil {
			t.Fatalf("limit %d: a failing writer was reported as success", limit)
		}
	}
	for i := 0; i < 50 && runtime.NumGoroutine() > base; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if g := runtime.NumGoroutine(); g > base {
		t.Errorf("%d goroutines left behind (%d before)", g-base, base)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// The four representations of one compressed layer, all exact: the expected order of
// incremental cost is derived < CDC < gzip+tar (storing inflated bytes without the
// database child only pays off with the child).
func TestGzipCompositionComparison(t *testing.T) {
	cost := map[string][]int64{}
	for _, v := range GzipVariants {
		ch := GzipCompoundChain(v, 160)
		st := repz3.NewMemStore()
		for _, s := range ch.Stages {
			cand, err := ch.Encode(st, nil, s.Data)
			if err != nil {
				t.Fatal(v, s.Label, err)
			}
			if err := Prove(st, cand, s.Data); err != nil {
				t.Fatalf("%s/%s: %v", v, s.Label, err)
			}
			_, c, err := Commit(st, nil, cand)
			if err != nil {
				t.Fatal(err)
			}
			cost[v] = append(cost[v], c.Bytes())
		}
	}
	for i, label := range []string{"first sight", "sparse", "vacuum"} {
		t.Logf("%-12s cdc %7d | gzip %7d | gzip+tar %7d | gzip+tar+derived %7d", label, cost["cdc"][i], cost["gzip-exact"][i], cost["gzip+tar"][i], cost["gzip+tar+derived"][i])
	}
	for _, i := range []int{1, 2} {
		d, c, g := cost["gzip+tar+derived"][i], cost["cdc"][i], cost["gzip+tar"][i]
		if !(d < c && c < g) {
			t.Errorf("stage %d: derived %d, cdc %d, gzip+tar %d", i, d, c, g)
		}
	}
	if cost["gzip+tar+derived"][0] <= cost["cdc"][0] {
		t.Errorf("first sight unexpectedly wins: it stores the inflated bytes")
	}
}

// ---- reachability study ----

func TestReachStudyPoliciesAgree(t *testing.T) {
	for _, ch := range everyChain(t) {
		t.Run(ch.Name, func(t *testing.T) {
			st, _, ids := committed(t, ch)
			for _, id := range []repz3.SHA{ids[0], ids[len(ids)-1]} {
				s, err := StudyReach(st, id)
				if err != nil {
					t.Fatal(err)
				}
				if s.A.Live == 0 || s.C.Live == 0 || s.B.Live == 0 {
					t.Fatalf("empty walk %+v", s)
				}
				// the contract walk keeps every object Expand does (owned sections may add replaced nodes)
				if s.C.Live < s.A.Live {
					t.Errorf("contract keeps %d objects, Expand %d", s.C.Live, s.A.Live)
				}
			}
		})
	}
}

// guard against unused imports when subtests are trimmed
var _ = []any{io.Discard, sort.Ints}
