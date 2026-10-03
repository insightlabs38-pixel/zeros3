// Ephemeral, external interoperability harness for ZeroS3 M3/M3-correction
// CopyObject.
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 modules purely as
// a black-box client to prove CopyObject's real S3 wire behavior (same-
// bucket, cross-bucket, overwrite, both metadata directives, new
// destination identity/timestamp, encoded source keys) against a real
// zeros3 server binary over ordinary HTTP with path-style addressing. It
// never inspects ZeroS3's on-disk store directly -- the zero-new-CAS-
// chunk-payload-bytes proof lives in zeros3's own Go test suite
// (TestCopyObject_SameBucketZeroNewCASChunkBytes and
// TestCopyObject_MetadataDirectiveReplaceUsesNewMetadataZeroNewChunkBytes),
// which can see the store; this harness proves the S3 wire contract,
// including the externally-observable half of the M3 correction pass: a
// copy's Last-Modified is a new, later timestamp than its source's, and
// the source's own Last-Modified never moves.
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
	storeDir, err := os.MkdirTemp("", "zeros3-m3-copy-harness-store-")
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
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	client := newClient("http://" + addr)
	ctx := context.Background()

	const srcBucket = "copy-src-bucket"
	const dstBucket = "copy-dst-bucket"
	const srcKey = "source.bin"
	body := bytes.Repeat([]byte{0x5A}, 512*1024) // 512KiB: spans several CDC chunks

	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(srcBucket)})
	check("CreateBucket (src)", err)
	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(dstBucket)})
	check("CreateBucket (dst)", err)

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(srcBucket), Key: aws.String(srcKey), Body: bytes.NewReader(body),
		ContentType: aws.String("application/octet-stream"), Metadata: map[string]string{"origin": "source"},
	})
	check("PutObject (source)", err)
	headSrcBefore, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(srcBucket), Key: aws.String(srcKey)})
	check("HeadObject (source, before any copy)", err)
	var srcLastModifiedBefore time.Time
	if headSrcBefore != nil && headSrcBefore.LastModified != nil {
		srcLastModifiedBefore = *headSrcBefore.LastModified
	}

	// HTTP's Last-Modified header has one-second resolution; sleep past
	// that granularity so a genuinely new destination timestamp is
	// externally observable, not just theoretically newer.
	time.Sleep(1200 * time.Millisecond)

	// 1. Same-bucket copy, default (COPY) metadata directive.
	const sameBucketDst = "same-bucket-copy.bin"
	copyResp, err := client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(srcBucket),
		Key:        aws.String(sameBucketDst),
		CopySource: aws.String(srcBucket + "/" + srcKey),
	})
	check("CopyObject (same bucket)", err)
	if copyResp != nil {
		requireTrue("CopyObject (same bucket) returns a CopyObjectResult ETag",
			copyResp.CopyObjectResult != nil && aws.ToString(copyResp.CopyObjectResult.ETag) != "",
			"missing ETag in CopyObjectResult")
	}
	get, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(srcBucket), Key: aws.String(sameBucketDst)})
	check("GetObject (same-bucket copy)", err)
	if get != nil {
		gotBody, _ := io.ReadAll(get.Body)
		get.Body.Close()
		requireTrue("same-bucket copy byte-exact round trip", bytes.Equal(gotBody, body),
			fmt.Sprintf("got %d bytes, want %d", len(gotBody), len(body)))
	}
	headCopy, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(srcBucket), Key: aws.String(sameBucketDst)})
	check("HeadObject (same-bucket copy)", err)
	if headCopy != nil {
		requireTrue("COPY directive preserves source metadata", headCopy.Metadata["origin"] == "source", fmt.Sprintf("%+v", headCopy.Metadata))
		requireTrue("COPY directive preserves source Content-Type",
			aws.ToString(headCopy.ContentType) == "application/octet-stream", aws.ToString(headCopy.ContentType))
		if headCopy.LastModified != nil {
			requireTrue("M3 correction: destination Last-Modified is a new, later timestamp than the source's (not reused)",
				headCopy.LastModified.After(srcLastModifiedBefore),
				fmt.Sprintf("dst=%v src=%v", headCopy.LastModified, srcLastModifiedBefore))
		}
	}

	// M3 correction: the source's own Last-Modified must never move as a
	// side effect of being copied.
	headSrcAfter, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(srcBucket), Key: aws.String(srcKey)})
	check("HeadObject (source, after copy)", err)
	if headSrcAfter != nil && headSrcAfter.LastModified != nil {
		requireTrue("M3 correction: source Last-Modified is unchanged by CopyObject",
			headSrcAfter.LastModified.Equal(srcLastModifiedBefore),
			fmt.Sprintf("before=%v after=%v", srcLastModifiedBefore, *headSrcAfter.LastModified))
	}

	// 2. Cross-bucket copy.
	const crossDstKey = "cross-bucket-copy.bin"
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(dstBucket),
		Key:        aws.String(crossDstKey),
		CopySource: aws.String(srcBucket + "/" + srcKey),
	})
	check("CopyObject (cross bucket)", err)
	get2, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(dstBucket), Key: aws.String(crossDstKey)})
	check("GetObject (cross-bucket copy)", err)
	if get2 != nil {
		gotBody, _ := io.ReadAll(get2.Body)
		get2.Body.Close()
		requireTrue("cross-bucket copy byte-exact round trip", bytes.Equal(gotBody, body),
			fmt.Sprintf("got %d bytes, want %d", len(gotBody), len(body)))
	}

	// 3. Overwrite an existing destination.
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(dstBucket), Key: aws.String(crossDstKey), Body: bytes.NewReader([]byte("stale content")),
	})
	check("PutObject (stale content to overwrite)", err)
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(dstBucket),
		Key:        aws.String(crossDstKey),
		CopySource: aws.String(srcBucket + "/" + srcKey),
	})
	check("CopyObject (overwrite existing destination)", err)
	get3, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(dstBucket), Key: aws.String(crossDstKey)})
	check("GetObject (after overwrite copy)", err)
	if get3 != nil {
		gotBody, _ := io.ReadAll(get3.Body)
		get3.Body.Close()
		requireTrue("overwrite copy replaces stale content with source content", bytes.Equal(gotBody, body),
			fmt.Sprintf("got %d bytes, want %d", len(gotBody), len(body)))
	}

	// 4. Missing source.
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(dstBucket),
		Key:        aws.String("wont-exist.bin"),
		CopySource: aws.String(srcBucket + "/does-not-exist.bin"),
	})
	requireTrue("CopyObject with a missing source is rejected", err != nil, "expected an error")

	// 5. Missing destination bucket.
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String("no-such-destination-bucket"),
		Key:        aws.String("wont-exist.bin"),
		CopySource: aws.String(srcBucket + "/" + srcKey),
	})
	requireTrue("CopyObject with a missing destination bucket is rejected", err != nil, "expected an error")

	// 6. REPLACE metadata directive.
	time.Sleep(1200 * time.Millisecond)
	const replaceKey = "replaced-metadata-copy.bin"
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:            aws.String(srcBucket),
		Key:               aws.String(replaceKey),
		CopySource:        aws.String(srcBucket + "/" + srcKey),
		MetadataDirective: types.MetadataDirectiveReplace,
		Metadata:          map[string]string{"origin": "replaced"},
		ContentType:       aws.String("text/replaced"),
	})
	check("CopyObject (REPLACE metadata directive)", err)
	headReplace, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(srcBucket), Key: aws.String(replaceKey)})
	check("HeadObject (REPLACE metadata directive)", err)
	if headReplace != nil {
		requireTrue("REPLACE directive uses the new metadata", headReplace.Metadata["origin"] == "replaced", fmt.Sprintf("%+v", headReplace.Metadata))
		requireTrue("REPLACE directive uses the new Content-Type", aws.ToString(headReplace.ContentType) == "text/replaced", aws.ToString(headReplace.ContentType))
		if headReplace.LastModified != nil {
			requireTrue("M3 correction: REPLACE destination Last-Modified is also a new, later timestamp",
				headReplace.LastModified.After(srcLastModifiedBefore),
				fmt.Sprintf("dst=%v src=%v", headReplace.LastModified, srcLastModifiedBefore))
		}
	}
	getReplace, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(srcBucket), Key: aws.String(replaceKey)})
	check("GetObject (REPLACE metadata directive)", err)
	if getReplace != nil {
		gotBody, _ := io.ReadAll(getReplace.Body)
		getReplace.Body.Close()
		requireTrue("REPLACE directive still copies the exact source bytes", bytes.Equal(gotBody, body),
			fmt.Sprintf("got %d bytes, want %d", len(gotBody), len(body)))
	}

	// 7. M3 correction pass (A2): x-amz-copy-source with tricky source
	// keys, sent the way the pinned AWS SDK Go v2 actually sends
	// CopySource on the wire -- completely raw, with zero percent-
	// encoding of its own (confirmed by direct wire inspection during the
	// correction pass). ZeroS3 must decode these leniently rather than
	// erroring on a literal, non-escaped '%'.
	trickyKeys := []string{
		"with space.bin",
		"100%done.bin",
		"a+b plus.bin",
		"dir/sub/tricky.bin",
	}
	for i, key := range trickyKeys {
		trickyBody := bytes.Repeat([]byte{byte(0x40 + i)}, 4096)
		_, err = client.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(srcBucket), Key: aws.String(key), Body: bytes.NewReader(trickyBody),
		})
		check(fmt.Sprintf("PutObject (tricky source key %q)", key), err)

		dstKey := fmt.Sprintf("tricky-copy-%d.bin", i)
		_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
			Bucket:     aws.String(srcBucket),
			Key:        aws.String(dstKey),
			CopySource: aws.String(srcBucket + "/" + key), // raw, unencoded -- matches the real SDK's wire form
		})
		check(fmt.Sprintf("CopyObject (tricky source key %q)", key), err)

		getTricky, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(srcBucket), Key: aws.String(dstKey)})
		check(fmt.Sprintf("GetObject (copy of tricky source key %q)", key), err)
		if getTricky != nil {
			gotBody, _ := io.ReadAll(getTricky.Body)
			getTricky.Body.Close()
			requireTrue(fmt.Sprintf("copy of tricky source key %q round-trips exact bytes", key),
				bytes.Equal(gotBody, trickyBody), fmt.Sprintf("got %d bytes, want %d", len(gotBody), len(trickyBody)))
		}
	}

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed =====\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}
