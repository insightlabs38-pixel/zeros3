package main

// FB-02 M-A6 snapshot-describe evidence: fresh (full read per describe)
// against one restore session's index, on synthetic snapshots whose N entries
// all name one real manifest, so the snapshot read is what differs.
//
//   go test -run '^$' -bench '^BenchmarkFB02Snapshot' -benchtime 20x -count 5 .
//   FB02_PROFILE='mode=session n=10000 case=ok' go test -count=1 -run '^TestFB02SnapshotProfile$' .
//
// The profile test runs one configuration per process, so its peak RSS
// (VmHWM, reset after the fixture is built) belongs to that run alone.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fb02SyntheticSnapshot(tb testing.TB, store *Store, n int) (string, int64) {
	tb.Helper()
	if _, err := store.PutObject("b", "seed", genRandomBytes(77, 4096), "text/plain", map[string]string{"m": "v"}); err != nil {
		tb.Fatal(err)
	}
	obj, _, err := store.headServed("b", "seed")
	if err != nil {
		tb.Fatal(err)
	}
	entries := make([]snapshotEntryV1, n)
	for i := range entries {
		entries[i] = snapshotEntryV1{Key: fmt.Sprintf("src/dir-%03d/object-%07d.bin", i%1000, i), ManifestUUID: obj.manifestUUID,
			ManifestSHA256: hex.EncodeToString(obj.manifestSHA256[:]), Size: obj.size, ETag: obj.etag, ContentType: obj.contentType}
	}
	// Keys must be strictly ascending.
	sort.Slice(entries, func(a, b int) bool { return entries[a].Key < entries[b].Key })
	d := snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(), CreatedAt: time.Now().UTC(),
		SourceBucket: "b", SourcePrefix: "src", Entries: entries}
	if err := store.publishSnapshot(d); err != nil {
		tb.Fatal(err)
	}
	fi, err := os.Stat(store.snapshotPath(d.SnapshotID))
	if err != nil {
		tb.Fatal(err)
	}
	return d.SnapshotID, fi.Size()
}

func fb02BenchSnapshotDescribe(b *testing.B, n int, session bool) {
	store, err := OpenStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateBucket("b"); err != nil {
		b.Fatal(err)
	}
	creds := Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}
	srv := NewServer(store, creds, "us-east-1")
	id, payload := fb02SyntheticSnapshot(b, store, n)
	d, err := store.readSnapshot(id)
	if err != nil {
		b.Fatal(err)
	}
	token := ""
	if session {
		if token, _, err = srv.snapSessions.open(store, id); err != nil {
			b.Fatal(err)
		}
	}
	empty := sha256.Sum256(nil)
	reqs := make([]*http.Request, 64)
	for i := range reqs {
		q := url.Values{"id": {id}, "key": {d.Entries[(i*7919)%n].Key}}
		if token != "" {
			q.Set("session", token)
		}
		reqs[i] = httptest.NewRequest(http.MethodGet, zeros3SnapshotObjectPath+"?"+q.Encode(), nil)
		if err := signSigV4Request(reqs[i], creds, "us-east-1", hex.EncodeToString(empty[:]), time.Now()); err != nil {
			b.Fatal(err)
		}
	}
	d = snapshotDescriptorV1{}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, reqs[i%len(reqs)])
		if rec.Code != 200 {
			b.Fatalf("describe: %d %s", rec.Code, rec.Body.String())
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(payload), "payload-B")
}

func fb02BenchSnapshotOpen(b *testing.B, n int) {
	store, err := OpenStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateBucket("b"); err != nil {
		b.Fatal(err)
	}
	srv := NewServer(store, Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}, "us-east-1")
	id, payload := fb02SyntheticSnapshot(b, store, n)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		token, _, err := srv.snapSessions.open(store, id)
		if err != nil {
			b.Fatal(err)
		}
		srv.snapSessions.close(token)
	}
	b.StopTimer()
	b.ReportMetric(float64(payload), "payload-B")
}

func BenchmarkFB02SnapshotDescribeFresh1k(b *testing.B)    { fb02BenchSnapshotDescribe(b, 1000, false) }
func BenchmarkFB02SnapshotDescribeSession1k(b *testing.B)  { fb02BenchSnapshotDescribe(b, 1000, true) }
func BenchmarkFB02SnapshotDescribeFresh10k(b *testing.B)   { fb02BenchSnapshotDescribe(b, 10000, false) }
func BenchmarkFB02SnapshotDescribeSession10k(b *testing.B) { fb02BenchSnapshotDescribe(b, 10000, true) }
func BenchmarkFB02SnapshotDescribeFresh100k(b *testing.B) {
	fb02BenchSnapshotDescribe(b, 100000, false)
}
func BenchmarkFB02SnapshotDescribeSession100k(b *testing.B) {
	fb02BenchSnapshotDescribe(b, 100000, true)
}
func BenchmarkFB02SnapshotSessionOpen1k(b *testing.B)   { fb02BenchSnapshotOpen(b, 1000) }
func BenchmarkFB02SnapshotSessionOpen10k(b *testing.B)  { fb02BenchSnapshotOpen(b, 10000) }
func BenchmarkFB02SnapshotSessionOpen100k(b *testing.B) { fb02BenchSnapshotOpen(b, 100000) }

func fb02ProcKiB(field string) int64 {
	data, _ := os.ReadFile("/proc/self/status")
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, field+":") {
			v, _ := strconv.ParseInt(strings.Fields(line)[1], 10, 64)
			return v
		}
	}
	return -1
}

// TestFB02SnapshotProfile runs one restore (case ok, deleted, corrupt), one
// describe loop (case describe) or one heap measurement (case heap) and
// prints a FB02PROFILE line. It is skipped unless FB02_PROFILE is set.
func TestFB02SnapshotProfile(t *testing.T) {
	spec := os.Getenv("FB02_PROFILE")
	if spec == "" {
		t.Skip("set FB02_PROFILE='mode=fresh|session n=N case=ok|deleted|corrupt|describe|heap'")
	}
	opt := map[string]string{}
	for _, f := range strings.Fields(spec) {
		k, v, _ := strings.Cut(f, "=")
		opt[k] = v
	}
	n, _ := strconv.Atoi(opt["n"])
	session := opt["mode"] == "session"
	srv, _, cfg, closeFn := newSnapshotTestServer(t, "b")
	defer closeFn()
	if !session {
		srv.snapSessions = nil
	}
	id, payload := fb02SyntheticSnapshot(t, srv.store, n)
	out := map[string]any{"mode": opt["mode"], "n": n, "case": opt["case"], "payload_bytes": payload}
	var decodes atomic.Int64
	var acks atomic.Int64
	withTestHook(t, func(point string) {
		switch point {
		case hookSnapshotDecoded:
			decodes.Add(1)
		case hookAfterAck:
			if acks.Add(1) != int64(n/2) {
				return
			}
			switch opt["case"] {
			case "deleted":
				if err := srv.store.deleteSnapshot(id); err != nil {
					t.Errorf("delete: %v", err)
				}
			case "corrupt":
				p := srv.store.snapshotPath(id)
				data, _ := os.ReadFile(p)
				data[len(data)/2] ^= 0x40
				_ = os.WriteFile(p, data, 0o644)
				later := time.Now().Add(time.Second)
				_ = os.Chtimes(p, later, later)
			}
		}
	})
	runtime.GC()
	debug.FreeOSMemory()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	heap0 := ms.HeapAlloc
	out["rss_base_kib"] = fb02ProcKiB("VmRSS")
	if err := os.WriteFile("/proc/self/clear_refs", []byte("5"), 0); err != nil {
		t.Logf("cannot reset VmHWM: %v", err)
	}
	start := time.Now()
	switch opt["case"] {
	case "heap":
		token, _, err := srv.snapSessions.open(srv.store, id)
		if err != nil {
			t.Fatal(err)
		}
		runtime.GC()
		runtime.ReadMemStats(&ms)
		out["index_retained_bytes"] = int64(ms.HeapAlloc) - int64(heap0)
		out["index_cost_estimate"] = srv.snapSessions.bytes
		out["retained_over_payload"] = float64(int64(ms.HeapAlloc)-int64(heap0)) / float64(payload)
		srv.snapSessions.close(token)
	case "describe":
		token := ""
		if session {
			token = openSnapshotSession(cfg, id)
		}
		d, _ := srv.store.readSnapshot(id)
		decodes.Store(0)
		start = time.Now()
		for i := 0; i < 20; i++ {
			if _, err := fetchSnapshotObjectDescriptorIn(cfg, id, d.Entries[(i*7919)%n].Key, token); err != nil {
				t.Fatal(err)
			}
		}
		out["describes"] = 20
	default:
		dest := cfg
		dest.Bucket = "b"
		res, err := restoreNamespace(restoreNamespaceConfig{Snapshot: cfg, SnapshotID: id, Dest: dest, DestPrefix: "out"})
		if err != nil {
			t.Fatal(err)
		}
		out["replicated"], out["failed"] = res.Replicated, res.Failed
		if len(res.Failures) > 0 {
			out["first_failure"] = res.Failures[0].Err.Error()
		}
	}
	out["elapsed_ms"] = float64(time.Since(start).Microseconds()) / 1000
	out["rss_peak_kib"] = fb02ProcKiB("VmHWM")
	out["snapshot_decodes"] = decodes.Load()
	line, _ := json.Marshal(out)
	fmt.Printf("FB02PROFILE %s\n", bytes.TrimSpace(line))
}
