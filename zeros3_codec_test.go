package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type repLabCodecFixture struct {
	Name, Class                     string
	Input, Root, LabRoot, LabRecipe []byte
	Objects                         map[string][]byte
}

// Lab builders emit the exact codec recipe and components. Only the existing
// Z3RP-v2/ZS3R-v1 envelope and derived-child addresses are translated; neither
// format nor the codec recipe changes. Production must decode those lab bytes.
func TestRepCodecLabWireFidelity(t *testing.T) {
	raw, err := os.ReadFile("testing-harnesses/testdata/i1b-codecs/lab-codecs.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 7 {
		t.Fatalf("fixture count %d", len(fixtures))
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			src := newRepMem()
			for text, data := range f.Objects {
				want, err := decodeHexSHA256(text)
				if err != nil {
					t.Fatal(err)
				}
				if got := src.put(data); got != want {
					t.Fatalf("component digest mismatch: %s", text)
				}
			}
			r, err := unmarshalRep(f.Root, repProdLimits)
			if err != nil || !bytes.Equal(r.Recipe, f.LabRecipe) {
				t.Fatalf("translated lab recipe: %v", err)
			}
			raw, err := r.marshal()
			if err != nil || !bytes.Equal(raw, f.Root) {
				t.Fatalf("production wire roundtrip: %v", err)
			}
			root := src.put(f.Root)
			env := &repEnv{Src: src, Limits: repProdLimits}
			var out bytes.Buffer
			n, sum, err := env.reconstruct(root, &out)
			if err != nil || n != int64(len(f.Input)) || sum != sha256.Sum256(f.Input) || !bytes.Equal(out.Bytes(), f.Input) {
				t.Fatalf("lab-wire decode n=%d err=%v", n, err)
			}
			reach, err := env.reachable(root)
			if err != nil {
				t.Fatal(err)
			}
			for id := range src.seen {
				if _, ok := reach.Role[id]; !ok {
					t.Fatalf("decoder read an unprotected component %x", id)
				}
			}
		})
	}
}

func repCodecFixtureBytes(t testing.TB) ([]byte, []byte) {
	t.Helper()
	plain, err := os.ReadFile("testing-harnesses/testdata/i1b-codecs/nested.tar")
	if err != nil {
		t.Fatal(err)
	}
	zst, err := os.ReadFile("testing-harnesses/testdata/i1b-codecs/nested.zst")
	if err != nil {
		t.Fatal(err)
	}
	return plain, zst
}
func repCodecGzip(t testing.TB, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := gzip.NewWriterLevel(&out, gzip.BestSpeed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestRepCodecNestedProduction(t *testing.T) {
	plain, zst := repCodecFixtureBytes(t)
	expected := map[string][]byte{}
	tr := tar.NewReader(bytes.NewReader(plain))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		expected[hdr.Name] = data
	}
	for _, fixture := range []struct {
		name string
		data []byte
		kind repKind
	}{{"gzip", repCodecGzip(t, plain), repKindGzip}, {"zstd", zst, repKindZstd}} {
		t.Run(fixture.name, func(t *testing.T) {
			l := newRepLab(t, repModePrefer)
			l.put("compressed", fixture.data)
			e, man := l.manifest("compressed")
			root := repMustRoot(t, l.s, e.manifestUUID)
			src := l.s.newRepSource(root.Descs, nil)
			defer src.close()
			env := &repEnv{Src: src, Limits: repProdLimits}
			call, err := env.open(root.Rep)
			if err != nil {
				t.Fatal(err)
			}
			if call.Rep.Kind != fixture.kind {
				t.Fatalf("kind=%d", call.Rep.Kind)
			}
			child, err := call.child(len(call.Rep.Members) - 1)
			if err != nil {
				t.Fatal(err)
			}
			if child.Rep.Kind != repKindConcat {
				t.Fatal("missing tar child")
			}
			var whole bytes.Buffer
			n, sum, err := env.reconstruct(root.Rep, &whole)
			if err != nil || n != int64(len(fixture.data)) || sum != sha256.Sum256(fixture.data) || !bytes.Equal(whole.Bytes(), fixture.data) {
				t.Fatalf("whole reconstruction: %v", err)
			}
			kinds := map[repKind]bool{}
			for i, m := range child.Rep.Members {
				if m.Role != repDerived {
					continue
				}
				nested, err := child.child(i)
				if err != nil {
					t.Fatal(err)
				}
				kinds[nested.Rep.Kind] = true
				var want []byte
				switch nested.Rep.Kind {
				case repKindSQLite:
					want = expected["data/app.db"]
				case repKindCAR:
					want = expected["data/blocks.car"]
				default:
					t.Fatalf("unexpected child %d", nested.Rep.Kind)
				}
				for _, count := range []int64{4096, 64 << 10} {
					rangeSrc := l.s.newRepSource(root.Descs, nil)
					rangeEnv := &repEnv{Src: rangeSrc, Limits: repProdLimits}
					outer, e := rangeEnv.open(root.Rep)
					if e != nil {
						t.Fatal(e)
					}
					tarCall, e := outer.child(len(outer.Rep.Members) - 1)
					if e != nil {
						t.Fatal(e)
					}
					nestedCall, e := tarCall.child(i)
					if e != nil {
						t.Fatal(e)
					}
					off := int64(4097)
					var out bytes.Buffer
					switch nestedCall.Rep.Kind {
					case repKindSQLite:
						err = sqliteReadRange(nestedCall, off, count, &out)
					case repKindCAR:
						err = carReadRange(nestedCall, off, count, &out)
					}
					if err != nil || !bytes.Equal(out.Bytes(), want[off:off+count]) {
						t.Fatalf("semantic child range: %v", err)
					}
					t.Logf("%s child=%d bytes=%d cold logical amplification=%.2fx", fixture.name, nestedCall.Rep.Kind, count, float64(rangeSrc.stats.LogicalBytes)/float64(count))
					rangeSrc.close()
				}
			}
			if !kinds[repKindSQLite] || !kinds[repKindCAR] {
				t.Fatalf("nested families absent: %v", kinds)
			}
			// A public S3 range remains a range of the compressed object's bytes.
			if got := repReadAll(t, l.s, man, byteRange{7, 1006}); !bytes.Equal(got, fixture.data[7:1007]) {
				t.Fatal("compressed byte range differs")
			}
			l.exact("nested codec")
			l.offline(func() {
				if _, err := compactStore(l.dir, repSmallCompact()); err != nil {
					t.Fatal(err)
				}
			})
			l.exact("after compact/restart")
		})
	}
}

func TestRepCodecUnsupportedFallsBack(t *testing.T) {
	plain, zst := repCodecFixtureBytes(t)
	gz := repCodecGzip(t, plain)
	badCRC := append([]byte(nil), gz...)
	badCRC[len(badCRC)-8] ^= 1
	cases := map[string][]byte{
		"multi-member gzip": append(append([]byte(nil), gz...), gz...),
		"gzip CRC":          badCRC, "truncated gzip": gz[:len(gz)-7],
		"truncated zstd": zst[:len(zst)-2], "magicless": zst[4:],
		"dictionary":         {0x28, 0xb5, 0x2f, 0xfd, 0x21, 1, 1, 9, 0, 0, 'x'},
		"window above 16MiB": {0x28, 0xb5, 0x2f, 0xfd, 0, 0x78, 9, 0, 0, 'x'},
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			l := newRepLab(t, repModePrefer)
			l.put("unsupported", data)
			_, man := l.manifest("unsupported")
			if repHasRoot(l.s, man.ManifestUUID) || l.s.format.StoreFormatVersion >= 6 {
				t.Fatal("unsupported input published optional state")
			}
			if got := repReadAll(t, l.s, man, byteRange{0, int64(len(data)) - 1}); !bytes.Equal(got, data) {
				t.Fatal("CDC fallback differs")
			}
			repVerifyOK(t, l.s, true)
		})
	}
}

func FuzzRepCodecDecode(f *testing.F) {
	raw, err := os.ReadFile("testing-harnesses/testdata/i1b-codecs/lab-codecs.json")
	if err != nil {
		f.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		f.Fatal(err)
	}
	for i, x := range fixtures {
		f.Add(x.Root, uint8(i))
	}
	f.Fuzz(func(t *testing.T, data []byte, pick uint8) {
		if len(data) > 1<<20 {
			return
		}
		fixture := fixtures[int(pick)%len(fixtures)]
		src := newRepMem()
		for _, object := range fixture.Objects {
			src.put(object)
		}
		root := src.put(data)
		env := &repEnv{Src: src, Limits: repLimits{MaxDepth: 4, MaxMembers: 4096, MaxRepBytes: 1 << 20, MaxWhole: 2 << 20, MaxObjects: 4096}}
		env.reconstruct(root, io.Discard)
		env.reachable(root)
	})
}

func TestRepCodecMissingChildFailsClosed(t *testing.T) {
	plain, zst := repCodecFixtureBytes(t)
	for name, data := range map[string][]byte{"gzip": repCodecGzip(t, plain), "zstd": zst} {
		t.Run(name, func(t *testing.T) {
			l := newRepLab(t, repModePrefer)
			l.put("obj", data)
			_, man := l.manifest("obj")
			root := repMustRoot(t, l.s, man.ManifestUUID)
			src := l.s.newRepSource(root.Descs, nil)
			call, err := (&repEnv{Src: src, Limits: repProdLimits}).open(root.Rep)
			src.close()
			if err != nil {
				t.Fatal(err)
			}
			child := call.Rep.Members[len(call.Rep.Members)-1].Child
			if err := os.Remove(l.s.chunkPath(child)); err != nil {
				t.Fatal(err)
			}
			if got := repReadAll(t, l.s, man, byteRange{0, int64(len(data)) - 1}); !bytes.Equal(got, data) {
				t.Fatal("CDC authority lost")
			}
			rr, err := l.s.computeReachability(false)
			if err != nil || rr.OK() || rr.RepFaults[man.ManifestUUID] == "" {
				t.Fatalf("missing child not fail-closed: %v", err)
			}
			l.close()
			if _, err := gcCollect(l.dir, true); !errors.Is(err, errGCUnsafe) {
				t.Fatalf("GC after losing codec child: %v", err)
			}
		})
	}
}

func TestRepCodecCrashAndOptionalFailure(t *testing.T) {
	plain, zst := repCodecFixtureBytes(t)
	for name, data := range map[string][]byte{"gzip": repCodecGzip(t, plain), "zstd": zst} {
		for _, point := range []string{hookRepAnalysisStarted, hookRepComponentsFlushed, hookRepObjectPublished, hookRepRootWritten, hookRepRootAfterRename, hookRepRootPublished} {
			t.Run(name+"/"+point, func(t *testing.T) {
				l := newRepLab(t, repModePrefer)
				l.put("keep", []byte("authoritative existing object"))
				repCrashPut(t, l, "crash", data, point, 1)
				if _, _, err := l.s.HeadObject("b", "crash"); err == nil {
					t.Fatal("crashed PUT became visible before namespace commit")
				}
				repVerifyOK(t, l.s, true)
				l.offline(func() {
					if _, err := gcCollect(l.dir, true); err != nil {
						t.Fatal(err)
					}
				})
				if roots := repRootFilesOf(t, l); len(roots) != 0 {
					t.Fatalf("orphan roots survived GC: %v", roots)
				}
			})
		}
		t.Run(name+"/optional failure", func(t *testing.T) {
			l := newRepLab(t, repModePrefer)
			tmp := filepath.Join(l.dir, "tmp")
			fired := false
			withTestHook(t, func(p string) {
				if p == hookRepAnalysisStarted && !fired {
					fired = true
					if err := os.Rename(tmp, tmp+".blocked"); err != nil {
						panic(err)
					}
				}
			})
			l.put("obj", data)
			testHook = nil
			if fired {
				if err := os.Rename(tmp+".blocked", tmp); err != nil {
					t.Fatal(err)
				}
			}
			if !fired || l.s.rep.optionalFailures.Load() != 1 {
				t.Fatal("optional failure not exercised")
			}
			l.exact("CDC-only after optional failure")
		})
	}
}

func TestRepCodecAnalysisDepth(t *testing.T) {
	data := bytes.Repeat([]byte("a bounded nested codec workload"), 1000)
	for i := 0; i < 5; i++ {
		data = repCodecGzip(t, data)
	}
	l := newRepLab(t, repModePrefer)
	l.put("nested", data)
	_, man := l.manifest("nested")
	root := repMustRoot(t, l.s, man.ManifestUUID)
	src := l.s.newRepSource(root.Descs, nil)
	defer src.close()
	env := &repEnv{Src: src, Limits: repProdLimits}
	reach, err := env.reachable(root.Rep)
	if err != nil {
		t.Fatal(err)
	}
	if reach.Height[root.Rep] > 3 {
		t.Fatalf("analysis exceeded default depth: %d", reach.Height[root.Rep])
	}
	l.exact("bounded nesting")
}

func FuzzRepCodecAnalysis(f *testing.F) {
	raw, err := os.ReadFile("testing-harnesses/testdata/i1b-codecs/lab-codecs.json")
	if err != nil {
		f.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		f.Fatal(err)
	}
	for _, x := range fixtures {
		if len(x.Input) <= 64<<10 {
			f.Add(x.Input)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		l := newRepLab(t, repModePrefer)
		l.put("candidate", data)
		_, man := l.manifest("candidate")
		if len(data) > 0 {
			if got := repReadAll(t, l.s, man, byteRange{0, int64(len(data)) - 1}); !bytes.Equal(got, data) {
				t.Fatal("analysis changed authoritative bytes")
			}
		}
		repVerifyOK(t, l.s, true)
	})
}

// This opt-in measurement writes/reads streams, not a materialized 64 MiB input.
// Run the compiled test binary separately to measure RSS without compiler memory.
func TestRepCodecLargeStreaming(t *testing.T) {
	if os.Getenv("ZEROS3_Z3_I1B_MEASURE") == "" {
		t.Skip("set ZEROS3_Z3_I1B_MEASURE=1")
	}
	for _, mode := range []struct {
		name  string
		level int
	}{{"best-speed", gzip.BestSpeed}, {"stored", gzip.NoCompression}} {
		t.Run(mode.name, func(t *testing.T) {
			const plainSize = 64 << 20
			compressed, err := os.Create(filepath.Join(t.TempDir(), "large.gz"))
			if err != nil {
				t.Fatal(err)
			}
			defer compressed.Close()
			gz, err := gzip.NewWriterLevel(compressed, mode.level)
			if err != nil {
				t.Fatal(err)
			}
			block := repTestSQLite(2, 4096, 1411)
			rng := rand.New(rand.NewSource(1412))
			for at := 0; at < plainSize; at += len(block) {
				if at != 0 {
					block[0] = 0x0d
				}
				rng.Read(block[1024:2048])
				binary.LittleEndian.PutUint64(block[len(block)-8:], uint64(at))
				if _, err := gz.Write(block); err != nil {
					t.Fatal(err)
				}
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			info, err := compressed.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := compressed.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			l := newRepLab(t, repModePrefer)
			start := time.Now()
			ing, err := l.s.ingestStream(compressed, true, ingestOptions{size: info.Size()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := l.s.commitIngested("b", "large.gz", ing, "application/gzip", nil, putCondition{}); err != nil {
				t.Fatal(err)
			}
			_, man := l.manifest("large.gz")
			if !repHasRoot(l.s, man.ManifestUUID) {
				err := l.s.repBuild(man)
				if mode.level != gzip.BestSpeed || !errors.Is(err, errRepUnsupported) || !strings.Contains(err.Error(), "predictor work budget") {
					t.Fatalf("unexpected large analysis rejection: %v", err)
				}
				// Keep this producer as an explicit negative control: its unit exceeds
				// the lab predictor budget. CDC still verifies the complete object.
				repVerifyOK(t, l.s, true)
				t.Logf("SYNTHETIC_FIXTURE unsupported compressed=%d reason=%v", info.Size(), err)
				return
			}
			root := repMustRoot(t, l.s, man.ManifestUUID)
			src := l.s.newRepSource(root.Descs, nil)
			defer src.close()
			env := &repEnv{Src: src, Limits: repProdLimits}
			n, sum, err := env.reconstruct(root.Rep, io.Discard)
			if err != nil || n != info.Size() || sum != ing.objSHA256 {
				t.Fatalf("stream proof: %v", err)
			}
			rr, err := l.s.computeReachability(false)
			if err != nil || !rr.OK() {
				t.Fatalf("large reachability: %v", err)
			}
			t.Logf("SYNTHETIC_FIXTURE plain=%d compressed=%d components=%d elapsed=%s", plainSize, info.Size(), rr.RepComponents, time.Since(start))
		})
	}

}

func TestRepCodecConcurrentFirstUse(t *testing.T) {
	raw, err := os.ReadFile("testing-harnesses/testdata/i1b-codecs/lab-codecs.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []repLabCodecFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	var f repLabCodecFixture
	for _, x := range fixtures {
		if x.Name == "ref155_zeros.zst" {
			f = x
		}
	}
	if len(f.Root) == 0 {
		t.Fatal("missing concurrent fixture")
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			src := newRepMem()
			for _, object := range f.Objects {
				src.put(object)
			}
			root := src.put(f.Root)
			env := &repEnv{Src: src, Limits: repProdLimits}
			var out bytes.Buffer
			if n, sum, err := env.reconstruct(root, &out); err != nil || n != int64(len(f.Input)) || sum != sha256.Sum256(f.Input) || !bytes.Equal(out.Bytes(), f.Input) {
				errs <- fmt.Errorf("concurrent decode n=%d: %v", n, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestRepCodecEconomics(t *testing.T) {
	if os.Getenv("ZEROS3_Z3_I1B_MEASURE") == "" {
		t.Skip("set ZEROS3_Z3_I1B_MEASURE=1")
	}
	plain, _ := repCodecFixtureBytes(t)
	files := map[string][]byte{}
	var names []string
	tr := tar.NewReader(bytes.NewReader(plain))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[h.Name] = data
		names = append(names, h.Name)
	}
	for _, codec := range []string{"gzip", "zstd"} {
		t.Run(codec, func(t *testing.T) {
			off, on := newRepLab(t, repModeOff), newRepLab(t, repModePrefer)
			var last []byte
			for rev := 0; rev < 4; rev++ {
				changed := map[string][]byte{}
				for k, v := range files {
					changed[k] = v
				}
				changed["data/app.db"] = repTestSQLiteUpdate(files["data/app.db"], 4096, []int{3, 17}, int64(1420+rev))
				tarBytes := repTestTar(t, changed, names)
				var encoded []byte
				if codec == "gzip" {
					encoded = repCodecGzip(t, tarBytes)
				} else {
					cmd := exec.Command("zstd", "-q", "-c", "-3", "--no-progress")
					cmd.Stdin = bytes.NewReader(tarBytes)
					var err error
					encoded, err = cmd.Output()
					if err != nil {
						t.Fatalf("requested producer measurement requires zstd: %v", err)
					}
				}
				key := fmt.Sprintf("rev%d", rev)
				off.put(key, encoded)
				on.put(key, encoded)
				_, man := on.manifest(key)
				if !repHasRoot(on.s, man.ManifestUUID) {
					t.Fatalf("measured history unsupported at %s", key)
				}
				a, b := repStoreBytes(t, off.s), repStoreBytes(t, on.s)
				t.Logf("SYNTHETIC_FIXTURE codec=%s rev=%d object=%d CDC=%d CDC_plus_rep=%d rep_chunks=%d root_bytes=%d", codec, rev, len(encoded), a.ChunkStoreFileBytes, b.ChunkStoreFileBytes, b.RepresentationReachableChunkCount, b.RepresentationMetadataFileBytes)
				last = encoded
			}
			on.offline(func() {
				if _, err := compactStore(on.dir, repSmallCompact()); err != nil {
					t.Fatal(err)
				}
			})
			_, man := on.manifest("rev3")
			root := repMustRoot(t, on.s, man.ManifestUUID)
			var cdcTimes, repTimes []time.Duration
			for i := 0; i < 5; i++ {
				start := time.Now()
				got := repReadAll(t, on.s, man, byteRange{0, int64(len(last)) - 1})
				cdcTimes = append(cdcTimes, time.Since(start))
				if !bytes.Equal(got, last) {
					t.Fatal("CDC whole differs")
				}
				src := on.s.newRepSource(root.Descs, nil)
				env := &repEnv{Src: src, Limits: repProdLimits}
				start = time.Now()
				n, sum, err := env.reconstruct(root.Rep, io.Discard)
				repTimes = append(repTimes, time.Since(start))
				src.close()
				if err != nil || n != int64(len(last)) || sum != sha256.Sum256(last) {
					t.Fatalf("direct whole: %v", err)
				}
			}
			slices.Sort(cdcTimes)
			slices.Sort(repTimes)
			t.Logf("codec=%s packed_whole_CDC_median=%s direct_rep_median=%s", codec, cdcTimes[2], repTimes[2])
		})
	}
}
