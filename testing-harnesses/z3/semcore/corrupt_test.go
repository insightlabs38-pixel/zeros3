package semcore

import (
	"bytes"
	"errors"
	"math/rand"
	"testing"
)

// familyStore builds a store whose components exercise every record shape: raw, converted,
// layout-permuted and gathered/residual derivations. It returns the backend, the recipes and
// the original files.
func familyStore(t *testing.T) (*MemBackend, []*Recipe, [][]byte) {
	t.Helper()
	r := rand.New(rand.NewSource(33))
	base := make([]byte, 2*64*48)
	r.Read(base)
	wide, _ := convert(base, DBF16, DF32)
	wide[100] ^= 0x01 // a residual: the F32 file is not an exact widening
	tr, _ := permute(base, 2, []uint64{64, 48}, []uint8{1, 0})
	files := [][]byte{
		stFile(vt{"w", "BF16", []int64{64, 48}, base}),
		stFile(vt{"w", "F32", []int64{64, 48}, wide}),
		stFile(vt{"wt", "BF16", []int64{48, 64}, tr}),
	}
	be := NewMemBackend()
	s, _ := NewStore(be)
	var recs []*Recipe
	for i, f := range files {
		recs = append(recs, placeTensorFile(t, s, string(rune('a'+i)), f))
	}
	return be, recs, files
}

func replayAllOK(s *Store, recs []*Recipe, files [][]byte) (failed int, wrong bool) {
	for i, rc := range recs {
		var out bytes.Buffer
		if err := s.Replay(rc, &out, 1<<20); err != nil {
			failed++
		} else if !bytes.Equal(out.Bytes(), files[i]) {
			wrong = true
		}
	}
	return
}

// Corruption at every level either fails closed or leaves the output exact; never wrong bytes.
func TestCorruptionMatrixNeverWrongBytes(t *testing.T) {
	be, recs, files := familyStore(t)
	// 1. component blobs: flip a byte at several offsets of every blob
	for _, id := range be.BlobIDs() {
		for _, at := range []int{0, 7, 1000, len(be.blobs[id]) - 1} {
			if at < 0 || at >= len(be.blobs[id]) {
				continue
			}
			be.Corrupt(id, at)
			s, _ := NewStore(be)
			if _, wrong := replayAllOK(s, recs, files); wrong {
				t.Fatalf("blob %s flip at %d produced wrong bytes", id.Short(), at)
			}
			be.Corrupt(id, at) // restore
		}
	}
	// 2. records (descriptor, dtype/layout, parents, ops, residual patches): every byte flip of
	// every record must be rejected at open, change identity, or still replay exactly
	var recIDs []ID
	_ = be.ListRecords(func(id ID, b []byte) error { recIDs = append(recIDs, id); return nil })
	for _, id := range recIDs {
		orig := append([]byte(nil), be.recs[id]...)
		for i := range orig {
			be.recs[id] = append([]byte(nil), orig...)
			be.recs[id][i] ^= 0x21
			s, err := NewStore(be)
			if err != nil {
				continue // rejected at open: fail closed
			}
			if _, wrong := replayAllOK(s, recs, files); wrong {
				t.Fatalf("record %s byte %d flip produced wrong bytes", id.Short(), i)
			}
		}
		be.recs[id] = orig
	}
	// 3. recipes: flips are rejected by the parser or fail verification
	for _, rc := range recs {
		w := rc.Marshal()
		s, _ := NewStore(be)
		for i := range w {
			m := append([]byte(nil), w...)
			m[i] ^= 0x10
			rr, err := ParseRecipe(m)
			if err != nil {
				continue
			}
			var out bytes.Buffer
			if err := s.Replay(rr, &out, 1<<20); err == nil && !bytes.Equal(out.Bytes(), files[indexOf(recs, rc)]) {
				// a flipped SHA with matching output is impossible; a wrong output must have failed
				t.Fatalf("recipe flip at %d produced wrong bytes without error", i)
			}
		}
	}
}

func indexOf(rs []*Recipe, r *Recipe) int {
	for i := range rs {
		if rs[i] == r {
			return i
		}
	}
	return -1
}

// A corrupt derived index can lower discovery but never establish a false equivalence: bogus
// entries in the payload and element-count indexes are ignored because every acceptance is
// proven by content.
func TestPoisonedIndexesNeverCreateFalseEquivalence(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	a := make([]byte, 2*64*64)
	b := make([]byte, 2*64*64)
	r.Read(a)
	r.Read(b)
	s, _ := NewStore(NewMemBackend())
	p := NewPlanner(s, DefaultOptions())
	placeTensorFile(t, s, "a", stFile(vt{"w", "BF16", []int64{64, 64}, a}))
	// poison: claim b's payload digest and the element count belong to a's component
	var aid ID
	for _, x := range s.All() {
		aid = x.CID
	}
	n, _ := s.Rec(aid).Desc.Elems()
	s.mu.Lock()
	s.byPayload[PayloadSHA(b)] = append(s.byPayload[PayloadSHA(b)], aid)
	s.byElems[n] = append(s.byElems[n], aid, aid, ID{9})
	s.mu.Unlock()
	f := stFile(vt{"w", "BF16", []int64{64, 64}, b})
	doc, _ := DetectAt("b", bytes.NewReader(f), int64(len(f)))
	rec, _, err := p.Ingest(doc)
	if err != nil {
		t.Fatalf("poisoned index must degrade, not fail: %v", err)
	}
	var out bytes.Buffer
	if err := s.Replay(rec, &out, 1<<20); err != nil || !bytes.Equal(out.Bytes(), f) {
		t.Fatalf("wrong bytes or error under a poisoned index: %v", err)
	}
}

// Hostile descriptors and records: bounds hold, nothing panics.
func TestHostileRecordsRejected(t *testing.T) {
	r := rand.New(rand.NewSource(8))
	for i := 0; i < 5000; i++ {
		b := make([]byte, r.Intn(300))
		r.Read(b)
		if r.Intn(2) == 0 && len(b) > 4 {
			copy(b, RecMagic)
		}
		if rr, err := ParseRecord(b); err == nil {
			// accepted bytes must re-serialize identically (one canonical form)
			if !bytes.Equal(rr.Marshal(), b) {
				t.Fatalf("non-canonical record accepted: %x", b)
			}
		}
	}
	if _, err := ParseRecord(nil); !errors.Is(err, ErrCorrupt) {
		t.Fatal("empty record accepted")
	}
}
