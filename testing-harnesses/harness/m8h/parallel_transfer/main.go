// Ephemeral benchmark-only harness for ZeroS3 M8H-B: measuring the
// bounded-parallel chunk-transfer worker pool against the exact same
// scenarios/methodology M8H-A used against the sequential (pre-M8H)
// baseline, so the two results are directly comparable.
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and never calls into ZeroS3's Go package internals. Every
// scenario drives the real `zeros3` CLI binary (replicate/repair) as an
// external subprocess against real `zeros3 serve` subprocesses on
// loopback, and populates fixtures through the real AWS SDK for Go v2 (a
// black-box S3 client). It makes NO changes to zeros3.go.
//
// Every scenario prints one or more single-line "RECORD <json>" entries to
// stdout.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log"
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

var runningCmds []*exec.Cmd

func fatalf(format string, args ...any) {
	for _, c := range runningCmds {
		stopZeroS3(c)
	}
	log.Fatalf(format, args...)
}

func startZeroS3(binPath, storeDir, addr string) (*exec.Cmd, error) {
	cmd := exec.Command(binPath, "-store", storeDir, "-addr", addr)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	runningCmds = append(runningCmds, cmd)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return cmd, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	cmd.Process.Kill()
	return nil, fmt.Errorf("zeros3 did not start listening on %s in time", addr)
}

func stopZeroS3(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	cmd.Process.Kill()
	cmd.Wait()
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

func newClient(endpoint string) *s3.Client {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		fatalf("config.LoadDefaultConfig: %v", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

func mustCreateBucket(cli *s3.Client, bucket string) {
	if _, err := cli.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		fatalf("CreateBucket(%s): %v", bucket, err)
	}
}

func mustPut(cli *s3.Client, bucket, key string, body []byte) {
	if _, err := cli.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body),
	}); err != nil {
		fatalf("PutObject(%s/%s): %v", bucket, key, err)
	}
}

// deterministicBody: SHA-256 counter mode, exactly matching M8H-A's own
// harness (harness/m8h/bench/main.go) -- reproducible, non-compressible,
// no cross-seed collisions, so results stay directly comparable.
func deterministicBody(seed uint32, n int) []byte {
	b := make([]byte, n)
	var seedBuf [4]byte
	binary.BigEndian.PutUint32(seedBuf[:], seed)
	pos := 0
	var counter uint64
	for pos < n {
		var ctrBuf [8]byte
		binary.BigEndian.PutUint64(ctrBuf[:], counter)
		h := sha256.New()
		h.Write(seedBuf[:])
		h.Write(ctrBuf[:])
		sum := h.Sum(nil)
		pos += copy(b[pos:], sum)
		counter++
	}
	return b
}

func humanBytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[i])
	}
	return fmt.Sprintf("%.2f %s", f, units[i])
}

func mibps(n int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return (float64(n) / (1024 * 1024)) / d.Seconds()
}

func record(kind string, fields map[string]any) {
	fields["kind"] = kind
	b, err := json.Marshal(fields)
	if err != nil {
		fatalf("marshal record: %v", err)
	}
	fmt.Println("RECORD " + string(b))
}

func progress(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[m8h-parallel] "+format+"\n", args...)
}

// ---------------------------------------------------------------------
// zeros3 CLI subprocess wrappers (-workers threaded through)
// ---------------------------------------------------------------------

func runReplicate(binPath, fromEndpoint, toEndpoint, srcURI, dstURI string, workers int, recursive bool) (string, time.Duration, error) {
	args := []string{"replicate"}
	if recursive {
		args = append(args, "-recursive")
	}
	args = append(args,
		"-workers", strconv.Itoa(workers),
		"-from", fromEndpoint, "-to", toEndpoint,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey,
		"-region", region,
		srcURI, dstURI)
	var buf bytes.Buffer
	cmd := exec.Command(binPath, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	return buf.String(), elapsed, err
}

func runReplicateDryRun(binPath, fromEndpoint, toEndpoint, srcURI, dstURI string) (string, time.Duration, error) {
	args := []string{"replicate", "-dry-run",
		"-from", fromEndpoint, "-to", toEndpoint,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey,
		"-region", region,
		srcURI, dstURI}
	var buf bytes.Buffer
	cmd := exec.Command(binPath, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	return buf.String(), elapsed, err
}

func runRepair(binPath, storeDir, peerEndpoint string, workers int) (string, time.Duration, error) {
	var buf bytes.Buffer
	cmd := exec.Command(binPath, "repair", "-store", storeDir, "-from", peerEndpoint,
		"-workers", strconv.Itoa(workers),
		"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	return buf.String(), elapsed, err
}

func runStatsWholeStore(binPath, storeDir string) int64 {
	out, err := exec.Command(binPath, "stats", "-store", storeDir, "-json").Output()
	if err != nil {
		fatalf("stats: %v", err)
	}
	var v struct {
		PhysicalCASBytes int64 `json:"physical_cas_bytes"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		fatalf("stats unmarshal: %v (%s)", err, out)
	}
	return v.PhysicalCASBytes
}

func startLatencyProxy(targetEndpoint string, delay time.Duration) (proxyURL string, stop func()) {
	target, err := url.Parse(targetEndpoint)
	if err != nil {
		fatalf("parsing proxy target: %v", err)
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	origDirector := rp.Director
	rp.Director = func(r *http.Request) {
		origDirector(r)
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatalf("proxy listen: %v", err)
	}
	srv := &http.Server{Handler: rp}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), func() { srv.Close() }
}

func stats(durs []time.Duration) (median, min, max time.Duration) {
	if len(durs) == 0 {
		return 0, 0, 0
	}
	s := append([]time.Duration{}, durs...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	min, max = s[0], s[len(s)-1]
	median = s[len(s)/2]
	return
}

// ---------------------------------------------------------------------
// Scenario 1: single-object replication, destination empty, worker
// matrix x payload sizes x proxy delay -- the direct M8H-A B1-Case-A /
// B8 comparison.
// ---------------------------------------------------------------------

func benchSingleObject(binPath, outDir string, runsN int, sizesMiB []int, delays []time.Duration, workerCounts []int) {
	for _, sizeMiB := range sizesMiB {
		for _, delay := range delays {
			for _, workers := range workerCounts {
				var elapsed []time.Duration
				var chunks int
				// One untimed warmup + runsN measured runs, fresh
				// content/servers each time so the destination is
				// genuinely empty on every run.
				for run := 0; run < runsN+1; run++ {
					srcDir := filepath.Join(outDir, fmt.Sprintf("so-src-%d-%v-%d-%d", sizeMiB, delay, workers, run))
					dstDir := filepath.Join(outDir, fmt.Sprintf("so-dst-%d-%v-%d-%d", sizeMiB, delay, workers, run))
					srcAddr, err := freePort()
					if err != nil {
						fatalf("%v", err)
					}
					dstAddr, err := freePort()
					if err != nil {
						fatalf("%v", err)
					}
					srcCmd, err := startZeroS3(binPath, srcDir, srcAddr)
					if err != nil {
						fatalf("%v", err)
					}
					dstCmd, err := startZeroS3(binPath, dstDir, dstAddr)
					if err != nil {
						fatalf("%v", err)
					}

					srcEndpoint := "http://" + srcAddr
					dstEndpoint := "http://" + dstAddr
					toEndpoint := dstEndpoint
					var stopProxy func()
					if delay > 0 {
						toEndpoint, stopProxy = startLatencyProxy(dstEndpoint, delay)
					}

					cli := newClient(srcEndpoint)
					mustCreateBucket(cli, "src")
					body := deterministicBody(uint32(700000+sizeMiB*97+run), sizeMiB*1024*1024)
					mustPut(cli, "src", "obj.bin", body)
					dstCli := newClient(dstEndpoint)
					mustCreateBucket(dstCli, "dst")

					out, el, err := runReplicate(binPath, srcEndpoint, toEndpoint, "s3://src/obj.bin", "s3://dst/obj.bin", workers, false)
					if err != nil {
						fatalf("replicate (size=%dMiB delay=%v workers=%d run=%d) failed: %v\n%s", sizeMiB, delay, workers, run, err, out)
					}
					if run > 0 {
						elapsed = append(elapsed, el)
					}
					if chunks == 0 {
						physBytes := runStatsWholeStore(binPath, dstDir)
						_ = physBytes
					}

					if stopProxy != nil {
						stopProxy()
					}
					stopZeroS3(srcCmd)
					stopZeroS3(dstCmd)
				}
				median, min, max := stats(elapsed)
				record("single_object", map[string]any{
					"size_mib":  sizeMiB,
					"delay_ms":  delay.Milliseconds(),
					"workers":   workers,
					"median_ms": median.Milliseconds(),
					"min_ms":    min.Milliseconds(),
					"max_ms":    max.Milliseconds(),
					"mibps":     mibps(int64(sizeMiB)*1024*1024, median),
				})
				progress("single_object size=%dMiB delay=%v workers=%d median=%v (%.2f MiB/s)", sizeMiB, delay, workers, median, mibps(int64(sizeMiB)*1024*1024, median))
			}
		}
	}
}

// ---------------------------------------------------------------------
// Scenario 2: planning-only dry-run, workers should have zero effect on
// wall time (B3.1 requirement) -- one confirmatory sample.
// ---------------------------------------------------------------------

func benchDryRunUnaffected(binPath, outDir string) {
	srcDir := filepath.Join(outDir, "dryrun-src")
	dstDir := filepath.Join(outDir, "dryrun-dst")
	srcAddr, _ := freePort()
	dstAddr, _ := freePort()
	srcCmd, err := startZeroS3(binPath, srcDir, srcAddr)
	if err != nil {
		fatalf("%v", err)
	}
	defer stopZeroS3(srcCmd)
	dstCmd, err := startZeroS3(binPath, dstDir, dstAddr)
	if err != nil {
		fatalf("%v", err)
	}
	defer stopZeroS3(dstCmd)
	srcEndpoint, dstEndpoint := "http://"+srcAddr, "http://"+dstAddr
	cli := newClient(srcEndpoint)
	mustCreateBucket(cli, "src")
	body := deterministicBody(42, 64*1024*1024)
	mustPut(cli, "src", "obj.bin", body)
	dstCli := newClient(dstEndpoint)
	mustCreateBucket(dstCli, "dst")

	_, el, err := runReplicateDryRun(binPath, srcEndpoint, dstEndpoint, "s3://src/obj.bin", "s3://dst/obj.bin")
	if err != nil {
		fatalf("dry-run replicate failed: %v", err)
	}
	record("dry_run_planning", map[string]any{"size_mib": 64, "planning_ms": el.Milliseconds()})
	progress("dry-run planning (64 MiB, no worker effect possible): %v", el)
}

// ---------------------------------------------------------------------
// Scenario 3: namespace replication, worker matrix.
// ---------------------------------------------------------------------

func benchNamespace(binPath, outDir string, runsN int, workerCounts []int) {
	const objects, objSizeMiB = 16, 4
	for _, workers := range workerCounts {
		var elapsed []time.Duration
		for run := 0; run < runsN+1; run++ {
			srcDir := filepath.Join(outDir, fmt.Sprintf("ns-src-%d-%d", workers, run))
			dstDir := filepath.Join(outDir, fmt.Sprintf("ns-dst-%d-%d", workers, run))
			srcAddr, _ := freePort()
			dstAddr, _ := freePort()
			srcCmd, err := startZeroS3(binPath, srcDir, srcAddr)
			if err != nil {
				fatalf("%v", err)
			}
			dstCmd, err := startZeroS3(binPath, dstDir, dstAddr)
			if err != nil {
				fatalf("%v", err)
			}
			srcEndpoint, dstEndpoint := "http://"+srcAddr, "http://"+dstAddr
			cli := newClient(srcEndpoint)
			mustCreateBucket(cli, "src")
			for i := 0; i < objects; i++ {
				body := deterministicBody(uint32(800000+run*1000+i), objSizeMiB*1024*1024)
				mustPut(cli, "src", fmt.Sprintf("ns/obj%d.bin", i), body)
			}
			dstCli := newClient(dstEndpoint)
			mustCreateBucket(dstCli, "dst")

			out, el, err := runReplicate(binPath, srcEndpoint, dstEndpoint, "s3://src/ns/", "s3://dst/ns/", workers, true)
			if err != nil {
				fatalf("namespace replicate (workers=%d run=%d) failed: %v\n%s", workers, run, err, out)
			}
			if run > 0 {
				elapsed = append(elapsed, el)
			}
			stopZeroS3(srcCmd)
			stopZeroS3(dstCmd)
		}
		median, min, max := stats(elapsed)
		total := int64(objects*objSizeMiB) * 1024 * 1024
		record("namespace", map[string]any{
			"objects": objects, "object_size_mib": objSizeMiB, "workers": workers,
			"median_ms": median.Milliseconds(), "min_ms": min.Milliseconds(), "max_ms": max.Milliseconds(),
			"mibps": mibps(total, median),
		})
		progress("namespace objects=%d workers=%d median=%v (%.2f MiB/s)", objects, workers, median, mibps(total, median))
	}
}

// ---------------------------------------------------------------------
// Scenario 4: repair, worker matrix.
// ---------------------------------------------------------------------

func chunkFilesUnder(storeDir string) []string {
	var out []string
	chunksDir := filepath.Join(storeDir, "chunks")
	filepath.Walk(chunksDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func benchRepair(binPath, outDir string, runsN int, workerCounts []int) {
	const objSizeMiB = 32
	for _, workers := range workerCounts {
		var elapsed []time.Duration
		for run := 0; run < runsN+1; run++ {
			localDir := filepath.Join(outDir, fmt.Sprintf("rep-local-%d-%d", workers, run))
			peerDir := filepath.Join(outDir, fmt.Sprintf("rep-peer-%d-%d", workers, run))
			localAddr, _ := freePort()
			peerAddr, _ := freePort()
			localCmd, err := startZeroS3(binPath, localDir, localAddr)
			if err != nil {
				fatalf("%v", err)
			}
			peerCmd, err := startZeroS3(binPath, peerDir, peerAddr)
			if err != nil {
				fatalf("%v", err)
			}
			localEndpoint, peerEndpoint := "http://"+localAddr, "http://"+peerAddr
			cli := newClient(localEndpoint)
			mustCreateBucket(cli, "b")
			body := deterministicBody(uint32(900000+run), objSizeMiB*1024*1024)
			mustPut(cli, "b", "k.bin", body)
			peerCli := newClient(peerEndpoint)
			mustCreateBucket(peerCli, "b")
			mustPut(peerCli, "b", "k.bin", body)

			stopZeroS3(localCmd)
			// Corrupt every local chunk on disk while the local server is
			// stopped (repair needs the store unlocked to open exclusively
			// here only to corrupt files -- the store itself doesn't need
			// to be open for this).
			for _, f := range chunkFilesUnder(localDir) {
				if err := os.WriteFile(f, []byte("corrupted-for-m8h-b-repair-benchmark"), 0o644); err != nil {
					fatalf("corrupt %s: %v", f, err)
				}
			}

			out, el, err := runRepair(binPath, localDir, peerEndpoint, workers)
			if err != nil {
				fatalf("repair (workers=%d run=%d) failed: %v\n%s", workers, run, err, out)
			}
			if run > 0 {
				elapsed = append(elapsed, el)
			}
			stopZeroS3(peerCmd)
		}
		median, min, max := stats(elapsed)
		record("repair", map[string]any{
			"object_size_mib": objSizeMiB, "workers": workers,
			"median_ms": median.Milliseconds(), "min_ms": min.Milliseconds(), "max_ms": max.Milliseconds(),
			"mibps": mibps(int64(objSizeMiB)*1024*1024, median),
		})
		progress("repair size=%dMiB workers=%d median=%v (%.2f MiB/s)", objSizeMiB, workers, median, mibps(int64(objSizeMiB)*1024*1024, median))
	}
}

func main() {
	binPath := flag.String("bin", "", "path to the zeros3 (M8H-B) binary to benchmark (required)")
	outDir := flag.String("out", "", "scratch directory for store dirs (default: fresh temp dir)")
	scenario := flag.String("scenario", "all", "which scenario to run: single|dryrun|namespace|repair|all")
	runs := flag.Int("runs", 3, "number of measured repeats per configuration (plus one warmup)")
	singleSizes := flag.String("single-sizes-mib", "64,256", "comma-separated payload sizes (MiB) for the single-object scenario")
	singleDelaysMs := flag.String("single-delays-ms", "0,5,10", "comma-separated proxy delays (ms) for the single-object scenario")
	flag.Parse()

	if *binPath == "" {
		fatalf("%v", "-bin is required")
	}
	if *outDir == "" {
		d, err := os.MkdirTemp("", "zeros3-m8h-parallel-*")
		if err != nil {
			fatalf("%v", err)
		}
		*outDir = d
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatalf("%v", err)
	}
	progress("scratch dir: %s", *outDir)

	workerCounts := []int{1, 2, 4, 8, 16}

	run := func(name string, fn func()) {
		if *scenario == "all" || *scenario == name {
			progress("=== running scenario %s ===", name)
			fn()
			progress("=== finished scenario %s ===", name)
		}
	}

	var sizesMiB []int
	for _, s := range strings.Split(*singleSizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			fatalf("bad -single-sizes-mib: %v", err)
		}
		sizesMiB = append(sizesMiB, n)
	}
	var delays []time.Duration
	for _, s := range strings.Split(*singleDelaysMs, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			fatalf("bad -single-delays-ms: %v", err)
		}
		delays = append(delays, time.Duration(n)*time.Millisecond)
	}

	run("single", func() {
		benchSingleObject(*binPath, *outDir, *runs, sizesMiB, delays, workerCounts)
	})
	run("dryrun", func() { benchDryRunUnaffected(*binPath, *outDir) })
	run("namespace", func() { benchNamespace(*binPath, *outDir, *runs, workerCounts) })
	run("repair", func() { benchRepair(*binPath, *outDir, *runs, workerCounts) })

	progress("all requested scenarios complete")
}
