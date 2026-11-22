package main

// Z4-P8 measurements and differential tests for the shared delta matcher.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// p8Pairs lists consecutive-revision (base, target) file pairs of the pinned families.
func p8Pairs(t *testing.T, root string) (names []string, pairs [][2]string) {
	for _, fam := range econFamilies(root) {
		if f := os.Getenv("ZEROS3_P8_FAMILY"); f != "" && !strings.Contains(","+f+",", ","+fam.name+",") {
			continue
		}
		if fam.name == "unrelated" {
			continue
		}
		if os.Getenv("ZEROS3_P8_FAMILY") != "" {
			p8CheckFamily(t, fam)
		}
		fs := fam.files
		if fam.name == "dex" { // pair each dex file with its predecessor of the same name
			byName := map[string][]string{}
			for _, f := range fs {
				byName[filepath.Base(f)] = append(byName[filepath.Base(f)], f)
			}
			var keys []string
			for k := range byName {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v := byName[k]
				for i := 1; i < len(v); i++ {
					names, pairs = append(names, fam.name), append(pairs, [2]string{v[i-1], v[i]})
				}
			}
			continue
		}
		for i := 1; i < len(fs); i++ {
			names, pairs = append(names, fam.name), append(pairs, [2]string{fs[i-1], fs[i]})
		}
	}
	return
}

func p8CheckFamily(t *testing.T, fam econFamily) {
	t.Helper()
	required := 2
	switch fam.name {
	case "src-tar-short", "dex-b8":
		required = 3
	case "parquet", "parquet-zstd", "sqlite":
		required = 4
	case "dex":
		required = 6
	}
	if len(fam.files) < required {
		t.Fatalf("expected at least %d inputs for %s, found %d", required, fam.name, len(fam.files))
	}
}

func p8Apply(bases [][]byte, ops []xdOp, want []byte) bool {
	out := make([]byte, 0, len(want))
	for _, o := range ops {
		if o.base < 0 {
			out = append(out, o.lit...)
		} else {
			out = append(out, bases[o.base][o.off:o.off+o.n]...)
		}
	}
	return bytes.Equal(out, want)
}

// TestP8Matcher compares matchers on real consecutive revisions: wall time of the
// match alone, exact reconstruction, and the serialized envelope size.
func TestP8Matcher(t *testing.T) {
	root := os.Getenv("ZEROS3_B2_CORPUS")
	if root == "" {
		t.Skip("research harness: set ZEROS3_B2_CORPUS")
	}
	names, pairs := p8Pairs(t, root)
	if len(pairs) == 0 {
		t.Fatalf("no matcher pairs for family %q in %s", os.Getenv("ZEROS3_P8_FAMILY"), root)
	}
	for k, pr := range pairs {
		b, err := os.ReadFile(pr[0])
		if err != nil {
			t.Fatal(err)
		}
		tg, err := os.ReadFile(pr[1])
		if err != nil {
			t.Fatal(err)
		}
		if len(tg) == 0 || len(b) == 0 || int64(len(tg)) > xdMaxObject || int64(len(b)) > xdMaxObject {
			t.Fatalf("unsupported matcher input size: %v (%d, %d bytes)", pr, len(b), len(tg))
		}
		src := []xdSrc{{sha256.Sum256(b), b}}
		for _, m := range strings.Split(envOr("ZEROS3_P8_MATCHERS", "xd"), ",") {
			best := time.Duration(1 << 62)
			var ops []xdOp
			for r := 0; r < 3; r++ {
				t0 := time.Now()
				ops = p8RunMatcher(m, [][]byte{b}, tg)
				best = min(best, time.Since(t0))
			}
			ok := p8Apply([][]byte{b}, ops, tg)
			blob, _, err := xdEncodeOps(src, ops, tg, 128<<10)
			row := map[string]any{"p8": "matcher", "family": names[k], "base": filepath.Base(filepath.Dir(pr[0])) + "/" + filepath.Base(pr[0]), "target": filepath.Base(pr[1]), "matcher": m,
				"tgt_mib": float64(len(tg)) / (1 << 20), "match_ms": best.Milliseconds(), "mibps": float64(len(tg)) / (1 << 20) / best.Seconds(), "ops": len(ops), "envelope": len(blob), "exact": ok && err == nil}
			p8Emit(t, row)
			if !ok || err != nil {
				t.Fatalf("%s %v: matcher %s does not reconstruct the target (%v)", names[k], pr, m, err)
			}
		}
	}
}

func p8Emit(t *testing.T, row map[string]any) {
	t.Helper()
	line, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(line))
	if out := os.Getenv("ZEROS3_B2_OUT"); out != "" {
		f, err := os.OpenFile(out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Write(append(line, '\n'))
		if err := errors.Join(err, f.Close()); err != nil {
			t.Fatal(err)
		}
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func p8RunMatcher(name string, bases [][]byte, t []byte) []xdOp {
	switch name {
	case "dw":
		p := dwDcoreDefaultParams()
		x, err := dwDcoreBuildIndex(bases, p)
		if err != nil {
			panic(err)
		}
		ops := x.Match(t)
		out := make([]xdOp, len(ops))
		var pos int64
		for i, o := range ops {
			out[i] = xdOp{tpos: pos, n: o.N, base: o.Base, off: o.Off, lit: o.Lit}
			pos += o.N
		}
		return out
	}
	return refXdMatch(bases, t)
}

var _ = fmt.Sprintf

// TestP8Rep times the production structured-representation build (analysis, staging, proof) and a whole-object
// replay of one file per run:
//
//	ZEROS3_P8_FILES=a.tar.gz,b.tar.zst ZEROS3_P8_REPS=3 ZEROS3_B2_OUT=out.jsonl go test -run TestP8Rep -count=1 .
func TestP8Rep(t *testing.T) {
	files := os.Getenv("ZEROS3_P8_FILES")
	if files == "" {
		t.Skip("research harness: set ZEROS3_P8_FILES")
	}
	reps := 1
	fmt.Sscan(os.Getenv("ZEROS3_P8_REPS"), &reps)
	for _, f := range strings.Split(files, ",") {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for r := 0; r < reps; r++ {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			s.setRepMode(repModePrefer)
			if err := s.CreateBucket("b"); err != nil {
				t.Fatal(err)
			}
			s.rep.q.Store(nil)
			// put with representations off so the build below is the only analysis
			s.setRepMode(repModeOff)
			if _, err := s.PutObject("b", "o", data, "application/octet-stream", nil); err != nil {
				t.Fatal(err)
			}
			s.setRepMode(repModePrefer)
			lm := testLiveManifests(t, s)
			if len(lm) != 1 {
				t.Fatalf("manifests: %d", len(lm))
			}
			man := lm[0].man
			base := dirBytes(t, dir)
			runtime.GC()
			var m0, m1 runtime.MemStats
			runtime.ReadMemStats(&m0)
			cpu0 := procCPUSeconds()
			resetRSSPeak()
			t0 := time.Now()
			err = s.repBuild(man)
			build := time.Since(t0).Seconds()
			cpu := procCPUSeconds() - cpu0
			rss := rssPeakMiB()
			runtime.ReadMemStats(&m1)
			row := map[string]any{"p8": "rep", "file": filepath.Base(f), "rep": r, "bytes": len(data), "build_s": build, "mibps": float64(len(data)) / (1 << 20) / build, "cpu_s": cpu, "rss_mib": rss,
				"alloc_mib": float64(m1.TotalAlloc-m0.TotalAlloc) / (1 << 20), "err": fmt.Sprint(err), "rep_phys": dirBytes(t, dir) - base}
			if err == nil {
				t1 := time.Now()
				rd, e2 := s.repOpenReader(man, byteRange{0, man.TotalLength - 1})
				ok := e2 == nil && rd != nil
				h := sha256.New()
				if ok {
					for {
						b, e := rd.next()
						if e != nil {
							break
						}
						h.Write(b)
					}
					rd.close()
				}
				row["replay_s"], row["exact"] = time.Since(t1).Seconds(), ok && fmt.Sprintf("%x", h.Sum(nil)) == man.ObjectSHA256
			}
			p8Emit(t, row)
			s.Close()
			if row["exact"] != true {
				t.Fatalf("could not build and replay %s exactly: %v", f, err)
			}
		}
	}
}

type refXdBucket struct {
	h   uint64
	pos [4]uint32
	n   uint8
}

// refXdMatch (the pre-P8 production matcher, kept as the benchmark reference) finds COPY/ADD ops with a content-sampled anchor index over the
// bases: base and target share one sampling rule, so shifted content aligns.
// Anchors only propose candidates; every COPY is verified byte-for-byte.
func refXdMatch(bases [][]byte, t []byte) []xdOp {
	total := 0
	for _, b := range bases {
		total += len(b)
	}
	k := 4
	for k < total>>18 {
		k <<= 1
	}
	mask := uint64(k - 1)
	capacity := 1
	for capacity < 4*(total/k+64) {
		capacity <<= 1
	}
	table := make([]refXdBucket, capacity)
	tmask := uint64(capacity - 1)
	for bi, b := range bases {
		if len(b) < xdWindow {
			continue
		}
		h := xdWindowHash(b)
		for i := 0; ; i++ {
			if xdMix(h)&mask == 0 {
				slot := h & tmask
				for probe := 0; probe < 16; probe++ {
					v := &table[slot]
					if v.n == 0 {
						v.h = h
					}
					if v.h == h {
						if v.n < 4 {
							v.pos[v.n] = uint32(bi)<<30 | uint32(i)
							v.n++
						}
						break
					}
					slot = (slot + 1) & tmask
				}
			}
			if i+xdWindow >= len(b) {
				break
			}
			h = (h-(uint64(b[i])+1)*xdPow)*xdP + uint64(b[i+xdWindow]) + 1
		}
	}
	var ops []xdOp
	start := 0
	emit := func(end int) {
		if end > start {
			ops = append(ops, xdOp{base: -1, lit: t[start:end], n: int64(end - start)})
		}
	}
	i := 0
	var h uint64
	if len(t) >= xdWindow {
		h = xdWindowHash(t)
	}
	for i+xdWindow <= len(t) {
		if xdMix(h)&mask == 0 {
			slot := h & tmask
			bestN, bestBack, bestBase, bestOff := 0, 0, 0, 0
			for probe := 0; probe < 16; probe++ {
				v := &table[slot]
				if v.n == 0 {
					break
				}
				if v.h == h {
					for _, p := range v.pos[:v.n] {
						bi, off := int(p>>30), int(p&(1<<30-1))
						b := bases[bi]
						f := 0
						for off+f < len(b) && i+f < len(t) && b[off+f] == t[i+f] {
							f++
						}
						bk := 0
						for bk < i-start && bk < off && b[off-bk-1] == t[i-bk-1] {
							bk++
						}
						if f >= xdWindow && f+bk > bestN {
							bestN, bestBack, bestBase, bestOff = f+bk, bk, bi, off-bk
						}
					}
					break
				}
				slot = (slot + 1) & tmask
			}
			if bestN >= xdMinCopy {
				emit(i - bestBack)
				if n := len(ops); n > 0 && ops[n-1].base == bestBase && ops[n-1].off+ops[n-1].n == int64(bestOff) && start == i-bestBack {
					ops[n-1].n += int64(bestN)
				} else {
					ops = append(ops, xdOp{base: bestBase, off: int64(bestOff), n: int64(bestN)})
				}
				i = i - bestBack + bestN
				start = i
				if i+xdWindow <= len(t) {
					h = xdWindowHash(t[i:])
				}
				continue
			}
		}
		if i+xdWindow >= len(t) {
			break
		}
		h = (h-(uint64(t[i])+1)*xdPow)*xdP + uint64(t[i+xdWindow]) + 1
		i++
	}
	emit(len(t))
	var pos int64
	for j := range ops {
		ops[j].tpos = pos
		pos += ops[j].n
	}
	return ops
}

// The shared matcher reconstructs every target exactly through the envelope, deterministically, for edited
// histories, unrelated data, repeats, zero-length and tiny inputs, with one and several bases.
func TestP8SharedMatcherExactAndDeterministic(t *testing.T) {
	hist := authHistory(21, 4, 300_000)
	rep := bytes.Repeat([]byte("abcdefgh"), 40_000)
	inputs := map[string][2][]byte{
		"history":   {hist[1], hist[2]},
		"unrelated": {genRandomBytes(1, 200_000), genRandomBytes(2, 200_000)},
		"repeats":   {rep, append(append([]byte{}, rep[:10_000]...), rep[3:]...)},
		"tiny":      {[]byte("hello"), []byte("hello world")},
		"identical": {hist[0], hist[0]},
		"shuffled":  {hist[0], append(append([]byte{}, hist[0][150_000:]...), hist[0][:150_000]...)},
	}
	for name, in := range inputs {
		srcs := []xdSrc{{sha256.Sum256(in[0]), in[0]}}
		if name == "history" {
			srcs = append(srcs, xdSrc{sha256.Sum256(hist[0]), hist[0]})
		}
		a, refsA, err := xdEncode(srcs, in[1], 64<<10)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b, _, _ := xdEncode(srcs, in[1], 64<<10)
		if !bytes.Equal(a, b) {
			t.Fatalf("%s: encoding is not deterministic", name)
		}
		h, more, err := xdParseHeader(a)
		if err != nil || more {
			t.Fatalf("%s: header %v", name, err)
		}
		var out []byte
		for i := range h.frames {
			ops, err := xdDecodeFrame(&h, i, a[h.frames[i].poff:h.frames[i].poff+h.frames[i].plen])
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range ops {
				if o.base < 0 {
					out = append(out, o.lit...)
					continue
				}
				for k, r := range refsA {
					if k == o.base {
						for _, s := range srcs {
							if s.sha == r.SHA {
								out = append(out, s.data[o.off:o.off+o.n]...)
							}
						}
					}
				}
			}
		}
		if !bytes.Equal(out, in[1]) {
			t.Fatalf("%s: reconstruction differs", name)
		}
	}
}

func TestP8WinCache(t *testing.T) {
	c := newAuthWinCache(1000)
	k := func(i int64) authWinKey { return authWinKey{off: i} }
	var loads atomic.Int64
	mk := func(n int) func() ([]byte, error) {
		return func() ([]byte, error) { loads.Add(1); return bytes.Repeat([]byte{7}, n), nil }
	}
	// concurrent loads of one key decode once
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.load(k(1), func() ([]byte, error) { time.Sleep(5 * time.Millisecond); return mk(100)() })
		}()
	}
	wg.Wait()
	if loads.Load() != 1 || c.hits.Load() != 15 {
		t.Fatalf("loads=%d hits=%d", loads.Load(), c.hits.Load())
	}
	// byte budget: older entries leave, an entry above half the budget is served but not kept
	for i := int64(2); i < 30; i++ {
		c.load(k(i), mk(100))
	}
	if c.used > 1000 || len(c.m) > 10 {
		t.Fatalf("cache holds %d bytes in %d entries over a 1000 byte budget", c.used, len(c.m))
	}
	before := loads.Load()
	c.load(k(100), mk(600))
	c.load(k(100), mk(600))
	if loads.Load() != before+2 {
		t.Fatal("an entry above half the budget was retained")
	}
	// a failed load is not cached and is reported to every waiter
	boom := errors.New("boom")
	if _, err := c.load(k(500), func() ([]byte, error) { return nil, boom }); err != boom {
		t.Fatal(err)
	}
	if d, err := c.load(k(500), mk(10)); err != nil || len(d) != 10 {
		t.Fatalf("retry after failure: %v", err)
	}
	// a record generation or body identity never aliases another entry
	g1, g2 := authWinKey{sha: [32]byte{1}, gen: 1}, authWinKey{sha: [32]byte{1}, gen: 2}
	c.load(g1, func() ([]byte, error) { return []byte("one"), nil })
	if d, _ := c.load(g2, func() ([]byte, error) { return []byte("two"), nil }); string(d) != "two" {
		t.Fatal("stale generation served")
	}
	var nilc *authWinCache
	if d, err := nilc.load(k(1), mk(3)); err != nil || len(d) != 3 {
		t.Fatal("nil cache must call through")
	}
}

// A decoded source or window cached while planning must never stand in for the physical check of a proof: a base
// chunk that rots between planning and proof fails the proof closed and the object stays CDC.
func TestAuthorityPlanCacheNeverHidesPhysicalCorruption(t *testing.T) {
	l := authHistoryLab(t, 91, 4, 700_000)
	base := l.man("k1")
	fires := 0
	old := testHook
	t.Cleanup(func() { testHook = old })
	testHook = func(point string) {
		if point == hookAuthComponentsFlushed {
			fires++
			if fires == 2 { // k2 is being committed; its base k1 was read (and cached) when it was planned
				for _, c := range base.Chunks {
					corruptChunkOnDisk(t, l.s, c.SHA256)
				}
				l.s.ccache.purge()
			}
		}
	}
	rep := l.migrate(AuthPolicy{MinSaving: 0.05, MaxDepth: 2, AllowDelta: true})
	testHook = old
	forms := map[string]string{}
	for _, p := range rep.Plans {
		forms[p.SHA[:12]] = p.Form + ":" + p.Reason
	}
	k2 := l.man("k2").ObjectSHA256[:12]
	if f := forms[k2]; !strings.HasPrefix(f, "cdc:proof failed") {
		t.Fatalf("object planned against a base that rotted before its proof was admitted: %v", forms)
	}
	// no byte served for any key is wrong (k1 itself may now be unreadable: that is the injected damage)
	for key, want := range l.want {
		man := l.man(key)
		rd := l.s.openObjectReader(man, byteRange{0, man.TotalLength - 1})
		var got []byte
		var err error
		for {
			var b []byte
			if b, err = rd.next(); err != nil {
				break
			}
			got = append(got, b...)
		}
		rd.close()
		if err == io.EOF && !bytes.Equal(got, want) {
			t.Fatalf("%s: wrong bytes served", key)
		}
	}
}

// The similarity sketch is persisted in records: the sliced parallel computation must equal the sequential one.
func TestP8SketchSlicedEqualsSequential(t *testing.T) {
	for _, n := range []int{10, 16, 17, 1000, 4<<20 + 15, 4<<20 + 16, 9<<20 + 5} {
		b := genRandomBytes(int64(n), n)
		var want []uint64
		if n >= xdWindow {
			sk := authSketch{k: 16}
			h := xdWindowHash(b)
			for i := 0; ; i++ {
				sk.add(xdMix(h))
				if i+xdWindow >= len(b) {
					break
				}
				h = (h-(uint64(b[i])+1)*xdPow)*xdP + uint64(b[i+xdWindow]) + 1
			}
			want = sk.out
		}
		if got := authFeatures(b, 16); !slices.Equal(got, want) {
			t.Fatalf("n=%d: sketch differs", n)
		}
	}
}

func TestP8IndexCacheConcurrentEviction(t *testing.T) {
	cache := &authIdxCache{budget: 2 << 20, m: make(map[[32]byte]*authIdxEnt)}
	inputs := make([][]byte, 8)
	for i := range inputs {
		inputs[i] = genRandomBytes(int64(i+1), 32<<10)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, input := range inputs {
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				sum := sha256.Sum256(input)
				x, err := cache.get(sum, input, dwDcoreDefaultParams())
				if err != nil || x == nil {
					t.Errorf("index %x: %v", sum[:4], err)
				}
			}()
		}
	}
	close(start)
	wg.Wait()
	var total int64
	for _, entry := range cache.m {
		total += entry.cost
		if entry.cost <= int64(len(entry.x.table))*8 {
			t.Fatal("cache budget omitted the retained source")
		}
	}
	if total != cache.used || total > cache.budget || len(cache.order) != len(cache.m) {
		t.Fatalf("inconsistent cache accounting: computed=%d used=%d budget=%d order=%d map=%d",
			total, cache.used, cache.budget, len(cache.order), len(cache.m))
	}
	oversize := &authIdxCache{budget: 1, m: make(map[[32]byte]*authIdxEnt)}
	if x, err := oversize.get(sha256.Sum256(inputs[0]), inputs[0], dwDcoreDefaultParams()); err != nil || x == nil {
		t.Fatalf("oversize entry did not produce a usable index: %v", err)
	}
	if oversize.used != 0 || len(oversize.m) != 0 || len(oversize.order) != 0 {
		t.Fatal("oversize entry was retained")
	}
}
