package main

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestFB05ReplayX9 runs the two versioned series of X9 in the actual store.
// Input archives are reconstructed and checked against the F6 hash list.
func TestFB05ReplayX9(t *testing.T) {
	corpus := os.Getenv("ZEROS3_FB05_X9_CORPUS")
	if corpus == "" {
		t.Skip("set ZEROS3_FB05_X9_CORPUS to the verified X9 versioned series")
	}
	sources, err := filepath.Glob(filepath.Join(corpus, "src-series", "*.go"))
	mustOK(t, err)
	trees, err := filepath.Glob(filepath.Join(corpus, "tree-series", "*.tar"))
	mustOK(t, err)
	if len(sources) != 32 || len(trees) != 8 {
		t.Fatalf("want 32 source revisions and 8 tree archives, got %d and %d", len(sources), len(trees))
	}
	mib, err := strconv.ParseInt(os.Getenv("ZEROS3_FB05_X9_BUDGET_MIB"), 10, 64)
	mustOK(t, err)
	s := b4OpenMaint(t, t.TempDir())
	defer s.Close()
	mustOK(t, s.CreateBucket("b"))
	opt := defaultMaintOptions()
	opt.MaxRewriteBytes = mib << 20
	opt.TargetBytes = 4 << 20
	opt.Compress = false
	var lastTree map[string][32]byte
	truth := map[string][32]byte{}
	for step, filename := range sources {
		payload, err := os.ReadFile(filename)
		mustOK(t, err)
		_, err = s.PutObject("b", "src/zeros3.go", payload, "application/octet-stream", nil)
		mustOK(t, err)
		truth["src/zeros3.go"] = sha256.Sum256(payload)
		if (step+1)%4 == 0 {
			current := fb05PutArchive(t, s, trees[step/4])
			for key := range lastTree {
				if _, ok := current[key]; !ok {
					mustOK(t, s.DeleteObject("b", key))
					delete(truth, key)
				}
			}
			for key, sum := range current {
				truth[key] = sum
			}
			lastTree = current
		}
		s.maintPruneHistory(4)
		rep, err := s.maintainOnline(t.Context(), opt)
		mustOK(t, err)
		if rep.Deferred != "" || !rep.LiveSetOK {
			t.Fatalf("incomplete maintenance at step %d: %+v", step+1, rep)
		}
		physical, live := fb05PackFootprint(t, s)
		fb05Result(t, map[string]any{"step": step + 1, "source_budget_mib": mib, "pack_bytes": physical, "live_pack_bytes": live,
			"written": rep.BytesWritten, "reclaimed_gross": rep.PackBytesReclaimed, "retired": rep.PacksRetired, "deferred_sources": rep.PacksDeferred})
	}
	for key, sum := range truth {
		_, got, err := s.GetObject("b", key)
		mustOK(t, err)
		if sha256.Sum256(got) != sum {
			t.Fatalf("replay changed %q", key)
		}
	}
	fb05CompressionDebt(t, s)
}

func TestFB05MixedCorpusDebt(t *testing.T) {
	corpus := os.Getenv("ZEROS3_FB05_MIXED_CORPUS")
	if corpus == "" {
		t.Skip("set ZEROS3_FB05_MIXED_CORPUS to the C14 corpus directory")
	}
	s := b4OpenMaint(t, t.TempDir())
	defer s.Close()
	mustOK(t, s.CreateBucket("b"))
	objects := 0
	err := filepath.WalkDir(corpus, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		key, err := filepath.Rel(corpus, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = s.PutObject("b", filepath.ToSlash(key), data, "application/octet-stream", nil)
		if err == nil {
			objects++
		}
		return err
	})
	mustOK(t, err)
	if objects == 0 {
		t.Fatal("mixed corpus was empty")
	}
	s.sealSegments()
	fb05Result(t, map[string]any{"mixed_objects": objects})
	fb05CompressionDebt(t, s)
}

func fb05PutArchive(t *testing.T, s *Store, path string) map[string][32]byte {
	t.Helper()
	f, err := os.Open(path)
	mustOK(t, err)
	defer f.Close()
	tr := tar.NewReader(f)
	bodies := map[string][]byte{}
	for {
		entry, err := tr.Next()
		if err == io.EOF {
			break
		}
		mustOK(t, err)
		if entry.Typeflag != tar.TypeReg {
			continue
		}
		key := "tree/" + strings.TrimPrefix(entry.Name, "./")
		bodies[key], err = io.ReadAll(tr)
		mustOK(t, err)
	}
	keys := make([]string, 0, len(bodies))
	for key := range bodies {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make(map[string][32]byte, len(keys))
	for _, key := range keys {
		data := bodies[key]
		_, err := s.PutObject("b", key, data, "application/octet-stream", nil)
		mustOK(t, err)
		result[key] = sha256.Sum256(data)
	}
	return result
}

func fb05PackFootprint(t *testing.T, s *Store) (physical, liveBytes int64) {
	t.Helper()
	reachable, err := s.reachabilityScan(false, nil)
	mustOK(t, err)
	if !reachable.OK() {
		t.Fatal("incomplete native reachability scan")
	}
	st := s.packSnap()
	for _, u := range packUsagesForState(st, reachable.liveSet()) {
		if !st.isGone(uint32(u.idx)) && !st.packs[u.idx].open {
			physical += u.Size
			if u.LiveRecords > 0 {
				liveBytes += u.livePhysical()
			}
		}
	}
	return physical, liveBytes
}

func fb05CompressionDebt(t *testing.T, s *Store) {
	t.Helper()
	measure := func() (price, serviceable int64) {
		st := s.packSnap()
		comp := newBlockCompressor(true, 0)
		for sum, loc := range st.primaries() {
			if st.isGone(uint32(loc.pack)) || st.packs[loc.pack].open {
				continue
			}
			price += int64(loc.stored)
			if loc.codec != packCodecRaw {
				continue
			}
			data, err := s.casReadExcluding(*sum, nil)
			mustOK(t, err)
			packed, _ := comp.encode(data)
			serviceable += int64(len(data) - len(packed))
		}
		return
	}
	initial, debt := measure()
	opt := defaultMaintOptions()
	opt.MaxRewriteBytes, opt.MaxStageBytes, opt.TargetBytes = 16<<20, 16<<20, 4<<20
	opt.RecompressRaw = true
	for pass := 1; pass <= 8; pass++ {
		rep, err := s.maintainOnline(context.Background(), opt)
		mustOK(t, err)
		price, remaining := measure()
		fb05Result(t, map[string]any{"compression_pass": pass, "initial_stored": initial, "initial_serviceable_debt": debt,
			"stored": price, "remaining_serviceable_debt": remaining, "candidate_bytes": rep.RawCandidateBytes,
			"probe_bytes": rep.RawProbeBytes, "written": rep.BytesWritten, "declined": rep.RawDeclined, "deferred": rep.Deferred})
		if rep.PacksWritten == 0 {
			break
		}
	}
	reachable, err := s.reachabilityScan(true, nil)
	mustOK(t, err)
	if !reachable.OK() {
		t.Fatal("deep verification failed after series recompression")
	}
}

func fb05Result(t *testing.T, row map[string]any) {
	t.Helper()
	b, err := json.Marshal(row)
	mustOK(t, err)
	fmt.Printf("FB05 %s\n", b)
}
