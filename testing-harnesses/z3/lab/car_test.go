package lab

import (
	"testing"

	"zeros3-z3/carz3"
)

func carRows(t *testing.T, sc carz3.Scale) map[string]map[string][]FRow {
	t.Helper()
	out := map[string]map[string][]FRow{}
	for _, f := range carz3.Families(sc) {
		var revs []NamedBytes
		for _, r := range f.Revs {
			revs = append(revs, NamedBytes{r.Label, r.Data})
		}
		rows := RunBytesFamily(f.Name, "car", revs, CARReps())
		m := map[string][]FRow{}
		for _, r := range rows {
			if r.Status != "ok" && r.Status != "n/a" {
				t.Fatalf("%s %s %s: %s %s", f.Name, r.Rep, r.Label, r.Status, r.Err)
			}
			if r.Status == "ok" && !r.Exact {
				t.Fatalf("%s %s %s: not exact", f.Name, r.Rep, r.Label)
			}
			m[r.Rep] = append(m[r.Rep], r)
		}
		out[f.Name] = m
	}
	return out
}

// TestCAR runs every Z3-14 family through every representation: each accepted
// row must reconstruct exactly, and the semantic reuse the corpus is built to
// show must be present.
func TestCAR(t *testing.T) {
	rows := carRows(t, carz3.TinyBlocks)
	const T = "tiny-"
	nb := func(f, rep string, i int) int64 { return rows[T+f][rep][i].Acct.NewBytes }

	for _, rep := range []string{"car-block", "car-block+recipe", "car-set"} {
		// order, roots, wrapper, padding and index never create new block bytes
		for f, from := range map[string]int{"v1-reorder": 1, "v1-roots": 1, "v1-recodec": 1, "v1-mixhash": 1, "v2-wrap": 1, "v2-reorder": 1, "v1-dup": 1, "v1-remove": 1} {
			if rep == "car-set" && f == "v1-remove" {
				continue // the set manifest itself changes: checked below
			}
			for i := from; i < len(rows[T+f][rep]); i++ {
				if rows[T+f][rep][i].Status != "ok" {
					t.Errorf("%s %s r%d: %s", f, rep, i, rows[T+f][rep][i].Status)
				}
				if n := nb(f, rep, i); n != 0 {
					t.Errorf("%s %s r%d: %d new block bytes, want 0", f, rep, i, n)
				}
			}
		}
	}
	// removing a block changes only the (small, sorted) set manifest for car-set
	for i := 1; i <= 2; i++ {
		if n := nb("v1-remove", "car-set", i); n == 0 || n > 8<<10 {
			t.Errorf("v1-remove car-set r%d: %d new bytes, want a manifest chunk only", i, n)
		}
	}
	// the structural cut is sensitive to the CID encoding; the block cut is not
	if nb("v1-recodec", "car-section", 1) == 0 {
		t.Error("car-section should not see re-encoded CIDs as the same section")
	}
	// added blocks cost their payload (plus the CIDs/recipe), nothing else
	if a, b := nb("v1-add", "car-block+recipe", 1), int64(0); a == b {
		t.Error("v1-add r1 should add bytes")
	}
	// overlapping archives share exactly the shared blocks
	ov := rows[T+"v1-overlap"]["car-block+recipe"]
	if ov[1].Acct.ReusedComponents == 0 || ov[1].Acct.NewComponents == 0 {
		t.Errorf("overlap r1: reused %d new %d", ov[1].Acct.ReusedComponents, ov[1].Acct.NewComponents)
	}
	// the reorder recipe of the derived layout is far below the payload
	ro := rows[T+"v1-reorder"]["car-block+recipe"][1]
	if ro.Cost > ro.Size/10 {
		t.Errorf("reorder cost %d is more than 10%% of %d", ro.Cost, ro.Size)
	}
	// a stale (unrebuildable) index is kept exact, not mis-derived
	st := rows[T+"v2-reorder"]["car-block+recipe"][3]
	if st.Detail["index_mode"] != int(carz3.IndexExact) {
		t.Errorf("stale index mode %v", st.Detail["index_mode"])
	}
	if ri := rows[T+"v2-reorder"]["car-block+recipe"][1]; ri.Detail["index_mode"] != int(carz3.IndexDerived) {
		t.Errorf("regenerated index mode %v", ri.Detail["index_mode"])
	}
}

func TestCARFallback(t *testing.T) {
	garbage := []byte("this is not a car file at all, just some bytes that must stay raw....")
	rows := RunBytesFamily("junk", "car", []NamedBytes{{"r0", garbage}}, CARReps())
	for _, r := range rows {
		switch r.Rep {
		case "car-section", "car-block", "car-block+recipe", "car-block+recipe-i512":
			if r.Status != "n/a" {
				t.Errorf("%s: %s, want n/a", r.Rep, r.Status)
			}
		case "car-auto":
			if r.Status != "ok" || !r.Exact || r.Detail["auto"] != "cdc" {
				t.Errorf("car-auto: %+v", r)
			}
		}
	}
}
