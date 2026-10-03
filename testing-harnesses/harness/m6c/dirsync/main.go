// Ephemeral, external interoperability harness for ZeroS3 M6C (recursive
// directory sync, `zeros3 sync LOCAL_DIRECTORY s3://bucket/prefix/`).
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
// client -- not zeros3's own HTTP client, not its own test signer -- lists
// and reads back exactly the objects a real `zeros3 sync` directory run
// produced, across a real process restart, that a locally-deleted file's
// remote object survives untouched, and that a real subprocess-level
// partial failure/conflict still leaves unrelated objects committed and
// correct while the process itself exits non-zero. Internal tests remain
// the source of truth for exact per-file protocol/precondition/mutation-
// detection correctness (see zeros3's own STATUS.md, "M6C" section); this
// harness is external, real-process, real-SDK evidence on top of that, not
// a replacement for it.
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

// runDirSync invokes the real `zeros3 sync LOCAL_DIRECTORY s3://...` CLI as
// a subprocess (never a Go package call), returning its combined
// stdout+stderr and exit error/exit-code.
func runDirSync(binPath, endpoint, localDir, dest string) (output string, exitCode int, err error) {
	cmd := exec.Command(binPath, "sync", "-endpoint", endpoint, "-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region, localDir, dest)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	exitCode = 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		}
	}
	return buf.String(), exitCode, err
}

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

func writeFixtureFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
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

// listAllKeys drives a real AWS SDK ListObjectsV2 paginator (proving the
// full listing path, not just individual GetObject calls) and returns the
// sorted set of keys under prefix.
func listAllKeys(ctx context.Context, client *s3.Client, bucket string) ([]string, error) {
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			keys = append(keys, aws.ToString(obj.Key))
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	storeDir, err := os.MkdirTemp("", "zeros3-m6c-dirsync-harness-store-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(storeDir)
	scratchDir, err := os.MkdirTemp("", "zeros3-m6c-dirsync-harness-scratch-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(scratchDir)
	treeDir := filepath.Join(scratchDir, "tree")

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
	// actually runs -- `cmd` is reassigned across the restarts below.
	defer func() { stopZeroS3(cmd) }()
	endpoint := "http://" + addr
	client := newClient(endpoint)
	ctx := context.Background()

	const bucket = "dirsync-bucket"
	const destPrefix = "dirsync/"
	dest := "s3://" + bucket + "/" + destPrefix
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	check("CreateBucket", err)

	// =========================================================================
	// Phase 1: deterministic fixture tree, initial directory sync, AWS SDK
	// ListObjectsV2 + GetObject round trip on every produced key.
	//
	//   tree/
	//     alpha.bin              (large enough for CDC/delta behavior to matter)
	//     text.txt
	//     nested/bravo.bin
	//     nested/unicode-✓.txt
	//     nested/deeper/charlie.bin
	// =========================================================================
	alphaOriginal := randomBytes(101, 3_000_000)
	textOriginal := []byte("a small text fixture file, unmodified for the whole run\n")
	bravoOriginal := randomBytes(102, 400_000)
	unicodeOriginal := []byte("unicode filename fixture -- ✓ -- content\n")
	charlieOriginal := randomBytes(103, 50_000)

	check("write alpha.bin", writeFixtureFile(filepath.Join(treeDir, "alpha.bin"), alphaOriginal))
	check("write text.txt", writeFixtureFile(filepath.Join(treeDir, "text.txt"), textOriginal))
	check("write nested/bravo.bin", writeFixtureFile(filepath.Join(treeDir, "nested/bravo.bin"), bravoOriginal))
	check("write nested/unicode-✓.txt", writeFixtureFile(filepath.Join(treeDir, "nested/unicode-✓.txt"), unicodeOriginal))
	check("write nested/deeper/charlie.bin", writeFixtureFile(filepath.Join(treeDir, "nested/deeper/charlie.bin"), charlieOriginal))

	out1, code1, err := runDirSync(binPath, endpoint, treeDir, dest)
	fmt.Print(out1)
	check("zeros3 sync (initial directory sync, brand-new tree)", err)
	requireTrue("initial directory sync exits zero", code1 == 0, fmt.Sprintf("exit code %d", code1))
	requireTrue("initial directory sync summary reports 5/5 files", strings.Contains(out1, "Files discovered:  5") && strings.Contains(out1, "Files synced:      5"), out1)

	expectedKeysPhase1 := []string{
		destPrefix + "alpha.bin",
		destPrefix + "nested/bravo.bin",
		destPrefix + "nested/deeper/charlie.bin",
		destPrefix + "nested/unicode-✓.txt",
		destPrefix + "text.txt",
	}
	sort.Strings(expectedKeysPhase1)
	listedKeys, err := listAllKeys(ctx, client, bucket)
	check("AWS SDK ListObjectsV2 after initial sync", err)
	requireTrue("ListObjectsV2 returns exactly the expected 5 keys", equalStringSlices(listedKeys, expectedKeysPhase1),
		fmt.Sprintf("got %v, want %v", listedKeys, expectedKeysPhase1))

	contentByKey := map[string][]byte{
		destPrefix + "alpha.bin":                 alphaOriginal,
		destPrefix + "text.txt":                  textOriginal,
		destPrefix + "nested/bravo.bin":          bravoOriginal,
		destPrefix + "nested/unicode-✓.txt":      unicodeOriginal,
		destPrefix + "nested/deeper/charlie.bin": charlieOriginal,
	}
	for _, key := range expectedKeysPhase1 {
		got, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		check("AWS SDK GetObject "+key, err)
		if got != nil {
			gotBytes := mustReadAll(got.Body)
			requireTrue("exact byte equality for "+key, bytes.Equal(gotBytes, contentByKey[key]),
				fmt.Sprintf("len got=%d want=%d", len(gotBytes), len(contentByKey[key])))
		}
	}

	// =========================================================================
	// Phase 2: modify a small region of alpha.bin, add a brand-new file, leave
	// everything else untouched. Rerun directory sync; verify every file's
	// content and that the new file appears; the aggregate reuse percentage
	// must be meaningful (most of the tree, and most of alpha.bin itself, is
	// unchanged).
	// =========================================================================
	alphaModified := make([]byte, 0, len(alphaOriginal)+8192)
	mid := len(alphaOriginal) / 2
	alphaModified = append(alphaModified, alphaOriginal[:mid]...)
	alphaModified = append(alphaModified, randomBytes(201, 8192)...)
	alphaModified = append(alphaModified, alphaOriginal[mid:]...)
	check("rewrite alpha.bin with a small localized edit", writeFixtureFile(filepath.Join(treeDir, "alpha.bin"), alphaModified))

	newFileContent := randomBytes(202, 20_000)
	check("write new file nested/deeper/newfile.bin", writeFixtureFile(filepath.Join(treeDir, "nested/deeper/newfile.bin"), newFileContent))

	out2, code2, err := runDirSync(binPath, endpoint, treeDir, dest)
	fmt.Print(out2)
	check("zeros3 sync (modified tree: 1 edited file + 1 new file)", err)
	requireTrue("modified-tree directory sync exits zero", code2 == 0, fmt.Sprintf("exit code %d", code2))
	requireTrue("modified-tree summary reports 6/6 files", strings.Contains(out2, "Files discovered:  6") && strings.Contains(out2, "Files synced:      6"), out2)
	noteInfo("phase 2 aggregate stats: %s | %s", statLine(out2, "Uploaded payload:"), statLine(out2, "Reuse:"))

	reuseLine := statLine(out2, "Reuse:")
	reusePct := parseTrailingPercent(reuseLine)
	requireTrue("aggregate reuse is meaningful (>=50%) when only a small edit touched one file among an otherwise-unchanged tree",
		reusePct >= 50.0, fmt.Sprintf("reuse line: %q", reuseLine))

	expectedKeysPhase2 := append(append([]string{}, expectedKeysPhase1...), destPrefix+"nested/deeper/newfile.bin")
	sort.Strings(expectedKeysPhase2)
	listedKeys2, err := listAllKeys(ctx, client, bucket)
	check("AWS SDK ListObjectsV2 after modified-tree sync", err)
	requireTrue("ListObjectsV2 returns exactly the expected 6 keys", equalStringSlices(listedKeys2, expectedKeysPhase2),
		fmt.Sprintf("got %v, want %v", listedKeys2, expectedKeysPhase2))

	gotAlpha, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(destPrefix + "alpha.bin")})
	check("AWS SDK GetObject alpha.bin after edit", err)
	if gotAlpha != nil {
		requireTrue("alpha.bin holds the modified content", bytes.Equal(mustReadAll(gotAlpha.Body), alphaModified), "bytes differ")
	}
	gotNew, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(destPrefix + "nested/deeper/newfile.bin")})
	check("AWS SDK GetObject newfile.bin", err)
	if gotNew != nil {
		requireTrue("newfile.bin holds the expected content", bytes.Equal(mustReadAll(gotNew.Body), newFileContent), "bytes differ")
	}
	gotBravoUnchanged, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(destPrefix + "nested/bravo.bin")})
	check("AWS SDK GetObject bravo.bin (should be unaffected by alpha.bin's edit)", err)
	if gotBravoUnchanged != nil {
		requireTrue("bravo.bin is unaffected by an unrelated file's edit", bytes.Equal(mustReadAll(gotBravoUnchanged.Body), bravoOriginal), "bytes differ")
	}

	// =========================================================================
	// Phase 3: non-destructive local deletion. Delete text.txt locally, rerun
	// directory sync, and prove via the AWS SDK that the remote object still
	// exists with its original content -- directory sync never deletes.
	// =========================================================================
	check("delete local text.txt", os.Remove(filepath.Join(treeDir, "text.txt")))

	out3, code3, err := runDirSync(binPath, endpoint, treeDir, dest)
	fmt.Print(out3)
	check("zeros3 sync (after local deletion of text.txt)", err)
	requireTrue("post-deletion directory sync exits zero", code3 == 0, fmt.Sprintf("exit code %d", code3))
	requireTrue("post-deletion summary reports 5 discovered (text.txt no longer local)", strings.Contains(out3, "Files discovered:  5"), out3)

	gotText, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(destPrefix + "text.txt")})
	check("AWS SDK GetObject text.txt after local deletion", err)
	if gotText != nil {
		requireTrue("text.txt's remote object survives byte-for-byte after its local file was deleted (non-destructive semantics)",
			bytes.Equal(mustReadAll(gotText.Body), textOriginal), "bytes differ -- remote object was altered or lost")
	}
	listedKeys3, err := listAllKeys(ctx, client, bucket)
	check("AWS SDK ListObjectsV2 after local deletion", err)
	requireTrue("text.txt's key is still listed even though it no longer exists locally",
		containsString(listedKeys3, destPrefix+"text.txt"), fmt.Sprintf("listing: %v", listedKeys3))

	// =========================================================================
	// Phase 4: real restart. Kill zeros3, start a fresh `zeros3 serve` on the
	// same store directory, and prove every key from phase 2/3 is still
	// correctly retrievable, plus a real `zeros3 verify -deep` pass.
	// =========================================================================
	stopZeroS3(cmd)
	cmd, err = startZeroS3(binPath, storeDir, addr)
	check("restart zeros3 on the same store directory", err)

	listedKeys4, err := listAllKeys(ctx, client, bucket)
	check("AWS SDK ListObjectsV2 after restart", err)
	requireTrue("listing is unchanged after restart", equalStringSlices(listedKeys4, expectedKeysPhase2),
		fmt.Sprintf("got %v, want %v", listedKeys4, expectedKeysPhase2))
	for _, key := range expectedKeysPhase2 {
		got, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		check("AWS SDK GetObject "+key+" after restart", err)
		if got == nil {
			continue
		}
		gotBytes := mustReadAll(got.Body)
		var want []byte
		switch key {
		case destPrefix + "alpha.bin":
			want = alphaModified
		case destPrefix + "nested/deeper/newfile.bin":
			want = newFileContent
		case destPrefix + "text.txt":
			want = textOriginal
		default:
			want = contentByKey[key]
		}
		requireTrue("exact byte equality for "+key+" after restart", bytes.Equal(gotBytes, want), "bytes differ after restart")
	}

	stopZeroS3(cmd)
	verifyOut, verifyErr := exec.Command(binPath, "verify", "-store", storeDir, "-deep").CombinedOutput()
	fmt.Println(string(verifyOut))
	check("zeros3 verify -deep after directory sync + restart", verifyErr)
	cmd, err = startZeroS3(binPath, storeDir, addr)
	check("restart zeros3 after verify", err)

	// =========================================================================
	// Phase 5: partial failure/conflict. Modify two unrelated files (a small
	// existing one and a brand-new large one), start the directory sync in the
	// background, and race a real AWS SDK PutObject directly against the large
	// file's destination key while that one file's own transfer is still in
	// flight. This is deliberately best-effort/timing-based (an external
	// harness has no injection point inside the CLI subprocess, unlike the
	// internal suite's deterministic test hook) -- what's actually asserted
	// below holds regardless of which side wins: the raced object holds
	// exactly one writer's content, never a mix, and the unrelated file's own
	// edit is correctly committed and retrievable either way.
	// =========================================================================
	bravoModified := append(append([]byte{}, bravoOriginal...), []byte("-phase5-edit")...)
	check("rewrite nested/bravo.bin with an unrelated small edit", writeFixtureFile(filepath.Join(treeDir, "nested/bravo.bin"), bravoModified))
	raceContent := randomBytes(301, 64_000_000)
	check("write large racing fixture zzz-race.bin (sorts last, gives the race a window)", writeFixtureFile(filepath.Join(treeDir, "zzz-race.bin"), raceContent))
	interloper := []byte("written directly by the real AWS SDK, racing the directory sync's own commit of this same key")

	raceCmd := exec.Command(binPath, "sync", "-endpoint", endpoint, "-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region, treeDir, dest)
	var raceOut bytes.Buffer
	raceCmd.Stdout = &raceOut
	raceCmd.Stderr = &raceOut
	if err := raceCmd.Start(); err != nil {
		log.Fatalf("starting racing directory sync: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	_, putErr := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(destPrefix + "zzz-race.bin"), Body: bytes.NewReader(interloper)})
	check("AWS SDK racing PutObject against zzz-race.bin", putErr)
	syncRaceErr := raceCmd.Wait()
	fmt.Print(raceOut.String())

	gotRace, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(destPrefix + "zzz-race.bin")})
	check("AWS SDK GetObject zzz-race.bin after the race", err)
	if gotRace != nil {
		gotRaceBytes := mustReadAll(gotRace.Body)
		syncWon := bytes.Equal(gotRaceBytes, raceContent)
		interloperWon := bytes.Equal(gotRaceBytes, interloper)
		requireTrue("raced object holds exactly one writer's content, never a corrupted mix", syncWon != interloperWon,
			fmt.Sprintf("neither/both matched: len=%d syncWon=%v interloperWon=%v", len(gotRaceBytes), syncWon, interloperWon))
		if interloperWon {
			requireTrue("when the AWS SDK PutObject won the race, the directory sync process reported a non-zero exit", syncRaceErr != nil, "sync exited 0 but did not win the race")
			requireTrue("the summary reports the raced file as failed, not silently as full success", strings.Contains(raceOut.String(), "directory sync completed with errors"), raceOut.String())
			noteInfo("phase 5 race outcome this run: AWS SDK PutObject won (zzz-race.bin correctly reported as a failed/conflicted file)")
		} else {
			noteInfo("phase 5 race outcome this run: zeros3 sync committed zzz-race.bin before the racing PutObject arrived (both are legitimate outcomes of a real race)")
		}
	}

	// The unrelated edited file must be correct regardless of which side won
	// the race on a completely different key -- proving partial-failure
	// isolation via a real subprocess, not just the in-process test suite.
	gotBravo5, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(destPrefix + "nested/bravo.bin")})
	check("AWS SDK GetObject bravo.bin after phase 5", err)
	if gotBravo5 != nil {
		requireTrue("bravo.bin's own edit committed correctly regardless of the unrelated zzz-race.bin race outcome",
			bytes.Equal(mustReadAll(gotBravo5.Body), bravoModified), "bytes differ -- an unrelated file's race incorrectly affected this file")
	}

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed, %d informational =====\n", pass, fail, info)
	if fail > 0 {
		os.Exit(1)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// parseTrailingPercent parses a "Reuse:               98.3%" line (printSyncStats'
// own format) back into a float64 percentage.
func parseTrailingPercent(line string) float64 {
	line = strings.TrimSpace(line)
	if !strings.HasSuffix(line, "%") {
		return -1
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return -1
	}
	last := strings.TrimSuffix(fields[len(fields)-1], "%")
	val, err := strconv.ParseFloat(last, 64)
	if err != nil {
		return -1
	}
	return val
}
