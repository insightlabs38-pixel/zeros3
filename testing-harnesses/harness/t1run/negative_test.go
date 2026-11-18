package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Negative controls: the runner is run against a throwaway module whose tests misbehave in each way a
// result parser could be fooled by, and it must return nonzero for every one of them.

const fixTests = `package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	flag.Parse()
	listing := flag.Lookup("test.list").Value.String() != ""
	if os.Getenv("T1_NEG_MAIN") == "setup" && !listing {
		fmt.Println("setup failed")
		os.Exit(4)
	}
	code := m.Run()
	if os.Getenv("T1_NEG_MAIN") == "teardown" && code == 0 && !listing {
		os.Exit(3)
	}
	os.Exit(code)
}

func TestOK(t *testing.T)    {}
func TestOK2(t *testing.T)   {}
func TestFail(t *testing.T)  { t.Fail() }
func TestSkip(t *testing.T)  { t.Skip("condition not met") }
func TestPanic(t *testing.T) { panic("boom") }
func TestSubFail(t *testing.T) {
	t.Run("a", func(t *testing.T) {})
	t.Run("b", func(t *testing.T) { t.Error("bad") })
}
func TestExit0(t *testing.T) { os.Exit(0) }
func TestKill(t *testing.T)  { syscall.Kill(os.Getpid(), syscall.SIGKILL); time.Sleep(time.Minute) }
func TestSleep(t *testing.T) { time.Sleep(time.Minute) }
func TestSpawn(t *testing.T) {
	c := exec.Command("sleep", "300")
	c.Start()
	os.WriteFile(os.Getenv("T1_NEG_PID"), []byte(strconv.Itoa(c.Process.Pid)), 0o644)
	time.Sleep(time.Minute)
}
func TestFatalLine(t *testing.T) { fmt.Println("PASS"); os.Exit(0) }
func TestEnv(t *testing.T) {
	if os.Getenv("ZEROS3_NEG_FAIL") != "" {
		t.Fatal("env says fail")
	}
}
`

var (
	toolOnce sync.Once
	toolBin  string
	fixDir   string
	cacheD   string
)

func setup(t *testing.T) {
	toolOnce.Do(func() {
		d, _ := os.MkdirTemp("", "t1neg")
		toolBin = filepath.Join(d, "t1run")
		if out, err := exec.Command("go", "build", "-o", toolBin, ".").CombinedOutput(); err != nil {
			panic(string(out))
		}
		fixDir = filepath.Join(d, "fix")
		os.MkdirAll(fixDir, 0o755)
		os.WriteFile(filepath.Join(fixDir, "go.mod"), []byte("module negfix\n\ngo 1.21\n"), 0o644)
		os.WriteFile(filepath.Join(fixDir, "zeros3.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
		os.WriteFile(filepath.Join(fixDir, "neg_test.go"), []byte(fixTests), 0o644)
		cacheD = filepath.Join(d, "cache")
	})
	if toolBin == "" {
		t.Fatal("setup failed")
	}
}

type runResult struct {
	code int
	out  string
	ev   evidence
}

func t1(t *testing.T, env []string, args ...string) runResult {
	t.Helper()
	setup(t)
	evf := filepath.Join(t.TempDir(), "ev.json")
	a := append([]string{"-root", fixDir, "-cache", cacheD, "-costs", "/nonexistent", "-evidence", evf, "-jobs", "2"}, args...)
	cmd := exec.Command(toolBin, a...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	r := runResult{out: string(out)}
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		r.code = ee.ExitCode()
	}
	if b, err := os.ReadFile(evf); err == nil {
		json.Unmarshal(b, &r.ev)
	}
	return r
}

func TestNegativeControls(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		args []string
		want func(r runResult) bool // nil: must exit nonzero
	}{
		{"pass", nil, []string{"-run", "^(TestOK|TestOK2)$"}, func(r runResult) bool { return r.code == 0 && r.ev.Passed == 2 }},
		{"failing test", nil, []string{"-run", "^TestFail$"}, nil},
		{"failing subtest", nil, []string{"-run", "^TestSubFail$"}, nil},
		{"panic", nil, []string{"-run", "^TestPanic$"}, nil},
		{"os.Exit(0) inside a test", nil, []string{"-run", "^TestExit0$"}, nil},
		{"test printing PASS then exiting 0", nil, []string{"-run", "^TestFatalLine$"}, nil},
		{"killed by signal", nil, []string{"-run", "^TestKill$"}, nil},
		{"timeout", nil, []string{"-run", "^TestSleep$", "-test-timeout", "2s"}, nil},
		{"TestMain teardown exits nonzero after PASS", []string{"T1_NEG_MAIN=teardown"}, []string{"-run", "^TestOK$"}, nil},
		{"TestMain teardown in a batch", []string{"T1_NEG_MAIN=teardown"}, []string{"-run", "^(TestOK|TestOK2)$"}, nil},
		{"TestMain setup failure", []string{"T1_NEG_MAIN=setup"}, []string{"-run", "^TestOK$"}, nil},
		{"selection matches nothing", nil, []string{"-run", "^NoSuchTest$"}, nil},
		{"batch with one failing member", nil, []string{"-run", "^(TestOK|TestOK2|TestFail)$"}, func(r runResult) bool {
			return r.code != 0 && len(r.ev.Failed) == 1 && r.ev.Failed[0] == "TestFail" && r.ev.Passed == 2
		}},
		{"batch with a crashing member keeps neighbours", nil, []string{"-run", "^(TestOK|TestOK2|TestPanic)$"}, func(r runResult) bool {
			return r.code != 0 && len(r.ev.Failed) == 1 && r.ev.Failed[0] == "TestPanic" && r.ev.Passed == 2
		}},
		{"disallowed skip", nil, []string{"-run", "^TestSkip$", "-allow-skip", "^NothingAllowed$"}, nil},
		{"environment makes a test fail", []string{"ZEROS3_NEG_FAIL=1"}, []string{"-run", "^TestEnv$"}, nil},
		{"scratch budget too small", nil, []string{"-run", "^TestOK$", "-disk-mib", "1"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := t1(t, c.env, append([]string{"-fresh"}, c.args...)...)
			if c.want == nil {
				want := 1
				if strings.Contains(c.name, "nothing") || strings.Contains(c.name, "budget") {
					want = 2 // refused before running anything
				}
				if r.code != want || want == 1 && len(r.ev.Failed) == 0 {
					t.Fatalf("exit %d, want %d (failed %v):\n%s", r.code, want, r.ev.Failed, r.out)
				}
				return
			}
			if !c.want(r) {
				t.Fatalf("code %d, evidence %+v\n%s", r.code, r.ev, r.out)
			}
		})
	}
}

func TestSkipsAreReportedAndNeverReused(t *testing.T) {
	for i := 0; i < 2; i++ {
		r := t1(t, nil, "-run", "^(TestSkip|TestOK)$")
		if r.code != 0 || len(r.ev.Skipped) != 1 || r.ev.Skipped[0] != "TestSkip" {
			t.Fatalf("run %d: code %d skipped %v\n%s", i, r.code, r.ev.Skipped, r.out)
		}
		if i == 1 && (r.ev.Fresh != 1 || r.ev.Reused != 1) {
			t.Fatalf("skip must rerun, pass must be reused: fresh %d reused %d", r.ev.Fresh, r.ev.Reused)
		}
	}
}

func TestCacheIdentity(t *testing.T) {
	t1(t, nil, "-fresh", "-run", "^TestEnv$")
	if r := t1(t, nil, "-run", "^TestEnv$"); r.ev.Reused != 1 {
		t.Fatalf("identical rerun not reused: %+v", r.ev)
	}
	// a changed environment is a different identity: the earlier pass proves nothing about it
	if r := t1(t, []string{"ZEROS3_NEG_FAIL=1"}, "-run", "^TestEnv$"); r.code == 0 || r.ev.Reused != 0 {
		t.Fatalf("changed environment reused or passed:\n%s", r.out)
	}
	// failures are never cached: the same failing run fails again
	for i := 0; i < 2; i++ {
		if r := t1(t, nil, "-run", "^TestFail$"); r.code == 0 || r.ev.Reused != 0 {
			t.Fatalf("failure reused or accepted on run %d", i)
		}
	}
	// a corrupted entry (partial write) is ignored
	r := t1(t, nil, "-run", "^TestEnv$")
	f := filepath.Join(cacheD, "results", r.ev.EnvHash, "TestEnv.json")
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatalf("no cache entry for identity %s: %v", r.ev.EnvHash, err)
	}
	os.WriteFile(f, b[:len(b)/2], 0o644)
	if r := t1(t, nil, "-run", "^TestEnv$"); r.code != 0 || r.ev.Reused != 0 || r.ev.Fresh != 1 {
		t.Fatalf("partial entry was trusted: %+v", r.ev)
	}
	// an entry stamped with another identity under the right name is ignored
	os.WriteFile(f, []byte(strings.Replace(string(b), r.ev.EnvHash, "deadbeef", 1)), 0o644)
	if r := t1(t, nil, "-run", "^TestEnv$"); r.ev.Reused != 0 {
		t.Fatalf("foreign entry was trusted")
	}
}

func TestConcurrentInvocations(t *testing.T) {
	setup(t)
	var wg sync.WaitGroup
	codes := make([]int, 3)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = t1(t, nil, "-run", "^(TestOK|TestOK2|TestEnv)$").code
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != 0 {
			t.Fatalf("invocation %d exited %d", i, c)
		}
	}
}

func TestInterruptKillsProcessGroup(t *testing.T) {
	setup(t)
	pidf := filepath.Join(t.TempDir(), "pid")
	cmd := exec.Command(toolBin, "-root", fixDir, "-cache", cacheD, "-costs", "/nonexistent", "-fresh", "-run", "^TestSpawn$")
	cmd.Env = append(os.Environ(), "T1_NEG_PID="+pidf)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var pid int
	for i := 0; i < 100 && pid == 0; i++ {
		time.Sleep(200 * time.Millisecond)
		if b, err := os.ReadFile(pidf); err == nil {
			pid = atoi(string(b))
		}
	}
	if pid == 0 {
		t.Fatal("test did not start its child")
	}
	cmd.Process.Signal(syscall.SIGINT)
	err := cmd.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 130 {
		t.Fatalf("interrupted run exit = %v, want 130", err)
	}
	time.Sleep(300 * time.Millisecond)
	if st, _ := os.ReadFile("/proc/" + strings.TrimSpace(string(mustRead(pidf))) + "/stat"); len(st) > 0 && !strings.Contains(string(st), ") Z ") {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatal("a process started by the test survived the interrupt")
	}
}

func TestNeedDisk(t *testing.T) {
	setup(t)
	if exec.Command(toolBin, "-need-disk", t.TempDir()+"=1").Run() != nil {
		t.Fatal("1 MiB should be available")
	}
	err := exec.Command(toolBin, "-need-disk", t.TempDir()+"=999999999").Run()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3 {
		t.Fatalf("exit = %v, want 3", err)
	}
}

func mustRead(p string) []byte { b, _ := os.ReadFile(p); return b }

func atoi(s string) int {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		n = n*10 + int(c-'0')
	}
	return n
}

// judge is the single place that turns output plus process outcome into verdicts; these cases pin the
// combinations that a pass-line parser alone would get wrong.
func TestJudge(t *testing.T) {
	pass := func(n string) string { return "=== RUN   " + n + "\n--- PASS: " + n + " (0.01s)\n" }
	ok := outcome{}
	cases := []struct {
		name  string
		tests []string
		out   string
		o     outcome
		want  map[string]string // test -> status
		retry int
	}{
		{"clean pass", []string{"TestA"}, pass("TestA") + "PASS\n", ok, map[string]string{"TestA": "pass"}, 0},
		{"pass lines then exit 1", []string{"TestA"}, pass("TestA") + "PASS\n", outcome{Exit: 1}, map[string]string{"TestA": "fail"}, 0},
		{"pass lines then signal", []string{"TestA"}, pass("TestA") + "PASS\n", outcome{Exit: -1, Signal: "killed"}, map[string]string{"TestA": "fail"}, 0},
		{"pass lines then timeout", []string{"TestA"}, pass("TestA") + "PASS\n", outcome{Exit: -1, TimedOut: true}, map[string]string{"TestA": "fail"}, 0},
		{"exit 0 without PASS trailer", []string{"TestA"}, pass("TestA"), ok, map[string]string{"TestA": "fail"}, 0},
		{"exit 0 with truncated output", []string{"TestA"}, "=== RUN   TestA\n", ok, map[string]string{"TestA": "fail"}, 0},
		{"parent PASS with failed subtest", []string{"TestA"}, "=== RUN   TestA\n    --- FAIL: TestA/x (0.00s)\n--- PASS: TestA (0.01s)\nPASS\n", ok, map[string]string{"TestA": "fail"}, 0},
		{"unexpected extra test", []string{"TestA"}, pass("TestA") + pass("TestB") + "PASS\n", ok, map[string]string{"TestA": "fail"}, 0},
		{"duplicate result", []string{"TestA"}, pass("TestA") + pass("TestA") + "PASS\n", ok, map[string]string{"TestA": "fail"}, 0},
		{"FAIL trailer without failing test", []string{"TestA"}, pass("TestA") + "FAIL\n", outcome{Exit: 1}, map[string]string{"TestA": "fail"}, 0},
		{"launch failure of a batch is retried alone", []string{"TestA", "TestB"}, "", outcome{StartErr: os.ErrNotExist, Exit: -1}, map[string]string{}, 2},
		{"launch failure alone fails", []string{"TestA"}, "", outcome{StartErr: os.ErrNotExist, Exit: -1}, map[string]string{"TestA": "fail"}, 0},
		{"crash keeps earlier passes, fails the running test, retries the rest", []string{"TestA", "TestB", "TestC"}, pass("TestA") + "=== RUN   TestB\npanic: x\n", outcome{Exit: 2}, map[string]string{"TestA": "pass", "TestB": "fail"}, 1},
		{"cancellation records nothing as failed", []string{"TestA", "TestB"}, pass("TestA"), outcome{Exit: -1, Canceled: true}, map[string]string{"TestA": "pass"}, 0},
		{"skip is a skip", []string{"TestA"}, "=== RUN   TestA\n    x_test.go:1: nope\n--- SKIP: TestA (0.00s)\nPASS\n", ok, map[string]string{"TestA": "skip"}, 0},
		{"missing test in a clean batch is retried", []string{"TestA", "TestB"}, pass("TestA") + "PASS\n", ok, map[string]string{"TestA": "pass"}, 1},
	}
	for _, c := range cases {
		final, retry := judge(c.tests, c.out, c.o)
		for tst, st := range c.want {
			if final[tst] == nil || final[tst].Status != st {
				t.Errorf("%s: %s = %+v, want %s", c.name, tst, final[tst], st)
			}
		}
		if len(final) != len(c.want) {
			t.Errorf("%s: %d verdicts, want %d", c.name, len(final), len(c.want))
		}
		if len(retry) != c.retry {
			t.Errorf("%s: retry %v, want %d", c.name, retry, c.retry)
		}
	}
}
