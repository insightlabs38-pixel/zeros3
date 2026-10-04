package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

type awsClient struct {
	c   *s3.Client
	ps  *s3.PresignClient
	ctx context.Context
}

// NewAWS returns the AWS SDK for Go v2 client with default SDK behavior
// (path-style addressing, default checksum behavior).
func NewAWS(cfg Config) (Client, error) {
	ctx := context.Background()
	ac, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(cfg.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")),
	)
	if err != nil {
		return nil, err
	}
	c := s3.NewFromConfig(ac, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = true
	})
	return &awsClient{c: c, ps: s3.NewPresignClient(c), ctx: ctx}, nil
}

func (a *awsClient) Name() string         { return "aws-sdk-go-v2" }
func (a *awsClient) Supports(string) bool { return true }

func awsErr(err error) error {
	if err == nil {
		return nil
	}
	se := &S3Error{Err: err}
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		se.Status = re.HTTPStatusCode()
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		se.Code = ae.ErrorCode()
	}
	return se
}

func (a *awsClient) ListBuckets() ([]string, error) {
	out, err := a.c.ListBuckets(a.ctx, &s3.ListBucketsInput{})
	if err != nil {
		return nil, awsErr(err)
	}
	var names []string
	for _, b := range out.Buckets {
		names = append(names, aws.ToString(b.Name))
	}
	return names, nil
}

func (a *awsClient) CreateBucket(b string) error {
	_, err := a.c.CreateBucket(a.ctx, &s3.CreateBucketInput{Bucket: &b})
	return awsErr(err)
}

func (a *awsClient) HeadBucket(b string) error {
	_, err := a.c.HeadBucket(a.ctx, &s3.HeadBucketInput{Bucket: &b})
	return awsErr(err)
}

func (a *awsClient) DeleteBucket(b string) error {
	_, err := a.c.DeleteBucket(a.ctx, &s3.DeleteBucketInput{Bucket: &b})
	return awsErr(err)
}

func (a *awsClient) Location(b string) (string, error) {
	out, err := a.c.GetBucketLocation(a.ctx, &s3.GetBucketLocationInput{Bucket: &b})
	if err != nil {
		return "", awsErr(err)
	}
	if out.LocationConstraint == "" {
		return "us-east-1", nil // S3 convention: the empty constraint is us-east-1
	}
	return string(out.LocationConstraint), nil
}

func (a *awsClient) Put(b, k string, body []byte, o PutOpts) (string, error) {
	in := &s3.PutObjectInput{Bucket: &b, Key: &k, Body: bytes.NewReader(body), Metadata: o.Meta}
	if o.ContentType != "" {
		in.ContentType = &o.ContentType
	}
	if o.ContentMD5 != "" {
		in.ContentMD5 = &o.ContentMD5
	}
	if o.CRC32 != "" {
		in.ChecksumCRC32 = &o.CRC32
	}
	if o.IfNoneMatch != "" {
		in.IfNoneMatch = &o.IfNoneMatch
	}
	if o.IfMatch != "" {
		in.IfMatch = &o.IfMatch
	}
	out, err := a.c.PutObject(a.ctx, in)
	if err != nil {
		return "", awsErr(err)
	}
	return unquote(aws.ToString(out.ETag)), nil
}

func (a *awsClient) Head(b, k string) (Obj, error) {
	out, err := a.c.HeadObject(a.ctx, &s3.HeadObjectInput{Bucket: &b, Key: &k})
	if err != nil {
		return Obj{}, awsErr(err)
	}
	return Obj{ETag: unquote(aws.ToString(out.ETag)), ContentType: aws.ToString(out.ContentType), Meta: lowerKeys(out.Metadata), Size: aws.ToInt64(out.ContentLength)}, nil
}

func (a *awsClient) Get(b, k string, o GetOpts) (Obj, error) {
	in := &s3.GetObjectInput{Bucket: &b, Key: &k}
	if o.Range != "" {
		in.Range = &o.Range
	}
	if o.IfNoneMatch != "" {
		in.IfNoneMatch = &o.IfNoneMatch
	}
	if o.IfMatch != "" {
		in.IfMatch = &o.IfMatch
	}
	out, err := a.c.GetObject(a.ctx, in)
	if err != nil {
		return Obj{}, awsErr(err)
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return Obj{}, awsErr(err)
	}
	return Obj{Body: body, ETag: unquote(aws.ToString(out.ETag)), ContentType: aws.ToString(out.ContentType), Meta: lowerKeys(out.Metadata),
		Size: int64(len(body)), ContentRange: aws.ToString(out.ContentRange)}, nil
}

func (a *awsClient) Delete(b, k string) error {
	_, err := a.c.DeleteObject(a.ctx, &s3.DeleteObjectInput{Bucket: &b, Key: &k})
	return awsErr(err)
}

func (a *awsClient) DeleteMany(b string, keys []string, quiet bool) ([]string, map[string]string, error) {
	ids := make([]types.ObjectIdentifier, len(keys))
	for i := range keys {
		ids[i] = types.ObjectIdentifier{Key: &keys[i]}
	}
	out, err := a.c.DeleteObjects(a.ctx, &s3.DeleteObjectsInput{Bucket: &b, Delete: &types.Delete{Objects: ids, Quiet: aws.Bool(quiet)}})
	if err != nil {
		return nil, nil, awsErr(err)
	}
	deleted := []string{}
	for _, d := range out.Deleted {
		deleted = append(deleted, aws.ToString(d.Key))
	}
	failed := map[string]string{}
	for _, e := range out.Errors {
		failed[aws.ToString(e.Key)] = aws.ToString(e.Code)
	}
	return deleted, failed, nil
}

func (a *awsClient) List(b string, o ListOpts) (ListPage, error) {
	in := &s3.ListObjectsV2Input{Bucket: &b}
	if o.Prefix != "" {
		in.Prefix = &o.Prefix
	}
	if o.Delimiter != "" {
		in.Delimiter = &o.Delimiter
	}
	if o.Token != "" {
		in.ContinuationToken = &o.Token
	}
	if o.Max > 0 {
		in.MaxKeys = aws.Int32(int32(o.Max))
	}
	dec := func(s string) string { return s }
	if o.EncodingURL {
		in.EncodingType = types.EncodingTypeUrl
		// The Go SDK does not decode for the caller (boto3 does).
		dec = func(s string) string {
			if d, err := url.QueryUnescape(s); err == nil {
				return d
			}
			return s
		}
	}
	out, err := a.c.ListObjectsV2(a.ctx, in)
	if err != nil {
		return ListPage{}, awsErr(err)
	}
	pg := ListPage{Truncated: aws.ToBool(out.IsTruncated), Next: aws.ToString(out.NextContinuationToken)}
	for _, c := range out.Contents {
		pg.Keys = append(pg.Keys, dec(aws.ToString(c.Key)))
	}
	for _, p := range out.CommonPrefixes {
		pg.Prefixes = append(pg.Prefixes, dec(aws.ToString(p.Prefix)))
	}
	return pg, nil
}

func (a *awsClient) Copy(dstB, dstK, srcB, srcK string, replace *PutOpts) (string, error) {
	src := srcB + "/" + (&url.URL{Path: srcK}).EscapedPath()
	in := &s3.CopyObjectInput{Bucket: &dstB, Key: &dstK, CopySource: &src}
	if replace != nil {
		in.MetadataDirective = types.MetadataDirectiveReplace
		in.Metadata = replace.Meta
		if replace.ContentType != "" {
			in.ContentType = &replace.ContentType
		}
	}
	out, err := a.c.CopyObject(a.ctx, in)
	if err != nil {
		return "", awsErr(err)
	}
	return unquote(aws.ToString(out.CopyObjectResult.ETag)), nil
}

func (a *awsClient) MPCreate(b, k string, o PutOpts) (string, error) {
	in := &s3.CreateMultipartUploadInput{Bucket: &b, Key: &k, Metadata: o.Meta}
	if o.ContentType != "" {
		in.ContentType = &o.ContentType
	}
	out, err := a.c.CreateMultipartUpload(a.ctx, in)
	if err != nil {
		return "", awsErr(err)
	}
	return aws.ToString(out.UploadId), nil
}

func (a *awsClient) MPPart(b, k, id string, n int, body []byte) (string, error) {
	out, err := a.c.UploadPart(a.ctx, &s3.UploadPartInput{Bucket: &b, Key: &k, UploadId: &id, PartNumber: aws.Int32(int32(n)), Body: bytes.NewReader(body)})
	if err != nil {
		return "", awsErr(err)
	}
	return unquote(aws.ToString(out.ETag)), nil
}

func (a *awsClient) MPParts(b, k, id string) ([]Part, error) {
	out, err := a.c.ListParts(a.ctx, &s3.ListPartsInput{Bucket: &b, Key: &k, UploadId: &id})
	if err != nil {
		return nil, awsErr(err)
	}
	var ps []Part
	for _, p := range out.Parts {
		ps = append(ps, Part{N: int(aws.ToInt32(p.PartNumber)), ETag: unquote(aws.ToString(p.ETag)), Size: aws.ToInt64(p.Size)})
	}
	return ps, nil
}

func (a *awsClient) MPUploads(b string) ([]Upload, error) {
	out, err := a.c.ListMultipartUploads(a.ctx, &s3.ListMultipartUploadsInput{Bucket: &b})
	if err != nil {
		return nil, awsErr(err)
	}
	var us []Upload
	for _, u := range out.Uploads {
		us = append(us, Upload{Key: aws.ToString(u.Key), ID: aws.ToString(u.UploadId)})
	}
	return us, nil
}

func (a *awsClient) MPComplete(b, k, id string, parts []Part) (string, error) {
	cp := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		cp[i] = types.CompletedPart{PartNumber: aws.Int32(int32(p.N)), ETag: aws.String(`"` + p.ETag + `"`)}
	}
	out, err := a.c.CompleteMultipartUpload(a.ctx, &s3.CompleteMultipartUploadInput{Bucket: &b, Key: &k, UploadId: &id, MultipartUpload: &types.CompletedMultipartUpload{Parts: cp}})
	if err != nil {
		return "", awsErr(err)
	}
	return unquote(aws.ToString(out.ETag)), nil
}

func (a *awsClient) MPAbort(b, k, id string) error {
	_, err := a.c.AbortMultipartUpload(a.ctx, &s3.AbortMultipartUploadInput{Bucket: &b, Key: &k, UploadId: &id})
	return awsErr(err)
}

func (a *awsClient) PresignGet(b, k string, ttl time.Duration) (string, error) {
	r, err := a.ps.PresignGetObject(a.ctx, &s3.GetObjectInput{Bucket: &b, Key: &k}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", err
	}
	return r.URL, nil
}

func (a *awsClient) PresignPut(b, k string, ttl time.Duration) (string, http.Header, error) {
	r, err := a.ps.PresignPutObject(a.ctx, &s3.PutObjectInput{Bucket: &b, Key: &k}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", nil, err
	}
	return r.URL, r.SignedHeader, nil
}
