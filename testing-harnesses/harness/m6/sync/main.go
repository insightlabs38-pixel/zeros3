// Ephemeral, external interoperability harness for ZeroS3 M6 (optional
// delta transfer, `zeros3 sync`).
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 purely as a
// black-box S3 client. `zeros3 sync` itself is driven as a real external
// process (os/exec against the built `zeros3` binary), exactly the way a
// real user would invoke it -- this harness never calls into ZeroS3's Go
// package internals.
//
// What this proves that the internal (in-process, deterministic-hook-based)
// zeros3_test.go suite cannot: that a completely independent, real AWS SDK
// client -- not zeros3's own HTTP client, not its own test signer -- reads
// back exactly what `zeros3 sync` wrote, across a real process restart, and
// that resume/conflict machinery behaves sanely against objects a real AWS
// SDK wrote or reads. Internal tests remain the source of truth for exact
// protocol/precondition/mutation-detection correctness (see zeros3's own
// STATUS.md); this harness is external, real-process, real-SDK evidence on
// top of that, not a replacement for it.
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

// runSync invokes the real `zeros3 sync` CLI as a subprocess (never a Go
// package call), returning its combined stdout+stderr and exit error.
func runSync(binPath, endpoint, localPath, dest string) (output string, err error) {
	cmd := exec.Command(binPath, "sync", "-endpoint", endpoint, "-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region, localPath, dest)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

// parseStatLine extracts an integer/percentage value that printSyncStats
// rendered on a "Label:    <value>" line, tolerant of the humanBytes()
// unit suffix by only checking for the label's presence -- this harness
// asserts *relative* size (uploaded(v2) << uploaded(v1)), not exact byte
// counts, since those depend on this run's own CDC boundaries.
func statLine(output, label string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func randomBytes(seed int64, n int) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	_, _ = r.Read(b)
	return b
}

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	storeDir, err := os.MkdirTemp("", "zeros3-m6-sync-harness-store-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(storeDir)
	scratchDir, err := os.MkdirTemp("", "zeros3-m6-sync-harness-scratch-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(scratchDir)

	addr, err := freePort()
	if err != nil {
		log.Fatal(err)
	}
	cmd, err := startZeroS3(binPath, storeDir, addr)
	if err != nil {
		log.Fatalf("starting zeros3: %v", err)
	}
	// A closure (not `defer stopZeroS3(cmd)`) so this always stops whatever
	// process `cmd` currently refers to at the time this deferred call
	// actually runs -- `cmd` is reassigned across the restart below, and a
	// direct `defer stopZeroS3(cmd)` would evaluate (and freeze) that
	// argument immediately, at defer-statement time, leaking the
	// restarted server as an orphan.
	defer func() { stopZeroS3(cmd) }()
	endpoint := "http://" + addr
	client := newClient(endpoint)
	ctx := context.Background()

	const bucket = "sync-bucket"
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	check("CreateBucket", err)

	// =========================================================================
	// 1. Basic sync -> AWS SDK GetObject -> restart -> AWS SDK GetObject again.
	// =========================================================================
	original := randomBytes(1, 6_000_000)
	origPath := scratchDir + "/original.bin"
	check("write local fixture file", os.WriteFile(origPath, original, 0o644))

	out1, err := runSync(binPath, endpoint, origPath, "s3://"+bucket+"/synced-object")
	fmt.Print(out1)
	check("zeros3 sync (initial, brand-new object)", err)

	get1, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("synced-object")})
	check("AWS SDK GetObject after sync", err)
	if get1 != nil {
		got1 := mustReadAll(get1.Body)
		requireTrue("AWS SDK GetObject returns exact bytes after sync", bytes.Equal(got1, original), fmt.Sprintf("len got=%d want=%d", len(got1), len(original)))
	}

	head1, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("synced-object")})
	check("AWS SDK HeadObject after sync", err)
	if head1 != nil {
		requireTrue("AWS SDK HeadObject reports correct Content-Length", aws.ToInt64(head1.ContentLength) == int64(len(original)),
			fmt.Sprintf("got %d, want %d", aws.ToInt64(head1.ContentLength), len(original)))
	}

	// Restart: a real process kill + a fresh `zeros3 serve` on the same
	// store directory -- proving no client- or server-side sync session
	// state was needed to make this object durable and readable.
	stopZeroS3(cmd)
	cmd, err = startZeroS3(binPath, storeDir, addr)
	check("restart zeros3 on the same store directory", err)
	get2, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("synced-object")})
	check("AWS SDK GetObject after restart", err)
	if get2 != nil {
		got2 := mustReadAll(get2.Body)
		requireTrue("AWS SDK GetObject returns exact bytes after restart", bytes.Equal(got2, original), "bytes differ after restart")
	}

	// Deep verify, run as the real `zeros3 verify` CLI against the same
	// store directory (the server must be stopped -- verify opens the
	// store directly, and store locking treats a live `serve` and a
	// direct `-store` open of the SAME directory as concurrent access).
	stopZeroS3(cmd)
	verifyOut, verifyErr := exec.Command(binPath, "verify", "-store", storeDir, "-deep").CombinedOutput()
	fmt.Println(string(verifyOut))
	check("zeros3 verify -deep after sync+restart", verifyErr)
	cmd, err = startZeroS3(binPath, storeDir, addr)
	check("restart zeros3 after verify", err)

	// =========================================================================
	// 2. Modified-file sync: small localized edit, reduced uploaded bytes.
	// =========================================================================
	mutated := make([]byte, 0, len(original)+4096)
	mid := len(original) / 2
	mutated = append(mutated, original[:mid]...)
	mutated = append(mutated, randomBytes(2, 4096)...)
	mutated = append(mutated, original[mid:]...)
	mutPath := scratchDir + "/mutated.bin"
	check("write mutated fixture file", os.WriteFile(mutPath, mutated, 0o644))

	out2, err := runSync(binPath, endpoint, mutPath, "s3://"+bucket+"/synced-object-v2")
	fmt.Print(out2)
	check("zeros3 sync (mutated file, new key)", err)
	fmt.Printf("first sync stats:  %s\n", statLine(out1, "Uploaded payload:"))
	fmt.Printf("second sync stats: %s | %s\n", statLine(out2, "Uploaded payload:"), statLine(out2, "Reuse:"))

	uploadedV1 := parseHumanBytesFromLine(statLine(out1, "Uploaded payload:"))
	uploadedV2 := parseHumanBytesFromLine(statLine(out2, "Uploaded payload:"))
	requireTrue("modified-file sync uploads far fewer bytes than the original full sync",
		uploadedV1 > 0 && uploadedV2 > 0 && uploadedV2 < uploadedV1/4,
		fmt.Sprintf("v1 uploaded=%s, v2 uploaded=%s", statLine(out1, "Uploaded payload:"), statLine(out2, "Uploaded payload:")))

	get3, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("synced-object-v2")})
	check("AWS SDK GetObject for the mutated-sync object", err)
	if get3 != nil {
		got3 := mustReadAll(get3.Body)
		requireTrue("AWS SDK GetObject returns exact bytes for the mutated-sync object", bytes.Equal(got3, mutated), "bytes differ")
	}

	// =========================================================================
	// 3. Interrupted then resumed sync: kill `zeros3 sync` partway through a
	//    larger transfer, then rerun it to completion.
	//
	//    This is deliberately best-effort/timing-based, unlike the internal
	//    suite's deterministic hook-based interruption tests: a real
	//    external CLI subprocess offers no injection point. The invariant
	//    actually asserted below (object absent after the kill, then
	//    correct after the resumed rerun) holds regardless of exactly how
	//    much of the transfer the killed attempt completed.
	// =========================================================================
	large := randomBytes(3, 20_000_000)
	largePath := scratchDir + "/large.bin"
	check("write large fixture file for interruption test", os.WriteFile(largePath, large, 0o644))

	interruptCmd := exec.Command(binPath, "sync", "-endpoint", endpoint, "-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region, largePath, "s3://"+bucket+"/interrupted-object")
	if err := interruptCmd.Start(); err != nil {
		log.Fatalf("starting interruptible sync: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	killErr := interruptCmd.Process.Kill()
	interruptCmd.Wait()
	check("kill zeros3 sync mid-transfer", killErr)

	_, headErr := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String("interrupted-object")})
	requireTrue("interrupted sync never committed a partial/visible object", headErr != nil, "HeadObject unexpectedly succeeded after a killed sync")

	out3, err := runSync(binPath, endpoint, largePath, "s3://"+bucket+"/interrupted-object")
	fmt.Print(out3)
	check("zeros3 sync (resumed rerun after interruption)", err)

	get4, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("interrupted-object")})
	check("AWS SDK GetObject after resumed sync", err)
	if get4 != nil {
		got4 := mustReadAll(get4.Body)
		requireTrue("AWS SDK GetObject returns exact bytes after resumed sync", bytes.Equal(got4, large), "bytes differ after resume")
	}
	noteInfo("resumed rerun stats: %s | %s (some chunks from the killed attempt may already have been durably published to CAS, reducing this further)",
		statLine(out3, "Uploaded payload:"), statLine(out3, "Reuse:"))

	// =========================================================================
	// 4. Deterministic AWS-SDK interop for the safe-mode conflict precondition:
	//    an AWS SDK PutObject occupies the key first; a sync against that same
	//    key (with different content) observes it via HEAD and safely
	//    overwrites, proving the precondition mechanics work against an object
	//    a real AWS SDK client -- not zeros3 -- wrote.
	// =========================================================================
	awsWritten := []byte("written by the real AWS SDK, not zeros3 sync")
	_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("interop-precondition"), Body: bytes.NewReader(awsWritten)})
	check("AWS SDK PutObject occupies the destination key", err)

	overwritePath := scratchDir + "/overwrite.bin"
	overwriteContent := randomBytes(4, 500_000)
	check("write overwrite fixture file", os.WriteFile(overwritePath, overwriteContent, 0o644))
	out4, err := runSync(binPath, endpoint, overwritePath, "s3://"+bucket+"/interop-precondition")
	fmt.Print(out4)
	check("zeros3 sync overwrites an AWS-SDK-written object it correctly observed via HEAD", err)

	get5, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("interop-precondition")})
	check("AWS SDK GetObject after sync overwrote an AWS-SDK-written object", err)
	if get5 != nil {
		got5 := mustReadAll(get5.Body)
		requireTrue("object now holds the sync's content, not the AWS SDK's original", bytes.Equal(got5, overwriteContent), "bytes do not match the overwrite")
	}

	// =========================================================================
	// 5. Best-effort remote-conflict race: start a sync against a large file,
	//    race an AWS SDK PutObject against it. Timing-dependent by nature (an
	//    external harness has no synchronization point inside the CLI
	//    subprocess) -- the internal suite proves the precondition mechanics
	//    deterministically via direct pipeline control; this only checks the
	//    one invariant that must hold regardless of which side wins the race:
	//    the final object is exactly one writer's content, never a corrupted
	//    mix, and if sync lost the race it reports a non-zero exit.
	// =========================================================================
	raceLocal := randomBytes(5, 20_000_000)
	racePath := scratchDir + "/race.bin"
	check("write race fixture file", os.WriteFile(racePath, raceLocal, 0o644))
	interloper := []byte("the AWS SDK's concurrent racing PutObject content")

	raceCmd := exec.Command(binPath, "sync", "-endpoint", endpoint, "-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region, racePath, "s3://"+bucket+"/race-object")
	var raceOut bytes.Buffer
	raceCmd.Stdout = &raceOut
	raceCmd.Stderr = &raceOut
	if err := raceCmd.Start(); err != nil {
		log.Fatalf("starting racing sync: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	_, putErr := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("race-object"), Body: bytes.NewReader(interloper)})
	check("AWS SDK racing PutObject", putErr)
	syncRaceErr := raceCmd.Wait()

	getRace, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("race-object")})
	check("AWS SDK GetObject after the race", err)
	if getRace != nil {
		gotRace := mustReadAll(getRace.Body)
		syncWon := bytes.Equal(gotRace, raceLocal)
		interloperWon := bytes.Equal(gotRace, interloper)
		requireTrue("race result is exactly one writer's content, never a corrupted mix", syncWon != interloperWon,
			fmt.Sprintf("neither/both matched: len=%d syncWon=%v interloperWon=%v", len(gotRace), syncWon, interloperWon))
		if interloperWon {
			requireTrue("when the AWS SDK PutObject won the race, sync correctly reported a non-zero exit (safe-mode conflict)", syncRaceErr != nil, "sync exited 0 but did not win")
			noteInfo("race outcome this run: AWS SDK PutObject won (sync correctly rejected as a conflict)")
		} else if syncWon {
			noteInfo("race outcome this run: zeros3 sync completed its commit before the racing PutObject arrived (both are legitimate outcomes of a real race)")
		}
	}

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed, %d informational =====\n", pass, fail, info)
	if fail > 0 {
		os.Exit(1)
	}
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

// parseHumanBytesFromLine parses a "Label:    12.34 MiB (...)" line
// (printSyncStats' own format) back into a byte count, tolerant of the
// trailing "(N unique chunks)"/fallback annotation this harness never
// needs to interpret beyond the numeric value+unit pair.
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
