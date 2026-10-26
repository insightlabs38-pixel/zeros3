package prodshadow

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

func randBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func newLab(t *testing.T) *Lab {
	t.Helper()
	l, err := NewLab(bins, t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Stop() })
	return l
}

func stage(t *testing.T, l *Lab, parts ...[]byte) (*kinds.Bundle, *Staged) {
	t.Helper()
	b, err := ConcatBundle(parts...)
	if err != nil {
		t.Fatal(err)
	}
	st, err := l.Sh.Stage(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	return b, st
}

// The mapping follows the production record limit: <= 256 KiB direct, larger virtual, empty nothing.
func TestMappingRespectsProductionInvariants(t *testing.T) {
	l := newLab(t)
	small, edge, big, huge := randBytes(1, 5000), randBytes(2, MaxChunk), randBytes(3, MaxChunk+1), randBytes(4, 3<<20)
	_, st := stage(t, l, small, edge, big, huge, nil)
	if st.Stats.Direct != 3 || st.Stats.Virtual != 2 { // small, edge and the 1 KiB-scale representation object
		t.Fatalf("direct %d virtual %d", st.Stats.Direct, st.Stats.Virtual)
	}
	for _, c := range [][]byte{small, edge} {
		if w, err := l.Br.Stat(sha256.Sum256(c)); err != nil || w.Size != int64(len(c)) {
			t.Fatalf("direct component must be one chunk of its own digest: %v", err)
		}
	}
	for _, c := range [][]byte{big, huge} {
		id := sha256.Sum256(c)
		if _, err := l.Br.Stat(id); !errors.Is(err, ErrNotFound) {
			t.Fatal("a virtual component must not exist as one oversized CAS chunk")
		}
		d := l.Sh.Src.desc(id)
		if d == nil || d.Size != int64(len(c)) || len(d.Refs) < 2 {
			t.Fatalf("descriptor missing or trivial: %+v", d)
		}
		for _, r := range d.Refs {
			if r.Len > MaxChunk {
				t.Fatalf("chunk %d exceeds production limit", r.Len)
			}
		}
		if err := l.Sh.Src.VerifyComponent(id); err != nil {
			t.Fatal(err)
		}
	}
	// empty component: addressable, zero bytes, no physical object
	if n, err := l.Sh.Src.Size(sha256.Sum256(nil)); err != nil || n != 0 {
		t.Fatalf("empty: %d %v", n, err)
	}
}

// What a direct chunk above the record limit would cost: compaction skips it, it stays loose.
func TestOversizedDirectChunkIsNeverPacked(t *testing.T) {
	l := newLab(t)
	big := randBytes(9, 1<<20)
	ok := randBytes(10, 200<<10)
	if _, err := l.Br.PutBatch([][]byte{big, ok}); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(big)
	live := filepath.Join(t.TempDir(), "live")
	WriteLive(live, map[[32]byte]bool{sum: true, sha256.Sum256(ok): true})
	if err := l.Stop(); err != nil {
		t.Fatal(err)
	}
	res, err := l.Store.Compact(1, false, live)
	if err != nil {
		t.Fatal(err)
	}
	if num(res, "skipped") != 1 || num(res, "chunks_packed") != 1 {
		t.Fatalf("compact result %v", res)
	}
	if st, _ := l.Store.Stats(); st.LooseChunkCount != 1 || st.LooseChunkFileBytes != 1<<20 {
		t.Fatalf("the oversized chunk must remain loose: %+v", st)
	}
}

func TestRangeReadsTouchOnlyRequiredChunks(t *testing.T) {
	l := newLab(t)
	big := randBytes(5, 4<<20)
	b, _ := stage(t, l, big)
	id := sha256.Sum256(big)
	src := l.Sh.Src
	src.ResetStats()
	buf := make([]byte, 4096)
	off := int64(2<<20 + 123)
	if n, err := src.ReadAt(id, buf, off); n != 4096 || err != nil || !bytes.Equal(buf, big[off:off+4096]) {
		t.Fatalf("range: %d %v", n, err)
	}
	if f := src.ChunkFetches.Load(); f < 1 || f > 2 {
		t.Fatalf("a 4 KiB range fetched %d production chunks", f)
	}
	// tail read and EOF semantics
	tail := make([]byte, 100)
	if n, err := src.ReadAt(id, tail, int64(len(big))-50); n != 50 || err == nil {
		t.Fatalf("tail: %d %v", n, err)
	}
	if _, err := src.ReadAt(id, tail, int64(len(big))+1); err == nil {
		t.Fatal("offset past end accepted")
	}
	// whole object through the contract
	var out bytes.Buffer
	if _, sum, err := l.Sh.Env().Reconstruct(b.ID, &out); err != nil || sum != id || !bytes.Equal(out.Bytes(), big) {
		t.Fatalf("reconstruct: %v", err)
	}
}

// Descriptor-level attacks: the Source must never silently return corrupt bytes
// from a full read; range reads are bounded to chunks production already verified,
// and VerifyComponent is the route to the logical digest.
func TestSourceRejectsBadMappings(t *testing.T) {
	l := newLab(t)
	// hand-made virtual component of equal-length pieces so substitution is same-length
	const piece = 64 << 10
	data := randBytes(6, 4*piece)
	other := randBytes(7, piece)
	var chunks [][]byte
	for i := 0; i < 4; i++ {
		chunks = append(chunks, data[i*piece:(i+1)*piece])
	}
	chunks = append(chunks, other)
	if _, err := l.Br.PutBatch(chunks); err != nil {
		t.Fatal(err)
	}
	id := sha256.Sum256(data)
	mk := func(idx ...int) *Descriptor {
		d := &Descriptor{}
		for _, i := range idx {
			d.Refs = append(d.Refs, Ref{sha256.Sum256(chunks[i]), uint32(len(chunks[i]))})
			d.Size += int64(len(chunks[i]))
		}
		return d
	}
	src := l.Sh.Src
	src.AddDescriptors(map[[32]byte]*Descriptor{id: mk(0, 1, 2, 3)})
	if err := src.VerifyComponent(id); err != nil {
		t.Fatalf("good mapping: %v", err)
	}
	for name, d := range map[string]*Descriptor{
		"reordered":   mk(1, 0, 2, 3),
		"substituted": mk(0, 1, 4, 3), // same length, different content
		"missing":     {Size: 4 * piece, Refs: []Ref{{sha256.Sum256(chunks[0]), piece}, {sha256.Sum256([]byte("absent")), piece}, {sha256.Sum256(chunks[2]), piece}, {sha256.Sum256(chunks[3]), piece}}},
		"wrong len":   {Size: 4*piece - 1, Refs: []Ref{{sha256.Sum256(chunks[0]), piece - 1}, {sha256.Sum256(chunks[1]), piece}, {sha256.Sum256(chunks[2]), piece}, {sha256.Sum256(chunks[3]), piece}}},
	} {
		src.Reset()
		src.AddDescriptors(map[[32]byte]*Descriptor{id: d})
		if err := src.VerifyComponent(id); err == nil {
			t.Errorf("%s: VerifyComponent accepted it", name)
		}
		if _, err := repz3.ReadObject(src, id, int64(4*piece), 1<<30); err == nil {
			t.Errorf("%s: ReadObject accepted it", name)
		}
	}
	// documented limit: a range inside an untouched chunk of a substituted mapping is
	// served from production-verified bytes; only VerifyComponent proves the logical SHA
	src.Reset()
	src.AddDescriptors(map[[32]byte]*Descriptor{id: mk(0, 1, 4, 3)})
	p := make([]byte, 16)
	if _, err := src.ReadAt(id, p, 10); err != nil || !bytes.Equal(p, data[10:26]) {
		t.Errorf("range in an untouched chunk: %v", err)
	}
}

type state struct {
	name string
	fn   func(t *testing.T, l *Lab)
}

// Same representations, every storage state: loose, packed raw, packed compressed,
// after restart, across tiers; including the nested gzip decode that needs two
// concurrent readers.
func TestSourceAcrossStorageStates(t *testing.T) {
	gz := kinds.GzipCompoundChain("gzip+tar+derived", 120)
	sq := kinds.SQLiteChain(300)
	build := func(t *testing.T, name string) (*Fixture, *Fixture) {
		a, err := NewFixture(bins, filepath.Join(t.TempDir(), name+"gz"), gz, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Put(0, true, Hooks{}); err != nil {
			t.Fatal(err)
		}
		s, err := NewFixture(bins, filepath.Join(t.TempDir(), name+"sq"), sq, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Put(0, true, Hooks{}); err != nil {
			t.Fatal(err)
		}
		return a, s
	}
	check := func(t *testing.T, step string, f *Fixture, i int) {
		t.Helper()
		if err := f.L.CheckGET(f.Key, f.Chain.Stages[i].Data); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if err := f.L.CheckShadow(f.Reps[i], f.Chain.Stages[i].Data); err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}
	for _, mode := range []struct {
		name     string
		compress bool
	}{{"packed-raw", false}, {"packed-deflate", true}} {
		t.Run(mode.name, func(t *testing.T) {
			a, s := build(t, mode.name)
			check(t, "loose", a, 0)
			check(t, "loose", s, 0)
			for _, f := range []*Fixture{a, s} {
				compressible := f == a // the SQLite pages are random bytes: DEFLATE correctly keeps them raw
				live := filepath.Join(t.TempDir(), "live")
				if _, err := f.L.LiveFile(live); err != nil {
					t.Fatal(err)
				}
				if err := f.L.Stop(); err != nil {
					t.Fatal(err)
				}
				if _, err := f.L.Store.Compact(1, mode.compress, live); err != nil {
					t.Fatal(err)
				}
				st, _ := f.L.Store.Stats()
				if st.LooseChunkCount != 0 || st.PackedChunkCount == 0 || (mode.compress && compressible && st.PackedCompressedRecs == 0) || (!mode.compress && st.PackedCompressedRecs != 0) {
					t.Fatalf("%s state: %+v", mode.name, st)
				}
				if err := f.L.Start(); err != nil { // restart: locator rebuilt, descriptors from roots only
					t.Fatal(err)
				}
				check(t, "packed after restart", f, 0)
				if err := f.L.Stop(); err != nil {
					t.Fatal(err)
				}
				if _, err := f.L.Store.SpreadPacks(); err != nil {
					t.Fatal(err)
				}
				if err := f.L.Start(); err != nil {
					t.Fatal(err)
				}
				check(t, "tiered", f, 0)
				f.L.Stop()
			}
		})
	}
}

// Real concurrent readers over one packed, tiered store: the gzip decode (parent and
// DERIVED child at once) plus independent readers of the same and other components.
func TestConcurrentReaders(t *testing.T) {
	gz := kinds.GzipCompoundChain("gzip+tar+derived", 120)
	f, err := NewFixture(bins, filepath.Join(t.TempDir(), "cc"), gz, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.L.Stop()
	if _, err := f.Put(0, true, Hooks{}); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(t.TempDir(), "live")
	if _, err := f.L.LiveFile(live); err != nil {
		t.Fatal(err)
	}
	f.L.Stop()
	if _, err := f.L.Store.Compact(1, true, live); err != nil {
		t.Fatal(err)
	}
	if _, err := f.L.Store.SpreadPacks(); err != nil {
		t.Fatal(err)
	}
	if err := f.L.Start(); err != nil {
		t.Fatal(err)
	}
	want := gz.Stages[0].Data
	reach, err := f.L.Sh.Env().Reachable(f.Reps[0])
	if err != nil {
		t.Fatal(err)
	}
	var comps []repz3.SHA
	for id := range reach.Role {
		comps = append(comps, id)
	}
	var wg sync.WaitGroup
	errc := make(chan error, 64)
	for w := 0; w < 8; w++ {
		wg.Add(2)
		go func() { // whole nested reconstruction (itself two concurrent readers)
			defer wg.Done()
			var out bytes.Buffer
			if _, sum, err := f.L.Sh.Env().Reconstruct(f.Reps[0], &out); err != nil || sum != sha256.Sum256(want) || !bytes.Equal(out.Bytes(), want) {
				errc <- fmt.Errorf("reconstruct: %v", err)
			}
		}()
		go func(w int) { // independent component readers, same and different components
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 200; i++ {
				id := comps[rng.Intn(len(comps))]
				n, err := f.L.Sh.Src.Size(id)
				if err != nil {
					errc <- err
					return
				}
				if n == 0 {
					continue
				}
				p := make([]byte, min(n, 777))
				off := rng.Int63n(n - int64(len(p)) + 1)
				got, err := f.L.Sh.Src.ReadAt(id, p, off)
				if err != nil || got != len(p) {
					errc <- fmt.Errorf("ReadAt %x: %d %v", id[:4], got, err)
					return
				}
				q := make([]byte, len(p))
				if _, err := f.L.Sh.Src.ReadAt(id, q, off); err != nil || !bytes.Equal(p, q) {
					errc <- fmt.Errorf("unstable read of %x", id[:4])
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
	if err := f.L.CheckGET(f.Key, want); err != nil {
		t.Fatal(err)
	}
	_ = os.Stdout
}
