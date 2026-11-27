package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestGF2BudgetedOverlay(t *testing.T) {
	root, families := gf0Corpus(t)
	for _, fam := range families {
		if fam.Name != "typescript-gzip" {
			continue
		}
		l := newAuthLab(t)
		l.s.writeBinaryManifests = true
		var input [][]byte
		for i := range fam.Paths {
			raw, _ := gf0ReadFixture(t, root, fam, i)
			l.put(fmt.Sprintf("original/%d", i), raw)
			input = append(input, raw)
		}
		ctx := context.Background()
		mustOK(t, l.s.ensureFeature(agFeature))
		_, err := l.s.AuthorityOptimize(ctx, OptimizePolicy{
			AuthPolicy: AuthPolicy{Apply: true, Order: "oldest", AllowDelta: true, AllowRep: true, CodecReplay: true, MaxDepth: 3},
			Profile:    "archive", Canonical: true, Splice: true, FastReencode: true,
		})
		mustOK(t, err)
		_, err = l.s.finishAuthorityArchive(ctx, 0)
		mustOK(t, err)
		l.close()
		baseline, _, _ := fb13Allocation(t, l.dir)
		const width = 32 << 10
		const operations = 120
		for phase := range 2 {
			l.open()
			if phase == 1 {
				// A frozen active set from the initial query distribution occupies
				// four verified output frames. Later queries may shift to unseen ones.
				for i, raw := range input {
					for _, frame := range []int{2, 6} {
						l.put(fmt.Sprintf("overlay/%d/%d", i, frame), raw[frame*width:(frame+1)*width])
					}
				}
				_, err := l.s.finishAuthorityArchive(ctx, 0)
				mustOK(t, err)
				l.close()
				l.open()
			}
			total, _, _ := fb13Allocation(t, l.dir)
			for _, missEvery := range []int{50, 10, 1} {
				cpu := fb13CPU()
				hits := 0
				var times []int64
				for i := range operations {
					obj, frame := i%len(input), 2+4*((i/len(input))%2)
					if i%missEvery == 0 {
						frame += 13
					}
					off := (i * 397) % (width - 4096)
					start := frame*width + off
					var got []byte
					before := time.Now()
					if phase == 1 && (frame == 2 || frame == 6) {
						got = l.read(fmt.Sprintf("overlay/%d/%d", obj, frame), byteRange{int64(off), int64(off + 4095)})
						hits++
					} else {
						got = l.read(fmt.Sprintf("original/%d", obj), byteRange{int64(start), int64(start + 4095)})
					}
					times = append(times, time.Since(before).Nanoseconds())
					if !bytes.Equal(got, input[obj][start:start+4096]) {
						t.Fatal("overlay did not preserve exact range")
					}
				}
				row, err := json.Marshal(map[string]any{"phase": phase, "miss_every": missEvery, "operations": operations,
					"overlay_hits": hits, "allocated": total, "baseline_allocated": baseline, "range_p50_p95_p99_ns": gf0Percentiles(times),
					"cpu_ns": int64(fb13CPU() - cpu)})
				mustOK(t, err)
				fmt.Printf("GF2OVERLAY %s\n", row)
			}
			l.verifyOK("query overlay store")
			l.close()
		}
	}
}
