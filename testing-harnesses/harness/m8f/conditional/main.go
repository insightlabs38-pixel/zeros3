// Ephemeral, external interoperability harness for ZeroS3 M8F-A (atomic S3
// conditional PutObject: If-None-Match: "*" and If-Match).
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 purely as a
// black-box S3 client. It never calls into ZeroS3's Go package internals
// and never populates the store directly: every write in every phase goes
// through the real AWS SDK's ordinary s3.Client.PutObject, using its native
// IfNoneMatch/IfMatch fields (present in the pinned SDK version -- these
// are the same request headers a real S3 conditional-write client sends).
//
// What this proves that the internal (in-process, deterministic-hook-based)
// zeros3_test.go suite cannot: that a completely independent, real AWS SDK
// client observes exactly the create-if-absent / compare-and-swap semantics
// M8F-A promises against a real running zeros3 server over the real HTTP
// wire protocol, that concurrent real SDK clients racing the same
// precondition still produce exactly one winner, that a process restart
// does not change who won, and that a failed conditional write's
// speculative CAS payload is safely collected by a real `zeros3 gc`
// subprocess. Internal tests remain the source of truth for exact
// commit-point/locking correctness (see zeros3's own STATUS.md); this
// harness is external, real-process, real-SDK evidence on top of that, not
// a replacement for it.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
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

func check1[T any](name string, v T, err error) T {
	if err != nil {
		fail++
		fmt.Printf("FAIL: %s: %v\n", name, err)
		var zero T
		return zero
	}
	pass++
	fmt.Printf("PASS: %s\n", name)
	return v
}

// check1err discards a call's non-error return value, keeping only its
// error -- lets a multi-return SDK call (which Go requires to appear alone,
// with no sibling arguments, when spread across a function's parameters) be
// passed straight into check's plain (name string, err error) signature.
func check1err[T any](_ T, err error) error { return err }

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

// errCodeAndStatus unwraps an AWS SDK error into the S3 error Code
// (e.g. "PreconditionFailed") and the HTTP status code the response
// actually carried, via smithy-go's own error types -- the same library
// the AWS SDK itself is built on, not string-matching err.Error().
func errCodeAndStatus(err error) (code string, status int) {
	if err == nil {
		return "", 0
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code = apiErr.ErrorCode()
	}
	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) {
		status = respErr.HTTPStatusCode()
	}
	return code, status
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
	dir, err := os.MkdirTemp("", "zeros3-m8f-"+tag+"-store-")
	if err != nil {
		log.Fatal(err)
	}
	s.dir = dir
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
	if err != nil {
		return err
	}
	s.client = newClient(s.endpoint)
	return nil
}

func (s *oneServer) stop() {
	stopZeroS3(s.cmd)
}

func runCLI(binPath string, args ...string) (output string, err error) {
	cmd := exec.Command(binPath, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

func runGCDryRun(binPath, storeDir string) (output string, err error) {
	return runCLI(binPath, "gc", "-store", storeDir)
}

func runGCApply(binPath, storeDir string) (output string, err error) {
	return runCLI(binPath, "gc", "-store", storeDir, "-apply")
}

func runVerifyDeep(binPath, storeDir string) (output string, err error) {
	return runCLI(binPath, "verify", "-store", storeDir, "-deep")
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

// putIfNoneMatchStar issues a real AWS SDK PutObject with
// IfNoneMatch: "*" -- create-if-absent.
func putIfNoneMatchStar(ctx context.Context, c *s3.Client, bucket, key string, body []byte) (*s3.PutObjectOutput, error) {
	return c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body),
		IfNoneMatch: aws.String("*"),
	})
}

// putIfMatch issues a real AWS SDK PutObject with IfMatch: etag -- a
// compare-and-swap update. etag should be the exact, already-quoted ETag
// string a prior HEAD/PUT/GetObjectOutput reported (the AWS SDK's own
// field already carries the quotes real S3 clients send).
func putIfMatch(ctx context.Context, c *s3.Client, bucket, key string, body []byte, etag string) (*s3.PutObjectOutput, error) {
	return c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body),
		IfMatch: aws.String(etag),
	})
}

func headETag(ctx context.Context, c *s3.Client, bucket, key string) (string, error) {
	out, err := c.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return "", err
	}
	return aws.ToString(out.ETag), nil
}

func chunkFilesUnder(storeDir string) ([]string, error) {
	var files []string
	root := filepath.Join(storeDir, "chunks")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
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

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	ctx := context.Background()

	// =========================================================================
	// Phase 1: create-only (If-None-Match: "*"). First PUT against an
	// absent key succeeds; a repeat against the now-existing key fails
	// with 412 PreconditionFailed; the first bytes remain intact.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p1")
		defer s.stop()
		check("Phase1: CreateBucket", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p1")})))

		body1 := []byte("phase1-first-body")
		_, err := putIfNoneMatchStar(ctx, s.client, "p1", "key", body1)
		check("Phase1: PutObject If-None-Match:* against an absent key succeeds", err)

		_, err = putIfNoneMatchStar(ctx, s.client, "p1", "key", []byte("phase1-second-body-must-be-rejected"))
		requireTrue("Phase1: repeat PutObject If-None-Match:* fails", err != nil, "expected an error")
		if err != nil {
			code, status := errCodeAndStatus(err)
			requireTrue("Phase1: repeat rejected with HTTP 412", status == 412, fmt.Sprintf("code=%q status=%d", code, status))
			requireTrue("Phase1: repeat rejected with error code PreconditionFailed", code == "PreconditionFailed", fmt.Sprintf("code=%q", code))
		}

		get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p1"), Key: aws.String("key")})
		if check1("Phase1: GetObject after rejected repeat", get, err) != nil {
			got := mustReadAll(get.Body)
			requireTrue("Phase1: first bytes remain intact", bytes.Equal(got, body1), fmt.Sprintf("got %d bytes", len(got)))
		}
	}()

	// =========================================================================
	// Phase 2: compare-and-swap update (If-Match). PUT v1, HEAD -> ETag A;
	// PUT v2 with If-Match: A succeeds, HEAD -> ETag B; PUT v3 with
	// If-Match: A (now stale) fails with 412; GET still returns v2.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p2")
		defer s.stop()
		check("Phase2: CreateBucket", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p2")})))

		_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("p2"), Key: aws.String("key"), Body: bytes.NewReader([]byte("v1"))})
		check("Phase2: PUT v1 (unconditional)", err)
		etagA, err := headETag(ctx, s.client, "p2", "key")
		check("Phase2: HEAD after v1", err)
		noteInfo("Phase2: ETag A = %s", etagA)

		_, err = putIfMatch(ctx, s.client, "p2", "key", []byte("v2"), etagA)
		check("Phase2: PUT v2 with If-Match: A succeeds", err)
		etagB, err := headETag(ctx, s.client, "p2", "key")
		check("Phase2: HEAD after v2", err)
		requireTrue("Phase2: ETag changed after CAS update", etagB != etagA, fmt.Sprintf("A=%s B=%s", etagA, etagB))

		_, err = putIfMatch(ctx, s.client, "p2", "key", []byte("v3-must-be-rejected"), etagA)
		requireTrue("Phase2: PUT v3 with stale If-Match: A fails", err != nil, "expected an error")
		if err != nil {
			code, status := errCodeAndStatus(err)
			requireTrue("Phase2: stale CAS rejected with HTTP 412", status == 412, fmt.Sprintf("code=%q status=%d", code, status))
			requireTrue("Phase2: stale CAS rejected with error code PreconditionFailed", code == "PreconditionFailed", fmt.Sprintf("code=%q", code))
		}

		get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p2"), Key: aws.String("key")})
		if check1("Phase2: GetObject after rejected stale CAS", get, err) != nil {
			got := mustReadAll(get.Body)
			requireTrue("Phase2: GET returns v2", string(got) == "v2", fmt.Sprintf("got %q", got))
		}
	}()

	// =========================================================================
	// Phase 3: concurrent create-only. N real AWS SDK clients race
	// If-None-Match: "*" writes with distinct bodies against the same
	// absent key. Exactly 1 must succeed and N-1 must fail with 412; GET
	// must return exactly the winning body; a restart must not change the
	// winner.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p3")
		defer s.stop()
		check("Phase3: CreateBucket", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p3")})))

		const n = 12
		bodies := make([][]byte, n)
		for i := range bodies {
			bodies[i] = randomBytes(int64(1000+i), 4096+i)
		}
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = putIfNoneMatchStar(ctx, s.client, "p3", "key", bodies[i])
			}(i)
		}
		wg.Wait()

		successes, failures, winner := 0, 0, -1
		for i, err := range errs {
			if err == nil {
				successes++
				winner = i
				continue
			}
			code, status := errCodeAndStatus(err)
			if code == "PreconditionFailed" && status == 412 {
				failures++
			} else {
				fail++
				fmt.Printf("FAIL: Phase3: writer %d got an unexpected error: %v (code=%q status=%d)\n", i, err, code, status)
			}
		}
		requireTrue("Phase3: exactly 1 success among N concurrent create-only writers", successes == 1, fmt.Sprintf("successes=%d", successes))
		requireTrue("Phase3: exactly N-1 precondition failures", failures == n-1, fmt.Sprintf("failures=%d want=%d", failures, n-1))

		if winner >= 0 {
			get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p3"), Key: aws.String("key")})
			if check1("Phase3: GetObject after concurrent create-only race", get, err) != nil {
				got := mustReadAll(get.Body)
				requireTrue("Phase3: GET returns exactly the winning body", bytes.Equal(got, bodies[winner]), "content does not match the declared winner")
			}
		}

		check("Phase3: restart zeros3", s.restart())
		get2, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p3"), Key: aws.String("key")})
		if check1("Phase3: GetObject after restart", get2, err) != nil && winner >= 0 {
			got2 := mustReadAll(get2.Body)
			requireTrue("Phase3: same winner remains after restart", bytes.Equal(got2, bodies[winner]), "winner changed across restart")
		}
	}()

	// =========================================================================
	// Phase 4: concurrent CAS update. Start from one committed ETag A; N
	// real AWS SDK clients race PutObject If-Match: A with distinct
	// bodies. Exactly 1 must succeed (no lost update) and N-1 must fail
	// with 412; a restart must not change the winner.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p4")
		defer s.stop()
		check("Phase4: CreateBucket", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p4")})))
		_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("p4"), Key: aws.String("key"), Body: bytes.NewReader([]byte("v0"))})
		check("Phase4: PUT v0 (unconditional)", err)
		etagA, err := headETag(ctx, s.client, "p4", "key")
		check("Phase4: HEAD after v0", err)

		const n = 12
		bodies := make([][]byte, n)
		for i := range bodies {
			bodies[i] = randomBytes(int64(2000+i), 4096+i)
		}
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = putIfMatch(ctx, s.client, "p4", "key", bodies[i], etagA)
			}(i)
		}
		wg.Wait()

		successes, failures, winner := 0, 0, -1
		for i, err := range errs {
			if err == nil {
				successes++
				winner = i
				continue
			}
			code, status := errCodeAndStatus(err)
			if code == "PreconditionFailed" && status == 412 {
				failures++
			} else {
				fail++
				fmt.Printf("FAIL: Phase4: writer %d got an unexpected error: %v (code=%q status=%d)\n", i, err, code, status)
			}
		}
		requireTrue("Phase4: exactly 1 success among N concurrent same-ETag CAS writers", successes == 1, fmt.Sprintf("successes=%d", successes))
		requireTrue("Phase4: exactly N-1 precondition failures (no lost update)", failures == n-1, fmt.Sprintf("failures=%d want=%d", failures, n-1))

		if winner >= 0 {
			get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p4"), Key: aws.String("key")})
			if check1("Phase4: GetObject after concurrent CAS race", get, err) != nil {
				got := mustReadAll(get.Body)
				requireTrue("Phase4: GET returns exactly the winning body", bytes.Equal(got, bodies[winner]), "content does not match the declared winner")
			}
		}

		check("Phase4: restart zeros3", s.restart())
		get2, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p4"), Key: aws.String("key")})
		if check1("Phase4: GetObject after restart", get2, err) != nil && winner >= 0 {
			got2 := mustReadAll(get2.Body)
			requireTrue("Phase4: same winner remains after restart", bytes.Equal(got2, bodies[winner]), "winner changed across restart")
		}
	}()

	// =========================================================================
	// Phase 5: GC of a failed conditional write's speculative CAS payload.
	// A losing If-Match writer's large, distinct body has already been
	// CDC-chunked and published into CAS before the commit-point
	// precondition rejects it (see zeros3.go section 10a). That payload
	// must never become visible, and `zeros3 gc -apply` must be able to
	// reclaim it while the surviving object and the store as a whole
	// remain fully valid.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p5")
		check("Phase5: CreateBucket", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p5")})))
		kept := randomBytes(3001, 8192)
		_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("p5"), Key: aws.String("kept"), Body: bytes.NewReader(kept)})
		check("Phase5: PUT the object that must survive", err)

		// A large, distinct body -- guaranteed to publish its own CAS
		// chunks distinct from `kept` -- with a deliberately wrong If-Match
		// so the commit is guaranteed to be rejected after the body has
		// already been chunked and written to CAS.
		loserBody := randomBytes(3002, 2*1024*1024)
		_, err = putIfMatch(ctx, s.client, "p5", "kept", loserBody, `"0000000000000000000000000000000000000000000000000000000000000000"`)
		requireTrue("Phase5: doomed If-Match write is rejected", err != nil, "expected an error")
		if err != nil {
			code, status := errCodeAndStatus(err)
			requireTrue("Phase5: doomed write rejected with HTTP 412", status == 412, fmt.Sprintf("code=%q status=%d", code, status))
		}

		get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p5"), Key: aws.String("kept")})
		if check1("Phase5: GetObject after doomed write", get, err) != nil {
			got := mustReadAll(get.Body)
			requireTrue("Phase5: kept object unchanged after doomed write", bytes.Equal(got, kept), "content mutated by a failed conditional write")
		}

		beforeFiles, err := chunkFilesUnder(s.dir)
		check("Phase5: discover CAS chunk files before GC", err)

		stopZeroS3(s.cmd)
		dryOut, err := runGCDryRun(binPath, s.dir)
		fmt.Print(dryOut)
		check("Phase5: zeros3 gc (dry run) sees the healthy live set", err)
		requireTrue("Phase5: gc dry run reports the store as healthy (ok=true)", strings.Contains(dryOut, "ok=true"), dryOut)
		requireTrue("Phase5: gc dry run reports unreachable chunks from the failed write", !strings.Contains(dryOut, "0 unreachable"), dryOut)

		applyOut, err := runGCApply(binPath, s.dir)
		fmt.Print(applyOut)
		check("Phase5: zeros3 gc -apply reclaims the failed write's payload", err)
		requireTrue("Phase5: gc -apply reports at least 1 chunk deleted", !strings.Contains(applyOut, "0 chunks"), applyOut)

		afterFiles, err := chunkFilesUnder(s.dir)
		check("Phase5: discover CAS chunk files after GC", err)
		requireTrue("Phase5: fewer chunk files remain after GC", len(afterFiles) < len(beforeFiles),
			fmt.Sprintf("before=%d after=%d", len(beforeFiles), len(afterFiles)))

		verifyOut, err := runVerifyDeep(binPath, s.dir)
		fmt.Print(verifyOut)
		check("Phase5: zeros3 verify -deep after GC", err)

		s.cmd, err = startZeroS3(binPath, s.dir, s.addr)
		check("Phase5: restart zeros3 after GC", err)
		s.client = newClient(s.endpoint)
		get3, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p5"), Key: aws.String("kept")})
		if check1("Phase5: GetObject after GC+restart", get3, err) != nil {
			got3 := mustReadAll(get3.Body)
			requireTrue("Phase5: kept object still exact after GC+restart", bytes.Equal(got3, kept), "content mismatch after GC")
		}
		s.stop()
	}()

	// =========================================================================
	// Phase 6: conditional GET/HEAD (M8F-B). Real AWS SDK GetObject/
	// HeadObject calls using their native IfMatch/IfNoneMatch fields
	// against a real zeros3 server: matching If-Match succeeds normally,
	// mismatching If-Match fails with 412, a matching If-None-Match comes
	// back as 304 Not Modified (with no body), and a mismatching
	// If-None-Match serves normally.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p6")
		defer s.stop()
		check("Phase6: CreateBucket", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p6")})))
		body := randomBytes(6001, 4096)
		_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("p6"), Key: aws.String("key"), Body: bytes.NewReader(body)})
		check("Phase6: PUT the object under test", err)
		etag, err := headETag(ctx, s.client, "p6", "key")
		check("Phase6: HEAD to observe the current ETag", err)

		// GET If-Match match -> normal 200, exact bytes.
		get1, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p6"), Key: aws.String("key"), IfMatch: aws.String(etag)})
		if check1("Phase6: GetObject If-Match (match) succeeds", get1, err) != nil {
			got := mustReadAll(get1.Body)
			requireTrue("Phase6: GET If-Match (match) returns exact bytes", bytes.Equal(got, body), "content mismatch")
		}

		// GET If-Match mismatch -> 412.
		_, err = s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p6"), Key: aws.String("key"), IfMatch: aws.String(`"0000000000000000000000000000000000000000000000000000000000000000"`)})
		requireTrue("Phase6: GetObject If-Match (mismatch) fails", err != nil, "expected an error")
		if err != nil {
			code, status := errCodeAndStatus(err)
			requireTrue("Phase6: GET If-Match (mismatch) rejected with HTTP 412", status == 412, fmt.Sprintf("code=%q status=%d", code, status))
		}

		// HEAD If-Match match/mismatch mirror GET.
		_, err = s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("p6"), Key: aws.String("key"), IfMatch: aws.String(etag)})
		check("Phase6: HeadObject If-Match (match) succeeds", err)
		_, err = s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("p6"), Key: aws.String("key"), IfMatch: aws.String(`"0000000000000000000000000000000000000000000000000000000000000000"`)})
		requireTrue("Phase6: HeadObject If-Match (mismatch) fails", err != nil, "expected an error")
		if err != nil {
			code, status := errCodeAndStatus(err)
			requireTrue("Phase6: HEAD If-Match (mismatch) rejected with HTTP 412", status == 412, fmt.Sprintf("code=%q status=%d", code, status))
		}

		// GET If-None-Match match -> 304, no body.
		_, err = s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p6"), Key: aws.String("key"), IfNoneMatch: aws.String(etag)})
		requireTrue("Phase6: GetObject If-None-Match (match) is not a plain success", err != nil, "expected a 304 (surfaced as an SDK error, since GetObject models only 2xx)")
		if err != nil {
			_, status := errCodeAndStatus(err)
			requireTrue("Phase6: GET If-None-Match (match) returns HTTP 304", status == 304, fmt.Sprintf("status=%d", status))
		}

		// GET If-None-Match mismatch -> normal 200.
		get2, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p6"), Key: aws.String("key"), IfNoneMatch: aws.String(`"0000000000000000000000000000000000000000000000000000000000000000"`)})
		if check1("Phase6: GetObject If-None-Match (mismatch) succeeds", get2, err) != nil {
			got := mustReadAll(get2.Body)
			requireTrue("Phase6: GET If-None-Match (mismatch) returns exact bytes", bytes.Equal(got, body), "content mismatch")
		}

		// Range interaction: a failed If-Match must short-circuit Range
		// processing, not silently serve a 206.
		_, err = s.client.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String("p6"), Key: aws.String("key"),
			IfMatch: aws.String(`"0000000000000000000000000000000000000000000000000000000000000000"`),
			Range:   aws.String("bytes=0-9"),
		})
		requireTrue("Phase6: failed If-Match short-circuits a Range GET", err != nil, "expected an error")
		if err != nil {
			_, status := errCodeAndStatus(err)
			requireTrue("Phase6: failed If-Match + Range still reports 412 (not 206/416)", status == 412, fmt.Sprintf("status=%d", status))
		}

		// Missing object: the conditional header is irrelevant, real S3
		// error (NoSuchKey/404) still applies.
		_, err = s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p6"), Key: aws.String("nosuchkey"), IfMatch: aws.String(etag)})
		requireTrue("Phase6: GetObject on a missing key still fails regardless of If-Match", err != nil, "expected an error")
		if err != nil {
			code, status := errCodeAndStatus(err)
			requireTrue("Phase6: missing key reports 404/NoSuchKey, not a conditional-write error", status == 404, fmt.Sprintf("code=%q status=%d", code, status))
		}
	}()

	// =========================================================================
	// Phase 7: conditional CopyObject source predicates (M8F-C). Real AWS
	// SDK CopyObject calls using CopySourceIfMatch/CopySourceIfNoneMatch
	// against a real zeros3 server: matching source If-Match succeeds and
	// clones exact bytes, mismatching source If-Match fails with 412 and
	// creates no destination, and a source If-None-Match that matches the
	// current source ETag also fails with 412.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p7")
		defer s.stop()
		check("Phase7: CreateBucket", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p7")})))
		body := randomBytes(7001, 4096)
		_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("p7"), Key: aws.String("src"), Body: bytes.NewReader(body)})
		check("Phase7: PUT the source object", err)
		etag, err := headETag(ctx, s.client, "p7", "src")
		check("Phase7: HEAD to observe the source's current ETag", err)

		_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: aws.String("p7"), Key: aws.String("dst1"),
			CopySource: aws.String("p7/src"), CopySourceIfMatch: aws.String(etag),
		})
		check("Phase7: CopyObject with matching CopySourceIfMatch succeeds", err)
		get1, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p7"), Key: aws.String("dst1")})
		if check1("Phase7: GetObject on the conditional copy", get1, err) != nil {
			got := mustReadAll(get1.Body)
			requireTrue("Phase7: conditional copy has the source's exact bytes", bytes.Equal(got, body), "content mismatch")
		}

		_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: aws.String("p7"), Key: aws.String("dst2"),
			CopySource:        aws.String("p7/src"),
			CopySourceIfMatch: aws.String(`"0000000000000000000000000000000000000000000000000000000000000000"`),
		})
		requireTrue("Phase7: CopyObject with a mismatching CopySourceIfMatch fails", err != nil, "expected an error")
		if err != nil {
			code, status := errCodeAndStatus(err)
			requireTrue("Phase7: mismatching source If-Match rejected with HTTP 412", status == 412, fmt.Sprintf("code=%q status=%d", code, status))
		}
		_, err = s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String("p7"), Key: aws.String("dst2")})
		requireTrue("Phase7: a rejected conditional copy creates no destination object", err != nil, "expected dst2 to not exist")

		_, err = s.client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket: aws.String("p7"), Key: aws.String("dst3"),
			CopySource: aws.String("p7/src"), CopySourceIfNoneMatch: aws.String(etag),
		})
		requireTrue("Phase7: CopyObject with a matching CopySourceIfNoneMatch fails", err != nil, "expected an error")
		if err != nil {
			code, status := errCodeAndStatus(err)
			requireTrue("Phase7: matching source If-None-Match rejected with HTTP 412", status == 412, fmt.Sprintf("code=%q status=%d", code, status))
		}
	}()

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed, %d informational =====\n", pass, fail, info)
	if fail > 0 {
		os.Exit(1)
	}
}
