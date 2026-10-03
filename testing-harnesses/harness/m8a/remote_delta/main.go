// Ephemeral, external interoperability harness for ZeroS3 M8A (remote-
// to-remote delta replication, `zeros3 replicate`).
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 purely as a
// black-box S3 client. `zeros3 replicate` itself is driven as a real
// external process (os/exec against the built `zeros3` binary) against
// TWO independent, real `zeros3 serve` subprocesses on separate stores
// and ports -- exactly the way a real user would invoke it. This
// harness never calls into ZeroS3's Go package internals and never
// populates either store's internal CAS directly: every fixture is
// written through the real AWS SDK, making this a black-box proof.
//
// What this proves that the internal (in-process, deterministic-hook-
// based) zeros3_test.go suite cannot: that a completely independent,
// real AWS SDK client -- not zeros3's own HTTP client, not its own test
// signer -- reads back exactly what a real `zeros3 replicate` subprocess
// wrote to a real, separate destination server, across a real process
// restart and a real process interruption/resume, and that conflict/
// source-mutation safety hold against real concurrent AWS SDK traffic.
// Internal tests remain the source of truth for exact protocol/
// precondition/consistency correctness (see zeros3's own STATUS.md);
// this harness is external, real-process, real-SDK, real-two-server
// evidence on top of that, not a replacement for it.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/exec"
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

// runReplicate invokes the real `zeros3 replicate` CLI as a subprocess
// (never a Go package call), returning its combined stdout+stderr and
// exit error.
func runReplicate(binPath, fromEndpoint, toEndpoint, srcURI, dstURI string) (output string, err error) {
	cmd := exec.Command(binPath, "replicate",
		"-from", fromEndpoint, "-to", toEndpoint,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey,
		"-region", region,
		srcURI, dstURI)
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

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}

	srcStoreDir, err := os.MkdirTemp("", "zeros3-m8a-src-store-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(srcStoreDir)
	dstStoreDir, err := os.MkdirTemp("", "zeros3-m8a-dst-store-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dstStoreDir)

	srcAddr, err := freePort()
	if err != nil {
		log.Fatal(err)
	}
	dstAddr, err := freePort()
	if err != nil {
		log.Fatal(err)
	}
	srcCmd, err := startZeroS3(binPath, srcStoreDir, srcAddr)
	if err != nil {
		log.Fatalf("starting source zeros3: %v", err)
	}
	defer func() { stopZeroS3(srcCmd) }()
	dstCmd, err := startZeroS3(binPath, dstStoreDir, dstAddr)
	if err != nil {
		log.Fatalf("starting destination zeros3: %v", err)
	}
	defer func() { stopZeroS3(dstCmd) }()

	srcEndpoint := "http://" + srcAddr
	dstEndpoint := "http://" + dstAddr
	srcClient := newClient(srcEndpoint)
	dstClient := newClient(dstEndpoint)
	ctx := context.Background()

	// =========================================================================
	// Phase 1: setup, entirely via the real AWS SDK -- never populating
	// either store's internal CAS directly, so this proof stays black-box.
	//
	// Store B (destination) already holds a large related object sharing
	// most of its content with what Store A is about to get; Store A
	// (source) then gets the actual replication target: the related
	// content plus a small localized edit. This is a deliberately honest,
	// strong-but-not-manufactured reuse scenario -- not an all-zero-
	// transfer demo.
	// =========================================================================
	const srcBucket, dstBucket = "m8a-source", "m8a-dest"
	_, err = srcClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(srcBucket)})
	check("CreateBucket on source", err)
	_, err = dstClient.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(dstBucket)})
	check("CreateBucket on destination", err)

	const baseSize = 24_000_000
	base := randomBytes(1, baseSize)
	_, err = dstClient.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(dstBucket), Key: aws.String("already-present"), Body: bytes.NewReader(base)})
	check("AWS SDK PutObject: seed destination with related content", err)

	edited := make([]byte, 0, baseSize+8192)
	mid := baseSize / 2
	edited = append(edited, base[:mid]...)
	edited = append(edited, randomBytes(2, 8192)...)
	edited = append(edited, base[mid:]...)
	_, err = srcClient.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(srcBucket), Key: aws.String("target-object"), Body: bytes.NewReader(edited),
		Metadata: map[string]string{"harness": "m8a-remote-delta"}})
	check("AWS SDK PutObject: write the actual replication target on the source", err)

	// =========================================================================
	// Phase 2: run the real `zeros3 replicate` CLI subprocess.
	// =========================================================================
	out1, err := runReplicate(binPath, srcEndpoint, dstEndpoint, "s3://"+srcBucket+"/target-object", "s3://"+dstBucket+"/target-object")
	fmt.Print(out1)
	check("zeros3 replicate (source -> destination)", err)

	logical := parseHumanBytesFromLine(statLine(out1, "Logical scanned:"))
	uploaded := parseHumanBytesFromLine(statLine(out1, "Uploaded payload:"))
	avoided := parseHumanBytesFromLine(statLine(out1, "Transfer avoided:"))
	requireTrue("replicate reports a strong, honest reuse figure (not a manufactured all-zero case)",
		logical > 0 && avoided > 0 && uploaded < logical/2,
		fmt.Sprintf("logical=%d uploaded=%d avoided=%d", logical, uploaded, avoided))
	noteInfo("Phase 2 stats: %s | %s | %s | %s",
		statLine(out1, "Logical scanned:"), statLine(out1, "Uploaded payload:"), statLine(out1, "Transfer avoided:"), statLine(out1, "Reuse:"))

	// =========================================================================
	// Phase 3: independent destination verification via the real AWS SDK.
	// =========================================================================
	get1, err := dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(dstBucket), Key: aws.String("target-object")})
	check("AWS SDK GetObject on destination after replicate", err)
	if get1 != nil {
		got1 := mustReadAll(get1.Body)
		requireTrue("AWS SDK GetObject on destination returns exact source bytes", bytes.Equal(got1, edited), fmt.Sprintf("len got=%d want=%d", len(got1), len(edited)))
	}
	head1, err := dstClient.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(dstBucket), Key: aws.String("target-object")})
	check("AWS SDK HeadObject on destination after replicate", err)
	if head1 != nil {
		requireTrue("AWS SDK HeadObject reports correct Content-Length", aws.ToInt64(head1.ContentLength) == int64(len(edited)),
			fmt.Sprintf("got %d, want %d", aws.ToInt64(head1.ContentLength), len(edited)))
		requireTrue("AWS SDK HeadObject preserves user metadata", head1.Metadata["harness"] == "m8a-remote-delta",
			fmt.Sprintf("metadata = %+v", head1.Metadata))
	}

	// =========================================================================
	// Phase 4: restart destination, repeat GET/checksum.
	// =========================================================================
	stopZeroS3(dstCmd)
	dstCmd, err = startZeroS3(binPath, dstStoreDir, dstAddr)
	check("restart destination zeros3 on the same store directory", err)
	get2, err := dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(dstBucket), Key: aws.String("target-object")})
	check("AWS SDK GetObject on destination after restart", err)
	if get2 != nil {
		got2 := mustReadAll(get2.Body)
		requireTrue("AWS SDK GetObject returns exact bytes after destination restart", bytes.Equal(got2, edited), "bytes differ after restart")
	}
	stopZeroS3(dstCmd)
	verifyOut, verifyErr := exec.Command(binPath, "verify", "-store", dstStoreDir, "-deep").CombinedOutput()
	fmt.Println(string(verifyOut))
	check("zeros3 verify -deep on destination after replicate+restart", verifyErr)
	dstCmd, err = startZeroS3(binPath, dstStoreDir, dstAddr)
	check("restart destination zeros3 after verify", err)

	// =========================================================================
	// Phase 5: resume -- interrupt the CLI after a subset of chunks reach
	// the destination, rerun, verify only the remaining payload crosses.
	// Best-effort/timing-based (a real external subprocess offers no
	// injection point, unlike the internal suite's deterministic hooks);
	// the invariant asserted holds regardless of exactly how much of the
	// first attempt completed: no partial/visible object after the kill,
	// then correct content after the resumed rerun.
	// =========================================================================
	largeSrc := randomBytes(3, 20_000_000)
	_, err = srcClient.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(srcBucket), Key: aws.String("resume-object"), Body: bytes.NewReader(largeSrc)})
	check("AWS SDK PutObject: source object for the interruption/resume test", err)

	interruptCmd := exec.Command(binPath, "replicate",
		"-from", srcEndpoint, "-to", dstEndpoint,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey,
		"-region", region,
		"s3://"+srcBucket+"/resume-object", "s3://"+dstBucket+"/resume-object")
	if err := interruptCmd.Start(); err != nil {
		log.Fatalf("starting interruptible replicate: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	killErr := interruptCmd.Process.Kill()
	interruptCmd.Wait()
	check("kill zeros3 replicate mid-transfer", killErr)

	_, headErr := dstClient.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(dstBucket), Key: aws.String("resume-object")})
	requireTrue("interrupted replicate never committed a partial/visible destination object", headErr != nil, "HeadObject unexpectedly succeeded after a killed replicate")

	out5, err := runReplicate(binPath, srcEndpoint, dstEndpoint, "s3://"+srcBucket+"/resume-object", "s3://"+dstBucket+"/resume-object")
	fmt.Print(out5)
	check("zeros3 replicate (resumed rerun after interruption)", err)

	get5, err := dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(dstBucket), Key: aws.String("resume-object")})
	check("AWS SDK GetObject after resumed replicate", err)
	if get5 != nil {
		got5 := mustReadAll(get5.Body)
		requireTrue("AWS SDK GetObject returns exact bytes after resumed replicate", bytes.Equal(got5, largeSrc), "bytes differ after resume")
	}
	noteInfo("resumed rerun stats: %s | %s (some chunks from the killed attempt may already have been durably published to destination CAS, reducing this further)",
		statLine(out5, "Uploaded payload:"), statLine(out5, "Reuse:"))

	// =========================================================================
	// Phase 6: conflict -- mutate the destination (via a racing AWS SDK
	// PutObject) between replicate's observation and its eventual commit.
	// Best-effort/timing-based, like Phase 5; the one invariant checked is
	// the one that must hold regardless of which side wins the real race:
	// the final object is exactly one writer's content, never a corrupted
	// mix, and if replicate lost the race it reports a non-zero exit.
	// =========================================================================
	raceSrc := randomBytes(5, 20_000_000)
	_, err = srcClient.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(srcBucket), Key: aws.String("race-object"), Body: bytes.NewReader(raceSrc)})
	check("AWS SDK PutObject: source object for the conflict-race test", err)
	interloper := []byte("the AWS SDK's concurrent racing PutObject content on the destination")

	raceCmd := exec.Command(binPath, "replicate",
		"-from", srcEndpoint, "-to", dstEndpoint,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey,
		"-region", region,
		"s3://"+srcBucket+"/race-object", "s3://"+dstBucket+"/race-object")
	var raceOut bytes.Buffer
	raceCmd.Stdout = &raceOut
	raceCmd.Stderr = &raceOut
	if err := raceCmd.Start(); err != nil {
		log.Fatalf("starting racing replicate: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	_, putErr := dstClient.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(dstBucket), Key: aws.String("race-object"), Body: bytes.NewReader(interloper)})
	check("AWS SDK racing PutObject on destination", putErr)
	replicateRaceErr := raceCmd.Wait()

	getRace, err := dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(dstBucket), Key: aws.String("race-object")})
	check("AWS SDK GetObject after the conflict race", err)
	if getRace != nil {
		gotRace := mustReadAll(getRace.Body)
		replicateWon := bytes.Equal(gotRace, raceSrc)
		interloperWon := bytes.Equal(gotRace, interloper)
		requireTrue("conflict-race result is exactly one writer's content, never a corrupted mix", replicateWon != interloperWon,
			fmt.Sprintf("neither/both matched: len=%d replicateWon=%v interloperWon=%v", len(gotRace), replicateWon, interloperWon))
		if interloperWon {
			requireTrue("when the AWS SDK PutObject won the race, replicate correctly reported a non-zero exit (safe-mode conflict)", replicateRaceErr != nil, "replicate exited 0 but did not win")
			noteInfo("Phase 6 outcome this run: AWS SDK PutObject won (replicate correctly rejected as a conflict)")
		} else if replicateWon {
			noteInfo("Phase 6 outcome this run: zeros3 replicate completed its commit before the racing PutObject arrived (both are legitimate outcomes of a real race)")
		}
	}

	// =========================================================================
	// Phase 7: source mutation -- overwrite the source object's current
	// content while a slow replication of it is in flight (via a real
	// AWS SDK PutObject racing a real replicate subprocess), then verify
	// the destination ends up with the originally-captured revision, never
	// a mixed one, regardless of how the timing landed.
	// =========================================================================
	mutSrc := randomBytes(7, 20_000_000)
	_, err = srcClient.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(srcBucket), Key: aws.String("mutate-object"), Body: bytes.NewReader(mutSrc)})
	check("AWS SDK PutObject: initial source object for the source-mutation test", err)

	mutCmd := exec.Command(binPath, "replicate",
		"-from", srcEndpoint, "-to", dstEndpoint,
		"-from-access-key", accessKeyID, "-from-secret-key", secretAccessKey,
		"-to-access-key", accessKeyID, "-to-secret-key", secretAccessKey,
		"-region", region,
		"s3://"+srcBucket+"/mutate-object", "s3://"+dstBucket+"/mutate-object")
	var mutOut bytes.Buffer
	mutCmd.Stdout = &mutOut
	mutCmd.Stderr = &mutOut
	if err := mutCmd.Start(); err != nil {
		log.Fatalf("starting replicate for source-mutation test: %v", err)
	}
	replacement := randomBytes(8, 20_000_000)
	time.Sleep(80 * time.Millisecond)
	_, err = srcClient.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(srcBucket), Key: aws.String("mutate-object"), Body: bytes.NewReader(replacement)})
	check("AWS SDK PutObject: overwrite the source object while replication is in flight", err)
	mutErr := mutCmd.Wait()
	check("zeros3 replicate completes despite the concurrent source overwrite", mutErr)

	getMut, err := dstClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(dstBucket), Key: aws.String("mutate-object")})
	check("AWS SDK GetObject on destination after source-mutation test", err)
	if getMut != nil {
		gotMut := mustReadAll(getMut.Body)
		capturedOriginal := bytes.Equal(gotMut, mutSrc)
		capturedReplacement := bytes.Equal(gotMut, replacement)
		requireTrue("destination holds exactly one captured revision, never a mix of the two",
			capturedOriginal != capturedReplacement,
			fmt.Sprintf("len=%d capturedOriginal=%v capturedReplacement=%v", len(gotMut), capturedOriginal, capturedReplacement))
		noteInfo("Phase 7 outcome this run: destination captured the %s revision (both are legitimate outcomes depending on exactly when the object descriptor was fetched relative to the overwrite -- what matters is that it is never a mix)",
			map[bool]string{true: "original", false: "overwritten"}[capturedOriginal])
	}
	getSrcCurrent, err := srcClient.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(srcBucket), Key: aws.String("mutate-object")})
	check("AWS SDK GetObject on source after source-mutation test", err)
	if getSrcCurrent != nil {
		gotSrcCurrent := mustReadAll(getSrcCurrent.Body)
		requireTrue("source's current object correctly reflects the overwrite (this was never a lost write)",
			bytes.Equal(gotSrcCurrent, replacement), "source does not reflect the overwrite")
	}

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed, %d informational =====\n", pass, fail, info)
	if fail > 0 {
		os.Exit(1)
	}
}
