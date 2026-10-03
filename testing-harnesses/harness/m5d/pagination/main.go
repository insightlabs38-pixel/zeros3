// Ephemeral, external interoperability harness for ZeroS3 M5-D: ListParts
// and ListMultipartUploads pagination.
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 modules purely as a
// black-box client. It drives the real S3 pagination API against a real
// zeros3 server binary over ordinary HTTP with path-style addressing, using
// only the AWS SDK's low-level S3 client calls (never any internal ZeroS3
// API), with deliberately tiny page sizes (max-parts=2, max-uploads=2) so
// that a small, cheap number of parts/uploads still forces multiple real
// pagination round trips.
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"sort"
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

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	storeDir, err := os.MkdirTemp("", "zeros3-m5d-pagination-harness-store-")
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
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	client := newClient("http://" + addr)
	ctx := context.Background()

	const bucket = "pagination-bucket"
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	check("CreateBucket", err)

	// =========================================================================
	// 1. ListParts pagination: 7 tiny parts, max-parts=2 (deliberately small),
	//    follow part-number-marker across pages.
	// =========================================================================
	const partsKey = "paginated-object.bin"
	create, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(partsKey)})
	check("CreateMultipartUpload (ListParts pagination scenario)", err)
	uploadID := aws.ToString(create.UploadId)

	const totalParts = 7
	for i := 1; i <= totalParts; i++ {
		_, err := client.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: aws.String(bucket), Key: aws.String(partsKey), UploadId: aws.String(uploadID),
			PartNumber: aws.Int32(int32(i)), Body: bytes.NewReader([]byte(fmt.Sprintf("tiny-part-%d", i))),
		})
		check(fmt.Sprintf("UploadPart %d", i), err)
	}

	var seenParts []int32
	var pages int
	partNumberMarker := (*string)(nil)
	for {
		pages++
		if pages > totalParts+2 {
			fail++
			fmt.Printf("FAIL: ListParts pagination: too many pages (possible infinite loop)\n")
			break
		}
		out, err := client.ListParts(ctx, &s3.ListPartsInput{
			Bucket: aws.String(bucket), Key: aws.String(partsKey), UploadId: aws.String(uploadID),
			MaxParts: aws.Int32(2), PartNumberMarker: partNumberMarker,
		})
		check(fmt.Sprintf("ListParts page %d", pages), err)
		if out == nil {
			break
		}
		requireTrue(fmt.Sprintf("ListParts page %d has at most 2 parts", pages), len(out.Parts) <= 2,
			fmt.Sprintf("got %d parts", len(out.Parts)))
		for _, p := range out.Parts {
			seenParts = append(seenParts, aws.ToInt32(p.PartNumber))
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		partNumberMarker = out.NextPartNumberMarker
	}
	requireTrue("ListParts pagination visited more than one page", pages > 1, fmt.Sprintf("only %d page(s)", pages))
	requireTrue("ListParts pagination returned every part exactly once, in order",
		func() bool {
			if len(seenParts) != totalParts {
				return false
			}
			for i, p := range seenParts {
				if p != int32(i+1) {
					return false
				}
			}
			return true
		}(),
		fmt.Sprintf("got %v, want [1..%d] in order", seenParts, totalParts))

	// A single unpaginated ListParts call (default max-parts) must still
	// return every part -- pagination is opt-in via small max-parts, not the
	// only way to get a complete listing.
	full, err := client.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String(partsKey), UploadId: aws.String(uploadID)})
	check("ListParts with default max-parts", err)
	if full != nil {
		requireTrue("ListParts with default max-parts returns all parts and IsTruncated=false",
			len(full.Parts) == totalParts && !aws.ToBool(full.IsTruncated),
			fmt.Sprintf("got %d parts, truncated=%v", len(full.Parts), aws.ToBool(full.IsTruncated)))
	}

	_, _ = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(partsKey), UploadId: aws.String(uploadID)})

	// =========================================================================
	// 2. ListMultipartUploads pagination: multiple keys, one key with two
	//    active uploads, max-uploads=2 (deliberately small), follow
	//    key-marker/upload-id-marker across pages. Also proves completed and
	//    aborted uploads never appear.
	// =========================================================================
	type activeUpload struct{ key, id string }
	var active []activeUpload
	makeUpload := func(key string) string {
		out, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key)})
		check("CreateMultipartUpload (ListMultipartUploads pagination scenario, key="+key+")", err)
		return aws.ToString(out.UploadId)
	}

	for _, key := range []string{"alpha", "charlie", "delta", "echo"} {
		id := makeUpload(key)
		active = append(active, activeUpload{key, id})
	}
	// "bravo" gets two concurrent active uploads, to exercise the upload-id
	// tie-break within one key across a page boundary.
	bravo1 := makeUpload("bravo")
	bravo2 := makeUpload("bravo")
	active = append(active, activeUpload{"bravo", bravo1}, activeUpload{"bravo", bravo2})

	// A completed and an aborted upload must never appear in the listing.
	completeKey := "completed-elsewhere.bin"
	completeUploadID := makeUpload(completeKey)
	upPart, err := client.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(completeKey), UploadId: aws.String(completeUploadID),
		PartNumber: aws.Int32(1), Body: bytes.NewReader([]byte("only part")),
	})
	check("UploadPart (for the completed upload that must be absent from listing)", err)
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(completeKey), UploadId: aws.String(completeUploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{
			{ETag: upPart.ETag, PartNumber: aws.Int32(1)},
		}},
	})
	check("CompleteMultipartUpload (upload that must be absent from listing)", err)

	sort.Slice(active, func(i, j int) bool {
		if active[i].key != active[j].key {
			return active[i].key < active[j].key
		}
		return active[i].id < active[j].id
	})

	var seenUploads []activeUpload
	pages = 0
	keyMarker, uploadIDMarker := (*string)(nil), (*string)(nil)
	for {
		pages++
		if pages > len(active)+2 {
			fail++
			fmt.Printf("FAIL: ListMultipartUploads pagination: too many pages (possible infinite loop)\n")
			break
		}
		out, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{
			Bucket: aws.String(bucket), MaxUploads: aws.Int32(2),
			KeyMarker: keyMarker, UploadIdMarker: uploadIDMarker,
		})
		check(fmt.Sprintf("ListMultipartUploads page %d", pages), err)
		if out == nil {
			break
		}
		requireTrue(fmt.Sprintf("ListMultipartUploads page %d has at most 2 uploads", pages), len(out.Uploads) <= 2,
			fmt.Sprintf("got %d uploads", len(out.Uploads)))
		for _, u := range out.Uploads {
			seenUploads = append(seenUploads, activeUpload{aws.ToString(u.Key), aws.ToString(u.UploadId)})
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		keyMarker, uploadIDMarker = out.NextKeyMarker, out.NextUploadIdMarker
	}
	requireTrue("ListMultipartUploads pagination visited more than one page", pages > 1, fmt.Sprintf("only %d page(s)", pages))
	requireTrue("ListMultipartUploads pagination returned every active upload exactly once, in order",
		func() bool {
			if len(seenUploads) != len(active) {
				return false
			}
			for i := range active {
				if seenUploads[i] != active[i] {
					return false
				}
			}
			return true
		}(),
		fmt.Sprintf("got %+v, want %+v", seenUploads, active))

	sawCompleted, sawAborted := false, false
	for _, u := range seenUploads {
		if u.key == completeKey {
			sawCompleted = true
		}
	}
	requireTrue("completed upload never appears in ListMultipartUploads pagination", !sawCompleted, "completed upload leaked into listing")

	abortKey := "aborted-elsewhere.bin"
	abortUploadID := makeUpload(abortKey)
	_, err = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(abortKey), UploadId: aws.String(abortUploadID)})
	check("AbortMultipartUpload (upload that must be absent from listing)", err)

	afterAbort, err := client.ListMultipartUploads(ctx, &s3.ListMultipartUploadsInput{Bucket: aws.String(bucket)})
	check("ListMultipartUploads after abort", err)
	if afterAbort != nil {
		for _, u := range afterAbort.Uploads {
			if aws.ToString(u.Key) == abortKey {
				sawAborted = true
			}
		}
	}
	requireTrue("aborted upload never appears in ListMultipartUploads", !sawAborted, "aborted upload leaked into listing")

	// Clean up remaining active uploads (hygiene, not asserted).
	for _, u := range active {
		_, _ = client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(u.key), UploadId: aws.String(u.id)})
	}

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed =====\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}
