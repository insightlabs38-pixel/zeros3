package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGF8CompiledSnapshotRepair(t *testing.T) {
	base := genRandomBytes(62, 480<<10)
	target := slices.Clone(base)
	for k := range 19 {
		target[745+k*17011] ^= byte(18 + k)
	}
	t.Run("bit-patch", func(t *testing.T) {
		gf8SnapshotRepair(t, base, target, repKindBitPatch)
	})
	t.Run("translate", func(t *testing.T) {
		raw := gf2TarFixture(t, false)
		base := gf2GzipFixture(t, raw, 1, 65<<10, "source.tar")
		target := gf2GzipFixture(t, raw, 9, 83<<10, "changed.tar")
		gf8SnapshotRepair(t, base, target, repKindDeflateTranslate)
	})
}

func gf8SnapshotRepair(t *testing.T, base, target []byte, kind repKind) {
	t.Helper()
	l := newAuthLab(t)
	l.put("base/anchor", base)
	bm := l.man("base/anchor")
	entries, err := l.s.captureSnapshotEntries("b", "base")
	mustOK(t, err)
	if len(entries) != 1 || entries[0].Key != "base/anchor" {
		t.Fatal("snapshot failed to pin its exact anchor")
	}
	id := newUUIDv7()
	mustOK(t, l.s.publishSnapshot(snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion,
		SnapshotID: id, CreatedAt: time.Now().UTC(), SourceBucket: "b", SourcePrefix: "base", Entries: entries}))
	l.put("target", target)
	tm := l.man("target")

	// An independent snapshot keeps the basis durable through expiration
	// before the new dependent has been published.
	mustOK(t, l.s.DeleteObject("b", "base/anchor"))
	delete(l.want, "base/anchor")
	l.offline(func() {
		_, err := pruneHistoryStore(l.dir, historyPruneOptions{Bucket: "b", KeepLast: pruneKeep(0)}, true)
		mustOK(t, err)
	})
	_, err = l.s.finishAuthorityArchive(t.Context(), 0)
	mustOK(t, err)
	mustOK(t, l.s.ensureFeature(agFeature))
	if kind == repKindDeflateTranslate {
		gf7Publish(t, l, base, target, bm, tm)
	} else {
		_, err = gf2PublishNative(t, l.s, bm, tm, base, target)
		mustOK(t, err)
	}
	rec, err := l.s.authLoad(sha256.Sum256(target))
	mustOK(t, err)
	if rec == nil || rec.Root == nil {
		t.Fatal("missing published representation")
	}
	mustOK(t, l.s.deleteSnapshot(id))
	_, err = l.s.finishAuthorityArchive(t.Context(), 0)
	mustOK(t, err)
	l.exact("dependency pinned after anchor prune and snapshot deletion")

	var wire bytes.Buffer
	mustOK(t, l.s.ExportAuthorityClosure(rec.SHA, &wire))
	peer := newAuthLab(t)
	mustOK(t, peer.s.ImportAuthorityClosure(&wire))
	peer.put("target", target)
	_, cfg := fb16TestServer(t, peer, "gf8-repair", true, false)
	cfg.Key = "target"

	// The damage seam removes one exact frame record from an isolated
	// scratch store. Verify catches it before the repair peer supplies
	// the missing digest. No namespace is republished during leaf repair.
	serialized, err := l.s.casRead(rec.Root.Rep)
	mustOK(t, err)
	representation, err := unmarshalRep(serialized, repProdLimits)
	mustOK(t, err)
	if representation.Kind != kind {
		t.Fatalf("repair exercised kind %d, want %d", representation.Kind, kind)
	}
	var index int
	if kind == repKindDeflateTranslate {
		layout, err := parseDeflateTranslate(representation.Recipe)
		mustOK(t, err)
		index = len(layout.index.Sources)
	} else {
		layout, err := parseBitPatchLayout(representation)
		mustOK(t, err)
		index = len(layout.sourceEnd)
	}
	frame := representation.Members[index].SHA
	reach, err := l.s.reachabilityScan(false, nil)
	mustOK(t, err)
	if !reach.OK() {
		t.Fatal("could not collect the live closure for relocation")
	}
	_, err = l.s.compactLive(reach.liveSet(), compactOptions{TargetBytes: 1 << 20, MinBytes: 1})
	mustOK(t, err)
	location, found := l.s.packLookup(frame)
	if !found {
		t.Fatal("finalized frame is absent from native packs")
	}
	pack := l.s.packSnap().packs[location.pack]
	to := tierHot
	if pack.tier == tierHot {
		to = tierCold
	}
	mustOK(t, l.s.movePack(pack.id, pack.tier, to))
	mustOK(t, l.s.reloadPacks())
	l.s.purgeReadCaches()
	l.exact("compiled closure survived pack relocation")
	for _, victim := range [][32]byte{frame, representation.Members[0].SHA} {
		removeChunkAnyLayout(t, l.s, victim)
		l.close()
		l.open()
		verified, err := l.s.Verify(true)
		mustOK(t, err)
		if verified.OK() {
			t.Fatal("missing compiled component passed verification")
		}
		mustOK(t, l.s.recoverAuthorityFromPeer("b", "target", "", cfg))
		l.close()
		l.open()
		l.exact("peer recovered exact compiled body")
	}
	l.verifyOK("after repeated repair reopen")
}

func TestGF8BitPatchBoundaries(t *testing.T) {
	f := repObject{Kind: repKindBitPatch, Size: 1, Members: []repMember{
		{Role: repStored, Size: 1}, {Role: repStored, Size: 2},
	}}
	f.Recipe = append([]byte{1}, make([]byte, sha256.Size)...)
	_, err := parseBitPatchLayout(&f)
	mustOK(t, err)
	bad := f
	bad.Recipe = append([]byte{2}, f.Recipe[1:]...)
	if _, err := parseBitPatchLayout(&bad); err == nil {
		t.Fatal("inconsistent placement admitted")
	}
	bad = f
	bad.Members = slices.Clone(f.Members)
	bad.Members[0].Size = repDirectMax + 1
	if _, err := parseBitPatchLayout(&bad); err == nil {
		t.Fatal("oversized stored input admitted")
	}
	if _, err := bitPatchApplyFrame([]byte{2, 3, 0}, 1, 1, func(int, int) ([]byte, error) { return nil, context.Canceled }); err != context.Canceled {
		t.Fatalf("cancelled source read was not propagated: %v", err)
	}
}

func FuzzGF8TranslateIndex(f *testing.F) {
	seed := translateIndex{
		BaseSize: 20, Size: 20, HeaderLen: 10, DeflateLen: 2, Pad: 6,
		Header: []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 3}, Trailer: make([]byte, 8),
		Sources: [][32]byte{{1}}, SourceSizes: []int{20},
		Frames: []translateFrame{{Start: 0, End: 10, Raw: true}},
	}
	sample, err := json.Marshal(seed)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(sample, uint32(10), uint8(0))
	f.Add([]byte("{}"), uint32(0), uint8(1))
	f.Fuzz(func(t *testing.T, plain []byte, offset uint32, flags uint8) {
		if len(plain) > 12<<10 {
			t.Skip()
		}
		wire := make([]byte, 5)
		binary.LittleEndian.PutUint32(wire, offset)
		wire[4] = flags
		wire = append(wire, xdDeflate(plain, packPriceLevel)...)
		plan, err := parseDeflateTranslate(wire)
		if err == nil && (plan.index.sourceEnds[len(plan.index.sourceEnds)-1] != plan.index.BaseSize ||
			plan.index.Frames[len(plan.index.Frames)-1].End+int(plan.index.Pad) != plan.index.DeflateLen*8) {
			t.Fatal("accepted translation index with a coverage gap")
		}
	})
}

type gf8Probe struct {
	method string
	ns     int64
}

func gf8WriteAccounting(t *testing.T) map[string]int64 {
	t.Helper()
	counters, err := os.ReadFile("/proc/self/io")
	if os.IsNotExist(err) {
		return nil
	}
	mustOK(t, err)
	writes := make(map[string]int64)
	for line := range strings.SplitSeq(string(counters), "\n") {
		name, value, ok := strings.Cut(line, ": ")
		if !ok || name != "wchar" && name != "write_bytes" {
			continue
		}
		n, err := strconv.ParseInt(value, 10, 64)
		mustOK(t, err)
		writes[name] = n
	}
	return writes
}

func gf8MixedLane(t *testing.T, base, target []byte, lane string, round int) {
	t.Helper()
	l := newAuthLab(t)
	l.s.writeBinaryManifests = true
	startWrites := gf8WriteAccounting(t)
	l.put("base", base)
	l.put("target", target)
	ingestWrites := gf8WriteAccounting(t)
	var prepare, prepareWall int64
	if lane != "cdc" {
		mustOK(t, l.s.ensureFeature(agFeature))
		c, start := fb13CPU(), time.Now()
		switch lane {
		case "archive":
			_, err := l.s.AuthorityOptimize(t.Context(), OptimizePolicy{
				AuthPolicy: AuthPolicy{Apply: true, Order: "oldest", AllowDelta: true, AllowRep: true, CodecReplay: true, MaxDepth: 3, HardCodecs: true},
				Profile:    "archive", Canonical: true, Splice: true, Executables: true, SparseFiles: true, FastReencode: true,
			})
			mustOK(t, err)
		case "translate":
			gf7Publish(t, l, base, target, l.man("base"), l.man("target"))
		case "compiled":
			_, err := gf2PublishNative(t, l.s, l.man("base"), l.man("target"), base, target)
			mustOK(t, err)
		default:
			t.Fatalf("unknown mixed lane %s", lane)
		}
		prepare = int64(fb13CPU() - c)
		prepareWall = time.Since(start).Nanoseconds()
	}
	before := time.Now()
	archive, err := l.s.finishAuthorityArchive(t.Context(), 0)
	mustOK(t, err)
	finalize := time.Since(before).Nanoseconds()
	l.close()
	closedWrites := gf8WriteAccounting(t)
	migrationIO := map[string]int64{}
	ingestIO := map[string]int64{}
	for k, v := range closedWrites {
		migrationIO[k] = v - ingestWrites[k]
		ingestIO[k] = ingestWrites[k] - startWrites[k]
	}
	allocated, pack, _ := fb13Allocation(t, l.dir)
	l.open()
	if lane != "cdc" {
		rec, err := l.s.authLoad(sha256.Sum256(target))
		mustOK(t, err)
		if lane == "archive" {
			if rec == nil || rec.Form == authFormPin {
				t.Fatal("archive lane did not retain an optimized target")
			}
		} else {
			if rec == nil || rec.Root == nil {
				t.Fatal("compiled body did not survive finalization")
			}
			wire, err := l.s.casRead(rec.Root.Rep)
			mustOK(t, err)
			rep, err := unmarshalRep(wire, repProdLimits)
			mustOK(t, err)
			want := repKindBitPatch
			if lane == "translate" {
				want = repKindDeflateTranslate
			}
			if rep.Kind != want {
				t.Fatalf("lane %s exercised kind %d, want %d", lane, rep.Kind, want)
			}
		}
	}
	srv, signer := newTestServerAndSignerFor(t, l.s)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	const n = 96
	out := make(chan gf8Probe, n*2)
	var wg sync.WaitGroup
	cpu := fb13CPU()
	for worker := range 2 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := range n {
				method, key, status := "GET", "target", http.StatusPartialContent
				at := (i*47317 + worker*18013) % (len(target) - 4096)
				want := target[at : at+4096]
				headers := map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", at, at+4095)}
				var input []byte
				switch i % 8 {
				case 0:
					method, status, headers = "HEAD", http.StatusOK, nil
				case 1:
					method, key, status, headers = "PUT", fmt.Sprintf("foreground/%d/%d", worker, i), http.StatusOK, nil
					input = genRandomBytes(int64(13+i+worker*n), 701)
				case 2:
					status, headers, want = http.StatusOK, nil, target
				}
				started := time.Now()
				resp := doSignedRequest(t, ts.Client(), ts.URL, signer, method, "/b/"+key, input, headers)
				got, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				duration := time.Since(started).Nanoseconds()
				if err != nil || resp.StatusCode != status {
					t.Errorf("mixed %s status %d err %v", method, resp.StatusCode, err)
					return
				}
				if method == "GET" && !bytes.Equal(got, want) {
					t.Error("mixed GET differs from source")
					return
				}
				if method == "HEAD" && resp.ContentLength != int64(len(target)) {
					t.Error("mixed HEAD length differs")
					return
				}
				if method == "GET" && status == http.StatusOK {
					method = "WHOLE"
				}
				out <- gf8Probe{method, duration}
			}
		}(worker)
	}
	wg.Wait()
	close(out)
	measured := map[string][]int64{}
	for p := range out {
		measured[p.method] = append(measured[p.method], p.ns)
	}
	if t.Failed() {
		t.FailNow()
	}
	for worker := range 2 {
		for i := 1; i < n; i += 8 {
			key := fmt.Sprintf("foreground/%d/%d", worker, i)
			if got := l.read(key, byteRange{0, 700}); !bytes.Equal(got, genRandomBytes(int64(13+i+worker*n), 701)) {
				t.Fatal("mixed PUT changed an acknowledged byte")
			}
		}
	}
	work := int64(fb13CPU() - cpu)
	l.verifyOK("mixed compiled research lane")
	tails := map[string][3]int64{}
	counts := map[string]int{}
	for method, samples := range measured {
		tails[method] = gf0Percentiles(samples)
		counts[method] = len(samples)
	}
	row, err := json.Marshal(map[string]any{"lane": lane, "round": round, "allocated": allocated, "pack": pack,
		"signed_ns": tails, "counts": counts, "prepare_cpu_ns": prepare, "prepare_wall_ns": prepareWall,
		"request_cpu_ns": work,
		"logical_bytes":  len(base) + len(target), "hot_overlay_bytes": 0,
		"ingest_process_io": ingestIO, "migration_process_io": migrationIO,
		"finalize_ns": finalize, "repack_written_bytes": archive.Repack.BytesWritten,
		"repack_read_bytes": archive.Repack.BytesRead})
	mustOK(t, err)
	fmt.Printf("GF8 %s\n", row)
}

func TestGF8SignedMixed(t *testing.T) {
	root, inputs := gf0Corpus(t)
	for _, family := range inputs {
		if family.Name != "typescript-gzip" {
			continue
		}
		b, _ := gf0ReadFixture(t, root, family, 0)
		v, _ := gf0ReadFixture(t, root, family, 1)
		for round := range 2 {
			modes := []string{"cdc", "compiled", "translate", "archive"}
			if round != 0 {
				slices.Reverse(modes)
			}
			for _, mode := range modes {
				t.Run(fmt.Sprintf("%d/%s", round, mode), func(t *testing.T) { gf8MixedLane(t, b, v, mode, round) })
			}
		}
		return
	}
	t.Fatal("missing pinned TypeScript development pair")
}
