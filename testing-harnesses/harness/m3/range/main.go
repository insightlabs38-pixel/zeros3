// Ephemeral, external interoperability harness for ZeroS3 M3 single-range
// GET.
//
// This program is NOT part of ZeroS3, is never linked into
// the zeros3 binary, and pulls in the pinned AWS SDK for Go v2 modules purely as
// a black-box client to prove Range GET's real S3 wire behavior (single
// byte, start/middle/end, open-ended, suffix, a region crossing multiple
// CDC chunks, and an unsatisfiable range) against a real zeros3 server
// binary over ordinary HTTP with path-style addressing.
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

// deterministicBody returns n bytes of non-repeating, easily-sliced
// content (byte i is i mod 251, a prime just under 256) so any [start,end]
// slice can be recomputed and compared exactly without storing the whole
// buffer twice.
func deterministicBody(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

func main() {
	binPath := os.Getenv("ZEROS3_BIN")
	if binPath == "" {
		binPath = "./zeros3-bin"
	}
	storeDir, err := os.MkdirTemp("", "zeros3-m3-range-harness-store-")
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

	const bucket = "range-bucket"
	const key = "range-object.bin"
	body := deterministicBody(500 * 1024) // spans several CDC chunks (max chunk size is 256KiB)
	size := int64(len(body))

	_, err = client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	check("CreateBucket", err)
	_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(body)})
	check("PutObject", err)

	cases := []struct {
		name               string
		rangeHeader        string
		wantStart, wantEnd int64
	}{
		{"first byte", "bytes=0-0", 0, 0},
		{"100 bytes at the midpoint", fmt.Sprintf("bytes=%d-%d", size/2, size/2+99), size / 2, size/2 + 99},
		{"last byte", fmt.Sprintf("bytes=%d-%d", size-1, size-1), size - 1, size - 1},
		{"open-ended start", fmt.Sprintf("bytes=%d-", size-2000), size - 2000, size - 1},
		{"suffix range", "bytes=-500", size - 500, size - 1},
		{"a region likely crossing a CDC chunk boundary", "bytes=200000-200300", 200000, 200300},
	}
	for _, c := range cases {
		name := "Range GET: " + c.name
		out, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Range: aws.String(c.rangeHeader)})
		if err != nil {
			check(name, err)
			continue
		}
		gotBody, readErr := io.ReadAll(out.Body)
		out.Body.Close()
		check(name+" (read body)", readErr)
		wantLen := c.wantEnd - c.wantStart + 1
		requireTrue(name+" ContentRange header present", aws.ToString(out.ContentRange) != "", "missing ContentRange")
		requireTrue(name+" body length matches", int64(len(gotBody)) == wantLen, fmt.Sprintf("got %d want %d", len(gotBody), wantLen))
		want := body[c.wantStart : c.wantEnd+1]
		requireTrue(name+" body bytes match", bytes.Equal(gotBody, want), "byte content mismatch")
	}

	// Unsatisfiable range: entirely past the object's end.
	_, err = client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Range: aws.String(fmt.Sprintf("bytes=%d-%d", size+100, size+200)),
	})
	requireTrue("an unsatisfiable range is rejected", err != nil, "expected an error (416)")

	fmt.Printf("\n===== SUMMARY: %d passed, %d failed =====\n", pass, fail)
	if fail > 0 {
		os.Exit(1)
	}
}
