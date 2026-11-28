package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestGF5CheckpointIncumbent(t *testing.T) {
	input := gf5FixtureReads(t)
	for _, lane := range []string{"cdc", "native-numeric"} {
		t.Run(lane, func(t *testing.T) {
			l := newAuthLab(t)
			l.s.writeBinaryManifests = true
			for i, raw := range input {
				l.put(fmt.Sprintf("model/%d.safetensors", i), raw)
			}
			ctx := context.Background()
			_, err := l.s.finishAuthorityArchive(ctx, 0)
			mustOK(t, err)
			before, cpu := time.Now(), fb13CPU()
			var forms map[string]int
			if lane == "native-numeric" {
				mustOK(t, l.s.ensureFeature(agFeature))
				rep, err := l.s.AuthorityMigrateContext(ctx, AuthPolicy{
					AllowDelta: true, NoReadCost: true, Apply: true, Order: "oldest", MaxDepth: 2, MaxBases: 2,
					Candidates: 2, Workers: 2, MinSaving: 0.005,
				})
				mustOK(t, err)
				forms = map[string]int{}
				for _, p := range rep.Plans {
					forms[p.Reason]++
				}
			}
			planWall, planCPU := time.Since(before).Nanoseconds(), int64(fb13CPU()-cpu)
			_, err = l.s.finishAuthorityArchive(ctx, 0)
			mustOK(t, err)
			l.close()
			total, packs, _ := fb13Allocation(t, l.dir)
			before = time.Now()
			l.open()
			startup := time.Since(before).Nanoseconds()
			var whole, small []int64
			before = time.Now()
			cpu = fb13CPU()
			for i, raw := range input {
				key := fmt.Sprintf("model/%d.safetensors", i)
				started := time.Now()
				got := l.read(key, byteRange{0, int64(len(raw)) - 1})
				whole = append(whole, time.Since(started).Nanoseconds())
				if !bytes.Equal(got, raw) {
					t.Fatal("native model whole restore differs")
				}
				for k := range 18 {
					at := (len(raw) - 4096) * k / 17
					started = time.Now()
					got = l.read(key, byteRange{int64(at), int64(at + 4095)})
					small = append(small, time.Since(started).Nanoseconds())
					if !bytes.Equal(got, raw[at:at+4096]) {
						t.Fatal("native model range differs")
					}
				}
			}
			readCPU, readWall := int64(fb13CPU()-cpu), time.Since(before).Nanoseconds()
			l.verifyOK("native checkpoint incumbent")
			row, err := json.Marshal(map[string]any{"lane": lane, "allocated": total, "pack_allocated": packs,
				"plan_cpu_ns": planCPU, "plan_wall_ns": planWall, "startup_ns": startup, "plan_reasons": forms,
				"read_cpu_ns": readCPU, "read_wall_ns": readWall, "whole_p50_p95_p99_ns": gf0Percentiles(whole),
				"range_p50_p95_p99_ns": gf0Percentiles(small)})
			mustOK(t, err)
			fmt.Printf("GF5INC %s\n", row)
		})
	}
}
