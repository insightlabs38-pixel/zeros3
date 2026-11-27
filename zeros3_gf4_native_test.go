package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type gf4Timed struct {
	op         string
	start, end time.Time
	hit        bool
}

func gf4NativeSeed(t *testing.T, input [][]byte) string {
	t.Helper()
	l := newAuthLab(t)
	for i, raw := range input {
		l.put(fmt.Sprintf("archive/%d", i), raw)
	}
	ctx := context.Background()
	mustOK(t, l.s.ensureFeature(agFeature))
	_, err := l.s.AuthorityOptimize(ctx, OptimizePolicy{
		AuthPolicy: AuthPolicy{Apply: true, Order: "oldest", AllowDelta: true, AllowRep: true, CodecReplay: true, MaxDepth: 3},
		Profile:    "archive", Canonical: true, Splice: true, FastReencode: true,
	})
	mustOK(t, err)
	_, err = l.s.finishAuthorityArchive(ctx, 0)
	mustOK(t, err)

	// One isolated partially live pack makes the bounded maintenance pass
	// do real physical work while foreground operations are in flight.
	live := genRandomBytes(2639, 8<<10)
	b, err := l.s.newPackBuilder(tierHot, nil, 7)
	mustOK(t, err)
	defer b.Abort()
	seen := map[[32]byte]bool{}
	gf2AddPackRecord(b, seen, live)
	for i := range 6 {
		gf2AddPackRecord(b, seen, genRandomBytes(int64(59031+i*733), 64<<10))
	}
	path, entries, err := b.Finish()
	mustOK(t, err)
	_, err = l.s.publishPack(tierHot, path, entries)
	mustOK(t, err)
	// The PUT sees the already-published chunk. Ensure this controlled
	// fixture has no alternate copy that would let carry skip the write.
	l.put("maint/live", live)
	part := gf1NativeParts(t, live, l.man("maint/live"))
	if len(part) != 1 || len(l.s.packLocs(part[0].member.SHA)) != 1 {
		t.Fatal("isolated carry source in unexpected geometry")
	}
	if err := os.Remove(l.s.chunkPath(part[0].member.SHA)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	l.verifyOK("maintenance seed with sole packed live record")
	l.close()
	return l.dir
}

func gf4NativeLane(t *testing.T, from string, input [][]byte, mode string, round int) {
	t.Helper()
	dir := t.TempDir()
	mustOK(t, os.CopyFS(dir, os.DirFS(from)))
	if err := os.Remove(filepath.Join(dir, "LOCK")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	s := b4OpenMaint(t, dir)
	l := &authLab{t: t, s: s, dir: dir}
	defer l.close()
	srv, signer := newTestServerAndSignerFor(t, s)
	server := httptest.NewServer(srv)
	defer server.Close()
	baseTotal, _, _ := fb13Allocation(t, dir)
	type bankTarget struct {
		path  string
		input []byte
		start int
	}
	var targets [gf4Keys]bankTarget
	for key := range targets {
		obj := key % len(input)
		targets[key] = bankTarget{fmt.Sprintf("archive/%d", obj), input[obj], (2 + key*3) * gf4CopyBytes}
	}
	old := uint8(0)
	state := gf4Policy{}
	cpu := fb13CPU()
	var times []gf4Timed
	copyCount, peakCopies := 0, 0
	var copyNS []int64
	var maintenance MaintReport
	for epoch := range 12 {
		// Three frozen phases, including a surprise to which the online
		// controller has no future visibility.
		hot := []int{3, 7, 2}[epoch/4]
		e := gf4Epoch{live: 255, slots: 2, writes: 1}
		e.count[hot] = 14
		e.count[(hot+4)%len(targets)] = 2
		if epoch == 4 {
			e.writes = 0
		} // one shared rewrite producer under contention
		desired := state.choose(mode, e, old)
		var wg sync.WaitGroup
		foreground := make(chan gf4Timed, 20)
		start := make(chan struct{})
		if epoch == 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				options := b4Opts()
				options.MaxSourcePacks = 1
				options.MaxRewriteBytes = 512 << 10
				options.MaxStageBytes = 512 << 10
				options.MaxLivePercent = 50
				options.DutyPercent = 20
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var err error
				maintenance, err = s.maintainOnline(ctx, options)
				if err != nil {
					t.Error(err)
				}
			}()
		}
		if promote := desired & ^old; promote != 0 {
			if bits.OnesCount8(promote) != 1 {
				t.Fatal("more than one copy admitted")
			}
			wg.Add(1)
			go func(key int) {
				defer wg.Done()
				<-start
				started := time.Now()
				v := targets[key]
				// Read the authoritative source and independently verify the exact
				// immutable bytes. The old routing remains active for this epoch.
				got := l.read(v.path, byteRange{int64(v.start), int64(v.start + gf4CopyBytes - 1)})
				if !bytes.Equal(got, v.input[v.start:v.start+gf4CopyBytes]) {
					t.Error("copy source proof failed")
					return
				}
				name := fmt.Sprintf("hot/%d", key)
				if _, err := s.PutObject("b", name, got, "application/octet-stream", nil); err != nil {
					t.Error(err)
					return
				}
				verified := l.read(name, byteRange{0, gf4CopyBytes - 1})
				if !bytes.Equal(verified, got) {
					t.Error("copy publish proof failed")
					return
				}
				copyNS = append(copyNS, time.Since(started).Nanoseconds())
			}(bits.TrailingZeros8(promote))
			copyCount++
		}
		for worker := range 2 {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				<-start
				for index := worker; index < 18; index += 2 {
					path, op := "", "GET"
					key := hot
					if index >= 14 {
						key = (hot + 4) % len(targets)
					}
					v := targets[key]
					off := (index*1597 + epoch*341) % (gf4CopyBytes - 4096)
					rangeStart := v.start + off
					path = v.path
					hit := old&(1<<key) != 0
					headers := map[string]string{}
					var sent []byte
					want, status := v.input[rangeStart:rangeStart+4096], http.StatusPartialContent
					switch index {
					case 16:
						op, status, hit = "HEAD", http.StatusOK, false
					case 17:
						op, status, hit = "PUT", http.StatusOK, false
						path = fmt.Sprintf("new/%02d", epoch)
						sent = genRandomBytes(int64(7283+epoch), 768)
					default:
						if hit {
							path = fmt.Sprintf("hot/%d", key)
							rangeStart = off
						}
						headers["Range"] = fmt.Sprintf("bytes=%d-%d", rangeStart, rangeStart+4095)
					}
					began := time.Now()
					resp := doSignedRequest(t, server.Client(), server.URL, signer, op, "/b/"+path, sent, headers)
					body, err := io.ReadAll(resp.Body)
					resp.Body.Close()
					ended := time.Now()
					if err != nil || resp.StatusCode != status {
						t.Errorf("native %s: status %d %v", op, resp.StatusCode, err)
						return
					}
					if op == "GET" && !bytes.Equal(body, want) {
						t.Error("native workload returned different bytes")
						return
					}
					if op == "HEAD" && resp.ContentLength != int64(len(v.input)) {
						t.Error("HEAD length changed")
						return
					}
					foreground <- gf4Timed{op, began, ended, hit}
				}
			}(worker)
		}
		close(start)
		wg.Wait()
		close(foreground)
		for sample := range foreground {
			times = append(times, sample)
		}
		if t.Failed() {
			t.FailNow()
		}
		for removed := old & ^desired; removed != 0; {
			key := bits.TrailingZeros8(removed)
			mustOK(t, s.DeleteObject("b", fmt.Sprintf("hot/%d", key)))
			removed &^= 1 << key
		}
		peakCopies = max(peakCopies, bits.OnesCount8(desired))
		state.observe(e, old, desired)
		old = desired
	}
	workCPU := int64(fb13CPU() - cpu)
	var gt, ht, pt []int64
	var hits, misses, overlap int
	for _, sample := range times {
		if sample.start.Before(maintenance.Finished) && sample.end.After(maintenance.Started) {
			overlap++
		}
		delay := sample.end.Sub(sample.start).Nanoseconds()
		switch sample.op {
		case "GET":
			gt = append(gt, delay)
			if sample.hit {
				hits++
			}
			if delay > gf4SLOUS*1000 {
				misses++
			}
		case "HEAD":
			ht = append(ht, delay)
		case "PUT":
			pt = append(pt, delay)
		}
	}
	if maintenance.RewriteSourceBytes <= 0 || maintenance.RewriteSourceBytes > 512<<10 || maintenance.BytesWritten <= 0 || overlap == 0 {
		t.Fatalf("did not exercise bounded physical contention: %+v overlapping=%d", maintenance, overlap)
	}
	for epoch := range 12 {
		key := fmt.Sprintf("new/%02d", epoch)
		got := l.read(key, byteRange{0, 767})
		if !bytes.Equal(got, genRandomBytes(int64(7283+epoch), 768)) {
			t.Fatal("acknowledged foreground PUT differs")
		}
	}
	l.verifyOK("GF4 native foreground")
	l.close()
	gross, _, _ := fb13Allocation(t, dir)
	_, err := pruneHistoryStore(dir, historyPruneOptions{Bucket: "b", Prefix: "hot/", KeepLast: pruneKeep(0)}, true)
	mustOK(t, err)
	l.open()
	_, err = l.s.finishAuthorityArchive(context.Background(), 0)
	mustOK(t, err)
	l.verifyOK("GF4 native reclaim")
	l.close()
	final, _, _ := fb13Allocation(t, dir)
	line, err := json.Marshal(map[string]any{
		"policy": mode, "round": round, "signed_gets": len(gt), "hot_hits": hits, "get_slo_misses": misses,
		"get_ns": gf0Percentiles(gt), "head_ns": gf0Percentiles(ht), "put_ns": gf0Percentiles(pt),
		"before_allocated": baseTotal, "pre_reclaim_allocated": gross, "after_reclaim_allocated": final,
		"materialized_bytes": copyCount * gf4CopyBytes, "copy_build_ns": gf0Percentiles(copyNS), "hot_peak_logical_bytes": peakCopies * gf4CopyBytes,
		"overlapping_operations": overlap, "work_cpu_ns": workCPU, "maintenance": maintenance,
	})
	mustOK(t, err)
	fmt.Printf("GF4NATIVE %s\n", line)
}

func TestGF4NativePolicyReplay(t *testing.T) {
	root, families := gf0Corpus(t)
	for _, fam := range families {
		if fam.Name != "typescript-gzip" {
			continue
		}
		var input [][]byte
		for i := range fam.Paths {
			data, _ := gf0ReadFixture(t, root, fam, i)
			input = append(input, data)
		}
		physical := gf4FrozenSeed(t, input)
		for round := range 2 {
			lanes := []string{"archive", "lru", "rent-aware", "responsive"}
			if round != 0 {
				for i, j := 0, len(lanes)-1; i < j; i, j = i+1, j-1 {
					lanes[i], lanes[j] = lanes[j], lanes[i]
				}
			}
			for _, mode := range lanes {
				t.Run(fmt.Sprintf("%d/%s", round, mode), func(t *testing.T) { gf4NativeLane(t, physical, input, mode, round) })
			}
		}
	}
}

// Cache only the expensive immutable research seed. The race run still
// clones that seed, does live signed requests and proves every outcome.
func gf4FrozenSeed(t *testing.T, input [][]byte) string {
	t.Helper()
	dir := os.Getenv("ZEROS3_GF4_SEED")
	if dir == "" {
		return gf4NativeSeed(t, input)
	}
	var pin []byte
	for _, raw := range input {
		sum := sha256.Sum256(raw)
		pin = append(pin, sum[:]...)
	}
	marker := filepath.Join(dir, ".gf4-fixture")
	previous, err := os.ReadFile(marker)
	if err == nil {
		if !bytes.Equal(previous, pin) {
			t.Fatal("wrong cached GF4 seed")
		}
		return dir
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	from := gf4NativeSeed(t, input)
	mustOK(t, os.Mkdir(dir, 0700))
	mustOK(t, os.CopyFS(dir, os.DirFS(from)))
	mustOK(t, os.WriteFile(marker, pin, 0600))
	return dir
}
