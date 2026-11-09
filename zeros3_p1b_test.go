package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func init() {
	// The production default packs medium objects from 256 KiB. Most of the
	// suite inspects, corrupts or deletes loose chunk files of objects of that
	// size, so test binaries keep the previous threshold unless
	// ZEROS3_TEST_MEDIUM_PACK=1; the medium-pack tests below set the policy
	// explicitly, and the layout-agnostic tests also pass under the default.
	if os.Getenv("ZEROS3_TEST_MEDIUM_PACK") != "1" && os.Getenv("ZEROS3_DIRECT_PACK_KIB") == "" && os.Getenv("ZEROS3_DIRECT_PACK_MIB") == "" {
		directPackMinBytes = defaultPackTargetBytes
	}
}

// Z4-P1B: durable-publication tests. Hooks fire on request, flush and commit
// goroutines, so every counter and shared variable here is concurrency-safe.

// withTimeout fails the test instead of hanging when fn deadlocks.
func withTimeout(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s: no progress after %v (deadlock?)", what, d)
	}
}

func TestP1B_DirSync_CoalescesAndOrders(t *testing.T) {
	var d dirSyncMap
	var mutated, executed atomic.Int64
	var snapshots atomic.Int64 // highest mutation count visible to a started fsync
	do := func(string) error {
		executed.Add(1)
		snap := mutated.Load()
		time.Sleep(2 * time.Millisecond)
		for {
			old := snapshots.Load()
			if snap <= old || snapshots.CompareAndSwap(old, snap) {
				break
			}
		}
		return nil
	}
	const callers = 64
	var wg sync.WaitGroup
	var bad atomic.Int64
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for k := 0; k < 10; k++ {
				mark := mutated.Add(1) // the directory mutation, then the sync
				if err := d.sync("dir", do); err != nil {
					bad.Add(1)
				}
				// The fsync that served this caller began after its mutation.
				if snapshots.Load() < mark {
					bad.Add(1)
				}
			}
		}()
	}
	withTimeout(t, 20*time.Second, "dirSync", wg.Wait)
	if bad.Load() != 0 {
		t.Fatalf("%d callers returned before an fsync covering their mutation", bad.Load())
	}
	if e := executed.Load(); e >= callers*10 || e < 1 {
		t.Fatalf("executed %d fsyncs for %d requests: no coalescing", e, callers*10)
	}
	d.mu.Lock()
	n := len(d.m)
	d.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d idle entries left in the map", n)
	}
}

func TestP1B_DirSync_ErrorReachesEveryRoundMember(t *testing.T) {
	var d dirSyncMap
	gate := make(chan struct{})
	var calls atomic.Int64
	do := func(string) error {
		n := calls.Add(1)
		if n == 1 {
			<-gate // hold the first round so the rest queue as one round
			return nil
		}
		return errors.New("fsync failed")
	}
	first := make(chan error, 1)
	go func() { first <- d.sync("x", do) }()
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	const joiners = 8
	errs := make(chan error, joiners)
	for i := 0; i < joiners; i++ {
		go func() { errs <- d.sync("x", do) }()
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	withTimeout(t, 10*time.Second, "errors", func() {
		if err := <-first; err != nil {
			t.Errorf("first round: %v", err)
		}
		for i := 0; i < joiners; i++ {
			if err := <-errs; err == nil {
				t.Error("a member of the failed round did not see the fsync error")
			}
		}
	})
	if err := d.sync("x", func(string) error { return nil }); err != nil {
		t.Fatalf("round after a failed one: %v", err)
	}
}

func TestP1B_DirSync_PanicDoesNotStrandJoiners(t *testing.T) {
	var d dirSyncMap
	gate := make(chan struct{})
	var started atomic.Bool
	do := func(string) error {
		started.Store(true)
		<-gate
		panic("simulated crash")
	}
	leader := make(chan any, 1)
	go func() {
		defer func() { leader <- recover() }()
		d.sync("p", do)
	}()
	for !started.Load() {
		time.Sleep(time.Millisecond)
	}
	// Joiners of the next round wait for the panicking one and must still run.
	next := make(chan error, 1)
	go func() { next <- d.sync("p", func(string) error { return nil }) }()
	time.Sleep(30 * time.Millisecond)
	close(gate)
	withTimeout(t, 10*time.Second, "panic", func() {
		if p := <-leader; p == nil {
			t.Error("panic swallowed")
		}
		if err := <-next; err != nil {
			t.Errorf("the next round failed: %v", err)
		}
	})
}

func TestP1B_PubRegistry_ClaimWaitRelease(t *testing.T) {
	var r pubRegistry
	k := [32]byte{1, 2, 3}
	c, ok := r.tryClaim(k)
	if !ok {
		t.Fatal("first claim not granted")
	}
	if c2, ok2 := r.tryClaim(k); ok2 || c2 != c {
		t.Fatal("second claim must return the owner's claim")
	}
	if r.inFlight(k) != c || r.inFlight([32]byte{9}) != nil {
		t.Fatal("inFlight wrong")
	}
	waiter := make(chan struct{})
	go func() { <-c.done; close(waiter) }()
	r.release(k, c)
	withTimeout(t, 5*time.Second, "release", func() { <-waiter })
	if r.inFlight(k) != nil {
		t.Fatal("claim survived release")
	}
	if _, ok := r.tryClaim(k); !ok {
		t.Fatal("digest not claimable after release")
	}
}

// Two batches publishing the same digests in opposite order must not deadlock
// (a publisher never waits for another while it holds claims), and the
// duplicates are stored once.
func TestP1B_ConcurrentOverlappingBatches(t *testing.T) {
	store, dir := newBucketStore(t)
	const n = 12
	pieces := make([][]byte, n)
	for i := range pieces {
		pieces[i] = genRandomBytes(int64(7000+i), 20000+i)
	}
	run := func(order []int) error {
		b := store.newCASBatch()
		defer b.Abort()
		for _, i := range order {
			if _, err := b.Add(pieces[i]); err != nil {
				return err
			}
		}
		return b.Flush()
	}
	fwd, rev := make([]int, n), make([]int, n)
	for i := range fwd {
		fwd[i], rev[i] = i, n-1-i
	}
	for round := 0; round < 20; round++ {
		if round%2 == 1 { // remove everything so this round races real publication
			for i := range pieces {
				sum := sha256.Sum256(pieces[i])
				os.Remove(store.chunkPath(sum))
			}
		}
		errs := make(chan error, 4)
		for _, o := range [][]int{fwd, rev, fwd, rev} {
			go func() { errs <- run(o) }()
		}
		withTimeout(t, 30*time.Second, "overlapping batches", func() {
			for i := 0; i < 4; i++ {
				if err := <-errs; err != nil {
					t.Error(err)
				}
			}
		})
	}
	for i := range pieces {
		sum := sha256.Sum256(pieces[i])
		if _, err := store.casStat(sum); err != nil {
			t.Fatalf("chunk %d missing: %v", i, err)
		}
	}
	if left := casStagedFiles(t, dir); len(left) != 0 {
		t.Fatalf("staging files left behind: %v", left)
	}
	casLooseChunks(t, dir) // every file hashes to its name
}

// A reader that sees a chunk file while another goroutine's publication is
// still pending must wait, even when it arrives after the rename.
func TestP1B_ObserverWaitsForDuplicateWriter(t *testing.T) {
	store, _ := newBucketStore(t)
	data := genRandomBytes(7100, 33000)
	sum := sha256.Sum256(data)
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	withTestHook(t, func(p string) {
		if p == hookCASBeforeDirSync {
			once.Do(func() { close(reached); <-release })
		}
	})
	w1 := make(chan error, 1)
	go func() { _, err := store.casWrite(data); w1 <- err }()
	<-reached
	// A second writer of the same content must not return before the first
	// one's directory fsync (it would otherwise acknowledge a non-durable chunk).
	w2 := make(chan error, 1)
	go func() { _, err := store.casWrite(data); w2 <- err }()
	time.Sleep(100 * time.Millisecond)
	select {
	case <-w2:
		t.Fatal("duplicate writer returned while the original publication was not durable")
	default:
	}
	close(release)
	withTimeout(t, 10*time.Second, "writers", func() {
		if err := <-w1; err != nil {
			t.Error(err)
		}
		if err := <-w2; err != nil {
			t.Error(err)
		}
	})
	if _, err := store.casStat(sum); err != nil {
		t.Fatal(err)
	}
}

// If the first publisher fails, a waiting duplicate publishes the chunk
// itself instead of trusting a file that was removed.
func TestP1B_DuplicateWriterTakesOverAfterFailure(t *testing.T) {
	store, dir := newBucketStore(t)
	data := genRandomBytes(7200, 31000)
	sum := sha256.Sum256(data)
	reached, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	withTestHook(t, func(p string) {
		if p == hookCASBeforeDirSync && first.CompareAndSwap(false, true) {
			close(reached)
			<-release
			// make the first publication fail: its directory vanishes
			os.RemoveAll(filepath.Dir(store.chunkPath(sum)))
		}
	})
	w1 := make(chan error, 1)
	go func() { _, err := store.casWrite(data); w1 <- err }()
	<-reached
	w2 := make(chan error, 1)
	go func() { _, err := store.casWrite(data); w2 <- err }()
	time.Sleep(50 * time.Millisecond)
	close(release)
	withTimeout(t, 20*time.Second, "takeover", func() {
		<-w1 // may fail or not, depending on when the directory vanished
		if err := <-w2; err != nil {
			t.Errorf("duplicate writer: %v", err)
		}
	})
	got, err := store.casRead(sum)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("chunk not readable after takeover: %v", err)
	}
	if left := casStagedFiles(t, dir); len(left) != 0 {
		t.Fatalf("staging left: %v", left)
	}
}

// A directory removed behind the readiness cache (administrative removal) is
// recreated by the next publication.
func TestP1B_StaleShardCacheRecovers(t *testing.T) {
	store, _ := newBucketStore(t)
	a := genRandomBytes(7300, 30000)
	if _, err := store.casWrite(a); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(a)
	if err := os.RemoveAll(filepath.Dir(filepath.Dir(store.chunkPath(sum)))); err != nil {
		t.Fatal(err)
	}
	if _, err := store.casWrite(a); err != nil {
		t.Fatalf("publish after the shard directory vanished: %v", err)
	}
	if got, err := store.casRead(sum); err != nil || !bytes.Equal(got, a) {
		t.Fatalf("read back: %v", err)
	}
}

func TestP1B_ShardDirsCreatedOnceUnderConcurrency(t *testing.T) {
	store, _ := newBucketStore(t)
	var mk, sy sync.Map
	old := testDirEvent
	testDirEvent = func(op, rel string) {
		m := &mk
		if op == "sync" {
			m = &sy
		}
		c, _ := m.LoadOrStore(rel, new(atomic.Int64))
		c.(*atomic.Int64).Add(1)
	}
	t.Cleanup(func() { testDirEvent = old })
	payloads := shardPayloads(t, "e5", "f6", 6, false)
	var wg sync.WaitGroup
	for _, p := range payloads {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.casWrite(p); err != nil {
				t.Error(err)
			}
		}()
	}
	withTimeout(t, 20*time.Second, "shard dirs", wg.Wait)
	for _, rel := range []string{"chunks/e5", "chunks/e5/f6"} {
		c, ok := mk.Load(rel)
		if !ok || c.(*atomic.Int64).Load() != 1 {
			t.Fatalf("%s created %v times", rel, c)
		}
	}
}

// --- journal group commit ---

func countJournalSyncs(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	withTestHook(t, func(p string) {
		if p == hookAfterJournalSync {
			n.Add(1)
		}
	})
	return &n
}

func TestP1B_GroupCommit_ConcurrentPutsReplayIdentically(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	syncs := countJournalSyncs(t)
	const writers, perWriter = 16, 8
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				// half the keys are shared so overwrites race inside batches
				key := fmt.Sprintf("k%d", (w*perWriter+i)%24)
				body := genRandomBytes(int64(w*1000+i), 3000+i)
				if _, err := store.PutObject("b", key, body, "", nil); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	withTimeout(t, 60*time.Second, "group commit", wg.Wait)
	if got := syncs.Load(); got > writers*perWriter {
		t.Fatalf("%d journal syncs for %d commits", got, writers*perWriter)
	}
	t.Logf("%d commits, %d journal fsyncs", writers*perWriter, syncs.Load())
	// Live state == replayed state, including history.
	before := namespaceSignature(store)
	store.Close()
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if after := namespaceSignature(reopened); after != before {
		t.Fatalf("replayed namespace differs from the live one:\nlive:   %s\nreplay: %s", before, after)
	}
	for k := 0; k < 24; k++ {
		if _, _, err := reopened.GetObject("b", fmt.Sprintf("k%d", k)); err != nil {
			t.Fatalf("k%d unreadable after replay: %v", k, err)
		}
	}
}

func namespaceSignature(s *Store) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var sb bytes.Buffer
	for _, b := range sortedKeys(s.buckets) {
		for _, k := range sortedKeys(s.buckets[b].objects) {
			o := s.buckets[b].objects[k]
			fmt.Fprintf(&sb, "%s/%s=%s:%d:%d;", b, k, o.manifestUUID, o.size, o.seq)
			for _, h := range s.history[b][k] {
				fmt.Fprintf(&sb, "h(%s,%s,%d);", h.versionID, h.manifestUUID, h.seq)
			}
		}
	}
	return sb.String()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Exactly one of N concurrent If-None-Match:* creates of one key wins, even
// when they share a batch.
func TestP1B_GroupCommit_ConditionalCreateRace(t *testing.T) {
	store, _ := newBucketStore(t)
	const n = 24
	var wins, losses atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.PutObjectChecked("b", "only", genRandomBytes(int64(i), 4000), "", nil, putCondition{ifNoneMatchStar: true})
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, errPreconditionFailed):
				losses.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	close(start)
	withTimeout(t, 60*time.Second, "conditional race", wg.Wait)
	if wins.Load() != 1 || losses.Load() != n-1 {
		t.Fatalf("wins=%d losses=%d", wins.Load(), losses.Load())
	}
}

// arrivalBarrier returns a hook fragment that blocks the first n callers of
// point until all n have arrived, so n requests reach the commit queue together.
func arrivalBarrier(n int, point string) func(p string) {
	var mu sync.Mutex
	arrived := 0
	release := make(chan struct{})
	return func(p string) {
		if p != point {
			return
		}
		mu.Lock()
		arrived++
		mine := arrived
		if mine == n {
			close(release)
		}
		mu.Unlock()
		if mine <= n {
			<-release
		}
	}
}

// A journal failure fails every request of the batch, publishes none of them,
// and leaves the store unable to acknowledge anything until it is reopened.
func TestP1B_GroupCommit_JournalFailureFailsWholeBatch(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutObject("b", "durable", []byte("x"), "", nil); err != nil {
		t.Fatal(err)
	}
	const m = 6
	withTestHook(t, arrivalBarrier(m, hookAfterManifestPublished))
	store.journal.f.Close() // every journal write now fails
	fail := make(chan error, m)
	for i := 0; i < m; i++ {
		go func() {
			_, err := store.PutObject("b", fmt.Sprintf("f%d", i), genRandomBytes(int64(100+i), 2000), "", nil)
			fail <- err
		}()
	}
	withTimeout(t, 30*time.Second, "failing batch", func() {
		for i := 0; i < m; i++ {
			if err := <-fail; err == nil {
				t.Error("a commit was acknowledged although the journal write failed")
			}
		}
	})
	for i := 0; i < m; i++ {
		if _, _, err := store.GetObject("b", fmt.Sprintf("f%d", i)); err == nil {
			t.Errorf("failed commit f%d is visible", i)
		}
	}
	if _, err := store.PutObject("b", "after", []byte("y"), "", nil); err == nil {
		t.Error("poisoned journal accepted another commit")
	}
	if _, _, err := store.GetObject("b", "durable"); err != nil {
		t.Errorf("earlier acknowledged object lost: %v", err)
	}
	store.queueDrained(t)
}

func (s *Store) queueDrained(t *testing.T) {
	t.Helper()
	s.commits.mu.Lock()
	defer s.commits.mu.Unlock()
	if len(s.commits.q) != 0 || s.commits.leading {
		t.Errorf("commit queue not drained: %d queued, leading=%v", len(s.commits.q), s.commits.leading)
	}
}

// A crash hook that panics inside the leader must fail the batch members
// instead of stranding them. The journal is then fail-stopped (poisoned): the
// queued follower is released with an error and nothing is committed on top of
// a commit whose outcome the namespace does not know.
func TestP1B_GroupCommit_LeaderPanicDoesNotStrandFollowers(t *testing.T) {
	store, dir := newBucketStore(t)
	barrier := arrivalBarrier(2, hookAfterManifestPublished)
	var armed atomic.Bool
	armed.Store(true)
	entered, gate := make(chan struct{}), make(chan struct{})
	withTestHook(t, func(p string) {
		barrier(p)
		if p == hookAfterJournalWriteBeforeSync && armed.CompareAndSwap(true, false) {
			close(entered)
			<-gate
			panic("simulated crash")
		}
	})
	type res struct {
		pan any
		err error
	}
	out := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func() {
			var r res
			defer func() { r.pan = recover(); out <- r }()
			_, r.err = store.PutObject("b", fmt.Sprintf("k%d", i), genRandomBytes(int64(i+1), 2000), "", nil)
		}()
	}
	<-entered
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		store.commits.mu.Lock()
		q := len(store.commits.q)
		store.commits.mu.Unlock()
		if q >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	close(gate)
	var panics, ok int
	withTimeout(t, 20*time.Second, "leader panic", func() {
		for i := 0; i < 2; i++ {
			r := <-out
			switch {
			case r.pan != nil:
				panics++
			case r.err != nil && strings.Contains(r.err.Error(), "poisoned"):
				ok++ // released with an error, not stranded; the journal is fail-stopped
			default:
				t.Errorf("follower: err=%v, want a poisoned-journal error", r.err)
			}
		}
	})
	if panics != 1 || ok != 1 {
		t.Fatalf("panics=%d released-with-error=%d, want 1 and 1", panics, ok)
	}
	// Fail-stop: nothing more is acknowledged until the store is reopened,
	// and the journal on disk is intact.
	if _, err := store.PutObject("b", "after", []byte("x"), "", nil); err == nil {
		t.Fatal("a commit was acknowledged after the leader panicked mid-commit")
	}
	store.queueDrained(t)
	store.Close()
	re, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("journal unreadable after the interrupted commit: %v", err)
	}
	re.Close()
}

// Publishing many small packs must not copy the whole pack list each time.
func TestP1B_PackLocatorPublishDoesNotCopyAllPacks(t *testing.T) {
	const n = 20000
	st := &packState{}
	var err error
	mk := func(i int) (packInfo, []packEntry) {
		var sum [32]byte
		sum[0], sum[1], sum[2], sum[3] = byte(i), byte(i>>8), byte(i>>16), 0xaa
		return packInfo{id: fmt.Sprintf("%064x", i), tier: tierHot, records: 1},
			[]packEntry{{sha: sum, off: 64, stored: 10, logical: 10}}
	}
	for i := 0; i < n-100; i++ {
		info, es := mk(i)
		if st, err = st.withPack(info, es); err != nil {
			t.Fatal(err)
		}
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for i := n - 100; i < n; i++ {
		info, es := mk(i)
		if st, err = st.withPack(info, es); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	perPublish := (after.TotalAlloc - before.TotalAlloc) / 100
	if perPublish > 64<<10 {
		t.Fatalf("%d bytes allocated per publication at %d packs: the pack list is being copied", perPublish, n)
	}
	// Forking an old snapshot must still be safe: the second successor copies.
	base := st
	a, _ := base.withPack(packInfo{id: fmt.Sprintf("%064x", n+1), tier: tierHot}, nil)
	b, _ := base.withPack(packInfo{id: fmt.Sprintf("%064x", n+2), tier: tierHot}, nil)
	if len(a.packs) != n+1 || len(b.packs) != n+1 || a.packs[n].id == b.packs[n].id {
		t.Fatalf("forked successors share state: %d %d", len(a.packs), len(b.packs))
	}
	if len(base.packs) != n {
		t.Fatal("base snapshot changed length")
	}
}

// --- medium-object packing (production default policy) ---

func mediumPolicy(t *testing.T) {
	t.Helper()
	oldMin, oldFlush, oldKiB := directPackMinBytes, directPackMediumFlushMin, directPackFlushKiB
	directPackMinBytes, directPackMediumFlushMin, directPackFlushKiB = 256<<10, 64<<10, 0
	t.Cleanup(func() { directPackMinBytes, directPackMediumFlushMin, directPackFlushKiB = oldMin, oldFlush, oldKiB })
}

func looseCount(t *testing.T, dir string) int { return len(casLooseChunks(t, dir)) }

func verifyDeepOK(t *testing.T, s *Store) {
	t.Helper()
	res, err := s.Verify(true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("verify -deep: %+v", res)
	}
}

// The policy changes only where new chunks are stored: the manifest of an
// object (chunk list, sizes, digests, ETag) is byte-for-byte what the loose
// policy produces, for sizes around the threshold.
func TestP1B_MediumPack_SameManifestAsLoose(t *testing.T) {
	for _, size := range []int{255 << 10, 256 << 10, 700 << 10, 3 << 20, 9<<20 + 12345} {
		body := genRandomBytes(int64(size), size)
		run := func(medium bool) (manifestV1, *Store, string) {
			if medium {
				mediumPolicy(t)
			} else {
				old := directPackMinBytes
				directPackMinBytes = defaultPackTargetBytes
				t.Cleanup(func() { directPackMinBytes = old })
			}
			store, dir := newBucketStore(t)
			e, err := store.PutObject("b", "k", body, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			man, err := store.readVerifiedManifest(e.manifestUUID, e.manifestSHA256)
			if err != nil {
				t.Fatal(err)
			}
			return man, store, dir
		}
		a, _, _ := run(false)
		b, sb, dirB := run(true)
		a.ManifestUUID, b.ManifestUUID, a.CreatedAt, b.CreatedAt, a.VersionID, b.VersionID = "", "", time.Time{}, time.Time{}, "", ""
		ja, jb := fmt.Sprint(a), fmt.Sprint(b)
		if ja != jb {
			t.Fatalf("size %d: manifests differ between policies", size)
		}
		_, got, err := sb.GetObject("b", "k")
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("size %d: read back differs: %v", size, err)
		}
		verifyDeepOK(t, sb)
		packs, _, _ := sb.packTotals()
		if size >= 256<<10 && (packs == 0 || looseCount(t, dirB) != 0) {
			t.Fatalf("size %d: want packed chunks, got %d packs, %d loose", size, packs, looseCount(t, dirB))
		}
		if size < 256<<10 && packs != 0 {
			t.Fatalf("size %d below the threshold was packed", size)
		}
	}
}

func TestP1B_MediumPack_DedupAndRevisions(t *testing.T) {
	mediumPolicy(t)
	store, dir := newBucketStore(t)
	body := genRandomBytes(11, 2<<20)
	if _, err := store.PutObject("b", "a", body, "", nil); err != nil {
		t.Fatal(err)
	}
	p1, _, _ := store.packTotals()
	if _, err := store.PutObject("b", "dup", body, "", nil); err != nil {
		t.Fatal(err)
	}
	if p2, _, _ := store.packTotals(); p2 != p1 {
		t.Fatalf("an exact duplicate published %d new packs", p2-p1)
	}
	// A small edit: only the few changed chunks are new; they are below the
	// 64 KiB minimum or form one small pack, and never rewrite shared chunks.
	rev := append([]byte(nil), body...)
	copy(rev[1<<20:], []byte("edited-in-the-middle"))
	if _, err := store.PutObject("b", "rev", rev, "", nil); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string][]byte{"a": body, "dup": body, "rev": rev} {
		if _, got, err := store.GetObject("b", k); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %v", k, err)
		}
	}
	verifyDeepOK(t, store)
	_ = dir
}

func TestP1B_MediumPack_RestartKeepsEverything(t *testing.T) {
	mediumPolicy(t)
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.CreateBucket("b")
	want := map[string][]byte{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := genRandomBytes(int64(900+i), 260<<10+i*97000)
			key := fmt.Sprintf("o%d", i)
			if _, err := store.PutObject("b", key, body, "", nil); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			want[key] = body
			mu.Unlock()
		}()
	}
	withTimeout(t, 60*time.Second, "concurrent medium puts", wg.Wait)
	store.Close()
	re, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	for k, body := range want {
		if _, got, err := re.GetObject("b", k); err != nil || !bytes.Equal(got, body) {
			t.Fatalf("%s after restart: %v", k, err)
		}
	}
	verifyDeepOK(t, re)
	if np, _, _ := re.packTotals(); np == 0 {
		t.Fatal("no packs were written")
	}
}

// Past the pack budget medium objects fall back to loose chunks; large ones do not.
func TestP1B_MediumPack_ValveFallsBackToLoose(t *testing.T) {
	mediumPolicy(t)
	old := mediumPackMaxPacks
	mediumPackMaxPacks = 1
	t.Cleanup(func() { mediumPackMaxPacks = old })
	store, dir := newBucketStore(t)
	a := genRandomBytes(21, 1<<20)
	if _, err := store.PutObject("b", "first", a, "", nil); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := store.packTotals(); n != 1 {
		t.Fatalf("first object: %d packs", n)
	}
	b := genRandomBytes(22, 1<<20)
	if _, err := store.PutObject("b", "second", b, "", nil); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := store.packTotals(); n != 1 || looseCount(t, dir) == 0 {
		t.Fatalf("valve not applied: %d packs, %d loose", n, looseCount(t, dir))
	}
	for k, want := range map[string][]byte{"first": a, "second": b} {
		if _, got, err := store.GetObject("b", k); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s: %v", k, err)
		}
	}
	verifyDeepOK(t, store)
}

func TestP1B_MediumPack_MultipartPartsAndCompletion(t *testing.T) {
	mediumPolicy(t)
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.CreateBucket("b")
	uid, err := store.CreateMultipartUpload("b", "mp", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	bodies := [][]byte{
		genRandomBytes(71, minMultipartPartSize+1234),
		genRandomBytes(72, minMultipartPartSize+2468),
		genRandomBytes(73, minMultipartPartSize+3702),
		genRandomBytes(74, 300<<10),
	}
	parts := make([]completedPart, len(bodies))
	for i, b := range bodies {
		etag, err := store.UploadPart("b", "mp", uid, i+1, b)
		if err != nil {
			t.Fatal(err)
		}
		parts[i] = completedPart{PartNumber: i + 1, ETag: etag}
	}
	// Replace part 2 (client retry): completion must use the new bytes.
	bodies[1] = genRandomBytes(99, minMultipartPartSize+777)
	if parts[1].ETag, err = store.UploadPart("b", "mp", uid, 2, bodies[1]); err != nil {
		t.Fatal(err)
	}
	whole := bytes.Join(bodies, nil)
	e, man, err := store.CompleteMultipartUpload("b", "mp", uid, parts)
	if err != nil {
		t.Fatal(err)
	}
	if man.TotalLength != int64(len(whole)) || e.size != int64(len(whole)) {
		t.Fatalf("size %d, want %d", man.TotalLength, len(whole))
	}
	if _, got, err := store.GetObject("b", "mp"); err != nil || !bytes.Equal(got, whole) {
		t.Fatalf("completed object differs: %v", err)
	}
	// The chunk list equals a fresh CDC pass over the logical object, which
	// is what a single PUT of the same bytes records.
	single, err := store.PutObject("b", "single", whole, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	sm, err := store.readVerifiedManifest(single.manifestUUID, single.manifestSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(sm.Chunks) != fmt.Sprint(man.Chunks) || sm.ObjectSHA256 != man.ObjectSHA256 {
		t.Fatal("multipart completion chunked the object differently from a single PUT")
	}
	verifyDeepOK(t, store)
	store.Close()
	re, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	if _, got, err := re.GetObject("b", "mp"); err != nil || !bytes.Equal(got, whole) {
		t.Fatalf("after restart: %v", err)
	}
	verifyDeepOK(t, re)
}

// Packs holding only dead records (a failed conditional PUT, an aborted
// multipart upload, a replaced part) are removed by an ordinary gc; live packs stay.
func TestP1B_MediumPack_DeadPacksCollectedByGC(t *testing.T) {
	mediumPolicy(t)
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	store.CreateBucket("b")
	live := genRandomBytes(31, 1<<20)
	if _, err := store.PutObject("b", "live", live, "", nil); err != nil {
		t.Fatal(err)
	}
	// a failed conditional PUT: its chunks are published, the object is not
	dead := genRandomBytes(32, 1<<20)
	if _, err := store.PutObjectChecked("b", "live", dead, "", nil, putCondition{ifNoneMatchStar: true}); !errors.Is(err, errPreconditionFailed) {
		t.Fatalf("conditional PUT: %v", err)
	}
	// an aborted multipart upload with a replaced part
	uid, err := store.CreateMultipartUpload("b", "mp", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := store.UploadPart("b", "mp", uid, 1, genRandomBytes(int64(40+i), minMultipartPartSize+99)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AbortMultipartUpload("b", "mp", uid); err != nil {
		t.Fatal(err)
	}
	before, _, _ := store.packTotals()
	store.Close()
	res, err := gcCollect(dir, true)
	if err != nil || !res.LiveSetOK {
		t.Fatalf("gc: %v %+v", err, res)
	}
	re, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	after, _, _ := re.packTotals()
	t.Logf("packs before gc %d, after %d (%+v)", before, after, res)
	if after != 1 {
		t.Fatalf("want exactly the live object's pack to remain, have %d (before gc %d)", after, before)
	}
	if _, got, err := re.GetObject("b", "live"); err != nil || !bytes.Equal(got, live) {
		t.Fatalf("live object damaged by gc: %v", err)
	}
	verifyDeepOK(t, re)
}
