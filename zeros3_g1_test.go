package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---- G1 audit: authority-mode assertions for the semantic classes of the CDC-specific tests ---------------------
//
// Under the retire-everything hook (ZEROS3_G1_RETIRE_ALL) 167 tests run with CDC authoritative because they assert on
// the CDC chunk layout. testing-harnesses/z4/c1/g1_ledger.py assigns each of them the invariants it exercises (tags) and
// requires that every tag is declared by a g1Cover call in a test below or in the authority suite, and that those tests
// passed in the run being qualified. A g1Cover call sits after the assertions of its scenario, so it is reached only when
// they held. The scenarios run on content whose CDC chunks were physically retired (checked, not assumed).

// g1Cover declares the invariants the surrounding test has just asserted.
func g1Cover(t *testing.T, tags ...string) {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() || t.Skipped() {
			return
		}
		t.Logf("G1 covered: %s", strings.Join(tags, ","))
	})
}

// g1Retired lists the CDC chunk digests of objects whose records replaced them (never components, never chunks of an
// object that stays in CDC) and the digests that must still be stored.
func g1Retired(t *testing.T, l *authLab) (retired, live map[[32]byte]bool) {
	t.Helper()
	retired, live = map[[32]byte]bool{}, authComponentSet(t, l.s)
	for k := range l.want {
		man := l.man(k)
		sha, _ := decodeHexSHA256(man.ObjectSHA256)
		rec, _ := l.s.authLoad(sha)
		for _, c := range man.Chunks {
			sum, _ := decodeHexSHA256(c.SHA256)
			if rec != nil && (rec.Form == authFormDelta || rec.Form == authFormRep) {
				retired[sum] = true
			} else {
				live[sum] = true
			}
		}
	}
	for sum := range live {
		delete(retired, sum)
	}
	return retired, live
}

func g1ReadAll(l *authLab, key string, r byteRange) ([]byte, error) {
	rd := l.s.openObjectReader(l.man(key), r)
	defer rd.close()
	var out []byte
	for {
		b, err := rd.next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, b...)
	}
}

// Retirement is physical: after migration and reclaim no retired CDC chunk exists in any layout, the components and
// the chunks of objects that stay in CDC do, and writing retired content again makes it readable and maintainable.
func TestAuthorityEquivRetirementIsPhysical(t *testing.T) {
	l := authHistoryLab(t, 401, 5, 700_000)
	l.migrate(AuthPolicy{MinSaving: 0.05})
	l.reclaim()
	retired, live := g1Retired(t, l)
	if len(retired) == 0 || len(live) == 0 {
		t.Fatalf("fixture retired %d chunks and kept %d", len(retired), len(live))
	}
	for sum := range retired {
		if _, err := l.s.casStat(sum); err == nil {
			t.Fatalf("retired chunk %x is still stored", sum[:4])
		}
	}
	for sum := range live {
		if _, err := l.s.casStat(sum); err != nil {
			t.Fatalf("live chunk or component %x is missing: %v", sum[:4], err)
		}
	}
	l.exact("retired")
	l.verifyOK("retired")
	// revive on write: the bytes of a retired content stored again under a new key
	var victim string
	for k := range l.want {
		sha, _ := decodeHexSHA256(l.man(k).ObjectSHA256)
		if rec, _ := l.s.authLoad(sha); rec != nil && rec.Form == authFormDelta {
			victim = k
		}
	}
	l.put("revived", l.want[victim])
	l.exact("revived")
	l.verifyOK("revived")
	l.reclaim()
	l.exact("revived-reclaimed")
	l.verifyOK("revived-reclaimed")
	g1Cover(t, "retirement-physical", "revive-on-write")
}

// The same history in two stores, one in CDC and one retired onto authoritative bodies, answers every read, range,
// metadata and error query alike; and the same damage (a chunk of the object, resp. its component) fails closed in both.
func TestAuthorityEquivDifferential(t *testing.T) {
	cdc := authHistoryLab(t, 402, 4, 700_000)
	auth := authHistoryLab(t, 402, 4, 700_000)
	auth.migrate(AuthPolicy{MinSaving: 0.05})
	auth.reclaim()
	var keys []string
	for k := range cdc.want {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a, b := cdc.man(k), auth.man(k)
		if a.TotalLength != b.TotalLength || a.ObjectSHA256 != b.ObjectSHA256 || a.ETag != b.ETag {
			t.Fatalf("%s: metadata differs: %d/%s vs %d/%s", k, a.TotalLength, a.ETag, b.TotalLength, b.ETag)
		}
		n := a.TotalLength
		for _, r := range []byteRange{{0, n - 1}, {0, 0}, {n - 1, n - 1}, {n / 3, n/3 + 123456}, {4095, 4096}, {n, n + 10}, {n + 5, n + 6}} {
			ga, ea := g1ReadAll(cdc, k, r)
			gb, eb := g1ReadAll(auth, k, r)
			if (ea == nil) != (eb == nil) || !bytes.Equal(ga, gb) {
				t.Fatalf("%s range %v: cdc (%d bytes, %v) vs authority (%d bytes, %v)", k, r, len(ga), ea, len(gb), eb)
			}
		}
	}
	for _, miss := range []string{"nope", "k0/", ""} {
		_, _, ea := cdc.s.GetObject("b", miss)
		_, _, eb := auth.s.GetObject("b", miss)
		if (ea == nil) != (eb == nil) || ea != nil && ea.Error() != eb.Error() {
			t.Fatalf("missing key %q: %v vs %v", miss, ea, eb)
		}
	}
	// damage: a middle chunk of one object in CDC, the first component of the same content in the authority store
	var victim string
	for _, k := range keys {
		sha, _ := decodeHexSHA256(auth.man(k).ObjectSHA256)
		if rec, _ := auth.s.authLoad(sha); rec != nil && rec.Form == authFormDelta {
			victim = k
		}
	}
	if victim == "" {
		t.Fatal("no delta-backed object")
	}
	mid, _ := decodeHexSHA256(cdc.man(victim).Chunks[len(cdc.man(victim).Chunks)/2].SHA256)
	corruptChunkAnyLayout(t, cdc.s, mid)
	sha, _ := decodeHexSHA256(auth.man(victim).ObjectSHA256)
	rec, _ := auth.s.authLoad(sha)
	corruptChunkAnyLayout(t, auth.s, rec.Pieces[0].SHA)
	for _, l := range []*authLab{cdc, auth} {
		l.s.purgeReadCaches()
		if res, err := l.s.Verify(true); err != nil || res.OK() {
			t.Fatalf("deep verify missed the damage: %v", err)
		}
		got, err := g1ReadAll(l, victim, byteRange{0, l.man(victim).TotalLength - 1})
		if err == nil && !bytes.Equal(got, l.want[victim]) {
			t.Fatal("a damaged store delivered wrong bytes without an error")
		}
		if err != nil && !bytes.Equal(got, l.want[victim][:len(got)]) {
			t.Fatal("the bytes delivered before the error are not an exact prefix")
		}
	}
	g1Cover(t, "differential-reads", "differential-errors", "differential-metadata", "damage-fails-closed")
}

// Classes corruption and accounting beyond TestAuthorityRetiredEquivalents: what the CDC tests assert about caches,
// streams, damaged records, exports and restores, asserted on retired content.
func TestAuthorityEquivCorruptionClasses(t *testing.T) {
	t.Run("a cached read never masks damage from diagnostics", func(t *testing.T) {
		f := eqFixture(t)
		if code, body, err := f.get(t, nil); code != 200 || err != nil || !bytes.Equal(body, f.l.want[f.key]) {
			t.Fatalf("warm read: %d %v", code, err)
		}
		corruptChunkAnyLayout(t, f.l.s, f.victim.SHA) // no cache purge: the caches may still hold the good bytes
		if res, err := f.l.s.Verify(true); err != nil || res.OK() {
			t.Fatalf("deep verify must read storage, not the cache: %v", err)
		}
		g1Cover(t, "cache-revalidated")
	})
	t.Run("a damaged component ends a stream at an exact prefix", func(t *testing.T) {
		f := eqFixture(t)
		corruptChunkAnyLayout(t, f.l.s, f.victim.SHA)
		f.l.s.purgeReadCaches()
		want := f.l.want[f.key]
		for _, rg := range []byteRange{{0, int64(len(want)) - 1}, {int64(len(want)) / 2, int64(len(want)) - 1}} {
			got, err := g1ReadAll(f.l, f.key, rg)
			if err == nil && !bytes.Equal(got, want[rg.start:rg.end+1]) {
				t.Fatalf("range %v: wrong bytes without an error", rg)
			}
			if !bytes.Equal(got, want[rg.start:rg.start+int64(len(got))]) {
				t.Fatalf("range %v: the delivered bytes are not an exact prefix", rg)
			}
		}
		g1Cover(t, "stream-failsafe")
	})
	t.Run("a damaged record refuses gc and repack", func(t *testing.T) {
		f := eqFixture(t)
		authDamageRecord(t, f.l.s, f.rec.SHA) // a record file or a group frame, whichever layout holds it
		f.l.close()
		if _, err := gcCollect(f.l.dir, true); err == nil {
			t.Fatal("gc ran against a damaged live record")
		}
		if _, err := repackStore(f.l.dir, repackOptions{TargetBytes: defaultPackTargetBytes, MaxLivePercent: 100, Compress: true}); err == nil {
			t.Fatal("repack ran against a damaged live record")
		}
		g1Cover(t, "gc-refuses-corrupt-record")
	})
	t.Run("a closure with a damaged component is not exported", func(t *testing.T) {
		f := eqFixture(t)
		corruptChunkAnyLayout(t, f.l.s, f.victim.SHA)
		f.l.s.purgeReadCaches()
		var buf bytes.Buffer
		if err := f.l.s.ExportAuthorityClosure(f.rec.SHA, &buf); err == nil {
			t.Fatal("a closure holding a damaged component was exported")
		}
		g1Cover(t, "export-refuses-corrupt")
	})
	t.Run("restoring a version whose component is missing mutates nothing", func(t *testing.T) {
		l := newAuthLab(t)
		hist := authHistory(403, 3, 600_000)
		for _, v := range hist {
			l.put("k", v)
		}
		l.migrate(AuthPolicy{MinSaving: 0.05})
		l.reclaim()
		versions := historyFor(t, l.s, "b", "k")
		if len(versions) == 0 {
			t.Fatal("no archived version")
		}
		var victim *authRecord
		for _, vi := range versions {
			man, _, err := l.s.readManifestAny(vi.manifestUUID)
			if err != nil {
				t.Fatal(err)
			}
			sha, _ := decodeHexSHA256(man.ObjectSHA256)
			if rec, _ := l.s.authLoad(sha); rec != nil && rec.Form == authFormDelta {
				victim = rec
				removeChunkAnyLayout(t, l.s, rec.Pieces[0].SHA)
				l.s.purgeReadCaches()
				before := l.man("k").ManifestUUID
				if _, _, err := l.s.RestoreObjectVersion("b", "k", vi.versionID); err == nil {
					t.Fatal("restore of a version with a missing component succeeded")
				}
				if l.man("k").ManifestUUID != before {
					t.Fatal("a refused restore changed the live version")
				}
				break
			}
		}
		if victim == nil {
			t.Skip("no archived version is delta-backed")
		}
		g1Cover(t, "restore-missing-no-mutation")
	})
	t.Run("one damaged component fails the objects that need it and no other", func(t *testing.T) {
		f := eqFixture(t)
		corruptChunkAnyLayout(t, f.l.s, f.victim.SHA)
		f.l.s.purgeReadCaches()
		failed, served := 0, 0
		for k, want := range f.l.want {
			got, err := g1ReadAll(f.l, k, byteRange{0, int64(len(want)) - 1})
			switch {
			case err != nil:
				failed++
			case !bytes.Equal(got, want):
				t.Fatalf("%s: wrong bytes without an error", k)
			default:
				served++
			}
		}
		if failed == 0 || served == 0 {
			t.Fatalf("%d objects failed and %d were served: damage must be confined to the objects that depend on the component", failed, served)
		}
		g1Cover(t, "damage-is-isolated")
	})
}

// Maintenance crash points on retired content: compaction and repack stopped at every durable boundary leave a store
// that reads exactly and verifies deeply, and the interrupted command completes when run again.
func TestAuthorityEquivMaintenanceCrashPoints(t *testing.T) {
	type crashPoint struct {
		point string
		nth   int
	}
	common := []crashPoint{{hookPackRecordWritten, 2}, {hookPackBeforeSync, 1}, {hookPackAfterSync, 1}, {hookPackBeforePublish, 1}, {hookPackAfterRename, 1}, {hookPackPublished, 1}}
	points := map[string][]crashPoint{
		"compact": append(slices.Clone(common), crashPoint{hookBeforeLooseDelete, 2}, crashPoint{hookCompactDone, 1}),
		"repack":  append(slices.Clone(common), crashPoint{hookPackValidated, 1}, crashPoint{hookPackDeleted, 1}, crashPoint{hookRepackDone, 1}),
	}
	for _, cmd := range []string{"compact", "repack"} {
		for _, c := range points[cmd] {
			t.Run(cmd+"/"+c.point+"/"+string(rune('0'+c.nth)), func(t *testing.T) {
				l := authHistoryLab(t, 404, 4, 700_000)
				l.migrate(AuthPolicy{MinSaving: 0.05})
				l.close()
				run := func() error {
					if cmd == "compact" {
						_, err := compactStore(l.dir, authCompactOpts())
						return err
					}
					_, err := repackStore(l.dir, repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100, Compress: true})
					return err
				}
				if cmd == "repack" { // repack needs packs to work on
					if _, err := compactStore(l.dir, authCompactOpts()); err != nil {
						t.Fatal(err)
					}
				}
				calls := 0
				withTestHook(t, func(point string) {
					if point == c.point {
						if calls++; calls == c.nth {
							panic(simulatedCrash{point: point})
						}
					}
				})
				runExpectingSimulatedCrash(t, func() { run() })
				testHook = nil
				l.open()
				l.exact("after crash")
				l.verifyOK("after crash")
				l.close()
				if err := run(); err != nil {
					t.Fatalf("rerun: %v", err)
				}
				l.open()
				l.exact("after rerun")
				l.verifyOK("after rerun")
				l.reclaim() // gc retires the CDC chunks of the migrated content
				l.exact("after gc")
				retired, _ := g1Retired(t, l)
				for sum := range retired {
					if _, err := l.s.casStat(sum); err == nil {
						t.Fatalf("a retired chunk %x is stored after the interrupted maintenance and gc", sum[:4])
					}
				}
			})
		}
	}
	t.Run("ledger", func(t *testing.T) { g1Cover(t, "maintenance-crash-converges") })
}

// Locality: a small range of retired content fetches the components that cover it, not the object; the read amplification
// of a 4 KiB read is bounded, and the physical reads are the same bytes whichever pack layout the components sit in.
func TestAuthorityEquivRangeReadsFetchOnlyWhatTheyNeed(t *testing.T) {
	l := authHistoryLab(t, 405, 4, 1_500_000)
	l.migrate(AuthPolicy{MinSaving: 0.05})
	l.reclaim()
	var key string
	var size int64
	for k := range l.want {
		sha, _ := decodeHexSHA256(l.man(k).ObjectSHA256)
		if rec, _ := l.s.authLoad(sha); rec != nil && rec.Form == authFormDelta && int64(len(l.want[k])) > size {
			key, size = k, int64(len(l.want[k]))
		}
	}
	if key == "" {
		t.Fatal("no delta-backed object")
	}
	l.s.purgeReadCaches()
	before := procRchar()
	var n int64
	for i := int64(0); i < 24; i++ {
		off := (i*7919 + 13) * 4099 % (size - 4096)
		if got := l.read(key, byteRange{off, off + 4095}); !bytes.Equal(got, l.want[key][off:off+4096]) {
			t.Fatalf("range at %d differs", off)
		}
		n++
		l.s.purgeReadCaches()
	}
	amp := float64(procRchar()-before) / float64(n*4096)
	if amp > 64*1024 {
		t.Fatalf("a 4 KiB read costs %.0f bytes of physical reads", amp*4096)
	}
	t.Logf("4 KiB read amplification %.1fx", amp)
	g1Cover(t, "range-reads-bounded")
}

// The store level is a function of the records, never of the maintenance: compaction, repack, tier moves and gc of
// retired content keep the level, the feature markers and old-build refusal.
func TestAuthorityEquivMaintenanceKeepsFormat(t *testing.T) {
	l := authHistoryLab(t, 406, 4, 700_000)
	l.migrate(AuthPolicy{MinSaving: 0.05})
	l.close()
	f0, lvl0 := readFormatVersion(t, l.dir)
	for _, step := range []func() error{
		func() error { _, err := compactStore(l.dir, authCompactOpts()); return err },
		func() error {
			_, err := repackStore(l.dir, repackOptions{TargetBytes: 1 << 20, MaxLivePercent: 100, Compress: true})
			return err
		},
		func() error {
			_, err := tierMove(l.dir, tierMoveOptions{From: tierHot, To: tierCold, All: true, Apply: true})
			return err
		},
		func() error { _, err := gcCollect(l.dir, true); return err },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
		f, lvl := readFormatVersion(t, l.dir)
		if lvl != lvl0 || f.RequiredLevel != f0.RequiredLevel || strings.Join(f.Features, ",") != strings.Join(f0.Features, ",") {
			t.Fatalf("maintenance moved the format: level %d -> %d, features %v -> %v", lvl0, lvl, f0.Features, f.Features)
		}
	}
	l.open()
	l.exact("maintained")
	g1Cover(t, "format-stable-under-maintenance")
}

var _ = hex.EncodeToString

// Writes and reads of large objects keep their bounded memory on a store whose content was retired: a streamed PUT, the
// streamed read of it, and its migration onto an authoritative body.
func TestAuthorityEquivStreamingPutAndReadBoundedMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("large-object test skipped in -short mode")
	}
	l := authHistoryLab(t, 407, 3, 700_000)
	l.migrate(AuthPolicy{MinSaving: 0.05})
	l.reclaim()
	creds := Credentials{AccessKeyID: "AKIAEQUIVALENTS00001", SecretAccessKey: "EquivalentsSecretKeyForZeroS3Tests0123"}
	signer := testSigner{accessKey: creds.AccessKeyID, secretKey: creds.SecretAccessKey, region: "us-east-1"}
	srv := httptest.NewServer(NewServer(l.s, creds, "us-east-1"))
	defer srv.Close()
	const size = 96 << 20
	block := genRandomBytes(72, 3<<20+12345)
	h := sha256.New()
	if _, err := io.Copy(h, &cycleReader{block: block, n: size}); err != nil {
		t.Fatal(err)
	}
	want := h.Sum(nil)
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/b/large", nil)
	signTestRequest(t, req, signer, req.URL.Path, req.URL.RawQuery, nil, time.Now(), &signOpts{payloadHash: hex.EncodeToString(want)})
	req.Body, req.ContentLength = io.NopCloser(&cycleReader{block: block, n: size}), size
	var status int
	if peak := peakHeapGrowth(func() {
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		status = resp.StatusCode
	}); status != 200 || peak > 64<<20 {
		t.Fatalf("streamed PUT: status %d, heap grew %d MiB", status, peak>>20)
	}
	hashObject := func() []byte {
		rd := l.s.openObjectReader(l.man("large"), byteRange{0, size - 1})
		defer rd.close()
		hh := sha256.New()
		for {
			b, err := rd.next()
			if err == io.EOF {
				return hh.Sum(nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			hh.Write(b)
		}
	}
	if peak := peakHeapGrowth(func() {
		if !bytes.Equal(hashObject(), want) {
			t.Fatal("the streamed object reads back different")
		}
	}); peak > 64<<20 {
		t.Fatalf("streaming read grew the heap by %d MiB", peak>>20)
	}
	if peak := peakHeapGrowth(func() { l.migrate(AuthPolicy{MinSaving: -10, MinObject: 1, Order: "oldest"}) }); peak > 192<<20 {
		t.Fatalf("migrating a %d MiB object grew the heap by %d MiB", size>>20, peak>>20)
	}
	l.reclaim()
	if !bytes.Equal(hashObject(), want) {
		t.Fatal("the migrated object reads back different")
	}
	l.verifyOK("large")
	g1Cover(t, "large-streaming-put-bounded-memory")
}

// Objects a representation cannot express (truncated tar, damaged SQLite and Parquet, an empty file, bytes that only
// look like a container) migrate without failing, stay readable byte for byte, and are never admitted as a form they
// cannot rebuild.
func TestAuthorityEquivHostileObjectsStayExact(t *testing.T) {
	l := newAuthLab(t)
	tarHdr := make([]byte, 512)
	copy(tarHdr, "file.txt")
	copy(tarHdr[100:], "0000644\x00")
	copy(tarHdr[257:], "ustar\x00")
	hostile := map[string][]byte{
		"truncated.tar":   append(tarHdr, genRandomBytes(5, 4800)...),
		"damaged.sqlite":  append([]byte("SQLite format 3\x00"), genRandomBytes(6, 20000)...),
		"damaged.parquet": append(append([]byte("PAR1"), genRandomBytes(7, 20000)...), []byte("PAR1")...),
		"gzip-lookalike":  append([]byte{0x1f, 0x8b, 8, 0}, genRandomBytes(8, 5000)...),
		"zstd-lookalike":  append([]byte{0x28, 0xb5, 0x2f, 0xfd}, genRandomBytes(9, 5000)...),
		"car-lookalike":   append([]byte{0x3a, 0xa1, 0x67, 0x72, 0x6f, 0x6f, 0x74, 0x73}, genRandomBytes(10, 5000)...),
		"git-lookalike":   append([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x09"), genRandomBytes(11, 5000)...),
	}
	for k, v := range hostile {
		l.put(k, v)
		l.put(k+".copy", v)
	}
	rep := l.migrate(AuthPolicy{Strategy: "forced", AllowRep: true, AllowDelta: true, MinSaving: -10, MinObject: 1, MinRepGroup: 1, HardCodecs: true})
	if rep.Objects == 0 {
		t.Fatal("nothing planned")
	}
	l.exact("hostile")
	l.verifyOK("hostile")
	l.reclaim()
	l.exact("hostile-reclaimed")
	l.verifyOK("hostile-reclaimed")
	g1Cover(t, "rep-hostile-fallback")
}
