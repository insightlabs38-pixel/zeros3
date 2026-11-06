package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"time"
	"zeros3-z3/lab"
	"zeros3-z3/simdelta"
)

// quality separates discovery from coding with a bounded, explicitly sampled
// exhaustive candidate oracle. It does not pretend to be a full-store oracle.
func quality(f Family) {
	if len(f.Paths) < 2 {
		return
	}
	a, e := os.ReadFile(f.Paths[0])
	must(e)
	b, e := os.ReadFile(f.Paths[len(f.Paths)-1])
	must(e)
	base := lab.ChunkScaled(a, 16<<10, 64<<10, 256<<10)
	target := lab.ChunkScaled(b, 16<<10, 64<<10, 256<<10)
	if len(base) > 64 {
		var sampled [][]byte
		for i := 0; i < 64; i++ {
			sampled = append(sampled, base[i*(len(base)-1)/63])
		}
		base = sampled
	}
	if len(target) > 16 {
		var sampled [][]byte
		for i := 0; i < 16; i++ {
			sampled = append(sampled, target[i*(len(target)-1)/15])
		}
		target = sampled
	}
	names := []string{"bottom16", "super4", "edges2", "local64", "coarse16", "sign8"}
	ix := []*simdelta.Index{simdelta.NewIndex(), simdelta.NewIndex(), simdelta.NewIndex(), simdelta.NewIndex(), simdelta.NewIndex(), simdelta.NewIndex()}
	features := func(b []byte) [][]uint64 {
		f := simdelta.Features(b, 16)
		return [][]uint64{f, simdelta.Superfeatures(f), simdelta.Edges(b), simdelta.BlockFeatures(b), simdelta.CoarseFeatures(b), simdelta.SignFeatures(b)}
	}
	build := time.Now()
	for id, b := range base {
		for j, f := range features(b) {
			ix[j].Add(uint32(id), f)
		}
	}
	buildMS := float64(time.Since(build).Microseconds()) / 1000
	type qualityRow struct {
		Family, Scheme                                                          string
		Bases, Queries, Useful, Hits, ExactOracleHits, Returned, UsefulReturned int
		QueryMS, BuildMS                                                        float64
		OracleBytes, SelectedBytes                                              int64
	}
	rows := make([]qualityRow, 6)
	for i := range rows {
		rows[i] = qualityRow{Family: f.Name, Scheme: names[i], Bases: len(base), BuildMS: buildMS}
	}
	for _, t := range target {
		direct, e := simdelta.Encode("direct", nil, t)
		must(e)
		costs := make([]int, len(base))
		best := len(direct)
		var bestIDs []int
		for i, b := range base {
			w, e := simdelta.Encode("adaptive", [][]byte{b}, t)
			must(e)
			costs[i] = len(w)
			if len(w) < best {
				best = len(w)
			}
		}
		for i, c := range costs {
			if c == best {
				bestIDs = append(bestIDs, i)
			}
		}
		sets := features(t)
		for j, x := range ix {
			row := &rows[j]
			row.Queries++
			row.OracleBytes += int64(best)
			t0 := time.Now()
			q := x.Query(sets[j], 4)
			row.QueryMS += float64(time.Since(t0).Microseconds()) / 1000
			row.Returned += len(q)
			chosen := len(direct)
			for _, c := range q {
				if costs[c.ID] < chosen {
					chosen = costs[c.ID]
				}
				if costs[c.ID]*10 < len(direct)*9 {
					row.UsefulReturned++
				}
			}
			if best*10 < len(direct)*9 {
				row.Useful++
				if chosen*10 < len(direct)*9 {
					row.Hits++
				}
				for _, c := range q {
					if sort.SearchInts(bestIDs, int(c.ID)) < len(bestIDs) {
						k := sort.SearchInts(bestIDs, int(c.ID))
						if bestIDs[k] == int(c.ID) {
							row.ExactOracleHits++
							break
						}
					}
				}
			}
			row.SelectedBytes += int64(chosen)
		}
	}
	for _, row := range rows {
		emit(row)
	}
	// Known first/last equality is reported, never treated as a related-history hit.
	fmt.Fprintf(os.Stderr, "quality %s first=%x last=%x\n", f.Name, sha256.Sum256(a), sha256.Sum256(b))
}
