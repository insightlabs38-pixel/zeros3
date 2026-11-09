package main

import (
	"bytes"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Z4-P2 end-to-end read-path tests: exact bytes over HTTP on every storage
// layout, the cached-manifest and cached-chunk integrity contract, pipelined
// streaming faults, and the P1C segment-sealing interplay.

type p2Env struct {
	t      *testing.T
	dir    string
	s      *Store
	srv    *Server
	ts     *httptest.Server
	signer testSigner
	client *http.Client
}

func p2Open(t *testing.T, dir string) *p2Env {
	t.Helper()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	srv, signer := newTestServerAndSignerFor(t, s)
	ts := httptest.NewServer(srv)
	e := &p2Env{t: t, dir: dir, s: s, srv: srv, ts: ts, signer: signer, client: ts.Client()}
	t.Cleanup(func() { ts.Close(); s.Close() })
	return e
}

func p2New(t *testing.T) *p2Env { return p2Open(t, t.TempDir()) }

// get returns the status, headers and body of a signed request to key.
func (e *p2Env) do(method, key string, headers map[string]string) (int, http.Header, []byte) {
	e.t.Helper()
	resp := doSignedRequest(e.t, e.client, e.ts.URL, e.signer, method, "/b/"+key, nil, headers)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil && resp.StatusCode/100 == 2 && method != http.MethodHead {
		return resp.StatusCode, resp.Header, b // a truncated body is reported by length, not here
	}
	return resp.StatusCode, resp.Header, b
}

func (e *p2Env) rangeGet(key string, start, end int64) (int, http.Header, []byte) {
	return e.do(http.MethodGet, key, map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", start, end)})
}

// p2Layouts builds one fixture per physical layout holding the same bodies.
type p2Layout struct {
	name string
	// setup prepares policy before the store is opened and returns a finisher
	// run after the objects are stored.
	setup func(t *testing.T) func(t *testing.T, e *p2Env)
}

func p2Layouts() []p2Layout {
	return []p2Layout{
		{"loose", func(t *testing.T) func(*testing.T, *p2Env) {
			useLooseLayout(t, "P2 differential: loose CAS chunks")
			return nil
		}},
		{"segments-and-packs", func(t *testing.T) func(*testing.T, *p2Env) {
			useProductionLayout(t)
			return nil
		}},
		{"sealed", func(t *testing.T) func(*testing.T, *p2Env) {
			useProductionLayout(t)
			return func(t *testing.T, e *p2Env) { e.s.sealSegments() }
		}},
		{"compressed-pack", func(t *testing.T) func(*testing.T, *p2Env) {
			useLooseLayout(t, "P2 differential: loose chunks converted to a compressed pack by compact")
			return func(t *testing.T, e *p2Env) {
				if _, err := e.s.compact(nil, compressTestOneBigPack); err != nil {
					t.Fatal(err)
				}
				if err := e.s.reloadPacks(); err != nil {
					t.Fatal(err)
				}
			}
		}},
	}
}

func p2Bodies() map[string][]byte {
	mixed := append(append(corpusEnglish(5, 700_000), genRandomBytes(6, 500_000)...), corpusJSON(7, 600_000)...)
	return map[string][]byte{
		"empty": {},
		"one":   {42},
		"4k":    genRandomBytes(1, 4096),
		"100k":  genRandomBytes(2, 100_000),
		"mixed": mixed,
		"big":   genRandomBytes(3, 3_000_000),
	}
}

// p2ChunkEdges returns the logical offsets at which chunks start/end.
func p2ChunkEdges(t *testing.T, s *Store, key string) []int64 {
	t.Helper()
	_, man, err := s.HeadObject("b", key)
	if err != nil {
		t.Fatal(err)
	}
	var edges []int64
	var off int64
	for _, c := range man.Chunks {
		off += c.Length
		edges = append(edges, off)
	}
	return edges
}

func TestP2_RangeReadsMatchReferenceOnEveryLayout(t *testing.T) {
	oldMin := pipeMinRange
	for _, lay := range p2Layouts() {
		for _, mode := range []string{"serial", "pipelined", "nocache"} {
			t.Run(lay.name+"/"+mode, func(t *testing.T) {
				fin := lay.setup(t)
				switch mode {
				case "pipelined":
					pipeMinRange = 64 << 10
				case "nocache":
					t.Setenv("ZEROS3_MANIFEST_CACHE_MIB", "0")
					t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "0")
				}
				t.Cleanup(func() { pipeMinRange = oldMin })
				e := p2New(t)
				bodies := p2Bodies()
				for k, b := range bodies {
					if _, err := e.s.PutObject("b", k, b, "application/x-test", map[string]string{"k": "v"}); err != nil {
						t.Fatal(err)
					}
				}
				if fin != nil {
					fin(t, e)
				}
				rng := rand.New(rand.NewPCG(11, 12))
				for k, body := range bodies {
					size := int64(len(body))
					// whole object, twice (second pass can come from the caches)
					for pass := 0; pass < 2; pass++ {
						st, h, got := e.do(http.MethodGet, k, nil)
						if st != 200 || !bytes.Equal(got, body) || h.Get("Content-Length") != fmt.Sprint(size) {
							t.Fatalf("%s whole GET pass %d: status %d, %d bytes (want %d)", k, pass, st, len(got), size)
						}
					}
					if size == 0 {
						if st, h, _ := e.rangeGet(k, 0, 0); st != 416 || h.Get("Content-Range") != "bytes */0" {
							t.Fatalf("empty object range: status %d %q", st, h.Get("Content-Range"))
						}
						continue
					}
					edges := p2ChunkEdges(t, e.s, k)
					var starts []int64
					starts = append(starts, 0, size-1, size/2)
					for _, ed := range edges {
						for _, d := range []int64{-1, 0, 1} {
							if v := ed + d; v >= 0 && v < size {
								starts = append(starts, v)
							}
						}
					}
					for i := 0; i < 40; i++ {
						starts = append(starts, rng.Int64N(size))
					}
					lens := []int64{1, 2, 4096, 65536, 70000, 1 << 20, size}
					n := 0
					for _, a := range starts {
						for _, l := range lens {
							if n++; n%3 != 0 && l > 4096 { // thin the large ones
								continue
							}
							b := min(a+l-1, size-1)
							for pass := 0; pass < 3; pass++ { // miss, doorkeeper sighting, cache hit
								st, h, got := e.rangeGet(k, a, b)
								if st != 206 || !bytes.Equal(got, body[a:b+1]) ||
									h.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", a, b, size) ||
									h.Get("Content-Length") != fmt.Sprint(b-a+1) {
									t.Fatalf("%s bytes=%d-%d pass %d: status %d len %d cr %q", k, a, b, pass, st, len(got), h.Get("Content-Range"))
								}
							}
						}
					}
					// open-ended, suffix, past-the-end clamp, unsatisfiable, malformed
					cases := []struct {
						hdr      string
						status   int
						from, to int64
					}{
						{fmt.Sprintf("bytes=%d-", size-1), 206, size - 1, size - 1},
						{"bytes=-1", 206, size - 1, size - 1},
						{fmt.Sprintf("bytes=-%d", size+100), 206, 0, size - 1},
						{fmt.Sprintf("bytes=0-%d", size+1000), 206, 0, size - 1},
						{fmt.Sprintf("bytes=%d-", size), 416, 0, 0},
						{fmt.Sprintf("bytes=%d-%d", size+5, size+9), 416, 0, 0},
						{"bytes=-0", 416, 0, 0},
						{"bytes=5-2", map[bool]int{true: 200, false: 416}[size > 5], 0, size - 1},
						{"bytes=abc", 200, 0, size - 1},
						{"bytes=0-1,3-4", 200, 0, size - 1},
					}
					for _, c := range cases {
						st, _, got := e.do(http.MethodGet, k, map[string]string{"Range": c.hdr})
						if st != c.status {
							t.Fatalf("%s %q: status %d, want %d", k, c.hdr, st, c.status)
						}
						if st != 416 && !bytes.Equal(got, body[c.from:c.to+1]) {
							t.Fatalf("%s %q: wrong bytes", k, c.hdr)
						}
					}
					// HEAD agrees with GET
					st, h, hb := e.do(http.MethodHead, k, nil)
					if st != 200 || len(hb) != 0 || h.Get("Content-Length") != fmt.Sprint(size) || h.Get("X-Amz-Meta-K") != "v" || h.Get("Content-Type") != "application/x-test" {
						t.Fatalf("%s HEAD: %d %v", k, st, h)
					}
				}
				if mode == "serial" || mode == "pipelined" {
					if st := e.s.mcache.stats(); st.Hits == 0 {
						t.Fatalf("manifest cache never hit: %+v", st)
					}
				}
				if mode == "pipelined" && e.s.pipeStreams.Load() != 0 {
					t.Fatalf("%d pipelined streams leaked", e.s.pipeStreams.Load())
				}
			})
		}
	}
}

// Ranges beyond 32-bit offsets are exercised against the index directly: a
// 6 GiB object cannot be stored in a unit test, but its served form can be
// built and searched.
func TestP2_IndexHandlesOffsetsBeyond32Bits(t *testing.T) {
	rng := rand.New(rand.NewPCG(21, 22))
	const n = 100_000
	m := p2Manifest(rng, n, func(int) int64 { return 70_000 + int64(rng.IntN(100_000)) })
	sm, err := newServedManifest(m, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if m.TotalLength < 4<<30 {
		t.Fatalf("fixture is only %d bytes", m.TotalLength)
	}
	for i := 0; i < 5000; i++ {
		off := rng.Int64N(m.TotalLength)
		c := sm.locate(off)
		if c >= n || sm.start(c) > off || off >= sm.start(c)+sm.length(c) {
			t.Fatalf("offset %d located chunk %d [%d,%d)", off, c, sm.start(c), sm.start(c)+sm.length(c))
		}
	}
	if sm.locate(m.TotalLength) != n || sm.locate(m.TotalLength-1) != n-1 {
		t.Fatal("end of object not located")
	}
}

func TestP2_ConditionalAndVersionSemanticsWithWarmCache(t *testing.T) {
	useProductionLayout(t)
	e := p2New(t)
	v1, v2 := genRandomBytes(31, 300_000), genRandomBytes(32, 300_001)
	e1, _ := e.s.PutObject("b", "k", v1, "text/one", map[string]string{"gen": "1"})
	e.do(http.MethodGet, "k", nil) // warm
	// 412 is decided by the namespace entry alone: no manifest is touched
	loads := e.s.mcache.stats().Loads
	if st, _, _ := e.do(http.MethodGet, "k", map[string]string{"If-Match": `"nope"`}); st != 412 {
		t.Fatalf("If-Match mismatch: %d", st)
	}
	if st, _, _ := e.do(http.MethodHead, "k", map[string]string{"If-Match": `"nope"`}); st != 412 {
		t.Fatalf("HEAD If-Match mismatch: %d", st)
	}
	if e.s.mcache.stats().Loads != loads {
		t.Fatal("a failed precondition loaded a manifest")
	}
	st, h, b := e.do(http.MethodGet, "k", map[string]string{"If-None-Match": `"` + e1.etag + `"`})
	if st != 304 || len(b) != 0 || h.Get("X-Amz-Meta-Gen") != "1" || h.Get("Last-Modified") == "" {
		t.Fatalf("304: %d %v", st, h)
	}
	// overwrite: the next read is v2 with v2's headers, never v1's cached ones
	e2, _ := e.s.PutObject("b", "k", v2, "text/two", map[string]string{"gen": "2"})
	st, h, b = e.do(http.MethodGet, "k", nil)
	if st != 200 || !bytes.Equal(b, v2) || h.Get("ETag") != `"`+e2.etag+`"` || h.Get("Content-Type") != "text/two" || h.Get("X-Amz-Meta-Gen") != "2" {
		t.Fatalf("after overwrite: %d %v", st, h)
	}
	if st, _, _ := e.do(http.MethodGet, "k", map[string]string{"If-Match": `"` + e1.etag + `"`}); st != 412 {
		t.Fatalf("old ETag still matches: %d", st)
	}
	// restore the first version: its (cached) manifest serves v1 again
	vers, _, _ := e.s.ListVersions("b", "k")
	if len(vers) == 0 {
		t.Fatal("no history")
	}
	if _, _, err := e.s.RestoreObjectVersion("b", "k", vers[0].versionID); err != nil {
		t.Fatal(err)
	}
	st, h, b = e.do(http.MethodGet, "k", nil)
	if st != 200 || !bytes.Equal(b, v1) || h.Get("X-Amz-Meta-Gen") != "1" {
		t.Fatalf("after restore: %d %v", st, h)
	}
	// delete: a cached manifest does not keep the object visible
	if err := e.s.DeleteObject("b", "k"); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := e.do(http.MethodGet, "k", nil); st != 404 {
		t.Fatalf("deleted object: %d", st)
	}
	if st, _, _ := e.do(http.MethodHead, "k", nil); st != 404 {
		t.Fatalf("deleted object HEAD: %d", st)
	}
	// recreate with identical content: a new version with its own manifest
	e3, _ := e.s.PutObject("b", "k", v1, "text/one", map[string]string{"gen": "1"})
	if st, h, b = e.do(http.MethodGet, "k", nil); st != 200 || !bytes.Equal(b, v1) || h.Get("ETag") != `"`+e3.etag+`"` {
		t.Fatalf("recreated: %d", st)
	}
}

func TestP2_ManifestCacheIntegrityContract(t *testing.T) {
	for _, big := range []bool{false, true} { // false = inline segment record; true = manifests/<id>.json
		t.Run(fmt.Sprintf("manifestFile=%v", big), func(t *testing.T) {
			if !big {
				useProductionLayout(t)
			}
			if big { // loose layout (and any manifest above the inline limit) keeps manifests/<id>.json
				useLooseLayout(t, "manifest as a file under manifests/")
			}
			e := p2New(t)
			body := genRandomBytes(41, 200_000)
			if _, err := e.s.PutObject("b", "k", body, "", nil); err != nil {
				t.Fatal(err)
			}
			entry, _ := e.s.lookupObject("b", "k")
			if _, err := os.Stat(e.dir + "/manifests/" + entry.manifestUUID + ".json"); (err == nil) != big {
				t.Fatalf("manifest file present=%v, want %v", err == nil, big)
			}
			if st, _, got := e.do(http.MethodGet, "k", nil); st != 200 || !bytes.Equal(got, body) {
				t.Fatalf("warm-up GET: %d", st)
			}
			// damage the manifest after it is cached: serving continues from the
			// verified copy and every byte is still chunk-verified...
			tamperManifest(t, e.s, entry.manifestUUID, []byte("{garbage"))
			if st, _, got := e.do(http.MethodGet, "k", nil); st != 200 || !bytes.Equal(got, body) {
				t.Fatalf("cached manifest should keep serving: %d", st)
			}
			if st, _, _ := e.do(http.MethodHead, "k", nil); st != 200 {
				t.Fatalf("HEAD from cached manifest: %d", st)
			}
			// ... but diagnostics read the real bytes, see the damage, and drop the cache
			v, err := e.s.Verify(false)
			if err != nil {
				t.Fatal(err)
			}
			if v.OK() {
				t.Fatal("verify did not report a damaged manifest while the cache was warm")
			}
			if st, _, _ := e.do(http.MethodGet, "k", nil); st != 500 {
				t.Fatalf("after verify the damaged manifest must fail closed, got %d", st)
			}
			if st, _, _ := e.do(http.MethodHead, "k", nil); st != 500 {
				t.Fatalf("HEAD: %d", st)
			}
			if c := e.s.mcache.stats(); c.Entries != 0 || c.LoadErrors == 0 {
				t.Fatalf("a failed load must not be cached: %+v", c)
			}
		})
	}
}

func TestP2_DamagedOrMissingManifestBeforeFirstLoad(t *testing.T) {
	useProductionLayout(t)
	for _, how := range []string{"corrupt", "missing"} {
		t.Run(how, func(t *testing.T) {
			dir := t.TempDir()
			e := p2Open(t, dir)
			if _, err := e.s.PutObject("b", "k", genRandomBytes(42, 150_000), "", nil); err != nil {
				t.Fatal(err)
			}
			entry, _ := e.s.lookupObject("b", "k")
			if how == "corrupt" {
				tamperManifest(t, e.s, entry.manifestUUID, []byte("{garbage"))
			} else {
				deleteManifest(t, e.s, entry.manifestUUID)
			}
			// a fresh process (cold caches) must fail closed, not serve
			e.ts.Close()
			e.s.Close()
			e2 := p2Open(t, dir)
			for _, m := range []string{http.MethodGet, http.MethodHead} {
				if st, _, _ := e2.do(m, "k", nil); st != 500 {
					t.Fatalf("%s %s manifest: %d", how, m, st)
				}
			}
			if _, err := e2.s.Verify(true); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestP2_ConcurrentFirstLoadsShareOneRead(t *testing.T) {
	useProductionLayout(t)
	dir := t.TempDir()
	e := p2Open(t, dir)
	body := genRandomBytes(43, 400_000)
	e.s.PutObject("b", "k", body, "", nil)
	e.ts.Close()
	e.s.Close()
	e = p2Open(t, dir) // cold
	var wg sync.WaitGroup
	var bad atomic.Int64
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if st, _, got := e.do(http.MethodGet, "k", nil); st != 200 || !bytes.Equal(got, body) {
				bad.Add(1)
			}
		}()
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d bad concurrent first reads", bad.Load())
	}
	if st := e.s.mcache.stats(); st.Loads != 1 {
		t.Fatalf("%d manifest loads for one object: %+v", st.Loads, st)
	}
}

func TestP2_ChunkCacheNeverMasksCorruptionFromDiagnostics(t *testing.T) {
	for _, lay := range p2Layouts()[:3] {
		t.Run(lay.name, func(t *testing.T) {
			fin := lay.setup(t)
			e := p2New(t)
			body := genRandomBytes(51, 600_000)
			e.s.PutObject("b", "k", body, "", nil)
			if fin != nil {
				fin(t, e)
			}
			edges := p2ChunkEdges(t, e.s, "k")
			if len(edges) < 3 {
				t.Fatalf("fixture has %d chunks", len(edges))
			}
			a, b := edges[0]+10, edges[0]+4106 // 4 KiB inside the second chunk
			for pass := 0; pass < 4; pass++ {  // sighting, then admission, then hits
				if st, _, got := e.rangeGet("k", a, b); st != 206 || !bytes.Equal(got, body[a:b+1]) {
					t.Fatalf("warm pass %d: %d", pass, st)
				}
			}
			if st := e.s.ccache.stats(); st.Admits == 0 || st.Hits == 0 {
				t.Fatalf("chunk was not cached: %+v", st)
			}
			_, man, _ := e.s.HeadObject("b", "k")
			sum := chunkSumHex(t, man.Chunks[1].SHA256)
			corruptChunkAnyLayout(t, e.s, sum)
			// served from the verified in-memory copy; never wrong bytes
			for pass := 0; pass < 2; pass++ {
				if st, _, got := e.rangeGet("k", a, b); st != 206 || !bytes.Equal(got, body[a:b+1]) {
					t.Fatalf("cached read after damage: %d", st)
				}
			}
			// the diagnostic reads the real record, finds it, and drops the caches
			if v, err := e.s.Verify(true); err != nil || v.OK() {
				t.Fatalf("deep verify must see the damage a warm chunk cache covers: ok=%v err=%v", v.OK(), err)
			}
			if st, _, _ := e.rangeGet("k", a, b); st == 206 {
				t.Fatal("served a damaged chunk after verify purged the cache")
			}
			// the damaged candidate was rejected before it could be cached
			for pass := 0; pass < 4; pass++ {
				e.rangeGet("k", a, b)
			}
			if st := e.s.ccache.stats(); st.Entries != 0 {
				t.Fatalf("a chunk that failed verification entered the cache: %+v", st)
			}
			// missing (not just corrupt) data behaves the same
			removeChunkAnyLayout(t, e.s, sum)
			if st, _, _ := e.rangeGet("k", a, b); st == 206 {
				t.Fatal("served a missing chunk")
			}
		})
	}
}

func TestP2_ChunkCacheBoundedAndScanResistant(t *testing.T) {
	c := newChunkCache(16 * 4096) // 4 KiB per shard
	data := bytes.Repeat([]byte{7}, 1000)
	hot := [32]byte{1}
	c.offer(&hot, data, 0)
	c.offer(&hot, data, 0) // admitted on the second sighting
	if _, ok := c.get(&hot); !ok {
		t.Fatal("repeated key not admitted")
	}
	for i := 0; i < 5000; i++ { // one-touch scan: nothing is copied or evicted
		k := [32]byte{1, byte(i), byte(i >> 8), 9}
		c.offer(&k, data, 0)
	}
	if _, ok := c.get(&hot); !ok {
		t.Fatal("a one-touch scan evicted a hot chunk")
	}
	if st := c.stats(); st.Bytes > st.Budget {
		t.Fatalf("over budget: %+v", st)
	}
	for i := 0; i < 5000; i++ { // twice-seen keys do enter, within budget
		k := [32]byte{2, byte(i), byte(i >> 8), 9}
		c.offer(&k, data, 0)
		c.offer(&k, data, 0)
		if st := c.stats(); st.Bytes > st.Budget {
			t.Fatalf("over budget: %+v", st)
		}
	}
	// a purge fences an in-flight admission
	c.purge()
	k := [32]byte{3}
	c.offer(&k, data, 0)
	c.offer(&k, data, 0) // epoch 0 is stale after the purge
	if _, ok := c.get(&k); ok || c.stats().StaleDrop == 0 {
		t.Fatalf("stale epoch inserted: %+v", c.stats())
	}
}

func TestP2_PipelinedStreamsFailSafeAndLeakNothing(t *testing.T) {
	oldMin, oldW := pipeMinRange, pipeWorkers
	pipeMinRange, pipeWorkers = 64<<10, 3
	t.Cleanup(func() { pipeMinRange, pipeWorkers = oldMin, oldW })
	for _, lay := range p2Layouts()[:3] {
		t.Run(lay.name, func(t *testing.T) {
			fin := lay.setup(t)
			e := p2New(t)
			body := genRandomBytes(61, 6_000_000)
			e.s.PutObject("b", "k", body, "", nil)
			if fin != nil {
				fin(t, e)
			}
			goroutines := runtime.NumGoroutine()
			// 1. a corrupt chunk in the middle: the response is cut short, never wrong
			_, man, _ := e.s.HeadObject("b", "k")
			mid := len(man.Chunks) / 2
			if mid < 2 {
				t.Fatalf("only %d chunks", len(man.Chunks))
			}
			sum := chunkSumHex(t, man.Chunks[mid].SHA256)
			var before int64
			for _, c := range man.Chunks[:mid] {
				before += c.Length
			}
			corruptChunkAnyLayout(t, e.s, sum)
			resp := doSignedRequest(t, e.client, e.ts.URL, e.signer, http.MethodGet, "/b/k", nil, nil)
			got, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil && int64(len(got)) == int64(len(body)) {
				t.Fatalf("a damaged object was delivered whole")
			}
			if !bytes.Equal(got, body[:len(got)]) {
				t.Fatal("bytes before the failure differ from the object")
			}
			if int64(len(got)) > before {
				t.Fatalf("delivered %d bytes past the damaged chunk at %d", len(got), before)
			}
			// 2. a client that walks away mid-stream
			e2 := p2New(t)
			e2.s.PutObject("b", "k", body, "", nil)
			for i := 0; i < 8; i++ {
				resp := doSignedRequest(t, e2.client, e2.ts.URL, e2.signer, http.MethodGet, "/b/k", nil, nil)
				io.CopyN(io.Discard, resp.Body, 100_000)
				resp.Body.Close()
			}
			// 3. concurrent full streams beyond the stream limit stay exact
			var wg sync.WaitGroup
			var bad atomic.Int64
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if st, _, g := e2.do(http.MethodGet, "k", nil); st != 200 || !bytes.Equal(g, body) {
						bad.Add(1)
					}
				}()
			}
			wg.Wait()
			if bad.Load() != 0 {
				t.Fatalf("%d concurrent pipelined reads were wrong", bad.Load())
			}
			deadline := time.Now().Add(5 * time.Second)
			for (e.s.pipeStreams.Load() != 0 || e2.s.pipeStreams.Load() != 0 || runtime.NumGoroutine() > goroutines+40) && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if e.s.pipeStreams.Load() != 0 || e2.s.pipeStreams.Load() != 0 {
				t.Fatalf("pipelined stream slots leaked: %d %d", e.s.pipeStreams.Load(), e2.s.pipeStreams.Load())
			}
			if n := runtime.NumGoroutine(); n > goroutines+40 {
				t.Fatalf("goroutines grew from %d to %d", goroutines, n)
			}
		})
	}
}

// A reader that is created and dropped without reading, or read once and
// closed, must return its stream slot and stop its workers.
func TestP2_PipeReaderCloseSemantics(t *testing.T) {
	oldMin := pipeMinRange
	pipeMinRange = 1
	t.Cleanup(func() { pipeMinRange = oldMin })
	useProductionLayout(t)
	e := p2New(t)
	body := genRandomBytes(71, 5_000_000)
	e.s.PutObject("b", "k", body, "", nil)
	entry, sm, err := e.s.headServed("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		rd := e.s.newStreamReader(sm, byteRange{0, entry.size - 1})
		if _, ok := rd.(*pipeReader); !ok {
			t.Skip("not enough CPUs for pipelined reads")
		}
		if i%2 == 1 {
			if d, err := rd.next(); err != nil || len(d) == 0 {
				t.Fatalf("first chunk: %v", err)
			}
		}
		rd.close()
		rd.close() // idempotent
	}
	if n := e.s.pipeStreams.Load(); n != 0 {
		t.Fatalf("%d stream slots leaked", n)
	}
	// reading to the end returns EOF repeatedly and releases the slot
	rd := e.s.newStreamReader(sm, byteRange{10, entry.size - 11})
	var got []byte
	for {
		d, err := rd.next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, d...)
	}
	if !bytes.Equal(got, body[10:entry.size-10]) {
		t.Fatal("pipelined range differs")
	}
	if _, err := rd.next(); err != io.EOF {
		t.Fatalf("second EOF: %v", err)
	}
	rd.close()
	if e.s.pipeStreams.Load() != 0 {
		t.Fatal("slot not returned")
	}
}

func TestP2_CachedManifestSurvivesSegmentSealAndRepack(t *testing.T) {
	useProductionLayout(t)
	setSegTarget(t, 64<<10, 16<<10)
	e := p2New(t)
	acked := p2PutMany(t, e, "a", 30, 9000)
	for k := range acked { // warm: manifests are inline records in the open segment
		e.do(http.MethodGet, k, nil)
	}
	if segFiles(t, e.dir) == nil {
		t.Skip("no open segment")
	}
	check := func(stage string) {
		t.Helper()
		for k, body := range acked {
			if st, _, got := e.do(http.MethodGet, k, nil); st != 200 || !bytes.Equal(got, body) {
				t.Fatalf("%s: %s -> %d", stage, k, st)
			}
			if st, _, _ := e.do(http.MethodHead, k, nil); st != 200 {
				t.Fatalf("%s: HEAD %s -> %d", stage, k, st)
			}
		}
	}
	check("warm, segment open")
	loads := e.s.mcache.stats().Loads
	e.s.sealSegments() // the physical location of every manifest and chunk changes
	check("after seal")
	if e.s.mcache.stats().Loads != loads {
		t.Fatalf("sealing a segment forced manifest reloads: %d -> %d", loads, e.s.mcache.stats().Loads)
	}
	if err := e.s.reloadPacks(); err != nil {
		t.Fatal(err)
	}
	check("after locator rebuild")
	// cold manifest cache reading sealed inline manifests
	e.s.mcache.purge()
	check("cold cache, sealed packs")
}

func p2PutMany(t *testing.T, e *p2Env, prefix string, n, size int) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("%s%03d", prefix, i)
		body := genRandomBytes(int64(9000+i+len(prefix)*131), size)
		if _, err := e.s.PutObject("b", k, body, "", nil); err != nil {
			t.Fatal(err)
		}
		out[k] = body
	}
	return out
}

func TestP2_ReadsDuringWritesSealsAndOverwrites(t *testing.T) {
	useProductionLayout(t)
	setSegTarget(t, 64<<10, 16<<10)
	e := p2New(t)
	stable := p2PutMany(t, e, "s", 20, 7000)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad atomic.Int64
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(r), 99))
			for {
				select {
				case <-stop:
					return
				default:
				}
				for k, body := range stable {
					a := rng.Int64N(int64(len(body)))
					b := min(a+int64(rng.IntN(4000)), int64(len(body))-1)
					if st, _, got := e.rangeGet(k, a, b); st != 206 || !bytes.Equal(got, body[a:b+1]) {
						bad.Add(1)
					}
				}
				// the churning key is always one of the two complete versions
				if st, _, got := e.do(http.MethodGet, "hot", nil); st == 200 {
					if !bytes.Equal(got, genRandomBytes(1, 20000)) && !bytes.Equal(got, genRandomBytes(2, 20001)) {
						bad.Add(1)
					}
				} else if st != 404 {
					bad.Add(1)
				}
			}
		}(r)
	}
	for i := 0; i < 8; i++ {
		e.s.PutObject("b", "hot", genRandomBytes(1+int64(i%2), 20000+i%2), "", nil)
		p2PutMany(t, e, fmt.Sprintf("w%d_", i), 25, 7000)
		e.s.sealSegments()
		if i%3 == 2 {
			e.s.DeleteObject("b", "hot")
		}
	}
	close(stop)
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d wrong or failed reads while writing/sealing/overwriting", bad.Load())
	}
	if v, err := e.s.Verify(true); err != nil || !v.OK() {
		t.Fatalf("verify after churn: %v %+v", err, v)
	}
}

func TestP2_ListingUsesCachedManifestsWithoutFillingTheCache(t *testing.T) {
	useProductionLayout(t)
	e := p2New(t)
	p2PutMany(t, e, "l", 12, 3000)
	resp := doSignedRequest(t, e.client, e.ts.URL, e.signer, http.MethodGet, "/b?list-type=2", nil, nil)
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Count(string(b), "<Key>") != 12 || strings.Count(string(b), "<LastModified>") != 12 {
		t.Fatalf("listing: %d %s", resp.StatusCode, b)
	}
	if st := e.s.mcache.stats(); st.Entries != 0 || st.Loads != 0 {
		t.Fatalf("a listing filled the manifest cache: %+v", st)
	}
}
