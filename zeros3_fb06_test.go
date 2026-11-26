package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFB06TagLevelCollisionAndReadFailure(t *testing.T) {
	dir, want := b4Fixture(t, 16, 72<<10)
	_, err := buildLocatorGeneration(dir, fb06Format(t, dir))
	mustOK(t, err)
	s, err := openStoreWithLocator(dir, true, true, false, false, true)
	mustOK(t, err)
	defer s.Close()
	st := s.packSnap()
	ix := st.levels[0]
	if ix.disk == nil || len(ix.recs) != 0 || len(ix.disk.tags) == 0 {
		t.Fatal("open did not install the compact level")
	}
	var first *locRec
	for rec := range ix.iterPrimaries() {
		first = rec
		break
	}
	if first == nil {
		t.Fatal("no generated digest")
	}
	known := first.sum
	var absent [32]byte
	const conflicts = 1000
	for i := 1; i <= conflicts; i++ {
		altered := known
		tail := binary.LittleEndian.Uint32(known[28:])
		binary.LittleEndian.PutUint32(altered[28:], tail^uint32(i))
		if locatorTag(&known, 32-ix.shift) != locatorTag(&altered, 32-ix.shift) {
			t.Fatal("mutation changed the tag")
		}
		if _, ok := s.packLookup(altered); ok || len(s.packLocs(altered)) != 0 {
			t.Fatal("tag collision supplied a pack location")
		}
		if _, err := s.casStat(altered); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tag collision supplied CAS presence: %v", err)
		}
		if _, err := s.casRead(altered); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tag collision supplied CAS content: %v", err)
		}
		if _, err := s.casStatLocked(altered); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("tag collision supplied publisher presence: %v", err)
		}
		var batched [2]*locRec
		st.bestMany([][32]byte{altered, known}, batched[:])
		if batched[0] != nil || batched[1] == nil || batched[1].sum != known {
			t.Fatal("batched lookup accepted collision or lost exact hit")
		}
		absent = altered
	}
	if _, _, _, err := s.casReadSpan(absent, int64(first.logical()), 0, 1, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tag collision supplied range data: %v", err)
	}
	knownID, absentID := hex.EncodeToString(known[:]), hex.EncodeToString(absent[:])
	for _, usage := range s.packUsagesLive(liveSetFromMap(map[string]bool{absentID: true})) {
		if usage.LiveRecords != 0 {
			t.Fatal("tag collision changed recorded liveness")
		}
	}
	request, err := json.Marshal(syncNegotiateRequest{Protocol: zeros3SyncProtocolVersion, CDC: zeros3SyncCDCFormat,
		Hash: zeros3SyncHashAlgorithm, Chunks: []syncChunkDescriptor{
			{SHA256: knownID, Length: int64(first.logical())}, {SHA256: absentID, Length: int64(first.logical())},
		}})
	mustOK(t, err)
	recorder := httptest.NewRecorder()
	(&Server{store: s}).handleSyncNegotiate(recorder, request)
	var response syncNegotiateResponse
	mustOK(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	if recorder.Code != http.StatusOK || len(response.Missing) != 1 || response.Missing[0] != absentID {
		t.Fatalf("negotiation omitted the absent exact digest: %s", recorder.Body.String())
	}
	b4Check(t, s, want)
	for i := range 12 {
		_, err := s.PutObject("b", fmt.Sprintf("young-%02d", i), b4Body(300+i, 320<<10), "application/octet-stream", nil)
		mustOK(t, err)
	}
	if latest := s.packSnap(); len(latest.levels) > locMaxLevels || latest.levels[0] != ix {
		t.Fatalf("young publications changed the base or level bound: %d levels", len(latest.levels))
	}

	// The server retains the open generation descriptor across replacements.
	// Model a local failed sidecar read and verify the canonical-pack fallback.
	f, err := os.OpenFile(filepath.Join(dir, locatorGenerationName), os.O_WRONLY, 0)
	mustOK(t, err)
	mustOK(t, f.Truncate(0))
	mustOK(t, f.Close())
	b4Check(t, s, want)
	if ix.disk.fallback.Load() == nil {
		t.Fatal("sidecar read failure did not rebuild pack indexes")
	}
	body := b4Body(2359, 56<<10)
	_, err = s.PutObject("b", "young-write", body, "application/octet-stream", nil)
	mustOK(t, err)
	_, got, err := s.GetObject("b", "young-write")
	mustOK(t, err)
	if sha256.Sum256(body) != sha256.Sum256(got) {
		t.Fatal("young write changed after cold fallback")
	}
	t.Logf("verified %d forced equal-tag misses and reconstructed the same pack locations after truncation", conflicts)
}

func fb06Format(t *testing.T, root string) storeFormat {
	t.Helper()
	f, err := loadOrInitFormat(root, filepath.Join(root, "FORMAT.json"))
	mustOK(t, err)
	return f
}

func TestFB06GenerationRoundTripAndFallback(t *testing.T) {
	dir, want := b4Fixture(t, 16, 72<<10)
	format := fb06Format(t, dir)
	packs, err := buildLocatorGeneration(dir, format)
	mustOK(t, err)
	if packs == 0 {
		t.Fatal("fixture has no sealed packs")
	}
	open := func(hit, maintain bool) *Store {
		t.Helper()
		s, err := openStoreWithLocator(dir, true, true, maintain, true, false)
		mustOK(t, err)
		if s.locatorFromGeneration.Load() != hit {
			t.Fatalf("locator generation hit=%v, want %v", s.locatorFromGeneration.Load(), hit)
		}
		b4Check(t, s, want)
		return s
	}
	s := open(true, true)
	mustOK(t, s.CreateBucket("new-journal-root"))
	_, err = s.maintainOnline(context.Background(), b4Opts())
	mustOK(t, err)
	if s.locatorFromGeneration.Load() {
		t.Fatal("maintenance retained generation-derived inventory")
	}
	mustOK(t, s.Close())
	s = open(true, false)
	mustOK(t, s.HeadBucket("new-journal-root"))
	mustOK(t, s.Close())

	files, err := listPackInventory(dir, format.StoreFormatVersion)
	mustOK(t, err)
	if _, err := loadLocatorGeneration(dir, "wrong-store", files); err == nil {
		t.Fatal("foreign store accepted")
	}
	path := filepath.Join(dir, locatorGenerationName)
	good, err := os.ReadFile(path)
	mustOK(t, err)
	damaged := bytes.Clone(good)
	damaged[len(damaged)-6] ^= 1
	mustOK(t, os.WriteFile(path, damaged, 0o600))
	s = open(false, false)
	mustOK(t, s.Close())
	mustOK(t, os.WriteFile(path, good[:len(good)-1], 0o600))
	s = open(false, false)
	mustOK(t, s.Close())

	mustOK(t, os.WriteFile(path, good, 0o600))
	s = open(true, false)
	large := b4Body(420, 2<<20)
	_, err = s.PutObject("b", "changes-pack-set", large, "application/octet-stream", nil)
	mustOK(t, err)
	want["changes-pack-set"] = large
	mustOK(t, s.Close())
	s = open(false, false)
	mustOK(t, s.Close())
	mustOK(t, os.Remove(path))
	s = open(false, false)
	mustOK(t, s.Close())
	_, err = buildLocatorGeneration(dir, format)
	mustOK(t, err)
	s = open(true, false)
	mustOK(t, s.Close())
	b4Verify(t, dir, want)
}

func TestFB06GenerationPublishCut(t *testing.T) {
	if root := os.Getenv("ZEROS3_FB06_CUT_ROOT"); root != "" {
		testHook = func(point string) {
			if point == hookLocatorBeforePublish {
				os.Exit(73)
			}
		}
		_, err := buildLocatorGeneration(root, fb06Format(t, root))
		t.Fatalf("did not hit generation publication cut: %v", err)
	}
	dir, want := b4Fixture(t, 8, 80<<10)
	format := fb06Format(t, dir)
	_, err := buildLocatorGeneration(dir, format)
	mustOK(t, err)
	path := filepath.Join(dir, locatorGenerationName)
	prior, err := os.ReadFile(path)
	mustOK(t, err)
	child := exec.Command(os.Args[0], "-test.run", "^TestFB06GenerationPublishCut$")
	child.Env = append(os.Environ(), "ZEROS3_FB06_CUT_ROOT="+dir)
	out, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 73 {
		t.Fatalf("publication cut: %v, %s", err, out)
	}
	current, err := os.ReadFile(path)
	mustOK(t, err)
	if !bytes.Equal(prior, current) {
		t.Fatal("process interruption altered the existing generation")
	}
	s, err := openStoreWithLocator(dir, true, true, false, true, false)
	mustOK(t, err)
	if !s.locatorFromGeneration.Load() {
		t.Fatal("predecessor did not open after publication cut")
	}
	b4Check(t, s, want)
	mustOK(t, s.Close())
}

func TestFB06GenerationCannotResurrectRetiredPacks(t *testing.T) {
	dir, want := b4Fixture(t, 14, 96<<10)
	format := fb06Format(t, dir)
	_, err := buildLocatorGeneration(dir, format)
	mustOK(t, err)
	s, err := openStoreWithLocator(dir, true, true, true, false, true)
	mustOK(t, err)
	if !s.locatorFromGeneration.Load() {
		t.Fatal("expected initial tag generation")
	}
	omitted := 0
	for key := range want {
		if omitted < 12 {
			mustOK(t, s.DeleteObject("b", key))
			delete(want, key)
			omitted++
		}
	}
	b4Prune(t, s)
	rep, err := s.maintainOnline(context.Background(), b4Opts())
	mustOK(t, err)
	if rep.PacksRetired == 0 || s.locatorFromGeneration.Load() {
		t.Fatalf("retirement did not switch to authority: %+v", rep)
	}
	mustOK(t, s.Close())
	s, err = openStoreWithLocator(dir, true, true, false, false, true)
	mustOK(t, err)
	if s.locatorFromGeneration.Load() {
		t.Fatal("reopen accepted generation containing retired packs")
	}
	b4Check(t, s, want)
	mustOK(t, s.Close())
	b4Verify(t, dir, want)
}
