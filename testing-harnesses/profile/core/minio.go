package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type minioClient struct {
	c      *minio.Client
	core   minio.Core
	disc   *minio.Client // no fixed region: its GetBucketLocation really queries ?location
	region string
	ctx    context.Context
}

// NewMinio returns the minio-go client. A second client without a fixed
// region performs bucket-location discovery, which minio-go otherwise
// answers from its own configuration without contacting the server.
func NewMinio(cfg Config) (Client, error) {
	host := strings.TrimPrefix(strings.TrimPrefix(cfg.Endpoint, "http://"), "https://")
	secure := strings.HasPrefix(cfg.Endpoint, "https://")
	creds := credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, "")
	c, err := minio.New(host, &minio.Options{Creds: creds, Region: cfg.Region, Secure: secure})
	if err != nil {
		return nil, err
	}
	disc, err := minio.New(host, &minio.Options{Creds: creds, Secure: secure})
	if err != nil {
		return nil, err
	}
	return &minioClient{c: c, core: minio.Core{Client: c}, disc: disc, region: cfg.Region, ctx: context.Background()}, nil
}

func (m *minioClient) Name() string           { return "minio-go" }
func (m *minioClient) Supports(f string) bool { return f != "crc32" }

func minioErr(err error) error {
	if err == nil {
		return nil
	}
	r := minio.ToErrorResponse(err)
	return &S3Error{Status: r.StatusCode, Code: r.Code, Err: err}
}

func (m *minioClient) ListBuckets() ([]string, error) {
	bs, err := m.c.ListBuckets(m.ctx)
	var names []string
	for _, b := range bs {
		names = append(names, b.Name)
	}
	return names, minioErr(err)
}

func (m *minioClient) CreateBucket(b string) error {
	return minioErr(m.c.MakeBucket(m.ctx, b, minio.MakeBucketOptions{Region: m.region}))
}

func (m *minioClient) HeadBucket(b string) error {
	ok, err := m.c.BucketExists(m.ctx, b)
	if err != nil {
		return minioErr(err)
	}
	if !ok {
		return &S3Error{Status: 404, Code: "NoSuchBucket", Err: errors.New("bucket does not exist")}
	}
	return nil
}

func (m *minioClient) DeleteBucket(b string) error { return minioErr(m.c.RemoveBucket(m.ctx, b)) }

func (m *minioClient) Location(b string) (string, error) {
	loc, err := m.disc.GetBucketLocation(m.ctx, b)
	return loc, minioErr(err)
}

func (m *minioClient) Put(b, k string, body []byte, o PutOpts) (string, error) {
	opts := minio.PutObjectOptions{ContentType: o.ContentType, UserMetadata: o.Meta, DisableMultipart: true}
	if o.IfMatch != "" {
		opts.SetMatchETag(o.IfMatch)
	}
	if o.IfNoneMatch != "" {
		opts.SetMatchETagExcept(o.IfNoneMatch)
	}
	if o.ContentMD5 != "" { // Core.PutObject takes the caller's Content-MD5 verbatim
		info, err := m.core.PutObject(m.ctx, b, k, bytes.NewReader(body), int64(len(body)), o.ContentMD5, "", opts)
		return unquote(info.ETag), minioErr(err)
	}
	info, err := m.c.PutObject(m.ctx, b, k, bytes.NewReader(body), int64(len(body)), opts)
	return unquote(info.ETag), minioErr(err)
}

func minioObj(info minio.ObjectInfo) Obj {
	return Obj{ETag: unquote(info.ETag), ContentType: info.ContentType, Meta: lowerKeys(info.UserMetadata), Size: info.Size}
}

func (m *minioClient) Head(b, k string) (Obj, error) {
	info, err := m.c.StatObject(m.ctx, b, k, minio.StatObjectOptions{})
	if err != nil {
		return Obj{}, minioErr(err)
	}
	return minioObj(info), nil
}

func (m *minioClient) Get(b, k string, o GetOpts) (Obj, error) {
	var opts minio.GetObjectOptions
	if o.Range != "" {
		opts.Set("Range", o.Range)
	}
	if o.IfNoneMatch != "" {
		opts.Set("If-None-Match", o.IfNoneMatch)
	}
	if o.IfMatch != "" {
		opts.Set("If-Match", o.IfMatch)
	}
	rc, info, hdr, err := m.core.GetObject(m.ctx, b, k, opts)
	if err != nil {
		return Obj{}, minioErr(err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return Obj{}, minioErr(err)
	}
	obj := minioObj(info)
	obj.Body, obj.Size, obj.ContentRange = body, int64(len(body)), hdr.Get("Content-Range")
	return obj, nil
}

func (m *minioClient) Delete(b, k string) error {
	return minioErr(m.c.RemoveObject(m.ctx, b, k, minio.RemoveObjectOptions{}))
}

// DeleteMany always uses minio-go's DeleteObjects call, which requests
// Quiet=true; Deleted keys are therefore never reported (nil).
func (m *minioClient) DeleteMany(b string, keys []string, _ bool) ([]string, map[string]string, error) {
	ch := make(chan minio.ObjectInfo, len(keys))
	for _, k := range keys {
		ch <- minio.ObjectInfo{Key: k}
	}
	close(ch)
	failed := map[string]string{}
	var top error
	for e := range m.c.RemoveObjects(m.ctx, b, ch, minio.RemoveObjectsOptions{}) {
		if e.Err == nil {
			continue
		}
		// minio-go reports a failed request (e.g. a missing bucket) once per key.
		if r := minio.ToErrorResponse(e.Err); e.ObjectName == "" || r.Code == "NoSuchBucket" {
			top = minioErr(e.Err)
		} else {
			failed[e.ObjectName] = minio.ToErrorResponse(e.Err).Code
		}
	}
	return nil, failed, top
}

func (m *minioClient) List(b string, o ListOpts) (ListPage, error) {
	res, err := m.core.ListObjectsV2(b, o.Prefix, "", o.Token, o.Delimiter, o.Max)
	if err != nil {
		return ListPage{}, minioErr(err)
	}
	pg := ListPage{Truncated: res.IsTruncated, Next: res.NextContinuationToken}
	for _, c := range res.Contents {
		pg.Keys = append(pg.Keys, c.Key)
	}
	for _, p := range res.CommonPrefixes {
		pg.Prefixes = append(pg.Prefixes, p.Prefix)
	}
	return pg, nil
}

func (m *minioClient) Copy(dstB, dstK, srcB, srcK string, replace *PutOpts) (string, error) {
	dst := minio.CopyDestOptions{Bucket: dstB, Object: dstK}
	if replace != nil {
		dst.ReplaceMetadata, dst.UserMetadata = true, replace.Meta
		if replace.ContentType != "" {
			dst.UserMetadata = map[string]string{"Content-Type": replace.ContentType}
			for k, v := range replace.Meta {
				dst.UserMetadata[k] = v
			}
		}
	}
	info, err := m.c.CopyObject(m.ctx, dst, minio.CopySrcOptions{Bucket: srcB, Object: srcK})
	return unquote(info.ETag), minioErr(err)
}

func (m *minioClient) MPCreate(b, k string, o PutOpts) (string, error) {
	id, err := m.core.NewMultipartUpload(m.ctx, b, k, minio.PutObjectOptions{ContentType: o.ContentType, UserMetadata: o.Meta})
	return id, minioErr(err)
}

func (m *minioClient) MPPart(b, k, id string, n int, body []byte) (string, error) {
	p, err := m.core.PutObjectPart(m.ctx, b, k, id, n, bytes.NewReader(body), int64(len(body)), minio.PutObjectPartOptions{})
	return unquote(p.ETag), minioErr(err)
}

func (m *minioClient) MPParts(b, k, id string) ([]Part, error) {
	res, err := m.core.ListObjectParts(m.ctx, b, k, id, 0, 1000)
	if err != nil {
		return nil, minioErr(err)
	}
	var ps []Part
	for _, p := range res.ObjectParts {
		ps = append(ps, Part{N: p.PartNumber, ETag: unquote(p.ETag), Size: p.Size})
	}
	return ps, nil
}

func (m *minioClient) MPUploads(b string) ([]Upload, error) {
	res, err := m.core.ListMultipartUploads(m.ctx, b, "", "", "", "", 1000)
	if err != nil {
		return nil, minioErr(err)
	}
	var us []Upload
	for _, u := range res.Uploads {
		us = append(us, Upload{Key: u.Key, ID: u.UploadID})
	}
	return us, nil
}

func (m *minioClient) MPComplete(b, k, id string, parts []Part) (string, error) {
	cp := make([]minio.CompletePart, len(parts))
	for i, p := range parts {
		cp[i] = minio.CompletePart{PartNumber: p.N, ETag: p.ETag}
	}
	info, err := m.core.CompleteMultipartUpload(m.ctx, b, k, id, cp, minio.PutObjectOptions{})
	return unquote(info.ETag), minioErr(err)
}

func (m *minioClient) MPAbort(b, k, id string) error {
	return minioErr(m.core.AbortMultipartUpload(m.ctx, b, k, id))
}

func (m *minioClient) PresignGet(b, k string, ttl time.Duration) (string, error) {
	u, err := m.c.PresignedGetObject(m.ctx, b, k, ttl, url.Values{})
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

func (m *minioClient) PresignPut(b, k string, ttl time.Duration) (string, http.Header, error) {
	u, err := m.c.PresignedPutObject(m.ctx, b, k, ttl)
	if err != nil {
		return "", nil, err
	}
	return u.String(), nil, nil
}
