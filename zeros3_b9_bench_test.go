//go:build b9bench && b6bench && b5locality && b1rangeexperiment

package main

// B9 campaign (not a regression test): go test -tags b9bench,b6bench,b5locality,b1rangeexperiment.
// Environment as B6-B8 (ZEROS3_B6_FX, ZEROS3_B6_CORPUS, ZEROS3_B6_OUT, ZEROS3_B6_FAMILIES, ZEROS3_B6_LANES).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

// b9Families are the pre-registered B9 families (testing-harnesses/next/b9/PREREGISTRATION.md).
func b9Families(c string) []b6Family {
	perFile := func(name, dir string, versions ...string) b6Family {
		f := b6Family{name: name}
		for _, v := range versions {
			root := filepath.Join(c, dir, v)
			var files []string
			filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && d.Name() != ".done" {
					files = append(files, p)
				}
				return nil
			})
			slices.Sort(files)
			for _, p := range files {
				rel, _ := filepath.Rel(root, p)
				f.puts = append(f.puts, b6Put{rel, p})
			}
		}
		return f
	}
	return []b6Family{
		perFile("cu-files", "cu", "9.5", "9.6", "9.7"),
		perFile("go-files", "gofiles", "1.22.0", "1.22.6", "1.23.0", "1.23.4"),
		perFile("rnd-files", "rnd", "0", "1", "2"),
		perFile("synth", "synth", "0", "1", "2"),
	}
}

// b9Lanes: B8 with record files (b8), the B8-G grouped price on record files (b8m: planned, the store is
// written with files as B8 would), and the B9 grouped store (b9).
func b9Lanes() []b6Lane {
	np := AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10}
	op := AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10}
	run := func(p OptimizePolicy, cfg string) func(context.Context, *Store) (any, error) {
		return func(ctx context.Context, s *Store) (any, error) {
			p.Apply, p.AllowDelta, p.AllowRep = true, true, true
			old := agWanted
			defer func() { agWanted = old }()
			agWanted = cfg == "b9"
			if cfg == "b8m" {
				p.recordGroup, p.Apply = 72, false
			}
			return s.AuthorityOptimize(ctx, p)
		}
	}
	lanes := []b6Lane{{"cdc", nil, ""}}
	for _, cfg := range []string{"b8", "b8m", "b9"} {
		lanes = append(lanes, b6Lane{cfg + "-archive", run(OptimizePolicy{AuthPolicy: op, Profile: "archive"}, cfg), ""},
			b6Lane{cfg + "-mixed", run(OptimizePolicy{AuthPolicy: np, Profile: "mixed"}, cfg), ""})
	}
	return lanes
}

func b9Campaign(t *testing.T, test func(*testing.T)) {
	b6FamilySet, b6LaneSet = b9Families, b9Lanes
	defer func() { b6FamilySet, b6LaneSet = b6Families, b6Lanes }()
	test(t)
}

func TestB9Build(t *testing.T) { b9Campaign(t, TestB6Build) }

// TestB9Lanes materializes every lane (B6's runner: optimize, gc + repack + compact, allocation, deep
// verify, the materialized model) and adds the record-store breakdown of each store.
func TestB9Lanes(t *testing.T) {
	b9Campaign(t, func(t *testing.T) {
		TestB6Lanes(t)
		fx := os.Getenv("ZEROS3_B6_FX")
		for _, f := range b9Families(os.Getenv("ZEROS3_B6_CORPUS")) {
			for _, ln := range b9Lanes() {
				if ln.run == nil || !b6Want("ZEROS3_B6_FAMILIES", f.name) || !b6Want("ZEROS3_B6_LANES", ln.name) {
					continue
				}
				dir := filepath.Join(fx, f.name, ln.name)
				if _, err := os.Stat(filepath.Join(dir, ".b9-records")); err == nil {
					continue
				}
				s, done := b6Open(t, dir)
				row := b9RecordStats(t, s, dir)
				row["kind"], row["family"], row["lane"] = "records", f.name, ln.name
				done()
				b6Emit(row)
				b, _ := json.Marshal(row)
				os.WriteFile(filepath.Join(dir, ".b9-records"), b, 0o644)
				t.Logf("%s", b)
			}
		}
	})
}

// b9RecordStats is the physical record store of one lane: record files and group files (allocated), the
// grouped frames, seal indexes, live and dead bytes, depth, and record lookup latency (cold and warm).
func b9RecordStats(t *testing.T, s *Store, dir string) map[string]any {
	row := map[string]any{}
	var fileAlloc, fileN, groupAlloc, groupN int64
	walk := func(root string, fn func(st *syscall.Stat_t, name string)) {
		filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			var st syscall.Stat_t
			if err == nil && !d.IsDir() && syscall.Stat(p, &st) == nil {
				fn(&st, d.Name())
			}
			return nil
		})
	}
	walk(filepath.Join(dir, "bodies"), func(st *syscall.Stat_t, name string) {
		switch filepath.Ext(name) {
		case ".body":
			fileAlloc += st.Blocks * 512
			fileN++
		case agSuffix:
			groupAlloc += st.Blocks * 512
			groupN++
		}
	})
	files, _ := s.authFiles()
	var recBytes, frames, maxDepth int64
	for _, f := range files {
		r, err := s.authLoad(f)
		if err != nil || r == nil {
			t.Fatalf("record %x: %v", f[:4], err)
		}
		b, _ := r.marshal()
		recBytes += int64(len(b))
		maxDepth = max(maxDepth, int64(r.Depth))
		if _, known, _ := s.agLookup(f); known {
			frames += s.agFrameBytes(f)
		}
	}
	ag, err := s.agCompact(false) // reports only: a reclaimed store has nothing below the live threshold
	if err != nil {
		t.Fatal(err)
	}
	row["records"], row["record_bytes"], row["max_depth"] = len(files), recBytes, maxDepth
	row["file_records"], row["file_alloc"], row["groups"], row["group_alloc"] = fileN, fileAlloc, groupN, groupAlloc
	row["frame_bytes"], row["group_bytes"], row["group_live"] = frames, ag.GroupBytes, ag.LiveBytes
	if len(files) > 0 {
		row["alloc_per_record"] = float64(fileAlloc+groupAlloc) / float64(len(files))
	}
	// Record lookup latency: cold (page cache and the index dropped) and warm, over up to 200 records.
	pick := files
	if len(pick) > 200 {
		pick = nil
		for i := range 200 {
			pick = append(pick, files[i*len(files)/200])
		}
	}
	var cold, warm []float64
	for _, f := range pick {
		dropCaches()
		s.auth.ag.mu.Lock()
		s.auth.ag.reset()
		s.auth.ag.mu.Unlock()
		t0 := time.Now()
		if r, err := s.authLoad(f); err != nil || r == nil {
			t.Fatal(err)
		}
		cold = append(cold, float64(time.Since(t0).Microseconds()))
		t0 = time.Now()
		s.authLoad(f)
		warm = append(warm, float64(time.Since(t0).Nanoseconds())/1e3)
	}
	slices.Sort(cold)
	slices.Sort(warm)
	if len(pick) > 0 {
		row["lookup_cold_us_p50"], row["lookup_cold_us_p95"] = cold[len(cold)/2], cold[len(cold)*95/100]
		row["lookup_warm_us_p50"], row["lookup_warm_us_p95"] = warm[len(warm)/2], warm[len(warm)*95/100]
	}
	// Recovery: index rebuild from the group files (open path), cold.
	dropCaches()
	t0 := time.Now()
	s.auth.ag.mu.Lock()
	if err := s.agReloadLocked(); err != nil {
		t.Fatal(err)
	}
	s.auth.ag.mu.Unlock()
	row["index_rebuild_cold_ms"] = float64(time.Since(t0).Microseconds()) / 1e3
	return row
}

func TestB9Reads(t *testing.T)      { b9Campaign(t, TestB6Reads) }
func TestB9Concurrent(t *testing.T) { b9Campaign(t, TestB6Concurrent) }
