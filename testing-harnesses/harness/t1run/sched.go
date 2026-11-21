package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type scheduler struct {
	ctx        context.Context
	root       string
	env        []string
	tbin       string
	resDir     string
	id         string
	costs      map[string]costEntry
	results    map[string]*result
	race       bool
	memBudget  int64
	diskBudget int64
	mu         sync.Mutex
	cond       *sync.Cond
	cpuUsed    float64
	memUsed    int64
	diskUsed   int64
	scratch    int
	notes      []string
	enospc     atomic.Bool
}

type unit struct {
	tests []string
	cost  float64
	mem   int64
	disk  int64
	cpu   float64 // CPUs the unit keeps busy: mostly-sleeping tests (grace periods, kill timers) overlap others
}

const defaultDiskMiB = 64

func (s *scheduler) cost(t string) (float64, int64, float64, int64) {
	if c, ok := s.costs[t]; ok {
		w := 1.0
		if c.Seconds >= 2 && c.CPU > 0 {
			w = min(1, max(0.25, c.CPU/c.Seconds))
		}
		return c.Seconds, max(c.RSSMiB, 64), w, max(c.DiskMiB, defaultDiskMiB)
	}
	return 4, 256, 1, 256 // unknown tests are scheduled as moderately heavy until measured
}

// diskBudget is the scratch space tests may occupy at once: free space minus a reserve for the system.
func diskBudget(dir string) int64 {
	if *diskMiB > 0 {
		return *diskMiB
	}
	f := freeMiB(dir)
	return max(0, f-max(512, f/20))
}

// preflight refuses to start when a selected test cannot fit, instead of failing hours in with ENOSPC.
func (s *scheduler) preflight(todo []string) error {
	for _, t := range todo {
		if _, _, _, d := s.cost(t); d > s.diskBudget {
			return fmt.Errorf("insufficient scratch space for %s: needs %d MiB, budget %d MiB (free %d MiB under %s)", t, d, s.diskBudget, freeMiB(*cacheDir), *cacheDir)
		}
	}
	return nil
}

func checkNeed(spec string) {
	i := strings.LastIndexByte(spec, '=')
	if i < 0 {
		die("-need-disk wants PATH=MIB")
	}
	need, err := strconv.ParseInt(spec[i+1:], 10, 64)
	must(err)
	p := spec[:i]
	for {
		if _, err := os.Stat(p); err == nil {
			break
		}
		if d := filepath.Dir(p); d == p {
			break
		} else {
			p = d
		}
	}
	if f := freeMiB(p); f < need {
		fmt.Fprintf(os.Stderr, "insufficient space under %s: %d MiB free, %d MiB needed\n", spec[:i], f, need)
		os.Exit(3)
	}
}

func (s *scheduler) plan(todo []string) []unit {
	var heavy []unit
	var small []string
	for _, t := range todo {
		c, m, w, d := s.cost(t)
		if c >= *batchSec {
			heavy = append(heavy, unit{[]string{t}, c, m, d, w})
		} else {
			small = append(small, t)
		}
	}
	sort.Strings(small)
	cur := unit{}
	for _, t := range small {
		c, m, _, d := s.cost(t)
		if cur.cost+c > *batchMax && len(cur.tests) > 0 {
			heavy = append(heavy, cur)
			cur = unit{}
		}
		cur.tests = append(cur.tests, t)
		cur.cost += c
		cur.mem = max(cur.mem, m)
		cur.disk = max(cur.disk, d)
		cur.cpu = 1
	}
	if len(cur.tests) > 0 {
		heavy = append(heavy, cur)
	}
	sort.SliceStable(heavy, func(i, j int) bool { return heavy[i].cost > heavy[j].cost })
	return heavy
}

func (s *scheduler) fits(u unit) bool {
	idle := s.memUsed == 0
	return s.cpuUsed+u.cpu <= float64(*jobs)+0.01+0.75 && (idle || s.memUsed+u.mem <= s.memBudget && s.diskUsed+u.disk <= s.diskBudget)
}

func (s *scheduler) run(todo []string) {
	units := s.plan(todo)
	s.cond = sync.NewCond(&s.mu)
	go func() { // wake the waiting dispatcher on cancellation
		<-s.ctx.Done()
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	}()
	var wg sync.WaitGroup
	var done int
	total := len(todo)
	for len(units) > 0 && s.ctx.Err() == nil && !s.enospc.Load() {
		s.mu.Lock()
		idx := -1 // first unit that fits the remaining budgets (the head always runs when nothing else does)
		for s.ctx.Err() == nil && !s.enospc.Load() {
			if s.cpuUsed < float64(*jobs)-0.01 {
				for i, u := range units {
					if s.fits(u) {
						idx = i
						break
					}
				}
				if idx >= 0 {
					break
				}
			}
			s.cond.Wait()
		}
		if idx < 0 {
			s.mu.Unlock()
			break
		}
		u := units[idx]
		units = append(units[:idx], units[idx+1:]...)
		s.cpuUsed += u.cpu
		s.memUsed += u.mem
		s.diskUsed += u.disk
		s.scratch++
		slot := s.scratch
		s.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := s.exec(u, slot)
			s.mu.Lock()
			s.cpuUsed -= u.cpu
			s.memUsed -= u.mem
			s.diskUsed -= u.disk
			done += n
			if *verbose {
				fmt.Fprintf(os.Stderr, "[%d/%d]\n", done, total)
			}
			s.cond.Broadcast()
			s.mu.Unlock()
		}()
	}
	wg.Wait()
	if s.enospc.Load() {
		s.notes = append(s.notes, "stopped early: a test process ran out of disk space (ENOSPC)")
	}
}

// exec runs a unit. judge decides per test; tests it hands back are run again, one process each when a
// batch made no progress, so a failing or crashing neighbour never hides or fails the others.
func (s *scheduler) exec(u unit, slot int) int {
	tmp := filepath.Join(*cacheDir, "tmp", strconv.Itoa(os.Getpid()), strconv.Itoa(slot))
	os.MkdirAll(tmp, 0o755)
	defer os.RemoveAll(tmp)
	groups := [][]string{u.tests}
	for len(groups) > 0 && s.ctx.Err() == nil {
		g := groups[0]
		groups = groups[1:]
		start := time.Now()
		pr := s.runProc(g, tmp)
		if pr.outcome.Leaked {
			s.mu.Lock()
			s.notes = append(s.notes, "processes left behind by "+strings.Join(g, ",")+" were killed")
			s.mu.Unlock()
		}
		if strings.Contains(pr.out, "no space left on device") {
			s.enospc.Store(true)
		}
		final, retry := judge(g, pr.out, pr.outcome)
		var tot float64
		for _, r := range final {
			tot += r.Seconds
		}
		for _, t := range g {
			r := final[t]
			if r == nil {
				continue
			}
			r.MaxRSS = pr.rss
			r.DiskMiB = pr.diskMiB
			r.Start = start.Sub(runStart).Seconds()
			if tot > 0 {
				r.CPU = pr.cpu * r.Seconds / tot
			}
			s.record(r, pr.outcome.clean())
		}
		switch {
		case len(retry) == 0:
		case len(retry) == len(g) && len(g) > 1:
			for _, t := range retry {
				groups = append(groups, []string{t})
			}
		default:
			groups = append(groups, retry)
		}
	}
	return len(u.tests)
}

// record stores a result; only a pass that ended a cleanly exiting process is published for reuse.
func (s *scheduler) record(r *result, clean bool) {
	r.Identity = s.id
	r.Complete = r.Status == "pass" && clean
	s.mu.Lock()
	s.results[r.Test] = r
	s.mu.Unlock()
	if r.Complete {
		b, _ := json.Marshal(r)
		if err := writeAtomic(filepath.Join(s.resDir, r.Test+".json"), b); err != nil {
			fmt.Fprintln(os.Stderr, "t1run: cache write:", err)
		}
	}
	if *verbose || r.Status == "fail" {
		fmt.Fprintf(os.Stderr, "%-4s %7.2fs %s\n", strings.ToUpper(r.Status), r.Seconds, r.Test)
	}
}

// sampleDisk records the peak size of the scratch tree of one test process.
func sampleDisk(dir string, stop <-chan struct{}, peak *atomic.Int64) {
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			var n int64
			filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					if fi, e := d.Info(); e == nil {
						n += fi.Size()
					}
				}
				return nil
			})
			if m := n >> 20; m > peak.Load() {
				peak.Store(m)
			}
		}
	}
}
