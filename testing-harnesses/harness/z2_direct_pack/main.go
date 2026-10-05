// z2_direct_pack measures ZeroS3 Z2-15 stream-native direct pack ingest
// through real zeros3 binaries (CLI + HTTP + AWS SDK) only. For each build
// it ingests objects into a fresh store and records the write path
// (PutObject wall time, MiB/s, server peak RSS, loose-file / pack counts,
// physical bytes, peak staging-directory use), the immediate read path
// (full GET, 16 MiB range, pack opens / pread64 under strace, structural
// locality), and -- for a build whose online path leaves loose chunks --
// the cost of reaching the packed state with `compact -layout locality`.
// Scenarios: unique (single large pseudo-random object), compress
// (compressible text, raw vs adaptive direct packs), ckpt (checkpoint v1
// then a localized v2), multipart, small (small objects stay loose).
//
// Usage:
//
//	go run ./harness/z2_direct_pack -bin NEW [-baseline-bin OLD] [-size-mib 256] \
//	    [-scenarios unique,compress,ckpt,multipart,small] [-compact-baseline]
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	accessKeyID     = "AKIAZEROS3EXAMPLE01"
	secretAccessKey = "zeros3exampleSecretKeyForM1TestingOnly01"
	region          = "us-east-1"
	bucket          = "dp"
)

var failed bool

// results holds the numbers the gates compare: results[label][metric].
var results = map[string]map[string]float64{}

func rec(label, key string, v float64) {
	if results[label] == nil {
		results[label] = map[string]float64{}
	}
	results[label][key] = v
}

// serverEnv is added to every server process (benchmark-only ZEROS3_* overrides).
var serverEnv []string

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
	cmd.Env = append(os.Environ(), serverEnv...)
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

// ---------------------------------------------------------------------------
// Physical store statistics.

type storeStats struct {
	Loose       int
	LooseBytes  int64
	Packs       int
	PackBytes   int64
	PackRecords int64
	TotalBytes  int64 // every file except staging
	Tmp         int
}

func statStore(dir string) storeStats {
	var st storeStats
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case strings.HasPrefix(rel, "tmp"+string(filepath.Separator)):
			st.Tmp++
			return nil
		case strings.HasPrefix(rel, "chunks"+string(filepath.Separator)):
			st.Loose++
			st.LooseBytes += info.Size()
		case strings.HasSuffix(p, ".pack"):
			st.Packs++
			st.PackBytes += info.Size()
			if f, err := os.Open(p); err == nil {
				var foot [64]byte
				if _, err := f.ReadAt(foot[:], info.Size()-64); err == nil && string(foot[:4]) == "ZSPF" {
					st.PackRecords += int64(binary.LittleEndian.Uint64(foot[8:16]))
				}
				f.Close()
			}
		}
		st.TotalBytes += info.Size()
		return nil
	})
	return st
}

// tmpSampler tracks the peak number of files and bytes in the store's
// staging directory while a request runs.
type tmpSampler struct {
	stop  chan struct{}
	done  chan struct{}
	Files int
	Bytes int64
}

func startTmpSampler(dir string) *tmpSampler {
	t := &tmpSampler{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(t.done)
		for {
			var files int
			var b int64
			ents, _ := os.ReadDir(filepath.Join(dir, "tmp"))
			for _, e := range ents {
				if info, err := e.Info(); err == nil && !e.IsDir() {
					files++
					b += info.Size()
				}
			}
			t.Files, t.Bytes = max(t.Files, files), max(t.Bytes, b)
			select {
			case <-t.stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	return t
}

func (t *tmpSampler) finish() { close(t.stop); <-t.done }

func statLine(tag string, st storeStats) {
	fmt.Printf("RECORD %s loose_files=%d loose_mib=%.1f packs=%d pack_mib=%.1f pack_records=%d physical_mib=%.1f tmp_left=%d\n",
		tag, st.Loose, float64(st.LooseBytes)/(1<<20), st.Packs, float64(st.PackBytes)/(1<<20), st.PackRecords, float64(st.TotalBytes)/(1<<20), st.Tmp)
}

// ---------------------------------------------------------------------------
// Operations.

func fatalf(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(2)
}

func genSHA(g *gen) string {
	h := sha256.New()
	_, _ = io.Copy(h, &gen{size: g.size, at: g.at})
	return hex.EncodeToString(h.Sum(nil))
}

func (s *server) put(key string, g *gen) (time.Duration, string) {
	body := &gen{size: g.size, at: g.at}
	t0 := time.Now()
	out, err := s.cli.PutObject(context.Background(), &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: body, ContentLength: aws.Int64(g.size)})
	if err != nil {
		fatalf("PutObject %s: %v", key, err)
	}
	return time.Since(t0), aws.ToString(out.ETag)
}

func (s *server) shaOf(key, rng string) string {
	in := &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}
	if rng != "" {
		in.Range = aws.String(rng)
	}
	out, err := s.cli.GetObject(context.Background(), in)
	if err != nil {
		fatalf("GetObject %s: %v", key, err)
	}
	defer out.Body.Close()
	h := sha256.New()
	_, _ = io.Copy(h, out.Body)
	return hex.EncodeToString(h.Sum(nil))
}

type readResult struct {
	FirstMiBps, FullMiBps float64
	Rng16MiBps, Rng16Ms   float64
	Exact                 bool
}

// readProbe times the immediate full GET (the first one the store serves
// after the write), two warm repeats and 16 MiB ranges, then checks byte
// exactness with an untimed hashed GET and hashed range.
func readProbe(srv *server, key string, g *gen) readResult {
	r := readResult{Exact: true}
	mib := float64(g.size) / (1 << 20)
	t0 := time.Now()
	if n, err := srv.drain(key, ""); err != nil || n != g.size {
		r.Exact = false
	}
	r.FirstMiBps = mib / time.Since(t0).Seconds()
	var rates []float64
	for i := 0; i < 2; i++ {
		t0 := time.Now()
		_, _ = srv.drain(key, "")
		rates = append(rates, mib/time.Since(t0).Seconds())
	}
	r.FullMiBps = pct(rates, 0.5)
	var lat []float64
	for i := 0; i < 6 && g.size > 32<<20; i++ {
		off := int64(splitmix(uint64(100+i)) % uint64(g.size-16<<20))
		t0 := time.Now()
		n, err := srv.drain(key, fmt.Sprintf("bytes=%d-%d", off, off+16<<20-1))
		lat = append(lat, ms(time.Since(t0)))
		if err != nil || n != 16<<20 {
			r.Exact = false
		}
	}
	if len(lat) > 0 {
		r.Rng16Ms = mean(lat)
		r.Rng16MiBps = 16 / (r.Rng16Ms / 1000)
		off := int64(splitmix(777) % uint64(g.size-16<<20))
		sh := sha256.Sum256(g.window(off, 16<<20))
		if srv.shaOf(key, fmt.Sprintf("bytes=%d-%d", off, off+16<<20-1)) != hex.EncodeToString(sh[:]) {
			r.Exact = false
		}
	}
	if srv.shaOf(key, "") != genSHA(g) {
		r.Exact = false
	}
	return r
}

// straceFullGet counts pack opens and pread64 calls of one full GET.
func straceFullGet(bin, store, key, work string) (opens, preads int) {
	if _, err := exec.LookPath("strace"); err != nil {
		return -1, -1
	}
	lg := filepath.Join(work, "strace.log")
	srv := startServer(bin, store, lg)
	resp, _ := http.Get("http://" + srv.addr + "/marker-probe")
	if resp != nil {
		resp.Body.Close()
	}
	_, _ = srv.drain(key, "")
	srv.stop()
	b, _ := os.ReadFile(lg)
	seen := false
	for _, ln := range strings.Split(string(b), "\n") {
		if !seen {
			seen = strings.Contains(ln, "GET /marker-probe")
			continue
		}
		switch {
		case strings.Contains(ln, "openat(") && strings.Contains(ln, `.pack"`) && !strings.Contains(ln, "= -1"):
			opens++
		case strings.Contains(ln, "pread64("):
			preads++
		}
	}
	if !seen {
		return -1, -1
	}
	return
}

func deepVerify(bin, store string) {
	if out, err := cliOut(bin, "verify", "-deep", "-store", store); err != nil {
		check("deep verify "+filepath.Base(store), false, "%v\n%s", err, out)
	}
}

type build struct {
	label string
	bin   string
	env   []string
}

func newStore(work, name string) string {
	dir := filepath.Join(work, name)
	_ = os.RemoveAll(dir)
	return dir
}

func startBucket(b build, dir string) *server {
	serverEnv = b.env
	srv := startServer(b.bin, dir, "")
	if _, err := srv.cli.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		fatalf("CreateBucket: %v", err)
	}
	return srv
}

// compactState runs `compact -layout locality` offline, returning wall
// seconds (0 when the build has no locality layout).
func compactState(b build, dir string) float64 {
	args := []string{"compact", "-store", dir, "-compression", "off", "-json"}
	if supportsLayout(b.bin) {
		args = append(args, "-layout", "locality")
	}
	t0 := time.Now()
	out, err := cliOut(b.bin, args...)
	if err != nil {
		fatalf("compact: %v\n%s", err, out)
	}
	return time.Since(t0).Seconds()
}

// ---------------------------------------------------------------------------
// Scenarios.

// unique: one pseudo-random object. Reports upload, immediate read, and
// time to the locality-packed state (put + compact).
func scenarioUnique(b build, sizeMiB int64, work string, doCompact bool) {
	g := &gen{size: sizeMiB << 20, at: randomAt(1)}
	dir := newStore(work, "unique-"+b.label)
	srv := startBucket(b, dir)
	_, rss0 := srv.rssKiB()
	samp := startTmpSampler(dir)
	d, _ := srv.put("big", g)
	samp.finish()
	_, hwm := srv.rssKiB()
	st := statStore(dir)
	rec(b.label, "put_mibps", float64(sizeMiB)/d.Seconds())
	rec(b.label, "rss_delta_mib", float64(hwm-rss0)/1024)
	rec(b.label, "loose_files", float64(st.Loose))
	rec(b.label, "packs", float64(st.Packs))
	fmt.Printf("RECORD unique build=%s put_s=%.2f put_mibps=%.1f rss_hwm_mib=%.0f rss_delta_mib=%.0f tmp_peak_files=%d tmp_peak_mib=%.1f\n",
		b.label, d.Seconds(), float64(sizeMiB)/d.Seconds(), float64(hwm)/1024, float64(hwm-rss0)/1024, samp.Files, float64(samp.Bytes)/(1<<20))
	statLine("unique-after-put build="+b.label, st)
	rd := readProbe(srv, "big", g)
	fmt.Printf("RECORD unique-read build=%s state=immediate first_get_mibps=%.0f warm_get_mibps=%.0f range16_mibps=%.0f range16_ms=%.1f exact=%v\n",
		b.label, rd.FirstMiBps, rd.FullMiBps, rd.Rng16MiBps, rd.Rng16Ms, rd.Exact)
	check(b.label+": immediate reads byte-exact", rd.Exact, "mismatch")
	rec(b.label, "first_get_mibps", rd.FirstMiBps)
	rec(b.label, "range16_mibps", rd.Rng16MiBps)
	srv.stop()
	opens, preads := straceFullGet(b.bin, dir, "big", work)
	fmt.Printf("RECORD unique-syscalls build=%s state=immediate pack_opens=%d pread64=%d\n", b.label, opens, preads)
	if !doCompact {
		deepVerify(b.bin, dir)
		return
	}
	cs := compactState(b, dir)
	st2 := statStore(dir)
	statLine("unique-after-compact build="+b.label, st2)
	rec(b.label, "time_to_packed_s", d.Seconds()+cs)
	fmt.Printf("RECORD unique-ready build=%s put_s=%.2f compact_s=%.2f time_to_packed_s=%.2f\n", b.label, d.Seconds(), cs, d.Seconds()+cs)
	srv = startBucket2(b, dir)
	rd2 := readProbe(srv, "big", g)
	fmt.Printf("RECORD unique-read build=%s state=compacted first_get_mibps=%.0f warm_get_mibps=%.0f range16_mibps=%.0f range16_ms=%.1f exact=%v\n",
		b.label, rd2.FirstMiBps, rd2.FullMiBps, rd2.Rng16MiBps, rd2.Rng16Ms, rd2.Exact)
	check(b.label+": compacted reads byte-exact", rd2.Exact, "mismatch")
	srv.stop()
	opens, preads = straceFullGet(b.bin, dir, "big", work)
	fmt.Printf("RECORD unique-syscalls build=%s state=compacted pack_opens=%d pread64=%d\n", b.label, opens, preads)
	deepVerify(b.bin, dir)
	_ = os.RemoveAll(dir)
}

// startBucket2 reopens an existing store.
func startBucket2(b build, dir string) *server {
	serverEnv = b.env
	return startServer(b.bin, dir, "")
}

// scenarioCompress: compressible text, one build config per env.
func scenarioCompress(b build, sizeMiB int64, work string) {
	g := &gen{size: sizeMiB << 20, at: textAt(7)}
	dir := newStore(work, "compress-"+b.label)
	srv := startBucket(b, dir)
	_, rss0 := srv.rssKiB()
	d, _ := srv.put("text", g)
	_, hwm := srv.rssKiB()
	st := statStore(dir)
	rd := readProbe(srv, "text", g)
	fmt.Printf("RECORD compress build=%s put_s=%.2f put_mibps=%.1f rss_delta_mib=%.0f stored_mib=%.1f logical_mib=%d saved_pct=%.1f first_get_mibps=%.0f warm_get_mibps=%.0f loose_files=%d packs=%d exact=%v\n",
		b.label, d.Seconds(), float64(sizeMiB)/d.Seconds(), float64(hwm-rss0)/1024, float64(st.PackBytes+st.LooseBytes)/(1<<20), sizeMiB,
		100*(1-float64(st.PackBytes+st.LooseBytes)/float64(sizeMiB<<20)), rd.FirstMiBps, rd.FullMiBps, st.Loose, st.Packs, rd.Exact)
	check(b.label+": compressible reads byte-exact", rd.Exact, "mismatch")
	rec(b.label, "c_put_mibps", float64(sizeMiB)/d.Seconds())
	rec(b.label, "c_saved_pct", 100*(1-float64(st.PackBytes+st.LooseBytes)/float64(sizeMiB<<20)))
	rec(b.label, "c_warm_get_mibps", rd.FullMiBps)
	srv.stop()
	deepVerify(b.bin, dir)
	_ = os.RemoveAll(dir)
}

// scenarioCkpt: checkpoint v1 then a localized v2 (~1/8 of 1 MiB regions rewritten).
func scenarioCkpt(b build, sizeMiB int64, work string) {
	g1 := &gen{size: sizeMiB << 20, at: ckptAt(1)}
	g2 := &gen{size: sizeMiB << 20, at: ckptAt(2)}
	dir := newStore(work, "ckpt-"+b.label)
	srv := startBucket(b, dir)
	d1, _ := srv.put("model-v1.bin", g1)
	s1 := statStore(dir)
	statLine("ckpt-v1 build="+b.label, s1)
	rd := readProbe(srv, "model-v1.bin", g1)
	fmt.Printf("RECORD ckpt-v1 build=%s put_s=%.2f put_mibps=%.1f first_get_mibps=%.0f range16_ms=%.1f exact=%v\n", b.label, d1.Seconds(), float64(sizeMiB)/d1.Seconds(), rd.FirstMiBps, rd.Rng16Ms, rd.Exact)
	d2, _ := srv.put("model-v2.bin", g2)
	s2 := statStore(dir)
	statLine("ckpt-v2 build="+b.label, s2)
	fmt.Printf("RECORD ckpt-v2 build=%s logical_mib=%d put_s=%.2f new_loose_files=%d new_loose_mib=%.1f new_packs=%d new_pack_records=%d new_pack_mib=%.1f new_physical_mib=%.1f\n",
		b.label, sizeMiB, d2.Seconds(), s2.Loose-s1.Loose, float64(s2.LooseBytes-s1.LooseBytes)/(1<<20), s2.Packs-s1.Packs, s2.PackRecords-s1.PackRecords,
		float64(s2.PackBytes-s1.PackBytes)/(1<<20), float64(s2.TotalBytes-s1.TotalBytes)/(1<<20))
	rd2 := readProbe(srv, "model-v2.bin", g2)
	check(b.label+": ckpt v1 exact", rd.Exact, "mismatch")
	check(b.label+": ckpt v2 exact", rd2.Exact, "mismatch")
	// Identical re-upload must store no new payload.
	s3 := statStore(dir)
	_, _ = srv.put("model-v2-dup.bin", g2)
	s4 := statStore(dir)
	rec(b.label, "ckpt_v2_new_physical_mib", float64(s2.TotalBytes-s1.TotalBytes)/(1<<20))
	rec(b.label, "ckpt_dup_new_records", float64(s4.Loose-s3.Loose+int(s4.PackRecords-s3.PackRecords)))
	fmt.Printf("RECORD ckpt-dup build=%s new_loose_files=%d new_packs=%d new_pack_records=%d\n", b.label, s4.Loose-s3.Loose, s4.Packs-s3.Packs, s4.PackRecords-s3.PackRecords)
	srv.stop()
	deepVerify(b.bin, dir)
	_ = os.RemoveAll(dir)
}

func scenarioMultipart(b build, sizeMiB int64, work string) {
	const part = 64 << 20
	dir := newStore(work, "mp-"+b.label)
	srv := startBucket(b, dir)
	ctx := context.Background()
	size := sizeMiB << 20
	cr, err := srv.cli.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("mp")})
	if err != nil {
		fatalf("CreateMultipartUpload: %v", err)
	}
	var parts []types.CompletedPart
	t0 := time.Now()
	for n, off := int32(1), int64(0); off < size; n, off = n+1, off+part {
		pg := &gen{size: min(part, size-off), at: func(p []byte, o int64) { randomAt(9)(p, off+o) }}
		up, err := srv.cli.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String("mp"), UploadId: cr.UploadId, PartNumber: aws.Int32(n), Body: pg, ContentLength: aws.Int64(pg.size)})
		if err != nil {
			fatalf("UploadPart: %v", err)
		}
		parts = append(parts, types.CompletedPart{ETag: up.ETag, PartNumber: aws.Int32(n)})
	}
	partsS := time.Since(t0).Seconds()
	sParts := statStore(dir)
	t1 := time.Now()
	if _, err := srv.cli.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("mp"), UploadId: cr.UploadId, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}}); err != nil {
		fatalf("CompleteMultipartUpload: %v", err)
	}
	completeS := time.Since(t1).Seconds()
	sDone := statStore(dir)
	statLine("mp-parts build="+b.label, sParts)
	statLine("mp-complete build="+b.label, sDone)
	fmt.Printf("RECORD multipart build=%s parts_s=%.2f complete_s=%.2f total_s=%.2f total_mibps=%.1f new_packs=%d new_pack_records=%d new_loose_files=%d\n",
		b.label, partsS, completeS, partsS+completeS, float64(sizeMiB)/(partsS+completeS), sDone.Packs-sParts.Packs, sDone.PackRecords-sParts.PackRecords, sDone.Loose-sParts.Loose)
	rec(b.label, "mp_total_s", partsS+completeS)
	rec(b.label, "mp_loose_files", float64(sDone.Loose))
	mg := &gen{size: size, at: randomAt(9)}
	rd := readProbe(srv, "mp", mg)
	check(b.label+": multipart object exact", rd.Exact, "mismatch")
	srv.stop()
	deepVerify(b.bin, dir)
	_ = os.RemoveAll(dir)
}

// scenarioConcurrent: 8 simultaneous distinct large PutObjects.
func scenarioConcurrent(b build, work string) {
	const n, mib = 8, 96
	dir := newStore(work, "conc-"+b.label)
	srv := startBucket(b, dir)
	_, rss0 := srv.rssKiB()
	t0 := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.put(fmt.Sprintf("c%d", i), &gen{size: mib << 20, at: randomAt(uint64(300 + i))})
		}()
	}
	wg.Wait()
	d := time.Since(t0)
	_, hwm := srv.rssKiB()
	st := statStore(dir)
	rec(b.label, "conc_rss_delta_mib", float64(hwm-rss0)/1024)
	fmt.Printf("RECORD concurrent build=%s puts=%d size_mib=%d wall_s=%.2f agg_mibps=%.1f rss_hwm_mib=%.0f rss_delta_mib=%.0f\n", b.label, n, mib, d.Seconds(), float64(n*mib)/d.Seconds(), float64(hwm)/1024, float64(hwm-rss0)/1024)
	statLine("concurrent build="+b.label, st)
	for i := 0; i < n; i++ {
		g := &gen{size: mib << 20, at: randomAt(uint64(300 + i))}
		if srv.shaOf(fmt.Sprintf("c%d", i), "") != genSHA(g) {
			check(b.label+": concurrent object exact", false, "c%d differs", i)
		}
	}
	srv.stop()
	deepVerify(b.bin, dir)
	_ = os.RemoveAll(dir)
}

func scenarioSmall(b build, work string) {
	dir := newStore(work, "small-"+b.label)
	srv := startBucket(b, dir)
	for _, mib := range []int64{1, 4, 16} {
		var lat []float64
		for i := 0; i < 5; i++ {
			g := &gen{size: mib << 20, at: randomAt(uint64(1000 + int(mib)*10 + i))}
			d, _ := srv.put(fmt.Sprintf("s%d-%d", mib, i), g)
			lat = append(lat, d.Seconds())
		}
		rec(b.label, fmt.Sprintf("small_%d_ms", mib), 1000*pct(lat, 0.5))
		fmt.Printf("RECORD small build=%s size_mib=%d put_ms_p50=%.1f\n", b.label, mib, 1000*pct(lat, 0.5))
	}
	srv.stop()
	st := statStore(dir)
	statLine("small build="+b.label, st)
	check(b.label+": small objects stay loose (no packs)", st.Packs == 0, "%d packs", st.Packs)
	deepVerify(b.bin, dir)
	_ = os.RemoveAll(dir)
}

func main() {
	bin := flag.String("bin", os.Getenv("ZEROS3_BIN"), "build under test")
	base := flag.String("baseline-bin", "", "baseline build (3df362b)")
	sizeMiB := flag.Int64("size-mib", 256, "object size for unique/ckpt/multipart")
	compMiB := flag.Int64("compress-mib", 192, "compressible object size")
	scen := flag.String("scenarios", "unique,compress,ckpt,multipart,small", "comma-separated scenarios")
	compact := flag.Bool("compact", true, "unique: also run compact -layout locality and re-measure")
	only := flag.String("only", "", "run only this build label (baseline|new|new-raw|new-adaptive)")
	newEnv := flag.String("new-env", "", "comma-separated K=V added to the build under test (label new)")
	workDir := flag.String("work", "", "work directory (default: temp dir)")
	flag.Parse()
	if *bin == "" && *base == "" {
		fatalf("-bin or -baseline-bin is required")
	}
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "AWS_") {
			os.Unsetenv(k)
		}
	}
	work := *workDir
	if work == "" {
		w, err := os.MkdirTemp("", "zeros3-z2-directpack-")
		if err != nil {
			fatalf("%v", err)
		}
		work = w
		defer os.RemoveAll(work)
	}
	_ = os.MkdirAll(work, 0o755)
	var builds, codecBuilds []build
	if *base != "" {
		builds = append(builds, build{label: "baseline", bin: *base})
	}
	if *bin != "" {
		nb := build{label: "new", bin: *bin}
		if *newEnv != "" {
			nb.env = strings.Split(*newEnv, ",")
		}
		builds = append(builds, nb)
		codecBuilds = []build{
			{label: "new", bin: *bin},
			{label: "new-raw", bin: *bin, env: []string{"ZEROS3_DIRECT_PACK_COMPRESS=off"}},
			{label: "new-adaptive", bin: *bin, env: []string{"ZEROS3_DIRECT_PACK_COMPRESS=auto"}},
		}
	}
	if *base != "" && *bin == "" {
		codecBuilds = []build{{label: "baseline", bin: *base}}
	}
	sel := func(b build) bool { return *only == "" || *only == b.label }
	for _, sc := range strings.Split(*scen, ",") {
		switch strings.TrimSpace(sc) {
		case "unique":
			for _, b := range builds {
				if sel(b) {
					scenarioUnique(b, *sizeMiB, work, *compact)
				}
			}
		case "compress":
			for _, b := range codecBuilds {
				if sel(b) {
					scenarioCompress(b, *compMiB, work)
				}
			}
		case "ckpt":
			for _, b := range builds {
				if sel(b) {
					scenarioCkpt(b, *sizeMiB, work)
				}
			}
		case "multipart":
			for _, b := range builds {
				if sel(b) {
					scenarioMultipart(b, *sizeMiB, work)
				}
			}
		case "concurrent":
			for _, b := range builds {
				if sel(b) {
					scenarioConcurrent(b, work)
				}
			}
		case "small":
			for _, b := range builds {
				if sel(b) {
					scenarioSmall(b, work)
				}
			}
		default:
			fatalf("unknown scenario %q", sc)
		}
	}
	gates(*sizeMiB)
	if failed {
		os.Exit(1)
	}
}

// gates compares the build under test with the baseline wherever both ran.
func gates(sizeMiB int64) {
	b, n := results["baseline"], results["new"]
	has := func(m map[string]float64, k string) bool { _, ok := m[k]; return ok }
	if has(n, "packs") {
		check("direct ingest leaves at most a small loose tail", n["loose_files"] <= 64, "%.0f loose files", n["loose_files"])
		check("direct ingest writes about one pack per 64 MiB", n["packs"] >= 1 && n["packs"] <= float64(sizeMiB/64+2), "%.0f packs", n["packs"])
		check("PutObject RSS grows <=24 MiB", n["rss_delta_mib"] <= 24, "%.0f MiB", n["rss_delta_mib"])
	}
	if has(n, "packs") && has(b, "packs") {
		check("PutObject throughput does not regress >10%", n["put_mibps"] >= 0.9*b["put_mibps"], "%.1f vs %.1f MiB/s", n["put_mibps"], b["put_mibps"])
		check("one PutObject beats PUT+compact by >=25%", has(n, "time_to_packed_s") && n["time_to_packed_s"] <= 0.75*b["time_to_packed_s"], "%.2fs vs %.2fs", n["time_to_packed_s"], b["time_to_packed_s"])
		check("immediate full GET >=1.2x baseline", n["first_get_mibps"] >= 1.2*b["first_get_mibps"], "%.0f vs %.0f MiB/s", n["first_get_mibps"], b["first_get_mibps"])
		check("immediate 16 MiB range not slower", n["range16_mibps"] >= 0.95*b["range16_mibps"], "%.0f vs %.0f MiB/s", n["range16_mibps"], b["range16_mibps"])
	}
	if has(n, "conc_rss_delta_mib") {
		check("8 concurrent large PutObjects add <=192 MiB RSS", n["conc_rss_delta_mib"] <= 192, "%.0f MiB", n["conc_rss_delta_mib"])
	}
	if has(n, "ckpt_v2_new_physical_mib") {
		check("localized checkpoint overwrite stores <=20% new bytes", n["ckpt_v2_new_physical_mib"] <= 0.2*float64(sizeMiB), "%.1f MiB", n["ckpt_v2_new_physical_mib"])
		check("identical re-upload stores no payload", n["ckpt_dup_new_records"] == 0, "%.0f new records/files", n["ckpt_dup_new_records"])
	}
	if has(n, "mp_loose_files") && has(b, "mp_loose_files") {
		check("multipart lifecycle leaves far fewer loose files", n["mp_loose_files"] <= b["mp_loose_files"]/10, "%.0f vs %.0f", n["mp_loose_files"], b["mp_loose_files"])
		check("multipart lifecycle not slower", n["mp_total_s"] <= 1.1*b["mp_total_s"], "%.2f vs %.2f s", n["mp_total_s"], b["mp_total_s"])
	}
	if has(n, "small_1_ms") && has(b, "small_1_ms") {
		for _, mib := range []int{1, 4, 16} {
			k := fmt.Sprintf("small_%d_ms", mib)
			check(fmt.Sprintf("%d MiB object latency within 2x of baseline", mib), n[k] <= 2*b[k], "%.1f vs %.1f ms", n[k], b[k])
		}
	}
	// Codec policy: adaptive compression is only acceptable online if it
	// costs little write throughput; otherwise the default must stay raw.
	raw, ad, def := results["new-raw"], results["new-adaptive"], results["new"]
	if has(raw, "c_put_mibps") && has(ad, "c_put_mibps") && has(def, "c_put_mibps") {
		penalty := 1 - ad["c_put_mibps"]/raw["c_put_mibps"]
		fmt.Printf("RECORD codec-policy adaptive_write_penalty_pct=%.0f adaptive_saved_pct=%.0f raw_warm_get_mibps=%.0f adaptive_warm_get_mibps=%.0f default_saved_pct=%.1f\n",
			100*penalty, ad["c_saved_pct"], raw["c_warm_get_mibps"], ad["c_warm_get_mibps"], def["c_saved_pct"])
		check("adaptive packs save >=50% on compressible data", ad["c_saved_pct"] >= 50, "%.1f%%", ad["c_saved_pct"])
		check("default online codec follows the measured penalty", (penalty > 0.2) == (def["c_saved_pct"] < 5), "penalty %.0f%% default saved %.1f%%", 100*penalty, def["c_saved_pct"])
	}
}

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
