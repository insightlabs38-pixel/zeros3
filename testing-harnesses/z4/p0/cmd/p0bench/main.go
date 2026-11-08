// p0bench: deterministic ZeroS3 P0 benchmark driver (stdlib only).
// It drives a real ZeroS3 process over HTTP/CLI, accounts CPU/RSS/IO from
// /proc and rusage, verifies every output byte-exactly and emits JSONL.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "p0bench: "+f+"\n", a...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		die("usage: p0bench env|calib|put|get|mixed|cli|verify ...")
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "env":
		cmdEnv(args)
	case "calib":
		cmdCalib(args)
	case "put":
		cmdPut(args)
	case "get":
		cmdGet(args)
	case "mixed":
		cmdMixed(args)
	case "cli":
		cmdCLI(args)
	case "mkstore":
		cmdMkstore(args)
	case "verifystore":
		cmdVerifyStore(args)
	case "readstore":
		cmdReadStore(args)
	case "startup":
		cmdStartup(args)
	default:
		die("unknown subcommand %q", os.Args[1])
	}
}

// ---- shared flags ----

type common struct {
	bin, profBin, work, out, label, envs, prof, cache, sargs string
	reps, conc                                               int
	noVerify                                                 bool
}

func addCommon(fs *flag.FlagSet) *common {
	c := &common{}
	fs.StringVar(&c.bin, "bin", "/tmp/claude-0/bin/zeros3", "plain production binary (timing runs)")
	fs.StringVar(&c.profBin, "profbin", "/tmp/claude-0/bin/zeros3-prof", "overlay-instrumented binary (profiling runs)")
	fs.StringVar(&c.work, "work", "/tmp/claude-0/p0work", "scratch dir for stores (disk-backed unless you point it at tmpfs)")
	fs.StringVar(&c.out, "out", "", "JSONL result file (append); empty = stdout only")
	fs.StringVar(&c.label, "label", "run", "scenario label")
	fs.StringVar(&c.envs, "env", "", "comma separated KEY=VAL for the server process")
	fs.StringVar(&c.sargs, "sargs", "", "extra `zeros3 serve` args, e.g. '-representations shadow'")
	fs.StringVar(&c.prof, "prof", "", "'' | cpu | full (cpu+mutex+block+alloc) | trace")
	fs.StringVar(&c.cache, "cache", "warm", "warm | cold (drop_caches before measured window, server restarted)")
	fs.IntVar(&c.reps, "reps", 3, "independent repetitions (fresh store + fresh process)")
	fs.IntVar(&c.conc, "conc", 1, "client concurrency")
	fs.BoolVar(&c.noVerify, "noverify", false, "skip fresh-process readback verification (never for reported results)")
	return c
}

func (c *common) binary() string {
	if c.prof != "" {
		return c.profBin
	}
	return c.bin
}
func (c *common) envList() []string {
	var e []string
	for _, kv := range strings.Split(c.envs, ",") {
		if strings.TrimSpace(kv) != "" {
			e = append(e, strings.TrimSpace(kv))
		}
	}
	switch c.prof {
	case "cpu":
		e = append(e, "ZEROS3_P0_CPU=1")
	case "full":
		e = append(e, "ZEROS3_P0_CPU=1", "ZEROS3_P0_MUTEX_FRAC=5", "ZEROS3_P0_BLOCK_NS=10000", "ZEROS3_P0_MEMRATE=4096")
	case "trace":
		e = append(e, "ZEROS3_P0_TRACE_SECS=8")
	}
	return e
}
func (c *common) srvArgs() []string { return strings.Fields(c.sargs) }
func (c *common) profDir(rep int) string {
	if c.prof == "" {
		return ""
	}
	return fmt.Sprintf("%s/prof/%s-r%d", c.work, c.label, rep)
}

func parseSize(s string) int64 {
	s = strings.TrimSpace(strings.ToLower(s))
	mult := int64(1)
	for _, u := range []struct {
		suf string
		m   int64
	}{{"kib", 1 << 10}, {"mib", 1 << 20}, {"gib", 1 << 30}, {"k", 1 << 10}, {"m", 1 << 20}, {"g", 1 << 30}} {
		if strings.HasSuffix(s, u.suf) {
			mult = u.m
			s = strings.TrimSuffix(s, u.suf)
			break
		}
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		die("bad size %q", s)
	}
	return int64(n * float64(mult))
}

// ---- output ----

var outMu sync.Mutex

func emit(c *common, r map[string]any) {
	r["label"] = c.label
	r["ts"] = time.Now().UTC().Format(time.RFC3339)
	b, _ := json.Marshal(r)
	outMu.Lock()
	defer outMu.Unlock()
	fmt.Println(string(b))
	if c.out != "" {
		f, err := os.OpenFile(c.out, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
}

// ---- latency stats ----

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[i]
}

func latStats(v []float64) map[string]any {
	if len(v) == 0 {
		return map[string]any{"n": 0}
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	var sum float64
	for _, x := range s {
		sum += x
	}
	return map[string]any{"n": len(s), "mean_ms": sum / float64(len(s)) * 1000, "p50_ms": pct(s, .5) * 1000, "p90_ms": pct(s, .9) * 1000,
		"p99_ms": pct(s, .99) * 1000, "p999_ms": pct(s, .999) * 1000, "max_ms": s[len(s)-1] * 1000, "min_ms": s[0] * 1000}
}

func hostInfo() map[string]any {
	return map[string]any{"gomaxprocs": runtime.GOMAXPROCS(0), "numcpu": runtime.NumCPU(), "goversion": runtime.Version()}
}
