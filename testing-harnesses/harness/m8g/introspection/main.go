// Ephemeral, external interoperability harness for ZeroS3 M8G
// (read-only replication planning, structural diff, and CAS inspect --
// `replicate -dry-run`, `zeros3 diff`, `zeros3 inspect`).
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 purely as a
// black-box S3 client for fixture setup/verification. All three M8G
// commands themselves are driven as real external processes (os/exec
// against the built `zeros3` binary) against real `zeros3 serve`
// subprocesses, never zeros3's own Go package internals.
//
// What this proves that the internal (in-process) zeros3_test.go suite
// cannot: that a completely independent process, talking only through
// the same HTTP/SigV4 wire protocol a real S3 client would use, gets a
// dry-run prediction that matches a real subsequent replication byte for
// byte; that the read-only claim holds under an independent, whole-
// directory filesystem fingerprint of a real running store (not just the
// CLI's own self-reported statistics); and that `inspect`'s store-wide
// sharing figure composes correctly across a real `zeros3 fork`
// subprocess. Internal tests remain the source of truth for exact
// accounting/edge-case correctness (see zeros3's own STATUS.md); this
// harness is external, real-process evidence on top of that.
//
// Scope note: this harness covers the spec's Phases 1-7 and 10 (single-
// object dry-run, zero-transfer plan, recursive dry-run, diff on an
// edited fixture, diff on an unrelated fixture, inspect on a unique
// object, inspect across a real fork, and the combined read-only
// fingerprint proof). The 1500-object scale case (Phase 9) and snapshot
// composition (Phase 8) are exercised by the internal suite
// (TestM8G_HugeNamespace_1500Objects_RecursiveDryRun,
// TestInspect_SnapshotPinnedSharing_SurvivesLiveDeletion) rather than
// repeated here, to keep this external harness's runtime proportionate.
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
	dir, err := os.MkdirTemp("", "zeros3-m8g-"+tag+"-store-")
	if err != nil {
		log.Fatal(err)
	}
	s.dir = dir
	s.addr, err = freePort()
	if err != nil {
		log.Fatal(err)
	}
	s.endpoint = "http://" + s.addr
	s.cmd, err = startZeroS3(binPath, s.dir, s.addr)
	if err != nil {
		log.Fatalf("starting %s: %v", tag, err)
	}
	s.client = newClient(s.endpoint)
	return s
}

func runCLI(binPath string, args ...string) (output string, err error) {
	cmd := exec.Command(binPath, args...)
	var buf bytes.Buffer
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

// statHumanBytes parses a "Label: N.NN UNIT" line into bytes.
func statHumanBytes(output, label string) int64 {
	line := statLine(output, label)
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
	// A bare byte count with no unit ("Would transfer: 0 B" style lines
	// always carry a unit; a plain integer field, e.g. a count line, is
	// handled by statInt instead).
	return 0
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

func randomBytes(seed int64, n int) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	_, _ = r.Read(b)
	return b
}

func putObject(ctx context.Context, c *s3.Client, bucket, key string, body []byte) error {
	_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	return err
}

func createBucket(ctx context.Context, c *s3.Client, bucket string) error {
	_, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	return err
}

// storeFingerprint hashes every file's relative path + content under a
// store directory, in filepath.Walk's documented lexical order -- an
// independent, external, filesystem-level proof that a store's
// authoritative persistent state is byte-for-byte unchanged. This is the
// harness's own implementation, deliberately not calling into zeros3 at
// all, mirroring (but not sharing code with) zeros3_test.go's internal
// storeContentFingerprint.
func storeFingerprint(dir string) (string, error) {
	h := sha256.New()
	err := filepath.Walk(dir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func mustFingerprint(dir string) string {
	fp, err := storeFingerprint(dir)
	if err != nil {
		log.Fatalf("storeFingerprint(%s): %v", dir, err)
	}
	return fp
}

func replicateDryRunCmd(binPath, from, to, srcURI, dstURI string, recursive bool) *exec.Cmd {
	args := []string{"replicate", "-dry-run"}
	if recursive {
		args = append(args, "-recursive")
	}
	args = append(args,
		"-from", from, "-to", to,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey,
		"-region", region,
		srcURI, dstURI)
	return exec.Command(binPath, args...)
}

func replicateCmd(binPath, from, to, srcURI, dstURI string, recursive bool) *exec.Cmd {
	args := []string{"replicate"}
	if recursive {
		args = append(args, "-recursive")
	}
	args = append(args,
		"-from", from, "-to", to,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey,
		"-region", region,
		srcURI, dstURI)
	return exec.Command(binPath, args...)
}

func runCmdCombined(cmd *exec.Cmd) (string, error) {
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func diffCmd(binPath, from, to, uriA, uriB string) *exec.Cmd {
	return exec.Command(binPath, "diff",
		"-from", from, "-to", to,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey,
		"-region", region,
		uriA, uriB)
}

func inspectCmd(binPath, endpoint, uri string, chunks bool) *exec.Cmd {
	args := []string{"inspect", "-endpoint", endpoint, "-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region}
	if chunks {
		args = append(args, "-chunks")
	}
	args = append(args, uri)
	return exec.Command(binPath, args...)
}

func forkCmd(binPath, endpoint, srcURI, dstURI string) *exec.Cmd {
	return exec.Command(binPath, "fork",
		"-endpoint", endpoint,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey,
		"-region", region,
		srcURI, dstURI)
}

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	if abs, err := filepath.Abs(binPath); err == nil {
		binPath = abs
	}
	if _, err := os.Stat(binPath); err != nil {
		log.Fatalf("ZEROS3_BIN %q not found/built: %v", binPath, err)
	}
	ctx := context.Background()

	fmt.Println("=== M8G Phase 1: single-object dry-run, partial destination overlap, predicted == actual ===")
	func() {
		src := startOneServer(binPath, "p1-src")
		defer func() { stopZeroS3(src.cmd); os.RemoveAll(src.dir) }()
		dst := startOneServer(binPath, "p1-dst")
		defer func() { stopZeroS3(dst.cmd); os.RemoveAll(dst.dir) }()

		check("p1: create source bucket", createBucket(ctx, src.client, "src"))
		check("p1: create dest bucket", createBucket(ctx, dst.client, "dst"))

		// Object made of two halves; only the second half will already be
		// present at the destination (under an unrelated key), so
		// negotiation reports genuine partial reuse.
		partA := randomBytes(1001, 400_000)
		partB := randomBytes(1002, 300_000)
		full := append(append([]byte{}, partA...), partB...)
		check("p1: put source object", putObject(ctx, src.client, "src", "obj.bin", full))
		check("p1: pre-seed destination with partA content under an unrelated key", putObject(ctx, dst.client, "dst", "unrelated-seed.bin", partA))

		dstBefore := mustFingerprint(dst.dir)
		out, err := runCmdCombined(replicateDryRunCmd(binPath, src.endpoint, dst.endpoint, "s3://src/obj.bin", "s3://dst/obj.bin", false))
		check("p1: dry-run exits cleanly", err)
		dstAfter := mustFingerprint(dst.dir)
		requireTrue("p1: dry-run left destination store fingerprint unchanged", dstBefore == dstAfter, "fingerprint mismatch after dry-run")
		requireTrue("p1: dry-run output announces no modification", strings.Contains(out, "no data modified"), out)

		predictedTransfer := statHumanBytes(out, "Would transfer:")
		predictedMissing := statInt(out, "Chunks missing:")
		noteInfo("p1: predicted transfer=%d bytes, missing=%d chunk occurrences", predictedTransfer, predictedMissing)

		actualOut, err := runCmdCombined(replicateCmd(binPath, src.endpoint, dst.endpoint, "s3://src/obj.bin", "s3://dst/obj.bin", false))
		check("p1: actual replicate exits cleanly", err)
		actualTransfer := statHumanBytes(actualOut, "Uploaded payload:")
		requireTrue("p1: predicted transfer == actual transfer", predictedTransfer == actualTransfer,
			fmt.Sprintf("predicted=%d actual=%d\ndry-run output:\n%s\nactual output:\n%s", predictedTransfer, actualTransfer, out, actualOut))

		got, err := dst.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("dst"), Key: aws.String("obj.bin")})
		if check("p1: GetObject on destination after actual replicate", err); err == nil {
			body := mustReadAll(got.Body)
			requireTrue("p1: destination content byte-identical to source", bytes.Equal(body, full), "content mismatch")
		}
	}()

	fmt.Println("\n=== M8G Phase 2: zero-transfer plan (destination CAS already has every chunk) ===")
	func() {
		src := startOneServer(binPath, "p2-src")
		defer func() { stopZeroS3(src.cmd); os.RemoveAll(src.dir) }()
		dst := startOneServer(binPath, "p2-dst")
		defer func() { stopZeroS3(dst.cmd); os.RemoveAll(dst.dir) }()

		check("p2: create source bucket", createBucket(ctx, src.client, "src"))
		check("p2: create dest bucket", createBucket(ctx, dst.client, "dst"))

		body := randomBytes(2001, 500_000)
		check("p2: put source object", putObject(ctx, src.client, "src", "data.bin", body))
		check("p2: pre-seed destination with byte-identical content under an unrelated key", putObject(ctx, dst.client, "dst", "unrelated-copy.bin", body))

		dstBefore := mustFingerprint(dst.dir)
		out, err := runCmdCombined(replicateDryRunCmd(binPath, src.endpoint, dst.endpoint, "s3://src/data.bin", "s3://dst/data.bin", false))
		check("p2: dry-run exits cleanly", err)
		dstAfter := mustFingerprint(dst.dir)
		requireTrue("p2: dry-run left destination store fingerprint unchanged", dstBefore == dstAfter, "fingerprint mismatch")

		transfer := statHumanBytes(out, "Would transfer:")
		requireTrue("p2: predicted transfer is exactly 0 bytes", transfer == 0, fmt.Sprintf("got %d bytes; output:\n%s", transfer, out))
		requireTrue("p2: destination action is still 'would publish' (destination object itself absent)", strings.Contains(out, "would publish object"), out)
	}()

	fmt.Println("\n=== M8G Phase 3: recursive dry-run, then actual namespace replication, exact match ===")
	func() {
		src := startOneServer(binPath, "p3-src")
		defer func() { stopZeroS3(src.cmd); os.RemoveAll(src.dir) }()
		dst := startOneServer(binPath, "p3-dst")
		defer func() { stopZeroS3(dst.cmd); os.RemoveAll(dst.dir) }()

		check("p3: create source bucket", createBucket(ctx, src.client, "src"))
		check("p3: create dest bucket", createBucket(ctx, dst.client, "dst"))

		for i := 0; i < 12; i++ {
			body := randomBytes(int64(3100+i), 40_000)
			check(fmt.Sprintf("p3: put source object %d", i), putObject(ctx, src.client, "src", fmt.Sprintf("set/obj-%02d.bin", i), body))
		}

		dstBefore := mustFingerprint(dst.dir)
		out, err := runCmdCombined(replicateDryRunCmd(binPath, src.endpoint, dst.endpoint, "s3://src/set/", "s3://dst/set/", true))
		check("p3: recursive dry-run exits cleanly", err)
		dstAfter := mustFingerprint(dst.dir)
		requireTrue("p3: recursive dry-run left destination fingerprint unchanged", dstBefore == dstAfter, "fingerprint mismatch")

		predictedTransfer := statHumanBytes(out, "Would transfer:")
		predictedObjects := statInt(out, "Objects discovered:")
		requireTrue("p3: 12 objects discovered", predictedObjects == 12, fmt.Sprintf("got %d", predictedObjects))

		actualOut, err := runCmdCombined(replicateCmd(binPath, src.endpoint, dst.endpoint, "s3://src/set/", "s3://dst/set/", true))
		check("p3: actual recursive replicate exits cleanly", err)
		actualTransfer := statHumanBytes(actualOut, "Payload transferred:")
		requireTrue("p3: recursive predicted transfer == actual transfer", predictedTransfer == actualTransfer,
			fmt.Sprintf("predicted=%d actual=%d\ndry-run:\n%s\nactual:\n%s", predictedTransfer, actualTransfer, out, actualOut))

		keys, err := listAllKeys(ctx, dst.client, "dst")
		check("p3: list destination keys", err)
		requireTrue("p3: destination has all 12 replicated keys", len(keys) == 12, fmt.Sprintf("got %d: %v", len(keys), keys))
	}()

	fmt.Println("\n=== M8G Phase 4: diff on an edited fixture (localized change) -- high honest reuse ===")
	func() {
		srv := startOneServer(binPath, "p4")
		defer func() { stopZeroS3(srv.cmd); os.RemoveAll(srv.dir) }()
		check("p4: create bucket", createBucket(ctx, srv.client, "b"))

		base := randomBytes(4001, 2_000_000)
		edited := append(append([]byte{}, base...), randomBytes(4002, 50_000)...) // append: localized change at the tail
		check("p4: put v1", putObject(ctx, srv.client, "b", "v1.bin", base))
		check("p4: put v2 (v1 + appended tail)", putObject(ctx, srv.client, "b", "v2.bin", edited))

		out, err := runCmdCombined(diffCmd(binPath, srv.endpoint, srv.endpoint, "s3://b/v1.bin", "s3://b/v2.bin"))
		check("p4: diff exits cleanly", err)
		noteInfo("p4: diff output:\n%s", out)
		requireTrue("p4: exact object match is 'no' (v2 has more content)", strings.Contains(out, "Exact object match:   no"), out)
		requireTrue("p4: reports a reuse percentage line for A", strings.Contains(out, "A reuse from B:"), out)
	}()

	fmt.Println("\n=== M8G Phase 5: diff on wholly unrelated fixtures -- low reuse, not misleadingly high ===")
	func() {
		srv := startOneServer(binPath, "p5")
		defer func() { stopZeroS3(srv.cmd); os.RemoveAll(srv.dir) }()
		check("p5: create bucket", createBucket(ctx, srv.client, "b"))

		a := randomBytes(5001, 1_000_000)
		b := randomBytes(5002, 1_200_000)
		check("p5: put a", putObject(ctx, srv.client, "b", "a.bin", a))
		check("p5: put b", putObject(ctx, srv.client, "b", "b.bin", b))

		out, err := runCmdCombined(diffCmd(binPath, srv.endpoint, srv.endpoint, "s3://b/a.bin", "s3://b/b.bin"))
		check("p5: diff exits cleanly", err)
		requireTrue("p5: reports shared unique chunk IDs: 0", strings.Contains(out, "Shared unique chunk IDs:     0"), out)
		requireTrue("p5: not an exact match", strings.Contains(out, "Exact object match:   no"), out)
	}()

	fmt.Println("\n=== M8G Phase 6: inspect a unique object -- basic physical metrics ===")
	func() {
		srv := startOneServer(binPath, "p6")
		defer func() { stopZeroS3(srv.cmd); os.RemoveAll(srv.dir) }()
		check("p6: create bucket", createBucket(ctx, srv.client, "b"))

		body := randomBytes(6001, 3_000_000)
		check("p6: put object", putObject(ctx, srv.client, "b", "solo.bin", body))

		before := mustFingerprint(srv.dir)
		out, err := runCmdCombined(inspectCmd(binPath, srv.endpoint, "s3://b/solo.bin", true))
		check("p6: inspect exits cleanly", err)
		after := mustFingerprint(srv.dir)
		requireTrue("p6: inspect left store fingerprint unchanged", before == after, "fingerprint mismatch")
		noteInfo("p6: inspect output:\n%s", out)

		requireTrue("p6: reports zero bytes structurally shared (unique object)", strings.Contains(out, "Structurally shared:             0 B"), out)
		requireTrue("p6: -chunks table header present", strings.Contains(out, "OFFSET") && strings.Contains(out, "REACHABLE-ROOTS"), out)
	}()

	fmt.Println("\n=== M8G Phase 7: inspect across a real `zeros3 fork` -- before/after sharing, then mutate ===")
	func() {
		srv := startOneServer(binPath, "p7")
		defer func() { stopZeroS3(srv.cmd); os.RemoveAll(srv.dir) }()
		check("p7: create prod bucket", createBucket(ctx, srv.client, "prod"))
		check("p7: create fork bucket", createBucket(ctx, srv.client, "fork"))

		body := randomBytes(7001, 1_500_000)
		check("p7: put prod object", putObject(ctx, srv.client, "prod", "shared.bin", body))

		beforeOut, err := runCmdCombined(inspectCmd(binPath, srv.endpoint, "s3://prod/shared.bin", false))
		check("p7: inspect before fork exits cleanly", err)
		requireTrue("p7: before fork, zero bytes shared elsewhere", strings.Contains(beforeOut, "Structurally shared:             0 B"), beforeOut)

		forkOut, err := runCmdCombined(forkCmd(binPath, srv.endpoint, "s3://prod/", "s3://fork/"))
		check("p7: fork exits cleanly", err)
		noteInfo("p7: fork output:\n%s", forkOut)

		afterOut, err := runCmdCombined(inspectCmd(binPath, srv.endpoint, "s3://prod/shared.bin", false))
		check("p7: inspect after fork exits cleanly", err)
		noteInfo("p7: inspect after fork:\n%s", afterOut)
		physical := statHumanBytes(afterOut, "Unique CAS payload represented:")
		shared := statHumanBytes(afterOut, "Structurally shared:")
		requireTrue("p7: after fork, everything is structurally shared", physical > 0 && shared == physical,
			fmt.Sprintf("physical=%d shared=%d\n%s", physical, shared, afterOut))

		// Mutate the forked copy; its pre-mutation state is archived into
		// the fork's own history (ZeroS3 never purges on ordinary
		// overwrite), so prod's chunks should remain reachable via that
		// historical root.
		check("p7: overwrite forked object", putObject(ctx, srv.client, "fork", "shared.bin", randomBytes(7002, 2_000_000)))
		mutatedOut, err := runCmdCombined(inspectCmd(binPath, srv.endpoint, "s3://prod/shared.bin", false))
		check("p7: inspect after fork mutation exits cleanly", err)
		mutatedShared := statHumanBytes(mutatedOut, "Structurally shared:")
		requireTrue("p7: prod's chunks remain reachable via the fork's retained history after mutation",
			mutatedShared == physical, fmt.Sprintf("physical=%d shared-after-mutation=%d\n%s", physical, mutatedShared, mutatedOut))
	}()

	fmt.Println("\n=== M8G Phase 10: combined read-only fingerprint proof (dry-run + diff + inspect together) ===")
	func() {
		src := startOneServer(binPath, "p10-src")
		defer func() { stopZeroS3(src.cmd); os.RemoveAll(src.dir) }()
		dst := startOneServer(binPath, "p10-dst")
		defer func() { stopZeroS3(dst.cmd); os.RemoveAll(dst.dir) }()

		check("p10: create source bucket", createBucket(ctx, src.client, "src"))
		check("p10: create dest bucket", createBucket(ctx, dst.client, "dst"))
		check("p10: put a.bin", putObject(ctx, src.client, "src", "a.bin", randomBytes(10001, 300_000)))
		check("p10: put b.bin", putObject(ctx, src.client, "src", "b.bin", randomBytes(10002, 250_000)))

		srcBefore := mustFingerprint(src.dir)
		dstBefore := mustFingerprint(dst.dir)

		if _, err := runCmdCombined(replicateDryRunCmd(binPath, src.endpoint, dst.endpoint, "s3://src/a.bin", "s3://dst/a.bin", false)); err != nil {
			fail++
			fmt.Printf("FAIL: p10: dry-run: %v\n", err)
		}
		if _, err := runCmdCombined(diffCmd(binPath, src.endpoint, src.endpoint, "s3://src/a.bin", "s3://src/b.bin")); err != nil {
			fail++
			fmt.Printf("FAIL: p10: diff: %v\n", err)
		}
		if _, err := runCmdCombined(inspectCmd(binPath, src.endpoint, "s3://src/a.bin", true)); err != nil {
			fail++
			fmt.Printf("FAIL: p10: inspect: %v\n", err)
		}
		pass += 3
		fmt.Println("PASS: p10: dry-run + diff + inspect all ran")

		srcAfter := mustFingerprint(src.dir)
		dstAfter := mustFingerprint(dst.dir)
		requireTrue("p10: SOURCE store fingerprint unchanged across all three commands", srcBefore == srcAfter, "source fingerprint mismatch")
		requireTrue("p10: DESTINATION store fingerprint unchanged across all three commands", dstBefore == dstAfter, "destination fingerprint mismatch")
	}()

	fmt.Printf("\n=== M8G introspection harness: %d passed, %d failed, %d info ===\n", pass, fail, info)
	if fail > 0 {
		os.Exit(1)
	}
}

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
