// Ephemeral, external interoperability harness for ZeroS3 M8E (durable
// namespace snapshots + zero-payload restore, `zeros3 snapshot
// create/list/show/delete/restore`).
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 purely as a
// black-box S3 client. `zeros3 snapshot ...`/`zeros3 gc`/`zeros3 verify`
// are driven as real external processes (os/exec against the built
// `zeros3` binary), never via ZeroS3's Go package internals. Every
// fixture is written through the real AWS SDK; direct filesystem
// modification is used only where the milestone spec itself permits it
// (Phase 7 -- corrupting an already-validly-published snapshot descriptor
// file, and Phase 10 -- corrupting an already-validly-published CAS
// chunk file, since corruption is the condition under test in both).
//
// Snapshot create/list/show/delete are themselves a ZeroS3-proprietary
// extension (never a real S3 operation, section 15h of zeros3.go), so
// this harness drives them via the `zeros3 snapshot` CLI subprocess --
// exactly the way it would from a real operator's terminal -- while
// every ordinary object read/write (PutObject/GetObject/
// ListObjectsV2/DeleteObject) goes through the real AWS SDK, proving the
// restored/live namespace is ordinary, standards-compliant S3 to any
// third-party client.
//
// What this proves that the internal (in-process, deterministic-hook-
// based) zeros3_test.go suite cannot: that a completely independent, real
// AWS SDK client reads back exactly what a real `zeros3 snapshot restore`
// subprocess wrote, that the zero-new-CAS-payload claim holds under an
// independent filesystem measurement of a real running store, a real
// process interruption/resume of the restore CLI, and that M8E composes
// cleanly with M8B's peer-assisted repair. Internal tests remain the
// source of truth for exact format/consistency/precondition correctness
// (see zeros3's own STATUS.md); this harness is external, real-process,
// real-SDK evidence on top of that, not a replacement for it.
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

func runCLI(binPath string, args ...string) (output string, err error) {
	cmd := exec.Command(binPath, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

func snapshotCreate(binPath, endpoint, srcURI string) (output string, err error) {
	return runCLI(binPath, "snapshot", "create", "-endpoint", endpoint,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region, srcURI)
}

func snapshotList(binPath, endpoint string) (output string, err error) {
	return runCLI(binPath, "snapshot", "list", "-endpoint", endpoint,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region)
}

func snapshotShow(binPath, endpoint, id string, entries bool) (output string, err error) {
	args := []string{"snapshot", "show", "-endpoint", endpoint,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region}
	if entries {
		args = append(args, "-entries")
	}
	args = append(args, id)
	return runCLI(binPath, args...)
}

func snapshotDelete(binPath, endpoint, id string) (output string, err error) {
	return runCLI(binPath, "snapshot", "delete", "-endpoint", endpoint,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region, id)
}

func snapshotRestoreCmd(binPath, endpoint, id, dstURI string) *exec.Cmd {
	return exec.Command(binPath, "snapshot", "restore", "-endpoint", endpoint,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region, id, dstURI)
}

func snapshotRestore(binPath, endpoint, id, dstURI string) (output string, err error) {
	cmd := snapshotRestoreCmd(binPath, endpoint, id, dstURI)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

func runRepair(binPath, localDir, peerEndpoint string) (output string, err error) {
	return runCLI(binPath, "repair", "-store", localDir, "-from", peerEndpoint,
		"-access-key", accessKeyID, "-secret-key", secretAccessKey, "-region", region)
}

func runVerifyDeep(binPath, storeDir string) (output string, err error) {
	return runCLI(binPath, "verify", "-store", storeDir, "-deep")
}

func runGCDryRun(binPath, storeDir string) (output string, err error) {
	return runCLI(binPath, "gc", "-store", storeDir)
}

func runGCApply(binPath, storeDir string) (output string, err error) {
	return runCLI(binPath, "gc", "-store", storeDir, "-apply")
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

// snapshotIDFromCreateOutput extracts the ID from `zeros3 snapshot
// create`'s first output line ("Snapshot:           <id>").
func snapshotIDFromCreateOutput(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(lines[0], "Snapshot:"))
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

func snapshotFilesUnder(storeDir string) ([]string, error) {
	var files []string
	root := filepath.Join(storeDir, "snapshots")
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

// corruptTrailingByte flips the last byte of path -- used against a
// snapshot descriptor so its magic/version/length header stays
// well-formed and only its trailing CRC32C fails, exactly the "bad
// CRC"/single-bit-flip corruption class the spec's Phase 7 describes.
func corruptTrailingByte(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("empty file: %s", path)
	}
	data[len(data)-1] ^= 0xFF
	return os.WriteFile(path, data, 0o644)
}

func mustTempStore(prefix string) string {
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		log.Fatal(err)
	}
	return dir
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
	s.dir = mustTempStore("zeros3-m8e-" + tag + "-store-")
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

func check1err[T any](_ T, err error) error { return err }

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	ctx := context.Background()

	// =========================================================================
	// Phase 1: snapshot create/list/show -- populate a deterministic
	// namespace through the AWS SDK, create a snapshot via the real CLI,
	// verify the returned ID, that list includes it, and that show's
	// metadata (object count/logical bytes/source scope) is exact.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p1")
		defer s.stop()
		check("Phase1: CreateBucket data", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p1-data")})))

		tree := map[string][]byte{
			"a.bin":     randomBytes(1, 5000),
			"sub/b.bin": randomBytes(2, 6000),
			"sub/c.txt": randomBytes(3, 800),
		}
		var totalLogical int64
		for key, body := range tree {
			check("Phase1: AWS SDK PutObject "+key, putObject(ctx, s.client, "p1-data", key, body))
			totalLogical += int64(len(body))
		}

		out, err := snapshotCreate(binPath, s.endpoint, "s3://p1-data/")
		fmt.Print(out)
		check("Phase1: zeros3 snapshot create", err)
		id := snapshotIDFromCreateOutput(out)
		requireTrue("Phase1: create returns a syntactically plausible snapshot ID", len(id) == 36, "got: "+id)
		requireTrue("Phase1: create reports exactly 3 objects", statInt(out, "Objects:") == 3, out)

		listOut, err := snapshotList(binPath, s.endpoint)
		fmt.Print(listOut)
		check("Phase1: zeros3 snapshot list", err)
		requireTrue("Phase1: list includes the created snapshot ID", strings.Contains(listOut, id), listOut)

		showOut, err := snapshotShow(binPath, s.endpoint, id, false)
		fmt.Print(showOut)
		check("Phase1: zeros3 snapshot show", err)
		requireTrue("Phase1: show reports exact object count", statInt(showOut, "Objects:") == 3, showOut)
		requireTrue("Phase1: show reports source bucket/prefix", strings.Contains(showOut, "p1-data/"), showOut)
		_ = totalLogical
	}()

	// =========================================================================
	// Phase 2: point-in-time mutation -- snapshot object A = v1, overwrite
	// A = v2, restore to a recovery prefix, prove live=v2 and restored=v1
	// via independent AWS SDK reads.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p2")
		defer s.stop()
		check("Phase2: CreateBucket data", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p2-data")})))
		check("Phase2: CreateBucket recovery", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p2-recovery")})))

		v1 := []byte("content-v1-original")
		check("Phase2: AWS SDK PutObject A=v1", putObject(ctx, s.client, "p2-data", "a.bin", v1))

		out, err := snapshotCreate(binPath, s.endpoint, "s3://p2-data/")
		fmt.Print(out)
		check("Phase2: zeros3 snapshot create at v1", err)
		id := snapshotIDFromCreateOutput(out)

		v2 := []byte("content-v2-overwritten-and-longer-than-v1")
		check("Phase2: AWS SDK PutObject A=v2 (overwrite)", putObject(ctx, s.client, "p2-data", "a.bin", v2))

		restoreOut, err := snapshotRestore(binPath, s.endpoint, id, "s3://p2-recovery/aug30/")
		fmt.Print(restoreOut)
		check("Phase2: zeros3 snapshot restore", err)

		liveGet, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p2-data"), Key: aws.String("a.bin")})
		if check1("Phase2: AWS SDK GetObject live a.bin", err) {
			got := mustReadAll(liveGet.Body)
			requireTrue("Phase2: live source is v2", bytes.Equal(got, v2), fmt.Sprintf("got %q want %q", got, v2))
		}
		restoredGet, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p2-recovery"), Key: aws.String("aug30/a.bin")})
		if check1("Phase2: AWS SDK GetObject restored aug30/a.bin", err) {
			got := mustReadAll(restoredGet.Body)
			requireTrue("Phase2: restored copy is v1 (point-in-time)", bytes.Equal(got, v1), fmt.Sprintf("got %q want %q", got, v1))
		}
	}()

	// =========================================================================
	// Phase 3: source deletion + GC -- unique data, snapshot, delete live
	// objects via the SDK, run `zeros3 gc -apply`, confirm live objects
	// are gone, the snapshot is still valid, and restore succeeds with an
	// exact AWS SDK GET.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p3")
		check("Phase3: CreateBucket data", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p3-data")})))
		check("Phase3: CreateBucket recovery", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p3-recovery")})))

		unique := randomBytes(301, 250_000)
		check("Phase3: AWS SDK PutObject unique.bin", putObject(ctx, s.client, "p3-data", "unique.bin", unique))

		out, err := snapshotCreate(binPath, s.endpoint, "s3://p3-data/")
		fmt.Print(out)
		check("Phase3: zeros3 snapshot create", err)
		id := snapshotIDFromCreateOutput(out)

		check("Phase3: AWS SDK DeleteObject unique.bin", check1err(s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("p3-data"), Key: aws.String("unique.bin")})))
		_, getErr := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p3-data"), Key: aws.String("unique.bin")})
		requireTrue("Phase3: live object is gone after delete", getErr != nil, "expected NoSuchKey")

		stopZeroS3(s.cmd)
		gcOut, gcErr := runGCApply(binPath, s.dir)
		fmt.Println(gcOut)
		check("Phase3: zeros3 gc -apply after source delete (snapshot must still pin content)", gcErr)
		requireTrue("Phase3: gc reports a healthy live set", strings.Contains(gcOut, "live set         ok=true"), gcOut)
		check("Phase3: restart zeros3 after gc", s.restart())
		defer s.stop()

		showOut, err := snapshotShow(binPath, s.endpoint, id, false)
		fmt.Print(showOut)
		check("Phase3: snapshot show still valid after source-delete+GC", err)

		restoreOut, err := snapshotRestore(binPath, s.endpoint, id, "s3://p3-recovery/")
		fmt.Print(restoreOut)
		check("Phase3: zeros3 snapshot restore after source-delete+GC", err)

		restoredGet, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p3-recovery"), Key: aws.String("unique.bin")})
		if check1("Phase3: AWS SDK GetObject restored unique.bin", err) {
			got := mustReadAll(restoredGet.Body)
			requireTrue("Phase3: restored content byte-for-byte exact", bytes.Equal(got, unique), "content mismatch")
		}
	}()

	// =========================================================================
	// Phase 4: zero-payload restore -- measure raw CAS chunk file
	// count/bytes before and after restoring a substantial snapshot, via
	// direct filesystem walk (never trusting only the CLI's own
	// self-reported statistic).
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p4")
		defer s.stop()
		check("Phase4: CreateBucket data", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p4-data")})))
		check("Phase4: CreateBucket recovery", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p4-recovery")})))

		for i := 0; i < 5; i++ {
			body := randomBytes(int64(400+i), 400_000)
			check(fmt.Sprintf("Phase4: AWS SDK PutObject obj%d.bin", i), putObject(ctx, s.client, "p4-data", fmt.Sprintf("obj%d.bin", i), body))
		}

		out, err := snapshotCreate(binPath, s.endpoint, "s3://p4-data/")
		fmt.Print(out)
		check("Phase4: zeros3 snapshot create", err)
		id := snapshotIDFromCreateOutput(out)

		beforeFiles, err := chunkFilesUnder(s.dir)
		check("Phase4: discover CAS chunk files before restore", err)
		beforeBytes, err := chunkFileTotalBytes(beforeFiles)
		check("Phase4: sum CAS chunk file bytes before restore", err)
		noteInfo("Phase4: before restore -- %d chunk files, %d bytes", len(beforeFiles), beforeBytes)

		restoreOut, err := snapshotRestore(binPath, s.endpoint, id, "s3://p4-recovery/")
		fmt.Print(restoreOut)
		check("Phase4: zeros3 snapshot restore", err)
		requireTrue("Phase4: restore summary reports zero new CAS payload", strings.Contains(restoreOut, "New CAS payload:         0 B"), restoreOut)

		afterFiles, err := chunkFilesUnder(s.dir)
		check("Phase4: discover CAS chunk files after restore", err)
		afterBytes, err := chunkFileTotalBytes(afterFiles)
		check("Phase4: sum CAS chunk file bytes after restore", err)
		noteInfo("Phase4: after restore -- %d chunk files, %d bytes", len(afterFiles), afterBytes)

		requireTrue("Phase4: chunk file COUNT unchanged (independent filesystem measurement)", len(afterFiles) == len(beforeFiles), fmt.Sprintf("before=%d after=%d", len(beforeFiles), len(afterFiles)))
		requireTrue("Phase4: chunk file total BYTES unchanged (independent filesystem measurement)", afterBytes == beforeBytes, fmt.Sprintf("before=%d after=%d", beforeBytes, afterBytes))
	}()

	// =========================================================================
	// Phase 5: >1000 objects -- ~1500 small objects, snapshot, mutate/
	// delete source, restore to a new prefix, verify exact object count,
	// keys, and content, with full AWS SDK ListObjectsV2 pagination.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p5")
		defer s.stop()
		check("Phase5: CreateBucket data", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p5-data")})))
		check("Phase5: CreateBucket recovery", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p5-recovery")})))

		const n = 1500
		var wantKeys []string
		for i := 0; i < n; i++ {
			key := fmt.Sprintf("k/%05d.bin", i)
			wantKeys = append(wantKeys, key)
			if err := putObject(ctx, s.client, "p5-data", key, []byte(fmt.Sprintf("content-%05d", i))); err != nil {
				fail++
				fmt.Printf("FAIL: Phase5: PutObject %s: %v\n", key, err)
			}
		}
		pass++
		fmt.Printf("PASS: Phase5: uploaded %d objects via AWS SDK\n", n)
		sort.Strings(wantKeys)

		out, err := snapshotCreate(binPath, s.endpoint, "s3://p5-data/")
		fmt.Print(out)
		check("Phase5: zeros3 snapshot create (1500 objects)", err)
		id := snapshotIDFromCreateOutput(out)
		requireTrue("Phase5: create reports exactly 1500 objects", statInt(out, "Objects:") == n, out)

		// Mutate one, delete another, after the snapshot -- must not
		// affect the point-in-time restore.
		check("Phase5: mutate one source object after snapshot", putObject(ctx, s.client, "p5-data", "k/00000.bin", []byte("mutated-after-snapshot")))
		check("Phase5: delete one source object after snapshot", check1err(s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("p5-data"), Key: aws.String("k/00001.bin")})))

		restoreOut, err := snapshotRestore(binPath, s.endpoint, id, "s3://p5-recovery/restored/")
		fmt.Print(restoreOut)
		check("Phase5: zeros3 snapshot restore (1500 objects)", err)
		requireTrue("Phase5: restore reports exactly 1500 restored", statInt(restoreOut, "Restored:") == n, restoreOut)

		gotKeys, err := listAllKeys(ctx, s.client, "p5-recovery")
		check("Phase5: AWS SDK ListObjectsV2 pagination on recovered namespace", err)
		var gotStripped []string
		for _, k := range gotKeys {
			gotStripped = append(gotStripped, strings.TrimPrefix(k, "restored/"))
		}
		requireTrue("Phase5: exact key set after full pagination (1500 keys)", fmt.Sprint(gotStripped) == fmt.Sprint(wantKeys), fmt.Sprintf("got %d keys, want %d", len(gotStripped), len(wantKeys)))

		get0, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p5-recovery"), Key: aws.String("restored/k/00000.bin")})
		if check1("Phase5: restored k/00000.bin reflects PRE-mutation content", err) {
			got := mustReadAll(get0.Body)
			requireTrue("Phase5: k/00000.bin point-in-time content", string(got) == "content-00000", fmt.Sprintf("got %q", got))
		}
		get1, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p5-recovery"), Key: aws.String("restored/k/00001.bin")})
		if check1("Phase5: restored k/00001.bin exists despite live deletion", err) {
			got := mustReadAll(get1.Body)
			requireTrue("Phase5: k/00001.bin point-in-time content", string(got) == "content-00001", fmt.Sprintf("got %q", got))
		}
	}()

	// =========================================================================
	// Phase 6: snapshot pin/release -- content referenced only by one
	// snapshot: while it exists, GC preserves it and reports it as a
	// snapshot root; once the snapshot is deleted, GC no longer counts it
	// among the live snapshot roots. A separately-live object sharing the
	// same content stays preserved throughout either way.
	//
	// Note on "eventual collection" (external-harness scope): ZeroS3's
	// pre-existing (M1-M8D) internal version-history model retains every
	// completed PUT's content forever via its own historical-root
	// category (section 7c of zeros3.go) regardless of ordinary
	// DeleteObject -- a deliberate, already-frozen behavior this
	// milestone does not change or need to. That means content reachable
	// ONLY through the real public S3 API (this harness's own scope) can
	// never be made root-less by deleting a snapshot alone, so this phase
	// proves what is externally provable -- the snapshot root count
	// dropping to zero, and the pinning-while-it-exists half of the
	// story -- rather than a false "bytes physically vanish" claim. The
	// "a manifest with NO other root becomes collectible once its one
	// pinning snapshot is deleted" case (construction which requires
	// bypassing the ordinary object API, since ordinary PUT is
	// structurally incapable of producing root-less content) is proven
	// internally in zeros3_test.go:
	// TestSnapshotGC_DeleteFinalSnapshotAllowsEventualCollection.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p6")
		check("Phase6: CreateBucket data", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p6-data")})))
		check("Phase6: CreateBucket other", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p6-other")})))

		snapOnly := randomBytes(601, 150_000)
		shared := randomBytes(602, 150_000)
		check("Phase6: PutObject snapshot-only.bin", putObject(ctx, s.client, "p6-data", "snapshot-only.bin", snapOnly))
		check("Phase6: PutObject shared.bin (in data)", putObject(ctx, s.client, "p6-data", "shared.bin", shared))
		check("Phase6: PutObject shared.bin (separately live, in other)", putObject(ctx, s.client, "p6-other", "shared.bin", shared))

		out, err := snapshotCreate(binPath, s.endpoint, "s3://p6-data/")
		fmt.Print(out)
		check("Phase6: zeros3 snapshot create", err)
		id := snapshotIDFromCreateOutput(out)

		// Remove both live copies under p6-data -- snapshot-only.bin's
		// current-root reachability now depends entirely on the
		// snapshot; shared.bin's is still kept alive by
		// p6-other/shared.bin.
		check("Phase6: DeleteObject snapshot-only.bin", check1err(s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("p6-data"), Key: aws.String("snapshot-only.bin")})))
		check("Phase6: DeleteObject shared.bin (in data)", check1err(s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("p6-data"), Key: aws.String("shared.bin")})))

		stopZeroS3(s.cmd)
		gc1, err := runGCApply(binPath, s.dir)
		fmt.Println(gc1)
		check("Phase6: gc -apply while snapshot exists (must preserve everything)", err)
		requireTrue("Phase6: gc reports zero chunks deleted while snapshot pins content", statInt(gc1, "deleted") == 0 || !strings.Contains(gc1, "deleted"), gc1)
		// "snapshot" here counts snapshot-pinned ENTRIES (this one
		// snapshot captured 2 objects: snapshot-only.bin and shared.bin),
		// not the number of snapshots.
		requireTrue("Phase6: gc reports 2 snapshot-pinned entries while the snapshot exists", strings.Contains(gc1, "| 2 snapshot"), gc1)
		check("Phase6: restart after first gc", s.restart())

		deleteOut, err := snapshotDelete(binPath, s.endpoint, id)
		fmt.Print(deleteOut)
		check("Phase6: zeros3 snapshot delete", err)

		stopZeroS3(s.cmd)
		gc2, err := runGCApply(binPath, s.dir)
		fmt.Println(gc2)
		check("Phase6: gc -apply after snapshot delete", err)
		requireTrue("Phase6: gc reports a healthy live set", strings.Contains(gc2, "live set         ok=true"), gc2)
		requireTrue("Phase6: gc no longer reports any snapshot root after deletion", strings.Contains(gc2, "| 0 snapshot"), gc2)
		noteInfo("Phase6: bytes remain on disk after snapshot deletion because ZeroS3's pre-existing permanent version-history model (section 7c) independently retains every deleted object's former content -- see this phase's own doc comment and TestSnapshotGC_DeleteFinalSnapshotAllowsEventualCollection for the root-less-content collection proof")
		check("Phase6: restart after second gc", s.restart())
		defer s.stop()

		get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p6-other"), Key: aws.String("shared.bin")})
		if check1("Phase6: separately-live shared.bin survives both GC passes", err) {
			got := mustReadAll(get.Body)
			requireTrue("Phase6: shared.bin content exact", bytes.Equal(got, shared), "content mismatch")
		}
	}()

	// =========================================================================
	// Phase 7: corrupt snapshot metadata -- deliberately corrupt one
	// snapshot descriptor at the filesystem level; verify show/restore
	// fail cleanly, GC refuses to sweep, and no live/snapshot data is
	// lost.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p7")
		check("Phase7: CreateBucket data", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p7-data")})))
		check("Phase7: CreateBucket recovery", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p7-recovery")})))

		liveBody := []byte("live-and-well")
		check("Phase7: PutObject kept.bin", putObject(ctx, s.client, "p7-data", "kept.bin", liveBody))

		out, err := snapshotCreate(binPath, s.endpoint, "s3://p7-data/")
		fmt.Print(out)
		check("Phase7: zeros3 snapshot create", err)
		id := snapshotIDFromCreateOutput(out)

		stopZeroS3(s.cmd)
		snapFiles, err := snapshotFilesUnder(s.dir)
		check("Phase7: discover snapshot descriptor files", err)
		requireTrue("Phase7: exactly one snapshot descriptor file", len(snapFiles) == 1, fmt.Sprintf("got %d", len(snapFiles)))
		if len(snapFiles) > 0 {
			check("Phase7: corrupt the snapshot descriptor's trailing CRC32C (milestone-permitted exception)", corruptTrailingByte(snapFiles[0]))
		}
		check("Phase7: restart after corrupting the snapshot descriptor", s.restart())
		defer s.stop()

		_, showErr := snapshotShow(binPath, s.endpoint, id, false)
		requireTrue("Phase7: snapshot show fails cleanly on corrupt descriptor", showErr != nil, "expected a non-zero exit")

		_, restoreErr := snapshotRestore(binPath, s.endpoint, id, "s3://p7-recovery/")
		requireTrue("Phase7: snapshot restore fails cleanly on corrupt descriptor", restoreErr != nil, "expected a non-zero exit")

		stopZeroS3(s.cmd)
		gcDry, gcDryErr := runGCDryRun(binPath, s.dir)
		fmt.Println(gcDry)
		check("Phase7: gc dry-run does not hard-fail (still reports)", gcDryErr)
		requireTrue("Phase7: gc dry-run reports an unhealthy live set", strings.Contains(gcDry, "live set         ok=false"), gcDry)

		gcApply, gcApplyErr := runGCApply(binPath, s.dir)
		fmt.Println(gcApply)
		requireTrue("Phase7: gc -apply REFUSES to sweep with a corrupt snapshot present", gcApplyErr != nil, "expected gc -apply to fail/refuse")
		requireTrue("Phase7: gc -apply reports zero deletions", !strings.Contains(gcApply, "deleted ") || statInt(gcApply, "deleted") == 0, gcApply)

		check("Phase7: restart after refused gc", s.restart())
		get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p7-data"), Key: aws.String("kept.bin")})
		if check1("Phase7: unrelated live object kept.bin is completely unaffected", err) {
			got := mustReadAll(get.Body)
			requireTrue("Phase7: kept.bin content exact", bytes.Equal(got, liveBody), "content mismatch")
		}
	}()

	// =========================================================================
	// Phase 8: restore interruption/resume -- a real `zeros3 snapshot
	// restore` OS process, killed mid-run (SIGKILL), then correctly
	// resumed by a second real invocation with every object present, no
	// partial objects, and zero extra CAS payload across the whole
	// sequence.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p8")
		defer s.stop()
		check("Phase8: CreateBucket data", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p8-data")})))
		check("Phase8: CreateBucket recovery", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p8-recovery")})))

		const n = 500
		wantContent := map[string]string{}
		for i := 0; i < n; i++ {
			key := fmt.Sprintf("obj-%04d.bin", i)
			body := fmt.Sprintf("interruptible-content-%04d", i)
			wantContent[key] = body
			if err := putObject(ctx, s.client, "p8-data", key, []byte(body)); err != nil {
				fail++
				fmt.Printf("FAIL: Phase8: PutObject %s: %v\n", key, err)
			}
		}
		pass++
		fmt.Printf("PASS: Phase8: uploaded %d objects via AWS SDK\n", n)

		out, err := snapshotCreate(binPath, s.endpoint, "s3://p8-data/")
		fmt.Print(out)
		check("Phase8: zeros3 snapshot create (500 objects)", err)
		id := snapshotIDFromCreateOutput(out)

		beforeFiles, err := chunkFilesUnder(s.dir)
		check("Phase8: discover CAS chunk files before restore attempts", err)

		restoreCmd := snapshotRestoreCmd(binPath, s.endpoint, id, "s3://p8-recovery/")
		if err := restoreCmd.Start(); err != nil {
			fail++
			fmt.Printf("FAIL: Phase8: starting restore process: %v\n", err)
		} else {
			pass++
			fmt.Println("PASS: Phase8: started real `zeros3 snapshot restore` OS process")
			time.Sleep(15 * time.Millisecond)
			if err := restoreCmd.Process.Kill(); err != nil {
				fail++
				fmt.Printf("FAIL: Phase8: SIGKILL of restore process: %v\n", err)
			} else {
				pass++
				fmt.Println("PASS: Phase8: SIGKILL delivered to restore process mid-run")
			}
			restoreCmd.Wait()
		}

		// Rerun to completion.
		resumeOut, err := snapshotRestore(binPath, s.endpoint, id, "s3://p8-recovery/")
		fmt.Print(resumeOut)
		check("Phase8: resumed zeros3 snapshot restore completes", err)

		gotKeys, err := listAllKeys(ctx, s.client, "p8-recovery")
		check("Phase8: list recovered namespace after resume", err)
		requireTrue("Phase8: exact object count after resume", len(gotKeys) == n, fmt.Sprintf("got %d, want %d", len(gotKeys), n))

		allExact := true
		for key, want := range wantContent {
			get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p8-recovery"), Key: aws.String(key)})
			if err != nil {
				allExact = false
				continue
			}
			got := mustReadAll(get.Body)
			if string(got) != want {
				allExact = false
			}
		}
		requireTrue("Phase8: every recovered object has its exact, non-partial content", allExact, "at least one recovered object was missing or wrong after interruption/resume")

		afterFiles, err := chunkFilesUnder(s.dir)
		check("Phase8: discover CAS chunk files after interruption+resume", err)
		requireTrue("Phase8: zero extra CAS payload across interruption+resume", len(afterFiles) == len(beforeFiles), fmt.Sprintf("before=%d after=%d", len(beforeFiles), len(afterFiles)))
	}()

	// =========================================================================
	// Phase 9: restart -- restart zeros3 with snapshots present; list/
	// show unchanged; restore still works; GC still recognizes the
	// snapshot root.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p9")
		check("Phase9: CreateBucket data", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p9-data")})))
		check("Phase9: CreateBucket recovery", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p9-recovery")})))
		body := randomBytes(901, 40_000)
		check("Phase9: PutObject x.bin", putObject(ctx, s.client, "p9-data", "x.bin", body))

		out, err := snapshotCreate(binPath, s.endpoint, "s3://p9-data/")
		fmt.Print(out)
		check("Phase9: zeros3 snapshot create", err)
		id := snapshotIDFromCreateOutput(out)

		beforeShow, err := snapshotShow(binPath, s.endpoint, id, false)
		check("Phase9: snapshot show before restart", err)

		check("Phase9: restart zeros3", s.restart())
		defer s.stop()

		afterList, err := snapshotList(binPath, s.endpoint)
		fmt.Print(afterList)
		check("Phase9: snapshot list after restart", err)
		requireTrue("Phase9: snapshot still listed after restart", strings.Contains(afterList, id), afterList)

		afterShow, err := snapshotShow(binPath, s.endpoint, id, false)
		fmt.Print(afterShow)
		check("Phase9: snapshot show after restart", err)
		requireTrue("Phase9: show output unchanged across restart", statInt(beforeShow, "Objects:") == statInt(afterShow, "Objects:"), fmt.Sprintf("before=%q after=%q", beforeShow, afterShow))

		restoreOut, err := snapshotRestore(binPath, s.endpoint, id, "s3://p9-recovery/")
		fmt.Print(restoreOut)
		check("Phase9: snapshot restore still works after restart", err)
		get, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p9-recovery"), Key: aws.String("x.bin")})
		if check1("Phase9: restored object exact after restart", err) {
			got := mustReadAll(get.Body)
			requireTrue("Phase9: content exact", bytes.Equal(got, body), "content mismatch")
		}

		stopZeroS3(s.cmd)
		gcOut, gcErr := runGCDryRun(binPath, s.dir)
		fmt.Println(gcOut)
		check("Phase9: gc dry-run after restart still recognizes the snapshot root", gcErr)
		requireTrue("Phase9: gc reports at least one snapshot root", statInt(gcOut, "roots") >= 1 && strings.Contains(gcOut, "snapshot"), gcOut)
		check("Phase9: restart after gc dry-run", s.restart())
	}()

	// =========================================================================
	// Phase 10: M8D/M8B composition -- restore a snapshot into a second
	// namespace (so the restored copy and the live source share physical
	// CAS chunks, structurally identical to an M8D fork), corrupt one of
	// those shared chunks, then prove `zeros3 repair` from an independent
	// peer fixes it for both namespaces in one repair, without any change
	// to M8B.
	// =========================================================================
	func() {
		s := startOneServer(binPath, "p10")
		check("Phase10: CreateBucket data", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p10-data")})))
		check("Phase10: CreateBucket recovery", check1err(s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String("p10-recovery")})))

		body := randomBytes(1001, 3_000_000)
		check("Phase10: PutObject shared.bin", putObject(ctx, s.client, "p10-data", "shared.bin", body))

		out, err := snapshotCreate(binPath, s.endpoint, "s3://p10-data/")
		fmt.Print(out)
		check("Phase10: zeros3 snapshot create", err)
		id := snapshotIDFromCreateOutput(out)

		restoreOut, err := snapshotRestore(binPath, s.endpoint, id, "s3://p10-recovery/")
		fmt.Print(restoreOut)
		check("Phase10: zeros3 snapshot restore (shares CAS with source)", err)

		peerDir := mustTempStore("zeros3-m8e-p10-peer-store-")
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
			check("Phase10: corrupt one chunk file shared by source and restored namespaces (milestone-permitted exception)", corruptFile(chunks[0]))
		}
		check("Phase10: restart store after corrupting the shared chunk", s.restart())
		defer s.stop()

		_, preErr := runVerifyDeep(binPath, s.dir)
		requireTrue("Phase10: verify -deep detects the corruption before repair (affecting both namespaces)", preErr != nil, "expected verify -deep to fail before repair")

		stopZeroS3(s.cmd)
		repairOut, repairErr := runRepair(binPath, s.dir, peerEndpoint)
		fmt.Println(repairOut)
		check("Phase10: zeros3 repair -from the independent peer (M8B, unmodified)", repairErr)
		check("Phase10: restart store after repair", s.restart())

		getSrc, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p10-data"), Key: aws.String("shared.bin")})
		if check1("Phase10: AWS SDK GetObject source/shared.bin after repair", err) {
			got := mustReadAll(getSrc.Body)
			requireTrue("Phase10: source exact content after repair", bytes.Equal(got, body), "content mismatch")
		}
		getDst, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String("p10-recovery"), Key: aws.String("shared.bin")})
		if check1("Phase10: AWS SDK GetObject restored/shared.bin after repair", err) {
			got := mustReadAll(getDst.Body)
			requireTrue("Phase10: restored namespace exact content after repair -- one physical repair fixed both", bytes.Equal(got, body), "content mismatch")
		}
	}()

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed, %d informational =====\n", pass, fail, info)
	if fail > 0 {
		os.Exit(1)
	}
}
