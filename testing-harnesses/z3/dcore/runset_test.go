package dcore

import (
	"bytes"
	"io"
	"math/rand/v2"
	"sort"
	"testing"
)

type memStore struct {
	files map[uint64][]byte
	fail  bool
}

func newMemStore() *memStore { return &memStore{files: map[uint64][]byte{}} }
func (m *memStore) Put(gen uint64, write func(io.Writer) error) (io.ReaderAt, int64, error) {
	var b bytes.Buffer
	if err := write(&b); err != nil {
		return nil, 0, err
	}
	if m.fail {
		return nil, 0, io.ErrShortWrite
	}
	m.files[gen] = b.Bytes()
	return bytes.NewReader(b.Bytes()), int64(b.Len()), nil
}
func (m *memStore) Drop(gen uint64) error { delete(m.files, gen); return nil }

func randPostings(seed uint64, n, ids int, featSpace uint64) []Posting {
	r := rand.New(rand.NewPCG(seed, 1))
	p := make([]Posting, n)
	for i := range p {
		f := r.Uint64()
		if featSpace > 0 {
			f = (r.Uint64() % featSpace) << 40
		}
		p[i] = Posting{f, uint32(r.IntN(ids))}
	}
	return p
}

func TestSortsAgree(t *testing.T) {
	for _, n := range []int{0, 1, 100, 5000, 200000} {
		a := randPostings(uint64(n), n, 1000, 0)
		b := append([]Posting{}, a...)
		c := append([]Posting{}, a...)
		SortReflect(a)
		SortFunc(b)
		c = SortBuckets(c, 4)
		for i := range a {
			if a[i] != b[i] || a[i] != c[i] {
				t.Fatalf("n=%d sorts differ at %d", n, i)
			}
		}
	}
	// heavy duplicates in few buckets
	a := randPostings(9, 50000, 50, 20)
	b := append([]Posting{}, a...)
	SortFunc(a)
	b = SortBuckets(b, 3)
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("dup-heavy sorts differ")
		}
	}
}

func uniqSorted(p []Posting) []Posting {
	SortFunc(p)
	out := p[:0]
	for i, v := range p {
		if i == 0 || v != p[i-1] {
			out = append(out, v)
		}
	}
	return out
}

func TestRunLookupMatchesReference(t *testing.T) {
	ps := uniqSorted(randPostings(1, 60000, 5000, 3000))
	var buf bytes.Buffer
	n, err := WriteRun(&buf, ps, 7)
	if err != nil || n != int64(buf.Len()) {
		t.Fatal(err, n, buf.Len())
	}
	run, err := OpenRun(bytes.NewReader(buf.Bytes()), int64(buf.Len()), true)
	if err != nil || run.Gen != 7 || run.Count != int64(len(ps)) {
		t.Fatal(err)
	}
	ref := map[uint64][]uint32{}
	for _, p := range ps {
		ref[p.Feature] = append(ref[p.Feature], p.ID)
	}
	for f, want := range ref {
		got, err := run.Lookup(f, 0)
		if err != nil || len(got) != len(want) {
			t.Fatalf("lookup %x: %v %v vs %v", f, err, got, want)
		}
		if len(want) > 3 {
			g2, _ := run.Lookup(f, 3)
			if len(g2) != 3 || g2[2] != want[len(want)-1] {
				t.Fatal("newest-k")
			}
		}
	}
	if got, _ := run.Lookup(0xdeadbeefcafe0000, 0); len(got) != 0 {
		t.Fatal("phantom")
	}
	// iterator reproduces the input
	it := run.Iter()
	for i := 0; it.Next(); i++ {
		if it.Cur != ps[i] {
			t.Fatalf("iter %d: %v vs %v", i, it.Cur, ps[i])
		}
	}
}

func TestRunCorruptionIsDetected(t *testing.T) {
	ps := uniqSorted(randPostings(2, 3000, 300, 100))
	var buf bytes.Buffer
	WriteRun(&buf, ps, 1)
	good := buf.Bytes()
	step := 1 + len(good)/2500
	for i := 0; i < len(good); i += step {
		c := append([]byte{}, good...)
		c[i] ^= 0x10
		run, err := OpenRun(bytes.NewReader(c), int64(len(c)), false)
		if err != nil {
			continue
		}
		if run.Verify() == nil {
			t.Fatalf("byte flip at %d passes CRC", i)
		}
	}
	// truncated / extended files are refused structurally
	for _, n := range []int{0, 10, len(good) - 1, len(good) / 2} {
		if _, err := OpenRun(bytes.NewReader(good[:n]), int64(n), false); err == nil {
			t.Fatalf("truncated %d accepted", n)
		}
	}
}

// Reference model of B1's index semantics for RunSet equivalence.
type refIndex struct {
	buckets map[uint64][]uint32
	dead    map[uint32]bool
}

func (x *refIndex) add(id uint32, f []uint64) {
	for _, h := range f {
		p := x.buckets[h]
		dup := false
		for _, q := range p {
			dup = dup || q == id
		}
		if dup {
			continue
		}
		if len(p) == 64 {
			copy(p, p[1:])
			p = p[:63]
		}
		x.buckets[h] = append(p, id)
	}
}
func (x *refIndex) query(f []uint64, k int) []Candidate2 {
	sc := map[uint32]int{}
	seen := map[uint64]bool{}
	for _, h := range f {
		if seen[h] {
			continue
		}
		seen[h] = true
		for _, id := range x.buckets[h] {
			if !x.dead[id] {
				sc[id]++
			}
		}
	}
	var out []Candidate2
	for id, n := range sc {
		out = append(out, Candidate2{id, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

func TestRunSetEquivalentToReferenceIndex(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	st := newMemStore()
	rs := NewRunSet(st, 700)
	ref := &refIndex{map[uint64][]uint32{}, map[uint32]bool{}}
	hot := make([]uint64, 40)
	for i := range hot {
		hot[i] = r.Uint64()
	}
	check := func(label string) {
		for q := 0; q < 60; q++ {
			var f []uint64
			for i := 0; i < 8; i++ {
				if r.IntN(3) == 0 {
					f = append(f, hot[r.IntN(len(hot))])
				} else {
					f = append(f, r.Uint64())
				}
			}
			got, want := rs.Query(f, 8), ref.query(f, 8)
			if len(got) != len(want) {
				t.Fatalf("%s: len %d vs %d", label, len(got), len(want))
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("%s: rank %d: %v vs %v", label, i, got[i], want[i])
				}
			}
		}
	}
	for id := uint32(1); id <= 1500; id++ {
		var f []uint64
		for i := 0; i < 6; i++ {
			if r.IntN(2) == 0 {
				f = append(f, hot[r.IntN(len(hot))]) // hot features exceed 64 ids -> eviction
			} else {
				f = append(f, r.Uint64())
			}
		}
		rs.Add(id, f)
		ref.add(id, f)
		if id%500 == 0 {
			check("during")
		}
	}
	if rs.Runs() < 3 {
		t.Fatalf("expected several runs, got %d", rs.Runs())
	}
	check("pre-compact")
	if err := rs.Compact(); err != nil {
		t.Fatal(err)
	}
	if rs.Runs() != 1 || len(st.files) != 1 {
		t.Fatalf("after compact runs=%d files=%d", rs.Runs(), len(st.files))
	}
	check("post-compact")
}

// Tombstones: with cold features (< 64 ids each) the live-filtered answer must
// equal the reference exactly, across flushes and a compaction.
func TestRunSetDeleteSemantics(t *testing.T) {
	r := rand.New(rand.NewPCG(8, 8))
	st := newMemStore()
	rs := NewRunSet(st, 300)
	ref := &refIndex{map[uint64][]uint32{}, map[uint32]bool{}}
	feats := map[uint32][]uint64{}
	for id := uint32(1); id <= 900; id++ {
		var f []uint64
		for i := 0; i < 5; i++ {
			f = append(f, uint64(r.IntN(2000))<<30) // ~2.2 ids per feature
		}
		feats[id] = f
		rs.Add(id, f)
		ref.add(id, f)
		if id%7 == 0 {
			d := uint32(1 + r.IntN(int(id)))
			rs.Delete(d)
			ref.dead[d] = true
		}
	}
	cmp := func(label string) {
		for id := uint32(1); id <= 900; id += 3 {
			got, want := rs.Query(feats[id], 8), ref.query(feats[id], 8)
			if len(got) != len(want) {
				t.Fatalf("%s id %d: %v vs %v", label, id, got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("%s id %d rank %d: %v vs %v", label, id, i, got[i], want[i])
				}
			}
		}
	}
	cmp("before-compact")
	if err := rs.Compact(); err != nil {
		t.Fatal(err)
	}
	cmp("after-compact")
}

func TestRunSetFailedFlushKeepsData(t *testing.T) {
	st := newMemStore()
	rs := NewRunSet(st, 10)
	for id := uint32(1); id < 4; id++ {
		rs.Add(id, []uint64{1, 2, 3})
	}
	st.fail = true
	if err := rs.Flush(); err == nil {
		t.Fatal("flush failure not reported")
	}
	if got := rs.Query([]uint64{1}, 4); len(got) != 3 {
		t.Fatalf("memtable lost after failed flush: %v", got)
	}
	st.fail = false
	if err := rs.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestRunSetCorruptRunLowersRecallOnly(t *testing.T) {
	st := newMemStore()
	rs := NewRunSet(st, 5)
	for id := uint32(1); id <= 30; id++ {
		rs.Add(id, []uint64{uint64(id%3) << 50, 99 << 48})
	}
	rs.Flush()
	before := rs.Query([]uint64{99 << 48}, 8)
	// corrupt the ordering of the oldest run's entries
	for gen, b := range st.files {
		for i := runHeader + runDirLen; i+runEntry < len(b)-4; i += runEntry {
			b[i+6], b[i+6+runEntry] = b[i+6+runEntry], b[i+6]
		}
		_ = gen
	}
	after := rs.Query([]uint64{99 << 48}, 8)
	if len(after) > len(before) {
		t.Fatalf("corruption invented candidates: %v vs %v", after, before)
	}
	for _, c := range after {
		if c.ID == 0 || c.ID > 30 {
			t.Fatalf("invented id %d", c.ID)
		}
	}
	if rs.Stats.Corrupt == 0 {
		t.Log("note: swap did not violate bucket order in any bucket read")
	}
}

func TestTombstoneCodec(t *testing.T) {
	b := EncodeTombstones([]uint32{3, 9, 11})
	ids, err := DecodeTombstones(b)
	if err != nil || len(ids) != 3 || ids[2] != 11 {
		t.Fatal(err, ids)
	}
	b[6] ^= 1
	if _, err := DecodeTombstones(b); err == nil {
		t.Fatal("corrupt tombstones accepted")
	}
}
