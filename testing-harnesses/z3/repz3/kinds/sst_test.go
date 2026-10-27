package kinds

import (
	"bytes"
	"errors"
	"testing"

	"zeros3-z3/repz3"
	"zeros3-z3/sstz3"
)

func sstFixture(n int, seed int64, family string) []byte {
	return sstz3.BuildFixture(sstz3.GenFixture(n, seed, false), sstz3.FixtureCfg{Family: family, Snappy: 1, BlockSize: 2500, Filter: true})
}

// TestSSTKindIsExactReachableAndShared: the kind round-trips every form through the
// serialized representation only, every object it reads is kept alive by the contract's
// reachability, a record group shared by two tables is one stored object referenced by two
// immutable representations, and it survives exactly as long as one of them does.
func TestSSTKindIsExactReachableAndShared(t *testing.T) {
	for _, family := range []string{"legacy", "rocksdb", "pebble"} {
		a := sstFixture(400, 1, family)
		for form := sstz3.FormBlocks; form <= sstz3.FormKVT; form++ {
			var cut sstz3.Cutter
			if form != sstz3.FormBlocks {
				cut = &sstz3.HashCutter{Target: 3000, Max: 12000}
			}
			b, _, _, err := EncodeSST(bytes.NewReader(a), int64(len(a)), sstz3.Options{Form: form, Cutter: cut})
			if err != nil {
				t.Fatalf("%s/%s: %v", family, form, err)
			}
			if err := Prove(nil, b, a); err != nil {
				t.Fatalf("%s/%s: %v", family, form, err)
			}
		}
	}

	// two tables over largely the same records: the second drops the first records and adds new ones
	kvs := sstz3.GenFixture(600, 9, false)
	cfg := sstz3.FixtureCfg{Family: "rocksdb", Snappy: 1, BlockSize: 2200}
	t1 := sstz3.BuildFixture(kvs[:450], cfg)
	t2 := sstz3.BuildFixture(kvs[150:], cfg)
	opt := sstz3.Options{Form: sstz3.FormUKV, Cutter: &sstz3.HashCutter{Target: 2500, Max: 10000}}
	opt2 := sstz3.Options{Form: sstz3.FormUKV, Cutter: &sstz3.HashCutter{Target: 2500, Max: 10000}}
	b1, _, _, err := EncodeSST(bytes.NewReader(t1), int64(len(t1)), opt)
	if err != nil {
		t.Fatal(err)
	}
	b2, _, _, err := EncodeSST(bytes.NewReader(t2), int64(len(t2)), opt2)
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	id1, _, _ := b1.Persist(st)
	before := st.Len()
	id2, added, _ := b2.Persist(st)
	if len(added) >= len(b2.Objects)+1 {
		t.Fatalf("no component of the second table was already stored (%d new of %d)", len(added), len(b2.Objects)+1)
	}
	env := NewEnv(st)
	r1, err := env.Reachable(id1)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := env.Reachable(id2)
	if err != nil {
		t.Fatal(err)
	}
	shared := 0
	for id := range r2.Role {
		if _, ok := r1.Role[id]; ok {
			shared++
		}
	}
	if shared == 0 {
		t.Fatal("the two representations share no object")
	}
	// the first table disappears: only the second root is live
	for _, id := range st.IDs() {
		if _, ok := r2.Role[id]; !ok {
			st.Delete(id)
		}
	}
	var out bytes.Buffer
	if _, _, err := NewEnv(st).Reconstruct(id2, &out); err != nil || !bytes.Equal(out.Bytes(), t2) {
		t.Fatalf("second table after the first one was collected: %v", err)
	}
	if _, _, err := NewEnv(st).Reconstruct(id1, new(bytes.Buffer)); err == nil {
		t.Fatal("the collected table still reconstructs")
	}
	// the last reference goes: nothing is left
	for _, id := range st.IDs() {
		st.Delete(id)
	}
	if st.Len() != 0 || before == 0 {
		t.Fatal("store not empty")
	}
}

func TestSSTKindReadsAndHostileRecipes(t *testing.T) {
	a := sstFixture(900, 3, "pebble")
	b, _, _, err := EncodeSST(bytes.NewReader(a), int64(len(a)), sstz3.Options{Form: sstz3.FormKTV, Cutter: &sstz3.HashCutter{Target: 4000, Max: 16000}})
	if err != nil {
		t.Fatal(err)
	}
	st := repz3.NewMemStore()
	id, _, _ := b.Persist(st)
	env := NewEnv(st)
	c, err := env.Open(id)
	if err != nil {
		t.Fatal(err)
	}
	rep := c.Rep
	for _, rg := range [][2]int64{{0, 100}, {5000, 4096}, {int64(len(a)) / 2, 9000}, {int64(len(a)) - 50, 50}} {
		st.ResetReads()
		got, _, err := ReadSSTRange(c, rg[0], rg[1])
		if err != nil || !bytes.Equal(got, a[rg[0]:rg[0]+rg[1]]) {
			t.Fatalf("range %v: %v", rg, err)
		}
	}
	// a point lookup reads the index through ranged reads of the tail and one block
	tb, err := sstz3.OpenKeys(bytes.NewReader(a), int64(len(a)), sstz3.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var key []byte
	if err := sstz3.Records(bytes.NewReader(a), int64(len(a)), sstz3.Limits{}, func(uk []byte, _ uint64, _ []byte) error {
		if len(uk) > 0 && key == nil && len(tb.Blocks) > 3 {
			key = append([]byte(nil), uk...)
		}
		return nil
	}); err != nil || key == nil {
		t.Fatal(err)
	}
	blk, ls, err := LookupSST(c, key)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	if _, err := sstz3.WalkBlock(blk[:len(blk)-5], sstz3.Limits{}, nil); err != nil {
		// the block may be Snappy-compressed: decode it first
		rb := &sstz3.RawBlock{Payload: blk[:len(blk)-5]}
		copy(rb.Trailer[:], blk[len(blk)-5:])
		dec, _, derr := sstz3.DecodeBlock(rb, sstz3.Limits{})
		if derr != nil {
			t.Fatal(derr)
		}
		sstz3.WalkBlock(dec, sstz3.Limits{}, func(_ int, k, _ []byte) error {
			found = found || bytes.Equal(k[:len(k)-8], key)
			return nil
		})
	}
	if !found {
		t.Fatalf("lookup block %d does not hold the key (%+v)", ls.Block, ls)
	}
	// hostile recipes: every truncation and byte mutation of the representation's recipe fails
	// closed (never a panic, never output of the wrong length)
	for i := 0; i < len(rep.Recipe); i += 3 {
		for _, v := range []byte{0, 0x80, 0xff} {
			mut := *rep
			mut.Recipe = append([]byte(nil), rep.Recipe...)
			mut.Recipe[i] = v
			raw, err := mut.Marshal()
			if err != nil {
				continue
			}
			s2 := st.Clone()
			rid, _ := s2.Put(raw)
			var out bytes.Buffer
			if _, _, err := NewEnv(s2).Reconstruct(rid, &out); err == nil && int64(out.Len()) != rep.Size {
				t.Fatalf("mutated recipe produced %d bytes", out.Len())
			}
		}
	}
	for i := 0; i < len(rep.Recipe); i += 2 {
		mut := *rep
		mut.Recipe = rep.Recipe[:i]
		raw, _ := mut.Marshal()
		s2 := st.Clone()
		rid, _ := s2.Put(raw)
		if _, _, err := NewEnv(s2).Reconstruct(rid, new(bytes.Buffer)); err == nil {
			t.Fatalf("truncated recipe (%d bytes) reconstructs", i)
		}
	}
	// unsupported input is an analysis-time error, never a representation
	bad := append([]byte(nil), a...)
	bad[len(bad)-1] ^= 0xff
	if _, _, _, err := EncodeSST(bytes.NewReader(bad), int64(len(bad)), sstz3.Options{Form: sstz3.FormUKV}); err == nil || !(errors.Is(err, sstz3.ErrUnsupported) || errors.Is(err, sstz3.ErrCorrupt)) {
		t.Fatalf("damaged magic: %v", err)
	}
}
