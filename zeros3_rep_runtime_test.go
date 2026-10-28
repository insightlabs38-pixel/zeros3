package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Benchmark the complete maintenance scan, including representation liveness.
// The SQLite-shaped workload has thousands of distinct page components.
func BenchmarkRepReachabilitySQLite(b *testing.B) {
	l := newRepLab(b, repModeShadow)
	l.put("db", repTestSQLite(4096, 4096, 1401))
	l.offline(func() {
		o := defaultCompactOptions()
		o.TargetBytes, o.MinBytes = 4<<20, 1
		if _, err := compactStore(l.dir, o); err != nil {
			b.Fatal(err)
		}
	})
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		rr, err := l.s.computeReachability(false)
		if err != nil || !rr.OK() || rr.RepRoots != 1 || rr.RepComponents < 4096 {
			b.Fatalf("reachability: roots=%d components=%d err=%v issues=%v", rr.RepRoots, rr.RepComponents, err, rr.Issues)
		}
	}
}

// A corrupt live root must be rejected before its oversized payload is read.
func TestRepOversizedRootReadBounded(t *testing.T) {
	l := newRepLab(t, repModeShadow)
	l.put("db", repTestSQLite(12, 4096, 1402))
	_, man := l.manifest("db")
	path := l.s.repRootPath(man.ManifestUUID)
	if err := os.Truncate(path, 4*repMaxRootSize); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := l.s.loadRepRoot(man.ManifestUUID)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, errRepDescriptor) {
		t.Fatalf("oversized root: %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("oversized root allocated %d bytes before rejection", allocated)
	}
	rr, err := l.s.computeReachability(false)
	if err != nil || rr.OK() || rr.RepFaults[man.ManifestUUID] == "" {
		t.Fatalf("oversized live root must fail closed: err=%v issues=%v", err, rr.Issues)
	}
	// Unrelated files are not touched by the read or failed maintenance scan.
	if fi, err := os.Stat(filepath.Join(l.dir, "manifests", man.ManifestUUID+".json")); err != nil || fi.Size() == 0 {
		t.Fatalf("manifest unavailable: %v", err)
	}
}

func TestRepTransferBackfill(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, mode := range []repMode{repModeOff, repModeShadow, repModePrefer} {
			for _, fail := range []bool{false, true} {
				if fail && mode != repModePrefer {
					continue
				}
				t.Run(fmt.Sprintf("native=%v/%s/failure=%v", native, mode, fail), func(t *testing.T) {
					_, src, dir, dst, creds, region := newReplicateTestServerPair(t)
					data := repTestSQLite(32, 4096, 1403)
					mustPutSourceObject(t, src, "src", "db", data, "application/octet-stream", nil)
					mustCreateReplicateBucket(t, dst, "dst")
					dst.store.setRepMode(mode)
					srcHTTP := httptest.NewServer(src)
					defer srcHTTP.Close()
					dstHTTP := httptest.NewServer(dst)
					defer dstHTTP.Close()
					tmp := filepath.Join(dir, "tmp")
					fired := false
					if fail {
						withTestHook(t, func(p string) {
							if p == hookRepAnalysisStarted && !fired {
								fired = true
								if err := os.Rename(tmp, tmp+".blocked"); err != nil {
									panic(err)
								}
							}
						})
					}
					cfg := syncClientConfig{Endpoint: dstHTTP.URL, Bucket: "dst", Key: "db", Creds: creds, Region: region, HTTPClient: dstHTTP.Client()}
					var err error
					if native {
						_, err = replicateObject(replicateConfig{Source: syncClientConfig{Endpoint: srcHTTP.URL, Bucket: "src", Key: "db", Creds: creds, Region: region, HTTPClient: srcHTTP.Client()}, Dest: cfg, Workers: 4})
					} else {
						path := filepath.Join(t.TempDir(), "db")
						if e := os.WriteFile(path, data, 0600); e != nil {
							t.Fatal(e)
						}
						cfg.LocalPath = path
						_, err = syncFile(cfg)
					}
					testHook = nil
					if fired {
						if e := os.Rename(tmp+".blocked", tmp); e != nil {
							t.Fatal(e)
						}
					}
					if err != nil {
						t.Fatalf("authoritative transfer failed: %v", err)
					}
					_, man := repManifestOf(t, dst.store, "dst", "db")
					wantRoot := mode != repModeOff && !fail
					if got := repHasRoot(dst.store, man.ManifestUUID); got != wantRoot {
						t.Fatalf("root=%v want %v", got, wantRoot)
					}
					if !wantRoot && dst.store.format.StoreFormatVersion >= storeFormatVersionRepresentations {
						t.Fatal("CDC-only transfer raised store level")
					}
					if fail && (!fired || dst.store.rep.optionalFailures.Load() != 1) {
						t.Fatal("optional failure was not exercised")
					}
					if _, got, e := dst.store.GetObject("dst", "db"); e != nil || !bytes.Equal(got, data) {
						t.Fatalf("CDC reconstruction: %v", e)
					}
					if got := repReadAll(t, dst.store, man, byteRange{101, 5000}); !bytes.Equal(got, data[101:5001]) {
						t.Fatal("range differs")
					}
					repVerifyOK(t, dst.store, true)
				})
			}
		}
	}
}

func TestRepBundleBackfillFullDeltaAndReuse(t *testing.T) {
	base := repTestSQLite(32, 4096, 1404)
	newer := repTestSQLiteUpdate(base, 4096, []int{3, 17}, 1405)
	src, snaps := deltaChain(t, map[string][]byte{"db": base}, map[string][]byte{"db": newer})
	full, _ := bundleExportT(t, src, snaps[0], true)
	delta, _ := deltaExportT(t, src, snaps[0], snaps[1], true)
	for _, mode := range []repMode{repModeOff, repModeShadow, repModePrefer} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			// Re-importing an existing CDC-only snapshot must also backfill reused manifests.
			if _, err := importBundleFile(dir, full); err != nil {
				t.Fatal(err)
			}
			res, err := importBundleFileMode(dir, full, mode)
			if err != nil || !res.SnapshotReused || res.ManifestsReused == 0 {
				t.Fatalf("reused import: %+v %v", res, err)
			}
			if _, err := importBundleFileMode(dir, delta, mode); err != nil {
				t.Fatal(err)
			}
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			roots, err := s.repRootFiles()
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if mode == repModeOff {
				want = 0
			}
			if len(roots) != want {
				t.Fatalf("roots=%d want %d", len(roots), want)
			}
			if mode == repModeOff && s.format.StoreFormatVersion >= 6 {
				t.Fatal("off import raised store level")
			}
			repVerifyOK(t, s, true)
		})
	}
}

func TestRepTransferCrashAfterAuthoritativeCommit(t *testing.T) {
	data := repTestSQLite(24, 4096, 1406)
	srcDir, snap := bundleFixture(t, map[string][]byte{"db": data})
	full, _ := bundleExportT(t, srcDir, snap, true)
	points := []struct {
		hook string
		root bool
	}{
		{hookRepAnalysisStarted, false}, {hookRepComponentsFlushed, false},
		{hookRepObjectPublished, false}, {hookRepRootWritten, false},
		{hookRepRootAfterRename, true}, {hookRepRootPublished, true},
	}
	for _, bundle := range []bool{false, true} {
		for _, point := range points {
			t.Run(fmt.Sprintf("bundle=%v/%s", bundle, point.hook), func(t *testing.T) {
				dir := t.TempDir()
				s, err := OpenStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				s.setRepMode(repModePrefer)
				fired := false
				withTestHook(t, func(p string) {
					if p == point.hook {
						fired = true
						panic("transfer crash")
					}
				})
				if bundle {
					f, err := os.Open(full)
					if err != nil {
						t.Fatal(err)
					}
					func() {
						defer func() {
							if p := recover(); p != nil && p != "transfer crash" {
								panic(p)
							}
						}()
						s.importBundle(f)
					}()
					f.Close()
				} else {
					if err := s.CreateBucket("dst"); err != nil {
						t.Fatal(err)
					}
					creds := Credentials{AccessKeyID: defaultAccessKeyID, SecretAccessKey: defaultSecretAccessKey}
					srv := httptest.NewServer(NewServer(s, creds, defaultRegion))
					path := filepath.Join(t.TempDir(), "db")
					if err := os.WriteFile(path, data, 0600); err != nil {
						t.Fatal(err)
					}
					syncFile(syncClientConfig{Endpoint: srv.URL, Bucket: "dst", Key: "db", LocalPath: path, Creds: creds, Region: defaultRegion, HTTPClient: srv.Client()})
					srv.Close()
				}
				testHook = nil
				s.Close()
				if !fired {
					t.Fatal("crash hook was not exercised")
				}
				s, err = OpenStore(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer s.Close()
				roots, err := s.repRootFiles()
				if err != nil {
					t.Fatal(err)
				}
				if (len(roots) == 1) != point.root {
					t.Fatalf("published roots %d; want root=%v", len(roots), point.root)
				}
				if bundle {
					valid, issues := s.scanSnapshots()
					if len(valid) != 1 || len(issues) != 0 {
						t.Fatalf("authoritative snapshot lost: %v", issues)
					}
				} else if _, got, err := s.GetObject("dst", "db"); err != nil || !bytes.Equal(got, data) {
					t.Fatalf("authoritative object lost: %v", err)
				}
				repVerifyOK(t, s, true)
			})
		}
	}
}

func TestRepBundleBackfillFailureAndCLI(t *testing.T) {
	data := repTestSQLite(24, 4096, 1407)
	src, snap := bundleFixture(t, map[string][]byte{"db": data})
	full, _ := bundleExportT(t, src, snap, true)
	t.Run("optional failure", func(t *testing.T) {
		dir := t.TempDir()
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		s.setRepMode(repModePrefer)
		tmp := filepath.Join(dir, "tmp")
		fired := false
		withTestHook(t, func(p string) {
			if p == hookRepAnalysisStarted && !fired {
				fired = true
				if err := os.Rename(tmp, tmp+".blocked"); err != nil {
					panic(err)
				}
			}
		})
		f, err := os.Open(full)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		res, err := s.importBundle(f)
		testHook = nil
		if fired {
			if e := os.Rename(tmp+".blocked", tmp); e != nil {
				t.Fatal(e)
			}
		}
		if err != nil || res.SnapshotID != snap || !fired || s.rep.optionalFailures.Load() != 1 {
			t.Fatalf("import=%+v err=%v fired=%v", res, err, fired)
		}
		if s.format.StoreFormatVersion >= 6 {
			t.Fatal("failed analysis raised format")
		}
		valid, issues := s.scanSnapshots()
		if len(valid) != 1 || len(issues) != 0 {
			t.Fatalf("snapshot not durable: %v", issues)
		}
		repVerifyOK(t, s, true)
	})
	t.Run("CLI mode", func(t *testing.T) {
		bin := buildZeros3Binary(t)
		dir := t.TempDir()
		stdout, stderr, code := runZeros3CLI(t, bin, "bundle", "import", "-store", dir, "-in", full, "-representations", "shadow", "-json")
		if code != 0 {
			t.Fatalf("CLI import: %s %s", stdout, stderr)
		}
		s, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		roots, err := s.repRootFiles()
		if err != nil || len(roots) != 1 {
			t.Fatalf("CLI roots=%v err=%v", roots, err)
		}
		repVerifyOK(t, s, true)
	})
}
