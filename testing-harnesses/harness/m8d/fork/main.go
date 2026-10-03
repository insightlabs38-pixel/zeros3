// Ephemeral, external interoperability harness for ZeroS3 M8D
// (copy-on-write namespace fork, `zeros3 fork`).
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 purely as a
// black-box S3 client. `zeros3 fork` itself is driven as a real external
// process (os/exec against the built `zeros3` binary) against ONE real
// `zeros3 serve` subprocess -- fork is same-store only, so unlike the
// M8A/M8B/M8C harnesses' two-server pattern, every phase here uses a
// single store/server with multiple buckets. This harness never calls
// into ZeroS3's Go package internals and never populates the store's
// internal CAS directly: every fixture is written through the real AWS
// SDK; the one milestone-permitted exception (Phase 10) directly
// corrupts an already-validly-published CAS chunk file on disk, since
// corruption is the condition under test there.
//
// What this proves that the internal (in-process, deterministic-hook-
// based) zeros3_test.go suite cannot: that a completely independent, real
// AWS SDK client reads back exactly what a real `zeros3 fork` subprocess
// wrote, that the zero-new-CAS-payload claim holds under an independent
// filesystem measurement of a real running store (not just the CLI's own
// self-reported statistic), a real process interruption/resume, and that
// M8D composes cleanly with M8B's peer-assisted repair across a chunk now
// shared by two namespaces. Internal tests remain the source of truth for
// exact mapping/overlap/precondition correctness (see zeros3's own
// STATUS.md); this harness is external, real-process, real-SDK evidence
// on top of that, not a replacement for it.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
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
)

const (
	accessKeyID     = "AKIAZEROS3EXAMPLE01"
	secretAccessKey = "zeros3exampleSecretKeyForM1TestingOnly01"
	region          = "us-east-1"
)

var pass, fail, info int

func check(name string, err error) {
	if err != nil {
		fail++
		fmt.Printf("FAIL: %s: %v\n", name, err)
		return
	}
	pass++
	fmt.Printf("PASS: %s\n", name)
}

func check1(name string, err error) bool {
	if err != nil {
		fail++
		fmt.Printf("FAIL: %s: %v\n", name, err)
		return false
	}
	pass++
	fmt.Printf("PASS: %s\n", name)
	return true
}

func requireTrue(name string, cond bool, detail string) {
	if !cond {
		fail++
		fmt.Printf("FAIL: %s: %s\n", name, detail)
		return
	}
	pass++
	fmt.Printf("PASS: %s\n", name)
}

func noteInfo(format string, args ...any) {
	info++
	fmt.Printf("INFO: "+format+"\n", args...)
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
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return cmd, nil
		}
		time.Sleep(50 * time.Millisecond)
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
		log.Fatalf("config.LoadDefaultConfig: %v", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

// forkCmd builds (but does not run) the real `zeros3 fork` subprocess
// command for one namespace fork. Flags precede the positional SOURCE/DEST
// arguments, matching Go's flag package semantics (parsing stops at the
// first non-flag argument) -- the same convention every other CLI
// invocation in this repository's harnesses already follows.
func forkCmd(binPath, endpoint, srcURI, dstURI string) *exec.Cmd {
	return exec.Command(binPath, "fork",
		"-endpoint", endpoint,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey,
		"-region", region,
		srcURI, dstURI)
}

// runFork runs forkCmd to completion, never a Go package call, returning
// its combined stdout+stderr and exit error.
func runFork(binPath, endpoint, srcURI, dstURI string) (output string, err error) {
	cmd := forkCmd(binPath, endpoint, srcURI, dstURI)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

func runRepair(binPath, localDir, peerEndpoint string) (output string, err error) {
	cmd := exec.Command(binPath, "repair",
		"-store", localDir,
		"-from", peerEndpoint,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey,
		"-region", region)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

func runVerifyDeep(binPath, storeDir string) (output string, err error) {
	var buf bytes.Buffer
	cmd := exec.Command(binPath, "verify", "-store", storeDir, "-deep")
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

func statLine(output, label string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func statInt(output, label string) int64 {
	line := statLine(output, label)
	fields := strings.Fields(line)
	for _, f := range fields {
		f = strings.ReplaceAll(f, ",", "")
		if n, err := strconv.ParseInt(f, 10, 64); err == nil {
			return n
		}
	}
	return 0
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

func randomBytes(seed int64, n int) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	_, _ = r.Read(b)
	return b
}

func mustReadAll(r interface{ Read([]byte) (int, error) }) []byte {
	var buf bytes.Buffer
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.Bytes()
}

func putObject(ctx context.Context, c *s3.Client, bucket, key string, body []byte) error {
	_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	return err
}

// listAllKeys returns every key under bucket via the real AWS SDK's own
// ListObjectsV2 pagination (never zeros3's own listing code), sorted.
func listAllKeys(ctx context.Context, c *s3.Client, bucket string) ([]string, error) {
	var keys []string
	var token *string
	for {
		out, err := c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), ContinuationToken: token})
		if err != nil {
			return nil, err
		}
		for _, o := range out.Contents {
			keys = append(keys, aws.ToString(o.Key))
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		token = out.NextContinuationToken
	}
	sort.Strings(keys)
	return keys, nil
}

// chunkFilesUnder returns every CAS chunk leaf file path under
// storeDir/chunks, sorted for determinism -- a black-box discovery
// mechanism, not an assumption about internal chunk naming.
func chunkFilesUnder(storeDir string) ([]string, error) {
	var files []string
	root := filepath.Join(storeDir, "chunks")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
	return files, err
}

// chunkFileTotalBytes independently sums the on-disk size of every CAS
// chunk file -- a second, orthogonal measurement to plain file *count*,
// so the zero-payload proof does not rely on any single metric.
func chunkFileTotalBytes(files []string) (int64, error) {
	var total int64
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			return 0, err
		}
		total += fi.Size()
	}
	return total, nil
}

func corruptFile(path string) error {
	return os.WriteFile(path, []byte("HARNESS-DELIBERATE-CORRUPTION-does-not-match-any-real-digest"), 0o644)
}

func mustTempStore(prefix string) string {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		log.Fatal(err)
	}
	return dir
}

// oneServer starts a single, fresh zeros3 store/server pair per phase --
// fork is same-store only, so every phase here needs exactly one server
// hosting both the source and destination buckets, unlike the M8A/M8B/
// M8C harnesses' two-server pattern.
type oneServer struct {
	binPath  string
	dir      string
	cmd      *exec.Cmd
	addr     string
	endpoint string
	client   *s3.Client
}

func startOneServer(binPath, tag string) *oneServer {
	s := &oneServer{binPath: binPath}
	s.dir = mustTempStore("zeros3-m8d-" + tag + "-store-")
	var err error
	s.addr, err = freePort()
	if err != nil {
		log.Fatal(err)
	}
	s.cmd, err = startZeroS3(binPath, s.dir, s.addr)
	if err != nil {
		log.Fatalf("starting zeros3 (%s): %v", tag, err)
	}
	s.endpoint = "http://" + s.addr
	s.client = newClient(s.endpoint)
	return s
}

func (s *oneServer) restart() error {
	stopZeroS3(s.cmd)
	var err error
	s.cmd, err = startZeroS3(s.binPath, s.dir, s.addr)
	return err
}

func (s *oneServer) stop() {
	stopZeroS3(s.cmd)
	os.RemoveAll(s.dir)
}

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	ctx := context.Background()

	// =========================================================================
	// Phase 1: basic fork -- populate a small source tree through the AWS
	// SDK, fork the whole bucket, verify destination keys and exact GET
	// bytes.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p1")
		defer s.stop()
		check("Phase1: CreateBucket prod", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p1-prod")})))
		check("Phase1: CreateBucket experiment", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p1-experiment")})))

		tree := map[string][]byte{
			"a.bin":     randomBytes(11, 5000),
			"sub/b.bin": randomBytes(12, 6000),
			"sub/c.txt": randomBytes(13, 800),
		}
		for key, body := range tree {
			check("Phase1: AWS SDK PutObject "+key, putObject(ctx, s.client, "p1-prod", key, body))
		}

		out, err := runFork(binPath, s.endpoint, "s3://p1-prod/", "s3://p1-experiment/")
		fmt.Print(out)
		check("Phase1: zeros3 fork whole bucket -> whole bucket", err)
		requireTrue("Phase1: reports exactly 3 objects forked", statInt(out, "Objects forked:") == 3, out)

		keys, err := listAllKeys(ctx, s.client, "p1-experiment")
		check("Phase1: AWS SDK ListObjectsV2 on fork", err)
		want := []string{"a.bin", "sub/b.bin", "sub/c.txt"}
		sort.Strings(want)
		requireTrue("Phase1: fork holds exactly the expected keys", fmt.Sprint(keys) == fmt.Sprint(want), fmt.Sprintf("got=%v want=%v", keys, want))

		for key, body := range tree {
			get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p1-experiment"), Key: aws.String(key)})
			if !check1("Phase1: AWS SDK GetObject "+key, err) {
				continue
			}
			got := mustReadAll(get.Body)
			requireTrue("Phase1: byte-for-byte GET equality for "+key, bytes.Equal(got, body), "content mismatch")
		}
	}()

	// =========================================================================
	// Phase 2: zero CAS payload proof -- measure CAS state before/after via
	// two independent filesystem measurements (chunk file count, chunk file
	// total bytes), never trusting only the CLI's own self-reported stat.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p2")
		defer s.stop()
		check("Phase2: CreateBucket prod", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p2-prod")})))
		check("Phase2: CreateBucket experiment", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p2-experiment")})))

		// Several objects, some sharing content, some not -- a
		// representative CAS population, not just one trivial object.
		shared := randomBytes(21, 400_000)
		check("Phase2: AWS SDK PutObject one.bin", putObject(ctx, s.client, "p2-prod", "one.bin", shared))
		check("Phase2: AWS SDK PutObject two.bin (identical content)", putObject(ctx, s.client, "p2-prod", "two.bin", shared))
		check("Phase2: AWS SDK PutObject three.bin (unrelated)", putObject(ctx, s.client, "p2-prod", "three.bin", randomBytes(22, 250_000)))

		stopZeroS3(s.cmd)
		beforeFiles, err := chunkFilesUnder(s.dir)
		check("Phase2: discover CAS chunk files before fork", err)
		beforeBytes, err := chunkFileTotalBytes(beforeFiles)
		check("Phase2: sum CAS chunk file bytes before fork", err)
		s.cmd, err = startZeroS3(binPath, s.dir, s.addr)
		check("Phase2: restart server after pre-fork measurement", err)

		out, err := runFork(binPath, s.endpoint, "s3://p2-prod/", "s3://p2-experiment/")
		fmt.Print(out)
		check("Phase2: zeros3 fork", err)
		requireTrue("Phase2: CLI reports 0 B new CAS payload", parseHumanBytesFromLine(statLine(out, "CAS payload bytes added:")) == 0, out)

		stopZeroS3(s.cmd)
		afterFiles, err := chunkFilesUnder(s.dir)
		check("Phase2: discover CAS chunk files after fork", err)
		afterBytes, err := chunkFileTotalBytes(afterFiles)
		check("Phase2: sum CAS chunk file bytes after fork", err)
		s.cmd, err = startZeroS3(binPath, s.dir, s.addr)
		check("Phase2: restart server after post-fork measurement", err)

		requireTrue("Phase2: independent measurement -- new CAS chunk files = 0",
			len(afterFiles) == len(beforeFiles), fmt.Sprintf("before=%d after=%d", len(beforeFiles), len(afterFiles)))
		requireTrue("Phase2: independent measurement -- new CAS payload bytes = 0",
			afterBytes == beforeBytes, fmt.Sprintf("before=%d after=%d", beforeBytes, afterBytes))
		noteInfo("Phase2: CAS before=%d files/%d bytes, after=%d files/%d bytes (manifests/journal still grew -- only chunk payload proven unchanged)",
			len(beforeFiles), beforeBytes, len(afterFiles), afterBytes)
	}()

	// =========================================================================
	// Phase 3: large logical clone -- a handful of related, sizable
	// objects, forked, with a visually compelling logical-vs-payload
	// report.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p3")
		defer s.stop()
		check("Phase3: CreateBucket prod", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p3-prod")})))
		check("Phase3: CreateBucket experiment", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p3-experiment")})))

		const objSize = 24_000_000 // 24 MiB each; several objects for a compelling logical total without an excessive harness runtime.
		var totalLogical int64
		bodies := make(map[string][]byte)
		for i := 0; i < 5; i++ {
			key := fmt.Sprintf("large/object-%d.bin", i)
			body := randomBytes(int64(300+i), objSize)
			bodies[key] = body
			totalLogical += int64(len(body))
			check("Phase3: AWS SDK PutObject "+key, putObject(ctx, s.client, "p3-prod", key, body))
		}

		out, err := runFork(binPath, s.endpoint, "s3://p3-prod/", "s3://p3-experiment/")
		fmt.Print(out)
		check("Phase3: zeros3 fork of a large logical namespace", err)
		requireTrue("Phase3: reports 0 B new CAS payload", parseHumanBytesFromLine(statLine(out, "CAS payload bytes added:")) == 0, out)
		reportedLogical := parseHumanBytesFromLine(statLine(out, "Logical bytes cloned:"))
		// parseHumanBytesFromLine round-trips through the CLI's own 2-decimal
		// human-readable rounding (humanBytes), so an exact match isn't
		// meaningful at this scale -- allow the rounding error inherent in
		// that format (well under 0.01% of a multi-object, multi-MiB total).
		logicalDelta := reportedLogical - totalLogical
		if logicalDelta < 0 {
			logicalDelta = -logicalDelta
		}
		requireTrue("Phase3: reports the full logical size cloned (within human-readable rounding)",
			logicalDelta < 10_000, fmt.Sprintf("reported=%d want=%d delta=%d", reportedLogical, totalLogical, logicalDelta))
		noteInfo("Phase3: Logical cloned: %d bytes (%.1f MiB), New CAS payload: 0 B", totalLogical, float64(totalLogical)/(1<<20))

		for key, body := range bodies {
			get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p3-experiment"), Key: aws.String(key)})
			if !check1("Phase3: AWS SDK GetObject "+key, err) {
				continue
			}
			got := mustReadAll(get.Body)
			requireTrue("Phase3: byte-for-byte GET equality for "+key, bytes.Equal(got, body), "content mismatch")
		}
	}()

	// =========================================================================
	// Phase 4: copy-on-write mutation -- after a fork, modify a small,
	// localized portion of a large destination object through the ordinary
	// AWS SDK, verify source unchanged, measure new CAS payload, and
	// demonstrate high chunk reuse.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p4")
		defer s.stop()
		check("Phase4: CreateBucket prod", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p4-prod")})))
		check("Phase4: CreateBucket experiment", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p4-experiment")})))

		const size = 20_000_000
		original := randomBytes(401, size)
		check("Phase4: AWS SDK PutObject big.bin", putObject(ctx, s.client, "p4-prod", "big.bin", original))

		out, err := runFork(binPath, s.endpoint, "s3://p4-prod/", "s3://p4-experiment/")
		fmt.Print(out)
		check("Phase4: zeros3 fork before mutation", err)

		stopZeroS3(s.cmd)
		beforeFiles, err := chunkFilesUnder(s.dir)
		check("Phase4: discover CAS chunk files before mutation", err)
		s.cmd, err = startZeroS3(binPath, s.dir, s.addr)
		check("Phase4: restart server after pre-mutation measurement", err)

		// Localized edit: keep the first 90% identical, replace the tail --
		// CDC content-defined chunk boundaries mean only the changed
		// region's chunks should differ.
		keepLen := size * 9 / 10
		edited := make([]byte, 0, size)
		edited = append(edited, original[:keepLen]...)
		edited = append(edited, randomBytes(402, size-keepLen)...)
		check("Phase4: AWS SDK PutObject localized edit to experiment/big.bin", putObject(ctx, s.client, "p4-experiment", "big.bin", edited))

		stopZeroS3(s.cmd)
		afterFiles, err := chunkFilesUnder(s.dir)
		check("Phase4: discover CAS chunk files after mutation", err)
		s.cmd, err = startZeroS3(binPath, s.dir, s.addr)
		check("Phase4: restart server after post-mutation measurement", err)

		newChunks := len(afterFiles) - len(beforeFiles)
		requireTrue("Phase4: localized edit added new CAS chunks (not zero)", newChunks > 0, fmt.Sprintf("before=%d after=%d", len(beforeFiles), len(afterFiles)))
		requireTrue("Phase4: localized edit did not rewrite the whole object's chunk set",
			newChunks < len(afterFiles)/2, fmt.Sprintf("new=%d total-after=%d (expected high reuse)", newChunks, len(afterFiles)))
		reuse := 100 * float64(len(afterFiles)-newChunks) / float64(len(afterFiles))
		noteInfo("Phase4: post-fork mutation added %d new chunk files out of %d total (%.1f%% of destination's chunks were reused, unchanged)",
			newChunks, len(afterFiles), reuse)

		get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p4-prod"), Key: aws.String("big.bin")})
		if check1("Phase4: AWS SDK GetObject prod/big.bin after fork mutation", err) {
			got := mustReadAll(get.Body)
			requireTrue("Phase4: source remains byte-for-byte unchanged after editing the fork", bytes.Equal(got, original), "source was mutated")
		}
		get2, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p4-experiment"), Key: aws.String("big.bin")})
		if check1("Phase4: AWS SDK GetObject experiment/big.bin after mutation", err) {
			got2 := mustReadAll(get2.Body)
			requireTrue("Phase4: fork reflects the localized edit exactly", bytes.Equal(got2, edited), "edit not reflected")
		}
	}()

	// =========================================================================
	// Phase 5: source divergence -- modify source after fork (dest
	// unaffected), then delete the source object (dest remains readable).
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p5")
		defer s.stop()
		check("Phase5: CreateBucket prod", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p5-prod")})))
		check("Phase5: CreateBucket experiment", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p5-experiment")})))
		check("Phase5: AWS SDK PutObject a.bin", putObject(ctx, s.client, "p5-prod", "a.bin", []byte("original content")))

		out, err := runFork(binPath, s.endpoint, "s3://p5-prod/", "s3://p5-experiment/")
		fmt.Print(out)
		check("Phase5: zeros3 fork", err)

		check("Phase5: AWS SDK PutObject overwrite source a.bin", putObject(ctx, s.client, "p5-prod", "a.bin", []byte("changed in source after fork")))
		get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p5-experiment"), Key: aws.String("a.bin")})
		if check1("Phase5: AWS SDK GetObject fork a.bin after source edit", err) {
			got := mustReadAll(get.Body)
			requireTrue("Phase5: fork remains unchanged after editing source", string(got) == "original content", fmt.Sprintf("got=%q", got))
		}

		_, err = s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("p5-prod"), Key: aws.String("a.bin")})
		check("Phase5: AWS SDK DeleteObject source a.bin", err)
		get2, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p5-experiment"), Key: aws.String("a.bin")})
		if check1("Phase5: AWS SDK GetObject fork a.bin after source deletion", err) {
			got2 := mustReadAll(get2.Body)
			requireTrue("Phase5: fork remains readable after source deletion", string(got2) == "original content", fmt.Sprintf("got=%q", got2))
		}
	}()

	// =========================================================================
	// Phase 6: destination conflict -- prepopulate one destination key,
	// run a multi-object fork, verify the conflicting key is not silently
	// overwritten, unrelated objects still fork, and the command exits
	// nonzero.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p6")
		defer s.stop()
		check("Phase6: CreateBucket prod", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p6-prod")})))
		check("Phase6: CreateBucket experiment", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p6-experiment")})))
		check("Phase6: AWS SDK PutObject prod a.bin", putObject(ctx, s.client, "p6-prod", "a.bin", []byte("a")))
		check("Phase6: AWS SDK PutObject prod conflict.bin", putObject(ctx, s.client, "p6-prod", "conflict.bin", []byte("new content")))
		check("Phase6: AWS SDK PutObject prod z.bin", putObject(ctx, s.client, "p6-prod", "z.bin", []byte("z")))
		check("Phase6: AWS SDK PutObject pre-existing experiment conflict.bin", putObject(ctx, s.client, "p6-experiment", "conflict.bin", []byte("must survive untouched")))

		out, err := runFork(binPath, s.endpoint, "s3://p6-prod/", "s3://p6-experiment/")
		fmt.Print(out)
		requireTrue("Phase6: zeros3 fork exits nonzero on destination conflict", err != nil, "expected nonzero exit")
		requireTrue("Phase6: failure report names conflict.bin", strings.Contains(out, "conflict.bin"), out)

		get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p6-experiment"), Key: aws.String("conflict.bin")})
		if check1("Phase6: AWS SDK GetObject pre-existing conflict.bin", err) {
			got := mustReadAll(get.Body)
			requireTrue("Phase6: pre-existing destination object was not silently overwritten", string(got) == "must survive untouched", fmt.Sprintf("got=%q", got))
		}
		for _, key := range []string{"a.bin", "z.bin"} {
			if _, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p6-experiment"), Key: aws.String(key)}); err != nil {
				fail++
				fmt.Printf("FAIL: Phase6: unrelated object %s should have forked: %v\n", key, err)
			} else {
				pass++
				fmt.Printf("PASS: Phase6: unrelated object %s forked despite conflict elsewhere\n", key)
			}
		}
	}()

	// =========================================================================
	// Phase 7: >1000 objects -- ~1500 small objects, complete pagination,
	// zero new CAS payload.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p7")
		defer s.stop()
		check("Phase7: CreateBucket prod", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p7-prod")})))
		check("Phase7: CreateBucket experiment", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p7-experiment")})))

		const n = 1500
		for i := 0; i < n; i++ {
			key := fmt.Sprintf("k/%04d", i)
			if err := putObject(ctx, s.client, "p7-prod", key, []byte("x")); err != nil {
				log.Fatalf("Phase7: seeding object %s: %v", key, err)
			}
		}
		pass++
		fmt.Printf("PASS: Phase7: seeded %d objects via AWS SDK\n", n)

		out, err := runFork(binPath, s.endpoint, "s3://p7-prod/", "s3://p7-experiment/")
		fmt.Print(out)
		check("Phase7: zeros3 fork of 1500 objects", err)
		requireTrue("Phase7: reports all 1500 objects forked", statInt(out, "Objects forked:") == n, out)
		requireTrue("Phase7: reports 0 B new CAS payload", parseHumanBytesFromLine(statLine(out, "CAS payload bytes added:")) == 0, out)

		keys, err := listAllKeys(ctx, s.client, "p7-experiment")
		check("Phase7: AWS SDK ListObjectsV2 pagination over 1500 objects", err)
		requireTrue("Phase7: exactly 1500 unique keys, no duplicates/missing", len(keys) == n, fmt.Sprintf("got %d", len(keys)))
	}()

	// =========================================================================
	// Phase 8: interruption/resume -- kill a real `zeros3 fork` process
	// mid-run, rerun, verify no partial/duplicated destination object and
	// zero unnecessary CAS payload duplication.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p8")
		defer s.stop()
		check("Phase8: CreateBucket prod", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p8-prod")})))
		check("Phase8: CreateBucket experiment", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p8-experiment")})))

		bodies := map[string][]byte{
			"ns/one.bin":   randomBytes(801, 9_000_000),
			"ns/two.bin":   randomBytes(802, 9_000_000),
			"ns/three.bin": randomBytes(803, 9_000_000),
		}
		for key, body := range bodies {
			check("Phase8: AWS SDK PutObject "+key, putObject(ctx, s.client, "p8-prod", key, body))
		}

		interruptCmd := forkCmd(binPath, s.endpoint, "s3://p8-prod/ns/", "s3://p8-experiment/ns/")
		if err := interruptCmd.Start(); err != nil {
			log.Fatalf("Phase8: starting interruptible fork: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
		killErr := interruptCmd.Process.Kill()
		interruptCmd.Wait()
		check("Phase8: kill zeros3 fork mid-run", killErr)

		out, err := runFork(binPath, s.endpoint, "s3://p8-prod/ns/", "s3://p8-experiment/ns/")
		fmt.Print(out)
		check("Phase8: resumed rerun completes cleanly", err)
		requireTrue("Phase8: resumed rerun reports all 3 objects forked, zero failed",
			statInt(out, "Objects forked:") == 3 && statInt(out, "Objects failed:") == 0, out)
		requireTrue("Phase8: resumed rerun adds zero new CAS payload (same-store fork, always)",
			parseHumanBytesFromLine(statLine(out, "CAS payload bytes added:")) == 0, out)

		for key, body := range bodies {
			get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p8-experiment"), Key: aws.String(key)})
			if !check1("Phase8: AWS SDK GetObject "+key+" after resume", err) {
				continue
			}
			got := mustReadAll(get.Body)
			requireTrue("Phase8: exact content for "+key+" after interrupted-then-resumed fork", bytes.Equal(got, body), "content mismatch after resume")
		}
	}()

	// =========================================================================
	// Phase 9: restart / verify / GC -- restart server after fork, AWS SDK
	// list/get exact, deep verify green, GC preserves both namespaces'
	// reachability.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p9")
		defer s.stop()
		check("Phase9: CreateBucket prod", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p9-prod")})))
		check("Phase9: CreateBucket experiment", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p9-experiment")})))

		bodies := map[string][]byte{
			"x/one.bin": randomBytes(901, 2_000_000),
			"x/two.bin": randomBytes(902, 3_000_000),
		}
		for key, body := range bodies {
			check("Phase9: AWS SDK PutObject "+key, putObject(ctx, s.client, "p9-prod", key, body))
		}
		out, err := runFork(binPath, s.endpoint, "s3://p9-prod/", "s3://p9-experiment/")
		fmt.Print(out)
		check("Phase9: zeros3 fork before restart", err)

		check("Phase9: restart zeros3 on the same store directory", s.restart())

		keys, err := listAllKeys(ctx, s.client, "p9-experiment")
		check("Phase9: AWS SDK ListObjectsV2 after restart", err)
		requireTrue("Phase9: fork lists exactly the expected keys after restart", len(keys) == len(bodies), fmt.Sprintf("keys=%v", keys))
		for key, body := range bodies {
			get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p9-experiment"), Key: aws.String(key)})
			if !check1("Phase9: AWS SDK GetObject "+key+" after restart", err) {
				continue
			}
			got := mustReadAll(get.Body)
			requireTrue("Phase9: exact content for "+key+" after restart", bytes.Equal(got, body), "content mismatch after restart")
		}

		stopZeroS3(s.cmd)
		verifyOut, verifyErr := runVerifyDeep(binPath, s.dir)
		fmt.Println(verifyOut)
		check("Phase9: zeros3 verify -deep after fork+restart", verifyErr)

		gcOut := new(bytes.Buffer)
		gcCmd := exec.Command(binPath, "gc", "-store", s.dir)
		gcCmd.Stdout = gcOut
		gcCmd.Stderr = gcOut
		gcErr := gcCmd.Run()
		fmt.Println(gcOut.String())
		check("Phase9: zeros3 gc (dry run) after fork sees a healthy live set", gcErr)

		check("Phase9: restart zeros3 after verify/gc", s.restart())
		for bucket, key := range map[string]string{"p9-prod": "x/one.bin", "p9-experiment": "x/two.bin"} {
			if _, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
				fail++
				fmt.Printf("FAIL: Phase9: %s/%s unreachable after GC: %v\n", bucket, key, err)
			} else {
				pass++
				fmt.Printf("PASS: Phase9: %s/%s remains reachable after GC\n", bucket, key)
			}
		}
	}()

	// =========================================================================
	// Phase 10: M8B composition -- corrupt a physical chunk shared by
	// source and fork (fork is same-store, so this corrupts it for *both*
	// namespaces simultaneously); repair from an independent peer server
	// holding the same content; confirm deep verify identifies the
	// affected objects and both source and fork return exact bytes again.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p10")
		defer s.stop()
		check("Phase10: CreateBucket prod", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p10-prod")})))
		check("Phase10: CreateBucket experiment", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p10-experiment")})))

		body := randomBytes(1001, 3_000_000)
		check("Phase10: AWS SDK PutObject prod shared.bin", putObject(ctx, s.client, "p10-prod", "shared.bin", body))
		out, err := runFork(binPath, s.endpoint, "s3://p10-prod/", "s3://p10-experiment/")
		fmt.Print(out)
		check("Phase10: zeros3 fork before corruption", err)

		// Independent peer, holding byte-identical content, purely as a
		// repair source -- fork's own store cannot be its own peer, since
		// corrupting a shared chunk corrupts it for both namespaces at
		// once.
		peerDir := mustTempStore("zeros3-m8d-p10-peer-store-")
		defer os.RemoveAll(peerDir)
		peerAddr, err := freePort()
		check("Phase10: allocate peer port", err)
		peerCmd, err := startZeroS3(binPath, peerDir, peerAddr)
		check("Phase10: start independent peer zeros3", err)
		defer stopZeroS3(peerCmd)
		peerEndpoint := "http://" + peerAddr
		peerClient := newClient(peerEndpoint)
		check("Phase10: CreateBucket on peer", check1err(peerClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("peer")})))
		check("Phase10: AWS SDK PutObject identical content on peer", putObject(ctx, peerClient, "peer", "shared.bin", body))

		stopZeroS3(s.cmd)
		chunks, err := chunkFilesUnder(s.dir)
		check("Phase10: discover store CAS chunk files", err)
		requireTrue("Phase10: at least one chunk file to corrupt", len(chunks) > 0, "no chunk files found")
		if len(chunks) > 0 {
			check("Phase10: corrupt one chunk file shared by prod and experiment (milestone-permitted exception)", corruptFile(chunks[0]))
		}
		check("Phase10: restart store after corrupting the shared chunk", s.restart())

		_, preErr := runVerifyDeep(binPath, s.dir)
		requireTrue("Phase10: verify -deep detects the corruption before repair (affecting both namespaces)", preErr != nil, "expected verify -deep to fail before repair")

		stopZeroS3(s.cmd)
		repairOut, repairErr := runRepair(binPath, s.dir, peerEndpoint)
		fmt.Println(repairOut)
		check("Phase10: zeros3 repair -from the independent peer", repairErr)
		check("Phase10: restart store after repair", s.restart())

		getProd, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p10-prod"), Key: aws.String("shared.bin")})
		if check1("Phase10: AWS SDK GetObject prod/shared.bin after repair", err) {
			got := mustReadAll(getProd.Body)
			requireTrue("Phase10: prod exact content after repair", bytes.Equal(got, body), "content mismatch")
		}
		getExp, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p10-experiment"), Key: aws.String("shared.bin")})
		if check1("Phase10: AWS SDK GetObject experiment/shared.bin after repair", err) {
			got := mustReadAll(getExp.Body)
			requireTrue("Phase10: experiment (fork) exact content after repair -- one physical repair fixed both namespaces", bytes.Equal(got, body), "content mismatch")
		}
	}()

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed, %d informational =====\n", pass, fail, info)
	if fail > 0 {
		os.Exit(1)
	}
}

// check1err adapts an AWS SDK call's (result, error) return into a plain
// error for check(), discarding the result -- used for CreateBucket calls
// throughout, whose result is never otherwise inspected.
func check1err[T any](_ T, err error) error { return err }
