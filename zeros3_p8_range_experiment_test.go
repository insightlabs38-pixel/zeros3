//go:build p8rangeexperiment

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	rand "math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

func TestP8NestedRangeExperiment(t *testing.T) {
	root := os.Getenv("ZEROS3_B2_CORPUS")
	out := os.Getenv("ZEROS3_RANGE_OUT")
	if root == "" || out == "" {
		t.Skip("set ZEROS3_B2_CORPUS and ZEROS3_RANGE_OUT")
	}
	inputs, err := filepath.Glob(filepath.Join(root, "src", "coreutils-*.tar"))
	if err != nil || len(inputs) != 3 {
		t.Fatalf("expected three pinned source tar revisions, found %d: %v", len(inputs), err)
	}
	corpus := make(map[[32]byte][]byte, len(inputs))
	for _, input := range inputs {
		data, err := os.ReadFile(input)
		if err != nil {
			t.Fatal(err)
		}
		corpus[sha256.Sum256(data)] = data
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows := json.NewEncoder(f)
	t.Setenv("ZEROS3_CHUNK_CACHE_MIB", "0")
	cdc := filepath.Join(t.TempDir(), "cdc")
	econBuild(t, cdc, inputs, repModeOff)
	econCompact(t, cdc)
	cdcBytes := dirBytes(t, cdc)
	t.Logf("CDC fixture retained %d bytes", cdcBytes)

	for _, window := range []int64{1 << 20, 128 << 10} {
		t.Run(strconv.FormatInt(window, 10), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "store")
			if err := econCopyTree(cdc, dir); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			cpu := procCPUSeconds()
			report, rss, _ := func() (AuthReport, float64, int64) {
				prior := authWindowSize
				authWindowSize = window
				defer func() { authWindowSize = prior }()
				return econMigrate(t, dir, AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 1, AllowDelta: true})
			}()
			wall := time.Since(start).Seconds()
			cpu = procCPUSeconds() - cpu
			t.Logf("window=%d migrated=%d CPU=%.2f wall=%.2f", window, report.Admitted, cpu, wall)
			econReclaim(t, dir)
			kept := dirBytes(t, dir)
			t.Logf("window=%d retained=%d", window, kept)
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			mans := s.liveManifests()
			if len(mans) != len(corpus) {
				t.Fatalf("wrong manifest count: %d", len(mans))
			}
			depths := make(map[int]int)
			var chosen manifestV1
			var maxDepth int
			for _, lm := range mans {
				m := lm.man
				sum, err := decodeHexSHA256(m.ObjectSHA256)
				if err != nil || corpus[sum] == nil {
					t.Fatalf("unexpected logical digest %s", m.ObjectSHA256)
				}
				h := sha256.New()
				r := s.openObjectReader(m, byteRange{0, m.TotalLength - 1})
				for {
					b, e := r.next()
					if e == io.EOF {
						break
					}
					if e != nil {
						t.Fatal(e)
					}
					h.Write(b)
				}
				r.close()
				if !bytes.Equal(h.Sum(nil), sum[:]) {
					t.Fatal("whole replay differs")
				}
				record, err := s.authLoad(sum)
				if err != nil {
					t.Fatal(err)
				}
				depth := 0
				if record != nil {
					depth = int(record.Depth)
				}
				depths[depth]++
				if depth > maxDepth || depth == maxDepth && m.ObjectSHA256 < chosen.ObjectSHA256 {
					maxDepth, chosen = depth, m
				}
			}
			t.Logf("window=%d whole digests match, depth counts=%v", window, depths)
			if maxDepth < 2 {
				t.Logf("window %d: actual maximum depth %d", window, maxDepth)
			}
			if report.Admitted == 0 {
				t.Fatal("no admitted deltas")
			}
			sum, _ := decodeHexSHA256(chosen.ObjectSHA256)
			want := corpus[sum]
			const n = 16
			rng := rand.New(rand.NewPCG(81, 201))
			offsets := make([]int64, n)
			for i := range offsets {
				offsets[i] = rng.Int64N(chosen.TotalLength - 4096)
			}
			for _, cacheMiB := range []int64{0, 64} {
				s.SetAuthorityCache(cacheMiB << 20)
				var times []int64
				passes := 1
				if cacheMiB > 0 {
					passes = 2
				}
				for pass := 0; pass < passes; pass++ {
					for _, offset := range offsets {
						begin := time.Now()
						r := s.openObjectReader(chosen, byteRange{offset, offset + 4095})
						var got []byte
						for {
							p, e := r.next()
							if e == io.EOF {
								break
							}
							if e != nil {
								t.Fatal(e)
							}
							got = append(got, p...)
						}
						r.close()
						duration := time.Since(begin)
						if !bytes.Equal(got, want[offset:offset+4096]) {
							t.Fatalf("inexact range at %d", offset)
						}
						if pass == passes-1 {
							times = append(times, duration.Microseconds())
						}
					}
				}
				slices.Sort(times)
				if err := rows.Encode(map[string]any{
					"window_bytes": window, "cache_mib": cacheMiB, "depths": depths,
					"target_sha256": chosen.ObjectSHA256, "range_us": times, "migrate_s": wall,
					"cpu_s": cpu, "rss_mib": rss, "cdc_bytes": cdcBytes, "phys_after": kept,
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
