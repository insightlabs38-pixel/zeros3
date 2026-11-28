package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"runtime/metrics"
	"runtime/pprof"
	"runtime/trace"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func satCounterFile(path string) map[string]int64 {
	out := make(map[string]int64)
	data, _ := os.ReadFile(path)
	for line := range strings.SplitSeq(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			if n, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				out[strings.TrimSuffix(f[0], ":")] = n
			}
		}
	}
	return out
}

func satDiff(before, after map[string]int64) map[string]int64 {
	out := make(map[string]int64)
	for k, v := range after {
		out[k] = v - before[k]
	}
	return out
}

func satScheduler() *metrics.Float64Histogram {
	m := []metrics.Sample{{Name: "/sched/latencies:seconds"}}
	metrics.Read(m)
	h := m[0].Value.Float64Histogram()
	return &metrics.Float64Histogram{Buckets: slices.Clone(h.Buckets), Counts: slices.Clone(h.Counts)}
}

func satPositiveEnv(t *testing.T, key string) int {
	t.Helper()
	v, err := strconv.Atoi(os.Getenv(key))
	mustOK(t, err)
	if v < 1 {
		t.Fatalf("%s must be positive", key)
	}
	return v
}

type satProbe struct {
	whole, small bool
	n            int64
	ns           int64
	err          error
}

func TestDFSatSigned(t *testing.T) {
	family := os.Getenv("ZEROS3_SAT_FAMILY")
	if family == "" {
		t.Skip("set ZEROS3_SAT_FAMILY for a saturation experiment")
	}
	if family != "curl" && family != "lua" && (family != "nginx" || os.Getenv("ZEROS3_SAT_FROZEN") != "1") {
		t.Fatal("family is outside the allowed partition")
	}
	n := satPositiveEnv(t, "ZEROS3_SAT_COUNT")
	clients := satPositiveEnv(t, "ZEROS3_SAT_CLIENTS")
	round, _ := strconv.Atoi(os.Getenv("ZEROS3_SAT_ROUND"))
	pair := 0
	if requested := os.Getenv("ZEROS3_SAT_PAIR"); requested != "" {
		var err error
		pair, err = strconv.Atoi(requested)
		mustOK(t, err)
	}
	if clients > 32 || n > 2400 {
		t.Fatal("outside measured bounds")
	}
	var objs []df0Object
	for _, f := range df0Corpus(t) {
		if f.Name != family {
			continue
		}
		if pair < 0 || pair+1 >= len(f.Files) {
			t.Fatal("invalid registered pair")
		}
		for i, pin := range f.Files[pair : pair+2] {
			objs = append(objs, df0Object{fmt.Sprintf("version/%02d", i), df0Fixture(t, pin)})
		}
		if os.Getenv("ZEROS3_SAT_DIVERSE") == "1" {
			if pair != 0 {
				t.Fatal("diverse control starts from the first registered pair")
			}
			for i, pin := range f.Files[2:] {
				objs = append(objs, df0Object{fmt.Sprintf("version/%02d", i+2), df0Fixture(t, pin)})
			}
		}
	}
	if len(objs) < 2 {
		t.Fatal("missing pinned history")
	}
	seed := df0Prepare(t, "translate", objs)
	if seed == nil {
		t.Fatal("missing native body")
	}
	clone := &authLab{t: t, dir: t.TempDir()}
	mustOK(t, os.CopyFS(clone.dir, os.DirFS(seed.dir)))
	var cold df0CachePages
	if os.Getenv("ZEROS3_SAT_COLD") == "1" {
		cold = df0DropPages(t, clone.dir)
	}
	clone.open()
	defer clone.close()
	if os.Getenv("ZEROS3_SAT_ALIASES") == "1" {
		// Present a distinct authorized identity on each concurrent request
		// while preserving exactly the same underlying compressed payload.
		original := objs[1]
		objs = objs[:1]
		for i := range clients {
			key := fmt.Sprintf("aliases/%02d", i)
			_, _, err := clone.s.CopyObject(CopyObjectRequest{
				SrcBucket: "b", SrcKey: original.key, DstBucket: "b", DstKey: key,
			})
			mustOK(t, err)
			objs = append(objs, df0Object{key, original.raw})
		}
	}
	allocated, packs, _ := fb13Allocation(t, clone.dir)
	srv, signer := newTestServerAndSignerFor(t, clone.s)
	mode := os.Getenv("ZEROS3_SAT_MODE")
	controls := satConfigure(t, srv, mode)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = clients + 1
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 45 * time.Second}
	cpuStat := "/sys/fs/cgroup/cpu.stat"
	ioStat := "/proc/self/io"
	rssStat := "/proc/self/status"
	var before, after runtime.MemStats
	var stage *os.File
	profileDir := os.Getenv("ZEROS3_SAT_PROFILE")
	if profileDir != "" {
		mustOK(t, os.MkdirAll(profileDir, 0700))
		var err error
		stage, err = os.Create(filepath.Join(profileDir, "cpu.pprof"))
		mustOK(t, err)
		defer stage.Close()
		mustOK(t, pprof.StartCPUProfile(stage))
		defer pprof.StopCPUProfile()
		runtime.SetMutexProfileFraction(1)
		runtime.SetBlockProfileRate(100000)
		defer runtime.SetMutexProfileFraction(0)
		defer runtime.SetBlockProfileRate(0)
		if os.Getenv("ZEROS3_SAT_TRACE") == "1" {
			file, err := os.Create(filepath.Join(profileDir, "schedule.trace"))
			mustOK(t, err)
			defer file.Close()
			mustOK(t, trace.Start(file))
			defer trace.Stop()
		}
	}
	var chunks, logical, stored, packTouches int64
	var statsMu sync.Mutex
	if os.Getenv("ZEROS3_SAT_OBSERVE") == "1" {
		repSourceObserver = func(s repReadStats, _ repCodecTrace) {
			statsMu.Lock()
			defer statsMu.Unlock()
			chunks += s.Chunks
			logical += s.LogicalBytes
			stored += s.StoredBytes
			packTouches += int64(len(s.Packs))
		}
		defer func() { ts.Close(); repSourceObserver = nil }()
	}
	sum := make(map[string][32]byte)
	for _, o := range objs {
		sum[o.key] = sha256.Sum256(o.raw)
	}
	out := make(chan satProbe, n)
	workload := os.Getenv("ZEROS3_SAT_WORKLOAD")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := range clients {
		wg.Go(func() {
			<-start
			for i := worker; i < n; i += clients {
				object := objs[1+(i+round)%(len(objs)-1)]
				req := mustSignedRequest(t, signer, "GET", ts.URL+"/b/"+object.key, nil)
				want := sum[object.key]
				size := int64(len(object.raw))
				p := satProbe{whole: true}
				code := http.StatusOK
				if workload == "ranges" && (i+round)%4 != 0 {
					length := 64 << 10
					p.small = i%2 == 0
					if p.small {
						length = 4096
					}
					offset := (129313 + i*16673 + round*7219) % (len(object.raw) - length)
					req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
					want = sha256.Sum256(object.raw[offset : offset+length])
					size = int64(length)
					code = http.StatusPartialContent
					p.whole = false
				}
				now := time.Now()
				r, err := client.Do(req)
				if err == nil {
					h := sha256.New()
					p.n, err = io.Copy(h, r.Body)
					r.Body.Close()
					if err == nil && (r.StatusCode != code || p.n != size || [32]byte(h.Sum(nil)) != want) {
						err = fmt.Errorf("signed status %d or digest/length differs for %s", r.StatusCode, object.key)
					}
				}
				p.ns = time.Since(now).Nanoseconds()
				p.err = err
				out <- p
			}
		})
	}
	writerDone := make(chan int, 1)
	var written map[string][]byte
	writerCtx, stopWriter := context.WithCancel(t.Context())
	defer stopWriter()
	var sampleDone = make(chan struct{})
	highWater := make(chan [2]uint64, 1)
	go func() {
		tick := time.NewTicker(30 * time.Millisecond)
		defer tick.Stop()
		var peak [2]uint64
		sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		for {
			select {
			case <-tick.C:
				metrics.Read(sample)
				peak[0] = max(peak[0], sample[0].Value.Uint64())
				peak[1] = max(peak[1], uint64(satCounterFile(rssStat)["VmRSS"])*1024)
			case <-sampleDone:
				highWater <- peak
				return
			}
		}
	}()
	if workload == "put" {
		written = make(map[string][]byte)
		go func() {
			<-start
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for i := 0; i < 64; i++ {
				select {
				case <-writerCtx.Done():
					writerDone <- len(written)
					return
				case <-tick.C:
				}
				key := fmt.Sprintf("writes/%04d", i)
				data := genRandomBytes(int64(i)+651, 769)
				r := doSignedRequest(t, client, ts.URL, signer, "PUT", "/b/"+key, data, nil)
				r.Body.Close()
				if r.StatusCode != 200 {
					t.Error("signed concurrent PUT failed")
					break
				}
				written[key] = data
			}
			writerDone <- len(written)
		}()
	}
	runtime.ReadMemStats(&before)
	schedStart := satScheduler()
	cg, proc := satCounterFile(cpuStat), satCounterFile(ioStat)
	startCPU, startWall := fb13CPU(), time.Now()
	close(start)
	wg.Wait()
	elapsed, cpu := time.Since(startWall), fb13CPU()-startCPU
	stopWriter()
	writes := 0
	if workload == "put" {
		writes = <-writerDone
	}
	close(sampleDone)
	peak := <-highWater
	close(out)
	runtime.ReadMemStats(&after)
	schedEnd := satScheduler()
	sched := make(map[string]uint64)
	for i, v := range schedEnd.Counts {
		if d := v - schedStart.Counts[i]; d != 0 {
			sched[fmt.Sprint(schedEnd.Buckets[i+1])] = d
		}
	}
	samples := make(map[string][]int64)
	var bytesOut int64
	for p := range out {
		mustOK(t, p.err)
		op := "whole"
		if !p.whole {
			op = "range64"
			if p.small {
				op = "range4"
			}
		}
		samples[op] = append(samples[op], p.ns)
		bytesOut += p.n
	}
	ts.Close() // all sources closed before collecting observer results
	byClass := make(map[string][3]int64)
	for op, ns := range samples {
		byClass[op] = gf0Percentiles(slices.Clone(ns))
	}
	df0Log(t, "DF2SAT", map[string]any{"family": family, "clients": clients, "round": round, "mode": mode,
		"objects": len(objs) - 1, "workload": workload, "operations": n, "puts": writes, "bytes": bytesOut,
		"cpu_ns": cpu.Nanoseconds(), "wall_ns": elapsed.Nanoseconds(), "latency_ns": samples, "p50_p95_p99_ns": byClass,
		"allocs": after.Mallocs - before.Mallocs, "alloc_bytes": after.TotalAlloc - before.TotalAlloc,
		"gc_cycles": after.NumGC - before.NumGC, "gc_pause_ns": after.PauseTotalNs - before.PauseTotalNs,
		"heap_start": before.HeapAlloc, "heap_peak": peak[0], "rss_peak": peak[1],
		"cpu_stat": satDiff(cg, satCounterFile(cpuStat)), "proc_io": satDiff(proc, satCounterFile(ioStat)),
		"scheduler_wait_upper_sec": sched, "cache_before_open": cold, "record_reads": chunks,
		"record_logical": logical, "record_stored": stored, "pack_touches": packTouches, "controls": controls(),
		"allocated_bytes": allocated, "pack_allocated_bytes": packs, "pair": pair,
	})
	for key, data := range written {
		if got := clone.read(key, byteRange{0, int64(len(data) - 1)}); sha256.Sum256(got) != sha256.Sum256(data) {
			t.Fatal("acknowledged PUT differs")
		}
	}
	if profileDir != "" {
		for _, kind := range []string{"mutex", "block"} {
			f, err := os.Create(filepath.Join(profileDir, kind+".pprof"))
			mustOK(t, err)
			mustOK(t, pprof.Lookup(kind).WriteTo(f, 0))
			mustOK(t, f.Close())
		}
	}
}

func satConfigure(t *testing.T, srv *Server, mode string) func() any {
	t.Helper()
	switch mode {
	case "", "baseline":
		return func() any { return nil }
	case "gate", "fifo", "merge":
		replay := newSatReplay(mode == "merge")
		if mode == "fifo" {
			replay.admission = make(chan struct{}, satReplaySlots)
		}
		srv.researchReplay = func(ctx context.Context, bucket, key string, man *servedManifest) (objectReader, error) {
			return replay.serve(srv.store, ctx, bucket, key, man)
		}
		return func() any { replay.mu.Lock(); defer replay.mu.Unlock(); return replay.stats }
	default:
		t.Fatalf("unsupported experiment mode %s", mode)
		return nil
	}
}
