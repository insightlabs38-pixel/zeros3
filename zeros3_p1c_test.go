package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Z4-P1C: small-object segment tests. Specification and protocol:
// testing-harnesses/z4/p1c/{FORMAT,DURABILITY_MODEL}.md. Crash and
// power-loss-state tests are in zeros3_p1c_crash_test.go.

func segFiles(t *testing.T, root string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(root, segDirName, "*"+segFileSuffix))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func packFiles(t *testing.T, root string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(root, "packs", "*"+packFileSuffix))
	return m
}

func p1cOpen(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func p1cReopen(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func p1cBody(i, size int) []byte { return genRandomBytes(int64(1000+i), size) }

// p1cPutN stores n objects of the given size and returns key -> body.
func p1cPutN(t *testing.T, s *Store, prefix string, n, size int) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("%s%04d", prefix, i)
		body := p1cBody(i+len(prefix)*7919, size)
		if _, err := s.PutObject("b", k, body, "application/octet-stream", nil); err != nil {
			t.Fatal(err)
		}
		out[k] = body
	}
	return out
}

func p1cCheck(t *testing.T, s *Store, want map[string][]byte) {
	t.Helper()
	for k, body := range want {
		_, got, err := s.GetObject("b", k)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("object %s: err=%v equal=%v", k, err, bytes.Equal(got, body))
		}
	}
}

func p1cVerify(t *testing.T, s *Store) {
	t.Helper()
	v, err := s.Verify(true)
	if err != nil || !v.OK() {
		t.Fatalf("verify -deep: %v %+v", err, v)
	}
}

// setSegIO replaces the data-barrier seams for one test.
func setSegIO(t *testing.T, write func(f *os.File, b []byte, off int64) (int, error), sync func(f *os.File) error) {
	t.Helper()
	ow, os_ := segWriteAt, segSync
	if write != nil {
		segWriteAt = write
	}
	if sync != nil {
		segSync = sync
	}
	t.Cleanup(func() { segWriteAt, segSync = ow, os_ })
}

func setSegTarget(t *testing.T, target, minSeal int64) {
	t.Helper()
	ot, om := segTargetBytes, segMinSealBytes
	segTargetBytes, segMinSealBytes = target, minSeal
	t.Cleanup(func() { segTargetBytes, segMinSealBytes = ot, om })
}

// --- format and policy --------------------------------------------------------

func TestP1C_Format_FirstSmallWriteRaisesLevelPolicyOffDoesNot(t *testing.T) {
	useProductionLayout(t)
	if !supportedStoreFormat(storeFormatVersionSegments) || supportedStoreFormat(10) || supportedStoreFormat(12) || !supportedStoreFormat(9) {
		t.Fatal("supported levels must be 1..9 and 11 (10 is reserved)")
	}
	s, dir := p1cOpen(t)
	if s.format.StoreFormatVersion != storeFormatVersion {
		t.Fatalf("fresh store level %d", s.format.StoreFormatVersion)
	}
	if _, err := s.PutObject("b", "k", p1cBody(1, 20_000), "text/plain", nil); err != nil {
		t.Fatal(err)
	}
	if s.format.StoreFormatVersion != storeFormatVersionSegments {
		t.Fatalf("level after first small write = %d, want %d", s.format.StoreFormatVersion, storeFormatVersionSegments)
	}
	for _, tr := range []string{"tiers/warm/TIER.json", "tiers/cold/TIER.json"} { // a level above 5 implies the tier roots
		if _, err := os.Stat(filepath.Join(dir, tr)); err != nil {
			t.Fatalf("tier root missing: %v", err)
		}
	}
	s.Close()
	p1cReopen(t, dir) // a level-11 store reopens

	t.Run("policy-off", func(t *testing.T) {
		useLooseLayout(t, "the loose policy must not raise the level or create segments")
		s, dir := p1cOpen(t)
		if _, err := s.PutObject("b", "k", p1cBody(2, 20_000), "text/plain", nil); err != nil {
			t.Fatal(err)
		}
		if s.format.StoreFormatVersion != storeFormatVersion {
			t.Fatalf("loose policy raised the level to %d", s.format.StoreFormatVersion)
		}
		if _, err := os.Stat(filepath.Join(dir, segDirName)); err == nil {
			t.Fatal("loose policy created segments/")
		}
	})
}

func TestP1C_SmallPutUsesSegmentAndSurvivesRestart(t *testing.T) {
	useProductionLayout(t)
	s, dir := p1cOpen(t)
	body := p1cBody(1, 20_000)
	entry, err := s.PutObject("b", "k", body, "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(segFiles(t, dir)); n != 1 {
		t.Fatalf("segment files = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifests", entry.manifestUUID+".json")); err == nil {
		t.Fatalf("a small manifest must be an inline record, found a manifest file")
	}
	if ents, _ := os.ReadDir(filepath.Join(dir, "chunks")); len(ents) != 0 {
		t.Fatalf("small chunks must not be loose")
	}
	p1cCheck(t, s, map[string][]byte{"k": body})
	s.Close()
	s2 := p1cReopen(t, dir)
	p1cCheck(t, s2, map[string][]byte{"k": body})
	p1cVerify(t, s2)
	if len(segFiles(t, dir)) != 1 || len(packFiles(t, dir)) != 0 {
		t.Fatalf("a small restart must re-adopt the open segment, not seal it: segs=%v packs=%v", segFiles(t, dir), packFiles(t, dir))
	}
}

func TestP1C_LargeManifestStaysAFile(t *testing.T) {
	useProductionLayout(t)
	s, dir := p1cOpen(t)
	md := map[string]string{}
	for i := 0; i < 40; i++ {
		md[fmt.Sprintf("k%02d", i)] = strings.Repeat("v", 2000) // > 64 KiB of metadata
	}
	body := p1cBody(3, 10_000)
	entry, err := s.PutObject("b", "big-md", body, "", md)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifests", entry.manifestUUID+".json")); err != nil {
		t.Fatalf("a manifest above %d bytes must stay a file: %v", segInlineManifestMax, err)
	}
	p1cCheck(t, s, map[string][]byte{"big-md": body})
	s.Close()
	p1cVerify(t, p1cReopen(t, dir))
}

// --- group commit ---------------------------------------------------------------

// gateFirstSync blocks the first segment fsync until `want` requests are queued
// behind it, which makes the next batch's size deterministic.
func gateFirstSync(t *testing.T, s *Store, want int) *atomic.Int64 {
	t.Helper()
	var syncs atomic.Int64
	var once sync.Once
	setSegIO(t, nil, func(f *os.File) error {
		syncs.Add(1)
		once.Do(func() {
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				s.segs.mu.Lock()
				n := len(s.segs.queue)
				s.segs.mu.Unlock()
				if n >= want {
					return
				}
				time.Sleep(time.Millisecond)
			}
		})
		return f.Sync()
	})
	return &syncs
}

func TestP1C_GroupCommitSharesOneWriteAndFsync(t *testing.T) {
	useProductionLayout(t)
	s, _ := p1cOpen(t)
	// warm up: the first PUT creates the segment (its own fsyncs)
	if _, err := s.PutObject("b", "warm", p1cBody(0, 4096), "", nil); err != nil {
		t.Fatal(err)
	}
	const n = 24
	syncs := gateFirstSync(t, s, n-1)
	before := s.segs.nBatches.Load()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.PutObject("b", fmt.Sprintf("g%02d", i), p1cBody(100+i, 4096), "", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	withTimeout(t, 60*time.Second, "group", wg.Wait)
	if got := syncs.Load(); got > 3 {
		t.Fatalf("%d segment fsyncs for %d concurrent small PUTs: commits are not shared", got, n)
	}
	if b := s.segs.nBatches.Load() - before; b > 3 {
		t.Fatalf("%d batches for %d concurrent PUTs", b, n)
	}
}

func TestP1C_DuplicateContentWrittenOnce(t *testing.T) {
	useProductionLayout(t)
	s, _ := p1cOpen(t)
	if _, err := s.PutObject("b", "warm", p1cBody(0, 4096), "", nil); err != nil {
		t.Fatal(err)
	}
	const n = 12
	gateFirstSync(t, s, n-1)
	body := p1cBody(55, 30_000)
	recsBefore := s.segs.nRecords.Load()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.PutObject("b", fmt.Sprintf("d%02d", i), body, "", nil); err != nil {
				t.Error(err)
			}
		}()
	}
	withTimeout(t, 60*time.Second, "dups", wg.Wait)
	chunks := 0
	_, man, _ := s.HeadObject("b", "d00")
	chunks = len(man.Chunks)
	recs := s.segs.nRecords.Load() - recsBefore
	// each distinct chunk once + one manifest per object (manifests differ by UUID)
	if want := int64(chunks + n); recs != want {
		t.Fatalf("records = %d, want %d (%d distinct chunks written once + %d manifests)", recs, want, chunks, n)
	}
	for i := 0; i < n; i++ {
		_, got, err := s.GetObject("b", fmt.Sprintf("d%02d", i))
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("d%02d: %v", i, err)
		}
	}
	// a later duplicate PUT writes only its manifest
	before := s.segs.nRecords.Load()
	if _, err := s.PutObject("b", "again", body, "", nil); err != nil {
		t.Fatal(err)
	}
	if got := s.segs.nRecords.Load() - before; got != 1 {
		t.Fatalf("duplicate PUT wrote %d records, want 1 (its manifest)", got)
	}
}

// --- failure isolation ------------------------------------------------------------

func TestP1C_BatchFailureLeavesAcknowledgedDataAndNoPartialState(t *testing.T) {
	useProductionLayout(t)
	for _, kind := range []string{"fsync-error", "short-write"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := p1cOpen(t)
			acked := p1cPutN(t, s, "ok", 5, 9000)
			var fail atomic.Bool
			setSegIO(t,
				func(f *os.File, b []byte, off int64) (int, error) {
					if kind == "short-write" && fail.CompareAndSwap(true, false) {
						n, _ := f.WriteAt(b[:len(b)/2], off)
						return n, errors.New("injected short write")
					}
					return f.WriteAt(b, off)
				},
				func(f *os.File) error {
					if kind == "fsync-error" && fail.CompareAndSwap(true, false) {
						return errors.New("injected fsync failure")
					}
					return f.Sync()
				})
			fail.Store(true)
			if _, err := s.PutObject("b", "doomed", p1cBody(77, 9000), "", nil); err == nil {
				t.Fatal("the failing batch was acknowledged")
			}
			if _, _, err := s.GetObject("b", "doomed"); !errors.Is(err, errNoSuchKey) {
				t.Fatalf("a failed PUT is visible: %v", err)
			}
			p1cCheck(t, s, acked) // earlier acknowledged objects are untouched
			// the writer recovers on a fresh segment
			later := p1cPutN(t, s, "later", 5, 9000)
			p1cCheck(t, s, later)
			p1cVerify(t, s)
			s.Close()
			s2 := p1cReopen(t, dir)
			p1cCheck(t, s2, acked)
			p1cCheck(t, s2, later)
			if _, _, err := s2.GetObject("b", "doomed"); !errors.Is(err, errNoSuchKey) {
				t.Fatalf("a failed PUT appeared after restart: %v", err)
			}
			p1cVerify(t, s2)
		})
	}
}

func TestP1C_FailedBatchFailsEveryMemberAndOnlyThem(t *testing.T) {
	useProductionLayout(t)
	s, _ := p1cOpen(t)
	p1cPutN(t, s, "warm", 1, 4096)
	const n = 8
	var failed atomic.Bool
	var once sync.Once
	setSegIO(t, nil, func(f *os.File) error {
		var err error
		once.Do(func() {
			// hold the first sync until the other n-1 requests queue, then fail it
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				s.segs.mu.Lock()
				q := len(s.segs.queue)
				s.segs.mu.Unlock()
				if q >= n-1 {
					break
				}
				time.Sleep(time.Millisecond)
			}
			failed.Store(true)
			err = errors.New("injected fsync failure")
		})
		if err != nil {
			return err
		}
		return f.Sync()
	})
	// the first request is a batch of one that fails; the queued n-1 form the next batch and succeed
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.PutObject("b", fmt.Sprintf("m%d", i), p1cBody(300+i, 5000), "", nil)
		}()
	}
	withTimeout(t, 60*time.Second, "members", wg.Wait)
	nf := 0
	for i, err := range errs {
		_, _, gerr := s.GetObject("b", fmt.Sprintf("m%d", i))
		if err != nil {
			nf++
			if !errors.Is(gerr, errNoSuchKey) {
				t.Fatalf("failed PUT %d is visible", i)
			}
		} else if gerr != nil {
			t.Fatalf("acknowledged PUT %d unreadable: %v", i, gerr)
		}
	}
	if nf == 0 || nf == n {
		t.Fatalf("%d of %d requests failed: a failed batch must fail its members and spare the next batch", nf, n)
	}
	p1cVerify(t, s)
}

// --- recovery of one segment --------------------------------------------------------

func TestP1C_RecoveryCutsTornTailsAndKeepsAcknowledgedRecords(t *testing.T) {
	useProductionLayout(t)
	cases := []struct {
		name string
		tail func(valid int64) []byte // bytes appended after the last acknowledged record
	}{
		{"partial-record-header", func(int64) []byte { return bytes.Repeat([]byte{0xAB}, 20) }},
		{"header-without-payload", func(int64) []byte {
			var h [packRecordHeaderSize]byte
			putPackRecordHeader(h[:], packEntry{sha: [32]byte{1}, stored: 500, logical: 500})
			return h[:]
		}},
		{"header-and-partial-payload", func(int64) []byte {
			var h [packRecordHeaderSize]byte
			p := genRandomBytes(5, 400)
			putPackRecordHeader(h[:], packEntry{sha: [32]byte{2}, stored: 4000, logical: 4000})
			return append(h[:], p...)
		}},
		{"complete-record-wrong-digest", func(int64) []byte {
			var h [packRecordHeaderSize]byte
			p := genRandomBytes(6, 700)
			putPackRecordHeader(h[:], packEntry{sha: [32]byte{3}, stored: 700, logical: 700})
			return append(h[:], p...)
		}},
		{"zero-filled-hole", func(int64) []byte { return make([]byte, 8192) }},
		{"absurd-length", func(int64) []byte {
			var h [packRecordHeaderSize]byte
			putPackRecordHeader(h[:], packEntry{sha: [32]byte{4}, stored: 0xFFFFFFF0, logical: 0xFFFFFFF0})
			return h[:]
		}},
		{"garbage", func(int64) []byte { return genRandomBytes(9, 3000) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, dir := p1cOpen(t)
			acked := p1cPutN(t, s, "a", 6, 7000)
			path := segFiles(t, dir)[0]
			info, _ := os.Stat(path)
			valid := info.Size()
			// a crashed process: the store is abandoned (not closed) and bytes follow the last fsynced record
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
			if err != nil {
				t.Fatal(err)
			}
			tail := c.tail(valid)
			f.Write(tail)
			f.Close()
			s2 := p1cReopen(t, dir)
			p1cCheck(t, s2, acked)
			if fi, _ := os.Stat(path); fi == nil || fi.Size() != valid {
				t.Fatalf("segment size after recovery = %v, want the valid prefix %d", fi, valid)
			}
			if q, _ := filepath.Glob(filepath.Join(dir, "tmp", "seg-torn-*")); len(q) != 1 {
				t.Fatalf("the cut tail must be quarantined in tmp/: %v", q)
			}
			// the recovered segment is writable again and nothing is lost on a further restart
			more := p1cPutN(t, s2, "b", 4, 7000)
			p1cVerify(t, s2)
			s2.Close()
			s3 := p1cReopen(t, dir)
			p1cCheck(t, s3, acked)
			p1cCheck(t, s3, more)
			p1cVerify(t, s3)
		})
	}
}

func TestP1C_RecoveryDropsUnusedAndBadSegments(t *testing.T) {
	useProductionLayout(t)
	s, dir := p1cOpen(t)
	acked := p1cPutN(t, s, "a", 3, 5000)
	s.Close()
	segDir := filepath.Join(dir, segDirName)
	hdr := make([]byte, packHeaderSize)
	putPackHeader(hdr)
	bad := append([]byte(nil), hdr...)
	bad[0] = 'X'
	files := map[string][]byte{
		"00000000000000f1.seg": nil,         // created, never written
		"00000000000000f2.seg": hdr[:5],     // torn header
		"00000000000000f3.seg": hdr,         // header only
		"00000000000000f4.seg": bad,         // wrong magic
		"not-a-segment.txt":    []byte("x"), // ignored
		"00000000000000f5.tmp": []byte("x"), // ignored
	}
	for n, b := range files {
		if err := os.WriteFile(filepath.Join(segDir, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s2 := p1cReopen(t, dir)
	p1cCheck(t, s2, acked)
	left, _ := os.ReadDir(segDir)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, n := range names {
		if strings.HasPrefix(n, "00000000000000f") && strings.HasSuffix(n, ".seg") {
			t.Fatalf("unusable segment %s survived recovery: %v", n, names)
		}
	}
	if len(segFiles(t, dir)) != 1 {
		t.Fatalf("segments after recovery = %v", names)
	}
	p1cVerify(t, s2)
}

// --- sealing, rollover, concurrent readers --------------------------------------------

func TestP1C_RolloverSealsIntoVerifiablePacks(t *testing.T) {
	useProductionLayout(t)
	setSegTarget(t, 96<<10, 32<<10)
	s, dir := p1cOpen(t)
	acked := p1cPutN(t, s, "r", 80, 9000)
	s.sealSegments()
	packs := packFiles(t, dir)
	if len(packs) < 4 {
		t.Fatalf("sealed packs = %d, want several rollovers", len(packs))
	}
	for _, p := range packs {
		if _, _, err := verifyPackFile(p); err != nil {
			t.Fatalf("sealed segment %s is not a valid pack-v1: %v", filepath.Base(p), err)
		}
	}
	if n := len(segFiles(t, dir)); n != 0 {
		t.Fatalf("segments after a full seal = %d", n)
	}
	p1cCheck(t, s, acked)
	p1cVerify(t, s)
	more := p1cPutN(t, s, "after", 5, 9000) // a fresh segment is created on demand
	p1cCheck(t, s, more)
	s.Close()
	s2 := p1cReopen(t, dir)
	p1cCheck(t, s2, acked)
	p1cCheck(t, s2, more)
	p1cVerify(t, s2)
}

func TestP1C_ReadersNeverSeeMissingOffsetsAcrossSeal(t *testing.T) {
	useProductionLayout(t)
	setSegTarget(t, 64<<10, 16<<10)
	s, _ := p1cOpen(t)
	acked := p1cPutN(t, s, "seed", 20, 8000)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad atomic.Int64
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for k, body := range acked {
					_, got, err := s.GetObject("b", k)
					if err != nil || !bytes.Equal(got, body) {
						bad.Add(1)
					}
				}
			}
		}()
	}
	for i := 0; i < 6; i++ { // writers force rollovers (background seals) while readers run
		p1cPutN(t, s, fmt.Sprintf("w%d_", i), 25, 8000)
		s.sealSegments()
	}
	close(stop)
	wg.Wait()
	if n := bad.Load(); n != 0 {
		t.Fatalf("%d reads failed or returned wrong bytes while segments were sealed", n)
	}
}

func TestP1C_HardCapRollsBeforeWriting(t *testing.T) {
	useProductionLayout(t)
	setSegTarget(t, 40<<10, 10<<10)
	s, dir := p1cOpen(t)
	big := map[string][]byte{}
	for i := 0; i < 6; i++ { // each PUT alone is ~ 60 KB, above the target
		k := fmt.Sprintf("big%d", i)
		big[k] = p1cBody(i+500, 60_000)
		if _, err := s.PutObject("b", k, big[k], "", nil); err != nil {
			t.Fatal(err)
		}
	}
	s.sealSegments()
	for _, p := range packFiles(t, dir) {
		fi, _ := os.Stat(p)
		if fi.Size() > segHardMax()+int64(packFooterSize)+200*packIndexEntrySize {
			t.Fatalf("segment %d bytes exceeds the hard cap %d", fi.Size(), segHardMax())
		}
	}
	p1cCheck(t, s, big)
	p1cVerify(t, s)
}

// --- maintenance, history, mixed layouts -------------------------------------------------

func TestP1C_GCAndRepackReclaimDeadRecordsAndKeepLiveOnes(t *testing.T) {
	useProductionLayout(t)
	setSegTarget(t, 64<<10, 16<<10)
	s, dir := p1cOpen(t)
	live := p1cPutN(t, s, "live", 30, 8000)
	// dead records: failed conditional PUTs leave chunks and a manifest no root names
	dead := 0
	for i := 0; i < 30; i++ {
		_, err := s.PutObjectChecked("b", "live0000", p1cBody(9000+i, 8000), "", nil, putCondition{ifNoneMatchStar: true})
		if err == nil {
			t.Fatal("conditional PUT unexpectedly succeeded")
		}
		dead++
	}
	// history: an overwrite pins the old version's inline manifest and chunks
	old := p1cBody(1, 8000)
	if _, err := s.PutObject("b", "versioned", old, "", nil); err != nil {
		t.Fatal(err)
	}
	cur := p1cBody(2, 8000)
	if _, err := s.PutObject("b", "versioned", cur, "", nil); err != nil {
		t.Fatal(err)
	}
	live["versioned"] = cur
	s.Close()

	before := len(packFiles(t, dir))
	res, err := gcCollect(dir, true)
	if err != nil || !res.LiveSetOK {
		t.Fatalf("gc: %v %+v", err, res)
	}
	if res.PackSummary.PackedDeadChunkCount == 0 && before > 0 {
		t.Logf("gc dead records reported: %+v", res.PackSummary)
	}
	rr, err := repackStore(dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: false})
	if err != nil {
		t.Fatalf("repack: %v", err)
	}
	if !rr.LiveSetOK {
		t.Fatalf("repack live set not OK: %+v", rr)
	}
	s2 := p1cReopen(t, dir)
	p1cCheck(t, s2, live)
	vs, _, err := s2.ListVersions("b", "versioned")
	if err != nil || len(vs) != 1 {
		t.Fatalf("history of 'versioned': %d versions, %v", len(vs), err)
	}
	p1cVerify(t, s2)
	// the pinned old version is still restorable after sealing, gc and repack
	if _, _, err := s2.RestoreObjectVersion("b", "versioned", vs[0].versionID); err != nil {
		t.Fatalf("restore of a pinned historical version: %v", err)
	}
	_, got, _ := s2.GetObject("b", "versioned")
	if !bytes.Equal(got, old) {
		t.Fatal("restored version has wrong bytes")
	}
}

func TestP1C_OldStoreAndPolicyChangesCoexist(t *testing.T) {
	useProductionLayout(t)
	dir := t.TempDir()
	var looseBodies map[string][]byte
	t.Run("old-layout-store", func(t *testing.T) {
		useLooseLayout(t, "builds a pre-segment store (loose chunks, manifest files)")
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		s.CreateBucket("b")
		looseBodies = p1cPutN(t, s, "old", 8, 20_000)
		if s.format.StoreFormatVersion != storeFormatVersion {
			t.Fatalf("old store level %d", s.format.StoreFormatVersion)
		}
		s.Close()
	})
	s := p1cReopen(t, dir) // new binary, default policy
	p1cCheck(t, s, looseBodies)
	if s.format.StoreFormatVersion != storeFormatVersion {
		t.Fatal("opening an old store must not raise its level")
	}
	segBodies := p1cPutN(t, s, "new", 8, 20_000) // first small write raises the level
	if s.format.StoreFormatVersion != storeFormatVersionSegments {
		t.Fatalf("level after first segment write = %d", s.format.StoreFormatVersion)
	}
	p1cCheck(t, s, looseBodies)
	p1cCheck(t, s, segBodies)
	p1cVerify(t, s)
	s.Close()
	// policy turned off again: segments and inline manifests stay readable, new writes are loose
	smallObjectSegments = false
	t.Cleanup(func() { smallObjectSegments = true })
	s2 := p1cReopen(t, dir)
	p1cCheck(t, s2, looseBodies)
	p1cCheck(t, s2, segBodies)
	again := p1cPutN(t, s2, "again", 4, 20_000)
	if ents, _ := os.ReadDir(filepath.Join(dir, "chunks")); len(ents) == 0 {
		t.Fatal("with the policy off new writes must be loose")
	}
	p1cCheck(t, s2, again)
	s2.Close()
	res, err := gcCollect(dir, true)
	if err != nil || !res.LiveSetOK {
		t.Fatalf("gc over a mixed store: %v %+v", err, res)
	}
	s3 := p1cReopen(t, dir)
	p1cCheck(t, s3, looseBodies)
	p1cCheck(t, s3, segBodies)
	p1cCheck(t, s3, again)
	p1cVerify(t, s3)
}

func TestP1C_MultipartCopyAndBundleRoundTripThroughSegments(t *testing.T) {
	useProductionLayout(t)
	s, dir := p1cOpen(t)
	uid, err := s.CreateMultipartUpload("b", "mp", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p1 := p1cBody(1, 60_000) // a single (final) part may be small
	e1, err := s.UploadPart("b", "mp", uid, 1, p1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CompleteMultipartUpload("b", "mp", uid, []completedPart{{PartNumber: 1, ETag: e1}}); err != nil {
		t.Fatal(err)
	}
	want := p1
	p1cCheck(t, s, map[string][]byte{"mp": want})
	src := p1cPutN(t, s, "src", 1, 30_000)
	if _, _, err := s.CopyObject(CopyObjectRequest{SrcBucket: "b", SrcKey: "src0000", DstBucket: "b", DstKey: "copy"}); err != nil {
		t.Fatalf("copy: %v", err)
	}
	p1cCheck(t, s, map[string][]byte{"copy": src["src0000"]})
	p1cVerify(t, s)
	s.Close()
	s2 := p1cReopen(t, dir)
	p1cCheck(t, s2, map[string][]byte{"mp": want, "copy": src["src0000"]})
	p1cVerify(t, s2)
}
