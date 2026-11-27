package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"
)

// The native revision uses immutable CAS source records. A newer root can
// depend on the exact source chunks even when the source root itself chooses
// another representation. The catalog charges those chunks explicitly.
type gf1CASPart struct {
	member  repMember
	payload []byte
	end     int
	id      int
}

type gf1PhysicalOption struct {
	patch gf2NativePatch
}

func gf1NativeParts(t *testing.T, raw []byte, man manifestV1) []gf1CASPart {
	t.Helper()
	var out []gf1CASPart
	pos := 0
	for _, chunk := range man.Chunks {
		n := int(chunk.Length)
		if n <= 0 || n > len(raw)-pos {
			t.Fatal("bad native CDC geometry")
		}
		data := raw[pos : pos+n]
		id := sha256.Sum256(data)
		if fmt.Sprintf("%x", id) != chunk.SHA256 {
			t.Fatal("native chunk differs from input")
		}
		pos += n
		out = append(out, gf1CASPart{repMember{Role: repStored, SHA: id, Size: chunk.Length}, data, pos, 0})
	}
	if pos != len(raw) {
		t.Fatal("incomplete native CDC basis")
	}
	return out
}

func gf1DirectWork(parts []gf1CASPart, size int) int64 {
	// The set of intersected records changes only when the range's start
	// or end crosses a record boundary. Enumerate those breakpoints.
	width := min(4096, size)
	var worst int64
	for _, part := range parts {
		for _, pos := range []int{part.end - width, part.end - width + 1, part.end - 1, part.end} {
			pos = max(0, min(pos, size-width))
			var decoded int64
			for _, p := range parts {
				if p.end > pos && p.end-len(p.payload) < pos+width {
					decoded += int64(len(p.payload))
				}
			}
			worst = max(worst, decoded)
		}
	}
	return worst
}

// Work is the verified incoming logical record bytes of one request.
// Full reads use one native source cache. For 4 KiB, at most two
// neighbouring compiled output frames can be touched.
func gf1CompiledWork(t *testing.T, base []byte, parts []gf1CASPart, target []byte, patch gf2NativePatch) (int64, int64) {
	t.Helper()
	union := make([]bool, len(parts))
	last, current := make([]bool, len(parts)), make([]bool, len(parts))
	var whole, worst, previous int64
	for i, program := range patch.programs {
		clear(current)
		access := func(off, n int) ([]byte, error) {
			first := sort.Search(len(parts), func(k int) bool { return parts[k].end > off })
			for k := first; k < len(parts) && parts[k].end-len(parts[k].payload) < off+n; k++ {
				current[k] = true
			}
			return base[off : off+n], nil
		}
		start := i * gf2FrameSize
		expected := target[start:min(start+gf2FrameSize, len(target))]
		reconstructed, err := bitPatchApplyFrame(program, len(expected), len(base), access)
		mustOK(t, err)
		if !bytes.Equal(reconstructed, expected) {
			t.Fatal("costed an inexact compiled option")
		}
		currentPrograms := int64(len(program))
		both := previous + currentPrograms
		whole += currentPrograms
		for k, part := range parts {
			if last[k] || current[k] {
				both += part.member.Size
			}
			union[k] = union[k] || current[k]
		}
		worst = max(worst, both)
		previous = currentPrograms
		last, current = current, last
	}
	for k, p := range parts {
		if union[k] {
			whole += p.member.Size
		}
	}
	meta := int64(len(patch.record))
	return worst + meta, whole + meta
}

func gf1PhysicalOptions(t *testing.T, l *authLab, input [][]byte, rangeFactor int64) (gf1Problem, [][]gf1PhysicalOption) {
	t.Helper()
	n := len(input)
	p := gf1Problem{
		options: make([][]gf1Option, n), rangeLimits: make([]int64, n), wholeLimits: make([]int64, n),
		packCount: 2, quantum: 4096, packHeader: packFixedBytes, packLimit: defaultPackTargetBytes,
		writeLimit: 16 << 20, maxDepth: 1,
	}
	ids := map[[32]byte]int{}
	putRecord := func(data []byte) int {
		id := sha256.Sum256(data)
		if existing, ok := ids[id]; ok {
			return existing
		}
		at := len(p.resources)
		ids[id] = at
		p.resources = append(p.resources, gf1Resource{0, packPriceLen(data) + packRecordBytes})
		return at
	}
	physical := make([][]gf1PhysicalOption, n)
	parts := make([][]gf1CASPart, n)
	manifests := make([]manifestV1, n)
	for i, raw := range input {
		manifests[i] = l.man(fmt.Sprintf("series/%02d", i))
		parts[i] = gf1NativeParts(t, raw, manifests[i])
		var components []int
		for k := range parts[i] {
			parts[i][k].id = putRecord(parts[i][k].payload)
			components = append(components, parts[i][k].id)
		}
		rangeWork := gf1DirectWork(parts[i], len(raw))
		p.rangeLimits[i] = rangeWork * rangeFactor
		p.wholeLimits[i] = int64(len(raw)) * 3
		p.options[i] = append(p.options[i], gf1Option{
			name: "direct", own: components, closure: components, rangeBytes: rangeWork, wholeBytes: int64(len(raw)),
		})
		physical[i] = append(physical[i], gf1PhysicalOption{})
	}
	cfg := gf2MatchConfig{"global-bitpatch", 8, 4, 8, true, true}
	for i, target := range input {
		for b, base := range input[:i] {
			var members []repMember
			var sourceIDs []int
			for _, part := range parts[b] {
				members = append(members, part.member)
				sourceIDs = append(sourceIDs, part.id)
			}
			copied, patch, err := gf2PrepareNativePatch(members, base, target, cfg)
			mustOK(t, err)
			if copied == 0 {
				continue
			}
			rangeCost, whole := gf1CompiledWork(t, base, parts[b], target, patch)
			own := make([]int, 0, len(patch.programs)+2)
			for _, program := range patch.programs {
				own = append(own, putRecord(program))
			}
			recordID := sha256.Sum256(patch.record)
			own = append(own, putRecord(patch.record))
			sum := sha256.Sum256(target)
			rec := authRecord{SHA: sum, Size: int64(len(target)), Gen: 1, Form: authFormRep,
				Root: &repRoot{Rep: recordID, ObjSHA: sum, ObjSize: int64(len(target)), ManifestUUID: manifests[i].ManifestUUID}}
			record, err := rec.marshal()
			mustOK(t, err)
			own = append(own, len(p.resources))
			p.resources = append(p.resources, gf1Resource{1, agEntryCost(int64(len(record)))})
			var write int64
			for _, id := range own {
				write += p.resources[id].bytes
			}
			p.options[i] = append(p.options[i], gf1Option{
				name: fmt.Sprintf("compiled-%d", b), own: own, closure: slices.Concat(own, sourceIDs),
				depth: 1, parents: 1, rangeBytes: rangeCost, wholeBytes: whole, writeBytes: write,
			})
			physical[i] = append(physical[i], gf1PhysicalOption{patch})
		}
	}
	return p, physical
}

func TestGF1PhysicalCompiledSelection(t *testing.T) {
	root, families := gf0Corpus(t)
	for _, family := range families {
		if family.Name != "typescript-gzip" && family.Name != "urllib3-gzip" {
			continue
		}
		t.Run(family.Name, func(t *testing.T) {
			var input [][]byte
			for i := range family.Paths {
				data, _ := gf0ReadFixture(t, root, family, i)
				input = append(input, data)
			}
			for _, factor := range []int64{1, 8} {
				t.Run(fmt.Sprint(factor), func(t *testing.T) {
					ctx := context.Background()
					l := newAuthLab(t)
					l.s.writeBinaryManifests = true
					for i, raw := range input {
						l.put(fmt.Sprintf("series/%02d", i), raw)
					}
					_, err := l.s.finishAuthorityArchive(ctx, 0)
					mustOK(t, err)
					l.close()
					before, bpack, _ := fb13Allocation(t, l.dir)
					l.open()
					cpu := fb13CPU()
					start := time.Now()
					problem, catalog := gf1PhysicalOptions(t, l, input, factor)
					exact, finished := problem.oracle(10000)
					best := problem.beam(32)
					cohort := problem.cohort(4)
					if !finished || cohort.plan.bytes != exact.plan.bytes || best.plan.bytes != exact.plan.bytes {
						t.Fatal("unexpected bounded-catalog gap")
					}
					planCPU, planWall := int64(fb13CPU()-cpu), time.Since(start).Nanoseconds()
					var options []string
					var limits, work []int64
					for i, option := range best.plan.choices {
						selected := problem.options[i][option]
						options = append(options, selected.name)
						work = append(work, selected.rangeBytes)
						limits = append(limits, problem.rangeLimits[i])
						if option == 0 {
							continue
						}
						mustOK(t, l.s.ensureFeature(agFeature))
						key := fmt.Sprintf("series/%02d", i)
						_, err = gf2CommitNativePatch(l.s, l.man(key), input[i], catalog[i][option].patch)
						mustOK(t, err)
					}
					_, err = l.s.finishAuthorityArchive(ctx, 0)
					mustOK(t, err)
					l.close()
					after, apack, _ := fb13Allocation(t, l.dir)
					l.open()
					l.exact("native global physical selection")
					l.verifyOK("native global physical selection")
					resolve := gf2SignedReader(t, l)
					var reads []int64
					for j, raw := range input {
						key := fmt.Sprintf("series/%02d", j)
						for i := range 24 {
							at := (len(raw) - 4096) * i / 23
							now := time.Now()
							got := resolve(key, at, 4096)
							reads = append(reads, time.Since(now).Nanoseconds())
							if !bytes.Equal(got, raw[at:at+4096]) {
								t.Fatal("joint signed range differs")
							}
						}
					}
					row, err := json.Marshal(map[string]any{
						"family": family.Name, "range_factor": factor, "choice": options, "range_work_upper_bytes": work, "range_caps_bytes": limits,
						"baseline_allocated": before, "baseline_pack": bpack, "allocated": after, "pack": apack,
						"modeled": best.plan.bytes, "baseline_model": problem.direct().bytes, "oracle_visits": exact.visited,
						"plan_cpu_ns": planCPU, "plan_wall_ns": planWall, "range_p50_p95_p99_ns": gf0Percentiles(reads),
					})
					mustOK(t, err)
					fmt.Printf("GF1PHYSICAL %s\n", row)
				})
			}
		})
	}
}
