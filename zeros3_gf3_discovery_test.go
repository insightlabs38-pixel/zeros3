package main

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"
)

type gf3Candidate struct {
	id, score int
}

type gf3Source struct {
	raw      []byte
	sketch   []uint64
	features []rsFeat
	level    uint8
}

func gf3PrepareSources(source [][]byte, profile rsParams) []gf3Source {
	out := make([]gf3Source, len(source))
	for i, b := range source {
		out[i].raw = b
		out[i].sketch = authFeatures(b, 16)
		out[i].features, out[i].level = profile.features(b)
	}
	return out
}

// Compare population counts rather than introducing an uncalibrated size
// model. The hierarchical mode admits denser evidence only when a rarer
// sample has insufficient support, and ranks the uncovered target ranges.
func gf3Shortlist(sources []gf3Source, target []byte, limit int, method string, profile rsParams) []int {
	scores := make([]gf3Candidate, 0, len(sources))
	hits := make([]map[uint32]int, len(sources))
	coarse := rsDefaultParams().MinLevel
	complement := method == "complement" || method == "complement-fine"
	binShift := uint(7)
	if method == "complement-fine" {
		binShift = 5
	}
	targetSketch := authFeatures(target, 16)
	targetFeatures, targetLevel := profile.features(target)
	for id, source := range sources {
		scores = append(scores, gf3Candidate{id: id})
		match := &scores[len(scores)-1]
		if method == "bottom16" {
			for _, h := range source.sketch {
				if slices.Contains(targetSketch, h) {
					match.score++
				}
			}
			continue
		}
		hits[id] = map[uint32]int{}
		level := max(targetLevel, source.level)
		i, j := 0, 0
		for i < len(source.features) && j < len(targetFeatures) {
			a, b := source.features[i], targetFeatures[j]
			switch {
			case a.h < b.h:
				i++
			case a.h > b.h:
				j++
			default:
				if a.lvl >= level && b.lvl >= level {
					if method == "region" {
						if a.lvl >= coarse && b.lvl >= coarse {
							match.score++
						}
					} else {
						// Combine evidence within two-KiB positions, so copies from the
						// same interval do not crowd out complementary sources.
						bin := b.pos >> binShift
						weight := 1
						if a.lvl >= coarse && b.lvl >= coarse {
							weight = 3
						}
						hits[id][bin] = max(hits[id][bin], weight)
					}
				}
				i++
				j++
			}
		}
	}
	var chosen []int
	covered := map[uint32]bool{}
	for len(chosen) < limit {
		if complement {
			for i := range scores {
				scores[i].score = 0
				for bin, weight := range hits[scores[i].id] {
					if !covered[bin] {
						scores[i].score += weight
					}
				}
				if slices.Contains(chosen, scores[i].id) {
					scores[i].score = -1
				}
			}
		}
		slices.SortFunc(scores, func(a, b gf3Candidate) int {
			return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.id, b.id))
		})
		if len(scores) == 0 || scores[0].score <= 0 {
			break
		}
		id := scores[0].id
		chosen = append(chosen, id)
		if complement {
			for bin := range hits[id] {
				covered[bin] = true
			}
		} else {
			scores = scores[1:]
		}
	}
	return chosen
}

type gf3MeasuredProgram struct {
	Sources []int
	Bytes   int
	Copied  int64
}

// Use a real multi-base native XD envelope and replay all COPY and literal
// steps against the independently supplied source and target bytes.
func gf3XD(t *testing.T, sources []gf3Source, subset []int, target []byte) gf3MeasuredProgram {
	t.Helper()
	bases := make([]xdSrc, 0, len(subset))
	for _, id := range subset {
		b := sources[id].raw
		bases = append(bases, xdSrc{sha256.Sum256(b), b})
	}
	blob, _, err := xdEncode(bases, target, 32<<10)
	mustOK(t, err)
	header, more, err := xdParseHeader(blob)
	mustOK(t, err)
	if more || header.total != int64(len(blob)) || header.sha != sha256.Sum256(target) {
		t.Fatal("invalid oracle candidate identity")
	}
	actual := make([]int, 0, len(header.bases))
	src := make([][]byte, 0, len(header.bases))
	for _, ref := range header.bases {
		at := slices.IndexFunc(bases, func(b xdSrc) bool { return b.sha == ref.SHA && int64(len(b.data)) == ref.Size })
		if at < 0 {
			t.Fatal("oracle emitted an undeclared basis")
		}
		actual = append(actual, subset[at])
		src = append(src, bases[at].data)
	}
	hash := sha256.New()
	var copied int64
	for i, frame := range header.frames {
		ops, err := xdDecodeFrame(&header, i, blob[frame.poff:frame.poff+frame.plen])
		mustOK(t, err)
		for _, op := range ops {
			data := op.lit
			if op.base >= 0 {
				data = src[op.base][op.off : op.off+op.n]
				copied += op.n
			}
			if !bytes.Equal(data, target[op.tpos:op.tpos+op.n]) {
				t.Fatal("oracle delta reconstructed different bytes")
			}
			hash.Write(data)
		}
	}
	if [32]byte(hash.Sum(nil)) != header.sha {
		t.Fatal("oracle replay altered the target digest")
	}
	return gf3MeasuredProgram{actual, len(blob), copied}
}

func gf3SearchSmall(t *testing.T, sources []gf3Source, shortlist []int, target []byte) gf3MeasuredProgram {
	t.Helper()
	best := gf3XD(t, sources, nil, target)
	shortlist = slices.Clone(shortlist)
	slices.Sort(shortlist)
	for i, left := range shortlist {
		cand := gf3XD(t, sources, []int{left}, target)
		if cand.Bytes < best.Bytes {
			best = cand
		}
		for _, right := range shortlist[i+1:] {
			cand = gf3XD(t, sources, []int{left, right}, target)
			if cand.Bytes < best.Bytes {
				best = cand
			}
		}
	}
	return best
}

func TestGF3NativeDiscoveryOracle(t *testing.T) {
	const size = 96 << 10
	source := make([][]byte, 12)
	for i := range source {
		source[i] = genRandomBytes(int64(8391+97*i), size)
	}
	profile := rsDefaultParams()
	dense := profile
	dense.MinLevel = 5
	start, cpu := time.Now(), fb13CPU()
	prep := gf3PrepareSources(source, dense)
	precomputeWall, precomputeCPU := time.Since(start).Nanoseconds(), int64(fb13CPU()-cpu)
	features := 0
	for _, s := range prep {
		features += len(s.features)
	}
	cases := []struct {
		name   string
		target []byte
	}{
		{"complementary-regions", slices.Concat(source[8][4009:16019], source[11][47003:76831], genRandomBytes(87, 48<<10))},
		{"short-containment", slices.Concat(genRandomBytes(197, 80<<10), source[10][5923:7053], source[9][591:2762])},
		{"unrelated-entropy", genRandomBytes(4961, size)},
	}
	all := make([]int, len(prep))
	for i := range all {
		all[i] = i
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oracle := gf3SearchSmall(t, prep, all, tc.target)
			for _, method := range []string{"bottom16", "region", "complement", "complement-fine"} {
				start, cpu := time.Now(), fb13CPU()
				shortlist := gf3Shortlist(prep, tc.target, 4, method, dense)
				elapsed, discoveryCPU := time.Since(start).Nanoseconds(), int64(fb13CPU()-cpu)
				selected := gf3SearchSmall(t, prep, shortlist, tc.target)
				if selected.Bytes < oracle.Bytes {
					t.Fatal("bounded evaluation beat exhaustive oracle")
				}
				recall := 0
				for _, id := range oracle.Sources {
					if slices.Contains(shortlist, id) {
						recall++
					}
				}
				row, err := json.Marshal(map[string]any{"case": tc.name, "method": method, "shortlist": shortlist,
					"recall": recall, "oracle": oracle, "selected": selected, "discovery_cpu_ns": discoveryCPU,
					"discovery_wall_ns": elapsed, "target_bytes": len(tc.target), "source_pool_bytes": len(source) * size, "dense_features": features, "precompute_wall_ns": precomputeWall, "precompute_cpu_ns": precomputeCPU})
				mustOK(t, err)
				fmt.Printf("GF3DISCOVERY %s\n", row)
			}
		})
	}
}
