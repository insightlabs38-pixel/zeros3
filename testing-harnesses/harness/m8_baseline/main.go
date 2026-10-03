// Ephemeral benchmark harness for ZeroS3's m7-gold baseline (M8 Phase 1).
//
// This program is NOT part of ZeroS3 and is never linked into the zeros3
// binary. It measures observed performance of a real zeros3
// server subprocess -- built from an exact, recorded zeros3.go commit --
// through the same three surfaces a real user has: the AWS SDK for Go v2
// (black-box S3 client) for ordinary PUT/GET/CopyObject, the real
// `zeros3` CLI subprocess for `sync`/`stats`/`verify`, and direct process
// start/stop timing for startup/replay. It never calls into ZeroS3's Go
// package internals and never modifies zeros3.go.
//
// This is measurement, not optimization: nothing here changes zeros3's
// implementation, and results are reported as "on this test environment"
// observations, not universal performance claims. See
// results/M8_BASELINE_BENCHMARKS.md for the exact recorded run this
// harness produced and the environment it ran on.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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

// --- statsResult mirrors zeros3's `stats -json` output (see
// zeros3.go's StatsResult / STATUS.md's "Dedup and stats"). Only the
// fields this harness reports on are declared. ---
type statsResult struct {
	LogicalCurrentBytes        int64   `json:"logical_current_bytes"`
	LogicalChunkReferenceBytes int64   `json:"logical_chunk_reference_bytes"`
	LogicalChunkReferenceCount int64   `json:"logical_chunk_reference_count"`
	ScopeUniqueChunkBytes      int64   `json:"scope_unique_chunk_bytes"`
	ScopeUniqueChunkCount      int64   `json:"scope_unique_chunk_count"`
	ScopeExclusiveChunkBytes   int64   `json:"scope_exclusive_chunk_bytes"`
	ScopeSharedChunkBytes      int64   `json:"scope_shared_chunk_bytes"`
	ChunkStoreFileBytes        int64   `json:"chunk_store_file_bytes"`
	DedupAvoidedBytes          int64   `json:"dedup_avoided_bytes"`
	DedupReduction             float64 `json:"dedup_reduction"`
}

// verifyResult mirrors zeros3's `verify -json` output (VerifyResult).
type verifyResult struct {
	Deep                 bool  `json:"deep"`
	JournalFramesChecked int   `json:"journal_frames_checked"`
	JournalOK            bool  `json:"journal_ok"`
	ManifestsChecked     int   `json:"manifests_checked"`
	ChunksChecked        int   `json:"chunks_checked"`
	CurrentRootCount     int   `json:"current_root_count"`
	HistoricalRootCount  int   `json:"historical_root_count"`
	MultipartRootCount   int   `json:"multipart_root_count"`
	UnreachableManifests int   `json:"unreachable_manifests"`
	UnreachableChunks    int   `json:"unreachable_chunks"`
	ReclaimableBytes     int64 `json:"reclaimable_bytes"`
}

// runningCmds tracks every zeros3 server subprocess this harness has
// started, so fatalf can kill them all before exiting -- otherwise a
// log.Fatalf mid-run (which skips defers) leaves an orphaned server
// process whose inherited stdout keeps this program's own output pipe
// open indefinitely, even after this process itself has exited.
var runningCmds []*exec.Cmd

func fatalf(format string, args ...any) {
	for _, c := range runningCmds {
		stopZeroS3(c)
	}
	log.Fatalf(format, args...)
}

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

// startZeroS3 launches the server and returns once it is accepting
// connections, reporting how long that took -- the raw material for B8's
// startup/replay measurement.
func startZeroS3(binPath, storeDir, addr string) (*exec.Cmd, time.Duration, error) {
	start := time.Now()
	cmd := exec.Command(binPath, "-store", storeDir, "-addr", addr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return cmd, time.Since(start), nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	cmd.Process.Kill()
	return nil, 0, fmt.Errorf("zeros3 did not start listening on %s in time", addr)
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
		fatalf("config.LoadDefaultConfig: %v", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

func runStatsScoped(binPath, storeDir, bucket, key string) statsResult {
	args := []string{"stats", "-store", storeDir, "-json"}
	if bucket != "" {
		args = append(args, "-bucket", bucket)
	}
	if key != "" {
		args = append(args, "-key", key)
	}
	out, err := exec.Command(binPath, args...).Output()
	if err != nil {
		fatalf("zeros3 stats failed: %v", err)
	}
	var res statsResult
	if err := json.Unmarshal(out, &res); err != nil {
		fatalf("parsing stats JSON: %v (output: %s)", err, out)
	}
	return res
}

func runVerifyCLI(binPath, storeDir string, deep bool) (verifyResult, time.Duration) {
	args := []string{"verify", "-store", storeDir, "-json"}
	if deep {
		args = append(args, "-deep")
	}
	start := time.Now()
	out, err := exec.Command(binPath, args...).Output()
	elapsed := time.Since(start)
	if err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || len(ee.Stderr) == 0 {
			fatalf("zeros3 verify failed: %v", err)
		}
	}
	var res verifyResult
	if uerr := json.Unmarshal(out, &res); uerr != nil {
		fatalf("parsing verify JSON: %v (output: %s)", uerr, out)
	}
	return res, elapsed
}

// runSyncCLI runs the real `zeros3 sync` CLI subprocess (never the Go
// package internals) and returns its full stdout (which already contains
// syncFile's own human-readable stats block: Logical scanned/Chunks/
// Uploaded payload/Transfer avoided/Reuse) plus wall time.
func runSyncCLI(binPath, localPath, endpoint, bucket, key string) (string, time.Duration, error) {
	// Flags must precede the positional LOCAL_PATH/s3://bucket/key
	// arguments: flag.FlagSet.Parse (which runSync uses) stops parsing at
	// the first non-flag argument, exactly like the standard library's
	// flag package always has.
	args := []string{"sync", "-endpoint", endpoint, localPath, "s3://" + bucket + "/" + key}
	start := time.Now()
	out, err := exec.Command(binPath, args...).CombinedOutput()
	elapsed := time.Since(start)
	return string(out), elapsed, err
}

// deterministicBody generates n reproducible, non-repetitive
// ("random-shaped") bytes from a small linear congruential generator --
// deterministic across every OS/architecture, never depending on
// math/rand's version-specific stream. This content is NOT compressible
// or repetitive; every B1/B2/B3 object below is built from it, and is
// labeled as such in the results doc.
func deterministicBody(seed uint32, n int) []byte {
	b := make([]byte, n)
	x := seed + 1
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return b
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func humanBytes(n int64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	f := float64(n)
	i := 0
	for f >= 1024 && i < len(units)-1 {
		f /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[i])
	}
	return fmt.Sprintf("%.2f %s", f, units[i])
}

func mibps(bytesN int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return (float64(bytesN) / (1024 * 1024)) / d.Seconds()
}

var results = &bytes.Buffer{}

func record(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	fmt.Println(line)
	results.WriteString(line)
	results.WriteByte('\n')
}

func section(name string) {
	record("")
	record("=== %s ===", name)
}

func mustCreateBucket(cli *s3.Client, bucket string) {
	ctx := context.Background()
	if _, err := cli.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		fatalf("CreateBucket(%s): %v", bucket, err)
	}
}

func main() {
	binPath := flag.String("bin", "", "path to the zeros3 binary to benchmark (required)")
	outDir := flag.String("out", "", "scratch directory for store dirs and result text (default: a fresh temp dir)")
	includeGiB := flag.Bool("big", false, "also run the optional 1 GiB PUT/GET scenario")
	flag.Parse()

	if *binPath == "" {
		fatalf("%v", "-bin is required")
	}
	if *outDir == "" {
		d, err := os.MkdirTemp("", "zeros3-m8-baseline-*")
		if err != nil {
			fatalf("%v", err)
		}
		*outDir = d
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatalf("%v", err)
	}

	record("ZeroS3 M8 baseline benchmark run")
	record("bin=%s", *binPath)
	record("out=%s", *outDir)
	record("started=%s", time.Now().UTC().Format(time.RFC3339))

	sizes := []int{16 << 20, 64 << 20, 256 << 20}
	if *includeGiB {
		sizes = append(sizes, 1<<30)
	}

	// ---------------------------------------------------------------
	// Store 1: B1 (PUT), B2 (GET), B3 (duplicate PUT / CAS reuse),
	// B5 (CopyObject) -- these naturally build on each other's objects.
	// ---------------------------------------------------------------
	store1 := filepath.Join(*outDir, "store1")
	addr1, err := freePort()
	if err != nil {
		fatalf("%v", err)
	}
	cmd1, _, err := startZeroS3(*binPath, store1, addr1)
	runningCmds = append(runningCmds, cmd1)
	if err != nil {
		fatalf("%v", err)
	}
	endpoint1 := "http://" + addr1
	cli1 := newClient(endpoint1)
	mustCreateBucket(cli1, "bench")

	section("B1. Ordinary PUT (deterministic pseudo-random, non-compressible content)")
	type putObj struct {
		size int
		key  string
	}
	var puts []putObj
	for _, sz := range sizes {
		key := fmt.Sprintf("put-%d", sz)
		body := deterministicBody(uint32(sz), sz)
		start := time.Now()
		_, err := cli1.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: aws.String("bench"), Key: aws.String(key), Body: bytes.NewReader(body),
		})
		elapsed := time.Since(start)
		if err != nil {
			fatalf("PutObject(%s): %v", key, err)
		}
		st := runStatsScoped(*binPath, store1, "bench", key)
		record("PUT %-10s  wall=%-10s  %.1f MiB/s  physical_chunk_bytes=%-12s chunks=%d",
			humanBytes(int64(sz)), elapsed.Round(time.Millisecond), mibps(int64(sz), elapsed),
			humanBytes(st.ScopeUniqueChunkBytes), st.ScopeUniqueChunkCount)
		puts = append(puts, putObj{size: sz, key: key})
	}

	section("B2. Ordinary GET (retrieve B1's objects, verify exact checksum equality)")
	for _, p := range puts {
		want := sha256Hex(deterministicBody(uint32(p.size), p.size))
		start := time.Now()
		out, err := cli1.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String("bench"), Key: aws.String(p.key)})
		if err != nil {
			fatalf("GetObject(%s): %v", p.key, err)
		}
		got, err := io.ReadAll(out.Body)
		out.Body.Close()
		elapsed := time.Since(start)
		if err != nil {
			fatalf("reading GetObject(%s) body: %v", p.key, err)
		}
		gotSum := sha256Hex(got)
		record("GET %-10s  wall=%-10s  %.1f MiB/s  checksum_match=%v",
			humanBytes(int64(p.size)), elapsed.Round(time.Millisecond), mibps(int64(len(got)), elapsed), gotSum == want)
		if gotSum != want {
			fatalf("GET %s: checksum mismatch", p.key)
		}
	}

	section("B3. Duplicate PUT / CAS reuse (same large object content, new key)")
	{
		p := puts[len(puts)-1] // largest size from B1
		body := deterministicBody(uint32(p.size), p.size)
		before := runStatsScoped(*binPath, store1, "", "")
		start := time.Now()
		_, err := cli1.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket: aws.String("bench"), Key: aws.String("dup-of-" + p.key), Body: bytes.NewReader(body),
		})
		elapsed := time.Since(start)
		if err != nil {
			fatalf("duplicate PutObject: %v", err)
		}
		after := runStatsScoped(*binPath, store1, "", "")
		dupScope := runStatsScoped(*binPath, store1, "bench", "dup-of-"+p.key)
		newPhysical := after.ChunkStoreFileBytes - before.ChunkStoreFileBytes
		reuse := 100.0
		if dupScope.ScopeUniqueChunkBytes > 0 {
			reuse = 100.0 * float64(dupScope.ScopeUniqueChunkBytes-newPhysical) / float64(dupScope.ScopeUniqueChunkBytes)
		}
		record("logical_bytes=%s  new_physical_chunk_bytes=%s  wall=%s  reuse=%.1f%%",
			humanBytes(int64(p.size)), humanBytes(newPhysical), elapsed.Round(time.Millisecond), reuse)
		record("NOTE: 'new physical chunk bytes' is store-wide CAS growth, not network transfer -- the client still sent the full %s request body over the wire; CAS reuse and network transfer are separate concepts (see README's dedup section).",
			humanBytes(int64(p.size)))
	}

	section("B4. Edited-object CDC reuse (deterministic large fixture: base + insertion + deletion + localized mutation)")
	{
		const baseSize = 64 << 20
		base := deterministicBody(0xB4B4B4B4, baseSize)

		insertAt := baseSize / 2
		insertion := append(append(append([]byte{}, base[:insertAt]...), deterministicBody(0xE1E1E1E1, 4096)...), base[insertAt:]...)

		deleteAt, deleteLen := baseSize/3, 8192
		deletion := append(append([]byte{}, base[:deleteAt]...), base[deleteAt+deleteLen:]...)

		mutateAt, mutateLen := (2*baseSize)/3, 4096
		mutation := append([]byte{}, base...)
		copy(mutation[mutateAt:mutateAt+mutateLen], deterministicBody(0xAAAAAAAA, mutateLen))

		variants := []struct {
			name string
			data []byte
		}{
			{"base", base},
			{"insertion", insertion},
			{"deletion", deletion},
			{"mutation", mutation},
		}
		for _, v := range variants {
			_, err := cli1.PutObject(context.Background(), &s3.PutObjectInput{
				Bucket: aws.String("bench"), Key: aws.String("edit-" + v.name), Body: bytes.NewReader(v.data),
			})
			if err != nil {
				fatalf("PutObject(edit-%s): %v", v.name, err)
			}
			st := runStatsScoped(*binPath, store1, "bench", "edit-"+v.name)
			reuse := 0.0
			if st.ScopeUniqueChunkBytes > 0 {
				reuse = 100.0 * float64(st.ScopeSharedChunkBytes) / float64(st.ScopeUniqueChunkBytes)
			}
			record("%-10s logical=%-10s unique_total=%-10s shared(reused)=%-10s exclusive(new)=%-10s reuse_of_this_object=%.1f%%",
				v.name, humanBytes(int64(len(v.data))), humanBytes(st.ScopeUniqueChunkBytes),
				humanBytes(st.ScopeSharedChunkBytes), humanBytes(st.ScopeExclusiveChunkBytes), reuse)
		}
		record("NOTE: 'base' itself shows 0%% shared (nothing preceded it in this fresh scope); insertion/deletion/mutation are each compared against everything already in the store, dominated by 'base'.")
	}

	section("B5. CopyObject (large object, same store)")
	{
		p := puts[len(puts)-1]
		before := runStatsScoped(*binPath, store1, "", "")
		start := time.Now()
		_, err := cli1.CopyObject(context.Background(), &s3.CopyObjectInput{
			Bucket: aws.String("bench"), Key: aws.String("copy-of-" + p.key),
			CopySource: aws.String("bench/" + p.key),
		})
		elapsed := time.Since(start)
		if err != nil {
			fatalf("CopyObject: %v", err)
		}
		after := runStatsScoped(*binPath, store1, "", "")
		newPhysical := after.ChunkStoreFileBytes - before.ChunkStoreFileBytes
		record("logical_object_size=%s  new_payload_chunk_bytes=%s  wall=%s",
			humanBytes(int64(p.size)), humanBytes(newPhysical), elapsed.Round(time.Millisecond))
		record("NOTE: claim is 'zero new PAYLOAD CHUNK bytes', not 'literally zero bytes written' -- a new manifest file is still created for the copy's own version identity.")
	}

	section("B7. Verify (basic and deep) on store1 after B1-B5")
	{
		basic, elapsed := runVerifyCLI(*binPath, store1, false)
		record("basic verify: wall=%s manifests_checked=%d chunks_checked=%d journal_frames=%d ok=%v",
			elapsed.Round(time.Millisecond), basic.ManifestsChecked, basic.ChunksChecked, basic.JournalFramesChecked, basic.JournalOK)
		deep, elapsed2 := runVerifyCLI(*binPath, store1, true)
		record("deep  verify: wall=%s manifests_checked=%d chunks_checked=%d journal_frames=%d ok=%v",
			elapsed2.Round(time.Millisecond), deep.ManifestsChecked, deep.ChunksChecked, deep.JournalFramesChecked, deep.JournalOK)
	}

	stopZeroS3(cmd1)

	// ---------------------------------------------------------------
	// Store 3: B6, M6 local delta sync (initial sync, edit, resync).
	// ---------------------------------------------------------------
	section("B6. M6 local delta sync (initial sync -> edited local file -> second sync)")
	{
		store3 := filepath.Join(*outDir, "store3")
		addr3, err := freePort()
		if err != nil {
			fatalf("%v", err)
		}
		cmd3, _, err := startZeroS3(*binPath, store3, addr3)
		runningCmds = append(runningCmds, cmd3)
		if err != nil {
			fatalf("%v", err)
		}
		endpoint3 := "http://" + addr3
		mustCreateBucket(newClient(endpoint3), "syncbench")

		const syncSize = 64 << 20
		local := deterministicBody(0xC6C6C6C6, syncSize)
		localPath := filepath.Join(*outDir, "sync-fixture.bin")
		if err := os.WriteFile(localPath, local, 0o644); err != nil {
			fatalf("%v", err)
		}

		out1, elapsed1, err := runSyncCLI(*binPath, localPath, endpoint3, "syncbench", "obj")
		if err != nil {
			fatalf("initial sync failed: %v\n%s", err, out1)
		}
		record("--- initial sync (wall=%s) ---", elapsed1.Round(time.Millisecond))
		record("%s", strings.TrimSpace(out1))

		// Localized edit: mutate ~0.1% of the file in one spot, same size.
		mutateAt := syncSize / 2
		copy(local[mutateAt:mutateAt+4096], deterministicBody(0xEEEEEEEE, 4096))
		if err := os.WriteFile(localPath, local, 0o644); err != nil {
			fatalf("%v", err)
		}

		out2, elapsed2, err := runSyncCLI(*binPath, localPath, endpoint3, "syncbench", "obj")
		if err != nil {
			fatalf("second sync failed: %v\n%s", err, out2)
		}
		record("--- second sync after localized edit (wall=%s) ---", elapsed2.Round(time.Millisecond))
		record("%s", strings.TrimSpace(out2))

		stopZeroS3(cmd3)
	}

	// ---------------------------------------------------------------
	// Store 4: B8, startup/replay timing under a nontrivial journal.
	// ---------------------------------------------------------------
	section("B8. Startup/replay (populated store, server start-to-ready timing)")
	{
		store4 := filepath.Join(*outDir, "store4")
		addr4, err := freePort()
		if err != nil {
			fatalf("%v", err)
		}
		cmd4, _, err := startZeroS3(*binPath, store4, addr4)
		runningCmds = append(runningCmds, cmd4)
		if err != nil {
			fatalf("%v", err)
		}
		endpoint4 := "http://" + addr4
		cli4 := newClient(endpoint4)
		mustCreateBucket(cli4, "startupbench")

		const numObjects = 3000
		const objSize = 8192
		for i := 0; i < numObjects; i++ {
			body := deterministicBody(uint32(i), objSize)
			key := "obj-" + strconv.Itoa(i)
			if _, err := cli4.PutObject(context.Background(), &s3.PutObjectInput{
				Bucket: aws.String("startupbench"), Key: aws.String(key), Body: bytes.NewReader(body),
			}); err != nil {
				fatalf("populate PutObject(%d): %v", i, err)
			}
		}
		stopZeroS3(cmd4)

		addr4b, err := freePort()
		if err != nil {
			fatalf("%v", err)
		}
		cmd4b, startupElapsed, err := startZeroS3(*binPath, store4, addr4b)
		runningCmds = append(runningCmds, cmd4b)
		if err != nil {
			fatalf("%v", err)
		}
		record("populated store: %d objects, %d journal PutObject records", numObjects, numObjects)
		record("cold-start-to-ready wall time: %s", startupElapsed.Round(time.Millisecond))
		stopZeroS3(cmd4b)
	}

	record("")
	record("finished=%s", time.Now().UTC().Format(time.RFC3339))

	resultsPath := filepath.Join(*outDir, "raw_output.txt")
	if err := os.WriteFile(resultsPath, results.Bytes(), 0o644); err != nil {
		fatalf("%v", err)
	}
	fmt.Println("\nraw output written to", resultsPath)
}
