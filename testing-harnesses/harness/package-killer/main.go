// Package Killer harness: the same frozen AWS SDK for Go v2 test logic,
// run unmodified against two different S3-compatible servers -- ZeroS3 and
// s3rver -- to evidence or refute the claim that ZeroS3 replaces s3rver's
// principal standalone local-S3-server use case (PACKAGE_KILLER.md).
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and s3rver (an npm package) is never installed into,
// vendored into, or imported by the zeros3 Go module -- it is started as
// an ephemeral subprocess purely as comparison infrastructure, exactly
// like zeros3-bin is.
//
// The single hard rule this file exists to prove it follows: runSharedMatrix
// is called once per target and must not branch on which target it is
// running against. Only the connection settings in the `target` struct
// (endpoint, credentials, path-style/region) may differ between the two
// calls -- never the request sequence, assertions, or any special-cased
// workflow.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// target is the only thing allowed to differ between a ZeroS3 run and an
// s3rver run: endpoint, credentials, and addressing/region settings the
// target itself requires. See the package doc comment.
type target struct {
	name            string
	endpoint        string
	accessKeyID     string
	secretAccessKey string
	region          string
	usePathStyle    bool
}

// result is one row of the shared matrix, recorded identically for both
// targets so the final comparison table is symmetric.
type result struct {
	op     string
	err    error
	detail string
}

func (r result) pass() bool { return r.err == nil }

func freePort() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer l.Close()
	return l.Addr().String(), nil
}

func waitListening(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func startZeroS3(binPath, storeDir, addr string) (*exec.Cmd, error) {
	cmd := exec.Command(binPath, "-store", storeDir, "-addr", addr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if !waitListening(addr, 5*time.Second) {
		cmd.Process.Kill()
		return nil, fmt.Errorf("zeros3 did not start listening on %s in time", addr)
	}
	return cmd, nil
}

func startS3rver(binPath, dataDir, host, port string) (*exec.Cmd, error) {
	cmd := exec.Command(binPath, "-d", dataDir, "-a", host, "-p", port, "--no-vhost-buckets")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if !waitListening(host+":"+port, 8*time.Second) {
		cmd.Process.Kill()
		return nil, fmt.Errorf("s3rver did not start listening on %s:%s in time", host, port)
	}
	return cmd, nil
}

func newClient(t target) *s3.Client {
	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(t.region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(t.accessKeyID, t.secretAccessKey, "")),
	)
	if err != nil {
		panic(err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(t.endpoint)
		o.UsePathStyle = t.usePathStyle
	})
}

// runSharedMatrix is the one and only test sequence. It is called
// identically for both targets -- see the package doc comment.
func runSharedMatrix(t target) []result {
	var results []result
	rec := func(op string, err error, detail string) {
		results = append(results, result{op: op, err: err, detail: detail})
	}

	client := newClient(t)
	ctx := context.Background()
	const bucket = "package-killer-bucket"
	const key = "package-killer-object.txt"
	const key2 = "package-killer-prefix/second.txt"
	body := []byte("Package Killer shared matrix payload -- identical bytes sent to both targets.\n")
	bodySum := sha256.Sum256(body)

	// --- CreateBucket ---
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	rec("CreateBucket", err, "")

	// --- ListBuckets ---
	lb, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	foundBucket := false
	if err == nil {
		for _, b := range lb.Buckets {
			if aws.ToString(b.Name) == bucket {
				foundBucket = true
			}
		}
	}
	if err == nil && !foundBucket {
		err = fmt.Errorf("created bucket %q not present in ListBuckets", bucket)
	}
	rec("ListBuckets", err, "")

	// --- PutObject (Content-Type + representative user metadata) ---
	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("text/plain"),
		Metadata:    map[string]string{"purpose": "package-killer-proof", "author": "zeros3-testing"},
	})
	rec("PutObject (Content-Type + metadata)", err, "")

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key2),
		Body:   bytes.NewReader([]byte("second object under a shared prefix\n")),
	})
	rec("PutObject (second key, for ListObjectsV2 prefix)", err, "")

	// --- HeadObject ---
	ho, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err == nil {
		if aws.ToInt64(ho.ContentLength) != int64(len(body)) {
			err = fmt.Errorf("HeadObject Content-Length = %d, want %d", aws.ToInt64(ho.ContentLength), len(body))
		} else if aws.ToString(ho.ContentType) != "text/plain" {
			err = fmt.Errorf("HeadObject Content-Type = %q, want %q", aws.ToString(ho.ContentType), "text/plain")
		} else if ho.Metadata["purpose"] != "package-killer-proof" {
			err = fmt.Errorf("HeadObject metadata[purpose] = %q, want %q", ho.Metadata["purpose"], "package-killer-proof")
		}
	}
	rec("HeadObject (Content-Length/Content-Type/metadata)", err, "")

	// --- GetObject (byte equality + Content-Type + metadata) ---
	go2, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err == nil {
		got, readErr := io.ReadAll(go2.Body)
		go2.Body.Close()
		if readErr != nil {
			err = readErr
		} else if !bytes.Equal(got, body) {
			err = fmt.Errorf("GetObject body mismatch: got %d bytes, want %d bytes (sha256 want %x)", len(got), len(body), bodySum)
		} else if aws.ToString(go2.ContentType) != "text/plain" {
			err = fmt.Errorf("GetObject Content-Type = %q, want %q", aws.ToString(go2.ContentType), "text/plain")
		} else if go2.Metadata["author"] != "zeros3-testing" {
			err = fmt.Errorf("GetObject metadata[author] = %q, want %q", go2.Metadata["author"], "zeros3-testing")
		}
	}
	rec("GetObject (exact bytes + Content-Type + metadata)", err, "")

	// --- ListObjectsV2 (list-content workflow) ---
	lo, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	if err == nil {
		if aws.ToInt32(lo.KeyCount) != 2 {
			err = fmt.Errorf("ListObjectsV2 KeyCount = %d, want 2", aws.ToInt32(lo.KeyCount))
		}
	}
	rec("ListObjectsV2 (list-content workflow)", err, "")

	loPrefix, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String("package-killer-prefix/")})
	if err == nil {
		if aws.ToInt32(loPrefix.KeyCount) != 1 {
			err = fmt.Errorf("ListObjectsV2 with prefix KeyCount = %d, want 1", aws.ToInt32(loPrefix.KeyCount))
		}
	}
	rec("ListObjectsV2 (prefix filter)", err, "")

	// --- Differentiator: Range GET (not a required GO criterion; recorded
	// honestly for both targets, see PACKAGE_KILLER.md "already-stable
	// differentiators") ---
	rg, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Range: aws.String("bytes=0-9")})
	if err == nil {
		got, readErr := io.ReadAll(rg.Body)
		rg.Body.Close()
		if readErr != nil {
			err = readErr
		} else if !bytes.Equal(got, body[:10]) {
			err = fmt.Errorf("Range GET returned %q, want %q", got, body[:10])
		}
	}
	rec("[differentiator] Range GET (bytes=0-9)", err, "not a required Package Killer parity criterion")

	// --- Differentiator: CopyObject ---
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(bucket),
		Key:        aws.String("package-killer-copy.txt"),
		CopySource: aws.String(bucket + "/" + key),
	})
	rec("[differentiator] CopyObject", err, "not a required Package Killer parity criterion")

	// --- DeleteObject + confirm gone ---
	_, err = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	rec("DeleteObject", err, "")

	_, err = client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err == nil {
		err = fmt.Errorf("HeadObject unexpectedly succeeded after DeleteObject")
	} else {
		err = nil // a not-found error here is the expected, passing outcome
	}
	rec("HeadObject confirms object gone after delete", err, "")

	// --- Clean up remaining keys, then DeleteBucket ---
	for _, k := range []string{key2, "package-killer-copy.txt"} {
		_, _ = client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(k)})
	}
	_, err = client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	rec("DeleteBucket", err, "")

	lb2, err := client.ListBuckets(ctx, &s3.ListBucketsInput{})
	if err == nil {
		for _, b := range lb2.Buckets {
			if aws.ToString(b.Name) == bucket {
				err = fmt.Errorf("deleted bucket %q still present in ListBuckets", bucket)
			}
		}
	}
	rec("ListBuckets confirms bucket gone after delete", err, "")

	return results
}

func printResults(name string, results []result) (pass, fail int) {
	fmt.Printf("\n=== %s ===\n", name)
	for _, r := range results {
		if r.pass() {
			pass++
			fmt.Printf("PASS: %s\n", r.op)
		} else {
			fail++
			fmt.Printf("FAIL: %s: %v\n", r.op, r.err)
		}
	}
	return pass, fail
}

func main() {
	zeros3Bin := os.Getenv("ZEROS3_BIN")
	if zeros3Bin == "" {
		fmt.Println("set ZEROS3_BIN to a built zeros3 binary")
		os.Exit(2)
	}
	s3rverBin := os.Getenv("S3RVER_BIN")
	if s3rverBin == "" {
		fmt.Println("set S3RVER_BIN to the installed s3rver executable (e.g. node_modules/.bin/s3rver)")
		os.Exit(2)
	}

	// --- ZeroS3 ---
	zStoreDir, err := os.MkdirTemp("", "pk-zeros3-store-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(zStoreDir)
	zAddr, err := freePort()
	if err != nil {
		panic(err)
	}
	zCmd, err := startZeroS3(zeros3Bin, zStoreDir, zAddr)
	if err != nil {
		panic(err)
	}
	zeros3Results := runSharedMatrix(target{
		name:            "ZeroS3",
		endpoint:        "http://" + zAddr,
		accessKeyID:     "AKIAZEROS3EXAMPLE01",
		secretAccessKey: "zeros3exampleSecretKeyForM1TestingOnly01",
		region:          "us-east-1",
		usePathStyle:    true,
	})
	zCmd.Process.Kill()
	zCmd.Wait()

	// --- s3rver ---
	sDataDir, err := os.MkdirTemp("", "pk-s3rver-data-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(sDataDir)
	sFreeAddr, err := freePort()
	if err != nil {
		panic(err)
	}
	_, sPortAddr, err := net.SplitHostPort(sFreeAddr)
	if err != nil {
		panic(err)
	}
	sCmd, err := startS3rver(s3rverBin, sDataDir, "127.0.0.1", sPortAddr)
	if err != nil {
		panic(err)
	}
	s3rverResults := runSharedMatrix(target{
		name:            "s3rver",
		endpoint:        "http://127.0.0.1:" + sPortAddr,
		accessKeyID:     "S3RVER",
		secretAccessKey: "S3RVER",
		region:          "us-east-1",
		usePathStyle:    true, // --no-vhost-buckets on the s3rver side is the matching target-required addressing setting
	})
	sCmd.Process.Kill()
	sCmd.Wait()

	zPass, zFail := printResults("ZeroS3", zeros3Results)
	sPass, sFail := printResults("s3rver", s3rverResults)

	fmt.Printf("\n=== Summary ===\n")
	fmt.Printf("ZeroS3: %d passed, %d failed\n", zPass, zFail)
	fmt.Printf("s3rver: %d passed, %d failed\n", sPass, sFail)

	requiredOps := map[string]bool{
		"CreateBucket": true, "ListBuckets": true, "DeleteBucket": true,
		"PutObject (Content-Type + metadata)": true, "GetObject (exact bytes + Content-Type + metadata)": true,
		"HeadObject (Content-Length/Content-Type/metadata)": true, "DeleteObject": true,
		"ListObjectsV2 (list-content workflow)": true,
	}
	goDecision := true
	for _, r := range zeros3Results {
		if requiredOps[r.op] && !r.pass() {
			goDecision = false
		}
	}
	for _, r := range s3rverResults {
		if requiredOps[r.op] && !r.pass() {
			goDecision = false
		}
	}
	if goDecision {
		fmt.Println("\nGO: every required Package Killer criterion passed identically on both targets using the same client code, only endpoint/credential/addressing settings changed.")
	} else {
		fmt.Println("\nNO-GO: at least one required Package Killer criterion did not pass identically on both targets.")
		os.Exit(1)
	}
}
