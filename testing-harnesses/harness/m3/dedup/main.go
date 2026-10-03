// Ephemeral, external dedup-evidence demo driver for ZeroS3 M3.
//
// This program is NOT part of ZeroS3 and is never linked into the zeros3
// binary. Unlike the white-box Go tests in zeros3_test.go
// (which call Store methods directly), this driver only ever talks to a
// real zeros3-bin server over the S3 wire protocol (via the pinned AWS
// SDK for Go v2) and reads dedup evidence back through zeros3's own
// `stats -json` CLI subcommand -- an entirely black-box measurement of
// the same CDC/CAS dedup story the product test suite proves internally.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
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

// statsResult mirrors the JSON field names zeros3's `stats -json`
// subcommand emits (see STATS_SPEC.md / zeros3.go's StatsResult); only
// the fields this driver reports on are declared.
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

var pass, fail int

func check(name string, cond bool, detail string) {
	if !cond {
		fail++
		fmt.Printf("FAIL: %s: %s\n", name, detail)
		return
	}
	pass++
	fmt.Printf("PASS: %s\n", name)
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

// runStats shells out to the zeros3 binary's own `stats -json` CLI
// subcommand against storeDir (the server is stopped first, so this is a
// plain local filesystem read, no HTTP involved) and parses its output.
func runStats(binPath, storeDir string, extraArgs ...string) statsResult {
	args := append([]string{"stats", "-store", storeDir, "-json"}, extraArgs...)
	out, err := exec.Command(binPath, args...).Output()
	if err != nil {
		log.Fatalf("zeros3 stats failed: %v", err)
	}
	var res statsResult
	if err := json.Unmarshal(out, &res); err != nil {
		log.Fatalf("parsing stats JSON: %v (output: %s)", err, out)
	}
	return res
}

// deterministicBody generates n reproducible, non-repetitive bytes from a
// tiny linear congruential generator -- deterministic across every OS/
// architecture without depending on math/rand's version-specific stream.
func deterministicBody(seedByte byte, n int) []byte {
	b := make([]byte, n)
	x := uint32(seedByte) + 1
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return b
}

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	storeDir, err := os.MkdirTemp("", "zeros3-m3-dedup-demo-store-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(storeDir)

	addr, err := freePort()
	if err != nil {
		log.Fatal(err)
	}
	cmd, err := startZeroS3(binPath, storeDir, addr)
	if err != nil {
		log.Fatalf("starting zeros3: %v", err)
	}
	client := newClient("http://" + addr)
	ctx := context.Background()

	const bucket = "dedup-demo"
	body := deterministicBody(1, 4*1024*1024) // 4MiB, deterministic, many CDC chunks

	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		log.Fatalf("CreateBucket: %v", err)
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("copy1"), Body: bytes.NewReader(body)}); err != nil {
		log.Fatalf("PutObject copy1: %v", err)
	}
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("copy2"), Body: bytes.NewReader(body)}); err != nil {
		log.Fatalf("PutObject copy2: %v", err)
	}
	stopZeroS3(cmd)

	identical := runStats(binPath, storeDir)
	fmt.Printf("\n--- Identical-object reuse (2 uploads of the same %d-byte object, real S3 PutObject calls) ---\n", len(body))
	fmt.Printf("logical_current_bytes=%d logical_chunk_reference_bytes=%d logical_chunk_reference_count=%d\n",
		identical.LogicalCurrentBytes, identical.LogicalChunkReferenceBytes, identical.LogicalChunkReferenceCount)
	fmt.Printf("scope_unique_chunk_bytes=%d scope_unique_chunk_count=%d chunk_store_file_bytes=%d\n",
		identical.ScopeUniqueChunkBytes, identical.ScopeUniqueChunkCount, identical.ChunkStoreFileBytes)
	fmt.Printf("dedup_avoided_bytes=%d dedup_reduction=%.1f%%\n", identical.DedupAvoidedBytes, identical.DedupReduction*100)

	check("logical bytes reflect both copies", identical.LogicalCurrentBytes == 2*int64(len(body)),
		fmt.Sprintf("got %d want %d", identical.LogicalCurrentBytes, 2*len(body)))
	check("chunk reference bytes are exactly double the unique bytes (two byte-identical copies)",
		identical.LogicalChunkReferenceBytes == 2*identical.ScopeUniqueChunkBytes,
		fmt.Sprintf("refs=%d unique=%d", identical.LogicalChunkReferenceBytes, identical.ScopeUniqueChunkBytes))
	check("unique chunk bytes equal chunk-store file bytes (nothing duplicated on disk)",
		identical.ScopeUniqueChunkBytes == identical.ChunkStoreFileBytes,
		fmt.Sprintf("unique=%d chunk_store_file_bytes=%d", identical.ScopeUniqueChunkBytes, identical.ChunkStoreFileBytes))
	check("dedup_avoided_bytes is positive", identical.DedupAvoidedBytes > 0, fmt.Sprintf("got %d", identical.DedupAvoidedBytes))
	check("dedup_reduction is close to 50% for one exact duplicate",
		identical.DedupReduction > 0.45 && identical.DedupReduction < 0.55,
		fmt.Sprintf("got %.3f", identical.DedupReduction))

	// --- Edited-object reuse: restart the server against the same store,
	// upload a near-duplicate with a small edit near the start, and
	// measure how much of it reuses chunks already published above. ---
	addr2, err := freePort()
	if err != nil {
		log.Fatal(err)
	}
	cmd2, err := startZeroS3(binPath, storeDir, addr2)
	if err != nil {
		log.Fatalf("restarting zeros3: %v", err)
	}
	client2 := newClient("http://" + addr2)

	insertion := deterministicBody(2, 4001)
	edited := append(append(append([]byte{}, body[:50000]...), insertion...), body[50000:]...)
	if _, err := client2.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("edited"), Body: bytes.NewReader(edited)}); err != nil {
		log.Fatalf("PutObject edited: %v", err)
	}
	stopZeroS3(cmd2)

	editedScope := runStats(binPath, storeDir, "-bucket", bucket, "-key", "edited")
	fmt.Printf("\n--- Edited-object reuse (%d-byte original vs %d-byte edited, 4001-byte insertion near the start) ---\n", len(body), len(edited))
	fmt.Printf("edited object: logical_current_bytes=%d scope_unique_chunk_bytes=%d scope_exclusive_chunk_bytes=%d scope_shared_chunk_bytes=%d\n",
		editedScope.LogicalCurrentBytes, editedScope.ScopeUniqueChunkBytes, editedScope.ScopeExclusiveChunkBytes, editedScope.ScopeSharedChunkBytes)
	reuseFraction := float64(editedScope.ScopeSharedChunkBytes) / float64(editedScope.LogicalCurrentBytes)
	fmt.Printf("reused (shared with copy1/copy2) = %d bytes (%.1f%% of the edited object)\n", editedScope.ScopeSharedChunkBytes, reuseFraction*100)

	check("edited object's logical size matches", editedScope.LogicalCurrentBytes == int64(len(edited)),
		fmt.Sprintf("got %d want %d", editedScope.LogicalCurrentBytes, len(edited)))
	check("CDC reuses the large majority of the edited object from the earlier upload",
		reuseFraction > 0.85, fmt.Sprintf("got %.3f", reuseFraction))

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed =====\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}
