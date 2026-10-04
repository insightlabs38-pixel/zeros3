// z2_cas_batch compares ordinary ingest throughput of two zeros3 builds (a
// baseline predating grouped loose-CAS publication and the build under
// test) through real servers and the AWS SDK for Go v2. Per build and run
// it uses a fresh store and records: unique-data PutObject throughput and
// the server's peak RSS, the loose chunk count and average chunk size, a
// duplicate PutObject (which must stage nothing new), small-object PUT
// latency, and optionally a multipart upload of the same total size. Every
// store is deep-verified. It fails if the build under test is not at least
// -min-speedup times faster on the large PutObject, if its peak RSS exceeds
// -max-rss-mib, or if its small-object latency regresses by more than
// -max-small-regress.
//
// Usage:
//
//	go run ./harness/z2_cas_batch -bin NEW -baseline-bin OLD [-size-mib 256] [-runs 3] [-multipart-mib 256]
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
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
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	accessKeyID     = "AKIAZEROS3EXAMPLE01"
	secretAccessKey = "zeros3exampleSecretKeyForM1TestingOnly01"
	region          = "us-east-1"
	partSize        = 16 << 20
)

var failed bool

func fail(format string, args ...any) {
	failed = true
	fmt.Printf("FAIL: "+format+"\n", args...)
}

func fatal(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(2)
}

// body is a deterministic incompressible seekable stream; seed shifts it so
// different objects share no chunks.
type body struct{ seed, base, pos, size int64 } // base: offset of byte 0 within the seed's stream

func splitmix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ x>>30) * 0xbf58476d1ce4e5b9
	x = (x ^ x>>27) * 0x94d049bb133111eb
	return x ^ x>>31
}

func (b *body) Read(p []byte) (int, error) {
	if b.pos >= b.size {
		return 0, io.EOF
	}
	if rest := b.size - b.pos; int64(len(p)) > rest {
		p = p[:rest]
	}
	var w [8]byte
	for i := 0; i < len(p); {
		off := b.base + b.pos + int64(i)
		binary.LittleEndian.PutUint64(w[:], splitmix(uint64(off/8)+uint64(b.seed)<<40))
		i += copy(p[i:], w[off%8:])
	}
	b.pos += int64(len(p))
	return len(p), nil
}

func (b *body) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		b.pos = off
	case io.SeekCurrent:
		b.pos += off
	case io.SeekEnd:
		b.pos = b.size + off
	}
	return b.pos, nil
}

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

func startServer(bin, dir string) (*exec.Cmd, string) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("%v", err)
	}
	addr := l.Addr().String()
	l.Close()
	cmd := exec.Command(bin, "-store", dir, "-addr", addr)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		fatal("start server: %v", err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			return cmd, addr
		} else if time.Now().After(deadline) {
			fatal("server did not start")
		}
	}
}

func client(addr string) *s3.Client {
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

func looseChunks(dir string) (count int, bytes int64) {
	_ = filepath.WalkDir(filepath.Join(dir, "chunks"), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				count++
				bytes += info.Size()
			}
		}
		return nil
	})
	return
}

type sample struct {
	putSecs, dupSecs, smallSecs, mpSecs float64
	rssMiB                              int64
	chunks                              int
	avgChunk                            int64
	dupNew                              int
}

func put(cli *s3.Client, key string, b *body) time.Duration {
	start := time.Now()
	if _, err := cli.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String("b"), Key: aws.String(key), Body: b, ContentLength: aws.Int64(b.size),
	}); err != nil {
		fatal("PutObject %s: %v", key, err)
	}
	return time.Since(start)
}

func multipart(cli *s3.Client, key string, size int64) time.Duration {
	ctx := context.Background()
	start := time.Now()
	cr, err := cli.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("b"), Key: aws.String(key)})
	if err != nil {
		fatal("CreateMultipartUpload: %v", err)
	}
	var parts []types.CompletedPart
	for n, off := int32(1), int64(0); off < size; n, off = n+1, off+partSize {
		pb := &body{seed: 9, base: off, size: min(partSize, size-off)}
		up, err := cli.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String("b"), Key: aws.String(key), UploadId: cr.UploadId,
			PartNumber: aws.Int32(n), Body: pb, ContentLength: aws.Int64(pb.size)})
		if err != nil {
			fatal("UploadPart %d: %v", n, err)
		}
		parts = append(parts, types.CompletedPart{ETag: up.ETag, PartNumber: aws.Int32(n)})
	}
	if _, err := cli.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String("b"), Key: aws.String(key),
		UploadId: cr.UploadId, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}}); err != nil {
		fatal("CompleteMultipartUpload: %v", err)
	}
	return time.Since(start)
}

func run(bin string, sizeMiB, smallMiB, mpMiB int64) sample {
	dir, err := os.MkdirTemp("", "z2-cas-batch-*")
	if err != nil {
		fatal("%v", err)
	}
	defer os.RemoveAll(dir)
	cmd, addr := startServer(bin, dir)
	cli := client(addr)
	if _, err := cli.CreateBucket(context.Background(), &s3.CreateBucketInput{Bucket: aws.String("b")}); err != nil {
		fatal("CreateBucket: %v", err)
	}
	var s sample
	size := sizeMiB << 20
	s.putSecs = put(cli, "large", &body{seed: 1, size: size}).Seconds()
	s.rssMiB = peakRSS(cmd.Process.Pid) >> 20
	count, total := looseChunks(dir)
	s.chunks, s.avgChunk = count, total/int64(max(count, 1))

	s.dupSecs = put(cli, "large-dup", &body{seed: 1, size: size}).Seconds()
	after, _ := looseChunks(dir)
	s.dupNew = after - count

	var small []float64
	for i := 0; i < 5; i++ {
		small = append(small, put(cli, fmt.Sprintf("small-%d", i), &body{seed: int64(100 + i), size: smallMiB << 20}).Seconds())
	}
	sort.Float64s(small)
	s.smallSecs = small[len(small)/2]
	if mpMiB > 0 {
		s.mpSecs = multipart(cli, "mp", mpMiB<<20).Seconds()
	}
	_ = cmd.Process.Signal(os.Interrupt)
	_ = cmd.Wait()
	if out, err := exec.Command(bin, "verify", "-deep", "-store", dir).CombinedOutput(); err != nil {
		fail("%s: deep verify: %v\n%s", bin, err, out)
	}
	return s
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func main() {
	bin := flag.String("bin", os.Getenv("ZEROS3_BIN"), "zeros3 build under test")
	base := flag.String("baseline-bin", "", "zeros3 build predating grouped CAS publication")
	sizeMiB := flag.Int64("size-mib", 256, "PutObject size")
	smallMiB := flag.Int64("small-mib", 4, "small-object size for the latency check")
	mpMiB := flag.Int64("multipart-mib", 0, "also upload this many MiB as a multipart object (0: skip)")
	runs := flag.Int("runs", 3, "runs per build")
	minSpeedup := flag.Float64("min-speedup", 1.25, "required PutObject throughput ratio over the baseline")
	maxRSS := flag.Int64("max-rss-mib", 128, "fail if the build under test's peak RSS exceeds this")
	maxSmall := flag.Float64("max-small-regress", 1.5, "allowed small-object latency ratio versus the baseline")
	flag.Parse()
	for _, kv := range os.Environ() { // ambient AWS_* settings would override the fixed credentials
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "AWS_") {
			os.Unsetenv(k)
		}
	}
	if *bin == "" || *base == "" {
		fatal("-bin and -baseline-bin are required")
	}
	type row struct {
		label string
		bin   string
		runs  []sample
	}
	rows := []*row{{label: "baseline", bin: *base}, {label: "grouped", bin: *bin}}
	for i := 0; i < *runs; i++ { // alternate builds so drift hits both
		for _, r := range rows {
			r.runs = append(r.runs, run(r.bin, *sizeMiB, *smallMiB, *mpMiB))
		}
	}
	col := func(r *row, f func(sample) float64) float64 {
		var v []float64
		for _, s := range r.runs {
			v = append(v, f(s))
		}
		return median(v)
	}
	mibps := float64(*sizeMiB)
	fmt.Printf("%-9s %9s %8s %8s %7s %9s %9s %10s %9s\n", "build", "put MiB/s", "put s", "rss MiB", "chunks", "avg chunk", "dup put s", fmt.Sprintf("%dMiB put s", *smallMiB), "mp MiB/s")
	speed := map[string]float64{}
	for _, r := range rows {
		put := col(r, func(s sample) float64 { return s.putSecs })
		speed[r.label] = mibps / put
		last := r.runs[len(r.runs)-1]
		mp := 0.0
		if *mpMiB > 0 {
			mp = float64(*mpMiB) / col(r, func(s sample) float64 { return s.mpSecs })
		}
		fmt.Printf("%-9s %9.1f %8.2f %8d %7d %9d %9.2f %10.3f %9.1f\n", r.label, mibps/put, put,
			int64(col(r, func(s sample) float64 { return float64(s.rssMiB) })), last.chunks, last.avgChunk,
			col(r, func(s sample) float64 { return s.dupSecs }), col(r, func(s sample) float64 { return s.smallSecs }), mp)
		if last.dupNew != 0 {
			fail("%s: a duplicate PutObject created %d new loose chunks", r.label, last.dupNew)
		}
	}
	g := rows[1]
	ratio := speed["grouped"] / speed["baseline"]
	fmt.Printf("PutObject speedup: %.2fx\n", ratio)
	if ratio < *minSpeedup {
		fail("PutObject speedup %.2fx is below the required %.2fx", ratio, *minSpeedup)
	}
	if rss := col(g, func(s sample) float64 { return float64(s.rssMiB) }); rss > float64(*maxRSS) {
		fail("peak RSS %.0f MiB exceeds %d MiB", rss, *maxRSS)
	}
	smallRatio := col(g, func(s sample) float64 { return s.smallSecs }) / col(rows[0], func(s sample) float64 { return s.smallSecs })
	fmt.Printf("small-object latency ratio: %.2fx\n", smallRatio)
	if smallRatio > *maxSmall {
		fail("small-object latency regressed %.2fx (limit %.2fx)", smallRatio, *maxSmall)
	}
	if failed {
		os.Exit(1)
	}
	fmt.Println("PASS")
}
