package main

// B3 scale tooling: a synthetic journal-only fixture and a fresh-process probe.
// Neither runs without its environment variable, and both compile against the
// pre-B3 tree so old and new builds measure the same store.
//
//	ZEROS3_B3_BUILD=DIR [ZEROS3_B3_N=1000000]  go test -run '^TestB3BuildFixture$' .
//	ZEROS3_B3_APPEND=DIR ZEROS3_B3_SUFFIX=20000 go test -run '^TestB3AppendFixture$' .
//	ZEROS3_B3_PROBE=DIR ZEROS3_B3_MODE=journal|checkpoint go test -run '^TestB3Probe$' -v .

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func b3EnvInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

type b3Key struct {
	key  string
	live *journalArchivedVersionPayload // current state, for archiving
}

func b3KeyName(i int) string { return fmt.Sprintf("data/%03d/%08d.bin", i%997, i) }

// b3Ops appends puts of keys [from, from+n), then deletes about 30% of them and
// overwrites about 2%, all as acknowledged journal frames.
func b3Ops(t *testing.T, s *Store, from, n int, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, 11))
	state := make([]*journalArchivedVersionPayload, n)
	mk := func(i int, reason string) *journalArchivedVersionPayload {
		var sum [32]byte
		for j := range sum {
			sum[j] = byte(rng.Uint32())
		}
		etag := md5.Sum(sum[:])
		return &journalArchivedVersionPayload{
			VersionID: newUUIDv7(), ManifestUUID: newUUIDv7(), ManifestSHA256: hex.EncodeToString(sum[:]),
			Size: 4096, ETag: hex.EncodeToString(etag[:]), ContentType: "application/octet-stream",
			ArchivedAt: time.Now().UTC(), Reason: reason,
		}
	}
	var recs []journalFrameIn
	flush := func() {
		if len(recs) > 0 {
			if _, err := s.journal.appendFrames(recs); err != nil {
				t.Fatal(err)
			}
			recs = recs[:0]
		}
	}
	add := func(recType uint8, v any) {
		b, err := json.Marshal(v)
		mustOK(t, err)
		recs = append(recs, journalFrameIn{recType: recType, payload: b})
		if len(recs) == 2000 {
			flush()
		}
	}
	put := func(i int, prev *journalArchivedVersionPayload) {
		cur := mk(i, historyReasonOverwritten)
		var archived *journalArchivedVersionPayload
		if prev != nil {
			p := *prev
			p.ArchivedAt, p.Reason, p.VersionID = time.Now().UTC(), historyReasonOverwritten, newUUIDv7()
			archived = &p
		}
		add(recordTypePutObjectRootV2, journalPutPayloadV2{Bucket: "b", Key: b3KeyName(from + i), ManifestUUID: cur.ManifestUUID,
			ManifestSHA256: cur.ManifestSHA256, Size: cur.Size, ETag: cur.ETag, ContentType: cur.ContentType, VersionID: newUUIDv7(), Previous: archived})
		state[i] = cur
	}
	for i := range n {
		put(i, nil)
	}
	for i := range n {
		switch r := rng.IntN(100); {
		case r < 30:
			p := *state[i]
			p.ArchivedAt, p.Reason, p.VersionID = time.Now().UTC(), historyReasonDeleted, newUUIDv7()
			add(recordTypeDeleteObjectRootV2, journalDeleteObjectPayloadV2{Bucket: "b", Key: b3KeyName(from + i), Archived: p})
			state[i] = nil
		case r < 32:
			put(i, state[i])
		}
	}
	flush()
}

func TestB3BuildFixture(t *testing.T) {
	dir := os.Getenv("ZEROS3_B3_BUILD")
	if dir == "" {
		t.Skip("set ZEROS3_B3_BUILD")
	}
	s, err := OpenStore(dir)
	mustOK(t, err)
	defer s.Close()
	mustOK(t, s.CreateBucket("b"))
	start := time.Now()
	b3Ops(t, s, 0, b3EnvInt("ZEROS3_B3_N", 1000000), 1)
	t.Logf("journal %d bytes, %d records in %v", s.journal.writeOffset, s.journal.nextSeq-1, time.Since(start))
}

func TestB3AppendFixture(t *testing.T) {
	dir := os.Getenv("ZEROS3_B3_APPEND")
	if dir == "" {
		t.Skip("set ZEROS3_B3_APPEND")
	}
	s, err := openStore(dir, true, true)
	mustOK(t, err)
	defer s.Close()
	b3Ops(t, s, 1<<24, b3EnvInt("ZEROS3_B3_SUFFIX", 20000), 2)
}

func b3Percentiles(d []time.Duration) map[string]float64 {
	slices.Sort(d)
	at := func(p float64) float64 { return float64(d[min(len(d)-1, int(float64(len(d))*p))].Nanoseconds()) / 1000 }
	return map[string]float64{"p50_us": at(.5), "p95_us": at(.95), "p99_us": at(.99)}
}

func b3RSSKiB() int64 {
	data, _ := os.ReadFile("/proc/self/status")
	for _, line := range bytes.Split(data, []byte("\n")) {
		var v int64
		if _, err := fmt.Sscanf(string(line), "VmRSS: %d kB", &v); err == nil {
			return v
		}
	}
	return 0
}

func TestB3Probe(t *testing.T) {
	dir := os.Getenv("ZEROS3_B3_PROBE")
	if dir == "" {
		t.Skip("set ZEROS3_B3_PROBE")
	}
	use := os.Getenv("ZEROS3_B3_MODE") == "checkpoint"
	out := map[string]any{"mode": os.Getenv("ZEROS3_B3_MODE")}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	s, err := openStore(dir, false, use)
	mustOK(t, err)
	out["open_ms"] = float64(time.Since(start).Microseconds()) / 1000
	runtime.ReadMemStats(&after)
	out["open_alloc_bytes"] = after.TotalAlloc - before.TotalAlloc
	out["open_mallocs"] = after.Mallocs - before.Mallocs
	runtime.GC()
	runtime.ReadMemStats(&after)
	out["live_heap_bytes"], out["live_heap_objects"] = after.HeapAlloc, after.HeapObjects
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	out["peak_rss_kib_after_open"], out["rss_kib_after_open"] = ru.Maxrss, b3RSSKiB()
	if use {
		out["checkpoint_bytes"] = fileSize(filepath.Join(dir, "journal", "namespace-v4.zck"), filepath.Join(dir, "journal", "namespace-v3.zck"))
	}
	out["journal_bytes"] = fileSize(filepath.Join(dir, "journal", "visibility.log"))

	rng := rand.New(rand.NewPCG(5, 6))
	n := b3EnvInt("ZEROS3_B3_N", 1000000)
	var hit, miss []time.Duration
	found := 0
	for range 20000 {
		k := b3KeyName(rng.IntN(n))
		t0 := time.Now()
		_, err := s.lookupObject("b", k)
		hit = append(hit, time.Since(t0))
		if err == nil {
			found++
		}
	}
	for range 5000 {
		t0 := time.Now()
		_, err := s.lookupObject("b", fmt.Sprintf("absent/%d", rng.IntN(1<<30)))
		miss = append(miss, time.Since(t0))
		if err == nil {
			t.Fatal("absent key found")
		}
	}
	out["lookup_hit"], out["lookup_miss"], out["lookup_found"] = b3Percentiles(hit), b3Percentiles(miss), found

	var pages []time.Duration
	keys := 0
	for range 200 {
		t0 := time.Now()
		p, err := s.ListObjectsV2("b", fmt.Sprintf("data/%03d/", rng.IntN(997)), "/", "", 1000)
		mustOK(t, err)
		pages = append(pages, time.Since(t0))
		keys += len(p.contents)
	}
	out["list_prefix_page"], out["list_prefix_keys_per_page"] = b3Percentiles(pages), keys/200
	t0 := time.Now()
	p, err := s.ListObjectsV2("b", "", "/", "", 1000)
	mustOK(t, err)
	out["list_root_delimiter_ms"], out["list_root_prefixes"] = float64(time.Since(t0).Microseconds())/1000, len(p.commonPrefixes)
	var walk []time.Duration
	after2 := ""
	for range 50 {
		t0 := time.Now()
		p, err := s.ListObjectsV2("b", "", "", after2, 1000)
		mustOK(t, err)
		walk = append(walk, time.Since(t0))
		after2 = p.lastConsumedKey
	}
	out["list_walk_page"] = b3Percentiles(walk)

	var vers []time.Duration
	withHistory := 0
	for range 5000 {
		t0 := time.Now()
		vs, _, err := s.ListVersions("b", b3KeyName(rng.IntN(n)))
		mustOK(t, err)
		vers = append(vers, time.Since(t0))
		if len(vs) > 0 {
			withHistory++
		}
	}
	out["versions"], out["versions_with_history"] = b3Percentiles(vers), withHistory

	if os.Getenv("ZEROS3_B3_MUTATE") != "" {
		s.Close()
		s, err = openStore(dir, true, use)
		mustOK(t, err)
		var muts []time.Duration
		for i := range 1000 {
			k := b3KeyName(rng.IntN(n))
			t0 := time.Now()
			if i%2 == 0 {
				_, err = s.PutObject("b", k, []byte(fmt.Sprint("m", i)), "text/plain", nil)
			} else {
				err = s.DeleteObject("b", k)
			}
			mustOK(t, err)
			muts = append(muts, time.Since(t0))
		}
		out["mutation"] = b3Percentiles(muts)
	}
	runtime.ReadMemStats(&after)
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	out["peak_rss_kib_final"], out["rss_kib_final"] = ru.Maxrss, b3RSSKiB()
	s.Close()
	line, _ := json.Marshal(out)
	fmt.Printf("B3RESULT %s\n", line)
	_ = sha256.Size
}

func fileSize(paths ...string) int64 {
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			return st.Size()
		}
	}
	return 0
}
