package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type df0Release struct {
	File   string
	Bytes  int
	SHA256 string
}

type df0Family struct {
	Name  string
	Split string
	Files []df0Release
}

type df0Object struct {
	key string
	raw []byte
}

func df0Fixture(t *testing.T, pin df0Release) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(os.Getenv("ZEROS3_DF0_FIXTURES"), pin.File))
	mustOK(t, err)
	sum := sha256.Sum256(raw)
	if len(raw) != pin.Bytes || hex.EncodeToString(sum[:]) != pin.SHA256 {
		t.Fatalf("compressed fixture digest or size differs: %s", pin.File)
	}
	return raw
}

func df0Corpus(t *testing.T) []df0Family {
	t.Helper()
	if os.Getenv("ZEROS3_DF0_FIXTURES") == "" {
		t.Skip("set ZEROS3_DF0_FIXTURES to the pinned release directory")
	}
	data, err := os.ReadFile("testing-harnesses/deep-frontier/DF0/corpus.json")
	mustOK(t, err)
	var families []df0Family
	mustOK(t, json.Unmarshal(data, &families))
	return families
}

func df0Log(t *testing.T, kind string, row any) {
	t.Helper()
	raw, err := json.Marshal(row)
	mustOK(t, err)
	fmt.Printf("%s %s\n", kind, raw)
}

func df0PackSizes(t *testing.T, s *Store, objects []df0Object) []map[string]any {
	t.Helper()
	var contents []map[string]any
	for _, obj := range objects {
		rec, err := s.authLoad(sha256.Sum256(obj.raw))
		mustOK(t, err)
		if rec == nil || rec.Root == nil {
			kind := "cdc"
			if rec != nil {
				kind = fmt.Sprintf("authority-%d", rec.Form)
			}
			contents = append(contents, map[string]any{"key": obj.key, "form": kind})
			continue
		}
		data, err := s.casRead(rec.Root.Rep)
		mustOK(t, err)
		rep, err := unmarshalRep(data, repProdLimits)
		mustOK(t, err)
		row := map[string]any{"key": obj.key, "form": rec.Form, "kind": rep.Kind, "root_bytes": len(data), "recipe_bytes": len(rep.Recipe)}
		var sources int
		switch rep.Kind {
		case repKindDeflateTranslate:
			p, err := parseDeflateTranslate(rep.Recipe)
			mustOK(t, err)
			sources = len(p.index.Sources)
		case repKindBitPatch:
			p, err := parseBitPatchLayout(rep)
			mustOK(t, err)
			sources = len(p.sourceEnd)
		default:
			contents = append(contents, row)
			continue
		}
		var sourceBytes, programBytes int64
		members := make(map[[32]byte]int64)
		for i, m := range rep.Members {
			members[m.SHA] = m.Size
			if i < sources {
				sourceBytes += m.Size
			} else {
				programBytes += m.Size
			}
		}
		var uniqueBytes int64
		for _, n := range members {
			uniqueBytes += n
		}
		row["source_records"], row["program_records"] = sources, len(rep.Members)-sources
		row["source_logical_bytes"], row["program_logical_bytes"], row["unique_member_logical_bytes"] = sourceBytes, programBytes, uniqueBytes
		contents = append(contents, row)
	}
	return contents
}

func df0Prepare(t *testing.T, lane string, objs []df0Object) *authLab {
	t.Helper()
	report := map[string]any{"case": strings.Split(t.Name(), "/"), "lane": lane}
	if lane == "translate" || lane == "bitpatch" {
		start, cpu := time.Now(), fb13CPU()
		for _, obj := range objs {
			if len(obj.raw) > bitPatchMaxObject {
				report["status"], report["limit_bytes"] = "existing-object-limit", bitPatchMaxObject
				df0Log(t, "DF0PREP", report)
				return nil
			}
			if lane == "translate" {
				if _, _, err := gf2SplitGzip(obj.raw); err != nil {
					report["status"], report["reason"] = "gzip-shape-refused", err.Error()
					df0Log(t, "DF0PREP", report)
					return nil
				}
			}
		}
		report["preflight_wall_ns"], report["preflight_cpu_ns"] = time.Since(start).Nanoseconds(), int64(fb13CPU()-cpu)
	}

	l := newAuthLab(t)
	l.s.writeBinaryManifests = true
	startIO := gf8WriteAccounting(t)
	var logical int
	unique := make(map[[32]byte]int)
	for _, obj := range objs {
		l.put(obj.key, obj.raw)
		logical += len(obj.raw)
		unique[sha256.Sum256(obj.raw)] = len(obj.raw)
	}
	ingestIO := gf8WriteAccounting(t)
	start, cpu := time.Now(), fb13CPU()
	switch lane {
	case "archive":
		mustOK(t, l.s.ensureFeature(agFeature))
		ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
		_, err := l.s.AuthorityOptimize(ctx, OptimizePolicy{
			AuthPolicy: AuthPolicy{Apply: true, Order: "oldest", AllowDelta: true, AllowRep: true, CodecReplay: true, MaxDepth: 3, HardCodecs: true},
			Profile:    "archive", Canonical: true, Splice: true, Executables: true, SparseFiles: true, FastReencode: true,
		})
		budgetErr := ctx.Err()
		cancel()
		if errors.Is(err, context.DeadlineExceeded) || (err != nil && errors.Is(budgetErr, context.DeadlineExceeded)) {
			report["status"], report["prepare_cpu_ns"], report["prepare_wall_ns"] = "archive-time-budget", int64(fb13CPU()-cpu), time.Since(start).Nanoseconds()
			df0Log(t, "DF0PREP", report)
			return nil
		}
		mustOK(t, err)
	case "bitpatch", "translate":
		mustOK(t, l.s.ensureFeature(agFeature))
		var translation []gf7Stats
		for i := 1; i < len(objs); i++ {
			base, target := objs[i-1], objs[i]
			if lane == "bitpatch" {
				_, err := gf2PublishNative(t, l.s, l.man(base.key), l.man(target.key), base.raw, target.raw)
				mustOK(t, err)
			} else {
				translation = append(translation, gf7Publish(t, l, base.raw, target.raw, l.man(base.key), l.man(target.key)))
			}
		}
		if lane == "translate" {
			report["compile_stats"] = translation
		}
	}
	report["prepare_cpu_ns"], report["prepare_wall_ns"] = int64(fb13CPU()-cpu), time.Since(start).Nanoseconds()
	if len(objs) > 2 {
		entries, err := l.s.captureSnapshotEntries("b", "version")
		mustOK(t, err)
		if len(entries) != len(objs) {
			t.Fatal("retained series snapshot is missing an object")
		}
		mustOK(t, l.s.publishSnapshot(snapshotDescriptorV1{
			SnapshotFormatVersion: snapshotFormatVersion, SnapshotID: newUUIDv7(),
			CreatedAt: time.Now().UTC(), SourceBucket: "b", SourcePrefix: "version", Entries: entries,
		}))
		report["snapshot_entries"] = len(entries)
	}
	finalStart, finalCPU := time.Now(), fb13CPU()
	finish, err := l.s.finishAuthorityArchive(t.Context(), 0)
	mustOK(t, err)
	l.close()
	endIO := gf8WriteAccounting(t)
	for key, end := range endIO {
		report["ingest_"+key] = ingestIO[key] - startIO[key]
		report["prepare_and_finalize_"+key] = end - ingestIO[key]
	}
	report["finalize_cpu_ns"], report["finalize_wall_ns"] = int64(fb13CPU()-finalCPU), time.Since(finalStart).Nanoseconds()
	report["repack_read_bytes"], report["repack_write_bytes"] = finish.Repack.BytesRead, finish.Repack.BytesWritten
	total, packs, manifests := fb13Allocation(t, l.dir)
	report["allocated_bytes"], report["pack_allocated_bytes"], report["manifest_allocated_bytes"] = total, packs, manifests
	report["logical_bytes"], report["status"] = logical, "measured"
	var uniqueBytes int
	for _, n := range unique {
		uniqueBytes += n
	}
	report["unique_visible_logical_bytes"] = uniqueBytes
	l.open()
	report["objects"] = df0PackSizes(t, l.s, objs)
	reach, err := l.s.reachabilityScan(false, nil)
	mustOK(t, err)
	if !reach.OK() {
		t.Fatal("prepared closure is not reachable")
	}
	var liveBytes int64
	var unknownLengths int
	for _, size := range reach.liveSet().chunks.lens {
		if size < 0 {
			unknownLengths++
		} else {
			liveBytes += size
		}
	}
	report["live_record_bytes"], report["live_record_unknown_lengths"] = liveBytes, unknownLengths
	storage, err := l.s.authorityStorage(reach)
	mustOK(t, err)
	report["authority_storage"] = storage
	l.verifyOK("DF0 native prepared closure")
	l.close()
	df0Log(t, "DF0PREP", report)
	return l
}

type df0CachePages struct {
	Pages, WarmBefore, WarmAfter, EvictionErrors int
}

func df0DropPages(t *testing.T, root string) df0CachePages {
	t.Helper()
	var p df0CachePages
	if runtime.GOARCH != "amd64" {
		return p
	}
	// Only advise data belonging to this disposable store. mincore measures
	// the effect, including files whose pages the filesystem cannot evict.
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || (!strings.Contains(path, "/chunks/") && filepath.Ext(path) != ".pack") {
			return err
		}
		info, err := d.Info()
		if err != nil || info.Size() == 0 {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := f.Sync(); err != nil {
			return err
		}
		m, err := syscall.Mmap(int(f.Fd()), 0, int(info.Size()), syscall.PROT_NONE, syscall.MAP_SHARED)
		if err != nil {
			return err
		}
		defer syscall.Munmap(m)
		pages := make([]byte, (len(m)+os.Getpagesize()-1)/os.Getpagesize())
		residency := func() (int, error) {
			_, _, syscallErr := syscall.Syscall(syscall.SYS_MINCORE, uintptr(unsafe.Pointer(&m[0])), uintptr(len(m)), uintptr(unsafe.Pointer(&pages[0])))
			runtime.KeepAlive(m)
			runtime.KeepAlive(pages)
			if syscallErr != 0 {
				return 0, syscallErr
			}
			n := 0
			for _, state := range pages {
				n += int(state & 1)
			}
			return n, nil
		}
		before, err := residency()
		if err != nil {
			return err
		}
		// Linux FADV_DONTNEED, with the amd64 fadvise64 argument layout.
		if _, _, callErr := syscall.Syscall6(syscall.SYS_FADVISE64, f.Fd(), 0, 0, 4, 0, 0); callErr != 0 {
			p.EvictionErrors++
		}
		after, err := residency()
		if err != nil {
			return err
		}
		p.Pages += len(pages)
		p.WarmBefore += before
		p.WarmAfter += after
		return nil
	})
	mustOK(t, err)
	return p
}

type df0Query struct {
	kind string
	off  int
	n    int
	id   int
}

func df0Queries(size, count int, seed uint64) []df0Query {
	rng := rand.New(rand.NewPCG(seed, seed^0xdf0001))
	classes := []string{"range4", "range4", "range4", "range4", "range4", "range4", "range4", "range4",
		"range64", "range64", "range64", "range64", "range64", "range64",
		"whole", "whole", "whole", "whole", "whole", "whole", "head", "head", "put", "put"}
	out := make([]df0Query, count)
	for i := range out {
		q := df0Query{kind: classes[i%len(classes)], id: i}
		if q.kind == "range4" {
			q.n = min(size, 4<<10)
		} else if q.kind == "range64" {
			q.n = min(size, 64<<10)
		}
		q.off = rng.IntN(size - q.n + 1)
		out[i] = q
	}
	rng.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

func df0SignedRun(t *testing.T, l *authLab, target df0Object, queries []df0Query, clients int, maintenance bool) map[string]any {
	t.Helper()
	srv, signer := newTestServerAndSignerFor(t, l.s)
	httpSrv := httptest.NewServer(srv)
	defer httpSrv.Close()
	type observation struct {
		q            df0Query
		started, end time.Time
	}
	out := make(chan observation, len(queries))
	start := make(chan struct{})
	var wg sync.WaitGroup
	type maintObservation struct {
		report       MaintReport
		started, end time.Time
	}
	maintenanceDone := make(chan maintObservation, 1)
	if maintenance {
		go func() {
			<-start
			now := time.Now()
			options := b4Opts()
			options.MaxSourcePacks = 1
			options.MaxRewriteBytes = 512 << 10
			options.MaxStageBytes = 512 << 10
			options.MaxLivePercent = 50
			options.DutyPercent = 20
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			moved, err := l.s.maintainOnline(ctx, options)
			if err != nil {
				t.Error(err)
			}
			maintenanceDone <- maintObservation{moved, now, time.Now()}
		}()
	}
	for client := range clients {
		wg.Add(1)
		go func(client int) {
			defer wg.Done()
			<-start
			for idx := client; idx < len(queries); idx += clients {
				q := queries[idx]
				path, method, code := "/b/"+target.key, "GET", http.StatusOK
				var input, want []byte
				var headers map[string]string
				switch q.kind {
				case "range4", "range64":
					code = http.StatusPartialContent
					headers = map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", q.off, q.off+q.n-1)}
					want = target.raw[q.off : q.off+q.n]
				case "whole":
					want = target.raw
				case "head":
					method = "HEAD"
				case "put":
					method, path = "PUT", fmt.Sprintf("/b/writes/%04d", q.id)
					input = genRandomBytes(int64(q.id)+7529, 769)
				}
				before := time.Now()
				resp := doSignedRequest(t, httpSrv.Client(), httpSrv.URL, signer, method, path, input, headers)
				got, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				end := time.Now()
				if err != nil || resp.StatusCode != code || method == "GET" && !bytes.Equal(got, want) ||
					method == "HEAD" && resp.ContentLength != int64(len(target.raw)) {
					t.Errorf("%s request %d failed exact signed check: status %d, error %v", q.kind, q.id, resp.StatusCode, err)
					return
				}
				out <- observation{q, before, end}
			}
		}(client)
	}
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	cpu, wall := fb13CPU(), time.Now()
	close(start)
	wg.Wait()
	cpuNS, wallNS := int64(fb13CPU()-cpu), time.Since(wall).Nanoseconds()
	runtime.ReadMemStats(&b)
	close(out)
	if t.Failed() {
		t.FailNow()
	}
	times := make(map[string][]int64)
	var maint maintObservation
	var overlapping int
	if maintenance {
		maint = <-maintenanceDone
	}
	for sample := range out {
		times[sample.q.kind] = append(times[sample.q.kind], sample.end.Sub(sample.started).Nanoseconds())
		if maintenance && sample.started.Before(maint.end) && sample.end.After(maint.started) {
			overlapping++
		}
	}
	n := 0
	percentiles := make(map[string][3]int64)
	for class, samples := range times {
		percentiles[class] = gf0Percentiles(slices.Clone(samples))
		n += len(samples)
	}
	if n != len(queries) {
		t.Fatalf("measured %d of %d requests", n, len(queries))
	}
	result := map[string]any{"clients": clients, "cpu_ns": cpuNS, "wall_ns": wallNS,
		"allocated_during_requests": b.TotalAlloc - a.TotalAlloc, "allocations": b.Mallocs - a.Mallocs,
		"latency_ns": times, "p50_p95_p99_ns": percentiles}
	if maintenance {
		if maint.report.BytesWritten == 0 || overlapping == 0 {
			t.Fatal("maintenance lane did not overlap requests with a real bounded rewrite")
		}
		result["maintenance"] = map[string]any{"report": maint.report, "wall_ns": maint.end.Sub(maint.started).Nanoseconds(), "overlapping_requests": overlapping}
	}
	for _, q := range queries {
		if q.kind == "put" {
			key := fmt.Sprintf("writes/%04d", q.id)
			if got := l.read(key, byteRange{0, 768}); !bytes.Equal(got, genRandomBytes(int64(q.id)+7529, 769)) {
				t.Fatalf("acknowledged write %s differs", key)
			}
		}
	}
	return result
}

func df0Trial(t *testing.T, seed *authLab, target df0Object, lane string, round, clients, count int, cold, maintenance bool) {
	t.Helper()
	l := &authLab{t: t, dir: t.TempDir()}
	mustOK(t, os.CopyFS(l.dir, os.DirFS(seed.dir)))
	pages := df0CachePages{}
	if cold {
		pages = df0DropPages(t, l.dir)
	}
	if maintenance {
		l.s = b4OpenMaint(t, l.dir)
		df0MaintenanceSeed(t, l)
	} else {
		l.open()
	}
	defer l.close()
	if !cold {
		exact := gf2SignedReader(t, l)
		if got := exact(target.key, 0, 0); !bytes.Equal(got, target.raw) {
			t.Fatal("warm-up whole GET differs")
		}
	}
	queries := df0Queries(len(target.raw), count, uint64(934123+round))
	if cold {
		queries = []df0Query{{kind: "range4", off: len(target.raw) / 3, n: 4096}}
	}
	row := df0SignedRun(t, l, target, queries, clients, maintenance)
	row["case"], row["lane"], row["round"] = strings.Split(t.Name(), "/"), lane, round
	row["cold"], row["cache_pages"] = cold, pages
	df0Log(t, "DF0READ", row)
}

func df0MaintenanceSeed(t *testing.T, l *authLab) {
	t.Helper()
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
	_, err = l.s.PutObject("b", "maint/live", live, "application/octet-stream", nil)
	mustOK(t, err)
	digest := sha256.Sum256(live)
	if len(l.s.packLocs(digest)) != 1 {
		t.Fatal("maintenance source is not a single packed live component")
	}
	if err := os.Remove(l.s.chunkPath(digest)); err != nil && !os.IsNotExist(err) {
		mustOK(t, err)
	}
}

func df0Case(t *testing.T, objs []df0Object, scope bool) {
	t.Helper()
	lanes := []string{"cdc", "archive", "bitpatch", "translate"}
	seeds := make(map[string]*authLab)
	for _, lane := range lanes {
		seeds[lane] = df0Prepare(t, lane, objs)
	}
	target := objs[len(objs)-1]
	count := 192
	if scope {
		count = 24
	}
	for round := range 2 {
		if round == 1 {
			slices.Reverse(lanes)
		}
		for _, lane := range lanes {
			if seed := seeds[lane]; seed != nil {
				t.Run(fmt.Sprintf("round-%d-%s", round, lane), func(t *testing.T) {
					df0Trial(t, seed, target, lane, round, 2, count, false, false)
					df0Trial(t, seed, target, lane, round, 1, 1, true, false)
				})
			}
		}
	}
}

func TestDF0Independent(t *testing.T) {
	split := os.Getenv("ZEROS3_DF0_SPLIT")
	if split != "validation" && split != "scope" && split != "blind" {
		t.Skip("set ZEROS3_DF0_SPLIT to one registered partition")
	}
	for _, family := range df0Corpus(t) {
		if family.Split != split {
			continue
		}
		t.Run(family.Name, func(t *testing.T) {
			var series []df0Object
			for i, pin := range family.Files {
				series = append(series, df0Object{fmt.Sprintf("version/%02d", i), df0Fixture(t, pin)})
			}
			for i := 1; i < len(series); i++ {
				t.Run(fmt.Sprintf("adjacent-%d", i), func(t *testing.T) {
					df0Case(t, series[i-1:i+1], family.Split == "scope")
				})
			}
			if family.Name == "curl" {
				t.Run("retained-series", func(t *testing.T) {
					df0Case(t, series, false)
				})
			}
		})
	}
}
