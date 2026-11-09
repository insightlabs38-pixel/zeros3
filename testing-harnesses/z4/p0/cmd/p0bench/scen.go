package main

import (
	"bufio"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const diskDev = "vda"

// ---- measurement window ----

type win struct {
	t0                        time.Time
	p0                        ProcStat
	d0                        Disk
	busy0, steal0, iow0, tot0 float64
	peak                      atomic.Int64
	stop                      chan struct{}
	pid                       int
}

func startWin(pid int) *win {
	w := &win{t0: time.Now(), p0: readProc(pid), d0: readDisk(diskDev), stop: make(chan struct{}), pid: pid}
	w.busy0, w.steal0, w.iow0, w.tot0 = readCPU()
	go func() {
		for {
			select {
			case <-w.stop:
				return
			case <-time.After(20 * time.Millisecond):
				if v := readProc(pid).RSSKB; v > w.peak.Load() {
					w.peak.Store(v)
				}
			}
		}
	}()
	return w
}

func (w *win) end() map[string]any {
	wall := time.Since(w.t0).Seconds()
	p1 := readProc(w.pid)
	d1 := readDisk(diskDev)
	close(w.stop)
	b1, s1, i1, t1 := readCPU()
	dp, dd := p1.Sub(w.p0), d1.Sub(w.d0)
	tot := t1 - w.tot0
	m := map[string]any{
		"wall_s": wall, "srv_user_s": dp.UserS, "srv_sys_s": dp.SysS, "srv_cpu_s": dp.UserS + dp.SysS,
		"srv_cpu_cores": (dp.UserS + dp.SysS) / wall, "srv_peak_rss_mib": float64(max(w.peak.Load(), p1.RSSKB)) / 1024, "srv_hwm_mib": float64(p1.HWMKB) / 1024,
		"srv_rchar": dp.RChar, "srv_wchar": dp.WChar, "srv_read_bytes": dp.ReadBytes, "srv_write_bytes": dp.WriteBy,
		"srv_syscr": dp.SysCR, "srv_syscw": dp.SysCW, "srv_ctx_vol": dp.CtxVol, "srv_ctx_invol": dp.CtxInvol, "srv_threads": p1.Threads,
		"dev_read_bytes": dd.SectorsR * 512, "dev_write_bytes": dd.SectorsW * 512, "dev_reads": dd.ReadsDone, "dev_writes": dd.WritesDone, "dev_flushes": dd.Flushes,
	}
	if tot > 0 {
		m["sys_cpu_busy_frac"] = (b1 - w.busy0) / tot
		m["sys_steal_frac"] = (s1 - w.steal0) / tot
		m["sys_iowait_frac"] = (i1 - w.iow0) / tot
	}
	return m
}

func mib(b int64, s float64) float64 { return float64(b) / (1 << 20) / s }

// ---- object sets ----

type setSpec struct {
	objs, revs        int
	size              int64
	sizes             string // "4k:50,64k:30,1m:20"
	kind              string
	dupfrac, editfrac float64
	revkeys           string // same | distinct
	bucket, keyPrefix string
	seed              uint64
	sizeStr           string
	files             string
	filemode          string
}

func addSet(fs *flag.FlagSet) *setSpec {
	s := &setSpec{}
	fs.IntVar(&s.objs, "objs", 100, "objects")
	fs.IntVar(&s.revs, "revs", 1, "revisions per object")
	fs.StringVar(&s.sizeStr, "size", "1mib", "object size")
	fs.StringVar(&s.sizes, "sizes", "", "mixed size distribution size:weight,... (overrides -size)")
	fs.StringVar(&s.kind, "kind", "random", "random | text | runs")
	fs.Float64Var(&s.dupfrac, "dupfrac", 0, "fraction of objects that are exact duplicates of an earlier object")
	fs.Float64Var(&s.editfrac, "editfrac", 0.02, "fraction of blocks edited per revision")
	fs.StringVar(&s.revkeys, "revkeys", "same", "same (version history on one key) | distinct")
	fs.StringVar(&s.bucket, "bucket", "bench", "bucket")
	fs.StringVar(&s.keyPrefix, "prefix", "k/", "key prefix")
	fs.Uint64Var(&s.seed, "seed", 1, "content seed")
	fs.StringVar(&s.files, "files", "", "comma separated corpus files (or @listfile); replaces generated content")
	fs.StringVar(&s.filemode, "filemode", "objs", "objs: each file is an object | revs: files are successive revisions of one key")
	return s
}

func (s *setSpec) fileList() []string {
	if s.files == "" {
		return nil
	}
	if strings.HasPrefix(s.files, "@") {
		b, err := os.ReadFile(s.files[1:])
		if err != nil {
			die("%v", err)
		}
		return strings.Fields(string(b))
	}
	return strings.Split(s.files, ",")
}

func (s *setSpec) resolve(_ string) {
	s.size = parseSize(s.sizeStr)
	if fl := s.fileList(); fl != nil {
		if s.filemode == "revs" {
			s.objs, s.revs, s.revkeys = 1, len(fl), "same"
		} else {
			s.objs = len(fl)
		}
	}
}

func (s *setSpec) sizeOf(i int) int64 {
	if s.sizes == "" {
		return s.size
	}
	type e struct {
		sz int64
		w  int
	}
	var es []e
	tw := 0
	for _, p := range strings.Split(s.sizes, ",") {
		kv := strings.SplitN(p, ":", 2)
		w := 1
		fmt.Sscan(kv[1], &w)
		es = append(es, e{parseSize(kv[0]), w})
		tw += w
	}
	r := rand.New(rand.NewPCG(s.seed, uint64(i)))
	x := r.IntN(tw)
	for _, e := range es {
		if x < e.w {
			return e.sz
		}
		x -= e.w
	}
	return es[0].sz
}

// base content for object i (rev 0).
func (s *setSpec) base(i int) Content {
	seed := s.seed*1000003 + uint64(i)
	if s.dupfrac > 0 && i > 0 {
		r := rand.New(rand.NewPCG(s.seed^0xabc, uint64(i)))
		if r.Float64() < s.dupfrac {
			j := r.IntN(i)
			seed = s.seed*1000003 + uint64(j)
			return NewContent(s.kind, seed, s.sizeOf(j))
		}
	}
	return NewContent(s.kind, seed, s.sizeOf(i))
}

func (s *setSpec) gen(rev int, pregenLimit int64) []*Obj {
	objs := make([]*Obj, s.objs)
	var total int64
	fl := s.fileList()
	for i := range objs {
		var c Content
		switch {
		case fl != nil && s.filemode == "revs":
			c = NewFileContent(fl[rev])
		case fl != nil:
			c = NewFileContent(fl[i])
		default:
			c = s.base(i)
			for r := 1; r <= rev; r++ {
				c = Revise(c, r, s.editfrac)
			}
		}
		key := fmt.Sprintf("%s%06d", s.keyPrefix, i)
		if s.revkeys == "distinct" && rev > 0 {
			key += fmt.Sprintf("-r%d", rev)
		}
		objs[i] = &Obj{Bucket: s.bucket, Key: key, C: c}
		total += c.Size
	}
	pre := total <= pregenLimit
	sem := make(chan struct{}, runtime.NumCPU())
	var wg sync.WaitGroup
	for _, o := range objs {
		wg.Add(1)
		sem <- struct{}{}
		go func(o *Obj) {
			defer wg.Done()
			defer func() { <-sem }()
			o.SHA = o.C.SHA256()
			if pre {
				o.Data = o.C.Bytes()
			}
		}(o)
	}
	wg.Wait()
	return objs
}

func totalBytes(o []*Obj) (n int64) {
	for _, x := range o {
		n += x.C.Size
	}
	return
}

// ---- scenario plumbing ----

func (c *common) newStore(rep int) string {
	d := fmt.Sprintf("%s/stores/%s-r%d", c.work, c.label, rep)
	os.RemoveAll(d)
	os.MkdirAll(d, 0o755)
	return d
}

func (c *common) start(store string, rep int) (*Server, *S3) {
	srv, err := StartServer(c.binary(), store, c.profDir(rep), c.srvArgs(), c.envList())
	if err != nil {
		die("%v", err)
	}
	cl := &S3{c: newClient(c.conc + 8), base: srv.URL}
	if err := waitReady(cl.c, srv.URL); err != nil {
		srv.Kill()
		die("%v", err)
	}
	return srv, cl
}

func rusageMap(s *Server) map[string]any {
	m := map[string]any{}
	if s.Rusage != nil {
		r := s.Rusage
		m["ru_maxrss_mib"] = float64(r.Maxrss) / 1024
		m["ru_user_s"] = float64(r.Utime.Sec) + float64(r.Utime.Usec)/1e6
		m["ru_sys_s"] = float64(r.Stime.Sec) + float64(r.Stime.Usec)/1e6
		m["ru_minflt"], m["ru_majflt"] = r.Minflt, r.Majflt
		m["ru_inblock"], m["ru_oublock"] = r.Inblock, r.Oublock
		m["ru_nvcsw"], m["ru_nivcsw"] = r.Nvcsw, r.Nivcsw
	}
	return m
}

func readMetrics(dir string) map[string]any {
	if dir == "" {
		return nil
	}
	b, err := os.ReadFile(dir + "/metrics.json")
	if err != nil {
		return nil
	}
	var m map[string]any
	if json_Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

func populate(cl *S3, objs []*Obj, opt PutOpt, conc int) error {
	return pool(len(objs), conc, func(i int) error { return cl.Put(objs[i], opt) })
}

func runCLICapture(bin string, args []string, env []string) (string, error) {
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	b, err := cmd.CombinedOutput()
	return string(b), err
}

// ---- put ----

func cmdPut(args []string) {
	fs := flag.NewFlagSet("put", flag.ExitOnError)
	c := addCommon(fs)
	set := addSet(fs)
	mode := fs.String("mode", "single", "single | multipart | chunked")
	payload := fs.String("payload", "unsigned", "unsigned | signed")
	part := fs.String("part", "8mib", "multipart part size")
	partConc := fs.Int("partconc", 1, "parallel parts per upload")
	pregen := fs.String("pregen", "1gib", "pregenerate bodies in RAM when one revision totals <= this")
	prep := fs.String("prep", "", "after each revision's window: '' | compact | gc (untimed)")
	fs.Parse(args)
	set.resolve(fs.Lookup("size").Value.String())
	opt := PutOpt{Mode: *mode, Payload: *payload, PartSize: parseSize(*part), PartConc: *partConc}
	_ = prep
	for rep := 0; rep < c.reps; rep++ {
		store := c.newStore(rep)
		srv, cl := c.start(store, rep)
		cl.CreateBucket(set.bucket)
		var all [][]*Obj
		prev := DuStore(store)
		for rev := 0; rev < set.revs; rev++ {
			objs := set.gen(rev, parseSize(*pregen))
			all = append(all, objs)
			lats := make([]float64, len(objs))
			if c.cache == "cold" {
				dropCaches()
			}
			w := startWin(srv.PID())
			err := pool(len(objs), c.conc, func(i int) error {
				t := time.Now()
				e := cl.Put(objs[i], opt)
				lats[i] = time.Since(t).Seconds()
				return e
			})
			m := w.end()
			if err != nil {
				die("put failed (result invalid): %v", err)
			}
			bytes := totalBytes(objs)
			sz := DuStore(store)
			m["scenario"], m["rep"], m["revision"] = "put", rep, rev
			m["params"] = map[string]any{"objs": set.objs, "size": set.size, "sizes": set.sizes, "kind": set.kind, "mode": *mode, "payload": *payload, "conc": c.conc,
				"dupfrac": set.dupfrac, "editfrac": set.editfrac, "revkeys": set.revkeys, "env": c.envs, "prof": c.prof, "part": *part}
			m["logical_bytes"] = bytes
			m["mib_s"] = mib(bytes, m["wall_s"].(float64))
			m["ops_s"] = float64(len(objs)) / m["wall_s"].(float64)
			m["lat"] = latStats(lats)
			m["store"] = sz
			m["store_growth_bytes"] = sz.Total - prev.Total
			m["phys_per_logical"] = float64(sz.Total-prev.Total) / float64(bytes)
			prev = sz
			m["go_version"] = runtime.Version()
			if c.prof != "" && rev == set.revs-1 {
				srv.Snapshot()
				m["go_metrics"] = readMetrics(srv.ProfDir)
			}
			m["verified"] = nil
			if rev == set.revs-1 {
				emitPut(c, srv, cl, store, m, all, set, c.conc, rep)
				continue
			}
			emit(c, m)
		}
		os.RemoveAll(store)
	}
}

func emitPut(c *common, srv *Server, cl *S3, store string, m map[string]any, all [][]*Obj, set *setSpec, conc, rep int) {
	srv.Stop()
	m["server_lifetime"] = rusageMap(srv)
	if !c.noVerify {
		// fresh process replay: every revision's latest bytes
		s2, cl2 := c.startPlain(store)
		bad, err := VerifyAll(cl2, all[len(all)-1], max(conc, 4))
		s2.Stop()
		if err != nil || bad > 0 {
			m["verified"] = false
			emit(c, m)
			die("VERIFY FAILED bad=%d err=%v", bad, err)
		}
		m["verified"] = true
	}
	emit(c, m)
}

func (c *common) startPlain(store string) (*Server, *S3) {
	srv, err := StartServer(c.bin, store, "", c.srvArgs(), nil)
	if err != nil {
		die("%v", err)
	}
	cl := &S3{c: newClient(16), base: srv.URL}
	if err := waitReady(cl.c, srv.URL); err != nil {
		die("%v", err)
	}
	return srv, cl
}

// ---- get / range ----

func cmdGet(args []string) {
	fs := flag.NewFlagSet("get", flag.ExitOnError)
	c := addCommon(fs)
	set := addSet(fs)
	mode := fs.String("mode", "whole", "whole | range | hot")
	rsize := fs.String("rsize", "64kib", "range request size")
	roff := fs.String("roff", "random", "range offset policy for -mode range: random | hot (one fixed offset per object) | eof (the last rsize bytes) | seq (consecutive ranges) | spread (8 fixed offsets per object)")
	nreq := fs.Int("nreq", 0, "requests (0 = objs for whole, 2000 for range)")
	zipf := fs.Float64("zipf", 1.2, "zipf s for -mode hot")
	prep := fs.String("prep", "", "'' (loose) | compact | compact-off | repack-auto")
	loadMode := fs.String("load", "single", "single | multipart (populate path)")
	pregen := fs.String("pregen", "1gib", "")
	phasesF := fs.String("phases", "", "several measured phases on one populated store: name|mode|rsize|roff|conc|nreq|warm-or-cold ; ... (overrides -mode/-rsize/-roff/-conc/-nreq)")
	fs.Parse(args)
	set.resolve(fs.Lookup("size").Value.String())
	rs := parseSize(*rsize)
	phases := parsePhases(*phasesF, getPhase{name: "run", mode: *mode, rsizeStr: *rsize, rs: rs, roff: *roff, conc: c.conc, nreq: *nreq, zipf: *zipf, cacheName: c.cache, cold: c.cache == "cold"})
	for rep := 0; rep < c.reps; rep++ {
		store := c.newStore(rep)
		srv, cl := c.startPlain2(store, c, rep, false)
		cl.CreateBucket(set.bucket)
		var objs []*Obj
		for rev := 0; rev < max(set.revs, 1); rev++ {
			objs = set.gen(rev, parseSize(*pregen))
			if err := populate(cl, objs, PutOpt{Mode: *loadMode, PartSize: 8 << 20, Payload: "unsigned"}, 4); err != nil {
				die("populate: %v", err)
			}
		}
		srv.Stop()
		var prepOut string
		switch *prep {
		case "compact":
			prepOut, _ = runCLICapture(c.bin, []string{"compact", "-store", store, "-compression", "auto"}, nil)
		case "compact-off":
			prepOut, _ = runCLICapture(c.bin, []string{"compact", "-store", store, "-compression", "off"}, nil)
		case "repack-auto":
			prepOut, _ = runCLICapture(c.bin, []string{"compact", "-store", store, "-compression", "off"}, nil)
			o2, _ := runCLICapture(c.bin, []string{"repack", "-apply", "-max-live-percent", "100", "-compression", "auto", "-store", store}, nil)
			prepOut += o2
		}
		_ = prepOut
		sz := DuStore(store)
		if phases[0].cold {
			dropCaches()
		}
		srv, cl = c.startPlain2(store, c, rep, true)
		if !phases[0].cold { // warm pass, untimed
			pool(len(objs), 4, func(i int) error { r := cl.Get(objs[i], 0, -1); return r.Err })
		}
		measure := func(ph getPhase) map[string]any {
			n := ph.nreq
			if n == 0 {
				if ph.mode == "whole" {
					n = len(objs)
				} else {
					n = 2000
				}
			}
			type op struct {
				o      *Obj
				off, n int64
			}
			ops := make([]op, n)
			rng := rand.New(rand.NewPCG(set.seed, 99))
			zf := rand.NewZipf(rng, ph.zipf, 1, uint64(len(objs)-1))
			for i := range ops {
				switch ph.mode {
				case "whole":
					ops[i] = op{objs[i%len(objs)], 0, -1}
				case "hot":
					ops[i] = op{objs[zf.Uint64()], 0, -1}
					if ph.rsizeStr != "" && ph.rsizeStr != "64kib" {
						o := ops[i].o
						ops[i].n = min(ph.rs, o.C.Size)
						ops[i].off = rng.Int64N(max(1, o.C.Size-ops[i].n))
					}
				default:
					o := objs[rng.IntN(len(objs))]
					k := min(ph.rs, o.C.Size)
					off := rng.Int64N(max(1, o.C.Size-k+1))
					switch ph.roff {
					case "hot":
						off = (o.C.Size / 3) % max(1, o.C.Size-k+1)
					case "eof":
						off = o.C.Size - k
					case "seq":
						o = objs[(int64(i)*k/max(1, o.C.Size))%int64(len(objs))]
						off = (int64(i) * k) % max(1, o.C.Size-k+1)
					case "spread":
						off = (int64(rng.IntN(8)) * (o.C.Size / 8)) % max(1, o.C.Size-k+1)
					}
					ops[i] = op{o, off, k}
				}
			}
			lats, ttfbs := make([]float64, n), make([]float64, n)
			var served atomic.Int64
			var badc atomic.Int64
			w := startWin(srv.PID())
			err := pool(n, ph.conc, func(i int) error {
				r := cl.Get(ops[i].o, ops[i].off, ops[i].n)
				if r.Err != nil {
					return r.Err
				}
				if !r.OK {
					badc.Add(1)
				}
				lats[i], ttfbs[i] = r.Lat, r.TTFB
				served.Add(r.N)
				return nil
			})
			m := w.end()
			if err != nil || badc.Load() > 0 {
				die("get failed (result invalid): err=%v mismatches=%d", err, badc.Load())
			}
			m["scenario"], m["rep"] = "get-"+ph.mode, rep
			m["params"] = map[string]any{"objs": set.objs, "size": set.size, "sizes": set.sizes, "kind": set.kind, "mode": ph.mode, "rsize": ph.rs, "conc": ph.conc, "cache": ph.cacheName, "phase": ph.name, "prep": *prep, "nreq": n, "roff": ph.roff, "load": *loadMode, "env": c.envs, "prof": c.prof, "revs": set.revs}
			m["served_bytes"] = served.Load()
			m["mib_s"] = mib(served.Load(), m["wall_s"].(float64))
			m["ops_s"] = float64(n) / m["wall_s"].(float64)
			m["lat"], m["ttfb"] = latStats(lats), latStats(ttfbs)
			m["read_amp_rchar"] = float64(m["srv_rchar"].(int64)) / float64(max(1, served.Load()))
			m["read_amp_dev"] = float64(m["dev_read_bytes"].(int64)) / float64(max(1, served.Load()))
			m["store"] = sz
			m["verified"] = true // every response byte-compared inside the window (or the sampled subset: see verify_mode)
			m["verify_mode"] = os.Getenv("P0_VERIFY")
			if c.prof != "" {
				srv.Snapshot()
				m["go_metrics"] = readMetrics(srv.ProfDir)
			}
			return m
		}
		var m map[string]any
		for pi, ph := range phases {
			if pi > 0 && ph.cold { // fresh process, empty page cache
				srv.Stop()
				dropCaches()
				srv, cl = c.startPlain2(store, c, rep, true)
			}
			m = measure(ph)
			if len(phases) > 1 {
				saved := c.label
				c.label = saved + "/" + ph.name
				emit(c, m)
				c.label = saved
			}
		}
		srv.Stop()
		if len(phases) == 1 {
			m["server_lifetime"] = rusageMap(srv)
			emit(c, m)
		}
		os.RemoveAll(store)
	}
}

// startPlain2 starts the server with profiling only for the measured process.
func (c *common) startPlain2(store string, cc *common, rep int, measured bool) (*Server, *S3) {
	bin, prof, env := c.bin, "", []string{}
	if measured {
		bin, prof, env = c.binary(), c.profDir(rep), c.envList()
	} else {
		for _, kv := range strings.Split(c.envs, ",") {
			if strings.TrimSpace(kv) != "" {
				env = append(env, strings.TrimSpace(kv))
			}
		}
	}
	srv, err := StartServer(bin, store, prof, c.srvArgs(), env)
	if err != nil {
		die("%v", err)
	}
	cl := &S3{c: newClient(max(c.conc, 4) + 8), base: srv.URL}
	if err := waitReady(cl.c, srv.URL); err != nil {
		die("%v", err)
	}
	return srv, cl
}

// ---- mixed ----

func cmdMixed(args []string) {
	fs := flag.NewFlagSet("mixed", flag.ExitOnError)
	c := addCommon(fs)
	set := addSet(fs)
	readers := fs.Int("readers", 4, "reader goroutines")
	writers := fs.Int("writers", 2, "writer goroutines")
	wsize := fs.String("wsize", "1mib", "writer object size")
	dur := fs.Duration("dur", 20*time.Second, "window")
	rsize := fs.String("rsize", "64kib", "reader range size (0 = whole)")
	fs.Parse(args)
	set.resolve(fs.Lookup("size").Value.String())
	rs := parseSize(*rsize)
	ws := parseSize(*wsize)
	for rep := 0; rep < c.reps; rep++ {
		store := c.newStore(rep)
		srv, cl := c.startPlain2(store, c, rep, false)
		cl.CreateBucket(set.bucket)
		objs := set.gen(0, 1<<30)
		if err := populate(cl, objs, PutOpt{Mode: "single", Payload: "unsigned"}, 4); err != nil {
			die("populate: %v", err)
		}
		srv.Stop()
		if c.cache == "cold" {
			dropCaches()
		}
		srv, cl = c.startPlain2(store, c, rep, true)
		var rl, wl []float64
		var mu sync.Mutex
		var bad atomic.Int64
		var rbytes, wbytes atomic.Int64
		var wobjs []*Obj
		deadline := time.Now().Add(*dur)
		w := startWin(srv.PID())
		var wg sync.WaitGroup
		var firstErr atomic.Value
		for r := 0; r < *readers; r++ {
			wg.Add(1)
			go func(r int) {
				defer wg.Done()
				rng := rand.New(rand.NewPCG(7, uint64(r)))
				for time.Now().Before(deadline) {
					o := objs[rng.IntN(len(objs))]
					var res GetRes
					if rs == 0 {
						res = cl.Get(o, 0, -1)
					} else {
						k := min(rs, o.C.Size)
						res = cl.Get(o, rng.Int64N(max(1, o.C.Size-k+1)), k)
					}
					if res.Err != nil {
						firstErr.Store(res.Err)
						return
					}
					if !res.OK {
						bad.Add(1)
					}
					rbytes.Add(res.N)
					mu.Lock()
					rl = append(rl, res.Lat)
					mu.Unlock()
				}
			}(r)
		}
		for k := 0; k < *writers; k++ {
			wg.Add(1)
			go func(k int) {
				defer wg.Done()
				for i := 0; time.Now().Before(deadline); i++ {
					o := &Obj{Bucket: set.bucket, Key: fmt.Sprintf("w/%d-%d", k, i), C: NewContent(set.kind, 5_000_000+uint64(k)*100000+uint64(i), ws)}
					o.Data = o.C.Bytes()
					o.SHA = o.C.SHA256()
					t := time.Now()
					if err := cl.Put(o, PutOpt{Mode: "single", Payload: "unsigned"}); err != nil {
						firstErr.Store(err)
						return
					}
					d := time.Since(t).Seconds()
					wbytes.Add(ws)
					mu.Lock()
					wl = append(wl, d)
					wobjs = append(wobjs, o)
					mu.Unlock()
				}
			}(k)
		}
		wg.Wait()
		m := w.end()
		if e := firstErr.Load(); e != nil || bad.Load() > 0 {
			die("mixed failed: %v mismatches=%d", e, bad.Load())
		}
		m["scenario"], m["rep"] = "mixed", rep
		m["params"] = map[string]any{"objs": set.objs, "size": set.size, "readers": *readers, "writers": *writers, "wsize": ws, "rsize": rs, "dur": dur.String(), "cache": c.cache, "prof": c.prof}
		m["read_lat"], m["write_lat"] = latStats(rl), latStats(wl)
		m["read_mib_s"] = mib(rbytes.Load(), m["wall_s"].(float64))
		m["write_mib_s"] = mib(wbytes.Load(), m["wall_s"].(float64))
		m["reads_s"], m["writes_s"] = float64(len(rl))/m["wall_s"].(float64), float64(len(wl))/m["wall_s"].(float64)
		if c.prof != "" {
			srv.Snapshot()
			m["go_metrics"] = readMetrics(srv.ProfDir)
		}
		srv.Stop()
		m["server_lifetime"] = rusageMap(srv)
		if !c.noVerify {
			s2, cl2 := c.startPlain(store)
			b1, e1 := VerifyAll(cl2, wobjs, 8)
			s2.Stop()
			if e1 != nil || b1 > 0 {
				die("mixed verify failed bad=%d err=%v", b1, e1)
			}
			m["verified"] = true
		}
		emit(c, m)
		os.RemoveAll(store)
	}
}

// ---- cli (maintenance operations) ----

func cmdCLI(args []string) {
	fs := flag.NewFlagSet("cli", flag.ExitOnError)
	c := addCommon(fs)
	store := fs.String("store", "", "store directory (command runs against it in place)")
	strace := fs.Bool("strace", false, "run under strace -f -c (separate from timing runs)")
	serve := fs.Bool("serve", false, "start a server on -store and substitute {EP} in the command; server cost is reported as srv_*")
	fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 {
		die("cli: command args required after --")
	}
	if c.cache == "cold" {
		dropCaches()
	}
	bin := c.binary()
	pd := c.profDir(0)
	env := append(os.Environ(), c.envList()...)
	if pd != "" {
		os.MkdirAll(pd, 0o755)
		env = append(env, "ZEROS3_P0_PROF_DIR="+pd)
	}
	var srv *Server
	var sw *win
	if *serve {
		var err error
		srv, err = StartServer(c.binary(), *store, pd, c.srvArgs(), c.envList())
		if err != nil {
			die("%v", err)
		}
		cl := newClient(4)
		if err := waitReady(cl, srv.URL); err != nil {
			die("%v", err)
		}
		for i := range rest {
			rest[i] = strings.ReplaceAll(rest[i], "{EP}", srv.URL)
		}
		sw = startWin(srv.PID())
		bin = c.bin // the client side is always the plain binary
	}
	var cenv []string
	for _, e := range env {
		if !strings.HasPrefix(e, "AWS_ACCESS_KEY_ID=") && !strings.HasPrefix(e, "AWS_SECRET_ACCESS_KEY=") {
			cenv = append(cenv, e)
		}
	}
	env = append(cenv, "AWS_ACCESS_KEY_ID="+accessKey, "AWS_SECRET_ACCESS_KEY="+secretKey)
	argv := append([]string{bin}, rest...)
	if *strace {
		sf := fmt.Sprintf("%s/strace-%s.txt", c.work, c.label)
		argv = append([]string{"strace", "-f", "-c", "-o", sf}, argv...)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	outPath := fmt.Sprintf("%s/cli-%s.out", c.work, c.label)
	os.MkdirAll(c.work, 0o755)
	of, _ := os.Create(outPath)
	cmd.Stdout, cmd.Stderr = of, of
	before := DuStore(*store)
	d0 := readDisk(diskDev)
	b0, s0, i0, t0 := readCPU()
	start := time.Now()
	if err := cmd.Start(); err != nil {
		die("%v", err)
	}
	pid := cmd.Process.Pid
	var peak, lastIO ProcStat
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(10 * time.Millisecond):
				p := readProc(pid)
				if p.RSSKB > peak.RSSKB {
					peak.RSSKB = p.RSSKB
				}
				if p.RChar > 0 || p.WChar > 0 {
					lastIO = p
				}
			}
		}
	}()
	err := cmd.Wait()
	wall := time.Since(start).Seconds()
	close(done)
	d1 := readDisk(diskDev)
	b1, s1, i1, t1 := readCPU()
	m := map[string]any{"scenario": "cli", "rep": 0, "wall_s": wall, "cmd": strings.Join(rest, " "), "exit_ok": err == nil, "cache": c.cache, "strace": *strace,
		"sampled_peak_rss_mib": float64(peak.RSSKB) / 1024, "sampled_rchar": lastIO.RChar, "sampled_wchar": lastIO.WChar, "sampled_syscr": lastIO.SysCR, "sampled_syscw": lastIO.SysCW,
		"dev_read_bytes": (d1.SectorsR - d0.SectorsR) * 512, "dev_write_bytes": (d1.SectorsW - d0.SectorsW) * 512, "dev_flushes": d1.Flushes - d0.Flushes,
		"store_before": before, "store_after": DuStore(*store)}
	if t1 > t0 {
		m["sys_steal_frac"] = (s1 - s0) / (t1 - t0)
		m["sys_busy_frac"] = (b1 - b0) / (t1 - t0)
		m["sys_iowait_frac"] = (i1 - i0) / (t1 - t0)
	}
	if ps := cmd.ProcessState; ps != nil {
		if ru, ok := ps.SysUsage().(*syscall.Rusage); ok {
			m["ru_maxrss_mib"] = float64(ru.Maxrss) / 1024
			m["ru_user_s"] = float64(ru.Utime.Sec) + float64(ru.Utime.Usec)/1e6
			m["ru_sys_s"] = float64(ru.Stime.Sec) + float64(ru.Stime.Usec)/1e6
			m["ru_majflt"], m["ru_minflt"] = ru.Majflt, ru.Minflt
		}
	}
	if srv != nil {
		for k, v := range sw.end() {
			m["srv_"+k] = v
		}
		if pd != "" {
			srv.Snapshot()
		}
		srv.Stop()
		m["srv_lifetime"] = rusageMap(srv)
	}
	if pd != "" {
		m["go_metrics"] = readMetrics(pd)
	}
	// first 2 KB of output (JSON summary of the command) kept for audit
	if f, e := os.Open(outPath); e == nil {
		sc := bufio.NewReaderSize(f, 1<<16)
		head := make([]byte, 4096)
		n, _ := sc.Read(head)
		m["out_head"] = string(head[:n])
		f.Close()
	}
	emit(c, m)
	if err != nil {
		os.Exit(1)
	}
	_ = filepath.Base
}

// getPhase is one measured read pattern against an already populated store.
type getPhase struct {
	name, mode, rsizeStr, roff, cacheName string
	rs                                    int64
	conc, nreq                            int
	zipf                                  float64
	cold                                  bool
}

// parsePhases reads "name|mode|rsize|roff|conc|nreq|warm|cold;..." (see -phases).
// A cold phase restarts the server on a dropped page cache before it runs.
func parsePhases(spec string, def getPhase) []getPhase {
	if strings.TrimSpace(spec) == "" {
		return []getPhase{def}
	}
	var out []getPhase
	for _, p := range strings.Split(spec, ";") {
		f := strings.Split(strings.TrimSpace(p), "|")
		if len(f) != 7 {
			die("bad phase %q: want name|mode|rsize|roff|conc|nreq|warm-or-cold", p)
		}
		conc, _ := strconv.Atoi(f[4])
		nreq, _ := strconv.Atoi(f[5])
		out = append(out, getPhase{name: f[0], mode: f[1], rsizeStr: f[2], rs: parseSize(f[2]), roff: f[3], conc: conc, nreq: nreq,
			zipf: def.zipf, cacheName: f[6], cold: f[6] == "cold"})
	}
	return out
}
