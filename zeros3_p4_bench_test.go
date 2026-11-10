package main

// Z4-P4 representation benchmark driver. It is a test only so that it can use
// the production package internals; it runs nothing unless ZEROS3_P4_FILES is
// set. One process measures one (object set, mode) so that ru_maxrss and the
// CPU accounting belong to that run alone:
//
//	ZEROS3_P4_FILES=a.tar,b.tar ZEROS3_P4_MODE=shadow ZEROS3_P4_DIR=/disk/store \
//	ZEROS3_P4_OUT=out.jsonl go test -run TestP4Probe -count=1 -timeout 1h
//
// An entry may be gen:merkle:N, gen:car:N or gen:tar:N for deterministic
// synthetic archives (N = key/block/file count).

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type p4Row struct {
	Label, Mode, Name string
	Size              int64
	Conc              int

	PutMS, PutMBs float64
	CPUMS         float64 // user+sys of the whole process during the timed phase
	AllocMB       float64
	HeapPeakMB    float64
	RSSMB         float64
	Goroutines    int

	Built, Rejected     int64
	AnalyzeMS, PublMS   float64
	ProofMS, RootMS     float64
	ReachMS             float64
	Components          int64
	RepRoots            int
	VirtualDescs        int
	RootBytes           int64
	LooseFiles, Packs   int
	PackBytes, LooseB   int64
	SegFiles            int
	StoreFiles          int
	StoreBytes          int64
	TmpFiles            int
	GetWholeMS          float64
	GetWholeOK          bool
	R4kUS, R64kUS       float64
	R1mUS               float64
	RangeOK             bool
	VerifyMS, DeepMS    float64
	VerifyOK            bool
	RepsVerified        int
	RepReads, Fallbacks int64
	QuiesceMS           float64 // deferred mode: wait until queued builds finish
	CompUnique          int     // distinct components over all roots
	CompBytes           int64   // logical bytes of the distinct components
	CompMedian          int64   // median component size
	GCMS, RepackMS      float64 // offline gc -apply and repack on the finished store
	GCBytes, RepackB    int64   // store bytes after each
	ReopenDeepMS        float64 // verify -deep after gc + repack, reopened
	ReopenOK            bool
}

func p4Gen(spec string) (string, []byte, error) {
	parts := strings.Split(spec, ":")
	n, _ := strconv.Atoi(parts[len(parts)-1])
	switch parts[1] {
	case "merkle":
		keys := repTestKeys(n, 77)
		return "merkle-" + parts[2], repTestMerkleSnapshot(3, keys, nil), nil
	case "car":
		return "car-" + parts[2], repTestCAR(repTestBlocks(n, 3000, 5)), nil
	case "tar":
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		r := rand.New(rand.NewSource(9))
		for i := 0; i < n; i++ {
			body := make([]byte, 200+r.Intn(60000))
			r.Read(body)
			tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("d%03d/f%06d.bin", i%97, i), Size: int64(len(body)), Mode: 0o644, Format: tar.FormatGNU})
			tw.Write(body)
		}
		tw.Close()
		return "tar-" + parts[2], buf.Bytes(), nil
	}
	return "", nil, fmt.Errorf("unknown generator %q", spec)
}

func p4Walk(root string) (files int, bytes int64, loose, packs, segs, tmp int, packB, looseB int64) {
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		files++
		bytes += fi.Size()
		rel, _ := filepath.Rel(root, p)
		switch {
		case strings.HasPrefix(rel, "tmp"):
			tmp++
		case strings.HasSuffix(p, ".pack"):
			packs++
			packB += fi.Size()
		case strings.HasSuffix(p, ".seg"):
			segs++
		case strings.HasPrefix(rel, "chunks") || strings.HasPrefix(rel, "cas"):
			loose++
			looseB += fi.Size()
		}
		return nil
	})
	return
}

func p4CPU() float64 {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return float64(ru.Utime.Sec*1000+int64(ru.Utime.Usec)/1000) + float64(ru.Stime.Sec*1000+int64(ru.Stime.Usec)/1000)
}

func p4RSSMB() float64 {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return float64(ru.Maxrss) / 1024
}

func TestP4Probe(t *testing.T) {
	files := os.Getenv("ZEROS3_P4_FILES")
	if files == "" {
		t.Skip("ZEROS3_P4_FILES not set")
	}
	mode, err := parseRepMode(os.Getenv("ZEROS3_P4_MODE"))
	if err != nil {
		t.Fatal(err)
	}
	conc, _ := strconv.Atoi(os.Getenv("ZEROS3_P4_CONC"))
	if conc < 1 {
		conc = 1
	}
	dir := os.Getenv("ZEROS3_P4_DIR")
	if dir == "" {
		dir = t.TempDir()
	} else {
		os.RemoveAll(dir)
		os.MkdirAll(dir, 0o755)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	p4Configure(s)
	s.setRepMode(mode)
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	type obj struct {
		name string
		data []byte
	}
	var objs []obj
	for _, f := range strings.Split(files, ",") {
		if strings.HasPrefix(f, "gen:") {
			n, d, err := p4Gen(f)
			if err != nil {
				t.Fatal(err)
			}
			objs = append(objs, obj{n, d})
			continue
		}
		d, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		objs = append(objs, obj{filepath.Base(f), d})
	}
	var total int64
	for _, o := range objs {
		total += int64(len(o.data))
	}
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	cpu0 := p4CPU()
	var peak uint64
	stop := make(chan struct{})
	var pwg sync.WaitGroup
	pwg.Add(1)
	go func() {
		defer pwg.Done()
		var ms runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				runtime.ReadMemStats(&ms)
				if ms.HeapInuse > peak {
					peak = ms.HeapInuse
				}
			}
		}
	}()
	var profFile *os.File
	if pf := os.Getenv("ZEROS3_P4_CPUPROF"); pf != "" {
		profFile, err = os.Create(pf)
		if err != nil {
			t.Fatal(err)
		}
		pprof.StartCPUProfile(profFile)
	}
	t0 := time.Now()
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for _, o := range objs {
		sem <- struct{}{}
		wg.Add(1)
		go func(o obj) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := s.PutObject("b", o.name, o.data, "application/octet-stream", nil); err != nil {
				t.Errorf("put %s: %v", o.name, err)
			}
		}(o)
	}
	wg.Wait()
	putDur := time.Since(t0)
	qs := time.Now()
	p4Quiesce(s)
	quiesce := time.Since(qs)
	if profFile != nil {
		pprof.StopCPUProfile()
		profFile.Close()
	}
	cpuAll := p4CPU() - cpu0
	runtime.ReadMemStats(&m1)
	close(stop)
	pwg.Wait()
	row := p4Row{Label: os.Getenv("ZEROS3_P4_LABEL"), Mode: mode.String(), Name: strconv.Itoa(len(objs)) + " obj", Size: total, Conc: conc,
		PutMS: float64(putDur.Microseconds()) / 1000, PutMBs: float64(total) / (1 << 20) / putDur.Seconds(),
		CPUMS: cpuAll, AllocMB: float64(m1.TotalAlloc-m0.TotalAlloc) / (1 << 20), HeapPeakMB: float64(peak) / (1 << 20),
		RSSMB: p4RSSMB(), Goroutines: runtime.NumGoroutine(), QuiesceMS: float64(quiesce.Microseconds()) / 1000}
	if len(objs) == 1 {
		row.Name = objs[0].name
	}
	bt := &s.rep.bt
	row.Built, row.Rejected = bt.built.Load(), bt.rejected.Load()
	row.AnalyzeMS, row.PublMS = float64(bt.analyzeNS.Load())/1e6, float64(bt.publishNS.Load())/1e6
	row.ProofMS, row.RootMS, row.ReachMS = float64(bt.proofNS.Load())/1e6, float64(bt.rootNS.Load())/1e6, float64(bt.rch.Load())/1e6
	row.Components = bt.components.Load()
	roots, _ := s.repRootFiles()
	row.RepRoots = len(roots)
	for _, u := range roots {
		if r, err := s.loadRepRoot(u); err == nil {
			row.VirtualDescs += len(r.Descs)
		}
	}
	row.RootBytes, _ = s.repRootBytes()
	row.StoreFiles, row.StoreBytes, row.LooseFiles, row.Packs, row.SegFiles, row.TmpFiles, row.PackBytes, row.LooseB = p4Walk(dir)

	// reads
	var readProf *os.File
	if pf := os.Getenv("ZEROS3_P4_READPROF"); pf != "" {
		readProf, _ = os.Create(pf)
		pprof.StartCPUProfile(readProf)
	}
	getOK, rangeOK := true, true
	var getDur time.Duration
	rr := rand.New(rand.NewSource(1))
	var d4, d64, d1m time.Duration
	var n4, n64, n1m int
	for _, o := range objs {
		want := sha256.Sum256(o.data)
		g0 := time.Now()
		e, man, err := s.HeadObject("b", o.name)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		rd := s.openObjectReader(man, byteRange{start: 0, end: e.size - 1})
		for {
			b, err := rd.next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("get %s: %v", o.name, err)
			}
			h.Write(b)
		}
		rd.close()
		getDur += time.Since(g0)
		var got [32]byte
		h.Sum(got[:0])
		if got != want {
			getOK = false
		}
		for _, spec := range []struct {
			n   int64
			cnt int
			d   *time.Duration
			c   *int
		}{{4 << 10, 100, &d4, &n4}, {64 << 10, 50, &d64, &n64}, {1 << 20, 20, &d1m, &n1m}} {
			if int64(len(o.data)) < 2*spec.n {
				continue
			}
			for i := 0; i < spec.cnt; i++ {
				off := rr.Int63n(int64(len(o.data)) - spec.n)
				r0 := time.Now()
				rd := s.openObjectReader(man, byteRange{start: off, end: off + spec.n - 1})
				var out []byte
				for {
					b, err := rd.next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatalf("range %s: %v", o.name, err)
					}
					out = append(out, b...)
				}
				rd.close()
				*spec.d += time.Since(r0)
				*spec.c++
				if !bytes.Equal(out, o.data[off:off+spec.n]) {
					rangeOK = false
				}
			}
		}
	}
	if readProf != nil {
		pprof.StopCPUProfile()
		readProf.Close()
	}
	row.GetWholeMS, row.GetWholeOK, row.RangeOK = float64(getDur.Microseconds())/1000, getOK, rangeOK
	if n4 > 0 {
		row.R4kUS = float64(d4.Microseconds()) / float64(n4)
	}
	if n64 > 0 {
		row.R64kUS = float64(d64.Microseconds()) / float64(n64)
	}
	if n1m > 0 {
		row.R1mUS = float64(d1m.Microseconds()) / float64(n1m)
	}
	row.RepReads, row.Fallbacks = s.rep.reads.Load(), s.rep.fallbacks.Load()
	v0 := time.Now()
	vr, err := s.Verify(false)
	row.VerifyMS = float64(time.Since(v0).Microseconds()) / 1000
	row.VerifyOK = err == nil && vr.OK()
	v0 = time.Now()
	vd, err := s.Verify(true)
	row.DeepMS = float64(time.Since(v0).Microseconds()) / 1000
	row.VerifyOK = row.VerifyOK && err == nil && vd.OK()
	row.RepsVerified = vd.RepresentationsVerified
	// component economics: distinct components over all roots and their logical sizes
	uniq := map[[32]byte]int64{}
	for _, u := range roots {
		r, err := s.loadRepRoot(u)
		if err != nil {
			continue
		}
		src := s.newRepSource(r.Descs, nil)
		reach, err := (&repEnv{Src: src, Limits: repProdLimits}).reachable(r.Rep)
		if err == nil {
			for _, id := range reach.Order {
				if _, ok := uniq[id]; !ok {
					n, _ := src.Size(id)
					uniq[id] = n
				}
			}
		}
		src.close()
	}
	var sizes []int64
	for _, n := range uniq {
		sizes = append(sizes, n)
		row.CompBytes += n
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] < sizes[j] })
	row.CompUnique = len(sizes)
	if len(sizes) > 0 {
		row.CompMedian = sizes[len(sizes)/2]
	}
	s.Close()
	if os.Getenv("ZEROS3_P4_MAINT") != "" {
		g0 := time.Now()
		_, gerr := gcCollect(dir, true)
		row.GCMS = float64(time.Since(g0).Microseconds()) / 1000
		_, row.GCBytes, _, _, _, _, _, _ = p4Walk(dir)
		r0 := time.Now()
		_, rerr := repackStore(dir, repackOptions{TargetBytes: 64 << 20, MaxLivePercent: 100})
		row.RepackMS = float64(time.Since(r0).Microseconds()) / 1000
		_, row.RepackB, _, _, _, _, _, _ = p4Walk(dir)
		s2, oerr := OpenStore(dir)
		if oerr == nil {
			d0 := time.Now()
			v2, verr := s2.Verify(true)
			row.ReopenDeepMS = float64(time.Since(d0).Microseconds()) / 1000
			row.ReopenOK = verr == nil && v2.OK() && gerr == nil && rerr == nil
			s2.Close()
		}
	}
	line, _ := json.Marshal(row)
	if out := os.Getenv("ZEROS3_P4_OUT"); out != "" {
		f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			f.Write(append(line, '\n'))
			f.Close()
		}
	}
	fmt.Println(string(line))
	if !row.GetWholeOK || !row.RangeOK || !row.VerifyOK {
		t.Fatalf("exactness check failed: get=%v range=%v verify=%v", row.GetWholeOK, row.RangeOK, row.VerifyOK)
	}
	_ = hex.EncodeToString
}

func p4Configure(s *Store) {
	if os.Getenv("ZEROS3_P4_BUILD") == "deferred" {
		w, _ := strconv.Atoi(os.Getenv("ZEROS3_P4_WORKERS"))
		s.enableRepDeferred(w, 0)
	}
}

func p4Quiesce(s *Store) { s.repQuiesce() }

// TestP4Interference measures foreground latency while one heavy representation
// is built. A fixed-rate foreground (PUT 64 KiB + GET of a 1 MiB range of a
// pre-loaded object) runs for the whole test; at t=1s the heavy object is PUT.
// ZEROS3_P4_INTERFERE = off | inline | deferred; ZEROS3_P4_HEAVY = file;
// ZEROS3_P4_RATE = foreground operations per second (default 200).
func TestP4Interference(t *testing.T) {
	mode := os.Getenv("ZEROS3_P4_INTERFERE")
	if mode == "" {
		t.Skip("ZEROS3_P4_INTERFERE not set")
	}
	heavy, err := os.ReadFile(os.Getenv("ZEROS3_P4_HEAVY"))
	if err != nil {
		t.Fatal(err)
	}
	rate, _ := strconv.Atoi(os.Getenv("ZEROS3_P4_RATE"))
	if rate < 1 {
		rate = 200
	}
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.CreateBucket("b")
	if mode != "off" {
		s.setRepMode(repModeShadow)
	}
	if mode == "deferred" {
		s.enableRepDeferred(1, 0)
	}
	base := genRandomBytes(1, 8<<20)
	if _, err := s.PutObject("b", "base", base, "x", nil); err != nil {
		t.Fatal(err)
	}
	type sample struct {
		at  time.Duration
		dur time.Duration
		get bool
	}
	var mu sync.Mutex
	var samples []sample
	t0 := time.Now()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	tick := time.NewTicker(time.Second / time.Duration(rate))
	go func() {
		i := 0
		for {
			select {
			case <-stop:
				tick.Stop()
				return
			case <-tick.C:
			}
			i++
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				s.fg.enter() // what ServeHTTP does for a real request
				defer s.fg.exit()
				st := time.Now()
				get := i%2 == 0
				if get {
					_, man, err := s.HeadObject("b", "base")
					if err != nil {
						return
					}
					off := int64(i%7) << 20
					rd := s.openObjectReader(man, byteRange{start: off, end: off + (1 << 20) - 1})
					for {
						if _, err := rd.next(); err != nil {
							break
						}
					}
					rd.close()
				} else {
					s.PutObject("b", fmt.Sprint("fg", i), genRandomBytes(int64(i), 64<<10), "x", nil)
				}
				mu.Lock()
				samples = append(samples, sample{time.Since(t0), time.Since(st), get})
				mu.Unlock()
			}(i)
		}
	}()
	time.Sleep(time.Second)
	cpu0 := p4CPU()
	hs := time.Now()
	if _, err := s.PutObject("b", "heavy", heavy, "x", nil); err != nil {
		t.Fatal(err)
	}
	ack := time.Since(hs)
	s.repQuiesce()
	done := time.Since(hs)
	cpu := p4CPU() - cpu0
	time.Sleep(500 * time.Millisecond)
	close(stop)
	wg.Wait()
	pct := func(xs []time.Duration, p float64) float64 {
		if len(xs) == 0 {
			return 0
		}
		sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
		return float64(xs[min(len(xs)-1, int(float64(len(xs))*p))].Microseconds()) / 1000
	}
	var before, during []time.Duration
	for _, sm := range samples {
		if sm.at < time.Second {
			before = append(before, sm.dur)
		} else if sm.at < time.Second+done {
			during = append(during, sm.dur)
		}
	}
	row := map[string]any{"mode": mode, "heavy_mb": float64(len(heavy)) / 1e6, "ack_ms": float64(ack.Microseconds()) / 1000, "avail_ms": float64(done.Microseconds()) / 1000,
		"cpu_ms": cpu, "fg_ops_before": len(before), "fg_ops_during": len(during),
		"before_p50_ms": pct(before, .5), "before_p99_ms": pct(before, .99),
		"during_p50_ms": pct(during, .5), "during_p95_ms": pct(during, .95), "during_p99_ms": pct(during, .99), "during_max_ms": pct(during, 1)}
	b, _ := json.Marshal(row)
	if out := os.Getenv("ZEROS3_P4_OUT"); out != "" {
		if f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			f.Write(append(b, '\n'))
			f.Close()
		}
	}
	fmt.Println(string(b))
}
