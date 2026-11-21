package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
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
	g1CDCSpecific(t, "hook")
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
	g1CDCSpecific(t, "hook")
	useLooseLayout(t, "fixture mixes loose and packed chunks / counts packs, so it needs a loose starting layout (production-layout pack behaviour: TestLayoutMatrix_* and the direct-pack tests)")
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
	g1CDCSpecific(t, "cdcref")
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

func TestRepCodecMetadataCountsBounded(t *testing.T) {
	for _, kind := range []repKind{repKindGzip, repKindZstd} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			src := newRepMem()
			// An otherwise physically present table with many empty inline units must
			// fail before allocating one Go record per claimed unit.
			table := binary.AppendUvarint(nil, 0)
			table = binary.AppendUvarint(table, (1<<16)+1)
			table = append(table, make([]byte, (1<<16)+1)...)
			id := src.put(table)
			r := &repObject{Kind: kind, Members: []repMember{{Role: repStored, SHA: id, Size: int64(len(table))}, {Role: repDerived}}}
			if kind == repKindGzip {
				r.Recipe = binary.AppendUvarint(nil, 10)
				r.Recipe = append(r.Recipe, []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 255}...)
				r.Recipe = append(r.Recipe, make([]byte, 8)...)
				r.Recipe = binary.AppendUvarint(r.Recipe, 1)
			} else {
				r.Recipe = []byte{1}
			}
			env := &repEnv{Src: src, Limits: repProdLimits}
			call := env.call([32]byte{}, r, 1, nil, io.Discard)
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			d, _ := repDecoderFor(kind)
			err := d.Decode(call)
			runtime.ReadMemStats(&after)
			if err == nil {
				t.Fatal("oversized unit count accepted")
			}
			if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
				t.Fatalf("unit count allocated %d bytes", allocated)
			}
		})
	}
}

// ---- Z4-P7 exact replay: the production Brotli and LZ4 against pinned producer output ----------------------------
//
// The vectors are the research module's (testing-harnesses/z3/{brotliz3,lz4z3}/testdata): SHA-256 of
// google/brotli 1.2.0 output for deterministic inputs, a pair of Arrow pages, and liblz4 1.9.4 / python-lz4
// / lz4 CLI blocks and frames. Nothing here links the research packages.

func hardBrotliInput(kind string, n int, r *rand.Rand) []byte {
	out := make([]byte, 0, n+64)
	words := []string{"alpha", "bravo", "charlie", "delta", "the ", "and ", "<div>", "</div>", "\n", " ", "http://", "value", "information", "available"}
	switch kind {
	case "text":
		for len(out) < n {
			out = append(out, words[r.Intn(len(words))]...)
			if r.Intn(3) == 0 {
				out = append(out, fmt.Sprintf("%04x", r.Intn(65536))...)
			}
		}
	case "rand":
		out = out[:n]
		r.Read(out)
	case "zeros":
		out = out[:n]
	case "ints":
		for i := 0; len(out) < n; i++ {
			out = binary.LittleEndian.AppendUint64(out, uint64(i*1000+r.Intn(3)))
		}
	case "floats":
		for len(out) < n {
			out = binary.LittleEndian.AppendUint64(out, math.Float64bits(r.NormFloat64()))
		}
	case "low":
		for len(out) < n {
			out = append(out, "abcd"[r.Intn(4)])
		}
	case "mixed":
		kinds := []string{"text", "rand", "zeros", "ints", "low"}
		for len(out) < n {
			out = append(out, hardBrotliInput(kinds[r.Intn(len(kinds))], []int{100, 3000, 70000}[r.Intn(3)], r)...)
		}
	}
	return out[:n]
}

func TestHardBrotliMatchesPinnedProducerOutput(t *testing.T) {
	if err := hcBrotliEnsureDict(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testing-harnesses/z3/brotliz3/testdata/golden.txt")
	if err != nil {
		t.Fatal(err)
	}
	golden := map[string]string{}
	for _, l := range strings.Split(string(raw), "\n") {
		if p := strings.Fields(l); len(p) == 2 {
			golden[p[0]] = p[1]
		}
	}
	params := [][2]int{{8, 22}, {9, 22}, {8, 18}, {8, 24}, {5, 22}, {6, 20}, {7, 17}}
	r := rand.New(rand.NewSource(11))
	checked := 0
	for _, k := range []string{"text", "rand", "zeros", "ints", "floats", "low", "mixed"} {
		for _, n := range []int{1, 2, 3, 10, 63, 64, 65, 300, 4096, 65535, 65536, 65537, 200000, 1048575, 1048576, 1300000} {
			data := hardBrotliInput(k, n, r)
			for _, p := range params {
				if n > 300000 && p != params[0] && p != params[1] || p[1] != 22 && n%65536 == 0 || testing.Short() && n > 300000 {
					continue
				}
				name := fmt.Sprintf("%s-%d/q%d/w%d", k, n, p[0], p[1])
				got, err := hcBrotliz3Encode(data, p[0], p[1])
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if sum := sha256.Sum256(got); hex.EncodeToString(sum[:]) != golden[name] {
					t.Errorf("%s differs from the pinned google/brotli output", name)
				}
				if dec, err := hcBrotliz3Decode(got, len(data)); err != nil || !bytes.Equal(dec, data) {
					t.Fatalf("%s: own output does not decode: %v", name, err)
				}
				checked++
			}
		}
	}
	if checked < 300 {
		t.Fatalf("only %d vectors compared", checked)
	}
	for _, name := range []string{"arrow_page_small", "arrow_page_mid"} {
		pay, err := os.ReadFile("testing-harnesses/z3/brotliz3/testdata/" + name + ".br")
		if err != nil {
			t.Fatal(err)
		}
		body, err := hcBrotliz3Decode(pay, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := hcBrotliz3Encode(body, 8, 22); err != nil || !bytes.Equal(got, pay) {
			t.Fatalf("%s: re-encoding differs from the Arrow page (%v)", name, err)
		}
	}
	for _, p := range [][2]int{{4, 22}, {10, 22}, {11, 22}, {8, 16}, {8, 25}, {-1, 22}} {
		if _, err := hcBrotliz3Encode([]byte("x"), p[0], p[1]); err == nil {
			t.Fatalf("quality %d window %d accepted: outside the ported encoder path", p[0], p[1])
		}
	}
}

func TestHardLZ4MatchesPinnedProducerOutput(t *testing.T) {
	raw, err := os.ReadFile("testing-harnesses/z3/lz4z3/testdata/MANIFEST.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		File, SHA, Predictor string
		Decoded, Accel       int
	}
	var m []map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, e := range m {
		f := struct {
			File, SHA, Predictor string
			Decoded, Accel       int
		}{File: e["file"].(string), SHA: fmt.Sprint(e["sha256_decoded"]), Predictor: fmt.Sprint(e["predictor"])}
		if v, ok := e["decoded"].(float64); ok {
			f.Decoded = int(v)
		}
		if v, ok := e["accel"].(float64); ok {
			f.Accel = int(v)
		}
		fixtures = append(fixtures, f)
	}
	pred := map[string]int{"default": hcLz4z3PredDefault, "default5": hcLz4z3PredDefault5, "continue": hcLz4z3PredContinue, "hc": hcLz4z3PredHC}
	blocks, frames := 0, 0
	for _, f := range fixtures {
		data, err := os.ReadFile("testing-harnesses/z3/lz4z3/testdata/" + f.File)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(f.File, "py_") { // raw blocks from the C library: the registered predictor reproduces them
			plain, seqs, err := hcLz4z3Decode(data, f.Decoded, hcLz4z3Limits{})
			if err != nil {
				t.Fatal(f.File, err)
			}
			if h := sha256.Sum256(plain); hex.EncodeToString(h[:]) != f.SHA {
				t.Fatalf("%s: decoded SHA mismatch", f.File)
			}
			if got, ok := hcLz4z3PredictBlock(pred[f.Predictor], plain, f.Accel); !ok || !bytes.Equal(got, data) {
				t.Fatalf("%s: predictor %s/%d does not reproduce the library block", f.File, f.Predictor, f.Accel)
			}
			if re, err := hcLz4z3Build(plain, seqs); err != nil || !bytes.Equal(re, data) {
				t.Fatalf("%s: explicit rebuild differs: %v", f.File, err)
			}
			blocks++
			continue
		}
	}
	// frames (lz4 CLI 1.9.4, linked and independent, fast and HC): every block is predicted, hybrid or
	// explicit, and the whole stream replays byte for byte
	files, _ := filepath.Glob("testing-harnesses/z3/lz4z3/testdata/*.lz4")
	for _, path := range files {
		if strings.HasPrefix(filepath.Base(path), "py_") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		plain, rec, st, err := hcLz4z3AnalyzeFrames(data, hcLz4z3Limits{})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var out bytes.Buffer
		if err := hcLz4z3ReplayFrames(&out, rec, bytes.NewReader(plain), hcLz4z3Limits{}); err != nil || !bytes.Equal(out.Bytes(), data) {
			t.Fatalf("%s: frame replay differs (%v)", path, err)
		}
		for _, f := range fixtures {
			if f.File == filepath.Base(path) && f.SHA != "" && f.SHA != "<nil>" {
				if h := sha256.Sum256(plain); hex.EncodeToString(h[:]) != f.SHA {
					t.Fatalf("%s: payload SHA mismatch", path)
				}
			}
		}
		t.Logf("%-28s blocks predicted/hybrid/explicit = %d/%d/%d", filepath.Base(path), st.Predicted, st.Hybrid, st.Explicit)
		frames++
	}
	if blocks < 4 || frames < 8 {
		t.Fatalf("fixtures covered: %d blocks, %d frames", blocks, frames)
	}
}

// Streaming-API producers (Z4-C1/T1): python-brotli's Compressor and Arrow's streaming compressor with chunked
// input, flushes, an explicit block size and a wrapping ring. The page analyzer must give each stream a recipe
// member that regenerates it, and the recipe must survive its wire form.
func TestHardBrotliStreamingProducers(t *testing.T) {
	raw, err := os.ReadFile("testing-harnesses/z3/brotliz3/testdata/streams/MANIFEST.json")
	if err != nil {
		t.Fatal(err)
	}
	var man []struct {
		File, Producer, Pattern string
		Params                  map[string]int
		Sha256                  string
		Decoded_size            int
		Decoded_sha256          string
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	lim := repStructuredParquetLimits()
	lim.Hard = true
	for _, m := range man {
		pay, err := os.ReadFile("testing-harnesses/z3/brotliz3/testdata/streams/" + m.File)
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256(pay); hex.EncodeToString(sum[:]) != m.Sha256 {
			t.Fatalf("%s: fixture damaged", m.File)
		}
		c := repPQNewCompressor(repPQCodecBrotli, lim)
		body, rec, err := c.Analyze(pay, int64(m.Decoded_size))
		if err != nil {
			t.Errorf("%s (%s, %s): %v", m.File, m.Producer, m.Pattern, err)
			continue
		}
		if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != m.Decoded_sha256 {
			t.Fatalf("%s: the body is not the decoded stream", m.File)
		}
		if rec.ID != repPQCompBrotli5+uint8(m.Params["quality"])-5 && (rec.ID != repPQCompLZ4Rec || len(rec.LZ4) < 4 || rec.LZ4[0] != repPQbrStreamTag) {
			t.Errorf("%s: unexpected compression record %+v", m.File, rec)
			continue
		}
		again, err := repPQNewCompressor(repPQCodecBrotli, lim).Replay(rec, body)
		if err != nil || !bytes.Equal(again, pay) {
			t.Fatalf("%s: replay differs (%v)", m.File, err)
		}
		// a damaged member must be refused, never replayed into other bytes
		if rec.ID == repPQCompLZ4Rec {
			for _, bad := range [][]byte{rec.LZ4[:2], append(append([]byte(nil), rec.LZ4...), 7), append([]byte{repPQbrStreamTag, 0xff, 0xff}, rec.LZ4[3:]...)} {
				if out, err := repPQNewCompressor(repPQCodecBrotli, lim).Replay(repPQCompRec{ID: repPQCompLZ4Rec, LZ4: bad}, body); err == nil && bytes.Equal(out, pay) {
					t.Errorf("%s: a damaged recipe member replayed to the original", m.File)
				}
			}
		}
		t.Logf("%-24s %8d bytes -> id %d, member %d bytes", m.File, len(pay), rec.ID, len(rec.LZ4))
	}
}

// Parallel page replay runs encoders side by side: they share no mutable state (the entropy tables are built once and
// read-only), so concurrent encodes of different inputs give exactly the serial bytes. Run with -race.
func TestHardBrotliConcurrentEncodersAreIndependent(t *testing.T) {
	if err := hcBrotliEnsureDict(); err != nil {
		t.Fatal(err)
	}
	r := rand.New(rand.NewSource(21))
	var inputs [][]byte
	for _, k := range []string{"text", "mixed", "ints", "low", "floats"} {
		inputs = append(inputs, hardBrotliInput(k, 150000+r.Intn(100000), r))
	}
	want := make([][]byte, len(inputs))
	for i, in := range inputs {
		want[i], _ = hcBrotliz3Encode(in, 5+i%5, 22)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := 0; it < 6; it++ {
				i := (g + it) % len(inputs)
				got, err := hcBrotliz3Encode(inputs[i], 5+i%5, 22)
				if err != nil || !bytes.Equal(got, want[i]) {
					t.Errorf("goroutine %d input %d: concurrent encode differs from the serial one (%v)", g, i, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestAuthorityOptInCodecReplay(t *testing.T) {
	plain, zstd := repCodecFixtureBytes(t)
	gz := repCodecGzip(t, plain)
	for _, tc := range []struct {
		name    string
		enabled bool
		tier    string
		admit   int
	}{
		{name: "default"},
		{name: "hot", enabled: true, tier: "hot"},
		{name: "archive", enabled: true, admit: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newAuthLab(t)
			l.put("gzip", gz)
			l.put("zstd", zstd)
			report := l.migrate(AuthPolicy{
				Strategy: "forced", MinSaving: -10, AllowRep: true, NoZN7: true,
				Tier: tc.tier, CodecReplay: tc.enabled,
			})
			admitted := 0
			for _, plan := range report.Plans {
				if plan.Form == "rep" {
					admitted++
				}
			}
			if admitted != tc.admit {
				t.Fatalf("admitted %d, want %d, plans %+v", admitted, tc.admit, report.Plans)
			}
			if admitted == 0 {
				return
			}
			l.reclaim()
			l.exact("codec group after reclaim")
			for key, want := range l.want {
				const off, n = 32768, 4096
				if got := l.read(key, byteRange{off, off + n - 1}); !bytes.Equal(got, want[off:off+n]) {
					t.Fatalf("%s range differs", key)
				}
				// a ranged authority context (a delta's base, an executor interval): this plaintext holds nested codec
				// members, so the body materializes
				sha := sha256.Sum256(want)
				if rec, _ := l.s.authLoad(sha); rec != nil && rec.Form == authFormRep {
					ctx := l.s.newAuthCtx()
					body, _, err := ctx.body(sha, 0, 1)
					mustOK(t, err)
					r := rand.New(rand.NewSource(int64(len(want))))
					for i := 0; i < 24; i++ {
						o := r.Int63n(int64(len(want)))
						p := make([]byte, min(1+r.Int63n(20000), int64(len(want))-o))
						if k, err := body.ReadAt(p, o); k != len(p) || !bytes.Equal(p, want[o:o+int64(len(p))]) {
							t.Fatalf("%s: ranged body read %d+%d: %v", key, o, len(p), err)
						}
					}
				}
			}
			l.verifyOK("codec group after reclaim")
		})
	}
}

func TestRepCodecTemporaryWritesRespectDiskReserve(t *testing.T) {
	l := newAuthLab(t)
	f, err := os.CreateTemp(t.TempDir(), "codec")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := repCodecTempWriter{store: l.s, file: f, reserve: math.MaxUint64}
	if n, err := w.Write([]byte("temporary bytes")); n != 0 || !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("wrote %d bytes, err %v", n, err)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("temporary file grew to %d bytes", info.Size())
	}
}

// seekFixtures are the producers the seekable codec path is checked against: zlib gzip (predicted headers, stored
// blocks), Go's flate, libzstd at both ends of its level range, and the multi-frame and repeat-table zstd vectors.
func seekFixtures(t *testing.T) map[string][]byte {
	files := map[string][]byte{}
	for _, f := range []string{"b2-seek/text.l6.gz", "b2-seek/text.l9.gz", "b2-seek/text.l3.zst", "b2-seek/text.l19.zst",
		"../z3/zstdz3/testdata/skip_concat.zst", "../z3/zstdz3/testdata/fx_repeat_tables.zst", "../z3/zstdz3/testdata/ref157_json.zst"} {
		b, err := os.ReadFile(filepath.Join("testing-harnesses/testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		files[filepath.Base(f)] = b
	}
	plain, nested := repCodecFixtureBytes(t)
	files["go-speed.gz"], files["nested.zst"] = repCodecGzip(t, plain), nested
	return files
}

// seekRestarts lists the stream offsets a seeker restarts at.
func seekRestarts(sk repSeeker) (offs []int64) {
	switch z := sk.(type) {
	case *gzSeek:
		for _, b := range z.bits {
			offs = append(offs, int64(len(z.g.header))+b/8)
		}
	case *zsSeek:
		for _, sg := range z.segs {
			offs = append(offs, sg.off)
		}
	}
	return offs
}

func seekRead(t *testing.T, s *Store, root *repRoot, off, n int64) ([]byte, error) {
	rd, err := s.repOpenRootReader(root, byteRange{off, off + n - 1})
	if err != nil || rd == nil {
		return nil, fmt.Errorf("no reader (%v)", err)
	}
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

func seekOf(t *testing.T, s *Store, root *repRoot) repSeeker {
	src := s.newRepSource(root.Descs, nil)
	defer src.close()
	call, err := (&repEnv{Src: src, Limits: repProdLimits}).open(root.Rep)
	mustOK(t, err)
	sk, err := repSeekerOf(call)
	mustOK(t, err)
	return sk
}

// A seek-indexed codec representation serves any range from the restart point before it, byte-identical to the
// original stream: ranges starting at, before and after every restart point, crossing them, and the whole object.
func TestRepCodecSeekableRanges(t *testing.T) {
	t.Run("sequential", func(t *testing.T) { seekableRanges(t) })
	defer func() { repSeekMinPiece = 0 }()
	repSeekMinPiece = 8 << 10
	t.Run("parallel", func(t *testing.T) { seekableRanges(t) })
}

func seekableRanges(t *testing.T) {
	l := newAuthLab(t)
	for name, data := range seekFixtures(t) {
		t.Run(name, func(t *testing.T) {
			l.put(name, data)
			root, _, err := l.s.repConstructAt(l.man(name), nil, nil, 0, true)
			mustOK(t, err)
			sk := seekOf(t, l.s, root)
			size := int64(len(data))
			if name == "go-speed.gz" || name == "nested.zst" {
				// the plaintext holds nested codec members: no plaintext range reader, so ranges replay the stream
				if sk != nil {
					t.Fatal("a container whose plaintext cannot be read by range is seekable")
				}
				if rd, err := l.s.repOpenRootReader(root, byteRange{size / 2, size/2 + 4095}); rd != nil || err != nil {
					t.Fatalf("native range reader without a seeker: %v", err)
				}
				return
			}
			if sk == nil {
				t.Fatal("no seek index")
			}
			offs := []int64{0, size - 1, size / 2}
			for _, b := range seekRestarts(sk) {
				offs = append(offs, b-1, b, b+1, b+2)
			}
			for o := int64(0); o < size; o += 4093 {
				offs = append(offs, o)
			}
			for i, off := range offs {
				if off < 0 || off >= size {
					continue
				}
				n := min([]int64{1, 9, 4096, 70000, 300000}[i%5], size-off)
				got, err := seekRead(t, l.s, root, off, n)
				if err != nil || !bytes.Equal(got, data[off:off+n]) {
					t.Fatalf("range %d+%d: %v (%d bytes)", off, n, err, len(got))
				}
			}
			if got, err := seekRead(t, l.s, root, 0, size); err != nil || !bytes.Equal(got, data) {
				t.Fatalf("whole: %v", err)
			}
			// a cold 4 KiB read replays a few units, not the stream
			var tr repCodecTrace
			repSourceObserver = func(_ repReadStats, cw repCodecTrace) { tr.Units += cw.Units; tr.PlainBytes += cw.PlainBytes }
			_, err = seekRead(t, l.s, root, size/2, min(4096, size-size/2))
			repSourceObserver = nil
			mustOK(t, err)
			if size > 1<<18 && (tr.Units > 16 || tr.PlainBytes > 3<<20) {
				t.Fatalf("a 4 KiB read decoded %d units and %d plaintext bytes", tr.Units, tr.PlainBytes)
			}
		})
	}
}

// The seek index is part of what a representation's proof establishes: an index that disagrees with the stream
// fails the whole decode, and a range crossing the disagreement fails instead of serving other bytes. Damaged
// units and plaintext chunks fail the ranges that need them.
func TestRepCodecSeekIndexIntegrity(t *testing.T) {
	files := seekFixtures(t)
	for _, name := range []string{"text.l9.gz", "text.l3.zst"} {
		t.Run(name, func(t *testing.T) {
			l := newAuthLab(t)
			data := files[name]
			l.put(name, data)
			man := l.man(name)
			root, _, err := l.s.repConstructAt(man, nil, nil, 0, true)
			mustOK(t, err)
			src := l.s.newRepSource(root.Descs, nil)
			env := &repEnv{Src: src, Limits: repProdLimits}
			call, err := env.open(root.Rep)
			mustOK(t, err)
			sk, err := repSeekerOf(call)
			mustOK(t, err)
			src.close()
			restarts := seekRestarts(sk)
			mid := len(restarts) / 2
			// forge: one index entry moves by a byte (and so, for gzip, every later unit)
			forged := *call.Rep
			var seek []uint64
			if call.Rep.Kind == repKindGzip {
				g, _ := gzipRecipe(call.Rep)
				seek = slices.Clone(g.seek)
				seek[mid] += 8
				forged.Recipe = repSeekAppend(call.Rep.Recipe[:len(call.Rep.Recipe)-len(repSeekAppend(nil, g.seek))], seek)
			} else {
				_, sizes, _ := zstdRecipe(call.Rep)
				seek = slices.Clone(sizes)
				seek[len(seek)/2]++
				seek[len(seek)/2+1]--
				forged.Recipe = repSeekAppend(call.Rep.Recipe[:len(call.Rep.Recipe)-len(repSeekAppend(nil, sizes))], seek)
			}
			b := l.s.newRepBuilder(man.TotalLength, nil, nil)
			defer b.close()
			for id, d := range root.Descs {
				b.descs[id] = d
			}
			id, err := b.finishRep(&forged)
			mustOK(t, err)
			mustOK(t, b.flush())
			if err := b.proof(id, man.TotalLength, root.ObjSHA); err == nil {
				t.Fatal("a representation with a forged seek index passed its proof")
			}
			bad := &repRoot{ManifestUUID: root.ManifestUUID, ObjSHA: root.ObjSHA, ObjSize: root.ObjSize, Rep: id, Descs: maps.Clone(root.Descs)}
			bad.Descs[id] = b.src.desc(id)
			if got, err := seekRead(t, l.s, bad, 0, int64(len(data))); err == nil && bytes.Equal(got, data) {
				t.Fatal("whole read of the forged representation succeeded")
			}
			lo, hi := restarts[max(0, mid-2)], restarts[min(len(restarts)-1, mid+3)]
			if got, err := seekRead(t, l.s, bad, lo, hi-lo); err == nil {
				t.Fatalf("a range crossing the forged entry served %d bytes (exact: %v)", len(got), bytes.Equal(got, data[lo:hi]))
			}
			// damage: one unit chunk and one plaintext chunk
			reach, err := env.reachable(root.Rep)
			mustOK(t, err)
			var victims [][32]byte
			for _, oid := range reach.Order {
				if r := reach.Role[oid]; r == repDepVia || r == repDepStored {
					victims = append(victims, oid)
				}
			}
			for _, v := range victims[len(victims)/2 : len(victims)/2+1] {
				corruptChunkAnyLayout(t, l.s, v)
			}
			failed := 0
			for _, off := range restarts {
				n := min(4096, int64(len(data))-off)
				got, err := seekRead(t, l.s, root, off, n)
				if err != nil {
					failed++
				} else if !bytes.Equal(got, data[off:off+n]) {
					t.Fatalf("a damaged representation served wrong bytes at %d", off)
				}
			}
			if failed == 0 {
				t.Fatal("no range noticed the damaged component")
			}
		})
	}
}

// Authority codec replay with seek indexes: S3 ranges and ranged dependency reads regenerate only the units they
// need, through gc, repack, reopen, deep verification and revert.
func TestAuthoritySeekableCodecReplay(t *testing.T) {
	l := newAuthLab(t)
	for _, f := range []string{"text.l6.gz", "text.l9.gz", "text.l3.zst", "text.l19.zst"} {
		b, err := os.ReadFile(filepath.Join("testing-harnesses/testdata/b2-seek", f))
		mustOK(t, err)
		l.put(f, b)
	}
	report := l.migrate(AuthPolicy{Strategy: "forced", AllowRep: true, MinSaving: -10, NoZN7: true, CodecReplay: true})
	reps := 0
	for _, p := range report.Plans {
		if p.Form == "rep" {
			reps++
		}
	}
	if reps != 4 {
		t.Fatalf("%d of 4 containers became representations: %+v", reps, report.Plans)
	}
	check := func(stage string) {
		l.exact(stage)
		for key, want := range l.want {
			sha := sha256.Sum256(want)
			ctx := l.s.newAuthCtx()
			body, _, err := ctx.body(sha, 0, 1)
			mustOK(t, err)
			r := rand.New(rand.NewSource(int64(len(want))))
			for i := 0; i < 16; i++ {
				o := r.Int63n(int64(len(want)))
				p := make([]byte, min(1+r.Int63n(4096), int64(len(want))-o))
				if k, err := body.ReadAt(p, o); k != len(p) || !bytes.Equal(p, want[o:o+int64(len(p))]) {
					t.Fatalf("%s %s: ranged body read %d+%d: %v", stage, key, o, len(p), err)
				}
			}
			if b := body.(*authRepBody); b.data != nil || ctx.fetched > 16*(3<<20)/2 {
				t.Fatalf("%s %s: ranged reads materialized the object (fetched %d)", stage, key, ctx.fetched)
			}
		}
	}
	check("migrated")
	l.reclaim()
	check("reclaimed")
	l.verifyOK("reclaimed")
	l.close()
	l.open()
	before := l.s.auth.rangeReads.Load()
	check("reopened")
	if l.s.auth.rangeReads.Load() == before {
		t.Fatal("ranges did not go through the authoritative representation")
	}
	sha := sha256.Sum256(l.want["text.l3.zst"])
	mustOK(t, l.s.AuthorityRevert(sha))
	if rec, _ := l.s.authLoad(sha); rec != nil {
		t.Fatal("reverted record remains")
	}
	l.exact("one reverted")
	l.reclaim()
	l.exact("one reverted, reclaimed")
	l.verifyOK("one reverted, reclaimed")
}
