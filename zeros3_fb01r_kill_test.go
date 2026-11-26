package main

// FB-01R: real process kills. A child process runs the engine with several
// concurrent writers and reports each operation before it starts (TRY) and
// after it was acknowledged (ACK); the parent SIGKILLs it at a random moment
// or the child kills itself at a commit boundary, then the parent reopens the
// store in the same boot, as after a crashed server, and checks that every
// reported acknowledgement survived. A kill keeps the page cache, so these
// cover process crashes, not power loss (zeros3_fb01r_x14_test.go models that).

import (
	"bufio"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestFB01RKillHelper(t *testing.T) {
	dir := os.Getenv("FB01R_KILL_DIR")
	if dir == "" {
		return
	}
	point, prefix := os.Getenv("FB01R_KILL_POINT"), os.Getenv("FB01R_KILL_PREFIX")
	occ, _ := strconv.Atoi(os.Getenv("FB01R_KILL_OCC"))
	writers, _ := strconv.Atoi(os.Getenv("FB01R_KILL_WRITERS"))
	s, err := OpenStore(dir)
	if err != nil {
		fmt.Println("OPENERR", err)
		os.Exit(3)
	}
	if err := s.CreateBucket("b"); err != nil && !strings.Contains(err.Error(), "BucketAlready") && !strings.Contains(err.Error(), "already") {
		fmt.Println("BUCKETERR", err)
		os.Exit(3)
	}
	var n atomic.Int64
	kill := func() { syscall.Kill(os.Getpid(), syscall.SIGKILL) }
	switch point {
	case "torn-write":
		testJournalFault = func(p string, size int) (int, error) {
			switch {
			case p == faultJournalWrite && n.Add(1) == int64(occ):
				return size / 2, syscall.EIO // half the group reaches the file
			case p == faultJournalRollback:
				kill() // before the rollback can remove it
			}
			return 0, nil
		}
	case "":
	default:
		testHook = func(p string) {
			if p == point && n.Add(1) == int64(occ) {
				kill()
			}
		}
	}
	var out sync.Mutex
	say := func(format string, a ...any) {
		out.Lock()
		fmt.Fprintf(os.Stdout, format+"\n", a...)
		out.Unlock()
	}
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := mrand.New(mrand.NewPCG(uint64(w), uint64(len(prefix))))
			var mine []string
			for i := 0; i < 4000; i++ {
				switch x := r.IntN(10); {
				case x == 0 && len(mine) > 1:
					j := r.IntN(len(mine))
					k := mine[j]
					mine = append(mine[:j], mine[j+1:]...)
					say("TRYDEL %s", k)
					if err := s.DeleteObject("b", k); err == nil {
						say("DEL %s", k)
					}
				case x <= 2 && len(mine) > 0:
					k := mine[r.IntN(len(mine))]
					v := i
					say("TRY %s %d", k, v)
					if _, err := s.PutObject("b", k, []byte(fmt.Sprintf("%s=%d", k, v)), "text/plain", nil); err == nil {
						say("ACK %s %d", k, v)
					}
				default:
					k := fmt.Sprintf("%s-w%d-%d", prefix, w, i)
					say("TRY %s %d", k, i)
					if _, err := s.PutObject("b", k, []byte(fmt.Sprintf("%s=%d", k, i)), "text/plain", nil); err == nil {
						say("ACK %s %d", k, i)
						mine = append(mine, k)
					}
				}
			}
		}()
	}
	wg.Wait()
	say("DONE")
	os.Exit(0)
}

// fb01rKillTruth is what the parent was told: acknowledged values and
// deletes, and attempts that may or may not have taken effect.
type fb01rKillTruth struct {
	acked   map[string]int // key -> acknowledged version
	tried   map[string]int // key -> highest version attempted
	deleted map[string]bool
	trydel  map[string]bool
	acks    int
}

func (k *fb01rKillTruth) line(l string) {
	f := strings.Fields(l)
	switch {
	case len(f) == 3 && f[0] == "TRY":
		v, _ := strconv.Atoi(f[2])
		k.tried[f[1]] = max(k.tried[f[1]], v)
	case len(f) == 3 && f[0] == "ACK":
		v, _ := strconv.Atoi(f[2])
		k.acked[f[1]] = max(k.acked[f[1]], v)
		k.acks++
	case len(f) == 2 && f[0] == "TRYDEL":
		k.trydel[f[1]] = true
	case len(f) == 2 && f[0] == "DEL":
		k.deleted[f[1]] = true
		k.acks++
	}
}

// lost counts acknowledged operations s does not show.
func (k *fb01rKillTruth) lost(s *Store) (lost int, detail []string) {
	for key, v := range k.acked {
		_, data, err := s.GetObject("b", key)
		if err != nil {
			if k.trydel[key] {
				continue // deleted later, acknowledged or not
			}
			lost++
			detail = append(detail, fmt.Sprintf("%s missing: %v", key, err))
			continue
		}
		got, _ := strconv.Atoi(strings.TrimPrefix(string(data), key+"="))
		if got < v || got > k.tried[key] {
			lost++
			detail = append(detail, fmt.Sprintf("%s=%d, acknowledged %d", key, got, v))
		}
	}
	for key := range k.deleted {
		if _, _, err := s.GetObject("b", key); err == nil {
			lost++
			detail = append(detail, key+" deleted but visible")
		}
	}
	return lost, detail
}

type fb01rKillResult struct {
	Kind        string `json:"kind"`
	Build       string `json:"build"`
	Point       string `json:"point"`
	Rounds      int    `json:"rounds"`
	Acked       int    `json:"acked_operations"`
	Lost        int    `json:"lost_operations"`
	Recoveries  int    `json:"automatic_recoveries"`
	TailBytes   int64  `json:"discarded_bytes"`
	Refusals    int    `json:"refusals"`
	SharedOpens int    `json:"shared_opens_ok"`
	OpenMaxUS   int64  `json:"owner_open_max_us"`
}

// TestFB01RKill: random SIGKILLs during concurrent writes, and kills at each
// commit boundary (before the fsync, after it but before the acknowledgement,
// after the apply but before the response, and after a torn write), each
// followed by an automatic owner open in the same boot with no acknowledged
// loss. FB01R_KILL_ROUNDS sets the random rounds (default 6).
func TestFB01RKill(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	rounds := 6
	if v, err := strconv.Atoi(os.Getenv("FB01R_KILL_ROUNDS")); err == nil && v > 0 {
		rounds = v
	}
	type plan struct {
		point string
		occ   int
	}
	var plans []plan
	for range rounds {
		plans = append(plans, plan{"random", 0})
	}
	for _, p := range []string{hookAfterJournalWriteBeforeSync, hookAfterJournalSync, hookAfterApplyBeforeResponse, "torn-write"} {
		for _, occ := range []int{1, 7, 40} {
			plans = append(plans, plan{p, occ})
		}
	}
	dir := t.TempDir()
	k := &fb01rKillTruth{acked: map[string]int{}, tried: map[string]int{}, deleted: map[string]bool{}, trydel: map[string]bool{}}
	results := map[string]*fb01rKillResult{}
	r := mrand.New(mrand.NewPCG(7, 7))
	for i, p := range plans {
		res := results[p.point]
		if res == nil {
			res = &fb01rKillResult{Kind: "kill-summary", Build: os.Getenv("FB01_BUILD"), Point: p.point}
			results[p.point] = res
		}
		res.Rounds++
		point := p.point
		if point == "random" {
			point = ""
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestFB01RKillHelper$", "-test.timeout=300s")
		cmd.Env = append(os.Environ(), "FB01R_KILL_DIR="+dir, "FB01R_KILL_POINT="+point, "FB01R_KILL_OCC="+strconv.Itoa(p.occ),
			"FB01R_KILL_WRITERS=8", fmt.Sprintf("FB01R_KILL_PREFIX=r%02d", i))
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = nil
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		before := k.acks
		var timer *time.Timer
		if p.point == "random" {
			timer = time.AfterFunc(time.Duration(20+r.IntN(400))*time.Millisecond, func() { cmd.Process.Kill() })
		}
		sc := bufio.NewScanner(stdout)
		done := false
		for sc.Scan() {
			if sc.Text() == "DONE" {
				done = true
			}
			if strings.HasPrefix(sc.Text(), "OPENERR") || strings.HasPrefix(sc.Text(), "BUCKETERR") {
				t.Fatalf("round %d (%s): child: %s", i, p.point, sc.Text())
			}
			k.line(sc.Text())
		}
		werr := cmd.Wait()
		if timer != nil {
			timer.Stop()
		}
		if done {
			t.Logf("round %d (%s): the child finished before the kill", i, p.point)
		} else if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
			t.Fatalf("round %d (%s): the child was not killed: %v", i, p.point, werr)
		}
		res.Acked += k.acks - before
		// A shared open first, as a reader would, then the owner.
		if sh, err := OpenStoreShared(dir); err == nil {
			if n, d := k.lost(sh); n > 0 {
				t.Errorf("round %d (%s): the shared view lost %d acknowledged operations: %v", i, p.point, n, d[:min(len(d), 5)])
			}
			res.SharedOpens++
			sh.Close()
		} else {
			t.Errorf("round %d (%s): shared open after a kill: %v", i, p.point, err)
		}
		start := time.Now()
		s, err := OpenStore(dir)
		res.OpenMaxUS = max(res.OpenMaxUS, time.Since(start).Microseconds())
		if err != nil {
			res.Refusals++
			t.Fatalf("round %d (%s): owner open after a kill: %v", i, p.point, err)
		}
		if rec := s.JournalRecovery(); rec != nil {
			res.Recoveries++
			res.TailBytes += rec.Bytes
		}
		if n, d := k.lost(s); n > 0 {
			res.Lost += n
			t.Errorf("round %d (%s): %d acknowledged operations lost: %v", i, p.point, n, d[:min(len(d), 5)])
		}
		s.Close()
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	vr, err := s.Verify(false)
	s.Close()
	if err != nil {
		t.Fatalf("verify after %d kills: %v", len(plans), err)
	}
	t.Logf("verify after %d kills: %+v", len(plans), vr)
	for _, p := range []string{"random", hookAfterJournalWriteBeforeSync, hookAfterJournalSync, hookAfterApplyBeforeResponse, "torn-write"} {
		res := results[p]
		t.Logf("%-34s rounds %2d acked %6d lost %d recoveries %d discarded %dB refusals %d owner open max %dus", p, res.Rounds, res.Acked, res.Lost, res.Recoveries, res.TailBytes, res.Refusals, res.OpenMaxUS)
		fb01EmitJSON(t, res)
	}
}

// TestFB01RKillWithoutWatermark: the same torn-write kill on the v1 journal
// alone (ZEROS3_JOURNAL_WATERMARK=off) is refused, as in FB-01, and needs
// salvage; with the watermark it recovers by itself (TestFB01RKill).
func TestFB01RKillWithoutWatermark(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns processes")
	}
	t.Setenv("ZEROS3_JOURNAL_WATERMARK", "off")
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestFB01RKillHelper$", "-test.timeout=300s")
	cmd.Env = append(os.Environ(), "FB01R_KILL_DIR="+dir, "FB01R_KILL_POINT=torn-write", "FB01R_KILL_OCC=7", "FB01R_KILL_WRITERS=4", "FB01R_KILL_PREFIX=off")
	out, _ := cmd.Output()
	k := &fb01rKillTruth{acked: map[string]int{}, tried: map[string]int{}, deleted: map[string]bool{}, trydel: map[string]bool{}}
	for _, l := range strings.Split(string(out), "\n") {
		k.line(l)
	}
	if _, err := OpenStore(dir); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("v1 alone: want a refusal of the torn tail, got %v", err)
	}
	rep, err := inspectJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Recoverable || rep.WatermarkStatus != "off" {
		t.Fatalf("inspect: %+v", rep)
	}
	if _, err := salvageJournal(dir, rep.ValidEnd, true); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, d := k.lost(s); n > 0 {
		t.Fatalf("%d acknowledged operations lost: %v", n, d)
	}
}
