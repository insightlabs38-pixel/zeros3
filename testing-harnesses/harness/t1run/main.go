// t1run builds the root test binary once per source revision and runs its tests as isolated
// processes under CPU and memory budgets, longest tests first. A passing test is recorded against
// the identity of the exact binary, mode and environment that ran it and is reused only for that
// identity; any source change produces a new binary hash and invalidates every earlier result.
//
//	go run ./harness/t1run -mode normal|race|authority|authority-race [flags]
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type result struct {
	Test    string  `json:"test"`
	Status  string  `json:"status"` // pass | fail | skip
	Seconds float64 `json:"seconds"`
	MaxRSS  int64   `json:"max_rss_kib,omitempty"`
	Cached  bool    `json:"cached,omitempty"`
	Output  string  `json:"output,omitempty"` // failures only
}

type costEntry struct {
	Seconds float64 `json:"s"`
	RSSMiB  int64   `json:"rss_mib,omitempty"`
}

type evidence struct {
	Command  string   `json:"command"`
	Mode     string   `json:"mode"`
	GoVer    string   `json:"go_version"`
	CPUs     int      `json:"cpus"`
	Jobs     int      `json:"jobs"`
	SrcHash  string   `json:"source_hash"`
	BinHash  string   `json:"test_binary_sha256"`
	EnvHash  string   `json:"identity"`
	Selected int      `json:"selected"`
	Fresh    int      `json:"fresh"`
	Reused   int      `json:"reused"`
	Passed   int      `json:"passed"`
	Failed   []string `json:"failed,omitempty"`
	Skipped  int      `json:"skipped"`
	BuildSec float64  `json:"build_seconds"`
	WallSec  float64  `json:"wall_seconds"`
	CPUSec   float64  `json:"test_cpu_seconds"`
	Exit     int      `json:"exit"`
	Results  []result `json:"results,omitempty"`
	Started  string   `json:"started"`
	Notes    []string `json:"notes,omitempty"`
}

var (
	mode      = flag.String("mode", "normal", "normal | race | authority | authority-race")
	jobs      = flag.Int("jobs", 0, "concurrent test processes (default: CPUs)")
	memMiB    = flag.Int64("mem-mib", 0, "memory budget for concurrent tests (default: 70% of MemAvailable)")
	runRe     = flag.String("run", "", "only top-level tests matching this regexp")
	skipRe    = flag.String("skip", "", "skip top-level tests matching this regexp")
	fast      = flag.Float64("fast", 0, "only tests whose recorded cost is at most this many seconds (unknown tests are included)")
	fresh     = flag.Bool("fresh", false, "ignore cached results")
	cacheDir  = flag.String("cache", "", "cache directory (default $VALIDATE_LOG_DIR/t1 or /tmp/zeros3-validate/t1)")
	costsFile = flag.String("costs", "", "measured per-test cost database (default z4/t1/costs-{normal,race}.json under testing-harnesses)")
	updCosts  = flag.Bool("update-costs", false, "rewrite the cost database from this run (only when every selected test ran fresh and passed)")
	evOut     = flag.String("evidence", "", "write machine-readable evidence JSON here")
	testTO    = flag.Duration("test-timeout", 0, "per-process timeout (default 30m, 90m race)")
	verbose   = flag.Bool("v", false, "print each result")
	batchSec  = flag.Float64("batch", 1.0, "tests costing less than this are batched")
	batchMax  = flag.Float64("batch-max", 6.0, "maximum summed cost of a batch")
	extraEnv  = flag.String("env", "", "extra KEY=VAL,KEY=VAL for the tests (part of the identity)")
)

func main() {
	flag.Parse()
	t0 := time.Now()
	root := rootDir()
	if _, err := os.Stat(filepath.Join(root, "zeros3.go")); err != nil {
		die("run from inside the repository")
	}
	thDir := filepath.Join(root, "testing-harnesses")
	race := strings.HasSuffix(*mode, "race")
	auth := strings.HasPrefix(*mode, "authority")
	if *jobs <= 0 {
		*jobs = runtime.NumCPU()
	}
	if *memMiB <= 0 {
		*memMiB = memAvailMiB() * 7 / 10
	}
	if *cacheDir == "" {
		base := os.Getenv("VALIDATE_LOG_DIR")
		if base == "" {
			base = "/tmp/zeros3-validate"
		}
		*cacheDir = filepath.Join(base, "t1")
	}
	if *testTO == 0 {
		*testTO = 30 * time.Minute
		if race {
			*testTO = 90 * time.Minute
		}
	}
	must(os.MkdirAll(filepath.Join(*cacheDir, "bin"), 0o755))
	goVer := strings.TrimSpace(run(root, nil, "go", "version"))
	src := sourceHash(root, goVer)
	ev := &evidence{Mode: *mode, GoVer: goVer, CPUs: runtime.NumCPU(), Jobs: *jobs, SrcHash: src, Started: t0.UTC().Format(time.RFC3339),
		Command: "t1run " + strings.Join(os.Args[1:], " ")}

	// One compile of the test executable and one of the production binary per source revision; every
	// test process that needs the binary uses ZEROS3_TEST_BIN instead of invoking the compiler.
	bt := time.Now()
	tbin := filepath.Join(*cacheDir, "bin", fmt.Sprintf("%s-%s.test", strings.TrimSuffix(*mode, "-race")+raceTag(race), src[:16]))
	pbin := filepath.Join(*cacheDir, "bin", "zeros3-"+src[:16])
	if _, err := os.Stat(tbin); err != nil {
		args := []string{"test", "-c", "-o", tbin}
		if race {
			args = append(args, "-race")
		}
		run(root, nil, "go", append(args, ".")...)
	}
	if _, err := os.Stat(pbin); err != nil {
		run(root, []string{"CGO_ENABLED=0"}, "go", "build", "-o", pbin, "zeros3.go")
	}
	ev.BuildSec = time.Since(bt).Seconds()
	ev.BinHash = fileHash(tbin)

	env := append(os.Environ(), "ZEROS3_TEST_BIN="+pbin, "ZEROS3_BINCACHE="+filepath.Join(*cacheDir, "oldbin"))
	if auth {
		env = append(env, "ZEROS3_G1_RETIRE_ALL=1")
	}
	if *extraEnv != "" {
		env = append(env, strings.Split(*extraEnv, ",")...)
	}
	must(os.MkdirAll(filepath.Join(*cacheDir, "oldbin"), 0o755))
	idh := sha256.New()
	fmt.Fprintf(idh, "%s\n%s\n%s\n%v\n", ev.BinHash, *mode, *extraEnv, goVer)
	for _, e := range env {
		if strings.HasPrefix(e, "ZEROS3_") && !strings.HasPrefix(e, "ZEROS3_TEST_BIN") && !strings.HasPrefix(e, "ZEROS3_BINCACHE") {
			fmt.Fprintln(idh, e)
		}
	}
	ev.EnvHash = hex.EncodeToString(idh.Sum(nil))[:24]
	resDir := filepath.Join(*cacheDir, "results", ev.EnvHash)
	must(os.MkdirAll(resDir, 0o755))

	if *costsFile == "" {
		*costsFile = "z4/t1/costs-normal.json"
		if race {
			*costsFile = "z4/t1/costs-race.json"
		}
	}
	costs := loadCosts(filepath.Join(thDir, *costsFile))
	tests := listTests(tbin, env)
	var sel []string
	var rr, sr *regexp.Regexp
	if *runRe != "" {
		rr = regexp.MustCompile(*runRe)
	}
	if *skipRe != "" {
		sr = regexp.MustCompile(*skipRe)
	}
	for _, t := range tests {
		if rr != nil && !rr.MatchString(t) || sr != nil && sr.MatchString(t) {
			continue
		}
		if c, ok := costs[t]; ok && *fast > 0 && c.Seconds > *fast {
			continue
		}
		sel = append(sel, t)
	}
	ev.Selected = len(sel)

	var todo []string
	results := map[string]*result{}
	for _, t := range sel {
		if !*fresh {
			if b, err := os.ReadFile(filepath.Join(resDir, t+".json")); err == nil {
				var r result
				if json.Unmarshal(b, &r) == nil && r.Status != "fail" {
					r.Cached = true
					results[t] = &r
					continue
				}
			}
		}
		todo = append(todo, t)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	sched := &scheduler{ctx: ctx, env: env, tbin: tbin, resDir: resDir, costs: costs, results: results, race: race, memBudget: *memMiB}
	sched.run(todo)

	for _, t := range sel {
		r := results[t]
		if r == nil {
			r = &result{Test: t, Status: "fail", Output: "not run (cancelled)"}
			results[t] = r
		}
		ev.Results = append(ev.Results, *r)
		switch {
		case r.Status == "fail":
			ev.Failed = append(ev.Failed, t)
		case r.Status == "skip":
			ev.Skipped++
		default:
			ev.Passed++
		}
		if r.Cached {
			ev.Reused++
		} else {
			ev.Fresh++
			ev.CPUSec += r.Seconds
		}
	}
	ev.WallSec = time.Since(t0).Seconds()
	if len(ev.Failed) > 0 {
		ev.Exit = 1
	}
	if *updCosts && ev.Exit == 0 && ev.Reused == 0 && *runRe == "" && *skipRe == "" && *fast == 0 {
		saveCosts(filepath.Join(thDir, *costsFile), costs, results)
	}
	if *evOut != "" {
		b, _ := json.MarshalIndent(ev, "", " ")
		must(os.WriteFile(*evOut, b, 0o644))
	}
	for _, t := range ev.Failed {
		r := results[t]
		fmt.Printf("--- FAIL %s (%.1fs)\n%s\n", t, r.Seconds, tail(r.Output, 4000))
	}
	fmt.Printf("t1run %s: %d selected, %d fresh, %d reused, %d passed, %d skipped, %d failed; build %.1fs wall %.1fs (test cpu %.0fs, %d jobs) bin=%s id=%s\n",
		*mode, ev.Selected, ev.Fresh, ev.Reused, ev.Passed, ev.Skipped, len(ev.Failed), ev.BuildSec, ev.WallSec, ev.CPUSec, *jobs, ev.BinHash[:12], ev.EnvHash[:8])
	os.Exit(ev.Exit)
}

func raceTag(r bool) string {
	if r {
		return "-race"
	}
	return ""
}

type scheduler struct {
	ctx       context.Context
	env       []string
	tbin      string
	resDir    string
	costs     map[string]costEntry
	results   map[string]*result
	race      bool
	memBudget int64
	mu        sync.Mutex
	cond      *sync.Cond
	cpuUsed   int
	memUsed   int64
	scratch   int
}

type unit struct {
	tests []string
	cost  float64
	mem   int64
}

func (s *scheduler) cost(t string) (float64, int64) {
	if c, ok := s.costs[t]; ok {
		return c.Seconds, max(c.RSSMiB, 64)
	}
	return 4, 256 // unknown tests are scheduled as moderately heavy until measured
}

func (s *scheduler) plan(todo []string) []unit {
	var heavy []unit
	var small []string
	for _, t := range todo {
		c, m := s.cost(t)
		if c >= *batchSec {
			heavy = append(heavy, unit{[]string{t}, c, m})
		} else {
			small = append(small, t)
		}
	}
	sort.Strings(small)
	cur := unit{}
	for _, t := range small {
		c, m := s.cost(t)
		if cur.cost+c > *batchMax && len(cur.tests) > 0 {
			heavy = append(heavy, cur)
			cur = unit{}
		}
		cur.tests = append(cur.tests, t)
		cur.cost += c
		cur.mem = max(cur.mem, m)
	}
	if len(cur.tests) > 0 {
		heavy = append(heavy, cur)
	}
	sort.SliceStable(heavy, func(i, j int) bool { return heavy[i].cost > heavy[j].cost })
	return heavy
}

func (s *scheduler) run(todo []string) {
	units := s.plan(todo)
	s.cond = sync.NewCond(&s.mu)
	var wg sync.WaitGroup
	var done int
	total := len(todo)
	for len(units) > 0 && s.ctx.Err() == nil {
		s.mu.Lock()
		// first unit that fits the remaining memory (the head always runs when nothing else does)
		idx := -1
		for s.ctx.Err() == nil {
			if s.cpuUsed < *jobs {
				for i, u := range units {
					if s.cpuUsed == 0 || s.memUsed+u.mem <= s.memBudget {
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
		s.cpuUsed++
		s.memUsed += u.mem
		s.scratch++
		slot := s.scratch
		s.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := s.exec(u, slot)
			s.mu.Lock()
			s.cpuUsed--
			s.memUsed -= u.mem
			done += n
			if *verbose {
				fmt.Fprintf(os.Stderr, "[%d/%d]\n", done, total)
			}
			s.cond.Broadcast()
			s.mu.Unlock()
		}()
	}
	wg.Wait()
}

var resLine = regexp.MustCompile(`^--- (PASS|FAIL|SKIP): (\S+) \(([0-9.]+)s\)`)

// exec runs a unit; tests of a batch the process did not finish (crash, timeout) are rerun alone so
// one failure never hides or fails its neighbours.
func (s *scheduler) exec(u unit, slot int) int {
	tmp := filepath.Join(*cacheDir, "tmp", strconv.Itoa(os.Getpid()), strconv.Itoa(slot))
	os.MkdirAll(tmp, 0o755)
	defer os.RemoveAll(tmp)
	pending := u.tests
	for len(pending) > 0 && s.ctx.Err() == nil {
		got, out, rss, timedOut := s.once(pending, tmp)
		var next []string
		for _, t := range pending {
			r, ok := got[t]
			if !ok {
				next = append(next, t)
				continue
			}
			r.MaxRSS = rss
			s.record(r)
		}
		if len(next) == len(pending) { // no progress: a process that crashed before reporting anything
			if len(pending) == 1 {
				s.record(&result{Test: pending[0], Status: "fail", Output: out, MaxRSS: rss, Seconds: s.timeoutSecs(timedOut)})
				return len(u.tests)
			}
			// isolate: run the first one alone
			first := pending[0]
			g2, o2, r2, to2 := s.once([]string{first}, tmp)
			if r, ok := g2[first]; ok {
				r.MaxRSS = r2
				s.record(r)
			} else {
				s.record(&result{Test: first, Status: "fail", Output: o2, MaxRSS: r2, Seconds: s.timeoutSecs(to2)})
			}
			next = pending[1:]
		}
		pending = next
	}
	return len(u.tests)
}

func (s *scheduler) timeoutSecs(to bool) float64 {
	if to {
		return testTO.Seconds()
	}
	return 0
}

func (s *scheduler) record(r *result) {
	s.mu.Lock()
	s.results[r.Test] = r
	s.mu.Unlock()
	if r.Status != "fail" {
		b, _ := json.Marshal(r)
		os.WriteFile(filepath.Join(s.resDir, r.Test+".json"), b, 0o644)
	}
	if *verbose || r.Status == "fail" {
		fmt.Fprintf(os.Stderr, "%-4s %7.2fs %s\n", strings.ToUpper(r.Status), r.Seconds, r.Test)
	}
}

func (s *scheduler) once(tests []string, tmp string) (map[string]*result, string, int64, bool) {
	pat := "^(" + strings.Join(tests, "|") + ")$"
	ctx, cancel := context.WithTimeout(s.ctx, *testTO)
	defer cancel()
	cmd := exec.Command(s.tbin, "-test.run", pat, "-test.v", "-test.count=1", "-test.timeout", testTO.String())
	cmd.Dir = rootDir()
	cmd.Env = append(append([]string{}, s.env...), "TMPDIR="+tmp)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Start(); err != nil {
		return nil, err.Error(), 0, false
	}
	pgid := cmd.Process.Pid
	fin := make(chan struct{})
	timedOut := false
	go func() {
		select {
		case <-ctx.Done():
			timedOut = ctx.Err() == context.DeadlineExceeded
			syscall.Kill(-pgid, syscall.SIGKILL)
		case <-fin:
		}
	}()
	err := cmd.Wait()
	close(fin)
	syscall.Kill(-pgid, syscall.SIGKILL) // servers a test left behind
	var rss int64
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		rss = ru.Maxrss
	}
	out := buf.String()
	got := map[string]*result{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	cur := ""
	var curOut strings.Builder
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "=== RUN   ") && !strings.Contains(line[10:], "/") {
			cur = strings.TrimSpace(line[10:])
			curOut.Reset()
		}
		curOut.WriteString(line + "\n")
		if m := resLine.FindStringSubmatch(line); m != nil && !strings.Contains(m[2], "/") {
			sec, _ := strconv.ParseFloat(m[3], 64)
			r := &result{Test: m[2], Seconds: sec, Status: strings.ToLower(m[1])}
			if r.Status == "fail" {
				r.Output = curOut.String()
			}
			got[m[2]] = r
		}
	}
	_ = err
	if len(got) < len(tests) {
		// the test that was running when the process died is the failure; the remainder is rerun.
		if cur != "" && got[cur] == nil {
			for _, t := range tests {
				if t == cur {
					got[t] = &result{Test: t, Status: "fail", Output: tail(out, 20000), Seconds: s.timeoutSecs(timedOut)}
				}
			}
		}
	}
	return got, tail(out, 20000), rss, timedOut
}

var rootOnce sync.Once
var rootPath string

func rootDir() string {
	rootOnce.Do(func() {
		wd, _ := os.Getwd()
		for d := wd; ; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "zeros3.go")); err == nil {
				rootPath = d
				return
			}
			if d == "/" {
				rootPath = wd
				return
			}
		}
	})
	return rootPath
}

func listTests(bin string, env []string) []string {
	cmd := exec.Command(bin, "-test.list", ".*")
	cmd.Dir = rootDir()
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		die("listing tests: " + err.Error())
	}
	var ts []string
	for _, l := range strings.Fields(string(out)) {
		if strings.HasPrefix(l, "Test") || strings.HasPrefix(l, "Fuzz") {
			ts = append(ts, l)
		}
	}
	sort.Strings(ts)
	return ts
}

func loadCosts(p string) map[string]costEntry {
	m := map[string]costEntry{}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func saveCosts(p string, old map[string]costEntry, res map[string]*result) {
	m := map[string]costEntry{}
	for t, r := range res {
		m[t] = costEntry{Seconds: float64(int(r.Seconds*100+0.5)) / 100, RSSMiB: r.MaxRSS / 1024}
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.MarshalIndent(m, "", " ")
	os.WriteFile(p, b, 0o644)
}

func sourceHash(root, goVer string) string {
	h := sha256.New()
	fmt.Fprintln(h, goVer)
	var files []string
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".go") || n == "go.mod" || n == "go.sum" {
			files = append(files, n)
		}
	}
	sort.Strings(files)
	for _, f := range files {
		b, _ := os.ReadFile(filepath.Join(root, f))
		fmt.Fprintf(h, "%s %d\n", f, len(b))
		h.Write(b)
	}
	// fixtures read by tests from testdata directories are part of the identity too
	filepath.WalkDir(filepath.Join(root, "testdata"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			fmt.Fprintf(h, "%s ", p)
			if f, err := os.Open(p); err == nil {
				io.Copy(h, f)
				f.Close()
			}
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

func fileHash(p string) string {
	f, err := os.Open(p)
	must(err)
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

func memAvailMiB() int64 {
	b, _ := os.ReadFile("/proc/meminfo")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "MemAvailable:") {
			f := strings.Fields(l)
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			return kb / 1024
		}
	}
	return 4096
}

func run(dir string, env []string, name string, args ...string) string {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		die(fmt.Sprintf("%s %v: %v\n%s", name, args, err, errb.String()))
	}
	return out.String()
}

// tail keeps the head and the end of long output: the cause of a crash is at the start of its dump.
func tail(s string, n int) string {
	if len(s) > n {
		return s[:n/2] + "\n...[output truncated]...\n" + s[len(s)-n/2:]
	}
	return s
}

func must(err error) {
	if err != nil {
		die(err.Error())
	}
}

func die(m string) {
	fmt.Fprintln(os.Stderr, "t1run:", m)
	os.Exit(2)
}
