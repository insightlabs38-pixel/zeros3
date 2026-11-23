package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
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
	if b4FixturePackBytes < 1<<20 { // loose ingest, so compaction cuts several packs
		old := smallObjectSegments
		smallObjectSegments = false
		defer func() { smallObjectSegments = old }()
	}
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
	opt.TargetBytes, opt.MinBytes = b4FixturePackBytes, 1
	_, err = compactStore(dir, opt)
	mustOK(t, err)
	return dir, want
}

var b4FixturePackBytes int64 = 1 << 20

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
	p.pin(b) // revives: the hold below reports it
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

// ---- Milestone B: failure, recovery, processes and the server workflow ----

// b4Garbage turns part of a fixture into garbage of every kind: packed
// history (pruned), loose chunks of an aborted upload and deleted objects.
func b4Garbage(t *testing.T, s *Store, want map[string][]byte) {
	t.Helper()
	old := smallObjectSegments
	smallObjectSegments = false
	defer func() { smallObjectSegments = old }()
	for i := range 4 {
		k := fmt.Sprintf("loose%d", i)
		_, err := s.PutObject("b", k, b4Body(9200+i, 30<<10), "application/octet-stream", nil)
		mustOK(t, err)
		mustOK(t, s.DeleteObject("b", k))
	}
	for i := 0; i < 8; i += 2 { // every other key: packs end up partly dead
		k := fmt.Sprintf("k%03d", i)
		want[k] = b4Body(9000+i, 70<<10)
		_, err := s.PutObject("b", k, want[k], "application/octet-stream", nil)
		mustOK(t, err)
	}
	for i := 9; i < 16; i += 2 {
		mustOK(t, s.DeleteObject("b", fmt.Sprintf("k%03d", i)))
		delete(want, fmt.Sprintf("k%03d", i))
	}
	up, err := s.CreateMultipartUpload("b", "aborted", "", nil)
	mustOK(t, err)
	_, err = s.UploadPart("b", "aborted", up, 1, b4Body(9100, 60<<10))
	mustOK(t, err)
	mustOK(t, s.AbortMultipartUpload("b", "aborted", up))
	b4Prune(t, s)
}

// TestB4HelperProcess is the child of the process tests (ZS3_B4_HELPER).
func TestB4HelperProcess(t *testing.T) {
	scenario, dir := os.Getenv("ZS3_B4_HELPER"), os.Getenv("ZS3_B4_DIR")
	switch scenario {
	case "":
		return
	case "owner":
		s, err := OpenStore(dir)
		if err == nil {
			s.Close()
			fmt.Println("opened")
		} else if errors.Is(err, errMaintOwned) {
			fmt.Println("refused")
		} else {
			fmt.Println("error", err)
		}
	case "shared":
		s, err := OpenStoreShared(dir)
		mustOK(t, err)
		fmt.Println("opened")
		io.Copy(io.Discard, os.Stdin) // hold the store until the parent closes stdin
		s.Close()
	case "cut":
		point := os.Getenv("ZS3_B4_POINT")
		occ, _ := strconv.Atoi(os.Getenv("ZS3_B4_OCC"))
		var inPass atomic.Bool
		var n atomic.Int64
		testHook = func(p string) {
			if p == point && inPass.Load() && int(n.Add(1)) == occ {
				os.Exit(93)
			}
		}
		smallObjectSegments = false // the writer's chunks are loose, the pass's own packs only
		s := b4OpenMaint(t, dir)
		stop := make(chan struct{})
		go func() { // acknowledged writes race the pass; the parent checks each one
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				k := fmt.Sprintf("w%04d", i)
				if _, err := s.PutObject("b", k, b4Body(20000+i, 3000+i%7*1000), "application/octet-stream", nil); err != nil {
					t.Error(err)
					return
				}
				fmt.Printf("ack %s %d\n", k, i)
			}
		}()
		time.Sleep(20 * time.Millisecond)
		inPass.Store(true)
		rep, err := s.maintainOnline(context.Background(), b4Opts())
		inPass.Store(false)
		close(stop)
		mustOK(t, err)
		fmt.Printf("pass complete: %d candidates, %d retired\n", rep.CandidatePacks, rep.PacksRetired)
	}
	os.Exit(0)
}

func b4Helper(t *testing.T, scenario, dir string, env ...string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestB4HelperProcess$", "-test.timeout=300s")
	cmd.Env = append(append(os.Environ(), "ZS3_B4_HELPER="+scenario, "ZS3_B4_DIR="+dir), env...)
	return cmd
}

// A crash (process exit with the page cache intact) at any publication or
// retirement boundary of a pass leaves a store that reopens, deep-verifies and
// reads every acknowledged object exactly; the next pass converges.
func TestB4CrashCutsRecover(t *testing.T) {
	cuts := []struct {
		point string
		occ   int
	}{
		{hookMaintMarked, 1}, {hookMaintReconciled, 1}, {hookPackValidated, 1}, {hookPackAfterRename, 1},
		{hookPackAfterDirSync, 1}, {hookMaintCarried, 1}, {hookMaintCondemned, 1}, {hookMaintFrozen, 1},
		{hookMaintSwapped, 1}, {hookMaintDrained, 1}, {hookMaintBeforeFileUnlink, 3},
		{hookMaintBeforePackUnlink, 1}, {hookMaintBeforePackUnlink, 2}, {hookMaintRetired, 1},
	}
	defer func(v int64) { b4FixturePackBytes = v }(b4FixturePackBytes)
	b4FixturePackBytes = 256 << 10
	for _, c := range cuts {
		t.Run(fmt.Sprintf("%s-%d", c.point, c.occ), func(t *testing.T) {
			dir, want := b4Fixture(t, 16, 90<<10)
			opt := defaultCompactOptions()
			opt.TargetBytes, opt.MinBytes = 200<<10, 1 // several packs, so retirement can stop between two
			s, err := OpenStore(dir)
			mustOK(t, err)
			b4Garbage(t, s, want)
			mustOK(t, s.Close())
			_, err = compactStore(dir, opt)
			mustOK(t, err)
			time.Sleep(10 * time.Millisecond)
			cmd := b4Helper(t, "cut", dir, "ZS3_B4_POINT="+c.point, "ZS3_B4_OCC="+strconv.Itoa(c.occ))
			out, err := cmd.Output()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 93 {
				t.Fatalf("cut not reached: %v\n%s", err, out)
			}
			acked := 0
			for _, line := range strings.Split(string(out), "\n") {
				if f := strings.Fields(line); len(f) == 3 && f[0] == "ack" {
					i, _ := strconv.Atoi(f[2])
					want[f[1]] = b4Body(20000+i, 3000+i%7*1000)
					acked++
				}
			}
			b4Verify(t, dir, want)
			s = b4OpenMaint(t, dir)
			rep, err := s.maintainOnline(context.Background(), b4Opts())
			mustOK(t, err)
			rep2, err := s.maintainOnline(context.Background(), b4Opts())
			mustOK(t, err)
			if rep2.PacksRetired != 0 || rep2.PendingPacks != 0 || rep2.ChunksDeleted != 0 {
				t.Fatalf("passes after the cut do not converge: %+v then %+v", rep, rep2)
			}
			mustOK(t, s.Close())
			b4Verify(t, dir, want)
			t.Logf("%d acknowledged writes; recovery pass reclaimed %d bytes", acked, rep.BytesReclaimed)
		})
	}
}

// A writer process cannot open a store a maintainer holds, and a maintainer
// cannot start beside another writer.
func TestB4WriterExclusion(t *testing.T) {
	dir, _ := b4Fixture(t, 2, 10<<10)
	s := b4OpenMaint(t, dir)
	out, err := b4Helper(t, "owner", dir).Output()
	mustOK(t, err)
	if !strings.Contains(string(out), "refused") {
		t.Fatalf("a second writer process was not refused: %s", out)
	}
	if _, err := OpenStore(dir); !errors.Is(err, errMaintOwned) {
		t.Fatalf("an in-process writer was not refused: %v", err)
	}
	mustOK(t, s.Close())
	w, err := OpenStore(dir)
	mustOK(t, err)
	if _, err := openStoreWith(dir, true, true, true); !errors.Is(err, errGCStoreInUse) {
		t.Fatalf("a maintainer started beside a writer: %v", err)
	}
	mustOK(t, w.Close())
}

// A shared opener in another process that was open before the pass keeps every
// file its view names: retired packs stay pending until it closes, then a later
// pass unlinks them.
func TestB4SharedReaderProcessDefersUnlink(t *testing.T) {
	dir, want := b4Fixture(t, 16, 90<<10)
	s := b4OpenMaint(t, dir)
	defer s.Close()
	b4Garbage(t, s, want)
	cmd := b4Helper(t, "shared", dir)
	stdin, err := cmd.StdinPipe()
	mustOK(t, err)
	stdout, err := cmd.StdoutPipe()
	mustOK(t, err)
	mustOK(t, cmd.Start())
	line, _ := bufio.NewReader(stdout).ReadString('\n')
	if strings.TrimSpace(line) != "opened" {
		t.Fatalf("helper: %q", line)
	}
	time.Sleep(10 * time.Millisecond)
	rep, err := s.maintainOnline(context.Background(), b4Opts())
	mustOK(t, err)
	if !strings.Contains(rep.Deferred, "reader lease") || rep.PendingPacks == 0 || rep.BytesReclaimed != 0 {
		t.Fatalf("pass did not defer for the shared reader: %+v", rep)
	}
	b4Check(t, s, want)
	stdin.Close()
	mustOK(t, cmd.Wait())
	rep, err = s.maintainOnline(context.Background(), b4Opts())
	mustOK(t, err)
	if rep.PendingPacks != 0 || rep.PacksRetired == 0 || rep.BytesReclaimed == 0 {
		t.Fatalf("pass after the reader closed: %+v", rep)
	}
	b4Check(t, s, want)
}

// An unavailable or corrupt replacement stops the pass before anything is
// removed; the original packs keep serving.
func TestB4BadReplacementRemovesNothing(t *testing.T) {
	for _, mode := range []string{"unavailable", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			dir, want, _ := b4DeadFixture(t)
			s := b4OpenMaint(t, dir)
			defer s.Close()
			before := s.packSnap()
			var once sync.Once
			testHook = func(p string) {
				switch {
				case mode == "unavailable" && p == hookPackBeforePublish:
					once.Do(func() {
						for _, d := range s.tmpDirs() {
							staged, _ := filepath.Glob(filepath.Join(d, "pack-*.tmp"))
							for _, f := range staged {
								os.Remove(f)
							}
						}
					})
				case mode == "corrupt" && p == hookPackAfterRename:
					once.Do(func() {
						newest, at := "", time.Time{}
						packs, _ := filepath.Glob(filepath.Join(tierPackDir(dir, tierHot), "*.pack"))
						for _, f := range packs {
							if fi, err := os.Stat(f); err == nil && fi.ModTime().After(at) {
								newest, at = f, fi.ModTime()
							}
						}
						f, err := os.OpenFile(newest, os.O_RDWR, 0)
						mustOK(t, err)
						f.WriteAt([]byte("garbage!"), 200)
						f.Close()
					})
				}
			}
			defer func() { testHook = nil }()
			rep, err := s.maintainOnline(context.Background(), b4Opts())
			testHook = nil
			if err == nil || rep.PacksRetired != 0 || rep.BytesReclaimed != 0 {
				t.Fatalf("pass with a bad replacement: err=%v %+v", err, rep)
			}
			for i, p := range before.packs {
				if _, err := os.Stat(p.path); err != nil {
					t.Fatalf("original pack %d removed: %v", i, err)
				}
			}
			b4Check(t, s, want)
		})
	}
}

// Cancellation at any phase leaves no condemned state behind and nothing lost.
func TestB4CancelledPass(t *testing.T) {
	for _, point := range []string{hookMaintMarked, hookMaintCarried, hookMaintSwapped} {
		t.Run(point, func(t *testing.T) {
			dir, want := b4Fixture(t, 16, 90<<10)
			s := b4OpenMaint(t, dir)
			defer s.Close()
			b4Garbage(t, s, want)
			ctx, cancel := context.WithCancel(context.Background())
			testHook = func(p string) {
				if p == point {
					cancel()
				}
			}
			rep, err := s.maintainOnline(ctx, b4Opts())
			testHook = nil
			if !errors.Is(err, context.Canceled) || !rep.Canceled {
				t.Fatalf("pass was not cancelled: %v %+v", err, rep)
			}
			if s.maint.Load() != nil {
				t.Fatal("the epoch outlived the pass")
			}
			_, err = s.PutObject("b", "after", []byte("x"), "", nil) // no pin waits on a dead epoch
			mustOK(t, err)
			want["after"] = []byte("x")
			b4Check(t, s, want)
			rep, err = s.maintainOnline(context.Background(), b4Opts())
			mustOK(t, err)
			if rep.PendingPacks != 0 {
				t.Fatalf("a later pass left pending packs: %+v", rep)
			}
			b4Check(t, s, want)
		})
	}
}

// The P9-comparable workload on real engine state: 192 signed PUTs (new keys,
// overwrites and re-puts of content that died earlier), 768 signed GETs whose
// bodies are hashed independently, and 32 online passes that retire packs and
// loose files while history is pruned between them. Segments seal often, so
// pack publication and segment ingestion overlap the passes.
func TestB4ConcurrentWorkloadDuringPasses(t *testing.T) {
	defer func(v int64) { segTargetBytes = v }(segTargetBytes)
	segTargetBytes = 256 << 10
	dir, base := b4Fixture(t, 24, 40<<10)
	s := b4OpenMaint(t, dir)
	defer s.Close()
	srv, signer := newTestServerAndSignerFor(t, s)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client := ts.Client()

	var mu sync.Mutex
	seen := map[string]map[[32]byte]bool{} // every body ever sent per key
	acked := map[string]bool{}             // keys a reader may expect
	for k, v := range base {
		seen[k] = map[[32]byte]bool{sha256.Sum256(v): true}
		acked[k] = true
	}
	put := func(key string, body []byte) error {
		mu.Lock()
		if seen[key] == nil {
			seen[key] = map[[32]byte]bool{}
		}
		seen[key][sha256.Sum256(body)] = true
		mu.Unlock()
		resp := doSignedRequest(t, client, ts.URL, signer, http.MethodPut, "/b/"+key, body, nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("PUT %s: %d %s", key, resp.StatusCode, b)
		}
		mu.Lock()
		acked[key] = true
		mu.Unlock()
		return nil
	}
	var writes, reads, failures atomic.Int64
	var wg sync.WaitGroup
	fail := func(err error) {
		if failures.Add(1) < 5 {
			t.Error(err)
		}
	}
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 48 {
				var key string
				var body []byte
				switch i % 4 {
				case 0: // new immutable key
					key, body = fmt.Sprintf("n%d-%d", w, i), b4Body(30000+w*100+i, 2000+i*997%60000)
				case 1: // overwrite a fixture key: its old version dies at the next prune
					key, body = fmt.Sprintf("k%03d", (w*7+i)%24), b4Body(40000+w*100+i, 30000+i*131%40000)
				case 2: // reuse content that an earlier overwrite killed
					key, body = fmt.Sprintf("r%d-%d", w, i), base[fmt.Sprintf("k%03d", (w*5+i)%24)]
				default: // a larger body: direct-pack ingest when big enough
					key, body = fmt.Sprintf("big%d-%d", w, i), b4Body(50000+w*100+i, 300<<10)
				}
				if err := put(key, body); err != nil {
					fail(err)
				}
				writes.Add(1)
			}
		}()
	}
	for r := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(r), 5))
			for range 96 {
				mu.Lock()
				keys := make([]string, 0, len(acked))
				for k := range acked {
					keys = append(keys, k)
				}
				mu.Unlock()
				k := keys[rng.IntN(len(keys))]
				resp := doSignedRequest(t, client, ts.URL, signer, http.MethodGet, "/b/"+k, nil, nil)
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				reads.Add(1)
				if err != nil || resp.StatusCode != http.StatusOK {
					fail(fmt.Errorf("GET %s: %d %v", k, resp.StatusCode, err))
					continue
				}
				mu.Lock()
				ok := seen[k][sha256.Sum256(body)]
				mu.Unlock()
				if !ok {
					fail(fmt.Errorf("GET %s returned bytes never written to it", k))
				}
			}
		}()
	}
	var reclaimed, retired, carried int64
	passErr := make(chan error, 1)
	go func() {
		defer close(passErr)
		opt := b4Opts()
		opt.TargetBytes = 512 << 10
		for i := range 32 {
			// Pace the passes behind the writers: under CPU contention they can otherwise all run before any
			// overwrite has made data dead (both this tree and B5 failed 9-12 of 30 runs with three busy loops).
			for deadline := time.Now().Add(10 * time.Second); writes.Load() < int64(i*6) && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			one := 1
			res, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", KeepLast: &one})
			if err == nil {
				err = s.ApplyHistoryPrune(&res)
			}
			if err != nil {
				passErr <- err
				return
			}
			rep, err := s.maintainOnline(context.Background(), opt)
			if err != nil {
				passErr <- err
				return
			}
			reclaimed += rep.BytesReclaimed
			retired += int64(rep.PacksRetired)
			carried += int64(rep.LateRecords + rep.RevivedRecords)
		}
	}()
	wg.Wait()
	mustOK(t, <-passErr)
	if failures.Load() > 0 {
		t.Fatalf("%d failed requests", failures.Load())
	}
	t.Logf("%d writes, %d reads, 32 passes: %d packs retired, %d bytes reclaimed, %d late/revived records carried", writes.Load(), reads.Load(), retired, reclaimed, carried)
	if retired == 0 || reclaimed == 0 {
		t.Fatal("the passes reclaimed nothing")
	}
	// Every key's current body is one that was written to it; the store
	// reopens and deep-verifies.
	final := map[string][]byte{}
	for k := range seen {
		_, body, err := s.GetObject("b", k)
		mustOK(t, err)
		if !seen[k][sha256.Sum256(body)] {
			t.Fatalf("%s: final body was never written", k)
		}
		final[k] = body
	}
	mustOK(t, s.Close())
	b4Verify(t, dir, final)
}

// The supported workflow: `zeros3 serve -maintenance all` runs passes in the
// background and on an authenticated request, reports machine-readable
// status, refuses a second writer process, and leaves a store that verifies.
func TestB4ServeMaintenanceWorkflow(t *testing.T) {
	if testing.Short() {
		t.Skip("real server process")
	}
	bin := buildZeros3Binary(t)
	dir, want := b4Fixture(t, 16, 90<<10)
	addr := freeTCPAddr(t)
	srv := exec.Command(bin, "serve", "-store", dir, "-addr", addr, "-maintenance", "all", "-maintenance-interval", "0", "-maintenance-keep-versions", "0")
	var logs bytes.Buffer
	srv.Stderr = &logs
	mustOK(t, srv.Start())
	defer srv.Process.Kill()
	waitForZeros3Serve(t, addr)
	base := "http://" + addr
	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	client := &http.Client{Timeout: 30 * time.Second}
	for i := 0; i < 8; i++ {
		k := fmt.Sprintf("k%03d", i)
		want[k] = b4Body(60000+i, 50<<10)
		resp := doSignedRequest(t, client, base, signer, http.MethodPut, "/b/"+k, want[k], nil)
		resp.Body.Close()
	}
	for i := 12; i < 16; i++ {
		resp := doSignedRequest(t, client, base, signer, http.MethodDelete, fmt.Sprintf("/b/k%03d", i), nil, nil)
		resp.Body.Close()
		delete(want, fmt.Sprintf("k%03d", i))
	}
	second := exec.Command(bin, "serve", "-store", dir, "-addr", freeTCPAddr(t))
	if out, err := second.CombinedOutput(); err == nil || !strings.Contains(string(out), "online maintenance") {
		t.Fatalf("a second server started beside the maintainer: %v\n%s", err, out)
	}
	time.Sleep(20 * time.Millisecond)
	resp := doSignedRequest(t, client, base, signer, http.MethodPost, zeros3MaintenancePath, nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("trigger: %d", resp.StatusCode)
	}
	var st MaintStatus
	for deadline := time.Now().Add(30 * time.Second); ; {
		resp := doSignedRequest(t, client, base, signer, http.MethodGet, zeros3MaintenancePath, nil, nil)
		mustOK(t, json.NewDecoder(resp.Body).Decode(&st))
		resp.Body.Close()
		if st.Passes > 0 && !st.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no pass completed: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st.Last == nil || st.Last.Error != "" || st.Last.BytesReclaimed == 0 || st.Last.PacksRetired == 0 {
		t.Fatalf("pass report: %+v", st.Last)
	}
	unsigned, err := client.Get(base + zeros3MaintenancePath)
	mustOK(t, err)
	unsigned.Body.Close()
	if unsigned.StatusCode == http.StatusOK {
		t.Fatal("the maintenance endpoint answered an unsigned request")
	}
	for k, v := range want {
		resp := doSignedRequest(t, client, base, signer, http.MethodGet, "/b/"+k, nil, nil)
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !bytes.Equal(got, v) {
			t.Fatalf("%s differs after the pass", k)
		}
	}
	srv.Process.Signal(syscall.SIGTERM)
	srv.Wait()
	if !strings.Contains(logs.String(), "maintenance pass: {") {
		t.Fatalf("no machine-readable pass log:\n%s", logs.String())
	}
	b4Verify(t, dir, want)
}

// Authoritative bodies under online maintenance: CDC chunks that bodies made
// redundant are reclaimed, and a delta whose only base lies in rewritten packs
// (its visible object deleted and pruned) stays exact through repeated passes.
func TestB4AuthorityClosuresSurvivePasses(t *testing.T) {
	l := newAuthLab(t)
	hist := authHistory(31, 4, 1_500_000)
	for i, v := range hist {
		l.put(fmt.Sprintf("k/v%d", i), v)
	}
	l.reclaim()
	if rep := l.migrate(AuthPolicy{MaxDepth: 2, MinSaving: 0.05}); rep.Admitted < 2 {
		t.Fatalf("admitted %d", rep.Admitted)
	}
	l.close()
	s := b4OpenMaint(t, l.dir)
	l.s = s
	mustOK(t, s.DeleteObject("b", "k/v0"))
	delete(l.want, "k/v0")
	zero := 0
	res, err := s.PlanHistoryPrune(historyPruneOptions{Bucket: "b", KeepLast: &zero})
	mustOK(t, err)
	mustOK(t, s.ApplyHistoryPrune(&res))
	time.Sleep(10 * time.Millisecond)
	var reclaimed int64
	for range 3 {
		rep, err := s.maintainOnline(context.Background(), b4Opts())
		mustOK(t, err)
		reclaimed += rep.BytesReclaimed
		l.exact("after pass")
	}
	if reclaimed == 0 {
		t.Fatal("no CDC chunk made redundant by a body was reclaimed")
	}
	l.verifyOK("online")
	l.close()
	l.open()
	l.exact("reopened")
	l.verifyOK("reopened")
}

// ---- Milestone C: matched latency under maintenance -----------------------

// TestB4LatencyUnderMaintenance measures a real server process under a closed
// loop of signed GET/HEAD/PUT/overwrite requests, from identical copies of one
// fixture, with maintenance off, with back-to-back online passes and with a
// pass started at most every 2 s. Run with
// ZEROS3_B4_BENCH=<output.json> (ZEROS3_B4_BENCH_SECONDS, _OBJECTS optional).
func TestB4LatencyUnderMaintenance(t *testing.T) {
	out := os.Getenv("ZEROS3_B4_BENCH")
	if out == "" {
		t.Skip("set ZEROS3_B4_BENCH to the output file")
	}
	secs, _ := strconv.Atoi(cmp.Or(os.Getenv("ZEROS3_B4_BENCH_SECONDS"), "30"))
	nobj, _ := strconv.Atoi(cmp.Or(os.Getenv("ZEROS3_B4_BENCH_OBJECTS"), "3000"))
	bin := buildZeros3Binary(t)
	work := t.TempDir()
	fixture := filepath.Join(work, "fixture")
	sizeOf := func(i int) int { return 4<<10 + int(uint64(i)*2654435761%uint64(380<<10)) }
	func() {
		old := smallObjectSegments
		smallObjectSegments = false
		defer func() { smallObjectSegments = old }()
		s, err := OpenStore(fixture)
		mustOK(t, err)
		mustOK(t, s.CreateBucket("b"))
		for i := range nobj {
			_, err := s.PutObject("b", fmt.Sprintf("o%05d", i), b4Body(i, sizeOf(i)), "application/octet-stream", nil)
			mustOK(t, err)
		}
		// Half of the objects get a second version: after the first prune the
		// fixture is about one third garbage, spread over every pack.
		for i := 0; i < nobj; i += 2 {
			_, err := s.PutObject("b", fmt.Sprintf("o%05d", i), b4Body(1_000_000+i, sizeOf(i)), "application/octet-stream", nil)
			mustOK(t, err)
		}
		mustOK(t, s.Close())
		opt := defaultCompactOptions()
		opt.TargetBytes = 16 << 20
		_, err = compactStore(fixture, opt)
		mustOK(t, err)
	}()
	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	type phase struct {
		Mode          string                `json:"mode"`
		Seconds       int                   `json:"seconds"`
		Ops           map[string]int        `json:"ops"`
		LatencyMS     map[string][4]float64 `json:"latency_ms_p50_p95_p99_max"`
		ServerCPUSec  float64               `json:"server_cpu_seconds"`
		ServerPeakRSS int64                 `json:"server_peak_rss_bytes"`
		StoreBefore   int64                 `json:"store_bytes_before"`
		StoreAfter    int64                 `json:"store_bytes_after"`
		StorePeak     int64                 `json:"store_bytes_peak"`
		ReadyMS       float64               `json:"restart_ready_ms"`
		Passes        []MaintReport         `json:"passes,omitempty"`
		Errors        int                   `json:"errors"`
	}
	run := func(mode string) phase {
		dir := filepath.Join(work, mode)
		os.RemoveAll(dir)
		mustOK(t, exec.Command("cp", "-a", fixture, dir).Run())
		p := phase{Mode: mode, Seconds: secs, Ops: map[string]int{}, LatencyMS: map[string][4]float64{}, StoreBefore: dirBytes(t, dir)}
		addr := freeTCPAddr(t)
		args := []string{"serve", "-store", dir, "-addr", addr}
		if mode != "idle" {
			args = append(args, "-maintenance", "all", "-maintenance-interval", "0", "-maintenance-keep-versions", "1", "-maintenance-max-source-packs", "4")
		}
		srv := exec.Command(bin, args...)
		srv.Stderr = io.Discard
		mustOK(t, srv.Start())
		defer srv.Process.Kill()
		waitForZeros3Serve(t, addr)
		base := "http://" + addr
		client := &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 64}}
		var mu sync.Mutex
		lat := map[string][]float64{}
		var errs atomic.Int64
		deadline := time.Now().Add(time.Duration(secs) * time.Second)
		var wg sync.WaitGroup
		var newKeys atomic.Int64
		for c := range 12 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rng := rand.New(rand.NewPCG(uint64(c), 77))
				for i := 0; time.Now().Before(deadline); i++ {
					var op, path string
					var body []byte
					method := http.MethodGet
					switch x := rng.IntN(100); {
					case x < 70:
						op, path = "get", fmt.Sprintf("/b/o%05d", rng.IntN(nobj))
					case x < 80:
						op, path, method = "head", fmt.Sprintf("/b/o%05d", rng.IntN(nobj)), http.MethodHead
					case x < 95:
						k := rng.IntN(nobj)
						op, path, method = "overwrite", fmt.Sprintf("/b/o%05d", k), http.MethodPut
						body = b4Body(2_000_000+c*1_000_000+i, sizeOf(k))
					default:
						n := newKeys.Add(1)
						op, path, method = "put", fmt.Sprintf("/b/new%06d", n), http.MethodPut
						body = b4Body(9_000_000+int(n), sizeOf(int(n)))
					}
					t0 := time.Now()
					resp := doSignedRequest(t, client, base, signer, method, path, body, nil)
					_, err := io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					d := float64(time.Since(t0).Microseconds()) / 1000
					if err != nil || resp.StatusCode != http.StatusOK {
						errs.Add(1)
					}
					mu.Lock()
					lat[op] = append(lat[op], d)
					mu.Unlock()
				}
			}()
		}
		stop := make(chan struct{})
		peakDisk := p.StoreBefore
		var driver sync.WaitGroup
		driver.Add(1)
		go func() { // keeps a pass running (paced: one start per 2 s at most), records reports, samples disk use
			defer driver.Done()
			seen := uint64(0)
			var lastKick time.Time
			for {
				select {
				case <-stop:
					return
				case <-time.After(200 * time.Millisecond):
				}
				peakDisk = max(peakDisk, dirBytes(t, dir))
				if mode == "idle" {
					continue
				}
				resp := doSignedRequest(t, client, base, signer, http.MethodGet, zeros3MaintenancePath, nil, nil)
				var st MaintStatus
				json.NewDecoder(resp.Body).Decode(&st)
				resp.Body.Close()
				if st.Passes > seen && st.Last != nil {
					seen = st.Passes
					p.Passes = append(p.Passes, *st.Last)
				}
				if !st.Running && (mode == "continuous" || time.Since(lastKick) >= 2*time.Second) {
					lastKick = time.Now()
					r := doSignedRequest(t, client, base, signer, http.MethodPost, zeros3MaintenancePath, nil, nil)
					r.Body.Close()
				}
			}
		}()
		wg.Wait()
		close(stop)
		driver.Wait()
		p.ServerCPUSec, p.ServerPeakRSS = b4ProcStats(srv.Process.Pid)
		srv.Process.Signal(syscall.SIGTERM)
		srv.Wait()
		p.Errors = int(errs.Load())
		for op, v := range lat {
			slices.Sort(v)
			q := func(f float64) float64 { return v[min(len(v)-1, int(f*float64(len(v))))] }
			p.Ops[op] = len(v)
			p.LatencyMS[op] = [4]float64{q(0.50), q(0.95), q(0.99), v[len(v)-1]}
		}
		p.StoreAfter, p.StorePeak = dirBytes(t, dir), peakDisk
		// Restart: time until a fresh server answers.
		t0 := time.Now()
		addr2 := freeTCPAddr(t)
		again := exec.Command(bin, "serve", "-store", dir, "-addr", addr2)
		mustOK(t, again.Start())
		waitForZeros3Serve(t, addr2)
		p.ReadyMS = float64(time.Since(t0).Microseconds()) / 1000
		again.Process.Signal(syscall.SIGTERM)
		again.Wait()
		s, err := OpenStore(dir)
		mustOK(t, err)
		res, err := s.Verify(true)
		mustOK(t, err)
		s.Close()
		if !res.OK() {
			t.Fatalf("%s: verify after the run: %v", mode, res.Issues)
		}
		os.RemoveAll(dir)
		return p
	}
	var phases []phase
	for _, m := range []string{"idle", "continuous", "paced", "idle-repeat", "continuous-repeat", "paced-repeat"} {
		phases = append(phases, run(strings.TrimSuffix(m, "-repeat")))
		phases[len(phases)-1].Mode = m
		t.Logf("%s: %v ops %v", m, phases[len(phases)-1].LatencyMS, phases[len(phases)-1].Ops)
	}
	data, err := json.MarshalIndent(map[string]any{"objects": nobj, "clients": 12, "cpus": runtime.NumCPU(), "fixture_bytes": dirBytes(t, fixture), "phases": phases}, "", " ")
	mustOK(t, err)
	mustOK(t, os.WriteFile(out, data, 0o644))
}

// b4ProcStats reads a process's CPU seconds and peak resident set.
func b4ProcStats(pid int) (cpu float64, peak int64) {
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		f := strings.Fields(string(b[bytes.LastIndexByte(b, ')')+2:]))
		ut, _ := strconv.ParseFloat(f[11], 64)
		st, _ := strconv.ParseFloat(f[12], 64)
		cpu = (ut + st) / 100
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(line, "VmHWM:"); ok {
				kb, _ := strconv.ParseInt(strings.Fields(v)[0], 10, 64)
				peak = kb << 10
			}
		}
	}
	return cpu, peak
}
