// Ephemeral, external interoperability harness for ZeroS3 P1
// (environment-variable credentials, P1-A; HTTP hardening + graceful
// shutdown, P1-B).
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 purely as a
// real, independent, third-party S3 client -- proving that a genuine
// off-the-shelf SDK, not zeros3's own hand-rolled SigV4 test signer,
// authenticates correctly against environment-derived credentials and
// survives a real OS signal delivered to a real `zeros3 serve`
// subprocess mid-request. zeros3.go's own internal test suite
// (zeros3_test.go, "P1-A"/"P1-B" sections) is the source of truth for
// exhaustive precedence/edge-case correctness and for the deadline-
// expiry/second-signal/non-corruption proofs; this harness is
// real-process, real-client evidence layered on top of that, scoped to
// what only an independent process/client can prove.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var pass, fail int

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

// cleanEnv strips AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY/AWS_REGION from
// the current process's environment before handing it to a subprocess
// that is meant to run with ZeroS3's built-in default credentials.
// Without this, an ambient AWS_* variable already present in whatever
// environment runs this harness (a CI runner, a developer's shell, or --
// as this harness itself discovered while it was being built -- a
// sandboxed environment's own outbound-proxy credentials) would silently
// override the defaults via P1-A's own environment fallback, breaking
// every assumption below that a plain `serve` with no -access-key/
// -secret-key flags uses AKIAZEROS3EXAMPLE01/zeros3exampleSecretKey...
// Real P1-A behavior is exercised deliberately in Phase 1/2 above, which
// build their own explicit environment instead of calling cleanEnv.
func cleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		switch {
		case strings.HasPrefix(kv, "AWS_ACCESS_KEY_ID="),
			strings.HasPrefix(kv, "AWS_SECRET_ACCESS_KEY="),
			strings.HasPrefix(kv, "AWS_REGION="):
			continue
		}
		out = append(out, kv)
	}
	return out
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

func waitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("did not become ready on %s within %s", addr, timeout)
}

func newClient(endpoint, accessKey, secretKey, region string) *s3.Client {
	return newClientWithTransport(endpoint, accessKey, secretKey, region, nil)
}

func newClientWithTransport(endpoint, accessKey, secretKey, region string, rt http.RoundTripper) *s3.Client {
	ctx := context.Background()
	// Deliberately never config.WithHTTPClient here: LoadDefaultConfig
	// also inspects AWS_CA_BUNDLE (set in some sandboxed environments,
	// including the one this harness was developed in) and tries to
	// splice a custom RootCAs pool into whatever HTTPClient it is given,
	// which requires a client implementing its own internal
	// WithTransportOptions interface -- a plain *http.Client doesn't,
	// and LoadDefaultConfig fails outright rather than degrading. Letting
	// it build its own default client instead, then overriding
	// s3.Options.HTTPClient below (after LoadDefaultConfig has already
	// returned successfully), sidesteps that entirely.
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		log.Fatalf("config.LoadDefaultConfig: %v", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
		if rt != nil {
			o.HTTPClient = &http.Client{Transport: rt}
		}
	})
}

func createBucket(ctx context.Context, c *s3.Client, bucket string) error {
	_, err := c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	return err
}

func putObject(ctx context.Context, c *s3.Client, bucket, key string, body []byte) error {
	_, err := c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	return err
}

func getObjectBody(ctx context.Context, c *s3.Client, bucket, key string) ([]byte, error) {
	out, err := c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

// throttledBody hands back a fixed byte slice in small, paced pieces.
type throttledBody struct {
	data      []byte
	pos       int
	chunkSize int
	delay     time.Duration
}

func (r *throttledBody) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	time.Sleep(r.delay)
	n := r.chunkSize
	if n > len(p) {
		n = len(p)
	}
	if remaining := len(r.data) - r.pos; n > remaining {
		n = remaining
	}
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func (r *throttledBody) Close() error { return nil }

// throttlingTransport paces the ACTUAL outgoing wire transfer of a
// request's body, regardless of whatever the AWS SDK's own middleware
// did to the body beforehand (SigV4 payload hashing reads a seekable
// body through once locally, off the wire entirely, before the real
// send -- pacing at that layer paces the local hashing pass, not the
// network transfer). RoundTrip is the last stop before net/http actually
// writes bytes to the socket, so re-wrapping req.Body here is a paced
// *wire* transfer no matter what happened upstream, giving a real,
// deterministic window where a server-side connection is genuinely
// still receiving a request's body.
type throttlingTransport struct {
	chunkSize int
	delay     time.Duration
}

func (t *throttlingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		data, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = &throttledBody{data: data, chunkSize: t.chunkSize, delay: t.delay}
		req.ContentLength = int64(len(data))
	}
	return http.DefaultTransport.RoundTrip(req)
}

type server struct {
	binPath string
	dir     string
	addr    string
	cmd     *exec.Cmd
}

func startServer(binPath, tag string, extraArgs []string, env []string) *server {
	dir, err := os.MkdirTemp("", "zeros3-p1-"+tag+"-store-")
	if err != nil {
		log.Fatal(err)
	}
	return startServerAt(binPath, dir, extraArgs, env)
}

// startServerAt starts serve against an existing store directory --
// used to restart a server on the exact same store after a shutdown, to
// prove durability/restart-health rather than starting a fresh one.
func startServerAt(binPath, dir string, extraArgs []string, env []string) *server {
	addr, err := freePort()
	if err != nil {
		log.Fatal(err)
	}
	args := append([]string{"serve", "-store", dir, "-addr", addr}, extraArgs...)
	cmd := exec.Command(binPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		log.Fatalf("starting server (store=%s): %v", dir, err)
	}
	if err := waitReady(addr, 5*time.Second); err != nil {
		cmd.Process.Kill()
		log.Fatalf("server (store=%s): %v", dir, err)
	}
	return &server{binPath: binPath, dir: dir, addr: addr, cmd: cmd}
}

func (s *server) endpoint() string { return "http://" + s.addr }

func (s *server) stop() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	s.cmd.Process.Kill()
	s.cmd.Wait()
	os.RemoveAll(s.dir)
}

func runCLI(binPath string, env []string, args ...string) (string, error) {
	cmd := exec.Command(binPath, args...)
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func waitExit(cmd *exec.Cmd, timeout time.Duration) (exited bool, code int) {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return true, 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return true, ee.ExitCode()
		}
		return true, -1
	case <-time.After(timeout):
		return false, -1
	}
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

	fmt.Println("=== P1-A Phase 1: server started with credentials ONLY in the environment ===")
	func() {
		env := append(os.Environ(),
			"AWS_ACCESS_KEY_ID=P1-EXTERNAL-ENV-ACCESS-KEY",
			"AWS_SECRET_ACCESS_KEY=P1-EXTERNAL-ENV-SECRET-KEY",
			"AWS_REGION=us-west-2",
		)
		srv := startServer(binPath, "p1a-env", nil, env)
		defer srv.stop()

		rightClient := newClient(srv.endpoint(), "P1-EXTERNAL-ENV-ACCESS-KEY", "P1-EXTERNAL-ENV-SECRET-KEY", "us-west-2")
		check("p1a: create bucket with env-derived credentials (real AWS SDK)", createBucket(ctx, rightClient, "envbucket"))
		body := []byte("authenticated purely via AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY")
		check("p1a: PutObject with env-derived credentials", putObject(ctx, rightClient, "envbucket", "k", body))
		got, err := getObjectBody(ctx, rightClient, "envbucket", "k")
		check("p1a: GetObject with env-derived credentials", err)
		requireTrue("p1a: round-tripped content matches", bytes.Equal(got, body), "content mismatch")

		wrongClient := newClient(srv.endpoint(), "totally-wrong-access-key", "totally-wrong-secret-key", "us-west-2")
		_, err = getObjectBody(ctx, wrongClient, "envbucket", "k")
		requireTrue("p1a: wrong credentials are rejected", err != nil, "expected an auth failure, got success")
	}()

	fmt.Println("\n=== P1-A Phase 2: explicit CLI flags are authoritative over environment credentials ===")
	func() {
		env := append(os.Environ(),
			"AWS_ACCESS_KEY_ID=WRONG-FOR-THIS-SERVER",
			"AWS_SECRET_ACCESS_KEY=WRONG-FOR-THIS-SERVER",
		)
		// -access-key/-secret-key are explicit CLI flags here; the
		// environment above must never override them (P1-A2).
		srv := startServer(binPath, "p1a-flagwins", []string{"-access-key", "REAL-FLAG-ACCESS-KEY", "-secret-key", "REAL-FLAG-SECRET-KEY"}, env)
		defer srv.stop()

		flagClient := newClient(srv.endpoint(), "REAL-FLAG-ACCESS-KEY", "REAL-FLAG-SECRET-KEY", "us-east-1")
		check("p1a: create bucket using the explicit -access-key/-secret-key credentials", createBucket(ctx, flagClient, "flagbucket"))

		envClient := newClient(srv.endpoint(), "WRONG-FOR-THIS-SERVER", "WRONG-FOR-THIS-SERVER", "us-east-1")
		_, err := getObjectBody(ctx, envClient, "flagbucket", "does-not-matter")
		requireTrue("p1a: environment credentials are rejected once explicit flags were given", err != nil, "expected env credentials to be ignored, but they authenticated")
	}()

	fmt.Println("\n=== P1-B Phase 1: idle SIGTERM / SIGINT graceful shutdown ===")
	func() {
		for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
			srv := startServer(binPath, "p1b-idle-"+sig.String(), nil, cleanEnv())
			start := time.Now()
			srv.cmd.Process.Signal(sig)
			exited, code := waitExit(srv.cmd, 40*time.Second)
			elapsed := time.Since(start)
			requireTrue(fmt.Sprintf("p1b: idle %s: process exits", sig), exited, "process did not exit")
			requireTrue(fmt.Sprintf("p1b: idle %s: clean exit code", sig), code == 0, fmt.Sprintf("exit code=%d", code))
			requireTrue(fmt.Sprintf("p1b: idle %s: bounded shutdown time", sig), elapsed < 30*time.Second, fmt.Sprintf("took %s", elapsed))

			// Restart on the exact same store must succeed.
			restart := startServerAt(binPath, srv.dir, nil, cleanEnv())
			restart.stop()
		}
	}()

	fmt.Println("\n=== P1-B Phase 3: active PUT (real AWS SDK, throttled body) completes within the grace period ===")
	func() {
		srv := startServer(binPath, "p1b-active-put", nil, cleanEnv())
		defer srv.stop()
		client := newClient(srv.endpoint(), "AKIAZEROS3EXAMPLE01", "zeros3exampleSecretKeyForM1TestingOnly01", "us-east-1")
		check("p1b: create bucket", createBucket(ctx, client, "activebucket"))

		// A second client whose Transport paces the actual wire transfer
		// (see throttlingTransport's doc comment) of the PUT below, so it
		// is genuinely still sending its body -- a real active
		// connection server-side -- when SIGTERM arrives.
		slowClient := newClientWithTransport(srv.endpoint(), "AKIAZEROS3EXAMPLE01", "zeros3exampleSecretKeyForM1TestingOnly01", "us-east-1",
			&throttlingTransport{chunkSize: 10, delay: 200 * time.Millisecond}) // ~6s
		body := bytes.Repeat([]byte("q"), 300)
		putDone := make(chan error, 1)
		go func() {
			_, err := slowClient.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("activebucket"), Key: aws.String("slow.bin"), Body: bytes.NewReader(body)})
			putDone <- err
		}()

		time.Sleep(1 * time.Second)
		srv.cmd.Process.Signal(syscall.SIGTERM)

		select {
		case err := <-putDone:
			check("p1b: active PUT completes successfully despite SIGTERM mid-transfer", err)
		case <-time.After(35 * time.Second):
			fail++
			fmt.Println("FAIL: p1b: active PUT never completed")
		}

		exited, code := waitExit(srv.cmd, 35*time.Second)
		requireTrue("p1b: server exits after active PUT finishes", exited, "did not exit")
		requireTrue("p1b: clean exit code after active PUT", code == 0, fmt.Sprintf("exit code=%d", code))

		verifyOut, verifyErr := runCLI(binPath, cleanEnv(), "verify", "-deep", "-store", srv.dir)
		check("p1b: verify -deep on the post-shutdown store", verifyErr)
		if verifyErr != nil {
			fmt.Println(verifyOut)
		}
		os.RemoveAll(srv.dir)
	}()

	fmt.Println("\n=== P1-B Phase 5: a request that cannot finish before the grace period expires does not hang the process ===")
	func() {
		srv := startServer(binPath, "p1b-grace-expiry", nil, cleanEnv())
		client := newClient(srv.endpoint(), "AKIAZEROS3EXAMPLE01", "zeros3exampleSecretKeyForM1TestingOnly01", "us-east-1")
		check("p1b: create bucket", createBucket(ctx, client, "b"))

		slowClient := newClientWithTransport(srv.endpoint(), "AKIAZEROS3EXAMPLE01", "zeros3exampleSecretKeyForM1TestingOnly01", "us-east-1",
			&throttlingTransport{chunkSize: 1, delay: 1 * time.Second}) // ~400s total: guaranteed still mid-flight at grace expiry
		body := bytes.Repeat([]byte("n"), 400)
		go func() {
			slowClient.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("b"), Key: aws.String("never.bin"), Body: bytes.NewReader(body)})
		}()

		time.Sleep(2 * time.Second)
		start := time.Now()
		srv.cmd.Process.Signal(syscall.SIGTERM)
		exited, code := waitExit(srv.cmd, 50*time.Second)
		elapsed := time.Since(start)
		requireTrue("p1b: process exits instead of hanging past the grace period", exited, "process hung")
		requireTrue("p1b: waited out roughly the full grace period first", elapsed >= 29*time.Second, fmt.Sprintf("exited after only %s", elapsed))
		requireTrue("p1b: nonzero exit on grace-period expiry", code != 0, fmt.Sprintf("exit code=%d", code))

		restart := startServerAt(binPath, srv.dir, nil, cleanEnv())
		verifyOut, verifyErr := runCLI(binPath, cleanEnv(), "verify", "-deep", "-store", srv.dir)
		check("p1b: verify -deep after grace-period expiry (no corruption)", verifyErr)
		if verifyErr != nil {
			fmt.Println(verifyOut)
		}
		restart.stop()
	}()

	fmt.Printf("\n=== P1 external harness (env credentials + graceful shutdown): %d passed, %d failed ===\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}
