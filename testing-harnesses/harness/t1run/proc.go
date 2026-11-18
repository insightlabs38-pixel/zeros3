package main

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
)

// outcome is how a test process ended, independent of what it printed.
type outcome struct {
	StartErr error
	Exit     int // process exit code; -1 when killed by a signal or never started
	Signal   string
	TimedOut bool
	Canceled bool
	Leaked   bool // processes of the group were still alive after the leader exited
}

func (o outcome) clean() bool {
	return o.StartErr == nil && o.Exit == 0 && o.Signal == "" && !o.TimedOut && !o.Canceled
}

func (o outcome) String() string {
	switch {
	case o.StartErr != nil:
		return "launch failed: " + o.StartErr.Error()
	case o.TimedOut:
		return "timed out"
	case o.Canceled:
		return "cancelled"
	case o.Signal != "":
		return "killed by signal " + o.Signal
	case o.Exit != 0:
		return "exit status " + strconv.Itoa(o.Exit)
	}
	return "exit status 0"
}

var (
	runLine = regexp.MustCompile(`^=== RUN\s+(\S+)`)
	resLine = regexp.MustCompile(`^(\s*)--- (PASS|FAIL|SKIP): (\S+) \(([0-9.]+)s\)`)
)

type parsed struct {
	results   map[string]*result
	started   []string
	failedSub map[string]string // top-level test -> first failed subtest
	dup       []string
	trailer   string
}

func top(name string) string {
	if i := strings.IndexByte(name, '/'); i >= 0 {
		return name[:i]
	}
	return name
}

// parse reads -test.v output. A test's own output is kept only for failures and skips.
func parse(out string) parsed {
	p := parsed{results: map[string]*result{}, failedSub: map[string]string{}}
	seen := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var cur strings.Builder
	last := ""
	for sc.Scan() {
		line := sc.Text()
		if m := runLine.FindStringSubmatch(line); m != nil && !strings.Contains(m[1], "/") {
			if !seen[m[1]] {
				seen[m[1]] = true
				p.started = append(p.started, m[1])
			}
			cur.Reset()
		}
		cur.WriteString(line + "\n")
		if strings.TrimSpace(line) != "" {
			last = line
		}
		m := resLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[3]
		sec, _ := strconv.ParseFloat(m[4], 64)
		if strings.Contains(name, "/") {
			if m[2] == "FAIL" && p.failedSub[top(name)] == "" {
				p.failedSub[top(name)] = name
			}
			continue
		}
		if p.results[name] != nil {
			p.dup = append(p.dup, name)
			continue
		}
		r := &result{Test: name, Seconds: sec, Status: strings.ToLower(m[2])}
		if r.Status != "pass" {
			r.Output = cur.String()
			if r.Status == "skip" {
				r.Output = tail(r.Output, 600)
			}
		}
		p.results[name] = r
	}
	if last == "PASS" || last == "FAIL" {
		p.trailer = last
	}
	return p
}

// judge decides, from the printed output and the process outcome together, what happened to each test of a
// process. A pass is only final when the process itself ended cleanly. Tests that did not run, or whose
// failure cannot be attributed to a single test, are returned in retry to be run one per process.
func judge(tests []string, out string, o outcome) (final map[string]*result, retry []string) {
	p := parse(out)
	final = map[string]*result{}
	want := map[string]bool{}
	for _, t := range tests {
		want[t] = true
	}
	fail := func(t, why string) {
		final[t] = &result{Test: t, Status: "fail", Output: why + "\n[t1run] process: " + o.String() + "\n" + tail(out, 20000), Exit: o.String()}
		if r := p.results[t]; r != nil {
			final[t].Seconds = r.Seconds
		}
	}
	if o.Canceled {
		for t, r := range p.results {
			if want[t] && r.Status != "fail" {
				final[t] = r
			}
		}
		return final, nil // not run, not failed: the run is reported as cancelled
	}
	var unexpected []string
	for t := range p.results {
		if !want[t] {
			unexpected = append(unexpected, t)
		}
	}
	for _, t := range p.started {
		if !want[t] {
			unexpected = append(unexpected, t)
		}
	}
	var incomplete, unstarted []string
	startedSet := map[string]bool{}
	for _, t := range p.started {
		startedSet[t] = true
	}
	for _, t := range tests {
		switch {
		case p.results[t] != nil:
		case startedSet[t]:
			incomplete = append(incomplete, t)
		default:
			unstarted = append(unstarted, t)
		}
	}
	anyFail := false
	for _, t := range tests {
		if r := p.results[t]; r != nil && r.Status == "fail" {
			anyFail = true
		}
	}
	// whole-process inconsistencies make every parsed pass untrustworthy
	whole := ""
	switch {
	case len(unexpected) > 0:
		whole = "tests outside the selection ran: " + strings.Join(unexpected, ",")
	case len(p.dup) > 0:
		whole = "duplicate result lines: " + strings.Join(p.dup, ",")
	case o.clean() && p.trailer != "PASS":
		whole = "exit status 0 without the final PASS line (output incomplete or malformed)"
	case !o.clean() && len(incomplete) == 0 && len(unstarted) == 0 && !anyFail:
		whole = "the process did not end cleanly after every test reported a result (TestMain, teardown, exit code, hang)"
	case p.trailer == "FAIL" && !anyFail && len(incomplete) == 0 && len(unstarted) == 0:
		whole = "process reported FAIL without a failing test"
	}
	if whole != "" {
		for _, t := range tests {
			fail(t, whole)
		}
		return final, nil
	}
	for _, t := range tests {
		r := p.results[t]
		if r == nil {
			continue
		}
		if sub := p.failedSub[t]; sub != "" && r.Status != "fail" {
			fail(t, "subtest "+sub+" failed but the parent reported "+strings.ToUpper(r.Status))
			continue
		}
		if r.Status == "fail" && !strings.Contains(r.Output, "--- FAIL") {
			r.Output = tail(out, 20000)
		}
		r.Exit = o.String()
		final[t] = r
	}
	switch {
	case len(incomplete) == 1:
		fail(incomplete[0], "the test did not report a result")
	case len(incomplete) > 1:
		retry = append(retry, incomplete...) // parallel tests: the culprit is found by running them alone
	}
	if len(tests) == 1 && len(unstarted) == 1 {
		fail(unstarted[0], "the test did not run (selection matched nothing or the process ended first)")
		return final, nil
	}
	retry = append(retry, unstarted...)
	return final, retry
}

type procRun struct {
	out     string
	outcome outcome
	rss     int64
	cpu     float64
	diskMiB int64
}

// runProc runs the test binary once under a process group, a timeout and the scheduler's cancellation.
func (s *scheduler) runProc(tests []string, tmp string) procRun {
	pat := "^(" + strings.Join(tests, "|") + ")$"
	ctx, cancel := context.WithTimeout(s.ctx, *testTO)
	defer cancel()
	cmd := exec.Command(s.tbin, "-test.run", pat, "-test.v", "-test.count=1", "-test.timeout", (*testTO + 0).String())
	cmd.Dir = s.root
	cmd.Env = append(append([]string{}, s.env...), "TMPDIR="+tmp)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	var pr procRun
	if err := cmd.Start(); err != nil {
		pr.outcome = outcome{StartErr: err, Exit: -1}
		pr.out = err.Error()
		return pr
	}
	pgid := cmd.Process.Pid
	var timedOut, canceled atomic.Bool
	fin := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				timedOut.Store(true)
			} else {
				canceled.Store(true)
			}
			syscall.Kill(-pgid, syscall.SIGKILL)
		case <-fin:
		}
	}()
	var peak atomic.Int64
	if *measureDisk {
		go sampleDisk(tmp, fin, &peak)
	}
	err := cmd.Wait()
	close(fin)
	<-stopped
	o := outcome{TimedOut: timedOut.Load(), Canceled: canceled.Load()}
	if err != nil {
		o.Exit = -1
		if ee, ok := err.(*exec.ExitError); ok {
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				o.Signal = ws.Signal().String()
			} else {
				o.Exit = ee.ExitCode()
			}
		} else {
			o.StartErr = err
		}
	}
	// servers a test left behind die with the group
	o.Leaked = syscall.Kill(-pgid, syscall.SIGKILL) == nil
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		pr.rss = ru.Maxrss
		pr.cpu = float64(ru.Utime.Sec+ru.Stime.Sec) + float64(ru.Utime.Usec+ru.Stime.Usec)/1e6
	}
	pr.outcome = o
	pr.out = buf.String()
	pr.diskMiB = peak.Load()
	return pr
}
