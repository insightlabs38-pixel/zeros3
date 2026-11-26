package main

// FB-01R E1: the X14 scenarios on the real engine with the ack watermark. Each
// trial pairs the damaged journal with the watermark the writer would have left
// at that moment: before the torn group for a crash scenario, at the end for
// damage to acknowledged history. Three conditions: the same kernel boot (a
// process kill), a host restart in lazy mode, and a host restart in sync mode.
// X14's crash shapes (zeroed, garbage and stale sectors) are power-loss
// shapes; within one boot only a prefix cut (S0) is possible, so the same-boot
// rows for S1, S2 and S8 measure the guard against impossible shapes.

import (
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

// fb01rSealFor is the slot a writer leaves when the journal ends at off.
func fb01rSealFor(t testing.TB, b *fb01X14Base, off int64, synced bool) []byte {
	t.Helper()
	w := journalWatermark{Gen: 1000, Boot: currentBootID(), Store: storeTag(fb01rStoreID(t, b.dir)), Offset: off, PID: 1}
	if synced {
		w.Flags |= wmFlagSynced
	}
	if off > 0 {
		i := slices.IndexFunc(b.frames, func(f fb01Frame) bool { return f.end == off })
		if i < 0 {
			t.Fatalf("no frame ends at %d", off)
		}
		w.FrameOff, w.Seq = b.frames[i].off, b.frames[i].seq
		w.LastCRC = uint32(b.data[off-4])<<24 | uint32(b.data[off-3])<<16 | uint32(b.data[off-2])<<8 | uint32(b.data[off-1])
	}
	return w.encode()
}

var fb01rX14Extra = []struct {
	name string
	make func(r *mrand.Rand, b *fb01X14Base) fb01X14Case
}{
	{"S0 process kill: final group cut at any byte", func(r *mrand.Rand, b *fb01X14Base) fb01X14Case {
		g := len(b.groups) - 1
		a, e := b.groups[g].start, b.groups[g].end
		return fb01X14Case{data: append([]byte(nil), b.data[:a+r.Int64N(e-a+1)]...), acked: g - 1, torn: g}
	}},
}

type fb01rX14Tally struct {
	Kind         string  `json:"kind"`
	Build        string  `json:"build"`
	Scenario     string  `json:"scenario"`
	Condition    string  `json:"condition"`
	Mode         string  `json:"mode"`
	Trials       int     `json:"trials"`
	OK           int     `json:"ok"`
	Recover      int     `json:"recover"` // owner discarded a proven tail, or shared served a proven prefix
	Refuse       int     `json:"refuse"`
	RefuseBelow  int     `json:"refuse_below_watermark"`
	Prefix       int     `json:"degraded_prefix"`
	Loss         int     `json:"loss"`
	AckedObjects int     `json:"acked_mutations"`
	LostObjects  int     `json:"lost_mutations"`
	TailBytes    int64   `json:"discarded_bytes"`
	Preserved    int     `json:"refusals_preserved"`
	OpenMedianUS int64   `json:"open_median_us"`
	RefusePct    float64 `json:"refuse_pct"`
	RecoverPct   float64 `json:"recover_pct"`
}

func TestFB01RX14Engine(t *testing.T) {
	fb01NoTailWait(t)
	trials := 4
	if v, err := strconv.Atoi(os.Getenv("FB01_X14_TRIALS")); err == nil && v > 0 {
		trials = v
	}
	if testing.Short() {
		trials = 1
	}
	var bases []*fb01X14Base
	for i := range 6 {
		bases = append(bases, fb01X14Build(t, uint64(1000+i), i%2 == 0))
	}
	scenarios := append(slices.Clone(fb01rX14Extra), fb01X14Scenarios...)
	conditions := []string{"same boot", "restart lazy", "restart sync"}
	modes := []bool{true, false}
	work := t.TempDir()
	for si, sc := range scenarios {
		for ci, cond := range conditions {
			for _, owner := range modes {
				tl := fb01rX14Tally{Kind: "x14r-engine-summary", Build: os.Getenv("FB01_BUILD"), Scenario: sc.name, Condition: cond, Mode: fb01ModeName(owner)}
				var opens []int64
				r := mrand.New(mrand.NewPCG(37, uint64(si))) // the same images as FB-01's E1 for S1-S8
				for trial := range trials {
					b := bases[trial%len(bases)]
					c := sc.make(r, b)
					tr := b.groups[c.acked].truth
					seal := int64(len(b.data))
					if c.torn >= 0 {
						seal = b.groups[c.torn].start
					}
					dir := filepath.Join(work, fmt.Sprintf("s%d-c%d-t%d-%t", si, ci, trial, owner))
					if err := os.CopyFS(dir, os.DirFS(b.dir)); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(fb01Journal(dir), c.data, 0o644); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(fb01rAckPath(dir), fb01rSealFor(t, b, seal, cond == "restart sync"), 0o644); err != nil {
						t.Fatal(err)
					}
					if c.dropCk {
						for _, n := range []string{nsGenName, namespaceCompactCheckpointName, namespaceCheckpointName} {
							os.Remove(filepath.Join(dir, "journal", n))
						}
					}
					if cond != "same boot" {
						other := currentBootID()
						other[0] ^= 0xff
						testBootID = &other
					}
					start := time.Now()
					s, err := openStore(dir, owner, true)
					opens = append(opens, time.Since(start).Microseconds())
					testBootID = nil
					tl.Trials++
					tl.AckedObjects += tr.acked()
					outcome := "REFUSE"
					var wp *watermarkProblem
					if err != nil {
						if errors.As(err, &wp) {
							tl.RefuseBelow++
						}
						if fb01SHA(fb01Journal(dir)) == fb01SHABytes(c.data) {
							tl.Preserved++
						} else {
							t.Errorf("%s | %s | %s | trial %d: a refusal changed the journal", sc.name, cond, tl.Mode, trial)
						}
					} else {
						lost, _ := tr.lost(s)
						tl.LostObjects += lost
						outcome = "OK"
						switch {
						case lost > 0 && s.Degraded() != nil:
							outcome = "PREFIX"
						case lost > 0:
							outcome = "LOSS"
						case s.JournalRecovery() != nil || s.JournalTailProven():
							outcome = "RECOVER"
						case s.Degraded() != nil:
							outcome = "PREFIX"
						}
						if rec := s.JournalRecovery(); rec != nil {
							tl.TailBytes += rec.Bytes
							if rec.Offset < seal {
								t.Errorf("%s | %s | trial %d: recovered below the watermark", sc.name, cond, trial)
							}
							if fb01SHA(fb01Journal(dir)) != fb01SHABytes(c.data[:rec.Offset]) {
								t.Errorf("%s | %s | trial %d: the kept journal is not the prefix", sc.name, cond, trial)
							}
						}
						s.Close()
					}
					switch outcome {
					case "OK":
						tl.OK++
					case "RECOVER":
						tl.Recover++
					case "REFUSE":
						tl.Refuse++
					case "PREFIX":
						tl.Prefix++
					case "LOSS":
						tl.Loss++
						t.Errorf("%s | %s | %s | trial %d (%s): silent loss of acknowledged mutations", sc.name, cond, tl.Mode, trial, c.detail)
					}
					crash := c.torn >= 0
					damaged := !crash || c.detail != "" // acknowledged history cut or damaged
					switch {
					case damaged && outcome != "REFUSE":
						t.Errorf("%s | %s | %s | trial %d: damage to acknowledged history was not refused (%s)", sc.name, cond, tl.Mode, trial, outcome)
					case damaged && wp == nil && checkpointKind(err) != checkpointContradicts:
						// The checkpoint's binding may catch a cut below it first.
						t.Errorf("%s | %s | %s | trial %d: refused without naming the watermark or the checkpoint: %v", sc.name, cond, tl.Mode, trial, err)
					case crash && sc.name == fb01rX14Extra[0].name && cond == "same boot" && outcome != "OK" && outcome != "RECOVER":
						t.Errorf("%s | %s | trial %d: a process kill was not recovered: %v", tl.Mode, sc.name, trial, err)
					case !damaged && cond == "restart sync" && outcome != "OK" && outcome != "RECOVER":
						t.Errorf("%s | %s | %s | trial %d: sync mode did not recover: %v", sc.name, cond, tl.Mode, trial, err)
					case crash && cond == "restart lazy" && outcome == "RECOVER":
						t.Errorf("%s | %s | %s | trial %d: lazy mode recovered after a restart", sc.name, cond, tl.Mode, trial)
					}
					os.RemoveAll(dir)
				}
				slices.Sort(opens)
				tl.OpenMedianUS = opens[len(opens)/2]
				tl.RefusePct = 100 * float64(tl.Refuse) / float64(tl.Trials)
				tl.RecoverPct = 100 * float64(tl.Recover) / float64(tl.Trials)
				t.Logf("%-58s %-13s %-6s OK %3d RECOVER %3d REFUSE %3d (below %3d) PREFIX %3d LOSS %d lost %d/%d discarded %dB open median %dus",
					sc.name, cond, tl.Mode, tl.OK, tl.Recover, tl.Refuse, tl.RefuseBelow, tl.Prefix, tl.Loss, tl.LostObjects, tl.AckedObjects, tl.TailBytes, tl.OpenMedianUS)
				fb01EmitJSON(t, tl)
			}
		}
	}
}
