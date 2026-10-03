// Ephemeral, external interoperability harness for ZeroS3 M5-B: persistent
// S3 multipart upload (CreateMultipartUpload/UploadPart/ListParts/
// CompleteMultipartUpload/AbortMultipartUpload/ListMultipartUploads).
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 modules purely as
// a black-box client. It drives the real S3 multipart API against a real
// zeros3 server binary over ordinary HTTP with path-style addressing,
// using only the AWS SDK's low-level S3 client calls (never any internal
// ZeroS3 API) so every assertion below reflects genuine S3 wire-protocol
// behavior. It restarts the zeros3 process mid-lifecycle (after some parts
// are uploaded, and again after completion) against the same store
// directory, proving multipart state and the completed object both
// survive a real process restart, not just an in-process code path.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

const (
	accessKeyID     = "AKIAZEROS3EXAMPLE01"
	secretAccessKey = "zeros3exampleSecretKeyForM1TestingOnly01"
	region          = "us-east-1"
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

func requireError(name string, err error) {
	if err == nil {
		fail++
		fmt.Printf("FAIL: %s: expected an error, got success\n", name)
		return
	}
	pass++
	fmt.Printf("PASS: %s (rejected: %v)\n", name, err)
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

func restartZeroS3(binPath, storeDir string, oldCmd *exec.Cmd) (*exec.Cmd, string, error) {
	if err := oldCmd.Process.Kill(); err != nil {
		return nil, "", fmt.Errorf("killing prior process: %w", err)
	}
	oldCmd.Wait()
	addr, err := freePort()
	if err != nil {
		return nil, "", err
	}
	cmd, err := startZeroS3(binPath, storeDir, addr)
	if err != nil {
		return nil, "", err
	}
	return cmd, addr, nil
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

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	storeDir, err := os.MkdirTemp("", "zeros3-m5b-multipart-harness-store-")
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

	const bucket = "mp-bucket"
	const key = "large-object.bin"

	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	check("CreateBucket", err)

	// --- 1. Create + upload 2 parts, list, restart, resume, upload a 3rd
	// part, complete, restart again, then verify. ---
	part1 := bytes.Repeat([]byte{0x11}, 6*1024*1024)
	part2 := bytes.Repeat([]byte{0x22}, 6*1024*1024)
	part3 := bytes.Repeat([]byte{0x33}, 2*1024*1024)
	whole := append(append(append([]byte{}, part1...), part2...), part3...)

	create, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), ContentType: aws.String("application/octet-stream"),
	})
	check("CreateMultipartUpload", err)
	uploadID := aws.ToString(create.UploadId)
	requireTrue("UploadId is non-empty", uploadID != "", "empty upload id")

	up1, err := client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		PartNumber: aws.Int32(1), Body: bytes.NewReader(part1),
	})
	check("UploadPart 1", err)
	up2, err := client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		PartNumber: aws.Int32(2), Body: bytes.NewReader(part2),
	})
	check("UploadPart 2", err)

	listParts, err := client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
	check("ListParts before restart", err)
	if listParts != nil {
		requireTrue("ListParts before restart shows exactly 2 parts", len(listParts.Parts) == 2, fmt.Sprintf("got %d parts", len(listParts.Parts)))
	}

	// Restart mid-lifecycle: an in-progress multipart upload with two
	// uploaded parts must survive a real process restart.
	cmd, addr, err = restartZeroS3(binPath, storeDir, cmd)
	if err != nil {
		log.Fatalf("restart (mid-upload): %v", err)
	}
	client = newClient("http://" + addr)

	listPartsAfterRestart, err := client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
	check("ListParts after mid-upload restart", err)
	if listPartsAfterRestart != nil {
		requireTrue("ListParts after restart still shows exactly 2 parts", len(listPartsAfterRestart.Parts) == 2, fmt.Sprintf("got %d parts", len(listPartsAfterRestart.Parts)))
	}

	up3, err := client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		PartNumber: aws.Int32(3), Body: bytes.NewReader(part3),
	})
	check("UploadPart 3 (after restart)", err)

	complete, err := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{
			{ETag: up1.ETag, PartNumber: aws.Int32(1)},
			{ETag: up2.ETag, PartNumber: aws.Int32(2)},
			{ETag: up3.ETag, PartNumber: aws.Int32(3)},
		}},
	})
	check("CompleteMultipartUpload", err)
	if complete != nil {
		requireTrue("CompleteMultipartUpload returns a non-empty ETag", aws.ToString(complete.ETag) != "", "empty ETag")
	}

	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	check("HeadObject after completion", err)
	if head != nil {
		requireTrue("HeadObject content-length matches whole object", aws.ToInt64(head.ContentLength) == int64(len(whole)),
			fmt.Sprintf("got %d want %d", aws.ToInt64(head.ContentLength), len(whole)))
	}

	get, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	check("GetObject after completion", err)
	var gotBody []byte
	if get != nil {
		gotBody, err = io.ReadAll(get.Body)
		get.Body.Close()
		check("GetObject read body", err)
		requireTrue("GetObject exact byte round trip", bytes.Equal(gotBody, whole), "byte mismatch")
		requireTrue("GetObject SHA-256 matches expected", sha256Hex(gotBody) == sha256Hex(whole), "sha256 mismatch")
	}

	// Range GET straddling the part1/part2 boundary.
	rangeStart, rangeEnd := len(part1)-100, len(part1)+100
	rangeGet, err := client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
		Range: aws.String(fmt.Sprintf("bytes=%d-%d", rangeStart, rangeEnd)),
	})
	check("Range GetObject across a part boundary", err)
	if rangeGet != nil {
		rangeBody, rerr := io.ReadAll(rangeGet.Body)
		rangeGet.Body.Close()
		check("Range GetObject read body", rerr)
		requireTrue("Range GetObject exact byte match", bytes.Equal(rangeBody, whole[rangeStart:rangeEnd+1]), "range byte mismatch")
	}

	// CopyObject of the completed multipart object.
	const copyKey = "large-object-copy.bin"
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(copyKey), CopySource: aws.String(bucket + "/" + key),
	})
	check("CopyObject of completed multipart object", err)
	copyGet, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(copyKey)})
	check("GetObject of the copy", err)
	if copyGet != nil {
		copyBody, cerr := io.ReadAll(copyGet.Body)
		copyGet.Body.Close()
		check("GetObject of the copy read body", cerr)
		requireTrue("Copy of a completed multipart object round-trips bytes", bytes.Equal(copyBody, whole), "byte mismatch")
	}

	// Restart again after completion: the finished object (and only the
	// finished object -- the retired upload session must not reappear).
	cmd, addr, err = restartZeroS3(binPath, storeDir, cmd)
	if err != nil {
		log.Fatalf("restart (after completion): %v", err)
	}
	client = newClient("http://" + addr)

	getAfterRestart2, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	check("GetObject after second restart", err)
	if getAfterRestart2 != nil {
		body2, rerr := io.ReadAll(getAfterRestart2.Body)
		getAfterRestart2.Body.Close()
		check("GetObject after second restart read body", rerr)
		requireTrue("GetObject after second restart SHA-256 matches", sha256Hex(body2) == sha256Hex(whole), "sha256 mismatch after restart")
	}
	_, err = client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
	requireError("ListParts on a completed (retired) upload ID after restart", err)

	// --- 2. Abort scenario. ---
	const abortKey = "aborted-object.bin"
	abortCreate, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(abortKey)})
	check("CreateMultipartUpload (abort scenario)", err)
	abortUploadID := aws.ToString(abortCreate.UploadId)
	_, err = client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(abortKey), UploadId: aws.String(abortUploadID),
		PartNumber: aws.Int32(1), Body: bytes.NewReader(bytes.Repeat([]byte{0x44}, 1024*1024)),
	})
	check("UploadPart (abort scenario)", err)
	_, err = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(abortKey), UploadId: aws.String(abortUploadID)})
	check("AbortMultipartUpload", err)
	_, err = client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String(abortKey), UploadId: aws.String(abortUploadID)})
	requireError("ListParts on an aborted upload ID", err)
	_, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(abortKey)})
	requireError("HeadObject for an aborted multipart upload's key never becomes visible", err)

	// --- 3. Negative scenarios. ---
	_, err = client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String("no-such-key"), UploadId: aws.String("00000000-0000-7000-8000-000000000000")})
	requireError("ListParts with a nonexistent upload ID", err)

	badCompleteCreate, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("bad-complete.bin")})
	check("CreateMultipartUpload (negative-completion scenario)", err)
	badUploadID := aws.ToString(badCompleteCreate.UploadId)
	badPart, err := client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String("bad-complete.bin"), UploadId: aws.String(badUploadID),
		PartNumber: aws.Int32(1), Body: bytes.NewReader(bytes.Repeat([]byte{0x55}, 10000)),
	})
	check("UploadPart (negative-completion scenario)", err)
	_ = badPart
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String("bad-complete.bin"), UploadId: aws.String(badUploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{
			{ETag: aws.String(`"00000000000000000000000000000000"`), PartNumber: aws.Int32(1)},
		}},
	})
	requireError("CompleteMultipartUpload with a wrong ETag", err)

	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String("bad-complete.bin"), UploadId: aws.String(badUploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{}},
	})
	requireError("CompleteMultipartUpload with an empty part list", err)

	// Clean up the still-open upload from the negative-completion scenario
	// so it doesn't linger (not asserted, just hygiene).
	_, _ = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("bad-complete.bin"), UploadId: aws.String(badUploadID)})

	// --- 4. ListMultipartUploads. ---
	lmuCreate1, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("lmu-a.bin")})
	check("CreateMultipartUpload (ListMultipartUploads scenario, a)", err)
	lmuCreate2, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("lmu-b.bin")})
	check("CreateMultipartUpload (ListMultipartUploads scenario, b)", err)
	lmu, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket)})
	check("ListMultipartUploads", err)
	if lmu != nil {
		requireTrue("ListMultipartUploads shows at least the two just-created uploads", len(lmu.Uploads) >= 2, fmt.Sprintf("got %d uploads", len(lmu.Uploads)))
	}
	_, _ = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("lmu-a.bin"), UploadId: lmuCreate1.UploadId})
	_, _ = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("lmu-b.bin"), UploadId: lmuCreate2.UploadId})

	cmd.Process.Kill()
	cmd.Wait()

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed =====\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}
