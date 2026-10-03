// Ephemeral, external interoperability harness for ZeroS3 M2.
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 modules purely as
// a black-box client to prove the canonical M2 workflow against a real
// zeros3 server binary over ordinary HTTP with path-style addressing and
// normal/default SDK integrity behavior (no special client configuration
// to force or disable checksums).
package main

import (
	"bytes"
	"context"
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
	fmt.Printf("SDK default RequestChecksumCalculation=%v ResponseChecksumValidation=%v\n",
		cfg.RequestChecksumCalculation, cfg.ResponseChecksumValidation)
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true // path-style addressing, as required
	})
}

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	storeDir, err := os.MkdirTemp("", "zeros3-harness-store-")
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
	endpoint := "http://" + addr
	ctx := context.Background()
	client := newClient(endpoint)

	const bucket = "harness-bucket"
	const key = "greeting.txt"
	body := []byte("Hello from the canonical AWS SDK Go v2 interoperability harness!")

	// 1. ListBuckets on an empty store.
	lb0, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	check("ListBuckets (empty store)", err)
	requireTrue("ListBuckets (empty store) has no harness bucket yet",
		!containsBucket(lb0.Buckets, bucket), "harness bucket already present")

	// 2. CreateBucket.
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	check("CreateBucket", err)

	// 3. HeadBucket.
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	check("HeadBucket (existing)", err)

	// 4. PutObject with ordinary/default SDK integrity behavior -- no
	// ChecksumAlgorithm override, no special client configuration.
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("text/plain; charset=utf-8"),
		Metadata:    map[string]string{"project": "zeros3", "milestone": "m2"},
	})
	check("PutObject (default SDK checksum behavior)", err)

	// 5. HeadObject.
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	check("HeadObject", err)
	if head != nil {
		requireTrue("HeadObject Content-Length matches", aws.ToInt64(head.ContentLength) == int64(len(body)),
			fmt.Sprintf("got %d want %d", aws.ToInt64(head.ContentLength), len(body)))
		requireTrue("HeadObject Content-Type round trips", aws.ToString(head.ContentType) == "text/plain; charset=utf-8",
			aws.ToString(head.ContentType))
		requireTrue("HeadObject metadata round trips", head.Metadata["project"] == "zeros3" && head.Metadata["milestone"] == "m2",
			fmt.Sprintf("%+v", head.Metadata))
	}

	// 6. GetObject: verify exact bytes, Content-Type, metadata.
	get, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	check("GetObject", err)
	if get != nil {
		gotBody, readErr := io.ReadAll(get.Body)
		get.Body.Close()
		check("GetObject read body", readErr)
		requireTrue("GetObject exact byte round trip", bytes.Equal(gotBody, body),
			fmt.Sprintf("got %d bytes, want %d bytes", len(gotBody), len(body)))
		requireTrue("GetObject Content-Type round trips", aws.ToString(get.ContentType) == "text/plain; charset=utf-8",
			aws.ToString(get.ContentType))
		requireTrue("GetObject metadata round trips", get.Metadata["project"] == "zeros3" && get.Metadata["milestone"] == "m2",
			fmt.Sprintf("%+v", get.Metadata))
	}

	// 7. ListObjectsV2: basic listing, then prefix/pagination.
	for i := 0; i < 5; i++ {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(fmt.Sprintf("logs/entry-%d.log", i)),
			Body:   bytes.NewReader([]byte(fmt.Sprintf("log entry %d", i))),
		})
		check(fmt.Sprintf("PutObject logs/entry-%d.log", i), err)
	}
	list, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	check("ListObjectsV2 (all)", err)
	if list != nil {
		requireTrue("ListObjectsV2 (all) KeyCount", aws.ToInt32(list.KeyCount) == 6,
			fmt.Sprintf("got %d", aws.ToInt32(list.KeyCount)))
	}

	listPrefix, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket), Prefix: aws.String("logs/"), Delimiter: aws.String("/"),
	})
	check("ListObjectsV2 (prefix+delimiter)", err)
	if listPrefix != nil {
		requireTrue("ListObjectsV2 (prefix+delimiter) sees 5 log entries", len(listPrefix.Contents) == 5,
			fmt.Sprintf("got %d", len(listPrefix.Contents)))
	}

	var pagedKeys []string
	var token *string
	for i := 0; i < 10; i++ {
		page, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket: aws.String(bucket), MaxKeys: aws.Int32(2), ContinuationToken: token,
		})
		check(fmt.Sprintf("ListObjectsV2 page %d", i), err)
		if page == nil {
			break
		}
		for _, obj := range page.Contents {
			pagedKeys = append(pagedKeys, aws.ToString(obj.Key))
		}
		if !aws.ToBool(page.IsTruncated) {
			break
		}
		token = page.NextContinuationToken
	}
	requireTrue("ListObjectsV2 pagination covers all 6 keys with no duplicates", len(pagedKeys) == 6 && allUnique(pagedKeys),
		fmt.Sprintf("%v", pagedKeys))

	// 8. DeleteObject + verify gone.
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	check("DeleteObject", err)
	_, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	requireTrue("HeadObject after DeleteObject returns not-found", err != nil, "expected an error")

	// Clean up the log entries so the bucket can be deleted.
	for i := 0; i < 5; i++ {
		_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(fmt.Sprintf("logs/entry-%d.log", i))})
		check(fmt.Sprintf("DeleteObject logs/entry-%d.log", i), err)
	}

	// 9. DeleteBucket + verify gone.
	_, err = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	check("DeleteBucket", err)
	_, err = client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	requireTrue("HeadBucket after DeleteBucket returns not-found", err != nil, "expected an error")

	// 10. Restart-persistence run: recreate the bucket/object, restart the
	// server process against the same store directory, and confirm the
	// object survives with identical bytes.
	const persistBucket = "harness-persist-bucket"
	const persistKey = "persisted.bin"
	persistBody := bytes.Repeat([]byte{0xAB, 0xCD, 0xEF, 0x01}, 100000)
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(persistBucket)})
	check("CreateBucket (restart-persistence)", err)
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(persistBucket), Key: aws.String(persistKey), Body: bytes.NewReader(persistBody),
	})
	check("PutObject (restart-persistence)", err)

	if err := cmd.Process.Kill(); err != nil {
		log.Fatalf("failed to stop zeros3 for restart: %v", err)
	}
	cmd.Wait()

	addr2, err := freePort()
	if err != nil {
		log.Fatal(err)
	}
	cmd2, err := startZeroS3(binPath, storeDir, addr2)
	if err != nil {
		log.Fatalf("restarting zeros3: %v", err)
	}
	defer func() {
		cmd2.Process.Kill()
		cmd2.Wait()
	}()
	client2 := newClient("http://" + addr2)
	getAfterRestart, err := client2.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(persistBucket), Key: aws.String(persistKey)})
	check("GetObject after restart", err)
	if getAfterRestart != nil {
		gotBody, readErr := io.ReadAll(getAfterRestart.Body)
		getAfterRestart.Body.Close()
		check("GetObject after restart read body", readErr)
		requireTrue("GetObject after restart exact byte round trip", bytes.Equal(gotBody, persistBody),
			fmt.Sprintf("got %d bytes, want %d bytes", len(gotBody), len(persistBody)))
	}

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed =====\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}

func containsBucket(buckets []types.Bucket, name string) bool {
	for _, b := range buckets {
		if aws.ToString(b.Name) == name {
			return true
		}
	}
	return false
}

func allUnique(ss []string) bool {
	seen := map[string]bool{}
	for _, s := range ss {
		if seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
