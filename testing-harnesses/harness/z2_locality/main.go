// z2_locality measures how physical pack layout and the packed read path
// interact, through real zeros3 binaries only (CLI + HTTP + optional strace).
//
// For each fixture it ingests objects, then builds up to two compacted
// stores from the same loose chunks:
//
//	digest    packs in chunk-digest order (the pre-Z2-13 layout)
//	locality  packs in manifest first-reference order (`compact -layout locality`)
//
// and serves them with the baseline binary (old one-record-per-ReadAt reader)
// and/or the current binary (coalesced reader). The matrix is therefore
//
//	A baseline : digest layout   + baseline reader
//	B reader   : digest layout   + current reader
//	C layout   : locality layout + current reader
//
// Per config it records full-GET throughput, 1/16/64 MiB range latency,
// small random reads, 8-way concurrent range readers, server peak RSS and,
// under strace when available, pack open() and pread64 counts for one full
// GET. Structural locality metrics are computed offline from manifest chunk
// order versus the pack-v1 indexes (documented in zeros3.go section 4b).
//
// Usage:
//
//	ZEROS3_BIN=cur [BASELINE_BIN=old] go run ./harness/z2_locality \
//	    [-size-mib 256] [-pack-size-mib 64] [-compression off] [-fixtures seq,site,ckpt]
package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	accessKeyID     = "AKIAZEROS3EXAMPLE01"
	secretAccessKey = "zeros3exampleSecretKeyForM1TestingOnly01"
	region          = "us-east-1"
	bucket          = "loc"
)

var failed bool

func check(name string, ok bool, detail string, args ...any) {
	if ok {
		fmt.Printf("PASS: %s\n", name)
		return
	}
	failed = true
	fmt.Printf("FAIL: %s: %s\n", name, fmt.Sprintf(detail, args...))
}

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ x>>30) * 0xbf58476d1ce4e5b9
	x = (x ^ x>>27) * 0x94d049bb133111eb
	return x ^ x>>31
}

// ---------------------------------------------------------------------------
// Deterministic objects.

// gen is a seekable deterministic byte stream.
type gen struct {
	size int64
	at   func(p []byte, off int64) // fills p with the bytes at off
	pos  int64
}

func (g *gen) Read(p []byte) (int, error) {
	if g.pos >= g.size {
		return 0, io.EOF
	}
	if rest := g.size - g.pos; int64(len(p)) > rest {
		p = p[:rest]
	}
	g.at(p, g.pos)
	g.pos += int64(len(p))
	return len(p), nil
}

func (g *gen) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		g.pos = off
	case io.SeekCurrent:
		g.pos += off
	case io.SeekEnd:
		g.pos = g.size + off
	}
	return g.pos, nil
}

func (g *gen) window(off, n int64) []byte {
	out := make([]byte, n)
	g.at(out, off)
	return out
}

func randomAt(seed uint64) func(p []byte, off int64) {
	return func(p []byte, off int64) {
		var w [8]byte
		for i := 0; i < len(p); {
			at := off + int64(i)
			binary.LittleEndian.PutUint64(w[:], splitmix(seed<<40^uint64(at/8)))
			i += copy(p[i:], w[at%8:])
		}
	}
}

// textAt is compressible word-salad text, deterministic per (seed, offset).
func textAt(seed uint64) func(p []byte, off int64) {
	words := strings.Fields("function return const let var if else for while this new await async export import class extends document window render state props fetch json <div> </div> margin padding display color background border-radius")
	return func(p []byte, off int64) {
		// 64-byte cells keyed by cell index so any window is reproducible.
		for i := 0; i < len(p); {
			at := off + int64(i)
			cell := uint64(at / 64)
			var sb strings.Builder
			x := splitmix(seed<<32 ^ cell)
			for sb.Len() < 64 {
				sb.WriteString(words[x%uint64(len(words))])
				sb.WriteByte(' ')
				x = splitmix(x)
			}
			i += copy(p[i:], sb.String()[at%64:64])
		}
	}
}

// ckptAt is checkpoint version v: the base random weights with ~1 in 8
// one-MiB regions rewritten per version (so versions share most chunks).
func ckptAt(v int) func(p []byte, off int64) {
	base := randomAt(1)
	return func(p []byte, off int64) {
		for i := 0; i < len(p); {
			at := off + int64(i)
			region := at >> 20
			n := min(int64(len(p)-i), (region+1)<<20-at)
			if v > 1 && splitmix(uint64(region)*131+uint64(v))%8 == 0 {
				randomAt(uint64(100+v))(p[i:i+int(n)], at)
			} else {
				base(p[i:i+int(n)], at)
			}
			i += int(n)
		}
	}
}

type fixtureObject struct {
	key  string
	g    *gen
	etag string
}

type fixture struct {
	name     string
	objs     []*fixtureObject
	compress string // compact -compression
	primary  int    // index of the object ranged/streamed in detail (-1: none)
}

func buildFixture(name string, sizeMiB int64) *fixture {
	f := &fixture{name: name, compress: "off"}
	switch name {
	case "seq":
		f.objs = []*fixtureObject{{key: "seq/big", g: &gen{size: sizeMiB << 20, at: randomAt(1)}}}
	case "ckpt":
		per := sizeMiB / 2 << 20
		for v := 1; v <= 3; v++ {
			f.objs = append(f.objs, &fixtureObject{key: fmt.Sprintf("ckpt/model-v%d.bin", v), g: &gen{size: per, at: ckptAt(v)}})
		}
	case "site":
		f.compress = "auto"
		for i := 0; i < 400; i++ {
			sz := int64(4<<10) + int64(splitmix(uint64(i)*7)%(900<<10))
			f.objs = append(f.objs, &fixtureObject{key: fmt.Sprintf("site/f%03d.js", i), g: &gen{size: sz, at: textAt(uint64(i + 1))}})
		}
		f.primary = -1
	default:
		fmt.Println("unknown fixture", name)
		os.Exit(2)
	}
	return f
}

// ---------------------------------------------------------------------------
// Server and client plumbing.

type server struct {
	cmd  *exec.Cmd
	addr string
	cli  *s3.Client
	// stracePID is the traced server pid when running under strace.
	strace bool
}

func freeAddr() string {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	defer l.Close()
	return l.Addr().String()
}

func newClient(addr string) *s3.Client {
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")))
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("http://" + addr)
		o.UsePathStyle = true
	})
}

func startServer(bin, store string, straceLog string) *server {
	addr := freeAddr()
	args := []string{"serve", "-store", store, "-addr", addr}
	var cmd *exec.Cmd
	if straceLog != "" {
		cmd = exec.Command("strace", append([]string{"-f", "-qq", "-s", "48", "-e", "trace=openat,pread64,read", "-o", straceLog, bin}, args...)...)
	} else {
		cmd = exec.Command(bin, args...)
	}
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		fmt.Println("FAIL: start:", err)
		os.Exit(2)
	}
	for deadline := time.Now().Add(120 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		} else if time.Now().After(deadline) {
			fmt.Println("FAIL: server did not start")
			os.Exit(2)
		}
	}
	return &server{cmd: cmd, addr: addr, cli: newClient(addr), strace: straceLog != ""}
}

func (s *server) pid() int {
	if !s.strace {
		return s.cmd.Process.Pid
	}
	b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", s.cmd.Process.Pid, s.cmd.Process.Pid))
	if f := strings.Fields(string(b)); len(f) > 0 {
		n, _ := strconv.Atoi(f[0])
		return n
	}
	return 0
}

// rssKiB reads VmRSS/VmHWM (KiB) of the server.
func (s *server) rssKiB() (rss, hwm int64) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", s.pid()))
	if err != nil {
		return 0, 0
	}
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Fields(ln)
		if len(f) >= 2 && (f[0] == "VmRSS:" || f[0] == "VmHWM:") {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			if f[0] == "VmRSS:" {
				rss = n
			} else {
				hwm = n
			}
		}
	}
	return
}

func (s *server) stop() {
	if s.strace {
		_ = exec.Command("pkill", "-INT", "-P", strconv.Itoa(s.cmd.Process.Pid)).Run()
	} else {
		_ = s.cmd.Process.Signal(os.Interrupt)
	}
	_ = s.cmd.Wait()
}

func (s *server) get(key, rng string) ([]byte, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if rng != "" {
		in.Range = aws.String(rng)
	}
	out, err := s.cli.GetObject(context.Background(), in)
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (s *server) drain(key, rng string) (int64, error) {
	in := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if rng != "" {
		in.Range = aws.String(rng)
	}
	out, err := s.cli.GetObject(context.Background(), in)
	if err != nil {
		return 0, err
	}
	defer out.Body.Close()
	return io.Copy(io.Discard, out.Body)
}

// ---------------------------------------------------------------------------
// Physical locality metrics from manifests + pack-v1 indexes.

type ploc struct {
	pack   int
	off    uint64 // payload offset
	stored uint32
}

func loadLocator(store string) (map[[32]byte]ploc, int, error) {
	var files []string
	for _, pat := range []string{"packs/*.pack", "tiers/*/packs/*.pack"} {
		m, _ := filepath.Glob(filepath.Join(store, pat))
		files = append(files, m...)
	}
	sort.Strings(files)
	loc := map[[32]byte]ploc{}
	for pi, p := range files {
		f, err := os.Open(p)
		if err != nil {
			return nil, 0, err
		}
		st, _ := f.Stat()
		var foot [64]byte
		if _, err := f.ReadAt(foot[:], st.Size()-64); err != nil || string(foot[:4]) != "ZSPF" {
			f.Close()
			return nil, 0, fmt.Errorf("%s: bad footer", p)
		}
		count := binary.LittleEndian.Uint64(foot[8:16])
		idxOff := binary.LittleEndian.Uint64(foot[16:24])
		idx := make([]byte, count*52)
		if _, err := f.ReadAt(idx, int64(idxOff)); err != nil {
			f.Close()
			return nil, 0, err
		}
		f.Close()
		for i := uint64(0); i < count; i++ {
			e := idx[i*52 : (i+1)*52]
			var sum [32]byte
			copy(sum[:], e[:32])
			if _, dup := loc[sum]; !dup {
				loc[sum] = ploc{pack: pi, off: binary.LittleEndian.Uint64(e[32:40]), stored: binary.LittleEndian.Uint32(e[40:44])}
			}
		}
	}
	return loc, len(files), nil
}

type manifestChunks struct {
	ETag   string `json:"etag"`
	Chunks []struct {
		SHA256 string `json:"sha256"`
		Length int64  `json:"length"`
	} `json:"chunks"`
}

type localityMetrics struct {
	Packs, Chunks      int
	Pairs              int
	SamePackPct        float64
	ContiguousPct      float64
	Runs               int
	AvgRun             float64
	P50Run, P95Run     int
	TransitionsPerGiB  float64
	LogicalGiB         float64
	TotalPackTransitns int
}

// measureLocality classifies consecutive manifest chunk pairs of the given
// objects (in the given order) against the physical locator.
func measureLocality(store string, f *fixture) (localityMetrics, error) {
	loc, packs, err := loadLocator(store)
	if err != nil {
		return localityMetrics{}, err
	}
	byETag := map[string]*manifestChunks{}
	files, _ := filepath.Glob(filepath.Join(store, "manifests", "*.json"))
	for _, p := range files {
		b, _ := os.ReadFile(p)
		var m manifestChunks
		if json.Unmarshal(b, &m) == nil {
			byETag[strings.Trim(m.ETag, `"`)] = &m
		}
	}
	var m localityMetrics
	m.Packs = packs
	var prev *ploc
	run := 0
	var runs []int
	var total int64
	for _, o := range f.objs {
		mc := byETag[strings.Trim(o.etag, `"`)]
		if mc == nil {
			return m, fmt.Errorf("no manifest for %s", o.key)
		}
		prev = nil
		for _, c := range mc.Chunks {
			raw, _ := hex.DecodeString(c.SHA256)
			var sum [32]byte
			copy(sum[:], raw)
			l, ok := loc[sum]
			if !ok {
				return m, fmt.Errorf("chunk %s of %s is not packed", c.SHA256, o.key)
			}
			m.Chunks++
			total += c.Length
			if prev != nil {
				m.Pairs++
				if prev.pack == l.pack {
					m.SamePackPct++
					if prev.off+uint64(prev.stored)+44 == l.off {
						m.ContiguousPct++
						run++
					} else {
						runs = append(runs, run)
						run = 1
					}
				} else {
					m.TotalPackTransitns++
					runs = append(runs, run)
					run = 1
				}
			} else {
				if run > 0 {
					runs = append(runs, run)
				}
				run = 1
			}
			cur := l
			prev = &cur
		}
		if run > 0 {
			runs = append(runs, run)
			run = 0
		}
	}
	if m.Pairs > 0 {
		m.SamePackPct = 100 * m.SamePackPct / float64(m.Pairs)
		m.ContiguousPct = 100 * m.ContiguousPct / float64(m.Pairs)
	}
	sort.Ints(runs)
	m.Runs = len(runs)
	if len(runs) > 0 {
		m.AvgRun = float64(m.Chunks) / float64(len(runs))
		m.P50Run = runs[len(runs)/2]
		m.P95Run = runs[min(len(runs)-1, len(runs)*95/100)]
	}
	m.LogicalGiB = float64(total) / (1 << 30)
	if m.LogicalGiB > 0 {
		m.TransitionsPerGiB = float64(m.TotalPackTransitns) / m.LogicalGiB
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Measurements.

type configResult struct {
	Name           string
	FullMiBps      float64
	Rng1MsMean     float64
	Rng1MsP50      float64
	Rng1MsP95      float64
	Rng16MiBps     float64
	Rng64MiBps     float64
	Small64KMsMean float64
	ConcMiBps      float64
	RSSBaseMiB     float64
	RSSFullDelta   float64
	RSSConcDelta   float64
	PackOpens      int
	PreadCalls     int
	PreadMiB       float64
	Exact          bool
}

func pct(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	return s[min(len(s)-1, int(float64(len(s))*p))]
}

func mean(xs []float64) float64 {
	t := 0.0
	for _, x := range xs {
		t += x
	}
	return t / float64(max(1, len(xs)))
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func measureConfig(name, bin, store string, f *fixture, useStrace bool, tmp string) configResult {
	r := configResult{Name: name, Exact: true}
	// A fresh server for RSS of one full pass.
	srv := startServer(bin, store, "")
	rss0, _ := srv.rssKiB()
	r.RSSBaseMiB = float64(rss0) / 1024
	var totalBytes int64
	for _, o := range f.objs {
		totalBytes += o.g.size
	}
	passAll := func() (time.Duration, error) {
		t0 := time.Now()
		for _, o := range f.objs {
			if _, err := srv.drain(o.key, ""); err != nil {
				return 0, err
			}
		}
		return time.Since(t0), nil
	}
	// Byte-exactness once per config (md5 vs the PUT ETag).
	for _, o := range f.objs {
		out, err := srv.cli.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(o.key)})
		if err != nil {
			r.Exact = false
			continue
		}
		h := md5.New()
		_, _ = io.Copy(h, out.Body)
		out.Body.Close()
		if hex.EncodeToString(h.Sum(nil)) != strings.Trim(o.etag, `"`) {
			r.Exact = false
		}
	}
	_, hwm1 := srv.rssKiB()
	r.RSSFullDelta = float64(hwm1)/1024 - r.RSSBaseMiB
	var rates []float64
	for i := 0; i < 4; i++ {
		d, err := passAll()
		if err != nil {
			r.Exact = false
			break
		}
		if i > 0 { // first pass warms the page cache
			rates = append(rates, float64(totalBytes)/(1<<20)/d.Seconds())
		}
	}
	r.FullMiBps = pct(rates, 0.5)

	// Ranges on the primary object (the largest for site).
	prim := f.objs[0]
	if f.primary >= 0 {
		prim = f.objs[f.primary]
	}
	for _, o := range f.objs {
		if o.g.size > prim.g.size {
			prim = o
		}
	}
	// Latency is timed on a drained body; byte-exactness is checked on a few
	// untimed samples so harness-side buffering never enters the numbers.
	rngRun := func(n int64, count int, seed uint64) (lat []float64, exact bool) {
		exact = true
		for i := 0; i < count; i++ {
			if prim.g.size <= n {
				return
			}
			off := int64(splitmix(seed+uint64(i)*7919) % uint64(prim.g.size-n))
			spec := fmt.Sprintf("bytes=%d-%d", off, off+n-1)
			t0 := time.Now()
			got, err := srv.drain(prim.key, spec)
			lat = append(lat, ms(time.Since(t0)))
			if err != nil || got != n {
				exact = false
			}
			if i < 3 {
				body, err := srv.get(prim.key, spec)
				if err != nil || !bytes.Equal(body, prim.g.window(off, n)) {
					exact = false
				}
			}
		}
		return
	}
	lat1, ok1 := rngRun(1<<20, 200, 11)
	r.Rng1MsMean, r.Rng1MsP50, r.Rng1MsP95 = mean(lat1), pct(lat1, 0.5), pct(lat1, 0.95)
	lat16, ok16 := rngRun(16<<20, 12, 13)
	if len(lat16) > 0 {
		r.Rng16MiBps = 16 / (mean(lat16) / 1000)
	}
	lat64, ok64 := rngRun(64<<20, 4, 17)
	if len(lat64) > 0 {
		r.Rng64MiBps = 64 / (mean(lat64) / 1000)
	}
	latS, okS := rngRun(64<<10, 300, 19)
	r.Small64KMsMean = mean(latS)
	r.Exact = r.Exact && ok1 && ok16 && ok64 && okS

	// 8 concurrent 16 MiB range readers.
	if prim.g.size > 32<<20 {
		rssBefore, _ := srv.rssKiB()
		t0 := time.Now()
		var wg sync.WaitGroup
		var moved int64
		var mu sync.Mutex
		for w := 0; w < 8; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < 3; i++ {
					off := int64(splitmix(uint64(w*100+i)) % uint64(prim.g.size-16<<20))
					n, err := srv.drain(prim.key, fmt.Sprintf("bytes=%d-%d", off, off+16<<20-1))
					mu.Lock()
					moved += n
					if err != nil || n != 16<<20 {
						r.Exact = false
					}
					mu.Unlock()
				}
			}(w)
		}
		wg.Wait()
		r.ConcMiBps = float64(moved) / (1 << 20) / time.Since(t0).Seconds()
		_, hwm2 := srv.rssKiB()
		r.RSSConcDelta = float64(hwm2-rssBefore) / 1024
	}
	srv.stop()

	// Syscall counts for one full GET of the primary object (strace).
	if useStrace {
		lg := filepath.Join(tmp, "strace-"+name+".log")
		ssrv := startServer(bin, store, lg)
		resp, _ := http.Get("http://" + ssrv.addr + "/marker-probe")
		if resp != nil {
			resp.Body.Close()
		}
		// One full GET of the primary object; for a many-small-objects
		// fixture (no primary) one pass over every object.
		targets := []*fixtureObject{prim}
		if f.primary < 0 {
			targets = f.objs
		}
		for _, o := range targets {
			if _, err := ssrv.drain(o.key, ""); err != nil {
				r.Exact = false
			}
		}
		ssrv.stop()
		b, _ := os.ReadFile(lg)
		seen := false
		for _, ln := range strings.Split(string(b), "\n") {
			if !seen {
				seen = strings.Contains(ln, "GET /marker-probe")
				continue
			}
			switch {
			case strings.Contains(ln, "openat(") && strings.Contains(ln, `.pack"`) && !strings.Contains(ln, "= -1"):
				r.PackOpens++
			case strings.Contains(ln, "pread64("):
				r.PreadCalls++
				if i := strings.LastIndex(ln, "= "); i >= 0 {
					n, _ := strconv.ParseInt(strings.TrimSpace(ln[i+2:]), 10, 64)
					r.PreadMiB += float64(n) / (1 << 20)
				}
			}
		}
		if !seen {
			fmt.Println("WARN: strace marker not found; counts unavailable")
			r.PackOpens, r.PreadCalls = -1, -1
		}
	}
	return r
}

// ---------------------------------------------------------------------------

func cliOut(bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	var b bytes.Buffer
	cmd.Stdout, cmd.Stderr = &b, &b
	err := cmd.Run()
	return b.String(), err
}

func supportsLayout(bin string) bool {
	out, _ := cliOut(bin, "compact", "-h")
	return strings.Contains(out, "-layout")
}

func copyDir(src, dst string) {
	if out, err := exec.Command("cp", "-a", src, dst).CombinedOutput(); err != nil {
		fmt.Println("FAIL: cp:", err, string(out))
		os.Exit(2)
	}
}

func main() {
	sizeMiB := flag.Int64("size-mib", 256, "seq object size / ckpt total (MiB)")
	packMiB := flag.Int64("pack-size-mib", 64, "compact -pack-size-mib")
	compression := flag.String("compression", "", "override compact -compression (default: off for seq/ckpt, auto for site)")
	fixtures := flag.String("fixtures", "seq,ckpt,site", "comma-separated fixtures")
	baseline := flag.String("baseline-bin", os.Getenv("BASELINE_BIN"), "pre-Z2-13 binary (old reader, digest layout)")
	useStrace := flag.Bool("strace", true, "count pack opens / pread64 under strace when available")
	jsonOut := flag.String("json", "", "write all results as JSON here")
	flag.Parse()
	cur := os.Getenv("ZEROS3_BIN")
	if cur == "" {
		fmt.Println("FAIL: ZEROS3_BIN is required")
		os.Exit(2)
	}
	if _, err := exec.LookPath("strace"); err != nil {
		*useStrace = false
	}
	work, err := os.MkdirTemp("", "zeros3-z2-locality-")
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(work)

	type row struct {
		Fixture  string
		Layout   map[string]localityMetrics
		Configs  []configResult
		CompactS map[string]float64
	}
	var all []row
	for _, name := range strings.Split(*fixtures, ",") {
		f := buildFixture(strings.TrimSpace(name), *sizeMiB)
		if *compression != "" {
			f.compress = *compression
		}
		loose := filepath.Join(work, f.name, "loose")
		_ = os.MkdirAll(filepath.Dir(loose), 0o755)
		srv := startServer(cur, loose, "")
		_, err := srv.cli.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
		check("CreateBucket", err == nil, "%v", err)
		t0 := time.Now()
		for _, o := range f.objs {
			g := &gen{size: o.g.size, at: o.g.at}
			out, err := srv.cli.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(o.key), Body: g, ContentLength: aws.Int64(g.size)})
			if err != nil {
				check("PutObject "+o.key, false, "%v", err)
				os.Exit(1)
			}
			o.etag = aws.ToString(out.ETag)
		}
		srv.stop()
		fmt.Printf("RECORD fixture=%s objects=%d ingest_ms=%d\n", f.name, len(f.objs), time.Since(t0).Milliseconds())

		r := row{Fixture: f.name, Layout: map[string]localityMetrics{}, CompactS: map[string]float64{}}
		dStore := filepath.Join(work, f.name, "digest")
		copyDir(loose, dStore)
		compactArgs := func(store string, layout string) []string {
			a := []string{"compact", "-store", store, "-pack-size-mib", fmt.Sprint(*packMiB), "-compression", f.compress, "-json"}
			if layout != "" {
				a = append(a, "-layout", layout)
			}
			return a
		}
		curHasLayout := supportsLayout(cur)
		dLayout := ""
		if curHasLayout {
			dLayout = "digest"
		}
		t1 := time.Now()
		out, err := cliOut(cur, compactArgs(dStore, dLayout)...)
		check("compact digest", err == nil, "%v %s", err, out)
		r.CompactS["digest"] = time.Since(t1).Seconds()
		lStore := ""
		if curHasLayout {
			lStore = filepath.Join(work, f.name, "locality")
			copyDir(loose, lStore)
			t2 := time.Now()
			out, err = cliOut(cur, compactArgs(lStore, "locality")...)
			check("compact locality", err == nil, "%v %s", err, out)
			r.CompactS["locality"] = time.Since(t2).Seconds()
		}
		os.RemoveAll(loose)

		if m, err := measureLocality(dStore, f); err != nil {
			check("locality metrics (digest)", false, "%v", err)
		} else {
			r.Layout["digest"] = m
		}
		if lStore != "" {
			if m, err := measureLocality(lStore, f); err != nil {
				check("locality metrics (locality)", false, "%v", err)
			} else {
				r.Layout["locality"] = m
			}
		}
		for _, lay := range []string{"digest", "locality"} {
			if m, ok := r.Layout[lay]; ok {
				fmt.Printf("RECORD locality fixture=%s layout=%s packs=%d chunks=%d samepack_pct=%.1f contiguous_pct=%.1f runs=%d avg_run=%.1f p50_run=%d p95_run=%d transitions=%d transitions_per_gib=%.0f\n",
					f.name, lay, m.Packs, m.Chunks, m.SamePackPct, m.ContiguousPct, m.Runs, m.AvgRun, m.P50Run, m.P95Run, m.TotalPackTransitns, m.TransitionsPerGiB)
			}
		}

		type cfg struct{ name, bin, store string }
		var cfgs []cfg
		if *baseline != "" {
			cfgs = append(cfgs, cfg{"A baseline digest+old-reader", *baseline, dStore})
		}
		if *baseline == "" || *baseline != cur {
			cfgs = append(cfgs, cfg{"B digest+current-reader", cur, dStore})
		}
		if lStore != "" {
			cfgs = append(cfgs, cfg{"C locality+current-reader", cur, lStore})
		}
		for _, c := range cfgs {
			res := measureConfig(strings.Fields(c.name)[0], c.bin, c.store, f, *useStrace, work)
			res.Name = c.name
			check(c.name+": every read byte-exact ("+f.name+")", res.Exact, "mismatch")
			fmt.Printf("RECORD config fixture=%s name=%q full_mibps=%.0f rng1_ms_mean=%.2f p50=%.2f p95=%.2f rng16_mibps=%.0f rng64_mibps=%.0f small64k_ms=%.2f conc8x16_mibps=%.0f rss_base_mib=%.0f rss_full_delta_mib=%.0f rss_conc_delta_mib=%.0f pack_opens=%d pread64=%d pread_mib=%.0f\n",
				f.name, c.name, res.FullMiBps, res.Rng1MsMean, res.Rng1MsP50, res.Rng1MsP95, res.Rng16MiBps, res.Rng64MiBps, res.Small64KMsMean,
				res.ConcMiBps, res.RSSBaseMiB, res.RSSFullDelta, res.RSSConcDelta, res.PackOpens, res.PreadCalls, res.PreadMiB)
			r.Configs = append(r.Configs, res)
		}
		// Structural and no-regression gates, with margins wide enough that
		// timing noise cannot trip them (the RECORD lines carry the numbers).
		if len(r.Configs) >= 2 && f.name != "site" {
			a, c := r.Configs[0], r.Configs[len(r.Configs)-1]
			if lm, ok := r.Layout["locality"]; ok {
				check(f.name+": locality layout makes >=90% of adjacent chunk pairs physically contiguous", lm.ContiguousPct >= 90, "%.1f%%", lm.ContiguousPct)
				check(f.name+": locality layout cuts pack transitions >=20x", lm.TotalPackTransitns*20 <= r.Layout["digest"].TotalPackTransitns, "%d vs %d", lm.TotalPackTransitns, r.Layout["digest"].TotalPackTransitns)
			}
			if *useStrace && a.PackOpens > 0 && c.PackOpens >= 0 && c.Name != a.Name {
				check(f.name+": one full GET opens >=8x fewer packs than baseline", c.PackOpens*8 <= a.PackOpens, "%d vs %d", c.PackOpens, a.PackOpens)
				check(f.name+": one full GET issues >=8x fewer pread64 than baseline", c.PreadCalls*8 <= a.PreadCalls, "%d vs %d", c.PreadCalls, a.PreadCalls)
			}
			if c.Name != a.Name {
				check(f.name+": full GET throughput does not regress >10%", c.FullMiBps >= 0.9*a.FullMiBps, "%.0f vs %.0f MiB/s", c.FullMiBps, a.FullMiBps)
				check(f.name+": 1 MiB range latency within 25% of baseline", c.Rng1MsMean <= 1.25*a.Rng1MsMean, "%.2f vs %.2f ms", c.Rng1MsMean, a.Rng1MsMean)
				check(f.name+": small random reads within 25% of baseline", c.Small64KMsMean <= 1.25*a.Small64KMsMean, "%.2f vs %.2f ms", c.Small64KMsMean, a.Small64KMsMean)
			}
			check(f.name+": full GETs add <=16 MiB RSS", c.RSSFullDelta <= 16, "%.1f MiB", c.RSSFullDelta)
			check(f.name+": 8 concurrent range readers add <=8x12 MiB RSS", c.RSSConcDelta <= 96, "%.1f MiB", c.RSSConcDelta)
		}
		fmt.Printf("RECORD compact_seconds fixture=%s %v\n", f.name, r.CompactS)
		all = append(all, r)
		os.RemoveAll(filepath.Join(work, f.name))
	}
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(all, "", "  ")
		_ = os.WriteFile(*jsonOut, b, 0o644)
	}
	if failed {
		os.Exit(1)
	}
}
