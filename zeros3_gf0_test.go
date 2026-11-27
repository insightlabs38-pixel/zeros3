package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

type gf0Family struct {
	Name, Split string
	Paths       []string
	Inputs      []gf0Input
}

type gf0Input struct {
	Name   string `json:"name"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type gf0Baseline struct {
	Family            string         `json:"family"`
	Split             string         `json:"split"`
	Lane              string         `json:"lane"`
	Round             int            `json:"round"`
	Inputs            []gf0Input     `json:"inputs"`
	Allocated         int64          `json:"allocated"`
	Packs             int64          `json:"packs"`
	Logical           int64          `json:"logical"`
	DependencyExtents int64          `json:"dependency_extents"`
	OptimizeCPU       int64          `json:"optimize_cpu_ns"`
	OptimizeWall      int64          `json:"optimize_wall_ns"`
	FinalizeCPU       int64          `json:"finalize_cpu_ns"`
	FinalizeWall      int64          `json:"finalize_wall_ns"`
	Reopen            int64          `json:"reopen_ns"`
	RangeNS           [2][3]int64    `json:"range_p50_p95_p99_ns"`
	WholeNS           [2][3]int64    `json:"whole_p50_p95_p99_ns"`
	ReadCASP95        int64          `json:"range_cas_p95_bytes"`
	Forms             map[string]int `json:"forms"`
}

func gf0Percentiles(samples []int64) [3]int64 {
	return [3]int64{fb14Quantile(samples, 50), fb14Quantile(samples, 95), fb14Quantile(samples, 99)}
}

func gf0Corpus(t *testing.T) (string, []gf0Family) {
	t.Helper()
	input := os.Getenv("ZEROS3_GF0_CORPUS")
	if input == "" {
		t.Skip("set ZEROS3_GF0_CORPUS to the pinned corpus manifest")
	}
	raw, err := os.ReadFile(input)
	mustOK(t, err)
	var families []gf0Family
	mustOK(t, json.Unmarshal(raw, &families))
	return os.Getenv("ZEROS3_GF0_FIXTURES"), families
}

func gf0ReadFixture(t *testing.T, root string, family gf0Family, index int) ([]byte, gf0Input) {
	t.Helper()
	name := family.Paths[index]
	path := name
	if !filepath.IsAbs(name) {
		path = filepath.Join(root, name)
	}
	raw, err := os.ReadFile(path)
	mustOK(t, err)
	actual := gf0Input{Name: name, Bytes: len(raw), SHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}
	if len(family.Inputs) != 0 && actual != family.Inputs[index] {
		t.Fatalf("fixture mismatch: %+v", actual)
	}
	return raw, actual
}

func TestGF0NativeBaseline(t *testing.T) {
	root, families := gf0Corpus(t)
	for _, family := range families {
		t.Run(family.Name, func(t *testing.T) {
			common := gf0Baseline{Family: family.Name, Split: family.Split}
			var versions [][]byte
			for index := range family.Paths {
				raw, input := gf0ReadFixture(t, root, family, index)
				if len(raw) == 0 {
					t.Fatal("empty fixture")
				}
				versions = append(versions, raw)
				common.Logical += int64(len(raw))
				common.Inputs = append(common.Inputs, input)
			}
			if len(versions) < 2 || len(versions) > 3 {
				t.Fatal("baseline needs two or three revisions")
			}
			for round := range 2 {
				lanes := []string{"cdc", "archive"}
				if round != 0 {
					slices.Reverse(lanes)
				}
				for _, lane := range lanes {
					t.Run(fmt.Sprintf("%d/%s", round, lane), func(t *testing.T) {
						gf0Measure(t, versions, common, round, lane)
					})
				}
			}
		})
	}
}

func gf0Measure(t *testing.T, versions [][]byte, row gf0Baseline, round int, lane string) {
	t.Helper()
	row.Round, row.Lane = round, lane
	l := newAuthLab(t)
	l.s.writeBinaryManifests = true
	for i, raw := range versions {
		l.put(fmt.Sprintf("series/%02d", i), raw)
	}
	// Both plans begin on the same compressed native representation.
	_, err := l.s.finishAuthorityArchive(context.Background(), 0)
	mustOK(t, err)
	begin, cpu := time.Now(), fb13CPU()
	if lane == "archive" {
		mustOK(t, l.s.ensureFeature(agFeature))
		_, err = l.s.AuthorityOptimize(context.Background(), OptimizePolicy{
			AuthPolicy: AuthPolicy{Apply: true, Order: "oldest", AllowDelta: true, AllowRep: true,
				CodecReplay: true, HardCodecs: true, MaxDepth: 3},
			Profile: "archive", Canonical: true, Executables: true, Splice: true,
			SparseFiles: true, FastReencode: true,
		})
		mustOK(t, err)
	}
	row.OptimizeCPU, row.OptimizeWall = int64(fb13CPU()-cpu), time.Since(begin).Nanoseconds()
	begin, cpu = time.Now(), fb13CPU()
	_, err = l.s.finishAuthorityArchive(context.Background(), 0)
	mustOK(t, err)
	row.FinalizeCPU, row.FinalizeWall = int64(fb13CPU()-cpu), time.Since(begin).Nanoseconds()
	l.close()
	begin = time.Now()
	l.open()
	row.Reopen = time.Since(begin).Nanoseconds()
	var cas []int64
	for pass := range 2 {
		var ranges, wholes []int64
		for i, raw := range versions {
			key := fmt.Sprintf("series/%02d", i)
			man := l.man(key)
			n := min(4096, len(raw))
			for p := range 9 {
				off := int64(p) * int64(len(raw)-n) / 8
				begin = time.Now()
				got := l.read(key, byteRange{off, off + int64(n) - 1})
				ranges = append(ranges, time.Since(begin).Nanoseconds())
				if !bytes.Equal(got, raw[off:off+int64(n)]) {
					t.Fatal("range differs from source")
				}
				if pass == 1 {
					got, ok, err := l.s.AuthorityReadCost(man, off, int64(n))
					mustOK(t, err)
					if !ok {
						var at int64
						for _, c := range man.Chunks {
							if at < off+int64(n) && at+c.Length > off {
								got += c.Length
							}
							at += c.Length
						}
					}
					cas = append(cas, got)
				}
			}
			for range 2 {
				begin = time.Now()
				got := l.read(key, byteRange{0, int64(len(raw)) - 1})
				wholes = append(wholes, time.Since(begin).Nanoseconds())
				if sha256.Sum256(got) != sha256.Sum256(raw) || !bytes.Equal(got, raw) {
					t.Fatal("whole object differs from the independent source")
				}
			}
		}
		row.RangeNS[pass], row.WholeNS[pass] = gf0Percentiles(ranges), gf0Percentiles(wholes)
	}
	row.ReadCASP95 = fb14Quantile(cas, 95)
	l.verifyOK("grand frontier baseline")
	st, err := l.s.AuthorityStatus()
	mustOK(t, err)
	row.Forms = st.ByForm
	dep := fb13DependencyBytes(t, l.s)
	row.DependencyExtents = dep.BodyBytes + dep.ChunkBytes
	l.close()
	row.Allocated, row.Packs, _ = fb13Allocation(t, l.dir)
	line, err := json.Marshal(row)
	mustOK(t, err)
	fmt.Printf("GF0 %s\n", line)
}
