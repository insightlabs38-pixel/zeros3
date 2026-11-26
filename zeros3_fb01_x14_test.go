package main

// FB-01 E1: the X14 crash and damage scenarios (S1-S8, S3x) on the real engine.
// Each trial damages a copy of a real store's journal the way the X14 model
// (F6 exp/x14-journal) does, then opens it under the strict default and the
// test-only historical policies P0 (2337f48) and P0R (P0 plus resync guard).
// FB01_X14_TRIALS sets trials per scenario (default 4); summaries go to
// FB01_RESULTS. v1 has no seal, so S3x differs from S3 only in that no
// checkpoint is present.

import (
	"encoding/json"
	"fmt"
	"maps"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"
)

const fb01Sector = 512

type fb01X14Group struct {
	start, end int64
	fresh      []string   // keys this group put that no earlier group touched
	truth      *fb01Truth // acknowledged state once the group's fsync returned
}

type fb01X14Base struct {
	dir    string
	data   []byte
	frames []fb01Frame
	groups []fb01X14Group
	ckOff  int64 // end of the checkpoint's covered prefix, 0 for none
}

// fb01X14Build writes 4-11 group commits of 1-8 frames (sometimes 10-29) to a
// real store: fresh puts, overwrites and deletes, the final group fresh puts
// only. With ck, a namespace checkpoint is published after an earlier group.
func fb01X14Build(t testing.TB, seed uint64, ck bool) *fb01X14Base {
	t.Helper()
	r := mrand.New(mrand.NewPCG(seed, 0x14))
	b := &fb01X14Base{dir: t.TempDir()}
	s, err := openStore(b.dir, true, true)
	if err != nil {
		t.Fatal(err)
	}
	s.ns.auto = false
	off := func() int64 {
		s.journal.mu.Lock()
		defer s.journal.mu.Unlock()
		return s.journal.writeOffset
	}
	tr := newFB01Truth()
	mark := func(start int64, fresh ...string) {
		b.groups = append(b.groups, fb01X14Group{start: start, end: off(), fresh: fresh, truth: tr.clone()})
	}
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	mark(0)
	n, next := 4+r.IntN(8), 0
	ckAt := -1
	if ck {
		ckAt = 1 + r.IntN(n-2)
	}
	val := func(k string) string { return fmt.Sprintf("%s/%d/%0*d", k, seed, r.IntN(400), 0) }
	for g := range n {
		final := g == n-1
		live := slices.Sorted(maps.Keys(tr.live))
		switch x := r.IntN(8); {
		case !final && x == 0 && len(live) > 0:
			pre := off()
			tr.del(t, s, live[r.IntN(len(live))])
			mark(pre)
		case !final && x == 1 && len(live) > 0:
			pre, k := off(), live[r.IntN(len(live))]
			tr.put(t, s, k, val(k))
			mark(pre)
		default:
			nf := 1 + r.IntN(8)
			if r.IntN(10) == 0 {
				nf = 10 + r.IntN(20)
			}
			fresh := func() string { next++; return fmt.Sprintf("o%04d", next) }
			if nf == 1 {
				pre, k := off(), fresh()
				tr.put(t, s, k, val(k))
				mark(pre, k)
				break
			}
			gate := fresh()
			kv := make([][2]string, nf-1)
			keys := make([]string, nf-1)
			for i := range kv {
				keys[i] = fresh()
				kv[i] = [2]string{keys[i], val(keys[i])}
			}
			before := tr.clone()
			pre := off()
			gs, ge := fb01PutGroup(t, s, tr, gate, kv)
			before.live[gate] = "gate"
			b.groups = append(b.groups, fb01X14Group{start: pre, end: gs, fresh: []string{gate}, truth: before})
			b.groups = append(b.groups, fb01X14Group{start: gs, end: ge, fresh: keys, truth: tr.clone()})
		}
		if g == ckAt {
			if err := s.nsConsolidate(true); err != nil {
				t.Fatal(err)
			}
			b.ckOff = off()
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if b.data, err = os.ReadFile(fb01Journal(b.dir)); err != nil {
		t.Fatal(err)
	}
	b.frames = fb01Frames(b.data)
	if last := b.groups[len(b.groups)-1]; last.end != int64(len(b.data)) || b.frames[len(b.frames)-1].end != last.end {
		t.Fatalf("group map does not cover the journal: %d vs %d", last.end, len(b.data))
	}
	return b
}

func (b *fb01X14Base) groupFrames(g int) []fb01Frame {
	var out []fb01Frame
	for _, f := range b.frames {
		if f.off >= b.groups[g].start && f.end <= b.groups[g].end {
			out = append(out, f)
		}
	}
	return out
}

// fb01X14Crash is X14 crashDuring: group g was written but its fsync never
// returned; the file keeps some of its sectors in one of three patterns.
func fb01X14Crash(r *mrand.Rand, b *fb01X14Base, g int, garbage bool) []byte {
	a, e := b.groups[g].start, b.groups[g].end
	d := append([]byte(nil), b.data[:a]...)
	var size int64
	switch r.IntN(4) {
	case 0:
		size = a
	case 1:
		size = e
	case 2:
		size = min(a+int64(r.IntN(int((e-a)/fb01Sector)+1))*fb01Sector, e)
	default:
		size = a + 1 + r.Int64N(e-a)
	}
	mode, p := r.IntN(3), []float64{0.1, 0.5, 0.9}[r.IntN(3)]
	cut := a + r.Int64N(e-a+1)
	for off := a; off < size; off += fb01Sector {
		end := min(off+fb01Sector, size)
		keep := mode == 2 || mode == 0 && off < cut || mode == 1 && r.Float64() < p
		switch {
		case keep:
			d = append(d, b.data[off:end]...)
		case garbage && r.IntN(3) == 0:
			gb := make([]byte, end-off)
			for i := range gb {
				gb[i] = byte(r.IntN(256))
			}
			d = append(d, gb...)
		default:
			d = append(d, make([]byte, end-off)...)
		}
	}
	return d
}

// fb01X14Corrupt is X14 corruptFrame: media damage to one durable frame.
func fb01X14Corrupt(r *mrand.Rand, d []byte, f fb01Frame) string {
	switch r.IntN(4) {
	case 0:
		d[f.off+r.Int64N(f.end-f.off)] ^= 1 << r.IntN(8)
		return "bit flip"
	case 1:
		s := (f.off + r.Int64N(f.end-f.off)) / fb01Sector * fb01Sector
		for i := s; i < s+fb01Sector && i < int64(len(d)); i++ {
			d[i] = 0
		}
		return "zeroed sector"
	case 2:
		s := (f.off + r.Int64N(f.end-f.off)) / fb01Sector * fb01Sector
		for i := s; i < s+fb01Sector && i < int64(len(d)); i++ {
			d[i] = byte(r.IntN(256))
		}
		return "garbage sector"
	default:
		d[f.off+16+int64(r.IntN(4))] ^= 1 << r.IntN(8)
		return "length bit flip"
	}
}

type fb01X14Case struct {
	data    []byte
	acked   int  // last acknowledged group
	torn    int  // the torn, unacknowledged group, or -1
	dropCk  bool // remove the checkpoint first (X14 keeps it out of the way)
	residue bool // S6 above every checkpoint: undetectable in v1
	detail  string
}

var fb01X14Scenarios = []struct {
	name string
	make func(r *mrand.Rand, b *fb01X14Base) fb01X14Case
}{
	{"S1 crash: torn final group", func(r *mrand.Rand, b *fb01X14Base) fb01X14Case {
		g := len(b.groups) - 1
		return fb01X14Case{data: fb01X14Crash(r, b, g, false), acked: g - 1, torn: g}
	}},
	{"S2 crash: torn final group, stale garbage sectors", func(r *mrand.Rand, b *fb01X14Base) fb01X14Case {
		g := len(b.groups) - 1
		return fb01X14Case{data: fb01X14Crash(r, b, g, true), acked: g - 1, torn: g}
	}},
	{"S3 media: acknowledged final group damaged (clean close)", func(r *mrand.Rand, b *fb01X14Base) fb01X14Case {
		g := len(b.groups) - 1
		d := append([]byte(nil), b.data...)
		fs := b.groupFrames(g)
		return fb01X14Case{data: d, acked: g, torn: -1, detail: fb01X14Corrupt(r, d, fs[r.IntN(len(fs))])}
	}},
	{"S3x media: final group damaged right after its ack", func(r *mrand.Rand, b *fb01X14Base) fb01X14Case {
		g := len(b.groups) - 1
		d := append([]byte(nil), b.data...)
		fs := b.groupFrames(g)
		return fb01X14Case{data: d, acked: g, torn: -1, dropCk: true, detail: fb01X14Corrupt(r, d, fs[r.IntN(len(fs))])}
	}},
	{"S4 media: acknowledged earlier group damaged", func(r *mrand.Rand, b *fb01X14Base) fb01X14Case {
		g := len(b.groups) - 1
		d := append([]byte(nil), b.data...)
		fs := b.groupFrames(r.IntN(g))
		f := fs[r.IntN(len(fs))]
		return fb01X14Case{data: d, acked: g, torn: -1, dropCk: f.off < b.ckOff, detail: fb01X14Corrupt(r, d, f)}
	}},
	{"S5 double: crash in final group + damage to the previous", func(r *mrand.Rand, b *fb01X14Base) fb01X14Case {
		g := len(b.groups) - 1
		d := fb01X14Crash(r, b, g, false)
		fs := b.groupFrames(g - 1)
		return fb01X14Case{data: d, acked: g - 1, torn: g, dropCk: true, detail: fb01X14Corrupt(r, d, fs[r.IntN(len(fs))])}
	}},
	{"S6 truncation at a frame boundary below the acknowledged end", func(r *mrand.Rand, b *fb01X14Base) fb01X14Case {
		cut := b.frames[r.IntN(len(b.frames)-1)].end
		return fb01X14Case{data: append([]byte(nil), b.data[:cut]...), acked: len(b.groups) - 1, torn: -1, residue: cut >= b.ckOff}
	}},
	{"S7 truncation mid-frame below the acknowledged end", func(r *mrand.Rand, b *fb01X14Base) fb01X14Case {
		f := b.frames[r.IntN(len(b.frames))]
		cut := f.off + 1 + r.Int64N(f.end-f.off-1)
		return fb01X14Case{data: append([]byte(nil), b.data[:cut]...), acked: len(b.groups) - 1, torn: -1}
	}},
	{"S8 crash: torn final group, stale sectors of this journal", func(r *mrand.Rand, b *fb01X14Base) fb01X14Case {
		g := len(b.groups) - 1
		d := fb01X14Crash(r, b, g, false)
		a := b.groups[g].start
		for off := a; off+fb01Sector <= int64(len(d)) && a >= fb01Sector; off += fb01Sector {
			if fb01AllZero(d[off : off+fb01Sector]) {
				src := r.Int64N(a/fb01Sector) * fb01Sector
				copy(d[off:off+fb01Sector], b.data[src:src+fb01Sector])
			}
		}
		return fb01X14Case{data: d, acked: g - 1, torn: g}
	}},
}

func fb01AllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

type fb01X14Mode struct {
	name   string
	owner  bool
	policy *journalPolicy
}

var fb01X14Modes = []fb01X14Mode{
	{"strict owner", true, nil},
	{"strict shared", false, nil},
	{"P0R owner", true, &journalPolicyP0R},
	{"P0 owner (2337f48)", true, &journalPolicyP0},
}

// fb01X14Tally is one summary row: a scenario under one mode.
type fb01X14Tally struct {
	Kind          string  `json:"kind"`
	Build         string  `json:"build"`
	Scenario      string  `json:"scenario"`
	Mode          string  `json:"mode"`
	Trials        int     `json:"trials"`
	OK            int     `json:"ok"`
	Refuse        int     `json:"refuse"`
	Prefix        int     `json:"prefix"`
	Loss          int     `json:"loss"`
	LossResidual  int     `json:"loss_s6_residual"`
	AckedObjects  int     `json:"acked_mutations"`
	LostObjects   int     `json:"lost_mutations"`
	PartialTorn   int     `json:"torn_group_partly_accepted"`
	WholeTorn     int     `json:"torn_group_wholly_accepted"`
	Preserved     int     `json:"journal_preserved"`
	OpenMedianUS  int64   `json:"open_median_us"`
	OpenP95US     int64   `json:"open_p95_us"`
	RefusePct     float64 `json:"refuse_pct"`
	LossPct       float64 `json:"loss_pct"`
	LostObjectPct float64 `json:"lost_mutation_pct"`
}

func TestFB01X14Engine(t *testing.T) {
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
	work := t.TempDir()
	for si, sc := range fb01X14Scenarios {
		tallies := make([]fb01X14Tally, len(fb01X14Modes))
		opens := make([][]int64, len(fb01X14Modes))
		r := mrand.New(mrand.NewPCG(37, uint64(si)))
		for trial := range trials {
			b := bases[trial%len(bases)]
			c := sc.make(r, b)
			tr := b.groups[c.acked].truth
			for mi, m := range fb01X14Modes {
				dir := filepath.Join(work, fmt.Sprintf("s%d-t%d-m%d", si, trial, mi))
				if err := os.CopyFS(dir, os.DirFS(b.dir)); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(fb01Journal(dir), c.data, 0o644); err != nil {
					t.Fatal(err)
				}
				if c.dropCk {
					for _, n := range []string{nsGenName, namespaceCompactCheckpointName, namespaceCheckpointName} {
						os.Remove(filepath.Join(dir, "journal", n))
					}
				}
				sha := fb01SHABytes(c.data)
				testJournalPolicy = m.policy
				start := time.Now()
				s, err := openStore(dir, m.owner, true)
				us := time.Since(start).Microseconds()
				tl := &tallies[mi]
				tl.Trials++
				opens[mi] = append(opens[mi], us)
				outcome, lost := "REFUSE", 0
				if err == nil {
					lost, _ = tr.lost(s)
					outcome = "OK"
					if lost > 0 {
						outcome = "LOSS"
						if s.JournalTail() != nil {
							outcome = "PREFIX"
						}
					}
					if c.torn >= 0 {
						seen := 0
						for _, k := range b.groups[c.torn].fresh {
							if _, _, gerr := s.GetObject("b", k); gerr == nil {
								seen++
							}
						}
						switch {
						case seen == len(b.groups[c.torn].fresh):
							tl.WholeTorn++
						case seen > 0:
							tl.PartialTorn++
						}
					}
					s.Close()
				}
				testJournalPolicy = nil
				tl.AckedObjects += tr.acked()
				tl.LostObjects += lost
				switch outcome {
				case "OK":
					tl.OK++
				case "REFUSE":
					tl.Refuse++
				case "PREFIX":
					tl.Prefix++
				case "LOSS":
					tl.Loss++
					if c.residue {
						tl.LossResidual++
					}
				}
				after, _ := os.ReadFile(fb01Journal(dir))
				preserved := fb01SHABytes(after) == sha
				if preserved {
					tl.Preserved++
				}
				if m.policy == nil {
					switch {
					case outcome == "LOSS" && !c.residue:
						t.Errorf("%s | %s | trial %d (%s): silent loss of %d acknowledged mutations", sc.name, m.name, trial, c.detail, lost)
					case !preserved:
						t.Errorf("%s | %s | trial %d: the strict open changed the journal", sc.name, m.name, trial)
					case outcome == "PREFIX" && m.owner:
						t.Errorf("%s | %s | trial %d: an owner served a prefix", sc.name, m.name, trial)
					}
				}
				os.RemoveAll(dir)
			}
		}
		for mi, m := range fb01X14Modes {
			tl := &tallies[mi]
			tl.Kind, tl.Build, tl.Scenario, tl.Mode = "x14-engine-summary", os.Getenv("FB01_BUILD"), sc.name, m.name
			slices.Sort(opens[mi])
			tl.OpenMedianUS = opens[mi][len(opens[mi])/2]
			tl.OpenP95US = opens[mi][len(opens[mi])*95/100]
			tl.RefusePct = 100 * float64(tl.Refuse) / float64(tl.Trials)
			tl.LossPct = 100 * float64(tl.Loss) / float64(tl.Trials)
			tl.LostObjectPct = 100 * float64(tl.LostObjects) / float64(max(tl.AckedObjects, 1))
			t.Logf("%-62s %-20s OK %3d REFUSE %3d PREFIX %3d LOSS %3d (S6 residual %d) lost %d/%d torn group partly/wholly accepted %d/%d preserved %d/%d open median %dus",
				sc.name, m.name, tl.OK, tl.Refuse, tl.Prefix, tl.Loss, tl.LossResidual, tl.LostObjects, tl.AckedObjects, tl.PartialTorn, tl.WholeTorn, tl.Preserved, tl.Trials, tl.OpenMedianUS)
			fb01EmitJSON(t, tl)
		}
	}
}

// fb01EmitJSON appends any row to FB01_RESULTS.
func fb01EmitJSON(t testing.TB, v any) {
	t.Helper()
	path := os.Getenv("FB01_RESULTS")
	if path == "" {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	fb01Mu.Lock()
	defer fb01Mu.Unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}
