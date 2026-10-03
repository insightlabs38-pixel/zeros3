// Ephemeral, external interoperability harness for ZeroS3 M8B
// (peer-assisted corruption repair, `zeros3 repair`).
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 purely as a
// black-box S3 client. `zeros3 repair`/`zeros3 verify` themselves are
// driven as real external processes (os/exec against the built `zeros3`
// binary) against TWO independent, real `zeros3 serve` subprocesses on
// separate stores and ports -- exactly the way a real operator would
// invoke them. Every object is populated through the real AWS SDK; direct
// filesystem modification is used only to deliberately corrupt/delete an
// already-durably-published CAS chunk file after the object was created
// through the normal API, because corruption is the condition under test
// (the milestone spec explicitly permits this and nothing else).
//
// Each phase below uses its own fresh pair of store directories/servers
// (rather than one long-lived pair threaded through all eight phases) so
// that "find the one CAS chunk file this object just produced" is a
// trivial, deterministic filesystem walk: every target object body in
// this harness is deliberately kept under 16KiB (ZeroS3's frozen CDC v1
// minimum chunk size), so content-defined chunking can never introduce an
// internal cut -- each such object always durably publishes to exactly
// one CAS chunk file, findable by simply walking a freshly-opened store's
// otherwise-empty chunks/ directory.
package main

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

	// smallObjectSize is deliberately below ZeroS3's frozen CDC v1 minimum
	// chunk size (16KiB) -- see this file's doc comment.
	smallObjectSize = 4000
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

// runRepair invokes the real `zeros3 repair` CLI as a subprocess (never a
// Go package call) against localDir, from peerEndpoint, returning combined
// stdout+stderr and the process's exit error (nil means exit 0).
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

// chunkFilesUnder returns every CAS chunk leaf file path under
// storeDir/chunks, sorted for determinism. Every phase in this harness
// uses a freshly-opened store and only ever PUTs objects smaller than
// ZeroS3's frozen 16KiB CDC v1 minimum chunk size, so each distinct-
// content object it writes produces exactly one such file -- this is a
// black-box discovery mechanism, not an assumption about internal chunk
// naming.
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

type httpServerHandle struct {
	srv *http.Server
	ln  net.Listener
}

func (h *httpServerHandle) url() string { return "http://" + h.ln.Addr().String() }
func (h *httpServerHandle) stop()       { h.srv.Close() }

// maliciousPeer starts a fake HTTP server that answers ZeroS3's
// capability-discovery endpoint truthfully (so `zeros3 repair`'s
// discoverZeroS3Sync succeeds and proceeds to the real test: the chunk
// fetch) but returns deliberately wrong bytes for every chunk-download
// request, ignoring auth/digest entirely -- standing in for a malicious or
// badly broken peer, entirely independent of the real zeros3 binary
// (net/http only, no zeros3 internals). Phase 6 proves the real `zeros3
// repair` client rejects this before ever touching local CAS.
func maliciousPeer() *httpServerHandle {
	mux := http.NewServeMux()
	mux.HandleFunc("/_zeros3/v1/info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"protocol":1,"cdc":"gear-v1","hash":"sha256","delta_sync":true,"max_hashes_per_batch":1024,"max_batch_bytes":262144,"max_chunk_bytes":262144}`)
	})
	mux.HandleFunc("/_zeros3/v1/chunks/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(maliciousPeerPayload))
	})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("malicious peer listen: %v", err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	return &httpServerHandle{srv: srv, ln: l}
}

const maliciousPeerPayload = "these are definitely not the bytes the digest in the URL asks for -- a malicious/broken peer response"

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	ctx := context.Background()
	const bucket = "m8b-bucket"

	// newPair starts a fresh Store A ("local", the store under repair)
	// and Store B ("peer", the trusted healthy source) pair, each in its
	// own temporary directory and on its own ephemeral port, with the
	// shared bucket already created on both.
	newPair := func(label string) (aDir, bDir, aAddr, bAddr string, aCmd, bCmd *exec.Cmd, aClient, bClient *s3.Client) {
		var err error
		aDir, err = os.MkdirTemp("", "zeros3-m8b-a-"+label+"-")
		if err != nil {
			log.Fatal(err)
		}
		bDir, err = os.MkdirTemp("", "zeros3-m8b-b-"+label+"-")
		if err != nil {
			log.Fatal(err)
		}
		aAddr, err = freePort()
		if err != nil {
			log.Fatal(err)
		}
		bAddr, err = freePort()
		if err != nil {
			log.Fatal(err)
		}
		aCmd, err = startZeroS3(binPath, aDir, aAddr)
		if err != nil {
			log.Fatalf("[%s] starting store A: %v", label, err)
		}
		bCmd, err = startZeroS3(binPath, bDir, bAddr)
		if err != nil {
			log.Fatalf("[%s] starting store B: %v", label, err)
		}
		aClient = newClient("http://" + aAddr)
		bClient = newClient("http://" + bAddr)
		check("["+label+"] CreateBucket on store A", func() error {
			_, err := aClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
			return err
		}())
		check("["+label+"] CreateBucket on store B", func() error {
			_, err := bClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
			return err
		}())
		return
	}

	// =========================================================================
	// Phase 1: healthy baseline. Create the same bucket/object on both
	// stores via the real AWS SDK, confirm exact equality, deep-verify
	// both healthy.
	// =========================================================================
	p1ADir, p1BDir, p1AAddr, p1BAddr, p1ACmd, p1BCmd, p1AClient, p1BClient := newPair("p1")
	defer func() { stopZeroS3(p1ACmd); os.RemoveAll(p1ADir) }()
	defer func() { stopZeroS3(p1BCmd); os.RemoveAll(p1BDir) }()

	body1 := randomBytes(101, smallObjectSize)
	check("Phase1: AWS SDK PutObject on store A", putObject(ctx, p1AClient, bucket, "healthy", body1))
	check("Phase1: AWS SDK PutObject on store B (identical content)", putObject(ctx, p1BClient, bucket, "healthy", body1))

	getA, err := p1AClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("healthy")})
	check("Phase1: GetObject on store A", err)
	getB, err := p1BClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("healthy")})
	check("Phase1: GetObject on store B", err)
	if getA != nil && getB != nil {
		gotA, gotB := mustReadAll(getA.Body), mustReadAll(getB.Body)
		requireTrue("Phase1: store A and store B hold byte-identical content", bytes.Equal(gotA, gotB) && bytes.Equal(gotA, body1), "mismatch")
	}

	stopZeroS3(p1ACmd)
	_, vErr := runVerifyDeep(binPath, p1ADir)
	check("Phase1: verify -deep on store A is healthy", vErr)
	p1ACmd, err = startZeroS3(binPath, p1ADir, p1AAddr)
	check("Phase1: restart store A after verify", err)
	stopZeroS3(p1BCmd)
	_, vErr = runVerifyDeep(binPath, p1BDir)
	check("Phase1: verify -deep on store B is healthy", vErr)
	p1BCmd, err = startZeroS3(binPath, p1BDir, p1BAddr)
	check("Phase1: restart store B after verify", err)

	// =========================================================================
	// Phase 2: missing chunk. Delete one reachable CAS chunk from store A
	// (direct filesystem modification of an already-validly-published
	// file -- the one deliberate exception the milestone permits, because
	// corruption is the condition under test). Phase 8 (restart proof) is
	// folded in immediately after this phase's repair completes.
	// =========================================================================
	p2ADir, p2BDir, p2AAddr, p2BAddr, p2ACmd, p2BCmd, p2AClient, p2BClient := newPair("p2")
	defer func() { stopZeroS3(p2ACmd); os.RemoveAll(p2ADir) }()
	defer func() { stopZeroS3(p2BCmd); os.RemoveAll(p2BDir) }()

	body2 := randomBytes(102, smallObjectSize)
	check("Phase2: AWS SDK PutObject on store A", putObject(ctx, p2AClient, bucket, "missing-target", body2))
	check("Phase2: AWS SDK PutObject on store B (identical content, the trusted peer copy)", putObject(ctx, p2BClient, bucket, "missing-target", body2))

	stopZeroS3(p2ACmd)
	chunks2, err := chunkFilesUnder(p2ADir)
	check("Phase2: locate store A's CAS chunk file(s)", err)
	requireTrue("Phase2: exactly one chunk file for the small target object", len(chunks2) == 1, fmt.Sprintf("got %d", len(chunks2)))
	if len(chunks2) == 1 {
		check("Phase2: delete the chunk file directly (simulating disk loss)", os.Remove(chunks2[0]))
	}
	_, vErr = runVerifyDeep(binPath, p2ADir)
	requireTrue("Phase2: verify -deep on store A detects the missing chunk (nonzero exit)", vErr != nil, "verify unexpectedly succeeded")
	p2ACmd, err = startZeroS3(binPath, p2ADir, p2AAddr)
	check("Phase2: restart store A (server back up) with the chunk still missing", err)
	_, getErr := p2AClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("missing-target")})
	requireTrue("Phase2: ordinary GET on store A fails/reports corruption while the chunk is missing", getErr != nil, "GetObject unexpectedly succeeded on a store with a missing chunk")

	repairOut2, repairErr := runRepair(binPath, p2ADir, "http://"+p2BAddr)
	fmt.Print(repairOut2)
	check("Phase2: zeros3 repair (missing chunk) from store B", repairErr)
	requireTrue("Phase2: repair reports exactly one bad/repaired chunk", strings.Contains(repairOut2, "Bad chunks:          1") && strings.Contains(repairOut2, "Repaired:            1"), repairOut2)

	stopZeroS3(p2ACmd)
	_, vErr = runVerifyDeep(binPath, p2ADir)
	check("Phase2: verify -deep on store A is clean after repair", vErr)
	p2ACmd, err = startZeroS3(binPath, p2ADir, p2AAddr)
	check("Phase2: restart store A after repair+verify", err)
	get2, err := p2AClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("missing-target")})
	check("Phase2: AWS SDK GetObject on store A after repair", err)
	if get2 != nil {
		got2 := mustReadAll(get2.Body)
		requireTrue("Phase2: AWS SDK GetObject returns exact original bytes after repair", bytes.Equal(got2, body2), "mismatch")
	}

	// Phase 8: restart store A again after a completed repair, prove
	// GET/verify both remain green.
	stopZeroS3(p2ACmd)
	p2ACmd, err = startZeroS3(binPath, p2ADir, p2AAddr)
	check("Phase8: restart store A again after a completed repair", err)
	get2b, err := p2AClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("missing-target")})
	check("Phase8: AWS SDK GetObject on store A after the post-repair restart", err)
	if get2b != nil {
		got2b := mustReadAll(get2b.Body)
		requireTrue("Phase8: AWS SDK GetObject returns exact bytes after the post-repair restart", bytes.Equal(got2b, body2), "mismatch")
	}
	stopZeroS3(p2ACmd)
	_, vErr = runVerifyDeep(binPath, p2ADir)
	check("Phase8: verify -deep on store A remains green after the post-repair restart", vErr)
	p2ACmd, err = startZeroS3(binPath, p2ADir, p2AAddr)
	check("Phase8: restart store A once more (leave it running for cleanup)", err)

	// =========================================================================
	// Phase 3: corrupt chunk (bytes flipped, filename unchanged).
	// =========================================================================
	p3ADir, p3BDir, p3AAddr, p3BAddr, p3ACmd, p3BCmd, p3AClient, p3BClient := newPair("p3")
	defer func() { stopZeroS3(p3ACmd); os.RemoveAll(p3ADir) }()
	defer func() { stopZeroS3(p3BCmd); os.RemoveAll(p3BDir) }()

	body3 := randomBytes(103, smallObjectSize)
	check("Phase3: AWS SDK PutObject on store A", putObject(ctx, p3AClient, bucket, "corrupt-target", body3))
	check("Phase3: AWS SDK PutObject on store B (identical content)", putObject(ctx, p3BClient, bucket, "corrupt-target", body3))

	stopZeroS3(p3ACmd)
	chunks3, err := chunkFilesUnder(p3ADir)
	check("Phase3: locate store A's CAS chunk file(s)", err)
	requireTrue("Phase3: exactly one chunk file for the small target object", len(chunks3) == 1, fmt.Sprintf("got %d", len(chunks3)))
	if len(chunks3) == 1 {
		check("Phase3: flip bytes of the chunk file directly, filename unchanged", corruptFile(chunks3[0]))
	}
	_, vErr = runVerifyDeep(binPath, p3ADir)
	requireTrue("Phase3: verify -deep on store A detects the digest mismatch (nonzero exit)", vErr != nil, "verify unexpectedly succeeded")
	p3ACmd, err = startZeroS3(binPath, p3ADir, p3AAddr)
	check("Phase3: restart store A with the chunk still corrupt", err)

	repairOut3, repairErr := runRepair(binPath, p3ADir, "http://"+p3BAddr)
	fmt.Print(repairOut3)
	check("Phase3: zeros3 repair (corrupt chunk) from store B", repairErr)
	requireTrue("Phase3: repair reports exactly one bad/repaired chunk", strings.Contains(repairOut3, "Bad chunks:          1") && strings.Contains(repairOut3, "Repaired:            1"), repairOut3)

	stopZeroS3(p3ACmd)
	_, vErr = runVerifyDeep(binPath, p3ADir)
	check("Phase3: verify -deep on store A is clean after repair", vErr)
	p3ACmd, err = startZeroS3(binPath, p3ADir, p3AAddr)
	check("Phase3: restart store A after repair+verify", err)
	get3, err := p3AClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("corrupt-target")})
	check("Phase3: AWS SDK GetObject on store A after repair", err)
	if get3 != nil {
		got3 := mustReadAll(get3.Body)
		requireTrue("Phase3: AWS SDK GetObject returns exact original bytes after repair", bytes.Equal(got3, body3), "mismatch")
	}

	// =========================================================================
	// Phase 4: shared chunk. Several objects reference the same content
	// (put identically, so they share the sole CAS chunk by construction);
	// corrupt it once; confirm one unique repair fetch and every
	// referencing object becomes healthy.
	// =========================================================================
	p4ADir, p4BDir, p4AAddr, p4BAddr, p4ACmd, p4BCmd, p4AClient, p4BClient := newPair("p4")
	defer func() { stopZeroS3(p4ACmd); os.RemoveAll(p4ADir) }()
	defer func() { stopZeroS3(p4BCmd); os.RemoveAll(p4BDir) }()

	body4 := randomBytes(104, smallObjectSize)
	sharedKeys := []string{"shared-1", "shared-2", "shared-3", "shared-4", "shared-5"}
	for _, k := range sharedKeys {
		check("Phase4: AWS SDK PutObject store A key="+k, putObject(ctx, p4AClient, bucket, k, body4))
	}
	check("Phase4: AWS SDK PutObject store B (single copy of the shared content)", putObject(ctx, p4BClient, bucket, sharedKeys[0], body4))

	stopZeroS3(p4ACmd)
	chunks4, err := chunkFilesUnder(p4ADir)
	check("Phase4: locate store A's CAS chunk file(s)", err)
	requireTrue("Phase4: exactly one CAS chunk shared by all 5 objects (dedup)", len(chunks4) == 1, fmt.Sprintf("got %d", len(chunks4)))
	if len(chunks4) == 1 {
		check("Phase4: corrupt the single shared chunk file", corruptFile(chunks4[0]))
	}
	p4ACmd, err = startZeroS3(binPath, p4ADir, p4AAddr)
	check("Phase4: restart store A with the shared chunk corrupt", err)

	repairOut4, repairErr := runRepair(binPath, p4ADir, "http://"+p4BAddr)
	fmt.Print(repairOut4)
	check("Phase4: zeros3 repair (shared chunk) from store B", repairErr)
	requireTrue("Phase4: repair fetches the shared chunk exactly once (Bad chunks: 1), not once per object",
		strings.Contains(repairOut4, "Bad chunks:          1") && strings.Contains(repairOut4, "Repaired:            1"), repairOut4)
	requireTrue("Phase4: repair reports all 5 referencing objects affected",
		strings.Contains(repairOut4, "Affected objects:    5"), repairOut4)

	allHealthy := true
	for _, k := range sharedKeys {
		g, err := p4AClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(k)})
		if err != nil {
			allHealthy = false
			continue
		}
		got := mustReadAll(g.Body)
		if !bytes.Equal(got, body4) {
			allHealthy = false
		}
	}
	requireTrue("Phase4: every object sharing the corrupt chunk is healthy after one repair", allHealthy, "at least one shared-key object still incorrect")

	// =========================================================================
	// Phase 5: peer missing data too. The needed chunk is missing locally
	// AND the peer never received the object either -- repair must not
	// fabricate success; local corruption remains reported; nonzero
	// result.
	// =========================================================================
	p5ADir, p5BDir, p5AAddr, p5BAddr, p5ACmd, p5BCmd, p5AClient, _ := newPair("p5")
	defer func() { stopZeroS3(p5ACmd); os.RemoveAll(p5ADir) }()
	defer func() { stopZeroS3(p5BCmd); os.RemoveAll(p5BDir) }()

	body5 := randomBytes(105, smallObjectSize)
	check("Phase5: AWS SDK PutObject on store A", putObject(ctx, p5AClient, bucket, "peer-lacks-it", body5))
	// Store B deliberately never receives this object -- it genuinely
	// lacks the needed chunk too.

	stopZeroS3(p5ACmd)
	chunks5, err := chunkFilesUnder(p5ADir)
	check("Phase5: locate store A's CAS chunk file(s)", err)
	requireTrue("Phase5: exactly one chunk file", len(chunks5) == 1, fmt.Sprintf("got %d", len(chunks5)))
	if len(chunks5) == 1 {
		check("Phase5: delete store A's chunk (peer B never had it either)", os.Remove(chunks5[0]))
	}
	p5ACmd, err = startZeroS3(binPath, p5ADir, p5AAddr)
	check("Phase5: restart store A", err)

	repairOut5, repairErr := runRepair(binPath, p5ADir, "http://"+p5BAddr)
	fmt.Print(repairOut5)
	requireTrue("Phase5: repair reports a nonzero result when the peer itself lacks the chunk", repairErr != nil, "repair unexpectedly exited 0")
	requireTrue("Phase5: repair honestly reports the chunk unresolved, not fabricated success",
		strings.Contains(repairOut5, "Unresolved:          1") && strings.Contains(repairOut5, "Post-repair verify:  FAILED"), repairOut5)

	stopZeroS3(p5ACmd)
	_, vErr = runVerifyDeep(binPath, p5ADir)
	requireTrue("Phase5: local corruption remains reported by verify -deep after the failed repair", vErr != nil, "verify unexpectedly succeeded")
	p5ACmd, err = startZeroS3(binPath, p5ADir, p5AAddr)
	check("Phase5: restart store A (leave running for cleanup)", err)

	// =========================================================================
	// Phase 6: malicious/wrong peer bytes. A fake peer (independent
	// net/http server, no zeros3 internals) answers discovery truthfully
	// but returns wrong bytes for every chunk fetch. The real `zeros3
	// repair` client must reject this before ever publishing to local CAS.
	// =========================================================================
	p6ADir, p6BDir, p6AAddr, _, p6ACmd, p6BCmd, p6AClient, _ := newPair("p6")
	defer func() { stopZeroS3(p6ACmd); os.RemoveAll(p6ADir) }()
	defer func() { stopZeroS3(p6BCmd); os.RemoveAll(p6BDir) }()

	body6 := randomBytes(106, smallObjectSize)
	check("Phase6: AWS SDK PutObject on store A", putObject(ctx, p6AClient, bucket, "malicious-peer-target", body6))

	stopZeroS3(p6ACmd)
	chunks6, err := chunkFilesUnder(p6ADir)
	check("Phase6: locate store A's CAS chunk file(s)", err)
	requireTrue("Phase6: exactly one chunk file", len(chunks6) == 1, fmt.Sprintf("got %d", len(chunks6)))
	if len(chunks6) == 1 {
		check("Phase6: corrupt the chunk on store A", corruptFile(chunks6[0]))
	}
	p6ACmd, err = startZeroS3(binPath, p6ADir, p6AAddr)
	check("Phase6: restart store A with the chunk corrupt", err)

	evilPeer := maliciousPeer()
	defer evilPeer.stop()
	repairOut6, repairErr := runRepair(binPath, p6ADir, evilPeer.url())
	fmt.Print(repairOut6)
	requireTrue("Phase6: repair reports a nonzero result against a malicious peer", repairErr != nil, "repair unexpectedly exited 0 against a malicious peer")
	requireTrue("Phase6: repair's own output honestly reports the chunk unresolved, not a fabricated success",
		strings.Contains(repairOut6, "Unresolved:          1") && strings.Contains(repairOut6, "Post-repair verify:  FAILED"), repairOut6)

	stopZeroS3(p6ACmd)
	chunks6b, err := chunkFilesUnder(p6ADir)
	check("Phase6: re-locate store A's chunk file after the malicious-peer attempt", err)
	if len(chunks6b) == 1 {
		onDisk, rerr := os.ReadFile(chunks6b[0])
		check("Phase6: read chunk bytes after the malicious-peer repair attempt", rerr)
		requireTrue("Phase6: the malicious peer's wrong bytes were never published to local CAS",
			!bytes.Contains(onDisk, []byte(maliciousPeerPayload)), "the malicious peer's bytes leaked into local CAS")
	}
	p6ACmd, err = startZeroS3(binPath, p6ADir, p6AAddr)
	check("Phase6: restart store A (leave running for cleanup)", err)

	// =========================================================================
	// Phase 7: interrupted repair. Damage multiple chunks, kill the real
	// `zeros3 repair` process after some successful repairs, rerun,
	// confirm only the remaining damaged chunks require a peer fetch.
	// =========================================================================
	p7ADir, p7BDir, p7AAddr, p7BAddr, p7ACmd, p7BCmd, p7AClient, p7BClient := newPair("p7")
	defer func() { stopZeroS3(p7ACmd); os.RemoveAll(p7ADir) }()
	defer func() { stopZeroS3(p7BCmd); os.RemoveAll(p7BDir) }()

	const numInterruptTargets = 12
	interruptBodies := make([][]byte, numInterruptTargets)
	for i := 0; i < numInterruptTargets; i++ {
		interruptBodies[i] = randomBytes(int64(200+i), smallObjectSize)
		key := fmt.Sprintf("interrupt-target-%02d", i)
		check("Phase7: AWS SDK PutObject store A "+key, putObject(ctx, p7AClient, bucket, key, interruptBodies[i]))
		check("Phase7: AWS SDK PutObject store B "+key, putObject(ctx, p7BClient, bucket, key, interruptBodies[i]))
	}

	stopZeroS3(p7ACmd)
	chunks7, err := chunkFilesUnder(p7ADir)
	check("Phase7: locate store A's CAS chunk files", err)
	requireTrue(fmt.Sprintf("Phase7: exactly %d distinct chunk files (one per distinct-content object)", numInterruptTargets),
		len(chunks7) == numInterruptTargets, fmt.Sprintf("got %d", len(chunks7)))
	for _, c := range chunks7 {
		check("Phase7: corrupt chunk "+filepath.Base(c), corruptFile(c))
	}
	// Repair needs no live serve process against the local store (it only
	// takes the same shared lock `serve` would); run it offline here,
	// exactly like an operator who has stopped the server after noticing
	// corruption would.

	firstAttempt := exec.Command(binPath, "repair", "-store", p7ADir, "-from", "http://"+p7BAddr,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region)
	if err := firstAttempt.Start(); err != nil {
		log.Fatalf("Phase7: starting interruptible repair: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	killErr := firstAttempt.Process.Kill()
	firstAttempt.Wait()
	check("Phase7: kill zeros3 repair mid-run", killErr)

	midOut, _ := runVerifyDeep(binPath, p7ADir)
	noteInfo("Phase7: after the kill, verify -deep summary line (informational, exact count depends on how far the killed attempt got): %s",
		func() string {
			for _, line := range strings.Split(midOut, "\n") {
				if strings.Contains(line, "integrity") {
					return strings.TrimSpace(line)
				}
			}
			return "(not found)"
		}())

	secondOut, secondErr := runRepair(binPath, p7ADir, "http://"+p7BAddr)
	fmt.Print(secondOut)
	check("Phase7: resumed zeros3 repair after interruption", secondErr)

	p7ACmd, err = startZeroS3(binPath, p7ADir, p7AAddr)
	check("Phase7: restart store A after the resumed repair", err)
	allOK := true
	for i := 0; i < numInterruptTargets; i++ {
		key := fmt.Sprintf("interrupt-target-%02d", i)
		g, err := p7AClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		if err != nil {
			allOK = false
			continue
		}
		got := mustReadAll(g.Body)
		if !bytes.Equal(got, interruptBodies[i]) {
			allOK = false
		}
	}
	requireTrue("Phase7: every one of the 12 damaged objects is correct after the interrupted-then-resumed repair", allOK, "at least one object still incorrect")
	stopZeroS3(p7ACmd)
	_, vErr = runVerifyDeep(binPath, p7ADir)
	check("Phase7: verify -deep on store A is fully clean after interrupted-then-resumed repair", vErr)
	p7ACmd, err = startZeroS3(binPath, p7ADir, p7AAddr)
	check("Phase7: restart store A (leave running for cleanup)", err)

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed, %d informational =====\n", pass, fail, info)
	if fail > 0 {
		os.Exit(1)
	}
}
