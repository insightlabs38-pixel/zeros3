// Ephemeral benchmark-only harness for ZeroS3 M8H-A: measuring whether
// sequential chunk transport is a material bottleneck for real content
// transfer (remote replication, namespace replication, local delta sync,
// peer repair), against the exact accepted M8G HEAD.
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and never calls into ZeroS3's Go package internals. Every
// scenario drives the real `zeros3` CLI binary (replicate/repair/sync/
// verify) as an external subprocess against real `zeros3 serve`
// subprocesses on loopback, and populates fixtures through the real AWS
// SDK for Go v2 (a black-box S3 client) wherever ordinary PUT suffices.
// It makes NO changes to zeros3.go and adds no goroutines, flags, or
// behavior to production transfer paths -- this is pure measurement.
//
// Every scenario prints one or more single-line "RECORD <json>" entries
// to stdout; the results markdown is built from those lines. Wall time
// is real; content is deterministic pseudo-random (a small LCG), so
// results are reproducible on the same machine/build but are explicitly
// loopback-environment-specific (see B7 in the results doc).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
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

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
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

// deterministicBody generates n reproducible bytes via SHA-256 counter
// mode (block i = SHA256(seed_be32 || i_be64)) -- deterministic across
// every OS/architecture, not compressible or repetitive, and with
// negligible collision probability between different seeds' output at
// any sub-chunk-length window (unlike a plain power-of-2-modulus linear
// congruential generator: an earlier draft of this harness used one and
// it produced large accidental cross-seed chunk collisions at multi-
// hundred-MiB scale, because two LCG orbits sharing the same
// multiplicative coset become byte-identical, phase-shifted, from their
// intersection point onward -- silently corrupting "destination
// completely empty" measurements. SHA-256 counter mode has no such
// structure.)
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
	fmt.Fprintf(os.Stderr, "[m8h-bench] "+format+"\n", args...)
}

// ---------------------------------------------------------------------
// zeros3 CLI subprocess wrappers
// ---------------------------------------------------------------------

func runReplicate(binPath, fromEndpoint, toEndpoint, srcURI, dstURI string, dryRun, recursive bool) (string, time.Duration, error) {
	args := []string{"replicate"}
	if recursive {
		args = append(args, "-recursive")
	}
	if dryRun {
		args = append(args, "-dry-run")
	}
	args = append(args,
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

func runRepair(binPath, storeDir, peerEndpoint string) (string, time.Duration, error) {
	var buf bytes.Buffer
	cmd := exec.Command(binPath, "repair", "-store", storeDir, "-from", peerEndpoint,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	return buf.String(), elapsed, err
}

func runVerifyDeep(binPath, storeDir string) (string, time.Duration, error) {
	var buf bytes.Buffer
	cmd := exec.Command(binPath, "verify", "-store", storeDir, "-deep")
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	return buf.String(), elapsed, err
}

func runSync(binPath, localPath, endpoint, bucket, key string) (string, time.Duration, error) {
	args := []string{"sync", "-endpoint", endpoint,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region,
		localPath, "s3://" + bucket + "/" + key}
	var buf bytes.Buffer
	cmd := exec.Command(binPath, args...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	return buf.String(), elapsed, err
}

// ---------------------------------------------------------------------
// stat-line parsing (from the CLI's own human-readable stdout -- the
// exact numbers a real operator would see, never a second internal
// recomputation)
// ---------------------------------------------------------------------

func statLine(output, label string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func parseHumanBytesFromLine(line string) int64 {
	fields := strings.Fields(line)
	for i := 0; i < len(fields)-1; i++ {
		val, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			continue
		}
		unit := strings.ToUpper(fields[i+1])
		mult := map[string]float64{"B": 1, "KIB": 1 << 10, "MIB": 1 << 20, "GIB": 1 << 30, "TIB": 1 << 40}[unit]
		if mult == 0 {
			continue
		}
		return int64(val * mult)
	}
	return 0
}

// parseIntAfterColon extracts the first integer token after label's colon,
// e.g. "Chunks:              128" -> 128, or "... (128 unique chunks)" via
// parseFirstInt.
func parseFirstInt(line string) int {
	var cur strings.Builder
	for _, r := range line {
		if r >= '0' && r <= '9' {
			cur.WriteRune(r)
		} else if cur.Len() > 0 {
			break
		}
	}
	n, _ := strconv.Atoi(cur.String())
	return n
}

func parseFirstIntAfter(line, marker string) int {
	idx := strings.Index(line, marker)
	if idx < 0 {
		return 0
	}
	return parseFirstInt(line[idx+len(marker):])
}

// ---------------------------------------------------------------------
// median/min/max over a slice of durations
// ---------------------------------------------------------------------

func stats(durs []time.Duration) (median, min, max time.Duration) {
	if len(durs) == 0 {
		return 0, 0, 0
	}
	sorted := append([]time.Duration{}, durs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	min, max = sorted[0], sorted[len(sorted)-1]
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		median = sorted[mid]
	} else {
		median = (sorted[mid-1] + sorted[mid]) / 2
	}
	return
}

// ---------------------------------------------------------------------
// chunk-file discovery for B4 (repair) corruption injection
// ---------------------------------------------------------------------

func chunkFilesUnder(storeDir string) []string {
	var files []string
	root := filepath.Join(storeDir, "chunks")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files
}

func corruptFile(path string) error {
	return os.WriteFile(path, []byte("HARNESS-DELIBERATE-CORRUPTION-does-not-match-any-real-digest"), 0o644)
}

// ---------------------------------------------------------------------
// B8: tiny stdlib latency-injecting reverse proxy (benchmark-only, never
// touches ZeroS3 itself). Delays every proxied request/response by a
// fixed amount before forwarding, simulating deterministic network RTT
// on top of genuine loopback.
// ---------------------------------------------------------------------

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

func main() {
	binPath := flag.String("bin", "", "path to the zeros3 binary to benchmark (required)")
	outDir := flag.String("out", "", "scratch directory for store dirs (default: fresh temp dir)")
	scenario := flag.String("scenario", "all", "which scenario to run: b1a|b1b|b2|b3|b4|b6|b8|all")
	includeGiB := flag.Bool("big", false, "also run the optional 1 GiB case-A scenario")
	runs := flag.Int("runs", 5, "number of measured repeats per configuration (plus one warmup)")
	flag.Parse()

	if *binPath == "" {
		fatalf("%v", "-bin is required")
	}
	if *outDir == "" {
		d, err := os.MkdirTemp("", "zeros3-m8h-bench-*")
		if err != nil {
			fatalf("%v", err)
		}
		*outDir = d
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatalf("%v", err)
	}
	progress("scratch dir: %s", *outDir)

	run := func(name string, fn func()) {
		if *scenario == "all" || *scenario == name {
			progress("=== running scenario %s ===", name)
			fn()
			progress("=== finished scenario %s ===", name)
		}
	}

	run("b1a", func() { benchB1CaseA(*binPath, *outDir, *includeGiB, *runs) })
	run("b1b", func() { benchB1CaseB(*binPath, *outDir, *runs) })
	run("b2", func() { benchB2(*binPath, *outDir, *runs) })
	run("b3", func() { benchB3(*binPath, *outDir, *runs) })
	run("b4", func() { benchB4(*binPath, *outDir) })
	run("b6", func() { benchB6(*binPath, *outDir, *runs) })
	run("b8", func() { benchB8(*binPath, *outDir) })

	progress("all requested scenarios complete")
}

// =======================================================================
// B1 Case A -- destination completely empty. Each measured run uses a
// fresh random seed (unique content, same size) so the destination CAS
// genuinely lacks every chunk each time, without needing to restart
// either server between runs.
// =======================================================================
func benchB1CaseA(binPath, outDir string, includeGiB bool, runsN int) {
	srcDir := filepath.Join(outDir, "b1a-src")
	dstDir := filepath.Join(outDir, "b1a-dst")
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
	defer stopZeroS3(srcCmd)
	defer stopZeroS3(dstCmd)
	srcEndpoint, dstEndpoint := "http://"+srcAddr, "http://"+dstAddr
	srcCli := newClient(srcEndpoint)
	mustCreateBucket(srcCli, "src")
	mustCreateBucket(newClient(dstEndpoint), "dst")

	sizes := []int{64 << 20, 256 << 20}
	if includeGiB {
		sizes = append(sizes, 1<<30)
	}
	for _, sz := range sizes {
		n := runsN
		if sz >= 1<<30 {
			n = 1 // 1 GiB is optional/expensive; a single measured run is enough to show the shape
		} else if sz >= 256<<20 {
			n = min(n, 3)
		}
		var elapsed []time.Duration
		var lastOut string
		for i := 0; i <= n; i++ { // i==0 is the warmup, not recorded
			seed := uint32(sz) + uint32(i)*7919 + 1
			body := deterministicBody(seed, sz)
			key := fmt.Sprintf("case-a-%d-%d", sz, i)
			mustPut(srcCli, "src", key, body)
			out, el, err := runReplicate(binPath, srcEndpoint, dstEndpoint, "s3://src/"+key, "s3://dst/"+key, false, false)
			if err != nil {
				fatalf("B1a replicate size=%d run=%d: %v\n%s", sz, i, err, out)
			}
			if i == 0 {
				progress("B1a size=%s warmup done (wall=%s)", humanBytes(int64(sz)), el)
				continue
			}
			elapsed = append(elapsed, el)
			lastOut = out
			logical := parseHumanBytesFromLine(statLine(out, "Logical scanned:"))
			uploaded := parseHumanBytesFromLine(statLine(out, "Uploaded payload:"))
			avoided := parseHumanBytesFromLine(statLine(out, "Transfer avoided:"))
			chunks := parseFirstInt(statLine(out, "Chunks:"))
			uniqueUploaded := parseFirstIntAfter(statLine(out, "Uploaded payload:"), "(")
			progress("B1a size=%s run=%d wall=%s %.1f MiB/s logical=%s uploaded=%s avoided=%s chunks=%d",
				humanBytes(int64(sz)), i, el.Round(time.Millisecond), mibps(uploaded, el), humanBytes(logical), humanBytes(uploaded), humanBytes(avoided), chunks)
			record("b1_case_a_run", map[string]any{
				"size_bytes": sz, "run": i, "elapsed_ms": el.Milliseconds(),
				"logical_bytes": logical, "uploaded_bytes": uploaded, "bytes_avoided": avoided,
				"total_chunks": chunks, "unique_chunks_uploaded": uniqueUploaded,
				"mibps": mibps(uploaded, el),
			})
		}
		if len(elapsed) > 0 {
			med, mn, mx := stats(elapsed)
			uploaded := parseHumanBytesFromLine(statLine(lastOut, "Uploaded payload:"))
			record("b1_case_a_summary", map[string]any{
				"size_bytes": sz, "measured_runs": len(elapsed),
				"median_ms": med.Milliseconds(), "min_ms": mn.Milliseconds(), "max_ms": mx.Milliseconds(),
				"median_mibps": mibps(uploaded, med),
			})
			progress("B1a size=%s SUMMARY median=%s min=%s max=%s median_throughput=%.1f MiB/s",
				humanBytes(int64(sz)), med.Round(time.Millisecond), mn.Round(time.Millisecond), mx.Round(time.Millisecond), mibps(uploaded, med))
		}
	}
}

// =======================================================================
// B1 Case B -- partial reuse. A base object is first replicated into the
// destination so its chunks are genuinely present in destination CAS.
// Each source variant is base-prefix(p%) + fresh-random-suffix((1-p)%),
// so the CDC-chunked prefix matches the destination's existing chunks
// almost exactly (content-defined chunking reproduces the same cut
// points over byte-identical content) while the suffix is guaranteed
// missing. Each measured run uses a fresh suffix seed so repeats don't
// silently see the previous run's own upload as "already present".
// =======================================================================
func benchB1CaseB(binPath, outDir string, runsN int) {
	srcDir := filepath.Join(outDir, "b1b-src")
	dstDir := filepath.Join(outDir, "b1b-dst")
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
	defer stopZeroS3(srcCmd)
	defer stopZeroS3(dstCmd)
	srcEndpoint, dstEndpoint := "http://"+srcAddr, "http://"+dstAddr
	srcCli, dstCli := newClient(srcEndpoint), newClient(dstEndpoint)
	mustCreateBucket(srcCli, "src")
	mustCreateBucket(dstCli, "dst")

	const baseSize = 128 << 20
	base := deterministicBody(0xB1B0B1B0, baseSize)
	mustPut(dstCli, "dst", "reuse-base", base) // seeds destination CAS with base's chunks

	for _, pct := range []int{25, 75, 95} {
		prefixLen := (baseSize * pct) / 100
		n := min(runsN, 3)
		var elapsed []time.Duration
		var lastOut string
		for i := 0; i <= n; i++ {
			suffixLen := baseSize - prefixLen
			suffix := deterministicBody(uint32(pct)*1000+uint32(i)+1, suffixLen)
			variant := append(append([]byte{}, base[:prefixLen]...), suffix...)
			key := fmt.Sprintf("case-b-%d-%d", pct, i)
			mustPut(srcCli, "src", key, variant)
			out, el, err := runReplicate(binPath, srcEndpoint, dstEndpoint, "s3://src/"+key, "s3://dst/"+key, false, false)
			if err != nil {
				fatalf("B1b replicate pct=%d run=%d: %v\n%s", pct, i, err, out)
			}
			if i == 0 {
				progress("B1b pct=%d warmup done", pct)
				continue
			}
			elapsed = append(elapsed, el)
			lastOut = out
			logical := parseHumanBytesFromLine(statLine(out, "Logical scanned:"))
			uploaded := parseHumanBytesFromLine(statLine(out, "Uploaded payload:"))
			avoided := parseHumanBytesFromLine(statLine(out, "Transfer avoided:"))
			chunks := parseFirstInt(statLine(out, "Chunks:"))
			progress("B1b pct=%d run=%d wall=%s %.1f MiB/s logical=%s uploaded=%s avoided=%s reuse=%s",
				pct, i, el.Round(time.Millisecond), mibps(uploaded, el), humanBytes(logical), humanBytes(uploaded), humanBytes(avoided), statLine(out, "Reuse:"))
			record("b1_case_b_run", map[string]any{
				"target_reuse_pct": pct, "run": i, "elapsed_ms": el.Milliseconds(),
				"logical_bytes": logical, "uploaded_bytes": uploaded, "bytes_avoided": avoided,
				"total_chunks": chunks, "mibps": mibps(uploaded, el),
			})
		}
		if len(elapsed) > 0 {
			med, mn, mx := stats(elapsed)
			uploaded := parseHumanBytesFromLine(statLine(lastOut, "Uploaded payload:"))
			avoided := parseHumanBytesFromLine(statLine(lastOut, "Transfer avoided:"))
			logical := parseHumanBytesFromLine(statLine(lastOut, "Logical scanned:"))
			record("b1_case_b_summary", map[string]any{
				"target_reuse_pct": pct, "measured_runs": len(elapsed),
				"median_ms": med.Milliseconds(), "min_ms": mn.Milliseconds(), "max_ms": mx.Milliseconds(),
				"median_mibps_over_uploaded": mibps(uploaded, med),
				"logical_bytes":              logical, "uploaded_bytes_last": uploaded, "bytes_avoided_last": avoided,
			})
			progress("B1b pct=%d SUMMARY median=%s median_throughput_over_uploaded=%.1f MiB/s actual_reuse=%.1f%%",
				pct, med.Round(time.Millisecond), mibps(uploaded, med), 100*float64(avoided)/float64(logical))
		}
	}
}

// statsResult mirrors the subset of `zeros3 stats -json` fields this
// harness needs (see zeros3.go's StatsResult).
type statsResult struct {
	ChunkStoreFileBytes   int64 `json:"chunk_store_file_bytes"`
	ScopeUniqueChunkCount int64 `json:"scope_unique_chunk_count"`
}

func runStatsWholeStore(binPath, storeDir string) statsResult {
	out, err := exec.Command(binPath, "stats", "-store", storeDir, "-json").Output()
	if err != nil {
		fatalf("zeros3 stats -store %s: %v", storeDir, err)
	}
	var res statsResult
	if err := json.Unmarshal(out, &res); err != nil {
		fatalf("parsing stats JSON: %v (output: %s)", err, out)
	}
	return res
}

// =======================================================================
// B2 -- recursive namespace replication. Two shapes: many medium objects,
// more numerous small objects. Each run replicates into its own fresh
// destination prefix (run<i>/), so every run's destination namespace is
// genuinely empty -- fresh content per object per run too, via a per-run
// seed offset, so CAS-level reuse across runs is not expected either.
//
// The real (non-dry-run) `replicate -recursive` CLI prints no aggregate
// stats to stdout (only the -dry-run path does, via printDryRunNamespace)
// -- so unlike B1/B3/B6, "uploaded bytes" here is measured the same way
// M8's own baseline harness measures physical CAS growth: `zeros3 stats
// -json` on the whole destination store, before and after each run.
// =======================================================================
func benchB2(binPath, outDir string, runsN int) {
	shapes := []struct {
		name  string
		count int
		size  int
	}{
		{"32x16MiB", 32, 16 << 20},
		{"128x4MiB", 128, 4 << 20},
	}
	for _, shape := range shapes {
		srcDir := filepath.Join(outDir, "b2-src-"+shape.name)
		dstDir := filepath.Join(outDir, "b2-dst-"+shape.name)
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
		srcCli := newClient(srcEndpoint)
		mustCreateBucket(srcCli, "src")
		mustCreateBucket(newClient(dstEndpoint), "dst")

		n := min(runsN, 2) // each run touches count*size bytes; keep repeats modest
		var elapsed []time.Duration
		var lastUploaded int64
		logicalBytes := int64(shape.count) * int64(shape.size)
		for i := 0; i <= n; i++ {
			for j := 0; j < shape.count; j++ {
				seed := uint32(shape.count)*1000003 + uint32(j) + uint32(i)*99991 + 1
				body := deterministicBody(seed, shape.size)
				mustPut(srcCli, "src", fmt.Sprintf("obj-%04d", j), body)
			}
			dstURI := fmt.Sprintf("s3://dst/run%d/", i)
			// planning time via dry-run first (doesn't mutate destination)
			dryOut, dryEl, dryErr := runReplicate(binPath, srcEndpoint, dstEndpoint, "s3://src/", dstURI, true, true)
			if dryErr != nil {
				fatalf("B2 dry-run shape=%s run=%d: %v\n%s", shape.name, i, dryErr, dryOut)
			}
			before := runStatsWholeStore(binPath, dstDir)
			out, el, err := runReplicate(binPath, srcEndpoint, dstEndpoint, "s3://src/", dstURI, false, true)
			if err != nil {
				fatalf("B2 replicate shape=%s run=%d: %v\n%s", shape.name, i, err, out)
			}
			after := runStatsWholeStore(binPath, dstDir)
			uploaded := after.ChunkStoreFileBytes - before.ChunkStoreFileBytes
			if i == 0 {
				progress("B2 shape=%s warmup done (wall=%s, dry-run=%s)", shape.name, el, dryEl)
				continue
			}
			elapsed = append(elapsed, el)
			lastUploaded = uploaded
			progress("B2 shape=%s run=%d wall=%s dry_run=%s %.1f MiB/s uploaded(CAS growth)=%s of logical=%s",
				shape.name, i, el.Round(time.Millisecond), dryEl.Round(time.Millisecond), mibps(uploaded, el), humanBytes(uploaded), humanBytes(logicalBytes))
			record("b2_run", map[string]any{
				"shape": shape.name, "object_count": shape.count, "object_size_bytes": shape.size,
				"run": i, "elapsed_ms": el.Milliseconds(), "dry_run_ms": dryEl.Milliseconds(),
				"logical_bytes": logicalBytes, "uploaded_bytes_cas_growth": uploaded,
				"mibps": mibps(uploaded, el),
			})
		}
		stopZeroS3(srcCmd)
		stopZeroS3(dstCmd)
		if len(elapsed) > 0 {
			med, mn, mx := stats(elapsed)
			record("b2_summary", map[string]any{
				"shape": shape.name, "measured_runs": len(elapsed), "logical_bytes": logicalBytes,
				"median_ms": med.Milliseconds(), "min_ms": mn.Milliseconds(), "max_ms": mx.Milliseconds(),
				"median_mibps": mibps(lastUploaded, med),
			})
			progress("B2 shape=%s SUMMARY median=%s min=%s max=%s median_throughput=%.1f MiB/s",
				shape.name, med.Round(time.Millisecond), mn.Round(time.Millisecond), mx.Round(time.Millisecond), mibps(lastUploaded, med))
		}
	}
}

// =======================================================================
// B3 -- M6 local delta sync: initial full sync, then a localized edit and
// a second sync.
// =======================================================================
func benchB3(binPath, outDir string, runsN int) {
	storeDir := filepath.Join(outDir, "b3-store")
	addr, _ := freePort()
	cmd, err := startZeroS3(binPath, storeDir, addr)
	if err != nil {
		fatalf("%v", err)
	}
	defer stopZeroS3(cmd)
	endpoint := "http://" + addr
	mustCreateBucket(newClient(endpoint), "syncbench")

	const syncSize = 128 << 20
	base := deterministicBody(0xC6C6C6C6, syncSize)
	localPath := filepath.Join(outDir, "b3-fixture.bin")
	if err := os.WriteFile(localPath, base, 0o644); err != nil {
		fatalf("%v", err)
	}

	out1, el1, err := runSync(binPath, localPath, endpoint, "syncbench", "obj")
	if err != nil {
		fatalf("B3 initial sync: %v\n%s", err, out1)
	}
	logical1 := parseHumanBytesFromLine(statLine(out1, "Logical scanned:"))
	uploaded1 := parseHumanBytesFromLine(statLine(out1, "Uploaded payload:"))
	progress("B3 initial sync wall=%s %.1f MiB/s logical=%s uploaded=%s", el1.Round(time.Millisecond), mibps(uploaded1, el1), humanBytes(logical1), humanBytes(uploaded1))
	record("b3_initial", map[string]any{
		"logical_bytes": logical1, "uploaded_bytes": uploaded1, "elapsed_ms": el1.Milliseconds(), "mibps": mibps(uploaded1, el1),
	})

	// Localized edit: mutate ~0.1% of the file in a handful of spots.
	edited := append([]byte{}, base...)
	for k := 0; k < 4; k++ {
		at := (syncSize / 5) * (k + 1)
		copy(edited[at:at+4096], deterministicBody(0xEEEEEEEE+uint32(k), 4096))
	}
	if err := os.WriteFile(localPath, edited, 0o644); err != nil {
		fatalf("%v", err)
	}
	out2, el2, err := runSync(binPath, localPath, endpoint, "syncbench", "obj")
	if err != nil {
		fatalf("B3 edited sync: %v\n%s", err, out2)
	}
	logical2 := parseHumanBytesFromLine(statLine(out2, "Logical scanned:"))
	uploaded2 := parseHumanBytesFromLine(statLine(out2, "Uploaded payload:"))
	avoided2 := parseHumanBytesFromLine(statLine(out2, "Transfer avoided:"))
	progress("B3 edited sync wall=%s %.1f MiB/s logical=%s uploaded=%s avoided=%s", el2.Round(time.Millisecond), mibps(uploaded2, el2), humanBytes(logical2), humanBytes(uploaded2), humanBytes(avoided2))
	record("b3_edited", map[string]any{
		"logical_bytes": logical2, "uploaded_bytes": uploaded2, "bytes_avoided": avoided2, "elapsed_ms": el2.Milliseconds(), "mibps": mibps(uploaded2, el2),
	})
}

// =======================================================================
// B4 -- peer-assisted repair with 1/16/64/256 bad chunks. Each K uses a
// fresh pair of stores/objects (unique seed) with >=256 chunks available
// to damage, built via ordinary PutObject through the real AWS SDK on
// both "local" (A, to be damaged) and "peer" (B, healthy) stores.
// Because repairFromPeer always finishes with a full deep Verify, this
// also separately times a deep-verify-only pass on an equivalent healthy
// store of the same size, to show how much of repair's wall time is the
// mandatory post-repair verification rather than the peer chunk fetch.
// =======================================================================
func benchB4(binPath, outDir string) {
	const objSize = 64 << 20 // ~1000+ chunks at ~64KiB target
	for _, k := range []int{1, 16, 64, 256} {
		aDir := filepath.Join(outDir, fmt.Sprintf("b4-a-%d", k))
		bDir := filepath.Join(outDir, fmt.Sprintf("b4-b-%d", k))
		aAddr, _ := freePort()
		bAddr, _ := freePort()
		aCmd, err := startZeroS3(binPath, aDir, aAddr)
		if err != nil {
			fatalf("%v", err)
		}
		bCmd, err := startZeroS3(binPath, bDir, bAddr)
		if err != nil {
			fatalf("%v", err)
		}
		aCli, bCli := newClient("http://"+aAddr), newClient("http://"+bAddr)
		mustCreateBucket(aCli, "bucket")
		mustCreateBucket(bCli, "bucket")
		body := deterministicBody(uint32(0xB4000000+k), objSize)
		mustPut(aCli, "bucket", "target", body)
		mustPut(bCli, "bucket", "target", body)

		stopZeroS3(aCmd)
		chunks := chunkFilesUnder(aDir)
		if len(chunks) < k {
			fatalf("B4 k=%d: only %d chunk files available to corrupt (need >= %d); increase objSize", k, len(chunks), k)
		}
		totalChunks := len(chunks)
		for i := 0; i < k; i++ {
			if err := corruptFile(chunks[i]); err != nil {
				fatalf("B4 corrupt: %v", err)
			}
		}
		aCmd, err = startZeroS3(binPath, aDir, aAddr)
		if err != nil {
			fatalf("%v", err)
		}

		out, el, err := runRepair(binPath, aDir, "http://"+bAddr)
		if err != nil && !strings.Contains(out, "Post-repair verify:") {
			fatalf("B4 repair k=%d: %v\n%s", k, err, out)
		}
		badChunks := parseFirstIntAfter(statLine(out, "Bad chunks:"), ":")
		repaired := parseFirstIntAfter(statLine(out, "Repaired:"), ":")
		payloadFetched := parseHumanBytesFromLine(statLine(out, "Payload fetched:"))
		progress("B4 k=%d (of %d total chunks) wall=%s bad=%d repaired=%d payload_fetched=%s %.1f MiB/s",
			k, totalChunks, el.Round(time.Millisecond), badChunks, repaired, humanBytes(payloadFetched), mibps(payloadFetched, el))

		stopZeroS3(aCmd)
		stopZeroS3(bCmd)

		// Separate deep-verify-only baseline on an equivalent healthy store,
		// to estimate how much of the above wall time is the mandatory
		// post-repair verify rather than the peer chunk fetch itself.
		healthyDir := filepath.Join(outDir, fmt.Sprintf("b4-healthy-%d", k))
		healthyAddr, _ := freePort()
		hCmd, err := startZeroS3(binPath, healthyDir, healthyAddr)
		if err != nil {
			fatalf("%v", err)
		}
		healthyCli := newClient("http://" + healthyAddr)
		mustCreateBucket(healthyCli, "bucket")
		mustPut(healthyCli, "bucket", "target", body)
		stopZeroS3(hCmd)
		_, verifyOnlyEl, verr := runVerifyDeep(binPath, healthyDir)
		if verr != nil {
			fatalf("B4 baseline verify: %v", verr)
		}
		progress("B4 k=%d deep-verify-only baseline wall=%s", k, verifyOnlyEl.Round(time.Millisecond))

		record("b4_run", map[string]any{
			"bad_chunks": k, "total_chunks_in_object": totalChunks,
			"repaired": repaired, "payload_fetched_bytes": payloadFetched,
			"elapsed_ms": el.Milliseconds(), "deep_verify_only_baseline_ms": verifyOnlyEl.Milliseconds(),
			"mibps_over_fetched": mibps(payloadFetched, el),
		})
	}
}

// =======================================================================
// B6 -- planning vs. transport. Uses M8G's exposed planner (`replicate
// -dry-run`) immediately before the real transfer on the same unchanged
// source/destination state, for a representative large single-object
// transfer (case A, empty destination).
// =======================================================================
func benchB6(binPath, outDir string, runsN int) {
	srcDir := filepath.Join(outDir, "b6-src")
	dstDir := filepath.Join(outDir, "b6-dst")
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
	defer stopZeroS3(srcCmd)
	defer stopZeroS3(dstCmd)
	srcEndpoint, dstEndpoint := "http://"+srcAddr, "http://"+dstAddr
	srcCli := newClient(srcEndpoint)
	mustCreateBucket(srcCli, "src")
	mustCreateBucket(newClient(dstEndpoint), "dst")

	const sz = 256 << 20
	n := min(runsN, 3)
	var planEl, totalEl []time.Duration
	for i := 0; i <= n; i++ {
		seed := uint32(0x60606060) + uint32(i)*131
		body := deterministicBody(seed, sz)
		key := fmt.Sprintf("b6-%d", i)
		mustPut(srcCli, "src", key, body)
		dryOut, dEl, dErr := runReplicate(binPath, srcEndpoint, dstEndpoint, "s3://src/"+key, "s3://dst/"+key, true, false)
		if dErr != nil {
			fatalf("B6 dry-run run=%d: %v\n%s", i, dErr, dryOut)
		}
		out, tEl, err := runReplicate(binPath, srcEndpoint, dstEndpoint, "s3://src/"+key, "s3://dst/"+key, false, false)
		if err != nil {
			fatalf("B6 real run=%d: %v\n%s", i, err, out)
		}
		if i == 0 {
			progress("B6 warmup done (plan=%s total=%s)", dEl, tEl)
			continue
		}
		planEl = append(planEl, dEl)
		totalEl = append(totalEl, tEl)
		share := 100 * dEl.Seconds() / tEl.Seconds()
		progress("B6 run=%d plan=%s total=%s planning_share=%.2f%%", i, dEl.Round(time.Millisecond), tEl.Round(time.Millisecond), share)
		record("b6_run", map[string]any{
			"size_bytes": sz, "run": i, "planning_ms": dEl.Milliseconds(), "total_ms": tEl.Milliseconds(),
			"planning_share_pct": share,
		})
	}
	if len(planEl) > 0 {
		medPlan, _, _ := stats(planEl)
		medTotal, _, _ := stats(totalEl)
		share := 100 * medPlan.Seconds() / medTotal.Seconds()
		record("b6_summary", map[string]any{
			"size_bytes": sz, "median_planning_ms": medPlan.Milliseconds(), "median_total_ms": medTotal.Milliseconds(),
			"planning_share_pct": share,
		})
		progress("B6 SUMMARY median_plan=%s median_total=%s planning_share=%.2f%%", medPlan.Round(time.Millisecond), medTotal.Round(time.Millisecond), share)
	}
}

// =======================================================================
// B8 (optional) -- controlled latency simulation. A tiny stdlib reverse
// proxy sits in front of the destination endpoint and adds a fixed delay
// to every proxied request before forwarding it on to the real zeros3
// destination server. `replicate`'s -to flag simply points at the proxy
// instead of the real destination -- no zeros3 change required.
// =======================================================================
func benchB8(binPath, outDir string) {
	srcDir := filepath.Join(outDir, "b8-src")
	dstDir := filepath.Join(outDir, "b8-dst")
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
	defer stopZeroS3(srcCmd)
	defer stopZeroS3(dstCmd)
	srcEndpoint, dstEndpoint := "http://"+srcAddr, "http://"+dstAddr
	srcCli := newClient(srcEndpoint)
	mustCreateBucket(srcCli, "src")
	mustCreateBucket(newClient(dstEndpoint), "dst")

	const sz = 16 << 20 // modest size: enough chunks (~256) to show amplification without huge wall time at 10ms/chunk
	for i, delayMS := range []int{0, 1, 5, 10} {
		proxyURL, stopProxy := startLatencyProxy(dstEndpoint, time.Duration(delayMS)*time.Millisecond)
		seed := uint32(0x80000000) + uint32(i)*17
		body := deterministicBody(seed, sz)
		key := fmt.Sprintf("b8-%d", delayMS)
		mustPut(srcCli, "src", key, body)
		out, el, err := runReplicate(binPath, srcEndpoint, proxyURL, "s3://src/"+key, "s3://dst/"+key, false, false)
		stopProxy()
		if err != nil {
			fatalf("B8 delay=%dms: %v\n%s", delayMS, err, out)
		}
		uploaded := parseHumanBytesFromLine(statLine(out, "Uploaded payload:"))
		chunks := parseFirstInt(statLine(out, "Chunks:"))
		progress("B8 delay=%dms wall=%s %.2f MiB/s chunks=%d", delayMS, el.Round(time.Millisecond), mibps(uploaded, el), chunks)
		record("b8_run", map[string]any{
			"delay_ms": delayMS, "elapsed_ms": el.Milliseconds(), "uploaded_bytes": uploaded, "chunks": chunks,
			"mibps": mibps(uploaded, el),
		})
	}
}
