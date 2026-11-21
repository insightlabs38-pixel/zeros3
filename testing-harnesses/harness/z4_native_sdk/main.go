// z4_native_sdk checks ordinary AWS SDK behavior on an already prepared
// native-authority replica. The caller supplies a disposable local store.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

func run(endpoint, bucket, key, original string) ([]string, error) {
	want, err := os.ReadFile(original)
	if err != nil {
		return nil, err
	}
	if len(want) < 4096 {
		return nil, errors.New("fixture must contain at least 4096 bytes")
	}
	creds := credentials.NewStaticCredentialsProvider(os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"), "")
	client := s3.New(s3.Options{Region: "us-east-1", Credentials: creds, BaseEndpoint: aws.String(endpoint), UsePathStyle: true,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var passed []string
	read := func(name string, expected []byte, span *string) error {
		body, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &name, Range: span})
		if err != nil {
			return err
		}
		defer body.Body.Close()
		data, err := io.ReadAll(io.LimitReader(body.Body, int64(len(expected))+1))
		if err != nil {
			return err
		}
		if len(data) != len(expected) || sha256.Sum256(data) != sha256.Sum256(expected) {
			return fmt.Errorf("%s returned different bytes", name)
		}
		return nil
	}
	if err := read(key, want, nil); err != nil {
		return passed, fmt.Errorf("native GET: %w", err)
	}
	passed = append(passed, "native_get")
	off := len(want)/2 - 2048
	span := fmt.Sprintf("bytes=%d-%d", off, off+4095)
	if err := read(key, want[off:off+4096], &span); err != nil {
		return passed, fmt.Errorf("native range: %w", err)
	}
	passed = append(passed, "native_range")
	head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: &bucket, Key: &key})
	if err != nil || aws.ToInt64(head.ContentLength) != int64(len(want)) || aws.ToString(head.ETag) == "" {
		return passed, fmt.Errorf("native HEAD failed metadata check: %v", err)
	}
	passed = append(passed, "native_head")

	_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: &bucket, Key: &key,
		IfNoneMatch: aws.String("*"), Body: strings.NewReader("conditional write must fail")})
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "PreconditionFailed" {
		return passed, fmt.Errorf("conditional PUT against native object: %v", err)
	}
	_, err = client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key, IfMatch: aws.String(`"invalid-etag"`)})
	if !errors.As(err, &apiErr) || apiErr.ErrorCode() != "PreconditionFailed" {
		return passed, fmt.Errorf("conditional GET against native object: %v", err)
	}
	passed = append(passed, "conditional_put", "conditional_get")

	copyKey := "external-sdk-copy"
	_, err = client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: &bucket, Key: &copyKey, CopySource: aws.String(bucket + "/" + url.PathEscape(key))})
	if err != nil {
		return passed, fmt.Errorf("copy native object: %w", err)
	}
	if err := read(copyKey, want, nil); err != nil {
		return passed, fmt.Errorf("read copy of native object: %w", err)
	}
	passed = append(passed, "copy_native")

	meta := "external-sdk-multipart"
	upload, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &bucket, Key: &meta})
	if err != nil {
		return passed, err
	}
	payload := bytes.Repeat([]byte("sdk multipart 12"), 400000)
	splits := [][]byte{payload[:5<<20], payload[5<<20:]}
	var parts []types.CompletedPart
	for i, body := range splits {
		number := int32(i + 1)
		part, err := client.UploadPart(ctx, &s3.UploadPartInput{Bucket: &bucket, Key: &meta, UploadId: upload.UploadId,
			PartNumber: &number, Body: bytes.NewReader(body)})
		if err != nil {
			return passed, err
		}
		parts = append(parts, types.CompletedPart{ETag: part.ETag, PartNumber: &number})
	}
	_, err = client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: &bucket, Key: &meta,
		UploadId: upload.UploadId, MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}})
	if err != nil {
		return passed, err
	}
	if err := read(meta, payload, nil); err != nil {
		return passed, err
	}
	passed = append(passed, "multipart_exact")
	return passed, nil
}

func main() {
	endpoint := flag.String("endpoint", "", "URL of the disposable ZeroS3 server")
	bucket := flag.String("bucket", "", "bucket containing the native replica")
	key := flag.String("key", "", "key of the native replica")
	original := flag.String("original", "", "file independently matching its exact object bytes")
	flag.Parse()
	if *endpoint == "" || *bucket == "" || *key == "" || *original == "" {
		log.Fatal("-endpoint, -bucket, -key and -original are required")
	}
	passed, err := run(*endpoint, *bucket, *key, *original)
	if reportErr := json.NewEncoder(os.Stdout).Encode(map[string]any{"passed": passed}); reportErr != nil {
		log.Fatal(reportErr)
	}
	if err != nil {
		log.Fatal(err)
	}
}
