package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"runtime/trace"
	"slices"
	"sync"
	"testing"
	"time"
)

type df0ProgramWork struct {
	Frames, RawFrames, Runs, SourceRuns, SourceTokens, LiteralTokens int
	SourceBits, EmittedBits                                          int
}

func df0ProgramCounts(t *testing.T, shape *deflateTranslatePlan, lo, n int, fetch func([32]byte) ([]byte, error)) df0ProgramWork {
	t.Helper()
	var work df0ProgramWork
	for _, frame := range shape.index.Frames {
		from := shape.index.HeaderLen + frame.Start/8
		to := shape.index.HeaderLen + (frame.End+7)/8
		if lo+n <= from || lo >= to {
			continue
		}
		work.Frames++
		work.EmittedBits += frame.End - frame.Start
		if frame.Raw {
			work.RawFrames++
			continue
		}
		raw, err := fetch(frame.Program)
		mustOK(t, err)
		wire := repWireReader{b: raw}
		for remaining := frame.Header.NTokens; remaining > 0; {
			header := int(wire.uv(uint64(remaining*2 + 1)))
			count := header >> 1
			if wire.err != nil || count <= 0 || count > remaining {
				t.Fatal("audited run has invalid token extent")
			}
			remaining -= count
			work.Runs++
			if header&1 == 0 {
				work.LiteralTokens += count
				for range count {
					wire.uv(1<<32 - 1)
				}
			} else {
				work.SourceRuns++
				work.SourceTokens += count
				wire.uv(uint64(len(shape.index.SourceTables) - 1))
				wire.uv(bitPatchMaxObject * 8)
				work.SourceBits += int(wire.uv(4 << 20))
				if shape.corrected {
					for range count {
						wire.uv(1<<18 - 1)
					}
				}
			}
		}
		if wire.err != nil || wire.pos != len(wire.b) {
			t.Fatal("audited runs did not consume the exact frame program")
		}
	}
	return work
}

func df0ReplayAudit(t *testing.T, l *authLab, target df0Object) {
	t.Helper()
	l.open()
	defer l.close()
	rec, err := l.s.authLoad(sha256.Sum256(target.raw))
	mustOK(t, err)
	if rec == nil || rec.Root == nil {
		t.Fatal("resolved compiled authority is missing")
	}
	root, err := l.s.casRead(rec.Root.Rep)
	mustOK(t, err)
	rep, err := unmarshalRep(root, repProdLimits)
	mustOK(t, err)
	if rep.Kind != repKindDeflateTranslate {
		t.Fatalf("profile has representation kind %d", rep.Kind)
	}
	parseCPU, parseStart := fb13CPU(), time.Now()
	var last *deflateTranslatePlan
	for range 40 {
		last, err = parseDeflateTranslate(rep.Recipe)
		mustOK(t, err)
	}
	parseWall := time.Since(parseStart)
	parseNS := fb13CPU() - parseCPU
	members := make(map[[32]byte]int64)
	for _, m := range rep.Members {
		members[m.SHA] = m.Size
	}
	source := &storeRepSource{s: l.s}
	defer source.close()
	sources := make(map[[32]byte]bool)
	for _, id := range last.index.Sources {
		sources[id] = true
	}
	get := func(id [32]byte) ([]byte, error) { return repReadObject(source, id, members[id], repDirectMax) }
	for _, n := range []int{4096, 64 << 10, len(target.raw)} {
		const iterations = 12
		var fetchWall, verifyWall, replayWall, replayCPU time.Duration
		var storedReads, sourceReads, readBytes, repeatedBytes int
		var tokens, frames, rawFrames, copiedRuns, emittedBits, sourceBits, runs int
		known := make(map[[32]byte]bool)
		for i := range iterations {
			at := (len(target.raw) - n) * i / (iterations - 1)
			start, cpu := time.Now(), fb13CPU()
			raw, _, err := last.read(at, n, len(rep.Recipe), func(id [32]byte) ([]byte, error) {
				started := time.Now()
				chunk, err := get(id)
				fetchWall += time.Since(started)
				storedReads++
				readBytes += len(chunk)
				if known[id] {
					repeatedBytes += len(chunk)
				}
				known[id] = true
				if sources[id] {
					sourceReads++
				}
				return chunk, err
			})
			replayWall += time.Since(start)
			replayCPU += fb13CPU() - cpu
			mustOK(t, err)
			check := time.Now()
			if sha256.Sum256(raw) != sha256.Sum256(target.raw[at:at+n]) {
				t.Fatal("audited read differs from requested compressed bytes")
			}
			verifyWall += time.Since(check)
			work := df0ProgramCounts(t, last, at, n, get)
			tokens += work.SourceTokens
			frames += work.Frames
			rawFrames += work.RawFrames
			copiedRuns += work.SourceRuns
			emittedBits += work.EmittedBits
			sourceBits += work.SourceBits
			runs += work.Runs
		}
		df0Log(t, "DF0AUDIT", map[string]any{
			"case": t.Name(), "size": n, "iterations": iterations, "frames": frames, "raw_frames": rawFrames,
			"runs": runs, "source_runs": copiedRuns, "source_decoded_tokens": tokens, "source_consumed_bits": sourceBits, "frame_bits": emittedBits,
			"stored_reads": storedReads, "source_reads": sourceReads, "read_bytes": readBytes, "reread_bytes": repeatedBytes,
			"index_bytes": len(rep.Recipe), "index_parses": 40, "index_parse_cpu_ns": parseNS, "index_parse_wall_ns": parseWall,
			"replay_wall_ns": replayWall, "replay_cpu_ns": replayCPU, "fetch_and_record_verification_wall_ns": fetchWall,
			"external_sha_check_wall_ns": verifyWall,
		})
	}
}

func df0SignedReadCosts(t *testing.T, seed *authLab, target df0Object) {
	t.Helper()
	seed.open()
	defer seed.close()
	for _, n := range []int{4096, 64 << 10, len(target.raw)} {
		var mu sync.Mutex
		var chunks, diskBytes, logicalBytes, packTouches, sources int64
		var codec repCodecTrace
		repSourceObserver = func(stats repReadStats, cw repCodecTrace) {
			mu.Lock()
			defer mu.Unlock()
			sources++
			chunks += stats.Chunks
			diskBytes += stats.StoredBytes
			logicalBytes += stats.LogicalBytes
			packTouches += int64(len(stats.Packs))
			codec.Units += cw.Units
			codec.UnitBytes += cw.UnitBytes
			codec.PlainBytes += cw.PlainBytes
			codec.OutBytes += cw.OutBytes
			codec.Restarts += cw.Restarts
		}
		func() {
			defer func() { repSourceObserver = nil }()
			queries := make([]df0Query, 12)
			for i := range 12 {
				at := (len(target.raw) - n) * i / 11
				class := "range4"
				if n == len(target.raw) {
					class = "whole"
				}
				queries[i] = df0Query{kind: class, off: at, n: n, id: i}
			}
			df0SignedRun(t, seed, target, queries, 1, false)
		}()
		df0Log(t, "DF0SIGNEDCOST", map[string]any{"case": t.Name(), "size": n, "requests": 12,
			"closed_sources": sources, "record_reads": chunks, "stored_record_bytes": diskBytes, "logical_record_bytes": logicalBytes,
			"pack_touches": packTouches, "codec_trace": codec})
	}
}

func df0Profiling(t *testing.T, seed *authLab, target df0Object, profileDir string) {
	t.Helper()
	mustOK(t, os.MkdirAll(profileDir, 0o700))
	experiment := func(name, class string, count int, cpuProfile bool) {
		t.Run(name, func(t *testing.T) {
			l := &authLab{t: t, dir: t.TempDir()}
			mustOK(t, os.CopyFS(l.dir, os.DirFS(seed.dir)))
			l.open()
			defer l.close()
			signed := gf2SignedReader(t, l)
			if !bytes.Equal(signed(target.key, 0, 0), target.raw) {
				t.Fatal("profile warmup differs")
			}
			path := filepath.Join(profileDir, name)
			f, err := os.Create(path)
			mustOK(t, err)
			defer f.Close()
			if cpuProfile {
				mustOK(t, pprof.StartCPUProfile(f))
				defer pprof.StopCPUProfile()
			} else {
				mustOK(t, trace.Start(f))
				defer trace.Stop()
			}
			queries := df0Queries(len(target.raw), count, 2491705)
			for i := range queries {
				if class == "" {
					continue
				}
				queries[i].kind = class
				if class == "range4" {
					queries[i].n = 4096
					queries[i].off = (i * 17381) % (len(target.raw) - 4096)
				}
			}
			r := df0SignedRun(t, l, target, queries, 2, false)
			r["profile"], r["profile_class"] = name, class
			df0Log(t, "DF0PROFILE", r)
		})
	}
	experiment("sparse.cpu", "range4", 1536, true)
	experiment("whole.cpu", "whole", 64, true)
	experiment("mixed.trace", "", 120, false)
}

func TestDF0LoadAndProfile(t *testing.T) {
	for _, f := range df0Corpus(t) {
		if f.Name != "curl" || f.Split != "validation" {
			continue
		}
		base, target := df0Object{"version/00", df0Fixture(t, f.Files[0])}, df0Object{"version/01", df0Fixture(t, f.Files[1])}
		lanes := []string{"cdc", "archive", "bitpatch", "translate"}
		seeds := make(map[string]*authLab)
		for _, lane := range lanes {
			seeds[lane] = df0Prepare(t, lane, []df0Object{base, target})
		}
		for round := range 2 {
			if round == 1 {
				slices.Reverse(lanes)
			}
			for _, clients := range []int{1, 2, 8, 32} {
				for _, lane := range lanes {
					seed := seeds[lane]
					if seed == nil || lane == "archive" && clients != 2 {
						continue
					}
					t.Run(fmt.Sprintf("round-%d-c%d-%s", round, clients, lane), func(t *testing.T) {
						df0Trial(t, seed, target, lane, round, clients, 240, false, false)
					})
				}
			}
			for _, lane := range []string{"cdc", "translate"} {
				t.Run(fmt.Sprintf("maint-%d-%s", round, lane), func(t *testing.T) {
					df0Trial(t, seeds[lane], target, lane, round, 8, 240, false, true)
				})
			}
			for _, lane := range lanes {
				seed := seeds[lane]
				if seed == nil {
					continue
				}
				for _, class := range []string{"range4", "range64", "whole"} {
					t.Run(fmt.Sprintf("first-%d-%s-%s", round, lane, class), func(t *testing.T) {
						df0FirstRead(t, seed, target, lane, class, round, true)
						df0FirstRead(t, seed, target, lane, class, round, false)
					})
				}
			}
		}
		df0ReplayAudit(t, seeds["translate"], target)
		df0SignedReadCosts(t, seeds["translate"], target)
		if path := os.Getenv("ZEROS3_DF0_PROFILE"); path != "" {
			df0Profiling(t, seeds["translate"], target, path)
		}
		return
	}
	t.Fatal("registered curl validation series missing")
}

func df0FirstRead(t *testing.T, seed *authLab, target df0Object, lane, class string, round int, cold bool) {
	t.Helper()
	l := &authLab{t: t, dir: t.TempDir()}
	mustOK(t, os.CopyFS(l.dir, os.DirFS(seed.dir)))
	var pages df0CachePages
	if cold {
		pages = df0DropPages(t, l.dir)
	}
	l.open()
	defer l.close()
	if !cold && !bytes.Equal(gf2SignedReader(t, l)(target.key, 0, 0), target.raw) {
		t.Fatal("matched warm first-request control differs")
	}
	n := 4096
	if class == "whole" {
		n = len(target.raw)
	} else if class == "range64" {
		n = 64 << 10
	}
	q := df0Query{kind: class, off: (len(target.raw) - n) / 3, n: n}
	row := df0SignedRun(t, l, target, []df0Query{q}, 1, false)
	row["case"], row["lane"], row["round"], row["cold"], row["cache_pages"] = t.Name(), lane, round, cold, pages
	df0Log(t, "DF0FIRST", row)
}

func TestDF0TailSamples(t *testing.T) {
	for _, f := range df0Corpus(t) {
		if f.Name != "lua" || f.Split != "validation" {
			continue
		}
		base, target := df0Object{"version/00", df0Fixture(t, f.Files[0])}, df0Object{"version/01", df0Fixture(t, f.Files[1])}
		lanes := []string{"cdc", "archive", "bitpatch", "translate"}
		seeds := make(map[string]*authLab)
		for _, lane := range lanes {
			seeds[lane] = df0Prepare(t, lane, []df0Object{base, target})
		}
		for round := range 2 {
			if round == 1 {
				slices.Reverse(lanes)
			}
			for _, lane := range lanes {
				if seed := seeds[lane]; seed != nil {
					t.Run(fmt.Sprintf("round-%d-%s", round, lane), func(t *testing.T) {
						df0Trial(t, seed, target, lane, round, 2, 1536, false, false)
					})
				}
			}
		}
		return
	}
	t.Fatal("registered Lua validation series missing")
}

func df0DiagnosticGzip(t *testing.T, payload []byte, level int) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := gzip.NewWriterLevel(&out, level)
	mustOK(t, err)
	_, err = w.Write(payload)
	mustOK(t, err)
	mustOK(t, w.Close())
	return out.Bytes()
}

func df0DiagnosticTar(t *testing.T, order []int, edit bool) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := tar.NewWriter(&out)
	for _, i := range order {
		payload := genRandomBytes(int64(i+8347), 32<<10)
		if edit && i == 0 {
			payload[118] ^= 17
		}
		mustOK(t, writer.WriteHeader(&tar.Header{Name: fmt.Sprintf("sample-%d", i), Mode: 0o644, Size: int64(len(payload))}))
		_, err := writer.Write(payload)
		mustOK(t, err)
	}
	mustOK(t, writer.Close())
	return out.Bytes()
}

func TestDF0Diagnostics(t *testing.T) {
	parent := df0DiagnosticTar(t, []int{0, 1, 2, 3, 4, 5}, false)
	base := df0DiagnosticGzip(t, parent, gzip.BestSpeed)
	for _, sample := range []df0Object{
		{"in-place", df0DiagnosticGzip(t, df0DiagnosticTar(t, []int{0, 1, 2, 3, 4, 5}, true), gzip.BestSpeed)},
		{"reordered-and-reencoded", df0DiagnosticGzip(t, df0DiagnosticTar(t, []int{2, 5, 1, 4, 0, 3}, false), gzip.BestCompression)},
		{"unrelated-entropy", df0DiagnosticGzip(t, genRandomBytes(43102, len(parent)), gzip.BestSpeed)},
		{"concatenated-members", append(bytes.Clone(base), df0DiagnosticGzip(t, genRandomBytes(42, 4<<10), gzip.BestSpeed)...)},
	} {
		t.Run(sample.key, func(t *testing.T) {
			objs := []df0Object{{"base", base}, {"target", sample.raw}}
			df0Case(t, objs, true)
		})
	}
}

func TestDF0LiveReadCostProbe(t *testing.T) {
	raw := append(gf2TarFixture(t, false), genRandomBytes(539, 90<<10)...)
	base := df0Object{"base", gf2GzipFixture(t, raw, 1, 65<<10, "source.tar")}
	target := df0Object{"target", gf2GzipFixture(t, raw, 9, 83<<10, "target.tar")}
	if len(target.raw) < 64<<10 {
		t.Fatal("probe fixture needs to support medium ranges")
	}
	seed := df0Prepare(t, "translate", []df0Object{base, target})
	if seed == nil {
		t.Fatal("probe fixture failed codec admission")
	}
	df0SignedReadCosts(t, seed, target)
}
