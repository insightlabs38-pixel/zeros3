package main

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Real-process kill qualification for the P1B publication path. SIGKILL
// models a process crash (page cache survives); it does not model power
// loss, which is covered by the ordering audit (testing-harnesses/z4/p1b).

type ackedPut struct {
	seed int64
	size int
}

// TestP1B_RealKill_AckedObjectsSurviveConcurrentKill hammers a real server
// with concurrent PUTs of mixed sizes (loose chunks, medium packs, shared
// keys), kills it with SIGKILL at a random moment, and checks after every kill
// that each acknowledged object is byte-identical, that no unacknowledged
// object is half-visible, that verify -deep passes and that gc keeps every
// acknowledged object. The store accumulates across rounds.
func TestP1B_RealKill_AckedObjectsSurviveConcurrentKill(t *testing.T) {
	mixed := []int{4 << 10, 20 << 10, 70 << 10, 300 << 10, 1<<20 + 7, 3 << 20}
	small := []int{100, 1 << 10, 4 << 10, 20 << 10, 70 << 10, 200 << 10}
	for _, mode := range []struct {
		name  string
		sizes []int
		env   []string
	}{
		{"default-policy", mixed, nil},
		// P1C: only small objects, 1 MiB segments so kills land in appends, rollovers and seals
		{"segments-small-objects", small, []string{"ZEROS3_SEGMENT_TARGET_MIB=1"}},
		{"loose-only", mixed, []string{"ZEROS3_DIRECT_PACK_MIB=0", "ZEROS3_SMALL_OBJECT_STORAGE=off"}},
	} {
		t.Run(mode.name, func(t *testing.T) { realKillRounds(t, mode.sizes, mode.env) })
	}
}

func realKillRounds(t *testing.T, sizes []int, extraEnv []string) {
	if testing.Short() {
		t.Skip("real-process kill rounds")
	}
	bin := buildZeros3Binary(t)
	storeDir := t.TempDir()
	signer := testSigner{accessKey: defaultAccessKeyID, secretKey: defaultSecretAccessKey, region: defaultRegion}
	acked := map[string]ackedPut{}         // unique keys: exact expectation
	sharedAcked := map[string][]ackedPut{} // shared keys: any acked or in-flight version
	sharedMaybe := map[string][]ackedPut{} // sent, outcome unknown
	var mu sync.Mutex
	rng := rand.New(rand.NewPCG(7, 11))

	for round := 0; round < 6; round++ {
		addr := freeTCPAddr(t)
		srv := exec.Command(bin, "-store", storeDir, "-addr", addr)
		srv.Env = os.Environ()
		srv.Env = append(srv.Env, extraEnv...)
		if err := srv.Start(); err != nil {
			t.Fatal(err)
		}
		waitForZeros3Serve(t, addr)
		client := &http.Client{Timeout: 20 * time.Second}
		if round == 0 {
			resp := doSignedRequest(t, client, "http://"+addr, signer, http.MethodPut, "/b", nil, nil)
			resp.Body.Close()
		}
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for c := 0; c < 12; c++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := rand.New(rand.NewPCG(uint64(round*100+c), 3))
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					size := sizes[r.IntN(len(sizes))]
					seed := int64(round)<<40 | int64(c)<<20 | int64(i)
					body := genRandomBytes(seed, size)
					shared := r.IntN(4) == 0
					key := fmt.Sprintf("u/%d/%d/%d", round, c, i)
					if shared {
						key = fmt.Sprintf("s/%d", r.IntN(5))
					}
					put := ackedPut{seed, size}
					if shared {
						mu.Lock()
						sharedMaybe[key] = append(sharedMaybe[key], put)
						mu.Unlock()
					}
					req, err := http.NewRequest(http.MethodPut, "http://"+addr+"/b/"+key, bytes.NewReader(body))
					if err != nil {
						return
					}
					signTestRequest(t, req, signer, req.URL.Path, req.URL.RawQuery, body, time.Now(), nil)
					resp, err := client.Do(req)
					if err != nil {
						return // killed mid-request: outcome unknown
					}
					resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						continue
					}
					mu.Lock()
					if shared {
						sharedAcked[key] = append(sharedAcked[key], put)
					} else {
						acked[key] = put
					}
					mu.Unlock()
				}
			}()
		}
		time.Sleep(time.Duration(150+rng.IntN(600)) * time.Millisecond)
		srv.Process.Kill()
		srv.Wait()
		close(stop)
		wg.Wait()

		s, err := OpenStore(storeDir)
		if err != nil {
			t.Fatalf("round %d: open after kill: %v", round, err)
		}
		mu.Lock()
		nAcked := len(acked)
		for key, p := range acked {
			_, got, err := s.GetObject("b", key)
			if err != nil || !bytes.Equal(got, genRandomBytes(p.seed, p.size)) {
				mu.Unlock()
				t.Fatalf("round %d: ACKNOWLEDGED object %s lost or corrupt after kill -9: %v", round, key, err)
			}
		}
		for key, versions := range sharedMaybe {
			_, got, err := s.GetObject("b", key)
			if errors.Is(err, errNoSuchKey) {
				if len(sharedAcked[key]) > 0 {
					mu.Unlock()
					t.Fatalf("round %d: acknowledged shared key %s vanished", round, key)
				}
				continue
			}
			if err != nil {
				mu.Unlock()
				t.Fatalf("round %d: shared key %s: %v", round, key, err)
			}
			ok := false
			for _, v := range versions {
				if bytes.Equal(got, genRandomBytes(v.seed, v.size)) {
					ok = true
					break
				}
			}
			if !ok {
				mu.Unlock()
				t.Fatalf("round %d: shared key %s holds bytes nobody wrote", round, key)
			}
		}
		mu.Unlock()
		if v, err := s.Verify(true); err != nil || !v.OK() {
			t.Fatalf("round %d: verify -deep after kill: %v %+v", round, err, v)
		}
		s.Close()
		if res, err := gcCollect(storeDir, true); err != nil || !res.LiveSetOK {
			t.Fatalf("round %d: gc: %v %+v", round, err, res)
		}
		s, err = OpenStore(storeDir)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		for key, p := range acked {
			if _, got, err := s.GetObject("b", key); err != nil || !bytes.Equal(got, genRandomBytes(p.seed, p.size)) {
				mu.Unlock()
				t.Fatalf("round %d: acknowledged object %s damaged by gc: %v", round, key, err)
			}
		}
		mu.Unlock()
		if left, _ := os.ReadDir(storeDir + "/tmp"); len(left) != 0 {
			var names []string
			for _, e := range left {
				names = append(names, e.Name())
			}
			t.Logf("round %d: %d staging files left in tmp/ after gc: %s", round, len(left), strings.Join(names[:min(3, len(names))], ","))
		}
		s.Close()
		t.Logf("round %d: %d acknowledged objects intact (verify -deep OK, gc OK)", round, nAcked)
	}
}

// --- deterministic crash matrix (simulated crash at every durability boundary) ---

func claimsEmpty(r *pubRegistry) bool {
	for i := range r.sh {
		r.sh[i].mu.Lock()
		n := len(r.sh[i].m)
		r.sh[i].mu.Unlock()
		if n != 0 {
			return false
		}
	}
	return true
}

// TestP1B_CrashMatrix_ConcurrentGroups simulates a crash at the Nth firing of
// each durability boundary while a group of concurrent PUTs (one shared body,
// one unique body each, and a body sharing chunks with an earlier acknowledged
// object) is in flight. After the crash every goroutine dies at its next hook
// (a crashed process does nothing further). The store is then reopened and:
// every acknowledged PUT is byte-identical, every other PUT is absent or
// complete, verify -deep passes, gc keeps all acknowledged data, no claim or
// queue entry leaked, and every unacknowledged PUT can be retried.
func TestP1B_CrashMatrix_ConcurrentGroups(t *testing.T) {
	useLooseLayout(t, "crash boundaries of the loose publication path (segment boundaries: TestP1C_Crash*)")
	points := []string{
		hookCASAfterStage, hookCASBeforeStageSync, hookCASAfterStageSync, hookCASBeforeRename,
		hookCASAfterFirstRename, hookCASBeforeDirSync, hookCASAfterDirSync, hookCASAfterFlush,
		hookAfterChunksPublished, hookAfterManifestPublished,
		hookAfterJournalWriteBeforeSync, hookAfterJournalSync, hookAfterApplyBeforeResponse,
		hookPackRecordWritten, hookPackBeforeSync, hookPackAfterSync, hookPackBeforePublish,
		hookPackAfterRename, hookPackAfterDirSync, hookPackPublished,
	}
	reached, total := 0, 0
	unreached := map[string]int{}
	for _, policy := range []string{"loose", "medium"} {
		for _, group := range []int{1, 6} {
			for _, point := range points {
				for _, nth := range []int{1, 3} {
					name := fmt.Sprintf("%s/g%d/%s/n%d", policy, group, point, nth)
					total++
					t.Run(name, func(t *testing.T) {
						if crashCase(t, policy, group, point, nth) {
							reached++
						} else {
							unreached[point]++
						}
					})
				}
			}
		}
	}
	t.Logf("crash actually injected in %d of %d cases; point never fired that many times (clean run, still verified): %v", reached, total, unreached)
	if reached < total/2 {
		t.Fatalf("only %d of %d cases injected a crash: the matrix is not exercising the boundaries", reached, total)
	}
}

func crashCase(t *testing.T, policy string, group int, point string, nth int) (reached bool) {
	if policy == "medium" {
		mediumPolicy(t)
	} else {
		old, oldSeg := directPackMinBytes, segMaxObjectBytes
		directPackMinBytes, segMaxObjectBytes = defaultPackTargetBytes, 0
		t.Cleanup(func() { directPackMinBytes, segMaxObjectBytes = old, oldSeg })
	}
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.CreateBucket("b")
	base := genRandomBytes(5, 600<<10)
	shared := genRandomBytes(6, 400<<10)
	if _, err := s.PutObject("b", "pre", base, "", nil); err != nil { // acknowledged before the crash
		t.Fatal(err)
	}
	bodies := map[string][]byte{}
	for i := 0; i < group; i++ {
		u := append([]byte(nil), base...) // shares chunks with "pre"
		copy(u[300<<10:], genRandomBytes(int64(100+i), 20<<10))
		bodies[fmt.Sprintf("uniq%d", i)] = u
		bodies[fmt.Sprintf("dup%d", i)] = shared
	}
	var fired atomic.Int32
	var crashed atomic.Bool
	withTestHook(t, func(p string) {
		if crashed.Load() {
			panic(simulatedCrash{point: p}) // a crashed process does nothing further
		}
		if p == point && fired.Add(1) == int32(nth) {
			crashed.Store(true)
			panic(simulatedCrash{point: p})
		}
	})
	var mu sync.Mutex
	ackedKeys := map[string]bool{}
	var wg sync.WaitGroup
	for k, body := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { recover() }()
			if _, err := s.PutObject("b", k, body, "", nil); err == nil {
				mu.Lock()
				ackedKeys[k] = true
				mu.Unlock()
			}
		}()
	}
	withTimeout(t, 60*time.Second, "crashed group", wg.Wait)
	testHook = nil
	reached = crashed.Load()
	if !claimsEmpty(&s.pub) || !claimsEmpty(&s.dirClaims) {
		t.Fatal("publication claims leaked by a crashed request")
	}
	s.commits.mu.Lock()
	stuck := len(s.commits.q) != 0
	s.commits.mu.Unlock()
	if stuck {
		t.Fatal("commit queue entries stranded by a crash")
	}
	s.Close()

	r, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer r.Close()
	check := func(stage string) {
		t.Helper()
		if _, got, err := r.GetObject("b", "pre"); err != nil || !bytes.Equal(got, base) {
			t.Fatalf("%s: previously acknowledged object damaged: %v", stage, err)
		}
		for k, body := range bodies {
			_, got, err := r.GetObject("b", k)
			switch {
			case err == nil && !bytes.Equal(got, body):
				t.Fatalf("%s: %s is visible with wrong bytes", stage, k)
			case err != nil && !errors.Is(err, errNoSuchKey):
				t.Fatalf("%s: %s: %v", stage, k, err)
			case err != nil && ackedKeys[k]:
				t.Fatalf("%s: ACKNOWLEDGED %s is missing after the crash", stage, k)
			}
		}
	}
	check("after crash")
	if v, err := r.Verify(true); err != nil || !v.OK() {
		t.Fatalf("verify -deep: %v %+v", err, v)
	}
	r.Close()
	if res, err := gcCollect(dir, true); err != nil || !res.LiveSetOK {
		t.Fatalf("gc: %v %+v", err, res)
	}
	r2, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	r = r2
	check("after gc")
	for k, body := range bodies { // retry of everything that did not commit
		if _, _, err := r.GetObject("b", k); err == nil {
			continue
		}
		if _, err := r.PutObject("b", k, body, "", nil); err != nil {
			t.Fatalf("retry of %s: %v", k, err)
		}
		if _, got, err := r.GetObject("b", k); err != nil || !bytes.Equal(got, body) {
			t.Fatalf("retried %s unreadable: %v", k, err)
		}
	}
	if v, err := r.Verify(true); err != nil || !v.OK() {
		t.Fatalf("verify -deep after retries: %v %+v", err, v)
	}
	return reached
}
