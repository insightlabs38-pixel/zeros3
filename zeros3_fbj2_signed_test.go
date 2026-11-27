package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fbj2SignedFixture struct {
	t      *testing.T
	server *httptest.Server
	signer testSigner
}

func (f fbj2SignedFixture) request(method, key string, body []byte, status int, want []byte) {
	f.t.Helper()
	req := mustSignedRequest(f.t, f.signer, method, f.server.URL+"/bench/"+key, body)
	resp, err := f.server.Client().Do(req)
	if err != nil {
		f.t.Error(err)
		return
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != status || (want != nil && !bytes.Equal(got, want)) {
		f.t.Errorf("%s %s: got %d, expected %d (%v)", method, key, resp.StatusCode, status, err)
	}
}

func fbj2Fill(b []byte, a, c uint64) {
	rng := rand.New(rand.NewPCG(a, c))
	for i := 0; i < len(b); i += 8 {
		binary.BigEndian.PutUint64(b[i:i+8], rng.Uint64())
	}
}

func fbj2Percentiles(d []time.Duration) map[string]int64 {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	if len(d) == 0 {
		return nil
	}
	return map[string]int64{"p50": d[len(d)/2].Nanoseconds(), "p95": d[len(d)*95/100].Nanoseconds(),
		"p99": d[len(d)*99/100].Nanoseconds(), "samples": int64(len(d))}
}

func fbj2SignedRun(t *testing.T, mode string, round, workers, size int, mixed bool, archive []byte) {
	format, watermark := "", mode
	if mode == "j2" || mode == "j2bin" {
		format, watermark = "j2", "lazy"
	}
	t.Setenv("ZEROS3_JOURNAL_FORMAT", format)
	t.Setenv("ZEROS3_JOURNAL_WATERMARK", watermark)
	dir := t.TempDir()
	s, err := openStoreWithFeatures(dir, true, nsCheckpointDefault(), false, false, false, journalResearch{binary: mode == "j2bin"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.CreateBucket("bench"); err != nil {
		t.Fatal(err)
	}
	srv, signer := newTestServerAndSignerFor(t, s)
	server := httptest.NewServer(srv)
	defer server.Close()
	f := fbj2SignedFixture{t: t, server: server, signer: signer}
	hot := make([]byte, 4096)
	fbj2Fill(hot, 77, 405)
	f.request(http.MethodPut, "hot", hot, 200, nil)
	f.request(http.MethodPut, "archive.tar.gz", archive, 200, nil)
	started := make(chan struct{})
	done := make(chan struct{})
	var reader sync.WaitGroup
	var reads, heads, full []time.Duration
	reader.Go(func() {
		<-started
		for i := 0; ; i++ {
			begin := time.Now()
			switch i % 8 {
			case 0:
				f.request(http.MethodGet, "archive.tar.gz", nil, 200, archive)
				full = append(full, time.Since(begin))
			case 1, 3, 5:
				f.request(http.MethodHead, "hot", nil, 200, nil)
				heads = append(heads, time.Since(begin))
			default:
				f.request(http.MethodGet, "hot", nil, 200, hot)
				reads = append(reads, time.Since(begin))
			}
			select {
			case <-done:
				return
			default:
			}
		}
	})
	total := max(256, workers*16)
	if os.Getenv("FBJ2_PROFILE_MODES") != "" {
		total *= 4
	}
	latencies := make([][]time.Duration, workers)
	var writers sync.WaitGroup
	for worker := range workers {
		writers.Go(func() {
			body := make([]byte, size)
			fbj2Fill(body, uint64(worker+1), 99)
			mine := make([]time.Duration, total/workers)
			<-started
			for i := range mine {
				key := fmt.Sprintf("w%d-k%d", worker, (i/3)%32)
				binary.BigEndian.PutUint64(body, uint64(i))
				at := time.Now()
				switch {
				case mixed && i%3 == 1:
					f.request(http.MethodHead, key, nil, 200, nil)
				case mixed && i%3 == 2:
					f.request(http.MethodDelete, key, nil, 204, nil)
				default:
					f.request(http.MethodPut, key, body, 200, nil)
				}
				mine[i] = time.Since(at)
			}
			latencies[worker] = mine
		})
	}
	begin := time.Now()
	close(started)
	writers.Wait()
	elapsed := time.Since(begin)
	close(done)
	reader.Wait()
	s.Close()
	at := time.Now()
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	open := time.Since(at)
	defer reopened.Close()
	for key, want := range map[string][]byte{"hot": hot, "archive.tar.gz": archive} {
		_, got, err := reopened.GetObject("bench", key)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("restart changed %s: %v", key, err)
		}
	}
	var all []time.Duration
	for _, slice := range latencies {
		all = append(all, slice...)
	}
	fbj2Report(map[string]any{"kind": "signed-engine", "mode": mode, "round": round, "writers": workers,
		"object_bytes": size, "mixed": mixed, "writer_ops_s": float64(len(all)) / elapsed.Seconds(),
		"request_ns": fbj2Percentiles(all), "read_4096_ns": fbj2Percentiles(reads), "head_ns": fbj2Percentiles(heads),
		"whole_archive_ns": fbj2Percentiles(full), "reopen_ns_cache_ambient": open.Nanoseconds(),
		"journal_bytes": reopened.journal.writeOffset})
}

func TestFBJ2SignedEngine(t *testing.T) {
	input := os.Getenv("FBJ2_ARCHIVE")
	if input == "" {
		t.Skip("set FBJ2_ARCHIVE to a pinned C19 gzip input")
	}
	archive, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	modes := []string{"off", "lazy", "sync", "j2"}
	if selected := os.Getenv("FBJ2_PROFILE_MODES"); selected != "" {
		modes = strings.Split(selected, ",")
		for _, mode := range modes {
			if mode != "off" && mode != "lazy" && mode != "sync" && mode != "j2" {
				t.Fatalf("unknown profile mode %q", mode)
			}
		}
		for round := range 5 {
			for i := range modes {
				fbj2SignedRun(t, modes[(i+round)%len(modes)], round, 8, 32<<10, false, archive)
			}
		}
		return
	}
	for round := range 3 {
		for _, point := range []struct {
			size  int
			mixed bool
		}{{1024, false}, {32 << 10, false}, {32 << 10, true}} {
			for _, workers := range []int{1, 8, 32} {
				for i := range modes {
					fbj2SignedRun(t, modes[(i+round)%len(modes)], round, workers, point.size, point.mixed, archive)
				}
			}
		}
	}
}
