// Ephemeral, external interoperability harness for ZeroS3 M8C (prefix /
// bucket delta replication, `zeros3 replicate -recursive`).
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 purely as a
// black-box S3 client. `zeros3 replicate -recursive` itself is driven as
// a real external process (os/exec against the built `zeros3` binary)
// against TWO independent, real `zeros3 serve` subprocesses on separate
// stores and ports -- exactly the way a real user would invoke it. This
// harness never calls into ZeroS3's Go package internals and never
// populates either store's internal CAS directly: every fixture is
// written through the real AWS SDK; the one milestone-permitted exception
// (Phase 9) directly corrupts an already-validly-published CAS chunk file
// on disk, since corruption is the condition under test there.
//
// What this proves that the internal (in-process, deterministic-hook-
// based) zeros3_test.go suite cannot: that a completely independent, real
// AWS SDK client reads back exactly what a real `zeros3 replicate
// -recursive` subprocess wrote across a real two-server namespace, real
// pagination beyond 1000 objects, a real process interruption/resume, and
// that M8C composes cleanly with M8B's peer-assisted repair. Internal
// tests remain the source of truth for exact mapping/negotiation/
// precondition correctness (see zeros3's own STATUS.md); this harness is
// external, real-process, real-SDK, real-two-server evidence on top of
// that, not a replacement for it.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// replicateRecursiveCmd builds (but does not run) the real `zeros3
// replicate -recursive` subprocess command for one namespace replication.
func replicateRecursiveCmd(binPath, fromEndpoint, toEndpoint, srcURI, dstURI string) *exec.Cmd {
	return exec.Command(binPath, "replicate", "-recursive",
		"-from", fromEndpoint, "-to", toEndpoint,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey,
		"-region", region,
		srcURI, dstURI)
}

// runReplicateRecursive runs replicateRecursiveCmd to completion, never a
// Go package call, returning its combined stdout+stderr and exit error.
func runReplicateRecursive(binPath, fromEndpoint, toEndpoint, srcURI, dstURI string) (output string, err error) {
	cmd := replicateRecursiveCmd(binPath, fromEndpoint, toEndpoint, srcURI, dstURI)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

// runRepair invokes the real `zeros3 repair` CLI as a subprocess (never a
// Go package call) against localDir, from peerEndpoint.
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

// twoServers starts a fresh source+destination zeros3 pair on freshly
// created, empty store directories, returning their commands, addresses,
// and AWS SDK clients. Each phase below gets its own pair (matching the
// M8B repair harness's own precedent) so phases stay fully independent
// and results stay deterministic.
type twoServers struct {
	binPath                  string
	srcDir, dstDir           string
	srcCmd, dstCmd           *exec.Cmd
	srcAddr, dstAddr         string
	srcEndpoint, dstEndpoint string
	srcClient, dstClient     *s3.Client
}

func startTwoServers(binPath, tag string) *twoServers {
	ts := &twoServers{binPath: binPath}
	ts.srcDir = mustTempStore("zeros3-m8c-" + tag + "-src-store-")
	ts.dstDir = mustTempStore("zeros3-m8c-" + tag + "-dst-store-")
	var err error
	ts.srcAddr, err = freePort()
	if err != nil {
		log.Fatal(err)
	}
	ts.dstAddr, err = freePort()
	if err != nil {
		log.Fatal(err)
	}
	ts.srcCmd, err = startZeroS3(binPath, ts.srcDir, ts.srcAddr)
	if err != nil {
		log.Fatalf("starting source zeros3 (%s): %v", tag, err)
	}
	ts.dstCmd, err = startZeroS3(binPath, ts.dstDir, ts.dstAddr)
	if err != nil {
		log.Fatalf("starting destination zeros3 (%s): %v", tag, err)
	}
	ts.srcEndpoint = "http://" + ts.srcAddr
	ts.dstEndpoint = "http://" + ts.dstAddr
	ts.srcClient = newClient(ts.srcEndpoint)
	ts.dstClient = newClient(ts.dstEndpoint)
	return ts
}

func (ts *twoServers) stop() {
	stopZeroS3(ts.srcCmd)
	stopZeroS3(ts.dstCmd)
	os.RemoveAll(ts.srcDir)
	os.RemoveAll(ts.dstDir)
}

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	ctx := context.Background()

	// =========================================================================
	// Phase 1: empty namespace.
	// =========================================================================
	func() {
		ts := startTwoServers(binPath, "p1")
		defer ts.stop()
		_, err := ts.srcClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p1-src")})
		check("Phase1: CreateBucket on source", err)
		_, err = ts.dstClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p1-dst")})
		check("Phase1: CreateBucket on destination", err)

		out, err := runReplicateRecursive(binPath, ts.srcEndpoint, ts.dstEndpoint, "s3://p1-src/", "s3://p1-dst/")
		fmt.Print(out)
		check("Phase1: zeros3 replicate -recursive on an empty source namespace succeeds", err)
		requireTrue("Phase1: reports zero objects discovered", statInt(out, "Objects discovered:") == 0, out)
		requireTrue("Phase1: reports zero payload transferred", parseHumanBytesFromLine(statLine(out, "Payload transferred:")) == 0, out)

		keys, err := listAllKeys(ctx, ts.dstClient, "p1-dst")
		check("Phase1: AWS SDK ListObjectsV2 on destination", err)
		requireTrue("Phase1: destination namespace remains empty", len(keys) == 0, fmt.Sprintf("keys=%v", keys))
	}()

	// =========================================================================
	// Phase 2: initial namespace replication -- the milestone's own worked
	// example tree, replicating only datasets/ into dst's archive/ prefix.
	// =========================================================================
	func() {
		ts := startTwoServers(binPath, "p2")
		defer ts.stop()
		_, err := ts.srcClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p2-src")})
		check("Phase2: CreateBucket on source", err)
		_, err = ts.dstClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p2-dst")})
		check("Phase2: CreateBucket on destination", err)

		tree := map[string][]byte{
			"datasets/a.bin":         randomBytes(101, 5000),
			"datasets/b.bin":         randomBytes(102, 6000),
			"datasets/sub/c.bin":     randomBytes(103, 7000),
			"datasets/sub/d.txt":     randomBytes(104, 800),
			"other/not-selected.bin": randomBytes(105, 900),
		}
		for key, body := range tree {
			check("Phase2: AWS SDK PutObject "+key, putObject(ctx, ts.srcClient, "p2-src", key, body))
		}

		out, err := runReplicateRecursive(binPath, ts.srcEndpoint, ts.dstEndpoint, "s3://p2-src/datasets/", "s3://p2-dst/archive/")
		fmt.Print(out)
		check("Phase2: zeros3 replicate -recursive (prefix -> prefix)", err)
		requireTrue("Phase2: reports exactly 4 objects discovered/replicated", statInt(out, "Objects discovered:") == 4 && statInt(out, "Replicated:") == 4, out)

		keys, err := listAllKeys(ctx, ts.dstClient, "p2-dst")
		check("Phase2: AWS SDK ListObjectsV2 on destination", err)
		want := []string{"archive/a.bin", "archive/b.bin", "archive/sub/c.bin", "archive/sub/d.txt"}
		sort.Strings(want)
		requireTrue("Phase2: destination holds exactly the expected mapped keys, nothing else",
			fmt.Sprint(keys) == fmt.Sprint(want), fmt.Sprintf("got=%v want=%v", keys, want))

		for srcKey, dstKey := range map[string]string{
			"datasets/a.bin":     "archive/a.bin",
			"datasets/b.bin":     "archive/b.bin",
			"datasets/sub/c.bin": "archive/sub/c.bin",
			"datasets/sub/d.txt": "archive/sub/d.txt",
		} {
			get, err := ts.dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p2-dst"), Key: aws.String(dstKey)})
			if !check1("Phase2: AWS SDK GetObject "+dstKey, err) {
				continue
			}
			got := mustReadAll(get.Body)
			requireTrue("Phase2: byte-for-byte GET equality for "+dstKey, bytes.Equal(got, tree[srcKey]), "content mismatch")
		}
	}()

	// =========================================================================
	// Phase 3: destination prepopulation / global CAS reuse -- the
	// strongest M8C demo. Destination already holds a related object
	// (different key) sharing most chunks with a source object under the
	// replicated prefix.
	// =========================================================================
	func() {
		ts := startTwoServers(binPath, "p3")
		defer ts.stop()
		_, err := ts.srcClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p3-src")})
		check("Phase3: CreateBucket on source", err)
		_, err = ts.dstClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p3-dst")})
		check("Phase3: CreateBucket on destination", err)

		const baseSize = 20_000_000
		base := randomBytes(201, baseSize)
		check("Phase3: AWS SDK PutObject seed destination with related content",
			putObject(ctx, ts.dstClient, "p3-dst", "related/existing.bin", base))

		mid := baseSize / 2
		edited := make([]byte, 0, baseSize+8192)
		edited = append(edited, base[:mid]...)
		edited = append(edited, randomBytes(202, 8192)...)
		edited = append(edited, base[mid:]...)
		check("Phase3: AWS SDK PutObject write source object under replicated prefix",
			putObject(ctx, ts.srcClient, "p3-src", "edited/new.bin", edited))
		check("Phase3: AWS SDK PutObject write an unrelated, brand-new source object",
			putObject(ctx, ts.srcClient, "p3-src", "edited/brand-new.bin", randomBytes(203, 50_000)))

		out, err := runReplicateRecursive(binPath, ts.srcEndpoint, ts.dstEndpoint, "s3://p3-src/edited/", "s3://p3-dst/landed/")
		fmt.Print(out)
		check("Phase3: zeros3 replicate -recursive with destination prepopulation", err)

		logical := parseHumanBytesFromLine(statLine(out, "Logical data:"))
		relayed := parseHumanBytesFromLine(statLine(out, "Payload transferred:"))
		avoided := parseHumanBytesFromLine(statLine(out, "Transfer avoided:"))
		requireTrue("Phase3: strong, honest reuse figure from prepopulated related content (not manufactured)",
			logical > 0 && avoided > 0 && relayed < logical/2,
			fmt.Sprintf("logical=%d relayed=%d avoided=%d", logical, relayed, avoided))
		noteInfo("Phase3 stats: %s | %s | %s | %s",
			statLine(out, "Logical data:"), statLine(out, "Payload transferred:"), statLine(out, "Transfer avoided:"), statLine(out, "Reuse:"))

		keys, err := listAllKeys(ctx, ts.dstClient, "p3-dst")
		check("Phase3: AWS SDK ListObjectsV2 on destination", err)
		want := []string{"landed/brand-new.bin", "landed/new.bin", "related/existing.bin"}
		requireTrue("Phase3: destination holds exactly the expected final objects", fmt.Sprint(keys) == fmt.Sprint(want), fmt.Sprintf("got=%v want=%v", keys, want))

		get, err := ts.dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p3-dst"), Key: aws.String("landed/new.bin")})
		if check1("Phase3: AWS SDK GetObject landed/new.bin", err) {
			got := mustReadAll(get.Body)
			requireTrue("Phase3: byte-for-byte GET equality for landed/new.bin", bytes.Equal(got, edited), "content mismatch")
		}
	}()

	// =========================================================================
	// Phase 4: multi-page source (>1000 lightweight objects).
	// =========================================================================
	func() {
		ts := startTwoServers(binPath, "p4")
		defer ts.stop()
		_, err := ts.srcClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p4-src")})
		check("Phase4: CreateBucket on source", err)
		_, err = ts.dstClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p4-dst")})
		check("Phase4: CreateBucket on destination", err)

		const n = 1500
		for i := 0; i < n; i++ {
			key := fmt.Sprintf("many/%04d.txt", i)
			if err := putObject(ctx, ts.srcClient, "p4-src", key, []byte(key)); err != nil {
				log.Fatalf("Phase4: AWS SDK PutObject %s: %v", key, err)
			}
		}
		pass++
		fmt.Printf("PASS: Phase4: AWS SDK populated %d source objects\n", n)

		out, err := runReplicateRecursive(binPath, ts.srcEndpoint, ts.dstEndpoint, "s3://p4-src/", "s3://p4-dst/")
		fmt.Print(out)
		check("Phase4: zeros3 replicate -recursive across >1000 objects", err)
		requireTrue("Phase4: reports exactly 1500 objects discovered/replicated",
			statInt(out, "Objects discovered:") == n && statInt(out, "Replicated:") == n, out)

		keys, err := listAllKeys(ctx, ts.dstClient, "p4-dst")
		check("Phase4: AWS SDK ListObjectsV2 (itself paginated) on destination", err)
		requireTrue("Phase4: destination holds exactly 1500 keys, no duplicates/missing", len(keys) == n, fmt.Sprintf("got %d keys", len(keys)))
		dedup := make(map[string]bool, len(keys))
		for _, k := range keys {
			dedup[k] = true
		}
		requireTrue("Phase4: no duplicate keys across pages", len(dedup) == n, fmt.Sprintf("got %d unique of %d", len(dedup), n))
	}()

	// =========================================================================
	// Phase 5: non-destructive semantics.
	// =========================================================================
	func() {
		ts := startTwoServers(binPath, "p5")
		defer ts.stop()
		_, err := ts.srcClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p5-src")})
		check("Phase5: CreateBucket on source", err)
		_, err = ts.dstClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p5-dst")})
		check("Phase5: CreateBucket on destination", err)

		destOnly := []byte("destination-only object, never touched")
		check("Phase5: AWS SDK PutObject destination-only object", putObject(ctx, ts.dstClient, "p5-dst", "dest-only.bin", destOnly))
		check("Phase5: AWS SDK PutObject source object a", putObject(ctx, ts.srcClient, "p5-src", "a.bin", []byte("keep me")))
		check("Phase5: AWS SDK PutObject source object b", putObject(ctx, ts.srcClient, "p5-src", "b.bin", []byte("will be deleted from source")))

		out1, err := runReplicateRecursive(binPath, ts.srcEndpoint, ts.dstEndpoint, "s3://p5-src/", "s3://p5-dst/")
		fmt.Print(out1)
		check("Phase5: initial zeros3 replicate -recursive", err)

		get, err := ts.dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p5-dst"), Key: aws.String("dest-only.bin")})
		if check1("Phase5: AWS SDK GetObject on destination-only object after replicate", err) {
			got := mustReadAll(get.Body)
			requireTrue("Phase5: destination-only object survives untouched", bytes.Equal(got, destOnly), "content changed")
		}

		_, err = ts.srcClient.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("p5-src"), Key: aws.String("b.bin")})
		check("Phase5: AWS SDK DeleteObject b.bin from source", err)

		out2, err := runReplicateRecursive(binPath, ts.srcEndpoint, ts.dstEndpoint, "s3://p5-src/", "s3://p5-dst/")
		fmt.Print(out2)
		check("Phase5: rerun zeros3 replicate -recursive after source deletion", err)
		requireTrue("Phase5: rerun discovers only the still-present source object", statInt(out2, "Objects discovered:") == 1, out2)

		get2, err := ts.dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p5-dst"), Key: aws.String("b.bin")})
		if check1("Phase5: AWS SDK GetObject on b.bin's previously-replicated destination copy", err) {
			got2 := mustReadAll(get2.Body)
			requireTrue("Phase5: b.bin's destination copy remains after source deletion (no implicit delete)",
				bytes.Equal(got2, []byte("will be deleted from source")), "content changed or missing")
		}
	}()

	// =========================================================================
	// Phase 6: partial failure -- one object's own source chunk is
	// deliberately corrupted on disk before replication runs (the
	// milestone's own permitted direct-filesystem exception), so its own
	// replicateObject call fails deterministically while unrelated objects
	// in the same run succeed. A timing-based conflict race (like M8A
	// Phase 6's own racing-PutObject technique) has two equally legitimate
	// outcomes depending on who wins; a corrupt/unavailable source chunk
	// is the one partial-failure trigger this harness can force
	// deterministically end-to-end, matching this phase's own required
	// "verify: unrelated objects succeed, failure recorded, overall
	// nonzero" assertions exactly rather than conditionally.
	// =========================================================================
	func() {
		ts := startTwoServers(binPath, "p6")
		defer ts.stop()
		_, err := ts.srcClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p6-src")})
		check("Phase6: CreateBucket on source", err)
		_, err = ts.dstClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p6-dst")})
		check("Phase6: CreateBucket on destination", err)

		check("Phase6: AWS SDK PutObject aaa.bin", putObject(ctx, ts.srcClient, "p6-src", "aaa.bin", randomBytes(601, 40_000)))
		// Kept under ZeroS3's frozen 16KiB CDC v1 minimum chunk size, so it
		// always publishes to exactly one chunk file -- a deterministic
		// target to corrupt directly on disk.
		corruptBody := randomBytes(602, 8000)
		check("Phase6: AWS SDK PutObject corrupt.bin", putObject(ctx, ts.srcClient, "p6-src", "corrupt.bin", corruptBody))
		check("Phase6: AWS SDK PutObject zzz.bin", putObject(ctx, ts.srcClient, "p6-src", "zzz.bin", randomBytes(603, 40_000)))

		sum := sha256.Sum256(corruptBody)
		h := hex.EncodeToString(sum[:])
		chunkPath := filepath.Join(ts.srcDir, "chunks", h[0:2], h[2:4], h)
		check("Phase6: corrupt corrupt.bin's single source chunk directly on disk (milestone-permitted exception)", corruptFile(chunkPath))

		out, runErr := runReplicateRecursive(binPath, ts.srcEndpoint, ts.dstEndpoint, "s3://p6-src/", "s3://p6-dst/")
		fmt.Print(out)

		requireTrue("Phase6: overall command exits non-zero (one object failed)", runErr != nil, "expected non-zero exit")
		requireTrue("Phase6: failure report names corrupt.bin", strings.Contains(out, "corrupt.bin"), out)
		requireTrue("Phase6: reports exactly 3 discovered, 2 replicated, 1 failed",
			statInt(out, "Objects discovered:") == 3 && statInt(out, "Replicated:") == 2 && statInt(out, "Failed:") == 1, out)

		for _, key := range []string{"aaa.bin", "zzz.bin"} {
			get, err := ts.dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p6-dst"), Key: aws.String(key)})
			check("Phase6: AWS SDK GetObject on unrelated succeeded object "+key, err)
			if get != nil {
				get.Body.Close()
			}
		}

		_, err = ts.dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p6-dst"), Key: aws.String("corrupt.bin")})
		requireTrue("Phase6: corrupt.bin was never committed at the destination", err != nil, "expected GetObject to fail (404)")
	}()

	// =========================================================================
	// Phase 7: interruption/resume.
	// =========================================================================
	func() {
		ts := startTwoServers(binPath, "p7")
		defer ts.stop()
		_, err := ts.srcClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p7-src")})
		check("Phase7: CreateBucket on source", err)
		_, err = ts.dstClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p7-dst")})
		check("Phase7: CreateBucket on destination", err)

		bodies := map[string][]byte{
			"ns/one.bin":   randomBytes(701, 9_000_000),
			"ns/two.bin":   randomBytes(702, 9_000_000),
			"ns/three.bin": randomBytes(703, 9_000_000),
		}
		for key, body := range bodies {
			check("Phase7: AWS SDK PutObject "+key, putObject(ctx, ts.srcClient, "p7-src", key, body))
		}

		interruptCmd := replicateRecursiveCmd(binPath, ts.srcEndpoint, ts.dstEndpoint, "s3://p7-src/ns/", "s3://p7-dst/ns/")
		if err := interruptCmd.Start(); err != nil {
			log.Fatalf("Phase7: starting interruptible replicate -recursive: %v", err)
		}
		// Interrupt on observed progress, not a timer: once the first object
		// is committed at the destination the client is provably mid-run, and
		// the next object's 9 MB transfer is far from finishing, so no
		// orphaned request can still be in flight toward the rerun.
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(2 * time.Millisecond) {
			keys, err := listAllKeys(ctx, ts.dstClient, "p7-dst")
			if err == nil && len(keys) > 0 {
				break
			}
			if time.Now().After(deadline) {
				log.Fatalf("Phase7: no object reached the destination before the deadline (last list error: %v)", err)
			}
		}
		killErr := interruptCmd.Process.Kill()
		interruptCmd.Wait()
		check("Phase7: kill zeros3 replicate -recursive mid-run", killErr)

		out, err := runReplicateRecursive(binPath, ts.srcEndpoint, ts.dstEndpoint, "s3://p7-src/ns/", "s3://p7-dst/ns/")
		fmt.Print(out)
		check("Phase7: resumed rerun completes cleanly", err)
		requireTrue("Phase7: resumed rerun reports all 3 objects replicated, zero failed",
			statInt(out, "Replicated:") == 3 && statInt(out, "Failed:") == 0, out)

		for key, body := range bodies {
			dstKey := "ns/" + strings.TrimPrefix(key, "ns/")
			get, err := ts.dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p7-dst"), Key: aws.String(dstKey)})
			if !check1("Phase7: AWS SDK GetObject "+dstKey+" after resume", err) {
				continue
			}
			got := mustReadAll(get.Body)
			requireTrue("Phase7: exact content for "+dstKey+" after interrupted-then-resumed run", bytes.Equal(got, body), "content mismatch after resume")
		}
		noteInfo("Phase7 resumed rerun stats: %s | %s (chunks the killed attempt already landed reduce this further)",
			statLine(out, "Payload transferred:"), statLine(out, "Reuse:"))
	}()

	// =========================================================================
	// Phase 8: restart -- restart destination after a successful run, prove
	// AWS SDK list/GET exact and deep verify clean.
	// =========================================================================
	func() {
		ts := startTwoServers(binPath, "p8")
		defer ts.stop()
		_, err := ts.srcClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p8-src")})
		check("Phase8: CreateBucket on source", err)
		_, err = ts.dstClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p8-dst")})
		check("Phase8: CreateBucket on destination", err)

		bodies := map[string][]byte{
			"x/one.bin": randomBytes(801, 2_000_000),
			"x/two.bin": randomBytes(802, 3_000_000),
		}
		for key, body := range bodies {
			check("Phase8: AWS SDK PutObject "+key, putObject(ctx, ts.srcClient, "p8-src", key, body))
		}
		out, err := runReplicateRecursive(binPath, ts.srcEndpoint, ts.dstEndpoint, "s3://p8-src/", "s3://p8-dst/")
		fmt.Print(out)
		check("Phase8: zeros3 replicate -recursive before restart", err)

		stopZeroS3(ts.dstCmd)
		ts.dstCmd, err = startZeroS3(binPath, ts.dstDir, ts.dstAddr)
		check("Phase8: restart destination zeros3 on the same store directory", err)

		keys, err := listAllKeys(ctx, ts.dstClient, "p8-dst")
		check("Phase8: AWS SDK ListObjectsV2 after restart", err)
		requireTrue("Phase8: destination lists exactly the replicated keys after restart", len(keys) == len(bodies), fmt.Sprintf("keys=%v", keys))
		for key, body := range bodies {
			get, err := ts.dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p8-dst"), Key: aws.String(key)})
			if !check1("Phase8: AWS SDK GetObject "+key+" after restart", err) {
				continue
			}
			got := mustReadAll(get.Body)
			requireTrue("Phase8: exact content for "+key+" after restart", bytes.Equal(got, body), "content mismatch after restart")
		}

		stopZeroS3(ts.dstCmd)
		verifyOut, verifyErr := runVerifyDeep(binPath, ts.dstDir)
		fmt.Println(verifyOut)
		check("Phase8: zeros3 verify -deep on destination after replicate+restart", verifyErr)
		ts.dstCmd, err = startZeroS3(binPath, ts.dstDir, ts.dstAddr)
		check("Phase8: restart destination zeros3 after verify", err)
	}()

	// =========================================================================
	// Phase 9: M8B compatibility -- corrupt one destination chunk after a
	// successful namespace replication, repair it from the source (a
	// healthy M8A/M8B peer for that same content), prove success. Proves
	// the milestone's own "replicated objects are ordinary, repairable
	// objects" requirement.
	// =========================================================================
	func() {
		ts := startTwoServers(binPath, "p9")
		defer ts.stop()
		_, err := ts.srcClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p9-src")})
		check("Phase9: CreateBucket on source", err)
		_, err = ts.dstClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p9-dst")})
		check("Phase9: CreateBucket on destination", err)

		body := randomBytes(901, 3_000_000)
		check("Phase9: AWS SDK PutObject source object", putObject(ctx, ts.srcClient, "p9-src", "repairable.bin", body))
		out, err := runReplicateRecursive(binPath, ts.srcEndpoint, ts.dstEndpoint, "s3://p9-src/", "s3://p9-dst/")
		fmt.Print(out)
		check("Phase9: zeros3 replicate -recursive before corruption", err)

		stopZeroS3(ts.dstCmd)
		chunks, err := chunkFilesUnder(ts.dstDir)
		check("Phase9: discover destination CAS chunk files", err)
		requireTrue("Phase9: destination has at least one chunk file to corrupt", len(chunks) > 0, "no chunk files found")
		if len(chunks) > 0 {
			check("Phase9: corrupt one destination chunk file directly on disk (milestone-permitted exception)", corruptFile(chunks[0]))
		}
		ts.dstCmd, err = startZeroS3(binPath, ts.dstDir, ts.dstAddr)
		check("Phase9: restart destination zeros3 after corrupting a chunk", err)

		preVerify, preErr := runVerifyDeep(binPath, ts.dstDir)
		_ = preVerify
		requireTrue("Phase9: verify -deep detects the corruption before repair", preErr != nil, "expected verify -deep to fail before repair")

		stopZeroS3(ts.dstCmd)
		repairOut, repairErr := runRepair(binPath, ts.dstDir, ts.srcEndpoint)
		fmt.Println(repairOut)
		check("Phase9: zeros3 repair -from the source (a healthy peer for this content)", repairErr)
		ts.dstCmd, err = startZeroS3(binPath, ts.dstDir, ts.dstAddr)
		check("Phase9: restart destination zeros3 after repair", err)

		get, err := ts.dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p9-dst"), Key: aws.String("repairable.bin")})
		if check1("Phase9: AWS SDK GetObject after repair", err) {
			got := mustReadAll(get.Body)
			requireTrue("Phase9: exact content after M8C replication + M8B repair", bytes.Equal(got, body), "content mismatch after repair")
		}
	}()

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed, %d informational =====\n", pass, fail, info)
	if fail > 0 {
		os.Exit(1)
	}
}

// check1 is check's boolean-returning twin, for call sites that need to
// skip dependent assertions when the checked call itself failed.
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
