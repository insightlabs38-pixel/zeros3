// z2_bulk_transfer measures remote replication of one large object between
// two real zeros3 servers on loopback, behind counting proxies that add a
// fixed per-request delay, for the per-chunk v1 transport (a pre-bulk
// client binary) and the bulk v2 transport (the build under test). Servers
// are the build under test in every run, so only the client's transport
// differs.
//
// It records, per configuration: elapsed time and throughput, HTTP request
// counts by endpoint at the proxy boundary, and peak RSS of the source
// server, destination server and client. Every run's destination object
// ETag is compared with the source's, and the first run of each
// configuration is deep-verified. It fails if the bulk path does not cut
// transfer requests by -min-reduction percent, or if any bulk process
// exceeds -max-rss-mib.
//
// Usage:
//
//	go run ./harness/z2_bulk_transfer -bin ZEROS3 -baseline-bin OLD_ZEROS3 \
//	    [-size-mib 256] [-delays-ms 0,5,10] [-runs 3] [-matrix-delay-ms 10]
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
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
)

var failed bool

func fail(format string, args ...any) {
	failed = true
	fmt.Printf("FAIL: "+format+"\n", args...)
}

var servers []*exec.Cmd

func fatal(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	for _, c := range servers {
		_ = c.Process.Kill()
	}
	os.Exit(2)
}

// reader is a deterministic incompressible byte stream.
type reader struct{ pos, size int64 }

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ x>>30) * 0xbf58476d1ce4e5b9
	x = (x ^ x>>27) * 0x94d049bb133111eb
	return x ^ x>>31
}

func (r *reader) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	if rest := r.size - r.pos; int64(len(p)) > rest {
		p = p[:rest]
	}
	var w [8]byte
	for i := 0; i < len(p); {
		off := r.pos + int64(i)
		binary.LittleEndian.PutUint64(w[:], splitmix(uint64(off/8)))
		i += copy(p[i:], w[off%8:])
	}
	r.pos += int64(len(p))
	return len(p), nil
}

type counts struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *counts) add(class string) {
	c.mu.Lock()
	c.n[class]++
	c.mu.Unlock()
}

func (c *counts) snapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]int{}
	for k, v := range c.n {
		out[k] = v
	}
	return out
}

func classify(r *http.Request) string {
	switch p := r.URL.Path; {
	case strings.HasPrefix(p, "/_zeros3/v1/chunks/"):
		return "v1_chunk_" + strings.ToLower(r.Method)
	case strings.HasPrefix(p, "/_zeros3/"):
		return strings.TrimPrefix(p, "/_zeros3/")
	}
	return "s3"
}

// proxy counts every request it forwards and delays each by delay.
func proxy(target string, delay time.Duration, c *counts) (string, func()) {
	u, err := url.Parse(target)
	if err != nil {
		fatal("%v", err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.Transport = &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 128, IdleConnTimeout: 30 * time.Second}
	d := rp.Director
	rp.Director = func(r *http.Request) {
		c.add(classify(r))
		d(r)
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("%v", err)
	}
	// A request is fully received before it is forwarded, as over a real
	// network: ReverseProxy otherwise races a backend that answers as soon
	// as it has read a small body and aborts the exchange.
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
		rp.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), func() { srv.Close() }
}

func freeAddr() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("%v", err)
	}
	defer l.Close()
	return l.Addr().String()
}

func startServer(bin, dir string) (*exec.Cmd, string) {
	addr := freeAddr()
	cmd := exec.Command(bin, "-store", dir, "-addr", addr)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		fatal("start server: %v", err)
	}
	servers = append(servers, cmd)
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return cmd, addr
		} else if time.Now().After(deadline) {
			fatal("server did not start")
		}
	}
}

// peakRSS is the process's high-water resident set size, which Linux keeps
// per process in VmHWM (wait4's ru_maxrss is not reliably per child).
func peakRSS(pid int) int64 {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			kb, _ := strconv.ParseInt(strings.Fields(line)[1], 10, 64)
			return kb << 10
		}
	}
	return -1
}

// stopServer returns the server's peak RSS, read just before it exits.
func stopServer(cmd *exec.Cmd) int64 {
	rss := peakRSS(cmd.Process.Pid)
	_ = cmd.Process.Signal(os.Interrupt)
	_ = cmd.Wait()
	return rss
}

func s3Client(addr string) *s3.Client {
	cfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")))
	if err != nil {
		fatal("%v", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String("http://" + addr)
		o.UsePathStyle = true
	})
}

func etag(addr, bucket, key string) string {
	out, err := s3Client(addr).HeadObject(context.Background(), &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return "head failed: " + err.Error()
	}
	return aws.ToString(out.ETag)
}

type config1 struct {
	label    string
	bin      string
	workers  int
	targetMB int // 0: default
	delay    time.Duration
}

type result struct {
	elapsed                         time.Duration
	src, dst                        map[string]int
	srvSrcRSS, srvDstRSS, clientRSS int64
	dstLoose                        int // loose chunk files in the destination store
}

func countFiles(root string) (n int) {
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func transferReqs(c map[string]int) int {
	return c["v1_chunk_get"] + c["v1_chunk_put"] + c["v2/chunks/fetch"] + c["v2/chunks/upload"]
}

func run(c config1, srcDir, dstDir string, bothSides bool, deep bool, srcETag *string, serverBin string) result {
	_ = os.RemoveAll(dstDir)
	srcCmd, srcAddr := startServer(serverBin, srcDir)
	dstCmd, dstAddr := startServer(serverBin, dstDir)
	if err := mustBucket(dstAddr, "dst"); err != nil {
		fatal("%v", err)
	}
	if *srcETag == "" {
		*srcETag = etag(srcAddr, "src", "obj")
	}
	r := result{src: map[string]int{}, dst: map[string]int{}}
	sc, dc := &counts{n: map[string]int{}}, &counts{n: map[string]int{}}
	srcDelay := time.Duration(0)
	if bothSides {
		srcDelay = c.delay
	}
	srcURL, stopSrc := proxy("http://"+srcAddr, srcDelay, sc)
	dstURL, stopDst := proxy("http://"+dstAddr, c.delay, dc)
	cmd := exec.Command(c.bin, "replicate", "-from", srcURL, "-to", dstURL,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey,
		"-region", region, "-workers", strconv.Itoa(c.workers), "s3://src/obj", "s3://dst/obj")
	cmd.Env = os.Environ()
	if c.targetMB > 0 {
		cmd.Env = append(cmd.Env, "ZEROS3_BULK_TARGET_MIB="+strconv.Itoa(c.targetMB))
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	if err := cmd.Start(); err != nil {
		fatal("%s replicate: %v", c.label, err)
	}
	exited, sampled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(sampled)
		for t := time.NewTicker(10 * time.Millisecond); ; {
			select {
			case <-exited:
				return
			case <-t.C:
				r.clientRSS = max(r.clientRSS, peakRSS(cmd.Process.Pid))
			}
		}
	}()
	err := cmd.Wait()
	close(exited)
	<-sampled
	r.elapsed = time.Since(start)
	if err != nil {
		fatal("%s replicate: %v\n%s", c.label, err, out.String())
	}
	stopSrc()
	stopDst()
	r.src, r.dst = sc.snapshot(), dc.snapshot()
	if got := etag(dstAddr, "dst", "obj"); got != *srcETag {
		fail("%s: destination ETag %s != source %s", c.label, got, *srcETag)
	}
	r.srvSrcRSS, r.srvDstRSS = stopServer(srcCmd), stopServer(dstCmd)
	r.dstLoose = countFiles(filepath.Join(dstDir, "chunks"))
	if deep {
		v := exec.Command(serverBin, "verify", "-store", dstDir, "-deep")
		if vout, err := v.CombinedOutput(); err != nil {
			fail("%s: deep verify: %v\n%s", c.label, err, vout)
		}
	}
	return r
}

func mustBucket(addr, bucket string) error {
	_, err := s3Client(addr).CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	return err
}

func median(v []time.Duration) time.Duration {
	s := append([]time.Duration(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

func ints(s string) []int {
	var out []int
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			n, err := strconv.Atoi(f)
			if err != nil {
				fatal("bad list %q", s)
			}
			out = append(out, n)
		}
	}
	return out
}

func main() {
	bin := flag.String("bin", os.Getenv("ZEROS3_BIN"), "zeros3 binary under test (servers and bulk client)")
	baseline := flag.String("baseline-bin", "", "pre-bulk zeros3 binary used as the v1 client (optional)")
	sizeMiB := flag.Int("size-mib", 256, "payload size")
	delays := flag.String("delays-ms", "0,5,10", "per-request proxy delays")
	runs := flag.Int("runs", 3, "measured runs per configuration")
	v1Workers := flag.String("v1-workers", "8", "worker counts for the v1 client")
	v2Workers := flag.String("v2-workers", "8", "worker counts for the bulk client")
	matrixDelay := flag.Int("matrix-delay-ms", -1, "if >= 0, also run the batch-target x workers matrix at this delay")
	matrixTargets := flag.String("matrix-targets-mib", "4,8,16", "batch targets for the matrix")
	matrixWorkers := flag.String("matrix-workers", "1,2,4,8", "worker counts for the matrix")
	bothSides := flag.Bool("both-sides", false, "delay the source endpoint too")
	minReduction := flag.Float64("min-reduction", 95, "required cut in transfer requests (percent)")
	maxRSSMiB := flag.Int64("max-rss-mib", 256, "fail if a bulk-mode process peaks above this")
	flag.Parse()
	// Ambient AWS_* settings would override the fixed credentials used here.
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "AWS_") {
			os.Unsetenv(k)
		}
	}
	if *bin == "" {
		fatal("-bin or ZEROS3_BIN is required")
	}

	root, err := os.MkdirTemp("", "z2-bulk-transfer-*")
	if err != nil {
		fatal("%v", err)
	}
	defer os.RemoveAll(root)
	srcDir, dstDir := filepath.Join(root, "src"), filepath.Join(root, "dst")

	cmd, addr := startServer(*bin, srcDir)
	if err := mustBucket(addr, "src"); err != nil {
		fatal("%v", err)
	}
	size := int64(*sizeMiB) << 20
	if _, err := s3Client(addr).PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String("src"), Key: aws.String("obj"), Body: &seekable{reader: &reader{size: size}, size: size}, ContentLength: aws.Int64(size),
	}); err != nil {
		fatal("populate source: %v", err)
	}
	stopServer(cmd)

	var cfgs []config1
	for _, d := range ints(*delays) {
		delay := time.Duration(d) * time.Millisecond
		if *baseline != "" {
			for _, w := range ints(*v1Workers) {
				cfgs = append(cfgs, config1{fmt.Sprintf("v1 workers=%d", w), *baseline, w, 0, delay})
			}
		}
		for _, w := range ints(*v2Workers) {
			cfgs = append(cfgs, config1{fmt.Sprintf("bulk workers=%d", w), *bin, w, 0, delay})
		}
	}
	if *matrixDelay >= 0 {
		for _, tgt := range ints(*matrixTargets) {
			for _, w := range ints(*matrixWorkers) {
				cfgs = append(cfgs, config1{fmt.Sprintf("bulk target=%dMiB workers=%d", tgt, w), *bin, w, tgt, time.Duration(*matrixDelay) * time.Millisecond})
			}
		}
	}

	var srcETag string
	type key struct {
		label string
		delay time.Duration
	}
	reqs := map[key]int{}
	fmt.Printf("%-30s %6s %9s %8s %8s %8s %8s %8s %8s\n", "config", "rtt", "median", "MiB/s", "xfer-req", "srcRSS", "dstRSS", "cliRSS", "bytes")
	for _, c := range cfgs {
		var times []time.Duration
		var last result
		for i := 0; i < *runs; i++ {
			last = run(c, srcDir, dstDir, *bothSides, i == 0, &srcETag, *bin)
			times = append(times, last.elapsed)
		}
		med := median(times)
		xfer := transferReqs(last.src) + transferReqs(last.dst)
		reqs[key{c.label, c.delay}] = xfer
		mibps := float64(size>>20) / med.Seconds()
		fmt.Printf("%-30s %5dms %9s %8.2f %8d %7dM %7dM %7dM %8d\n", c.label, c.delay.Milliseconds(), med.Round(time.Millisecond), mibps, xfer,
			last.srvSrcRSS>>20, last.srvDstRSS>>20, last.clientRSS>>20, size)
		rec, _ := json.Marshal(map[string]any{
			"config": c.label, "delay_ms": c.delay.Milliseconds(), "median_ms": med.Milliseconds(), "mib_per_s": mibps,
			"runs_ms": func() []int64 {
				var o []int64
				for _, t := range times {
					o = append(o, t.Milliseconds())
				}
				return o
			}(),
			"source_requests": last.src, "dest_requests": last.dst, "transfer_requests": xfer,
			"dest_loose_files": last.dstLoose, "rss_source_mib": last.srvSrcRSS >> 20, "rss_dest_mib": last.srvDstRSS >> 20, "rss_client_mib": last.clientRSS >> 20,
		})
		fmt.Println("RECORD " + string(rec))
		if strings.HasPrefix(c.label, "bulk") {
			for who, rss := range map[string]int64{"source": last.srvSrcRSS, "destination": last.srvDstRSS, "client": last.clientRSS} {
				if rss > *maxRSSMiB<<20 {
					fail("%s: %s peak RSS %d MiB exceeds %d", c.label, who, rss>>20, *maxRSSMiB)
				}
			}
		}
	}
	if *baseline != "" {
		for _, d := range ints(*delays) {
			delay := time.Duration(d) * time.Millisecond
			for _, w1 := range ints(*v1Workers) {
				for _, w2 := range ints(*v2Workers) {
					a, b := reqs[key{fmt.Sprintf("v1 workers=%d", w1), delay}], reqs[key{fmt.Sprintf("bulk workers=%d", w2), delay}]
					cut := 100 * float64(a-b) / float64(a)
					fmt.Printf("request reduction at %dms (v1 w=%d -> bulk w=%d): %d -> %d (%.2f%%)\n", d, w1, w2, a, b, cut)
					if cut < *minReduction {
						fail("transfer request reduction %.2f%% below %.0f%%", cut, *minReduction)
					}
				}
			}
		}
	}
	if failed {
		os.Exit(1)
	}
	fmt.Println("PASS")
}

// seekable lets the SDK sign and retry a generated body without buffering it.
type seekable struct {
	reader *reader
	size   int64
}

func (s *seekable) Read(p []byte) (int, error) { return s.reader.Read(p) }
func (s *seekable) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		s.reader.pos = off
	case io.SeekCurrent:
		s.reader.pos += off
	case io.SeekEnd:
		s.reader.pos = s.size + off
	}
	return s.reader.pos, nil
}
