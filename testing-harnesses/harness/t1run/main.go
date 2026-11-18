// t1run builds the root test binary once per source revision and runs its tests as isolated
// processes under CPU and memory budgets, longest tests first. A passing test is recorded against
// the identity of the exact binary, mode and environment that ran it and is reused only for that
// identity; any source change produces a new binary hash and invalidates every earlier result.
//
//	go run ./harness/t1run -mode normal|race|authority|authority-race [flags]
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
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
	Test     string  `json:"test"`
	Status   string  `json:"status"` // pass | fail | skip
	Seconds  float64 `json:"seconds"`
	MaxRSS   int64   `json:"max_rss_kib,omitempty"`
	CPU      float64 `json:"cpu_seconds,omitempty"`
	DiskMiB  int64   `json:"disk_mib,omitempty"`
	Start    float64 `json:"start,omitempty"` // seconds after the run began (timeline and utilization)
	Cached   bool    `json:"cached,omitempty"`
	Complete bool    `json:"complete,omitempty"` // set only on a pass that ended a cleanly exiting process
	Identity string  `json:"identity,omitempty"`
	Exit     string  `json:"process,omitempty"`
	Output   string  `json:"output,omitempty"` // failures and skip reasons only
}

type costEntry struct {
	Seconds float64 `json:"s"`
	CPU     float64 `json:"cpu,omitempty"`
	RSSMiB  int64   `json:"rss_mib,omitempty"`
	DiskMiB int64   `json:"disk_mib,omitempty"`
}

type evidence struct {
	Command     string   `json:"command"`
	Mode        string   `json:"mode"`
	GoVer       string   `json:"go_version"`
	CPUs        int      `json:"cpus"`
	Jobs        int      `json:"jobs"`
	GitHead     string   `json:"git_head,omitempty"`
	GitTree     string   `json:"git_tree,omitempty"`
	GitDirty    int      `json:"git_dirty_files"`
	SrcHash     string   `json:"source_hash"`
	Fixtures    string   `json:"fixture_hash"`
	BinHash     string   `json:"test_binary_sha256"`
	ProdBinHash string   `json:"production_binary_sha256"`
	EnvHash     string   `json:"identity"`
	Selected    int      `json:"selected"`
	Fresh       int      `json:"fresh"`
	Reused      int      `json:"reused"`
	Passed      int      `json:"passed"`
	Failed      []string `json:"failed,omitempty"`
	Skipped     []string `json:"skipped,omitempty"`
	NotRun      []string `json:"not_run,omitempty"`
	Interrupted bool     `json:"interrupted,omitempty"`
	BuildSec    float64  `json:"build_seconds"`
	WallSec     float64  `json:"wall_seconds"`
	CPUSec      float64  `json:"test_cpu_seconds"`
	BusyCPUSec  float64  `json:"process_cpu_seconds"` // user+system CPU of the test processes, children included
	Exit        int      `json:"exit"`
	Results     []result `json:"results,omitempty"`
	Started     string   `json:"started"`
	Notes       []string `json:"notes,omitempty"`
}

var runStart time.Time

var (
	mode        = flag.String("mode", "normal", "normal | race | authority | authority-race")
	jobs        = flag.Int("jobs", 0, "concurrent test processes (default: CPUs)")
	memMiB      = flag.Int64("mem-mib", 0, "memory budget for concurrent tests (default: 70% of MemAvailable)")
	diskMiB     = flag.Int64("disk-mib", 0, "scratch-space budget for concurrent tests (default: free space minus a reserve)")
	runRe       = flag.String("run", "", "only top-level tests matching this regexp")
	skipRe      = flag.String("skip", "", "skip top-level tests matching this regexp")
	fast        = flag.Float64("fast", 0, "only tests whose recorded cost is at most this many seconds (unknown tests are included)")
	fresh       = flag.Bool("fresh", false, "ignore cached results")
	cacheDir    = flag.String("cache", "", "cache directory (default $VALIDATE_LOG_DIR/t1 or /tmp/zeros3-validate/t1)")
	costsFile   = flag.String("costs", "", "measured per-test cost database (default z4/t1/costs-{normal,race}.json under testing-harnesses)")
	updCosts    = flag.Bool("update-costs", false, "rewrite the cost database from this run (only when every selected test ran fresh and passed)")
	evOut       = flag.String("evidence", "", "write machine-readable evidence JSON here")
	testTO      = flag.Duration("test-timeout", 0, "per-process timeout (default 30m, 90m race)")
	verbose     = flag.Bool("v", false, "print each result")
	batchSec    = flag.Float64("batch", 1.0, "tests costing less than this are batched")
	batchMax    = flag.Float64("batch-max", 6.0, "maximum summed cost of a batch")
	extraEnv    = flag.String("env", "", "extra KEY=VAL,KEY=VAL for the tests (part of the identity)")
	rootFlag    = flag.String("root", "", "module directory holding zeros3.go and the tests (default: the enclosing repository)")
	measureDisk = flag.Bool("measure-disk", false, "sample each test's scratch usage (implied by -update-costs)")
	allowSkip   = flag.String("allow-skip", "", "regexp of tests that may skip; any other skipped test fails the run")
	needDisk    = flag.String("need-disk", "", "PATH=MIB: check that the filesystem of PATH has that much free space, then exit")
)

func main() {
	flag.Parse()
	if *needDisk != "" {
		checkNeed(*needDisk)
		return
	}
	t0 := time.Now()
	runStart = t0
	if *rootFlag != "" {
		rootPath, _ = filepath.Abs(*rootFlag)
		rootOnce.Do(func() {})
	}
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
	*cacheDir, _ = filepath.Abs(*cacheDir)
	if *testTO == 0 {
		*testTO = 30 * time.Minute
		if race {
			*testTO = 90 * time.Minute
		}
	}
	if *updCosts {
		*measureDisk = true
	}
	must(os.MkdirAll(filepath.Join(*cacheDir, "bin"), 0o755))
	cleanStale(filepath.Join(*cacheDir, "tmp"))
	goVer := strings.TrimSpace(run(root, nil, "go", "version"))
	src := sourceHash(root, goVer)
	ev := &evidence{Mode: *mode, GoVer: goVer, CPUs: runtime.NumCPU(), Jobs: *jobs, SrcHash: src, Fixtures: fixtureHash(root), Started: t0.UTC().Format(time.RFC3339),
		Command: "t1run " + strings.Join(os.Args[1:], " ")}
	ev.GitHead = strings.TrimSpace(runOpt(root, "git", "rev-parse", "HEAD"))
	ev.GitTree = strings.TrimSpace(runOpt(root, "git", "rev-parse", "HEAD^{tree}"))
	ev.GitDirty = len(strings.Fields(runOpt(root, "git", "status", "--porcelain")))

	// One compile of the test executable and one of the production binary per source revision; every
	// test process that needs the binary uses ZEROS3_TEST_BIN instead of invoking the compiler.
	bt := time.Now()
	tbin := filepath.Join(*cacheDir, "bin", fmt.Sprintf("%s-%s.test", strings.TrimSuffix(*mode, "-race")+raceTag(race), src[:16]))
	pbin := filepath.Join(*cacheDir, "bin", "zeros3-"+src[:16])
	must(buildOnce(tbin, func(tmp string) error {
		args := []string{"test", "-c", "-o", tmp}
		if race {
			args = append(args, "-race")
		}
		return runErr(root, nil, "go", append(args, ".")...)
	}))
	must(buildOnce(pbin, func(tmp string) error {
		return runErr(root, []string{"CGO_ENABLED=0"}, "go", "build", "-o", tmp, "zeros3.go")
	}))
	ev.BuildSec = time.Since(bt).Seconds()
	ev.BinHash = fileHash(tbin)
	ev.ProdBinHash = fileHash(pbin)

	env := append(os.Environ(), "ZEROS3_TEST_BIN="+pbin, "ZEROS3_BINCACHE="+filepath.Join(*cacheDir, "oldbin"))
	if auth {
		env = append(env, "ZEROS3_G1_RETIRE_ALL=1")
	}
	if *extraEnv != "" {
		env = append(env, strings.Split(*extraEnv, ",")...)
	}
	must(os.MkdirAll(filepath.Join(*cacheDir, "oldbin"), 0o755))
	ev.EnvHash = identityOf(ev.BinHash, *mode, *extraEnv, goVer, ev.Fixtures, env, testTO.String())
	// immutable fixture images are valid for exactly this test binary and environment
	env = append(env, "ZEROS3_FIXTURE_CACHE="+filepath.Join(*cacheDir, "fixtures", ev.EnvHash))
	resDir := filepath.Join(*cacheDir, "results", ev.EnvHash)
	must(os.MkdirAll(resDir, 0o755))

	if *costsFile == "" {
		*costsFile = "z4/t1/costs-normal.json"
		if race {
			*costsFile = "z4/t1/costs-race.json"
		}
	}
	costsPath := *costsFile
	if !filepath.IsAbs(costsPath) {
		costsPath = filepath.Join(thDir, costsPath)
	}
	costs := loadCosts(costsPath)
	tests := listTests(tbin, env, root)
	var sel []string
	var rr, sr, ar *regexp.Regexp
	if *runRe != "" {
		rr = regexp.MustCompile(*runRe)
	}
	if *skipRe != "" {
		sr = regexp.MustCompile(*skipRe)
	}
	if *allowSkip != "" {
		ar = regexp.MustCompile(*allowSkip)
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
	if len(sel) == 0 {
		die("the selection matches no test")
	}

	var todo []string
	results := map[string]*result{}
	for _, t := range sel {
		if !*fresh {
			if r, ok := loadCached(resDir, t, ev.EnvHash); ok {
				results[t] = r
				continue
			}
		}
		todo = append(todo, t)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	sched := &scheduler{ctx: ctx, root: root, env: env, tbin: tbin, resDir: resDir, id: ev.EnvHash, costs: costs, results: results, race: race, memBudget: *memMiB}
	sched.diskBudget = diskBudget(*cacheDir)
	must(sched.preflight(todo))
	sched.run(todo)

	for _, t := range sel {
		r := results[t]
		if r == nil {
			ev.NotRun = append(ev.NotRun, t)
			r = &result{Test: t, Status: "fail", Output: "not run (the run was cancelled or ended first)"}
			results[t] = r
		}
		ev.Results = append(ev.Results, *r)
		switch {
		case r.Status == "fail":
			ev.Failed = append(ev.Failed, t)
		case r.Status == "skip":
			ev.Skipped = append(ev.Skipped, t)
			if ar == nil || !ar.MatchString(t) {
				if *allowSkip != "" {
					ev.Failed = append(ev.Failed, t)
					ev.Notes = append(ev.Notes, "skipped but not allowed: "+t)
				}
			}
		default:
			ev.Passed++
		}
		if r.Cached {
			ev.Reused++
		} else {
			ev.Fresh++
			ev.CPUSec += r.Seconds
			ev.BusyCPUSec += r.CPU
		}
	}
	ev.Interrupted = ctx.Err() != nil
	ev.Notes = append(ev.Notes, sched.notes...)
	ev.WallSec = time.Since(t0).Seconds()
	if len(ev.Failed) > 0 {
		ev.Exit = 1
	}
	if ev.Interrupted {
		ev.Exit = 130
	}
	if *updCosts && ev.Exit == 0 && ev.Reused == 0 && *runRe == "" && *skipRe == "" && *fast == 0 {
		saveCosts(costsPath, results)
	}
	if *evOut != "" {
		b, _ := json.MarshalIndent(ev, "", " ")
		must(writeAtomic(*evOut, b))
	}
	for _, t := range ev.Failed {
		r := results[t]
		fmt.Printf("--- FAIL %s (%.1fs)\n%s\n", t, r.Seconds, tail(r.Output, 4000))
	}
	fmt.Printf("t1run %s: %d selected, %d fresh, %d reused, %d passed, %d skipped, %d failed; build %.1fs wall %.1fs (test wall %.0fs, cpu %.0fs = %.0f%% of %d jobs) bin=%s id=%s\n",
		*mode, ev.Selected, ev.Fresh, ev.Reused, ev.Passed, len(ev.Skipped), len(ev.Failed), ev.BuildSec, ev.WallSec, ev.CPUSec, ev.BusyCPUSec, 100*ev.BusyCPUSec/max(1, (ev.WallSec-ev.BuildSec)*float64(*jobs)), *jobs, ev.BinHash[:12], ev.EnvHash[:8])
	os.Exit(ev.Exit)
}

func raceTag(r bool) string {
	if r {
		return "-race"
	}
	return ""
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

func listTests(bin string, env []string, root string) []string {
	cmd := exec.Command(bin, "-test.list", ".*")
	cmd.Dir = root
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

func saveCosts(p string, res map[string]*result) {
	m := map[string]costEntry{}
	for t, r := range res {
		m[t] = costEntry{Seconds: float64(int(r.Seconds*100+0.5)) / 100, CPU: float64(int(r.CPU*100+0.5)) / 100, RSSMiB: r.MaxRSS / 1024, DiskMiB: r.DiskMiB}
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.MarshalIndent(m, "", " ")
	os.WriteFile(p, b, 0o644)
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

func runCmd(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s %v: %v\n%s", name, args, err, errb.String())
	}
	return out.String(), nil
}

func run(dir string, env []string, name string, args ...string) string {
	out, err := runCmd(dir, env, name, args...)
	must(err)
	return out
}

func runErr(dir string, env []string, name string, args ...string) error {
	_, err := runCmd(dir, env, name, args...)
	return err
}

// runOpt is for identity metadata that may legitimately be unavailable (no git checkout).
func runOpt(dir string, name string, args ...string) string {
	out, _ := runCmd(dir, nil, name, args...)
	return out
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
