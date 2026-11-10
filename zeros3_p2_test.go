package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Z4-P2: manifest cache, range index and the read path built on them.

// p2Manifest builds a canonical manifest with n chunks of the given lengths.
func p2Manifest(rng *rand.Rand, n int, lens func(i int) int64) manifestV1 {
	m := manifestV1{
		ManifestFormatVersion: 1, CDCFormatVersion: 1, HashAlgorithm: "sha256",
		ManifestUUID: newUUIDv7(), ObjectSHA256: strings.Repeat("ab", 32), ETag: "etag",
		ContentType: "text/plain", CreatedAt: time.Unix(1700000000, 0).UTC(), VersionID: "v",
		Metadata: []metadataKV{{Key: "k", Value: `weird "chunks":[ value ,"chunks":[{"sha256":"` + strings.Repeat("0", 64) + `","length":1}]`}},
	}
	for i := 0; i < n; i++ {
		var d [32]byte
		for j := range d {
			d[j] = byte(rng.Uint32())
		}
		l := lens(i)
		m.Chunks = append(m.Chunks, chunkRef{SHA256: hex.EncodeToString(d[:]), Length: l})
		m.TotalLength += l
	}
	return m
}

// slowServed is the definition of the served form: encoding/json for everything.
func slowServed(data []byte, id string, sha [32]byte) (*servedManifest, error) {
	var man manifestV1
	if err := json.Unmarshal(data, &man); err != nil {
		return nil, fmt.Errorf("manifest %s is corrupt: %w", id, err)
	}
	return newServedManifest(man, sha)
}

func sameServed(a, b *servedManifest) bool {
	if !bytes.Equal(a.sums, b.sums) || len(a.ends) != len(b.ends) {
		return false
	}
	for i := range a.ends {
		if a.ends[i] != b.ends[i] {
			return false
		}
	}
	am, bm := a.man, b.man
	aj, _ := json.Marshal(am)
	bj, _ := json.Marshal(bm)
	return bytes.Equal(aj, bj) && a.cost == b.cost
}

func TestP2_ParseServedMatchesEncodingJSON(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{0, 1, 2, 3, 17, 1000} {
		m := p2Manifest(rng, n, func(i int) int64 { return int64(rng.IntN(300000)) })
		data, _ := json.Marshal(m)
		sha := sha256.Sum256(data)
		fast, err1 := parseServedManifest(data, m.ManifestUUID, sha)
		slow, err2 := slowServed(data, m.ManifestUUID, sha)
		if err1 != nil || err2 != nil {
			t.Fatalf("n=%d fast=%v slow=%v", n, err1, err2)
		}
		if !sameServed(fast, slow) {
			t.Fatalf("n=%d: fast and slow parse disagree", n)
		}
		if fast.chunkCount() != n || n > 0 && fast.ends[n-1] != m.TotalLength {
			t.Fatalf("n=%d: bad index", n)
		}
		back := fast.manifest()
		if fmt.Sprint(back.Chunks) != fmt.Sprint(m.Chunks) {
			t.Fatalf("n=%d: manifest() does not round trip", n)
		}
	}
	// the fast path must actually be used for canonical manifests
	m := p2Manifest(rng, 5, func(int) int64 { return 7 })
	data, _ := json.Marshal(m)
	if _, _, _, _, ok := scanCanonicalChunks(data); !ok {
		t.Fatal("canonical manifest did not take the fast path")
	}
}

// Anything unusual must decode exactly as encoding/json does (the oracle),
// including rejection: whitespace, escapes, reordering, duplicates, bad hex,
// negative or huge lengths, truncation, wrong totals.
func TestP2_ParseServedAgreesOnMutations(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	m := p2Manifest(rng, 6, func(i int) int64 { return int64(1 + rng.IntN(100)) })
	good, _ := json.Marshal(m)
	sha := sha256.Sum256(good)
	check := func(name string, data []byte) {
		t.Helper()
		fast, err1 := parseServedManifest(data, "id", sha)
		slow, err2 := slowServed(data, "id", sha)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("%s: fast err=%v slow err=%v\n%s", name, err1, err2, data)
		}
		if err1 == nil && !sameServed(fast, slow) {
			t.Fatalf("%s: fast and slow parse disagree\n%s", name, data)
		}
	}
	check("good", good)
	s := string(good)
	first := m.Chunks[0].SHA256
	for name, mut := range map[string]string{
		"space":      strings.Replace(s, `"chunks":[`, `"chunks": [`, 1),
		"upper":      strings.Replace(s, first, strings.ToUpper(first), 1),
		"badhex":     strings.Replace(s, first, "zz"+first[2:], 1),
		"short":      strings.Replace(s, first, first[2:], 1),
		"negative":   strings.Replace(s, `"length":`, `"length":-`, 1),
		"leadzero":   strings.Replace(s, `"length":`, `"length":0`, 1),
		"huge":       strings.Replace(s, `"length":`, `"length":99999999999999999999`, 1),
		"maxint":     strings.Replace(s, `"length":`, `"length":9223372036854775807,"x":`, 1),
		"float":      strings.Replace(s, `"length":`, `"length":1.5,"y":`, 1),
		"escape":     strings.Replace(s, `"sha256"`, `"sha256"`, 1),
		"extra":      strings.Replace(s, `"length":`, `"extra":1,"length":`, 1),
		"nullchunks": strings.Replace(s, `"chunks":[`, `"chunks":null,"z":[`, 1),
		"dupchunks":  strings.Replace(s, `"object_sha256"`, `"chunks":[{"sha256":"`+first+`","length":1}],"object_sha256"`, 1),
		"total":      strings.Replace(s, fmt.Sprintf(`"total_length":%d`, m.TotalLength), `"total_length":1`, 1),
		"trunc":      s[:len(s)/2],
		"trailing":   s + " x",
		"emptyarr":   strings.Replace(s, `"chunks":[`, `"chunks":[]`+`,"zz":[`, 1),
	} {
		check(name, []byte(mut))
	}
	for i := 0; i < 3000; i++ { // random single-byte and splice damage
		b := append([]byte(nil), good...)
		switch rng.IntN(3) {
		case 0:
			b[rng.IntN(len(b))] = byte(rng.Uint32())
		case 1:
			j := rng.IntN(len(b))
			b = append(b[:j], b[min(len(b), j+1+rng.IntN(8)):]...)
		default:
			j := rng.IntN(len(b))
			b = append(b[:j], append([]byte{byte(rng.Uint32())}, b[j:]...)...)
		}
		check(fmt.Sprintf("mut%d", i), b)
	}
}

func FuzzServedManifestParse(f *testing.F) {
	rng := rand.New(rand.NewPCG(5, 6))
	for _, n := range []int{0, 1, 3} {
		m := p2Manifest(rng, n, func(i int) int64 { return int64(i + 1) })
		data, _ := json.Marshal(m)
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		sha := sha256.Sum256(data)
		fast, err1 := parseServedManifest(data, "id", sha)
		slow, err2 := slowServed(data, "id", sha)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("fast err=%v slow err=%v", err1, err2)
		}
		if err1 == nil && !sameServed(fast, slow) {
			t.Fatal("disagree")
		}
	})
}

func TestP2_LocateMatchesLinearScan(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	for trial := 0; trial < 200; trial++ {
		n := rng.IntN(40)
		lens := make([]int64, n)
		for i := range lens {
			if rng.IntN(5) != 0 { // zero-length chunks are legal and must be skipped
				lens[i] = int64(1 + rng.IntN(50))
			}
		}
		m := p2Manifest(rng, n, func(i int) int64 { return lens[i] })
		sm, err := newServedManifest(m, [32]byte{})
		if err != nil {
			t.Fatal(err)
		}
		for off := int64(0); off <= m.TotalLength+2; off++ { // ranges are validated non-negative before they reach the reader
			// reference: first chunk i with start(i) <= off < start(i)+len(i)
			want, pos := n, int64(0)
			for i, l := range lens {
				if off < pos+l && off >= pos {
					want = i
					break
				}
				pos += l
			}
			if got := sm.locate(off); got != want {
				t.Fatalf("trial %d off=%d: locate=%d want %d (lens %v)", trial, off, got, want, lens)
			}
		}
	}
}

func TestP2_ServedManifestRejectsInvalid(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	m := p2Manifest(rng, 3, func(int) int64 { return 10 })
	m.Chunks[1].Length = -5
	if _, err := newServedManifest(m, [32]byte{}); err == nil {
		t.Fatal("negative chunk length accepted")
	}
	m = p2Manifest(rng, 2, func(int) int64 { return 10 })
	m.Chunks[0].Length, m.Chunks[1].Length = math.MaxInt64, math.MaxInt64
	if _, err := newServedManifest(m, [32]byte{}); err == nil {
		t.Fatal("overflowing lengths accepted")
	}
	m = p2Manifest(rng, 2, func(int) int64 { return 10 })
	m.TotalLength++
	if _, err := newServedManifest(m, [32]byte{}); err == nil {
		t.Fatal("total_length mismatch accepted")
	}
	m = p2Manifest(rng, 2, func(int) int64 { return 10 })
	m.Chunks[0].SHA256 = "00"
	if _, err := newServedManifest(m, [32]byte{}); err == nil {
		t.Fatal("short digest accepted")
	}
}

// ---- the cache ----

func p2Served(id string, sha [32]byte, cost int64) *servedManifest {
	return &servedManifest{man: manifestV1{ManifestUUID: id}, sha: sha, cost: cost}
}

func p2Sha(i int) (s [32]byte) {
	s[0], s[1], s[2] = byte(i), byte(i>>8), byte(i>>16)
	return
}

func TestP2_CacheIsBoundedAndEvictsWithClock(t *testing.T) {
	c := newManifestCache(8 * 1000) // 1000 bytes per shard
	for i := 0; i < 5000; i++ {
		sha := p2Sha(i)
		id := fmt.Sprint(i)
		got, err := c.get(sha, id, func() (*servedManifest, error) { return p2Served(id, sha, 100), nil })
		if err != nil || got.man.ManifestUUID != id {
			t.Fatal(err)
		}
		if st := c.stats(); st.Bytes > st.Budget {
			t.Fatalf("cache holds %d bytes of a %d budget", st.Bytes, st.Budget)
		}
	}
	st := c.stats()
	if st.Entries == 0 || st.Evictions == 0 || st.Oversize != 0 {
		t.Fatalf("unexpected stats %+v", st)
	}
	// ring and map stay consistent
	for i := range c.shards {
		sh := &c.shards[i]
		if len(sh.ring) != len(sh.m) {
			t.Fatalf("shard %d: ring %d map %d", i, len(sh.ring), len(sh.m))
		}
		var sum int64
		for slot, e := range sh.ring {
			if e.slot != slot || sh.m[e.val.sha] != e {
				t.Fatalf("shard %d slot %d inconsistent", i, slot)
			}
			sum += e.val.cost
		}
		if sum != sh.bytes {
			t.Fatalf("shard %d: bytes %d recomputed %d", i, sh.bytes, sum)
		}
	}
	// an oversized entry is returned but never retained
	big := p2Sha(99999)
	if _, err := c.get(big, "big", func() (*servedManifest, error) { return p2Served("big", big, 5000), nil }); err != nil {
		t.Fatal(err)
	}
	if c.peek(big, "big") != nil || c.stats().Oversize != 1 {
		t.Fatal("oversized entry retained")
	}
}

func TestP2_CacheClockKeepsReferencedEntries(t *testing.T) {
	c := newManifestCache(8 * 300) // 3 entries of 100 per shard
	var shaHot [32]byte
	load := func(id string, sha [32]byte) func() (*servedManifest, error) {
		return func() (*servedManifest, error) { return p2Served(id, sha, 100), nil }
	}
	c.get(shaHot, "hot", load("hot", shaHot))
	for i := 1; i < 400; i++ {
		sha := p2Sha(i)
		sha[0] = 0 // same shard as the hot entry
		c.get(shaHot, "hot", load("hot", shaHot))
		c.get(sha, fmt.Sprint(i), load(fmt.Sprint(i), sha))
	}
	if c.peek(shaHot, "hot") == nil {
		t.Fatal("a continuously referenced entry was evicted by a scan")
	}
}

func TestP2_CacheSingleFlightAndErrorsAreNotCached(t *testing.T) {
	c := newManifestCache(1 << 20)
	sha := p2Sha(1)
	var loads atomic.Int64
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := c.get(sha, "m", func() (*servedManifest, error) {
				loads.Add(1)
				<-gate
				return p2Served("m", sha, 10), nil
			})
			if err != nil || got.man.ManifestUUID != "m" {
				t.Errorf("get: %v", err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("%d loads for one digest", loads.Load())
	}

	bad := p2Sha(2)
	boom := errors.New("boom")
	for i := 0; i < 3; i++ {
		if _, err := c.get(bad, "x", func() (*servedManifest, error) { return nil, boom }); !errors.Is(err, boom) {
			t.Fatalf("error not returned: %v", err)
		}
	}
	if c.peek(bad, "x") != nil || c.stats().LoadErrors != 3 {
		t.Fatalf("a failed load was cached or not retried: %+v", c.stats())
	}
	// a loader panic is an error, not a wedged flight
	pan := p2Sha(3)
	if _, err := c.get(pan, "p", func() (*servedManifest, error) { panic("x") }); err == nil {
		t.Fatal("panic swallowed")
	}
	if _, err := c.get(pan, "p", func() (*servedManifest, error) { return p2Served("p", pan, 1), nil }); err != nil {
		t.Fatalf("flight left behind after panic: %v", err)
	}
}

func TestP2_CacheNameMismatchAndPurgeFence(t *testing.T) {
	c := newManifestCache(1 << 20)
	sha := p2Sha(7)
	c.get(sha, "a", func() (*servedManifest, error) { return p2Served("a", sha, 10), nil })
	// the same digest asked for under another name never returns the first entry
	got, err := c.get(sha, "b", func() (*servedManifest, error) { return p2Served("b", sha, 10), nil })
	if err != nil || got.man.ManifestUUID != "b" {
		t.Fatalf("served %v for the wrong name (%v)", got, err)
	}
	// purge drops everything and fences a load already in flight
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	sha2 := p2Sha(8)
	go func() {
		c.get(sha2, "z", func() (*servedManifest, error) {
			close(started)
			<-release
			return p2Served("z", sha2, 10), nil
		})
		close(done)
	}()
	<-started
	c.purge()
	close(release)
	<-done
	if c.peek(sha2, "z") != nil || c.stats().Rejects != 1 {
		t.Fatalf("a load that raced a purge was inserted: %+v", c.stats())
	}
	if st := c.stats(); st.Entries != 0 && c.peek(sha, "b") != nil {
		t.Fatalf("purge left entries: %+v", st)
	}
}

func TestP2_CacheDisabledAndNilAreTransparent(t *testing.T) {
	for _, c := range []*manifestCache{nil, newManifestCache(0)} {
		n := 0
		for i := 0; i < 3; i++ {
			c.get(p2Sha(1), "a", func() (*servedManifest, error) { n++; return p2Served("a", p2Sha(1), 1), nil })
		}
		if n != 3 || c.peek(p2Sha(1), "a") != nil {
			t.Fatalf("disabled cache retained something (%d loads)", n)
		}
		c.purge()
		_ = c.stats()
	}
}

func BenchmarkP2_ManifestParse(b *testing.B) {
	for _, n := range []int{16, 1024, 16384, 262144} { // 1 MiB .. 16 GiB at 64 KiB chunks
		rng := rand.New(rand.NewPCG(1, uint64(n)))
		m := p2Manifest(rng, n, func(int) int64 { return 40000 + int64(rng.IntN(50000)) })
		data, _ := json.Marshal(m)
		sha := sha256.Sum256(data)
		b.Run(fmt.Sprintf("chunks=%d/stdlib", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var man manifestV1
				if err := json.Unmarshal(data, &man); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("chunks=%d/served", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := parseServedManifest(data, m.ManifestUUID, sha); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("chunks=%d/sha256", n), func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				_ = sha256.Sum256(data)
			}
		})
		sm, _ := parseServedManifest(data, m.ManifestUUID, sha)
		b.Run(fmt.Sprintf("chunks=%d/locate", n), func(b *testing.B) {
			var off int64
			for i := 0; i < b.N; i++ {
				off = (off + 7919_000_003) % sm.man.TotalLength
				_ = sm.locate(off)
			}
		})
	}
}

// A working set far beyond the budget makes copy-ins pointless; the cache must
// notice and stop paying for them, and must keep admitting when the set fits.
func TestP2_ChunkCacheAdmissionAdaptsToTheWorkingSet(t *testing.T) {
	run := func(keys, ops int) (admits, hits int64, c *chunkCache) {
		c = newChunkCache(16 * (64 << 10)) // 1 MiB: 64 chunks of 16 KiB
		rng := rand.New(rand.NewPCG(5, uint64(keys)))
		data := bytes.Repeat([]byte{9}, 16<<10)
		for i := 0; i < ops; i++ {
			var k [32]byte
			n := rng.IntN(keys)
			k[0], k[1], k[2] = byte(n), byte(n>>8), byte(n>>16)
			if _, ok := c.get(&k); !ok {
				c.offer(&k, data, 0)
			}
		}
		st := c.stats()
		return st.Admits, st.Hits, c
	}
	// working set 8x the budget: admissions must collapse well below the miss count
	admits, hits, c := run(512, 400_000)
	if st := c.stats(); st.MaxThrottle == 0 || admits > 120_000 {
		t.Fatalf("thrash: %d admissions for 400k lookups (%d hits), throttle %d", admits, hits, st.MaxThrottle)
	}
	// working set that fits: nearly everything hits and nothing is throttled
	admits2, hits2, c2 := run(40, 400_000)
	if st := c2.stats(); st.MaxThrottle != 0 || hits2 < 390_000 {
		t.Fatalf("hot set: %d hits, %d admissions, throttle %d", hits2, admits2, st.MaxThrottle)
	}
	// once the workload turns friendly the throttle relaxes and admissions resume
	rng := rand.New(rand.NewPCG(8, 8))
	data := bytes.Repeat([]byte{9}, 16<<10)
	for i := 0; i < 1_000_000; i++ {
		var k [32]byte
		n := rng.IntN(30)
		k[0], k[1], k[2] = byte(n), byte(n>>8), 0x77
		if _, ok := c.get(&k); !ok {
			c.offer(&k, data, 0)
		}
	}
	if st := c.stats(); st.MaxThrottle != 0 {
		t.Fatalf("throttle did not relax for a hot set: %d", st.MaxThrottle)
	}
}
