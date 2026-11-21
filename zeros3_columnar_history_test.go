package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestAuthorityColumnarHistory compares independent retained stores built from the same pinned revisions.
func TestAuthorityColumnarHistory(t *testing.T) {
	dir := os.Getenv("ZEROS3_C5_COLUMNAR")
	if dir == "" {
		t.Skip("set ZEROS3_C5_COLUMNAR to the derived corpus directory")
	}
	var fixture struct {
		Cohorts []struct {
			Name  string
			Files []struct {
				File, SHA256 string
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "MANIFEST.json"))
	mustOK(t, err)
	mustOK(t, json.Unmarshal(raw, &fixture))
	if len(fixture.Cohorts) == 0 {
		t.Fatal("empty columnar fixture")
	}
	type stats struct {
		AllocatedBytes int64        `json:"allocated_bytes"`
		FileBytes      int64        `json:"file_bytes"`
		MigrationMS    int64        `json:"migration_ms"`
		ReclaimMS      int64        `json:"reclaim_ms"`
		WholeMiBPS     float64      `json:"whole_mib_s"`
		PurgedRangeUS  []int64      `json:"purged_range_us"`
		WarmRangeUS    []int64      `json:"warm_range_us"`
		Plans          []AuthPlan   `json:"plans,omitempty"`
		Verification   VerifyResult `json:"verification"`
	}
	result := map[string]map[string]stats{}
	for _, cohort := range fixture.Cohorts {
		passed := t.Run(cohort.Name, func(t *testing.T) {
			type file struct {
				name, digest string
				data         []byte
			}
			files := make([]file, 0, len(cohort.Files))
			for _, info := range cohort.Files {
				p, err := os.ReadFile(filepath.Join(dir, filepath.Base(info.File)))
				mustOK(t, err)
				if fmt.Sprintf("%x", sha256.Sum256(p)) != info.SHA256 || len(p) < 8192 {
					t.Fatalf("unexpected fixture bytes: %s", info.File)
				}
				files = append(files, file{info.File, info.SHA256, p})
			}
			if len(files) < 2 {
				t.Fatal("need at least two ordered revisions")
			}
			result[cohort.Name] = map[string]stats{}
			for _, mode := range []string{"cdc", "b2-delta", "structured"} {
				passed := t.Run(mode, func(t *testing.T) {
					l := newAuthLab(t)
					for _, f := range files {
						l.put(f.name, f.data)
					}
					var m stats
					if mode != "cdc" {
						start := time.Now()
						report := l.migrate(AuthPolicy{Strategy: "forced", MinSaving: -10, MinObject: 1, MinRepGroup: 1,
							AllowRep: mode == "structured", HardCodecs: true, AllowDelta: mode == "b2-delta", MaxBases: 1, MaxDepth: 2})
						m.MigrationMS = time.Since(start).Milliseconds()
						m.Plans = report.Plans
					}
					start := time.Now()
					l.reclaim()
					m.ReclaimMS = time.Since(start).Milliseconds()
					m.FileBytes = dirBytes(t, l.dir)
					allocated, err := exec.Command("du", "-s", "-B1", l.dir).Output()
					mustOK(t, err)
					count := strings.Fields(string(allocated))
					if len(count) != 2 {
						t.Fatalf("unexpected allocated-byte output: %q", allocated)
					}
					m.AllocatedBytes, err = strconv.ParseInt(count[0], 10, 64)
					mustOK(t, err)
					m.Verification = l.verifyOK("columnar retained store")
					var wholeBytes int64
					l.s.purgeReadCaches()
					start = time.Now()
					for _, f := range files {
						n := int64(len(f.data))
						got := l.read(f.name, byteRange{0, n - 1})
						if fmt.Sprintf("%x", sha256.Sum256(got)) != f.digest {
							t.Fatalf("%s whole digest mismatch", f.name)
						}
						wholeBytes += n
					}
					m.WholeMiBPS = float64(wholeBytes) / (1 << 20) / time.Since(start).Seconds()
					for _, f := range files {
						size := int64(len(f.data))
						for _, off := range []int64{0, size / 3, size * 2 / 3, size - 4096} {
							l.s.purgeReadCaches()
							for _, group := range []*[]int64{&m.PurgedRangeUS, &m.WarmRangeUS} {
								start = time.Now()
								got := l.read(f.name, byteRange{off, off + 4095})
								*group = append(*group, time.Since(start).Microseconds())
								if !bytes.Equal(got, f.data[off:off+4096]) {
									t.Fatalf("%s range at %d differs", f.name, off)
								}
							}
						}
					}
					result[cohort.Name][mode] = m
				})
				if !passed {
					return
				}
			}
		})
		if !passed {
			break
		}
	}
	out := os.Getenv("ZEROS3_C5_COLUMNAR_OUT")
	if out != "" {
		b, err := json.MarshalIndent(result, "", "  ")
		mustOK(t, err)
		mustOK(t, os.WriteFile(out, append(b, '\n'), 0600))
	}
}
