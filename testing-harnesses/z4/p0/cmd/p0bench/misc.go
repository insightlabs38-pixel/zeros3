package main

import (
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

func json_Unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

func sh(name string, args ...string) string {
	b, _ := exec.Command(name, args...).CombinedOutput()
	return strings.TrimSpace(string(b))
}
func readFile(p string) string { b, _ := os.ReadFile(p); return strings.TrimSpace(string(b)) }

func cmdEnv(args []string) {
	fs := flag.NewFlagSet("env", flag.ExitOnError)
	work := fs.String("work", "/tmp/claude-0/p0work", "")
	bin := fs.String("bin", "/tmp/claude-0/bin/zeros3", "")
	fs.Parse(args)
	e := map[string]any{}
	cpu := readFile("/proc/cpuinfo")
	for _, l := range strings.Split(cpu, "\n") {
		if strings.HasPrefix(l, "model name") && e["cpu_model"] == nil {
			e["cpu_model"] = strings.TrimSpace(strings.SplitN(l, ":", 2)[1])
		}
		if strings.HasPrefix(l, "flags") && e["cpu_flags_select"] == nil {
			var keep []string
			want := map[string]bool{"avx": true, "avx2": true, "avx512f": true, "avx512bw": true, "avx512vl": true, "aes": true, "sha_ni": true, "pclmulqdq": true, "bmi2": true, "sse4_2": true, "erms": true, "vaes": true, "avx512_vnni": true, "hypervisor": true}
			for _, f := range strings.Fields(strings.SplitN(l, ":", 2)[1]) {
				if want[f] {
					keep = append(keep, f)
				}
			}
			e["cpu_flags_select"] = keep
		}
		if strings.HasPrefix(l, "cpu MHz") && e["cpu_mhz"] == nil {
			e["cpu_mhz"] = strings.TrimSpace(strings.SplitN(l, ":", 2)[1])
		}
	}
	e["logical_cpus"] = runtime.NumCPU()
	e["lscpu_cores_threads"] = sh("sh", "-c", "lscpu | grep -E 'Thread|Core|Socket|L1d|L2|L3' | tr -s ' ' | tr '\\n' ';'")
	e["meminfo"] = sh("sh", "-c", "grep -E 'MemTotal|MemAvailable|SwapTotal|Dirty' /proc/meminfo | tr -s ' ' | tr '\\n' ';'")
	e["kernel"] = sh("uname", "-sr")
	e["os_release"] = sh("sh", "-c", ". /etc/os-release; echo $PRETTY_NAME")
	e["go"] = sh("go", "version")
	e["go_env"] = sh("go", "env", "GOFLAGS", "GOAMD64", "CGO_ENABLED", "GOMAXPROCS")
	e["cgroup_cpu_max"] = readFile("/sys/fs/cgroup/cpu.max")
	e["cgroup_mem_max"] = readFile("/sys/fs/cgroup/memory.max")
	e["cgroup_present"] = fileExists("/sys/fs/cgroup/cpu.max")
	e["mount_work"] = sh("sh", "-c", "df -T "+*work+" 2>/dev/null | tail -1; mkdir -p "+*work+"; df -T "+*work+" | tail -1")
	e["block_vda_rotational"] = readFile("/sys/block/vda/queue/rotational")
	e["block_vda_scheduler"] = readFile("/sys/block/vda/queue/scheduler")
	e["block_vda_write_cache"] = readFile("/sys/block/vda/queue/write_cache")
	e["virt"] = sh("sh", "-c", "systemd-detect-virt 2>/dev/null; dmesg 2>/dev/null | grep -i -m1 -E 'firecracker|hypervisor'")
	e["thp"] = readFile("/sys/kernel/mm/transparent_hugepage/enabled")
	e["cpu_governor"] = readFile("/sys/devices/system/cpu/cpu0/cpufreq/scaling_governor")
	e["cpu_pressure"] = readFile("/proc/pressure/cpu")
	e["ulimit_n"] = sh("sh", "-c", "ulimit -n")
	e["tools"] = map[string]string{"strace": sh("sh", "-c", "which strace"), "perf": sh("sh", "-c", "which perf || echo none"), "python": sh("python3", "--version"), "clang": sh("sh", "-c", "clang --version | head -1")}
	_, drop := os.Stat("/proc/sys/vm/drop_caches")
	e["drop_caches_writable"] = drop == nil && os.WriteFile("/proc/sys/vm/drop_caches", []byte("1"), 0) == nil
	e["env_zeros3"] = sh("sh", "-c", "env | grep ^ZEROS3_ || true")
	e["env_go"] = sh("sh", "-c", "env | grep -E '^(GO|GODEBUG)' || true")
	e["gomaxprocs_default"] = runtime.GOMAXPROCS(0)
	if b, err := os.ReadFile(*bin); err == nil {
		s := sha256.Sum256(b)
		e["server_binary_sha256"] = fmt.Sprintf("%x", s)
	}
	e["git_head"] = sh("git", "rev-parse", "HEAD")
	e["zeros3_go_sha256"] = sh("sh", "-c", "sha256sum zeros3.go | cut -d' ' -f1")
	e["time_utc"] = time.Now().UTC().Format(time.RFC3339)
	b, _ := json.MarshalIndent(e, "", " ")
	fmt.Println(string(b))
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// calib characterizes CPU/memory/storage noise: repeated fixed work, spread reported.
func cmdCalib(args []string) {
	fs := flag.NewFlagSet("calib", flag.ExitOnError)
	work := fs.String("work", "/tmp/claude-0/p0work", "")
	reps := fs.Int("reps", 7, "")
	fs.Parse(args)
	os.MkdirAll(*work, 0o755)
	out := map[string]any{"calib": true}
	buf := make([]byte, 64<<20)
	for i := range buf {
		buf[i] = byte(i * 31)
	}
	spread := func(v []float64) map[string]any {
		s := append([]float64(nil), v...)
		sort.Float64s(s)
		return map[string]any{"median": s[len(s)/2], "min": s[0], "max": s[len(s)-1], "cv_pct": cv(s) * 100, "runs": v}
	}
	// single-core SHA-256 throughput (MiB/s)
	var v1, vN, vcp []float64
	for r := 0; r < *reps; r++ {
		t := time.Now()
		for k := 0; k < 4; k++ {
			sha256.Sum256(buf)
		}
		v1 = append(v1, 256/time.Since(t).Seconds())
	}
	out["sha256_1core_mib_s"] = spread(v1)
	for r := 0; r < *reps; r++ {
		t := time.Now()
		done := make(chan struct{}, runtime.NumCPU())
		for g := 0; g < runtime.NumCPU(); g++ {
			go func() {
				for k := 0; k < 4; k++ {
					sha256.Sum256(buf)
				}
				done <- struct{}{}
			}()
		}
		for g := 0; g < runtime.NumCPU(); g++ {
			<-done
		}
		vN = append(vN, float64(256*runtime.NumCPU())/time.Since(t).Seconds())
	}
	out["sha256_allcores_mib_s"] = spread(vN)
	dst := make([]byte, len(buf))
	for r := 0; r < *reps; r++ {
		t := time.Now()
		for k := 0; k < 8; k++ {
			copy(dst, buf)
		}
		vcp = append(vcp, 512/time.Since(t).Seconds())
	}
	out["memcpy_1core_mib_s"] = spread(vcp)
	// fsync latency + sequential write on the work dir
	f, _ := os.Create(*work + "/calib.dat")
	small := make([]byte, 4096)
	var fl []float64
	for i := 0; i < 400; i++ {
		t := time.Now()
		f.Write(small)
		f.Sync()
		fl = append(fl, time.Since(t).Seconds()*1000)
	}
	sort.Float64s(fl)
	out["fsync4k_ms"] = map[string]any{"p50": fl[len(fl)/2], "p99": fl[int(float64(len(fl))*.99)], "max": fl[len(fl)-1]}
	var wv []float64
	for r := 0; r < 3; r++ {
		t := time.Now()
		for k := 0; k < 16; k++ {
			f.Write(buf)
		}
		f.Sync()
		wv = append(wv, 1024/time.Since(t).Seconds())
	}
	out["seqwrite_fsync_mib_s"] = spread(wv)
	f.Close()
	syscall.Sync()
	dropped := dropCaches()
	t := time.Now()
	g, _ := os.Open(*work + "/calib.dat")
	n := int64(0)
	for {
		m, err := g.Read(buf)
		n += int64(m)
		if err != nil {
			break
		}
	}
	g.Close()
	out["cold_seqread_mib_s"] = float64(n) / (1 << 20) / time.Since(t).Seconds()
	out["drop_caches_worked"] = dropped
	t = time.Now()
	g, _ = os.Open(*work + "/calib.dat")
	for {
		if _, err := g.Read(buf); err != nil {
			break
		}
	}
	g.Close()
	out["warm_seqread_mib_s"] = float64(n) / (1 << 20) / time.Since(t).Seconds()
	os.Remove(*work + "/calib.dat")
	b1, s1, _, t1 := readCPU()
	out["cpu_steal_frac_since_boot"] = s1 / t1
	out["cpu_busy_frac_since_boot"] = b1 / t1
	out["time_utc"] = time.Now().UTC().Format(time.RFC3339)
	bb, _ := json.Marshal(out)
	fmt.Println(string(bb))
}

func cv(s []float64) float64 {
	var m, v float64
	for _, x := range s {
		m += x
	}
	m /= float64(len(s))
	for _, x := range s {
		v += (x - m) * (x - m)
	}
	return sqrt(v/float64(len(s))) / m
}

func sqrt(x float64) float64 {
	z := x / 2
	if z == 0 {
		return 0
	}
	for i := 0; i < 40; i++ {
		z = (z + x/z) / 2
	}
	return z
}
