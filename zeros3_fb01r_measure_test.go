package main

// FB-01R measurements: the cost of each durability design. Runs only with
// FB01R_MEASURE=1 and writes rows to FB01_RESULTS (kind fb01r-measure-*).
//
//   - journal: one group of k frames per append, for v1 (no watermark),
//     Design A (ack watermark, lazy and sync) and Design B (the v2 prototype,
//     piggyback and sync seal): latency, throughput and allocations;
//   - syscalls: fsync and pwrite64 per group, counted with strace on a child
//     process (difference of two run lengths, so open and close cancel);
//   - engine: PutObject with 1, 8 and 32 writers per watermark mode;
//   - restart: owner open time over a long journal, with and without a torn
//     tail, and the v2 recovery scan over the same frames.
//
// Timings depend on the machine; the README records the one measured.

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fb01rAppender interface {
	appendGroup(payloads [][]byte) error
	close() error
}

type fb01rV1 struct{ j *Journal }

func (a fb01rV1) appendGroup(payloads [][]byte) error {
	in := make([]journalFrameIn, len(payloads))
	for i, p := range payloads {
		in[i] = journalFrameIn{recType: recordTypePutObjectRoot, payload: p}
	}
	_, err := a.j.appendFrames(in)
	return err
}
func (a fb01rV1) close() error { return a.j.close() }

type fb01rV2 struct{ j *v2Journal }

func (a fb01rV2) appendGroup(payloads [][]byte) error { return a.j.append(payloads) }
func (a fb01rV2) close() error                        { return a.j.close() }

var fb01rDesigns = []string{"v1", "A-lazy", "A-sync", "B-piggyback", "B-sync"}

// fb01rOpenDesign opens an empty journal of one design in dir. The caller
// sets ZEROS3_JOURNAL_WATERMARK through fb01rDesignEnv first.
func fb01rOpenDesign(t testing.TB, design, dir string) fb01rAppender {
	t.Helper()
	path := filepath.Join(dir, "journal.log")
	switch design {
	case "B-piggyback", "B-sync":
		j, err := v2Create(path, design == "B-sync")
		if err != nil {
			t.Fatal(err)
		}
		return fb01rV2{j}
	}
	id := "fb01r-measure"
	if design == "v1" {
		id = "" // no watermark: the FB-01 journal
	}
	j, applyErr, err := openJournalOpts(path, journalOpenOpts{storeID: id, apply: func(journalRecord) error { return nil }})
	if err != nil || applyErr != nil {
		t.Fatal(err, applyErr)
	}
	return fb01rV1{j}
}

func fb01rDesignEnv(design string) string {
	switch design {
	case "A-lazy":
		return "lazy"
	case "A-sync":
		return "sync"
	}
	return "off"
}

func fb01rPayloads(k, size int) [][]byte {
	out := make([][]byte, k)
	for i := range out {
		out[i] = make([]byte, size)
		rand.Read(out[i])
	}
	return out
}

func fb01rPct(d []time.Duration, p float64) float64 {
	if len(d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(p * float64(len(s)-1))
	return float64(s[i].Microseconds())
}

func fb01rMedian(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func TestFB01RMeasure(t *testing.T) {
	if os.Getenv("FB01R_MEASURE") == "" {
		t.Skip("set FB01R_MEASURE=1 to measure")
	}
	t.Run("journal", fb01rMeasureJournal)
	t.Run("syscalls", fb01rMeasureSyscalls)
	t.Run("engine", fb01rMeasureEngine)
	t.Run("restart", fb01rMeasureRestart)
}

// fb01rMeasureJournal: groups of 1 and 8 frames of 300 bytes, designs
// interleaved over three rounds so drift in the disk hits each alike.
func fb01rMeasureJournal(t *testing.T) {
	const rounds, perRound, size = 5, 600, 300
	for _, k := range []int{1, 8} {
		payloads := fb01rPayloads(k, size)
		lat := map[string][]time.Duration{}
		tput := map[string][]float64{}
		for r := 0; r < rounds; r++ {
			for _, d := range fb01rDesigns {
				t.Setenv("ZEROS3_JOURNAL_WATERMARK", fb01rDesignEnv(d))
				a := fb01rOpenDesign(t, d, t.TempDir())
				start := time.Now()
				for i := 0; i < perRound; i++ {
					t0 := time.Now()
					if err := a.appendGroup(payloads); err != nil {
						t.Fatal(err)
					}
					lat[d] = append(lat[d], time.Since(t0))
				}
				tput[d] = append(tput[d], float64(perRound)/time.Since(start).Seconds())
				a.close()
			}
		}
		for _, d := range fb01rDesigns {
			t.Setenv("ZEROS3_JOURNAL_WATERMARK", fb01rDesignEnv(d))
			a := fb01rOpenDesign(t, d, t.TempDir())
			allocs := testing.AllocsPerRun(200, func() {
				if err := a.appendGroup(payloads); err != nil {
					t.Fatal(err)
				}
			})
			a.close()
			tp := append([]float64(nil), tput[d]...)
			sort.Float64s(tp)
			row := map[string]any{"kind": "fb01r-measure-journal", "design": d, "frames_per_group": k, "payload_bytes": size,
				"groups": rounds * perRound, "ack_p50_us": fb01rPct(lat[d], 0.5), "ack_p99_us": fb01rPct(lat[d], 0.99),
				"groups_per_s": fb01rMedian(tp), "groups_per_s_min": tp[0], "groups_per_s_max": tp[len(tp)-1], "allocs_per_group": allocs}
			t.Logf("journal k=%d %-12s p50 %6.0f us  p99 %6.0f us  %7.0f groups/s (%5.0f-%5.0f)  %4.1f allocs", k, d,
				row["ack_p50_us"], row["ack_p99_us"], row["groups_per_s"], tp[0], tp[len(tp)-1], allocs)
			fb01EmitJSON(t, row)
		}
	}
}

// TestFB01RMeasureHelper is the strace child: it appends FB01R_N groups of
// one 300-byte frame in the design FB01R_DESIGN, then exits.
func TestFB01RMeasureHelper(t *testing.T) {
	d := os.Getenv("FB01R_DESIGN")
	if d == "" {
		t.Skip("helper process")
	}
	n, _ := strconv.Atoi(os.Getenv("FB01R_N"))
	os.Setenv("ZEROS3_JOURNAL_WATERMARK", fb01rDesignEnv(d))
	a := fb01rOpenDesign(t, d, t.TempDir())
	payloads := fb01rPayloads(1, 300)
	for i := 0; i < n; i++ {
		if err := a.appendGroup(payloads); err != nil {
			t.Fatal(err)
		}
	}
	a.close()
}

var fb01rStraceRow = regexp.MustCompile(`^\s*[0-9.]+\s+[0-9.]+\s+\d+\s+(\d+)\s+(?:\d+\s+)?(\w+)\s*$`)

func fb01rStrace(t *testing.T, design string, n int) map[string]int {
	t.Helper()
	out := filepath.Join(t.TempDir(), "strace")
	cmd := exec.Command("strace", "-f", "-c", "-o", out, "-e", "trace=fsync,fdatasync,pwrite64,write",
		os.Args[0], "-test.run=^TestFB01RMeasureHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "FB01R_DESIGN="+design, "FB01R_N="+strconv.Itoa(n), "FB01_RESULTS=")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("strace %s: %v\n%s", design, err, b)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]int{}
	for _, line := range bytes.Split(b, []byte("\n")) {
		if m := fb01rStraceRow.FindSubmatch(line); m != nil {
			c, _ := strconv.Atoi(string(m[1]))
			calls[string(m[2])] = c
		}
	}
	return calls
}

func fb01rMeasureSyscalls(t *testing.T) {
	if _, err := exec.LookPath("strace"); err != nil {
		t.Skip("no strace")
	}
	const lo, hi = 200, 600
	for _, d := range fb01rDesigns {
		a, b := fb01rStrace(t, d, lo), fb01rStrace(t, d, hi)
		per := func(sc string) float64 { return float64(b[sc]-a[sc]) / float64(hi-lo) }
		row := map[string]any{"kind": "fb01r-measure-syscalls", "design": d, "fsync_per_group": per("fsync") + per("fdatasync"),
			"pwrite_per_group": per("pwrite64"), "write_per_group": per("write"),
			"open_close_fsyncs": a["fsync"] + a["fdatasync"] - int(float64(lo)*(per("fsync")+per("fdatasync")))}
		t.Logf("syscalls %-12s fsync/group %.2f  pwrite/group %.2f  fsyncs at open+close %v", d,
			row["fsync_per_group"], row["pwrite_per_group"], row["open_close_fsyncs"])
		fb01EmitJSON(t, row)
	}
}

// fb01rMeasureEngine: PutObject of 1 KiB objects, two seconds per point,
// every (mode, writers) point once per round over three rounds.
func fb01rMeasureEngine(t *testing.T) {
	const dur, rounds = 2 * time.Second, 3
	body := make([]byte, 1024)
	rand.Read(body)
	type point struct {
		mode    string
		writers int
	}
	lat := map[point][]time.Duration{}
	tput := map[point][]float64{}
	allocs := map[string]float64{}
	modes, counts := []string{"off", "lazy", "sync"}, []int{1, 8, 32}
	for r := 0; r < rounds; r++ {
		for _, mode := range modes {
			t.Setenv("ZEROS3_JOURNAL_WATERMARK", mode)
			for _, writers := range counts {
				pt := point{mode, writers}
				s, err := OpenStore(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				if err := s.CreateBucket("b"); err != nil {
					t.Fatal(err)
				}
				var mu sync.Mutex
				var wg sync.WaitGroup
				n := 0
				deadline := time.Now().Add(dur)
				start := time.Now()
				for w := 0; w < writers; w++ {
					wg.Add(1)
					go func(w int) {
						defer wg.Done()
						var mine []time.Duration
						for i := 0; time.Now().Before(deadline); i++ {
							t0 := time.Now()
							if _, err := s.PutObject("b", fmt.Sprintf("w%d-%d", w, i%64), body, "application/octet-stream", nil); err != nil {
								t.Error(err)
								return
							}
							mine = append(mine, time.Since(t0))
						}
						mu.Lock()
						lat[pt] = append(lat[pt], mine...)
						n += len(mine)
						mu.Unlock()
					}(w)
				}
				wg.Wait()
				tput[pt] = append(tput[pt], float64(n)/time.Since(start).Seconds())
				if writers == 1 && r == 0 {
					k := 0
					allocs[mode] = testing.AllocsPerRun(200, func() {
						k++
						if _, err := s.PutObject("b", fmt.Sprintf("a%d", k%64), body, "application/octet-stream", nil); err != nil {
							t.Fatal(err)
						}
					})
				}
				s.Close()
			}
		}
	}
	for _, mode := range modes {
		for _, writers := range counts {
			pt := point{mode, writers}
			tp := append([]float64(nil), tput[pt]...)
			sort.Float64s(tp)
			row := map[string]any{"kind": "fb01r-measure-engine", "mode": mode, "writers": writers, "puts": len(lat[pt]),
				"rounds": rounds, "puts_per_s": fb01rMedian(tp), "puts_per_s_min": tp[0], "puts_per_s_max": tp[len(tp)-1],
				"ack_p50_us": fb01rPct(lat[pt], 0.5), "ack_p99_us": fb01rPct(lat[pt], 0.99)}
			if writers == 1 {
				row["allocs_per_put"] = allocs[mode]
			}
			t.Logf("engine %-4s writers %2d  %7.0f puts/s (%5.0f-%5.0f)  p50 %6.0f us  p99 %6.0f us  allocs %v", mode, writers,
				row["puts_per_s"], tp[0], tp[len(tp)-1], row["ack_p50_us"], row["ack_p99_us"], row["allocs_per_put"])
			fb01EmitJSON(t, row)
		}
	}
}

// fb01rMeasureRestart: a store with 20,000 acknowledged puts, reopened clean
// in each mode, then with a torn final frame (recovery in lazy mode, refusal
// and salvage without the watermark), and the v2 scan over equal frames.
func fb01rMeasureRestart(t *testing.T) {
	const puts, reps = 20000, 5
	t.Setenv("ZEROS3_JOURNAL_WATERMARK", "lazy")
	base := t.TempDir()
	s, err := OpenStore(base)
	if err != nil {
		t.Fatal(err)
	}
	s.CreateBucket("b")
	var wg sync.WaitGroup
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < puts; i += 32 {
				if _, err := s.PutObject("b", fmt.Sprintf("k%05d", i), []byte(strconv.Itoa(i)), "", nil); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	s.Close()
	jsize := fb01rSize(t, fb01Journal(base))
	timeOpen := func(dir string) (time.Duration, error) {
		t0 := time.Now()
		s, err := OpenStore(dir)
		d := time.Since(t0)
		if err == nil {
			s.Close()
		}
		return d, err
	}
	emit := func(what, mode string, ds []time.Duration) {
		ms := make([]float64, len(ds))
		for i, d := range ds {
			ms[i] = float64(d.Microseconds()) / 1000
		}
		row := map[string]any{"kind": "fb01r-measure-restart", "case": what, "mode": mode, "journal_bytes": jsize,
			"frames": puts + 1, "open_ms_median": fb01rMedian(ms), "reps": len(ds)}
		t.Logf("restart %-34s %-4s %8.1f ms (median of %d)", what, mode, row["open_ms_median"], len(ds))
		fb01EmitJSON(t, row)
	}
	for _, mode := range []string{"off", "lazy", "sync"} {
		t.Setenv("ZEROS3_JOURNAL_WATERMARK", mode)
		var ds []time.Duration
		for r := 0; r < reps; r++ {
			d := t.TempDir()
			copyTreeT(t, base, d)
			if mode != "off" {
				timeOpen(d) // the first open in this mode writes its slot
			}
			dd, err := timeOpen(d)
			if err != nil {
				t.Fatal(err)
			}
			ds = append(ds, dd)
		}
		emit("clean reopen", mode, ds)
	}
	// A torn final frame: half of a copy of the last frame appended.
	t.Setenv("ZEROS3_JOURNAL_WATERMARK", "lazy")
	tear := func(d string) {
		img := fb01rRead(t, fb01Journal(d))
		last := img[fb01rLastFrame(img):]
		f, err := os.OpenFile(fb01Journal(d), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.Write(last[:len(last)/2])
		f.Close()
	}
	var rec, ref, salv []time.Duration
	for r := 0; r < reps; r++ {
		d := t.TempDir()
		copyTreeT(t, base, d)
		timeOpen(d)
		tear(d)
		dd, err := timeOpen(d)
		if err != nil {
			t.Fatalf("recovery open: %v", err)
		}
		rec = append(rec, dd)

		d = t.TempDir()
		copyTreeT(t, base, d)
		os.Remove(fb01rAckPath(d))
		tear(d)
		t.Setenv("ZEROS3_JOURNAL_WATERMARK", "off")
		dd, err = timeOpen(d)
		if err == nil {
			t.Fatal("a torn tail without the watermark opened")
		}
		ref = append(ref, dd)
		rep, _ := inspectJournal(d)
		t0 := time.Now()
		if _, err := salvageJournal(d, rep.ValidEnd, true); err != nil {
			t.Fatal(err)
		}
		if _, err := timeOpen(d); err != nil {
			t.Fatal(err)
		}
		salv = append(salv, time.Since(t0))
		t.Setenv("ZEROS3_JOURNAL_WATERMARK", "lazy")
	}
	emit("torn tail: automatic recovery", "lazy", rec)
	emit("torn tail: refusal", "off", ref)
	emit("torn tail: salvage, then open", "off", salv)

	// The v2 recovery scan over the same frame payloads, one frame per group.
	img := fb01rRead(t, fb01Journal(base))
	var payloads [][]byte
	for off := int64(0); off < int64(len(img)); {
		n := int64(be32(img[off+16 : off+20]))
		payloads = append(payloads, img[off+journalHeaderSize:off+journalHeaderSize+n])
		off += journalHeaderSize + n + 4
	}
	dir := t.TempDir()
	j, err := v2Create(filepath.Join(dir, "v2.log"), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range payloads {
		if err := j.append([][]byte{p}); err != nil {
			t.Fatal(err)
		}
	}
	j.close()
	var scans, v1scans []time.Duration
	for r := 0; r < reps; r++ {
		t0 := time.Now()
		b, err := os.ReadFile(filepath.Join(dir, "v2.log"))
		if err != nil {
			t.Fatal(err)
		}
		if out := v2Recover(b); out.verdict != "ok" {
			t.Fatalf("v2 scan: %+v", out)
		}
		scans = append(scans, time.Since(t0))
		t0 = time.Now()
		f, err := os.Open(fb01Journal(base))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := scanJournalRange(f, 0, int64(len(img)), 0, false, func(journalRecord) error { return nil }); err != nil {
			t.Fatal(err)
		}
		f.Close()
		v1scans = append(v1scans, time.Since(t0))
	}
	emit("scan only: v1 frames", "-", v1scans)
	emit("scan only: v2 groups with commit records", "-", scans)
	fb01EmitJSON(t, map[string]any{"kind": "fb01r-measure-bytes", "v1_journal_bytes": len(img),
		"v2_journal_bytes": fb01rSize(t, filepath.Join(dir, "v2.log")), "groups": len(payloads)})
}

func be32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// fb01rLastFrame is the start of the last frame of a clean v1 journal image.
func fb01rLastFrame(img []byte) int64 {
	last := int64(0)
	for off := int64(0); off < int64(len(img)); {
		last = off
		off += journalHeaderSize + int64(be32(img[off+16:off+20])) + 4
	}
	return last
}

func fb01rSize(t testing.TB, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}
