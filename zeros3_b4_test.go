package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// B4: online GC and repack on a live store (section 13d2).

func b4Body(seed, n int) []byte {
	r := rand.New(rand.NewPCG(uint64(seed), 0xb4))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	return b
}

func b4Opts() MaintOptions {
	o := defaultMaintOptions()
	o.MaxSourcePacks, o.TargetBytes, o.Wait, o.MaxLivePercent = 0, 1<<20, 20*time.Second, 101
	return o
}

func b4OpenMaint(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := openStoreWith(dir, true, true, true)
	mustOK(t, err)
	return s
}

// b4Fixture writes objects with loose chunks, compacts them into packs and
// returns the store directory with the bodies by key.
func b4Fixture(t *testing.T, n, size int) (string, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	mustOK(t, err)
	mustOK(t, s.CreateBucket("b"))
	want := map[string][]byte{}
	for i := range n {
		k := fmt.Sprintf("k%03d", i)
		want[k] = b4Body(i, size)
		_, err := s.PutObject("b", k, want[k], "application/octet-stream", nil)
		mustOK(t, err)
	}
	mustOK(t, s.Close())
	opt := defaultCompactOptions()
	opt.TargetBytes, opt.MinBytes = 1<<20, 1
	_, err = compactStore(dir, opt)
	mustOK(t, err)
	return dir, want
}

func b4Check(t *testing.T, s *Store, want map[string][]byte) {
	t.Helper()
	for k, v := range want {
		_, got, err := s.GetObject("b", k)
		if err != nil || !bytes.Equal(got, v) {
			t.Fatalf("%s: %v (len %d want %d)", k, err, len(got), len(v))
		}
	}
}

func b4Verify(t *testing.T, dir string, want map[string][]byte) {
	t.Helper()
	s, err := OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	b4Check(t, s, want)
	res, err := s.Verify(true)
	mustOK(t, err)
	if !res.OK() {
		t.Fatalf("deep verify after maintenance: %+v", res.Issues)
	}
}

func b4Prune(t *testing.T, s *Store) {
	t.Helper()
	zero := 0
	res, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", KeepLast: &zero})
	mustOK(t, err)
	mustOK(t, s.ApplyHistoryPrune(&res))
}

func TestB4OpGateWaitsForEarlierOperations(t *testing.T) {
	var g opGate
	old := g.enter()
	done := make(chan error, 1)
	go func() { done <- g.wait(context.Background(), time.Minute) }()
	time.Sleep(10 * time.Millisecond)
	late := g.enter() // entered after the wait began: not waited for
	select {
	case err := <-done:
		t.Fatalf("wait returned (%v) while an earlier operation was running", err)
	default:
	}
	g.exit(old)
	mustOK(t, <-done)
	if err := g.wait(context.Background(), 20*time.Millisecond); !errors.Is(err, errMaintWait) {
		t.Fatalf("a running operation did not bound the wait: %v", err)
	}
	g.exit(late)
	mustOK(t, g.wait(context.Background(), time.Second))
}

func TestB4PinTableCondemnReviveHold(t *testing.T) {
	var p pinTable
	a, b := sha256.Sum256([]byte("a")), sha256.Sum256([]byte("b"))
	p.pin(a)
	if p.condemn(a) {
		t.Fatal("a pinned key was condemned")
	}
	if !p.condemn(b) {
		t.Fatal("an unpinned key was not condemned")
	}
	p.pin(b) // revives
	if !p.revived(b) {
		t.Fatal("a pin of a condemned key did not revive it")
	}
	c := sha256.Sum256([]byte("c"))
	p.condemn(c)
	ch := make(chan struct{})
	if rv := p.hold([][32]byte{b, c}, ch); len(rv) != 1 || rv[0] != b {
		t.Fatalf("hold reported %d revived keys", len(rv))
	}
	pinned := make(chan struct{})
	go func() { p.pin(c); close(pinned) }()
	select {
	case <-pinned:
		t.Fatal("a pin did not wait for a held key")
	case <-time.After(20 * time.Millisecond):
	}
	p.release([][32]byte{b, c}, ch, true)
	<-pinned
	if p.condemn(c) {
		t.Fatal("a key pinned after its unlink was condemned again")
	}
}

// A pass on a live store removes what history pruning, deletes and an aborted
// upload left behind, keeps every live object, multipart part and snapshot
// root, and the store reopens and deep-verifies.
func TestB4OnlineReclaimsDeadData(t *testing.T) {
	dir, want := b4Fixture(t, 24, 96<<10)
	old := smallObjectSegments
	smallObjectSegments = false // online writes go loose: loose garbage too
	t.Cleanup(func() { smallObjectSegments = old })
	s := b4OpenMaint(t, dir)
	defer s.Close()
	// Overwrite half the keys twice and delete a few; prune history so their
	// old versions (packed and loose) lose every root.
	for round := range 2 {
		for i := range 12 {
			k := fmt.Sprintf("k%03d", i)
			want[k] = b4Body(1000*(round+1)+i, 80<<10)
			_, err := s.PutObject("b", k, want[k], "application/octet-stream", nil)
			mustOK(t, err)
		}
	}
	for i := 20; i < 24; i++ {
		mustOK(t, s.DeleteObject("b", fmt.Sprintf("k%03d", i)))
		delete(want, fmt.Sprintf("k%03d", i))
	}
	// An open upload keeps its parts; an aborted one does not.
	open, err := s.CreateMultipartUpload("b", "mp", "", nil)
	mustOK(t, err)
	part := b4Body(5000, 70<<10)
	_, err = s.UploadPart("b", "mp", open, 1, part)
	mustOK(t, err)
	gone, err := s.CreateMultipartUpload("b", "mp2", "", nil)
	mustOK(t, err)
	_, err = s.UploadPart("b", "mp2", gone, 1, b4Body(5001, 70<<10))
	mustOK(t, err)
	mustOK(t, s.AbortMultipartUpload("b", "mp2", gone))
	// A snapshot keeps k019's current content alive after it is deleted.
	entries, err := s.captureSnapshotEntries("b", "")
	mustOK(t, err)
	mustOK(t, s.publishSnapshot(snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(), SourceBucket: "b", Entries: entries}))
	snapBody := want["k019"]
	mustOK(t, s.DeleteObject("b", "k019"))
	delete(want, "k019")
	b4Prune(t, s)

	time.Sleep(10 * time.Millisecond) // files from before the mark are strictly older than T0
	rep, err := s.maintainOnline(context.Background(), b4Opts())
	mustOK(t, err)
	t.Logf("pass: %+v", rep)
	if rep.BytesReclaimed == 0 || rep.PacksRetired == 0 || rep.ChunksDeleted == 0 {
		t.Fatalf("nothing reclaimed: %+v", rep)
	}
	if rep.Deferred != "" || !rep.LiveSetOK {
		t.Fatalf("pass deferred or unsafe: %+v", rep)
	}
	b4Check(t, s, want)
	parts, err := s.ListParts("b", "mp", open)
	mustOK(t, err)
	if len(parts) != 1 {
		t.Fatal("the open upload lost its part")
	}
	_, man, err := s.CompleteMultipartUpload("b", "mp", open, []completedPart{{PartNumber: 1, ETag: parts[0].etag}})
	mustOK(t, err)
	want["mp"] = part
	if man.TotalLength != int64(len(part)) {
		t.Fatal("completed upload has the wrong length")
	}
	b4Check(t, s, want)
	// A second pass converges: nothing more to retire.
	rep2, err := s.maintainOnline(context.Background(), b4Opts())
	mustOK(t, err)
	if rep2.PacksRetired != 0 || rep2.ChunksDeleted != 0 || rep2.PendingPacks != 0 {
		t.Fatalf("second pass still reclaims: %+v", rep2)
	}
	mustOK(t, s.Close())
	b4Verify(t, dir, want)
	// The snapshot still names k019's content: its bytes survived.
	s, err = OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	snaps, err := s.listSnapshots()
	mustOK(t, err)
	for _, e := range snaps[0].Entries {
		if e.Key == "k019" {
			sha, _ := decodeHexSHA256(e.ManifestSHA256)
			man, _, err := s.readManifestSHA(e.ManifestUUID, sha)
			mustOK(t, err)
			got, err := s.readManifestRange(man, byteRange{0, man.TotalLength - 1})
			if err != nil || !bytes.Equal(got, snapBody) {
				t.Fatalf("snapshot content lost: %v", err)
			}
		}
	}
}

// b4DeadContent puts body under key, deletes it and prunes history, so the
// body's chunks are dead but still physically present (in packs).
func b4DeadFixture(t *testing.T) (string, map[string][]byte, []byte) {
	t.Helper()
	dir, want := b4Fixture(t, 16, 90<<10)
	dead := want["k003"]
	s, err := OpenStore(dir)
	mustOK(t, err)
	mustOK(t, s.DeleteObject("b", "k003"))
	delete(want, "k003")
	b4Prune(t, s)
	mustOK(t, s.Close())
	return dir, want, dead
}

// A writer that reuses the digests of dead content while a pass runs keeps its
// object, whether its pins land before condemnation (late), between
// condemnation and the locator swap (revived: carried before the swap), or
// after the swap (absent: written again).
func TestB4DeadDigestReusedDuringPass(t *testing.T) {
	for _, tc := range []struct {
		point string
		check func(MaintReport) bool
	}{
		{hookMaintReconciled, func(r MaintReport) bool { return r.LateRecords > 0 }},
		{hookMaintCondemned, func(r MaintReport) bool { return r.RevivedRecords > 0 }},
		{hookMaintSwapped, func(r MaintReport) bool { return r.PacksRetired > 0 }},
	} {
		t.Run(tc.point, func(t *testing.T) {
			dir, want, dead := b4DeadFixture(t)
			s := b4OpenMaint(t, dir)
			defer s.Close()
			var once sync.Once
			testHook = func(p string) {
				if p == tc.point {
					once.Do(func() {
						_, err := s.PutObject("b", "reborn", dead, "application/octet-stream", nil)
						mustOK(t, err)
					})
				}
			}
			defer func() { testHook = nil }()
			time.Sleep(10 * time.Millisecond)
			rep, err := s.maintainOnline(context.Background(), b4Opts())
			testHook = nil
			mustOK(t, err)
			if !tc.check(rep) || rep.PacksRetired == 0 {
				t.Fatalf("the reuse was not exercised as intended: %+v", rep)
			}
			want["reborn"] = dead
			b4Check(t, s, want)
			mustOK(t, s.Close())
			b4Verify(t, dir, want)
		})
	}
}

// A PUT that began after the mark and whose chunks and manifest are published
// but not yet committed when everything dead is condemned commits afterwards
// and stays readable; so does one that began before the mark (G1 waits for it).
func TestB4InFlightPutAcrossPass(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprintf("before-mark=%v", early), func(t *testing.T) {
			dir, want, dead := b4DeadFixture(t)
			s := b4OpenMaint(t, dir)
			defer s.Close()
			body := append(append([]byte(nil), dead...), b4Body(77, 40<<10)...) // reuses dead chunks plus new bytes
			published, release := make(chan struct{}), make(chan struct{})
			var relOnce sync.Once
			free := func() { relOnce.Do(func() { close(release) }) }
			var armed atomic.Bool
			armed.Store(true)
			var putErr error
			var putDone atomic.Bool
			var wg sync.WaitGroup
			startPut := func() {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, putErr = s.PutObject("b", "inflight", body, "application/octet-stream", nil)
					putDone.Store(true)
				}()
			}
			testHook = func(p string) {
				switch p {
				case hookAfterManifestPublished:
					if armed.CompareAndSwap(true, false) {
						close(published)
						<-release
					}
				case hookMaintReconciled: // after G1: the PUT is tracked from its start
					if early && !putDone.Load() {
						t.Error("G1 returned while an operation older than the mark was running")
					}
					if !early {
						startPut()
						<-published
					}
				case hookMaintCondemned:
					if !early {
						free() // commits during the swap and the reader drain
					}
				}
			}
			defer func() { testHook = nil }()
			if early {
				startPut()
				<-published
				time.AfterFunc(100*time.Millisecond, free)
			}
			rep, err := s.maintainOnline(context.Background(), b4Opts())
			free()
			wg.Wait()
			testHook = nil
			mustOK(t, err)
			mustOK(t, putErr)
			if rep.PacksRetired == 0 || rep.LateRecords == 0 && rep.RevivedRecords == 0 && !early {
				t.Fatalf("nothing retired or carried late: %+v", rep)
			}
			want["inflight"] = body
			b4Check(t, s, want)
			mustOK(t, s.Close())
			b4Verify(t, dir, want)
		})
	}
}

// A reader that opened an object before its content died, or that holds an old
// locator generation naming a retired pack, keeps reading: nothing it can name
// is unlinked until it finishes, and the pass defers the unlink instead of
// waiting past its limit. A later pass completes the retirement.
func TestB4OldReadersSurviveRetirement(t *testing.T) {
	dir, want := b4Fixture(t, 16, 90<<10)
	s := b4OpenMaint(t, dir)
	defer s.Close()
	done := s.beginRead()
	_, man, err := s.HeadObject("b", "k005")
	mustOK(t, err)
	_, liveMan, err := s.HeadObject("b", "k006")
	mustOK(t, err)
	rd := s.openObjectReader(man, byteRange{0, man.TotalLength - 1})
	first, err := rd.next()
	mustOK(t, err)
	old := s.packSnap()
	mustOK(t, s.DeleteObject("b", "k005"))
	b4Prune(t, s)
	time.Sleep(10 * time.Millisecond)
	opt := b4Opts()
	opt.Wait = 200 * time.Millisecond
	rep, err := s.maintainOnline(context.Background(), opt)
	mustOK(t, err)
	if rep.Deferred == "" || rep.PendingPacks == 0 {
		t.Fatalf("the pass did not defer for the old reader: %+v", rep)
	}
	if s.packSnap() == old {
		t.Fatal("the locator was not replaced")
	}
	got := append([]byte(nil), first...)
	for {
		b, err := rd.next()
		if err != nil {
			break
		}
		got = append(got, b...)
	}
	rd.close()
	if !bytes.Equal(got, want["k005"]) {
		t.Fatal("an old reader lost the content of an object deleted under it")
	}
	if data, err := s.readManifestRangeWith(old, liveMan); err != nil || !bytes.Equal(data, want["k006"]) {
		t.Fatalf("a read through the old locator failed: %v", err)
	}
	done()
	delete(want, "k005")
	rep, err = s.maintainOnline(context.Background(), b4Opts())
	mustOK(t, err)
	if rep.PendingPacks != 0 || rep.PacksRetired == 0 {
		t.Fatalf("pending retirement was not completed: %+v", rep)
	}
	b4Check(t, s, want)
	mustOK(t, s.Close())
	b4Verify(t, dir, want)
}

// readManifestRangeWith reads a whole object through one locator snapshot.
func (s *Store) readManifestRangeWith(st *packState, man manifestV1) ([]byte, error) {
	var out []byte
	for _, c := range man.Chunks {
		sum, err := decodeHexSHA256(c.SHA256)
		if err != nil {
			return nil, err
		}
		loc, ok := st.lookup(sum)
		if !ok {
			return nil, fmt.Errorf("chunk %x not in the snapshot", sum[:6])
		}
		data, err := st.readPacked(sum, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, data...)
	}
	return out, nil
}
