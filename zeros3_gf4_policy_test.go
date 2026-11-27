package main

import (
	"encoding/json"
	"fmt"
	"math/bits"
	"slices"
	"testing"
)

const (
	gf4Keys      = 8
	gf4States    = 1 << gf4Keys
	gf4CopyBytes = 32 << 10
	// Service costs in microseconds from the pinned signed/overlay probes.
	gf4ArchiveUS = 15000
	gf4HotUS     = 150
	gf4CDCUS     = 180
	gf4BuildUS   = 45000
	gf4RentUS    = 450 // objective price per 32 KiB resident through one epoch
	gf4SLOUS     = 2500
)

// Static retained increments come from the paired signed native archive.
const (
	gf4CDCExtra     = 3563520
	gf4PatchExtra   = 585728
	gf4ColdWholeUS  = 1150000
	gf4PatchWholeUS = 21000
	gf4CDCWholeUS   = 6500
	gf4WholeSLOUS   = 50000
)

type gf4Epoch struct {
	count, whole  [gf4Keys]int
	background    int
	live          uint8
	slots, writes int
	slowExtra     int // bounded synthetic pressure, not calibrated physical cold I/O
}

type gf4Metrics struct {
	Objective       int64 `json:"objective_us"`
	Violations      int   `json:"violations"`
	Reads           int   `json:"reads"`
	P99             int64 `json:"p99_us"`
	WholeP99        int64 `json:"whole_p99_us"`
	WholeViolations int   `json:"whole_violations"`
	WholeReads      int   `json:"whole_reads"`
	CopyWrites      int   `json:"copy_writes"`
	WrittenBytes    int64 `json:"written_bytes"`
	PeakHotBytes    int   `json:"peak_hot_bytes"`
	HotByteEpochs   int64 `json:"hot_byte_epochs"`
}

func gf4Feasible(e gf4Epoch, old, next uint8) bool {
	return next & ^e.live == 0 && bits.OnesCount8(next) <= e.slots && bits.OnesCount8(next & ^old) <= e.writes
}

func gf4Service(e gf4Epoch, old, next uint8, key int, mode string, whole bool) int {
	if whole {
		switch mode {
		case "cdc":
			return gf4CDCWholeUS + e.slowExtra
		case "bitpatch":
			return gf4PatchWholeUS + e.slowExtra
		default:
			return gf4ColdWholeUS + e.slowExtra
		}
	}
	switch mode {
	case "cdc":
		return gf4CDCUS + e.slowExtra/4
	case "bitpatch":
		return 500 + e.slowExtra/4
	}
	if next&(1<<key) != 0 {
		return gf4HotUS + e.slowExtra/4
	}
	return gf4ArchiveUS + e.slowExtra + 200*bits.OnesCount8(next & ^old)
}

func gf4Retained(next uint8, mode string) int {
	switch mode {
	case "cdc":
		return gf4CDCExtra
	case "bitpatch":
		return gf4PatchExtra
	}
	return bits.OnesCount8(next) * gf4CopyBytes
}

func gf4Cost(e gf4Epoch, old, next uint8, mode string) int64 {
	score := int64(e.background * gf4CDCUS)
	for i, n := range e.count {
		score += int64(n * gf4Service(e, old, next, i, mode, false))
		score += int64(e.whole[i] * gf4Service(e, old, next, i, mode, true))
	}
	rent := (gf4Retained(next, mode)*gf4RentUS + gf4CopyBytes - 1) / gf4CopyBytes
	return score + int64(rent+bits.OnesCount8(next & ^old)*gf4BuildUS)
}

func gf4Offline(trace []gf4Epoch) []uint8 {
	const inf = 1 << 60
	prior := make([]int64, gf4States)
	next := make([]int64, gf4States)
	prev := make([][gf4States]uint8, len(trace))
	for i := 1; i < len(prior); i++ {
		prior[i] = inf
	}
	for epoch, e := range trace {
		for mask := range next {
			next[mask] = inf
		}
		for mask, score := range prior {
			if score == inf {
				continue
			}
			for candidate := range next {
				if !gf4Feasible(e, uint8(mask), uint8(candidate)) {
					continue
				}
				cost := score + gf4Cost(e, uint8(mask), uint8(candidate), "archive")
				if cost < next[candidate] {
					next[candidate] = cost
					prev[epoch][candidate] = uint8(mask)
				}
			}
		}
		prior, next = next, prior
	}
	last := slices.Index(prior, slices.Min(prior))
	out := make([]uint8, len(trace))
	for epoch := len(trace) - 1; epoch >= 0; epoch-- {
		out[epoch] = uint8(last)
		last = int(prev[epoch][last])
	}
	return out
}

type gf4Policy struct {
	history      [3][gf4Keys]int
	age          [gf4Keys]int
	last         [gf4Keys]int
	observations int
}

func (s *gf4Policy) observe(e gf4Epoch, old, next uint8) {
	s.history[s.observations%len(s.history)] = e.count
	for key, n := range e.count {
		if n > 0 {
			s.last[key] = s.observations + 1
		}
		if next&(1<<key) == 0 {
			s.age[key] = 0
		} else if old&(1<<key) == 0 {
			s.age[key] = 1
		} else {
			s.age[key]++
		}
	}
	s.observations++
}

func (s *gf4Policy) choose(method string, e gf4Epoch, old uint8) uint8 {
	if method == "archive" || method == "cdc" || method == "bitpatch" {
		return 0
	}
	var best uint8
	bestScore := int64(-1 << 60)
	for bitsIndex := range gf4States {
		mask := uint8(bitsIndex)
		if !gf4Feasible(e, old, mask) {
			continue
		}
		score := int64(0)
		for k := range gf4Keys {
			if mask&(1<<k) == 0 {
				continue
			}
			switch method {
			case "fixed-hot":
				if k == 0 {
					score += 1
				}
				if k != 0 {
					score -= 1
				}
			case "lru":
				// Preserve empty occupancy before there is an observed access.
				score += int64(s.last[k])*1000 - 1
			case "rent-aware", "responsive":
				var a, b, weight, count int
				for lag := range min(s.observations, 3) {
					n := s.history[(s.observations-1-lag)%3][k]
					w := 3 - lag
					count += w * n
					weight += w
					if lag == 0 {
						a, b = n, n
					} else {
						a, b = min(a, n), max(b, n)
					}
				}
				// A short forecast with a pessimistic allowance for phase
				// variance. Only completed epochs enter this estimate.
				prediction := max(0, count/max(weight, 1)-(b-a)/4)
				if method == "responsive" && s.observations > 0 {
					last := s.history[(s.observations-1)%3]
					sum := 0
					for _, n := range last {
						sum += n
					}
					if last[k] >= 12 && 4*last[k] >= 3*sum {
						prediction = max(prediction, last[k])
					}
				}
				score += int64(3 * (prediction*(gf4ArchiveUS-gf4HotUS) - gf4RentUS))
				if old&(1<<k) == 0 {
					score -= gf4BuildUS
				} else if s.age[k] < 3 {
					score += gf4BuildUS / 2
				}
			}
		}
		if score > bestScore {
			bestScore, best = score, mask
		}
	}
	return best
}

func gf4Replay(trace []gf4Epoch, mode string) gf4Metrics {
	var result gf4Metrics
	var latencies, wholeLatencies []int64
	var old uint8
	var online gf4Policy
	var offline []uint8
	if mode == "offline" {
		offline = gf4Offline(trace)
	}
	for i, e := range trace {
		var next uint8
		if offline != nil {
			next = offline[i]
		} else {
			next = online.choose(mode, e, old)
		}
		if !gf4Feasible(e, old, next) {
			panic("workload policy exceeded resource budget")
		}
		result.Objective += gf4Cost(e, old, next, mode)
		retained := gf4Retained(next, mode)
		result.PeakHotBytes = max(result.PeakHotBytes, retained)
		result.HotByteEpochs += int64(retained)
		result.CopyWrites += bits.OnesCount8(next & ^old)
		for _ = range e.background {
			latencies = append(latencies, gf4CDCUS)
		}
		result.Reads += e.background
		for k, visits := range e.count {
			delay := gf4Service(e, old, next, k, mode, false)
			for _ = range visits {
				latencies = append(latencies, int64(delay))
			}
			if delay > gf4SLOUS {
				result.Violations += visits
			}
			result.Reads += visits
			if e.whole[k] > 0 {
				delay := gf4Service(e, old, next, k, mode, true)
				for range e.whole[k] {
					wholeLatencies = append(wholeLatencies, int64(delay))
				}
				if delay > gf4WholeSLOUS {
					result.WholeViolations += e.whole[k]
				}
				result.WholeReads += e.whole[k]
			}
		}
		online.observe(e, old, next)
		old = next
	}
	result.P99 = fb14Quantile(latencies, 99)
	result.WholeP99 = fb14Quantile(wholeLatencies, 99)
	result.WrittenBytes = int64(result.CopyWrites) * gf4CopyBytes
	return result
}

func gf4Trace(name string) []gf4Epoch {
	trace := make([]gf4Epoch, 48)
	for i := range trace {
		e := gf4Epoch{live: 255, slots: 2, writes: 1, background: 8}
		switch name {
		case "stable-small-hot-set":
			e.count[3] = 36
			e.count[i%gf4Keys]++
		case "rotate":
			e.count[(i/8)%gf4Keys] = 34
			e.count[(i/8+3)%gf4Keys] = 4
		case "surprise":
			key := 2
			if i >= 16 && i < 29 {
				key = 7
			}
			e.count[key] = 64
		case "archive-restore":
			if i%7 < 4 {
				e.count[i%gf4Keys] = 6
			} else {
				for k := range gf4Keys {
					e.count[k] = 4
				}
				e.slowExtra = 6000
				e.whole[(i/7)%gf4Keys] = 1
			}
		case "prune-and-pressure":
			if i >= 24 {
				e.live = 0b11110000
				e.slots = 1
			}
			if i >= 40 {
				e.writes = 0
			}
			e.count[bits.TrailingZeros8(e.live)] = 17
			e.count[7] = 7
			if i%8 < 4 {
				e.slowExtra = 3000
			}
		case "replication-maintenance":
			e.count[1] = 25
			e.count[0] = i % 9
			if i%6 < 2 {
				e.writes = 0
				e.slowExtra = 9000
			}
		}
		trace[i] = e
	}
	return trace
}

func TestGF4OfflineOptimality(t *testing.T) {
	trace := gf4Trace("rotate")[:3]
	var visit func(int, uint8, int64)
	best := int64(1 << 60)
	visit = func(epoch int, prior uint8, cost int64) {
		if epoch == len(trace) {
			best = min(best, cost)
			return
		}
		for mask := range gf4States {
			e := trace[epoch]
			if gf4Feasible(e, prior, uint8(mask)) {
				visit(epoch+1, uint8(mask), cost+gf4Cost(e, prior, uint8(mask), "archive"))
			}
		}
	}
	visit(0, 0, 0)
	got := gf4Replay(trace, "offline")
	if got.Objective != best {
		t.Fatalf("offline objective %d, exhaustive %d", got.Objective, best)
	}
}

func TestGF4WorkloadReplay(t *testing.T) {
	for _, name := range []string{"stable-small-hot-set", "rotate", "surprise", "archive-restore", "prune-and-pressure", "replication-maintenance"} {
		trace := gf4Trace(name)
		optimum := gf4Replay(trace, "offline").Objective
		for _, mode := range []string{"cdc", "bitpatch", "archive", "fixed-hot", "lru", "rent-aware", "responsive", "offline"} {
			result := gf4Replay(trace, mode)
			regret := result.Objective - optimum
			if mode != "cdc" && mode != "bitpatch" && regret < 0 {
				t.Fatalf("online %s beat exact comparable optimum", mode)
			}
			row, err := json.Marshal(map[string]any{"scenario": name, "policy": mode, "metrics": result, "regret_vs_archive_oracle_us": regret})
			mustOK(t, err)
			fmt.Printf("GF4 %s\n", row)
		}
	}
}
