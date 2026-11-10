package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ---- /proc accounting ----

type ProcStat struct {
	UserS, SysS        float64
	RSSKB, HWMKB       int64
	RChar, WChar       int64
	ReadBytes, WriteBy int64
	SysCR, SysCW       int64
	CtxVol, CtxInvol   int64
	Threads            int64
}

func readProc(pid int) ProcStat {
	var p ProcStat
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		s := string(b)
		s = s[strings.LastIndex(s, ")")+2:]
		f := strings.Fields(s)
		ut, _ := strconv.ParseFloat(f[11], 64)
		st, _ := strconv.ParseFloat(f[12], 64)
		p.UserS, p.SysS = ut/100, st/100
	}
	kv := func(path string, fn func(k string, v int64)) {
		fh, err := os.Open(path)
		if err != nil {
			return
		}
		defer fh.Close()
		sc := bufio.NewScanner(fh)
		for sc.Scan() {
			l := sc.Text()
			i := strings.Index(l, ":")
			if i < 0 {
				continue
			}
			v := strings.Fields(strings.TrimSpace(l[i+1:]))
			if len(v) == 0 {
				continue
			}
			n, _ := strconv.ParseInt(v[0], 10, 64)
			fn(l[:i], n)
		}
	}
	kv(fmt.Sprintf("/proc/%d/status", pid), func(k string, v int64) {
		switch k {
		case "VmRSS":
			p.RSSKB = v
		case "VmHWM":
			p.HWMKB = v
		case "voluntary_ctxt_switches":
			p.CtxVol = v
		case "nonvoluntary_ctxt_switches":
			p.CtxInvol = v
		case "Threads":
			p.Threads = v
		}
	})
	kv(fmt.Sprintf("/proc/%d/io", pid), func(k string, v int64) {
		switch k {
		case "rchar":
			p.RChar = v
		case "wchar":
			p.WChar = v
		case "read_bytes":
			p.ReadBytes = v
		case "write_bytes":
			p.WriteBy = v
		case "syscr":
			p.SysCR = v
		case "syscw":
			p.SysCW = v
		}
	})
	return p
}

func (a ProcStat) Sub(b ProcStat) ProcStat {
	a.UserS -= b.UserS
	a.SysS -= b.SysS
	a.RChar -= b.RChar
	a.WChar -= b.WChar
	a.ReadBytes -= b.ReadBytes
	a.WriteBy -= b.WriteBy
	a.SysCR -= b.SysCR
	a.SysCW -= b.SysCW
	a.CtxVol -= b.CtxVol
	a.CtxInvol -= b.CtxInvol
	return a
}

// ---- block device + CPU steal ----

type Disk struct{ SectorsR, SectorsW, ReadsDone, WritesDone, Flushes int64 }

func readDisk(dev string) Disk {
	var d Disk
	b, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return d
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 14 && f[2] == dev {
			g := func(i int) int64 {
				if i >= len(f) {
					return 0
				}
				n, _ := strconv.ParseInt(f[i], 10, 64)
				return n
			}
			d = Disk{SectorsR: g(5), SectorsW: g(9), ReadsDone: g(3), WritesDone: g(7), Flushes: g(16)}
		}
	}
	return d
}
func (a Disk) Sub(b Disk) Disk {
	return Disk{a.SectorsR - b.SectorsR, a.SectorsW - b.SectorsW, a.ReadsDone - b.ReadsDone, a.WritesDone - b.WritesDone, a.Flushes - b.Flushes}
}

// cpu jiffies from /proc/stat: user nice system idle iowait irq softirq steal
func readCPU() (busy, steal, iowait, total float64) {
	b, _ := os.ReadFile("/proc/stat")
	f := strings.Fields(strings.SplitN(string(b), "\n", 2)[0])
	var v [10]float64
	for i := 1; i < len(f) && i <= 10; i++ {
		v[i-1], _ = strconv.ParseFloat(f[i], 64)
	}
	for _, x := range v {
		total += x
	}
	return v[0] + v[1] + v[2] + v[5] + v[6], v[7], v[4], total
}

func dropCaches() bool {
	syscall.Sync()
	return os.WriteFile("/proc/sys/vm/drop_caches", []byte("3"), 0) == nil
}

// ---- server lifecycle ----

type Server struct {
	cmd     *exec.Cmd
	Addr    string
	URL     string
	Store   string
	ProfDir string
	Rusage  *syscall.Rusage
	logf    *os.File
}

func freePort() int {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// StartServer launches the binary. extraEnv are KEY=VAL entries.
func StartServer(bin, store, profDir string, extraArgs, extraEnv []string) (*Server, error) {
	port := freePort()
	s := &Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Store: store, ProfDir: profDir}
	s.URL = "http://" + s.Addr
	args := append([]string{"serve", "-store", store, "-addr", s.Addr, "-access-key", accessKey, "-secret-key", secretKey, "-region", region}, extraArgs...)
	if cpus := os.Getenv("P0_SERVER_CPUS"); cpus != "" { // pin the server (taskset execs, so the PID is the server's)
		args = append([]string{"-c", cpus, bin}, args...)
		bin = "taskset"
	}
	s.cmd = exec.Command(bin, args...)
	s.cmd.Env = append(os.Environ(), extraEnv...)
	if profDir != "" {
		os.MkdirAll(profDir, 0o755)
		s.cmd.Env = append(s.cmd.Env, "ZEROS3_P0_PROF_DIR="+profDir)
	}
	lf, _ := os.CreateTemp("", "p0srv-*.log")
	s.logf = lf
	s.cmd.Stdout, s.cmd.Stderr = lf, lf
	if err := s.cmd.Start(); err != nil {
		return nil, err
	}
	for i := 0; i < 600; i++ {
		c, err := net.DialTimeout("tcp", s.Addr, 50*time.Millisecond)
		if err == nil {
			c.Close()
			return s, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.Kill()
	return nil, fmt.Errorf("server did not start; log %s", lf.Name())
}

func (s *Server) PID() int { return s.cmd.Process.Pid }

// Snapshot asks the profiling overlay to flush (SIGUSR1) and waits briefly.
func (s *Server) Snapshot() {
	if s.ProfDir != "" {
		s.cmd.Process.Signal(syscall.SIGUSR1)
		time.Sleep(300 * time.Millisecond)
	}
}

// Stop does a graceful SIGTERM; returns rusage of the whole lifetime.
func (s *Server) Stop() error {
	s.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		s.cmd.Process.Kill()
		<-done
	}
	if ps := s.cmd.ProcessState; ps != nil {
		if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
			s.Rusage = ru
		}
	}
	if s.logf != nil {
		s.logf.Close()
		os.Remove(s.logf.Name())
	}
	return nil
}

func (s *Server) Kill() {
	s.cmd.Process.Kill()
	s.cmd.Wait()
}

func newClient(conc int) *http.Client {
	return &http.Client{Transport: &http.Transport{
		MaxIdleConns: conc * 2, MaxIdleConnsPerHost: conc * 2, DisableCompression: true,
		ForceAttemptHTTP2: false, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}, Timeout: 30 * time.Minute}
}

// ---- store physical accounting ----

type StoreSize struct {
	Total, Chunks, Packs, Journal, Tmp, Other int64
	Files, ChunkFiles, PackFiles              int
}

func DuStore(root string) StoreSize {
	var z StoreSize
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		z.Total += fi.Size()
		z.Files++
		switch {
		case strings.HasPrefix(rel, "chunks") || strings.HasPrefix(rel, "cas"):
			z.Chunks += fi.Size()
			z.ChunkFiles++
		case strings.Contains(rel, "packs/"):
			z.Packs += fi.Size()
			z.PackFiles++
		case strings.Contains(rel, "journal"):
			z.Journal += fi.Size()
		case strings.HasPrefix(rel, "tmp") || strings.Contains(rel, "/tmp/"):
			z.Tmp += fi.Size()
		default:
			z.Other += fi.Size()
		}
		return nil
	})
	return z
}

func waitReady(c *http.Client, url string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, "GET", url+"/", nil)
		sign(req, "UNSIGNED-PAYLOAD")
		r, err := c.Do(req)
		if err == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("server not ready")
}
