// Package core is the reusable black-box conformance suite for the ZeroS3
// Core Client Profile v1 (see ../../../S3_COMPAT.md).
//
// It targets a running endpoint, not a fixture: every scenario talks to the
// server only through a Client (AWS SDK for Go v2 or minio-go) and a plain
// http.Client for presigned URLs, and it knows nothing about ZeroS3's packs,
// tiers, compression or locator. A native client in another language can use
// the scenario list below as its acceptance checklist.
package core

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Profile names the contract version the suite checks.
const Profile = "zeros3-core-client-v1"

// Config addresses one endpoint. Region is the endpoint's configured SigV4
// region; Prefix keeps bucket names unique per run.
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Region    string
	Prefix    string
	Verbose   bool
}

// Obj is the client-neutral view of a HEAD/GET response.
type Obj struct {
	Body         []byte
	ETag         string // unquoted
	ContentType  string
	Meta         map[string]string // lower-case keys
	Size         int64
	ContentRange string
}

type PutOpts struct {
	ContentType string
	Meta        map[string]string
	ContentMD5  string // base64; forces a Content-MD5 header
	CRC32       string // base64; forces x-amz-checksum-crc32
	IfNoneMatch string
	IfMatch     string
}

type GetOpts struct {
	Range       string
	IfNoneMatch string
	IfMatch     string
}

type ListOpts struct {
	Prefix, Delimiter, Token string
	Max                      int
}

type ListPage struct {
	Keys, Prefixes []string
	Truncated      bool
	Next           string
}

type Part struct {
	N    int
	ETag string // unquoted
	Size int64
}

type Upload struct{ Key, ID string }

// Client is the operation set of the Core Client Profile. Implementations
// return *S3Error for any HTTP-level S3 failure.
type Client interface {
	Name() string
	Supports(feature string) bool // "crc32", "presign"
	ListBuckets() ([]string, error)
	CreateBucket(b string) error
	HeadBucket(b string) error
	DeleteBucket(b string) error
	Location(b string) (string, error)
	Put(b, k string, body []byte, o PutOpts) (etag string, err error)
	Head(b, k string) (Obj, error)
	Get(b, k string, o GetOpts) (Obj, error)
	Delete(b, k string) error
	DeleteMany(b string, keys []string, quiet bool) (deleted []string, failed map[string]string, err error)
	List(b string, o ListOpts) (ListPage, error)
	Copy(dstB, dstK, srcB, srcK string, replace *PutOpts) (etag string, err error)
	MPCreate(b, k string, o PutOpts) (id string, err error)
	MPPart(b, k, id string, n int, body []byte) (etag string, err error)
	MPParts(b, k, id string) ([]Part, error)
	MPUploads(b string) ([]Upload, error)
	MPComplete(b, k, id string, parts []Part) (etag string, err error)
	MPAbort(b, k, id string) error
	PresignGet(b, k string, ttl time.Duration) (string, error)
	PresignPut(b, k string, ttl time.Duration) (url string, header http.Header, err error)
}

// S3Error is an HTTP-level S3 failure, normalized across client libraries.
type S3Error struct {
	Status int
	Code   string
	Err    error
}

func (e *S3Error) Error() string { return fmt.Sprintf("S3 %d %s: %v", e.Status, e.Code, e.Err) }
func (e *S3Error) Unwrap() error { return e.Err }

func statusOf(err error) int {
	var se *S3Error
	if errors.As(err, &se) {
		return se.Status
	}
	return 0
}

func codeOf(err error) string {
	var se *S3Error
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

// Failure names the operation and contract clause that was violated.
type Failure struct {
	Scenario string `json:"scenario"`
	Contract string `json:"contract"`
	Detail   string `json:"detail"`
}

// Report is the machine-readable summary of one client's run.
type Report struct {
	Profile  string    `json:"profile"`
	Client   string    `json:"client"`
	Endpoint string    `json:"endpoint"`
	Passed   int       `json:"passed"`
	Failed   int       `json:"failed"`
	Skipped  int       `json:"skipped"`
	Failures []Failure `json:"failures,omitempty"`
	Cases    []string  `json:"cases,omitempty"` // verbose only
}

// Scenario is one portable conformance scenario.
type Scenario struct {
	Name string
	Run  func(t *T)
}

// T is the per-scenario context.
type T struct {
	C        Client
	Cfg      Config
	HTTP     *http.Client
	scenario string
	rep      *Report
}

func (t *T) ok(contract string) {
	t.rep.Passed++
	if t.Cfg.Verbose {
		t.rep.Cases = append(t.rep.Cases, "PASS "+t.scenario+": "+contract)
	}
}

func (t *T) fail(contract, format string, args ...any) {
	t.rep.Failed++
	t.rep.Failures = append(t.rep.Failures, Failure{t.scenario, contract, fmt.Sprintf(format, args...)})
	if t.Cfg.Verbose {
		t.rep.Cases = append(t.rep.Cases, "FAIL "+t.scenario+": "+contract)
	}
}

func (t *T) skip(contract string) {
	t.rep.Skipped++
	if t.Cfg.Verbose {
		t.rep.Cases = append(t.rep.Cases, "SKIP "+t.scenario+": "+contract)
	}
}

// Check records one contract clause.
func (t *T) Check(contract string, cond bool, format string, args ...any) bool {
	if cond {
		t.ok(contract)
	} else {
		t.fail(contract, format, args...)
	}
	return cond
}

// NoErr records an operation that must succeed.
func (t *T) NoErr(contract string, err error) bool {
	return t.Check(contract, err == nil, "%v", err)
}

// Status records an operation that must fail with the given HTTP status.
func (t *T) Status(contract string, err error, status int) bool {
	return t.Check(contract, statusOf(err) == status, "want HTTP %d, got %v", status, err)
}

// Bucket creates a uniquely named bucket for a scenario.
func (t *T) Bucket(tag string) string {
	b := fmt.Sprintf("%s-%s-%s", t.Cfg.Prefix, tag, randHex(3))
	if !t.NoErr("CreateBucket "+tag, t.C.CreateBucket(b)) {
		panic(skipScenario{})
	}
	return b
}

type skipScenario struct{}

// Data returns deterministic pseudo-random bytes (incompressible).
func Data(seed byte, n int) []byte {
	b := make([]byte, n)
	x := uint32(seed)*2654435761 + 1
	for i := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[i] = byte(x)
	}
	return b
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func md5Hex(b []byte) string { s := md5.Sum(b); return hex.EncodeToString(s[:]) }
func md5B64(b []byte) string { s := md5.Sum(b); return base64.StdEncoding.EncodeToString(s[:]) }

// Scenarios is the ordered portable scenario list.
var Scenarios = []Scenario{
	{"bucket", scBucket}, {"object", scObject}, {"range", scRange}, {"conditional", scConditional},
	{"integrity", scIntegrity}, {"copy", scCopy}, {"list", scList}, {"batch-delete", scBatchDelete},
	{"multipart", scMultipart}, {"presign", scPresign},
}

// Run executes the named scenarios (all when names is empty) with c.
func Run(c Client, cfg Config, names ...string) Report {
	if cfg.Prefix == "" {
		cfg.Prefix = "core-" + randHex(2)
	}
	rep := Report{Profile: Profile, Client: c.Name(), Endpoint: cfg.Endpoint}
	t := &T{C: c, Cfg: cfg, HTTP: &http.Client{Timeout: 60 * time.Second}, rep: &rep}
	for _, sc := range Scenarios {
		if len(names) > 0 && !contains(names, sc.Name) {
			continue
		}
		t.scenario = sc.Name
		func() {
			defer func() {
				switch r := recover().(type) {
				case nil, skipScenario:
				default:
					t.fail("scenario panicked", "%v", r)
				}
			}()
			sc.Run(t)
		}()
	}
	return rep
}

func contains(l []string, s string) bool {
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func put(t *T, b, k string, body []byte, o PutOpts) string {
	etag, err := t.C.Put(b, k, body, o)
	if err != nil {
		t.fail("PutObject "+k, "%v", err)
		panic(skipScenario{})
	}
	return etag
}

func scBucket(t *T) {
	c := t.C
	b := fmt.Sprintf("%s-bkt-%s", t.Cfg.Prefix, randHex(3))
	before, err := c.ListBuckets()
	t.NoErr("ListBuckets", err)
	t.Check("ListBuckets omits an absent bucket", !contains(before, b), "present: %v", before)
	t.NoErr("CreateBucket", c.CreateBucket(b))
	after, _ := c.ListBuckets()
	t.Check("ListBuckets lists the new bucket", contains(after, b), "got %v", after)
	t.NoErr("HeadBucket existing", c.HeadBucket(b))
	t.Status("HeadBucket missing is 404", c.HeadBucket(b+"-missing"), 404)
	loc, err := c.Location(b)
	want := t.Cfg.Region
	t.Check("GetBucketLocation returns the endpoint's region", err == nil && loc == want, "got %q err %v want %q", loc, err, want)
	_, err = c.Location(b + "-missing")
	t.Status("GetBucketLocation missing bucket is 404", err, 404)
	put(t, b, "k", []byte("x"), PutOpts{})
	t.Status("DeleteBucket non-empty is 409", c.DeleteBucket(b), 409)
	t.NoErr("DeleteObject", c.Delete(b, "k"))
	t.NoErr("DeleteBucket empty", c.DeleteBucket(b))
	t.Status("HeadBucket after delete is 404", c.HeadBucket(b), 404)
	t.Status("DeleteBucket missing is 404", c.DeleteBucket(b), 404)
}

func scObject(t *T) {
	c := t.C
	b := t.Bucket("obj")
	meta := map[string]string{"origin": "conformance", "n": "42"}
	body := Data(1, 700_000)
	etag := put(t, b, "dir/sub/file", body, PutOpts{ContentType: "application/x-test", Meta: meta})
	t.Check("PutObject ETag is the content MD5", etag == md5Hex(body), "etag %q want %q", etag, md5Hex(body))
	h, err := c.Head(b, "dir/sub/file")
	if t.NoErr("HeadObject", err) {
		t.Check("HeadObject size/etag/type", h.Size == int64(len(body)) && h.ETag == md5Hex(body) && h.ContentType == "application/x-test", "%+v", h)
		t.Check("HeadObject user metadata round-trips", h.Meta["origin"] == "conformance" && h.Meta["n"] == "42", "%v", h.Meta)
	}
	g, err := c.Get(b, "dir/sub/file", GetOpts{})
	t.Check("GetObject body is byte-exact", err == nil && bytes.Equal(g.Body, body), "err %v len %d", err, len(g.Body))
	t.Check("GetObject carries the metadata", g.Meta["origin"] == "conformance", "%v", g.Meta)

	etag0 := put(t, b, "empty", nil, PutOpts{})
	t.Check("empty object ETag", etag0 == md5Hex(nil), "%q", etag0)
	g, err = c.Get(b, "empty", GetOpts{})
	t.Check("empty object GET", err == nil && len(g.Body) == 0, "err %v", err)

	for i, k := range []string{"sp ace/pl+us/per%cent", "dou//ble/slash", "ünï/日本語.bin", "a&b<c>\"d'.txt", "enc%2Fslash", "trailing/"} {
		kb := Data(byte(10+i), 1000+i)
		put(t, b, k, kb, PutOpts{})
		g, err := c.Get(b, k, GetOpts{})
		t.Check(fmt.Sprintf("key %q round-trips", k), err == nil && bytes.Equal(g.Body, kb), "err %v", err)
	}

	body2 := Data(2, 4096)
	put(t, b, "dir/sub/file", body2, PutOpts{})
	g, _ = c.Get(b, "dir/sub/file", GetOpts{})
	t.Check("overwrite replaces the object", bytes.Equal(g.Body, body2), "len %d", len(g.Body))

	_, err = c.Get(b, "nope", GetOpts{})
	t.Check("GetObject missing is 404 NoSuchKey", statusOf(err) == 404 && codeOf(err) == "NoSuchKey", "%v", err)
	_, err = c.Head(b, "nope")
	t.Status("HeadObject missing is 404", err, 404)
	t.NoErr("DeleteObject existing", c.Delete(b, "dir/sub/file"))
	t.NoErr("DeleteObject missing is idempotent", c.Delete(b, "dir/sub/file"))
	_, err = c.Head(b, "dir/sub/file")
	t.Status("deleted object is gone", err, 404)
	_, err = c.Get("nobucket-"+randHex(3), "k", GetOpts{})
	t.Check("GetObject missing bucket is 404", statusOf(err) == 404, "%v", err)
}

func scRange(t *T) {
	c := t.C
	b := t.Bucket("rng")
	body := Data(3, 1_000_000)
	put(t, b, "r", body, PutOpts{})
	for _, tc := range []struct {
		rng    string
		lo, hi int
	}{
		{"bytes=0-9", 0, 9}, {"bytes=100000-200000", 100000, 200000}, {"bytes=999990-", 999990, 999999},
		{"bytes=-50", 999950, 999999}, {"bytes=0-0", 0, 0}, {"bytes=999999-9999999", 999999, 999999},
	} {
		g, err := c.Get(b, "r", GetOpts{Range: tc.rng})
		t.Check("range "+tc.rng, err == nil && bytes.Equal(g.Body, body[tc.lo:tc.hi+1]), "err %v len %d", err, len(g.Body))
		if err == nil {
			wantCR := fmt.Sprintf("bytes %d-%d/%d", tc.lo, tc.hi, len(body))
			t.Check("Content-Range for "+tc.rng, g.ContentRange == wantCR, "got %q want %q", g.ContentRange, wantCR)
		}
	}
	_, err := c.Get(b, "r", GetOpts{Range: "bytes=2000000-"})
	t.Status("unsatisfiable range is 416", err, 416)
}

func scConditional(t *T) {
	c := t.C
	b := t.Bucket("cond")
	body := []byte("conditional body")
	etag := put(t, b, "k", body, PutOpts{})
	_, err := c.Get(b, "k", GetOpts{IfNoneMatch: etag})
	t.Status("GET If-None-Match(current) is 304", err, 304)
	g, err := c.Get(b, "k", GetOpts{IfNoneMatch: "deadbeef"})
	t.Check("GET If-None-Match(other) is 200", err == nil && bytes.Equal(g.Body, body), "%v", err)
	g, err = c.Get(b, "k", GetOpts{IfMatch: etag})
	t.Check("GET If-Match(current) is 200", err == nil && bytes.Equal(g.Body, body), "%v", err)
	_, err = c.Get(b, "k", GetOpts{IfMatch: "deadbeef"})
	t.Status("GET If-Match(other) is 412", err, 412)

	_, err = c.Put(b, "k", []byte("second"), PutOpts{IfNoneMatch: "*"})
	t.Status("PUT If-None-Match:* on existing is 412", err, 412)
	_, err = c.Put(b, "fresh", []byte("first"), PutOpts{IfNoneMatch: "*"})
	t.NoErr("PUT If-None-Match:* on absent", err)
	_, err = c.Put(b, "k", []byte("stale"), PutOpts{IfMatch: "deadbeef"})
	t.Status("PUT If-Match(other) is 412", err, 412)
	_, err = c.Put(b, "k", []byte("v2"), PutOpts{IfMatch: etag})
	t.NoErr("PUT If-Match(current)", err)
	g, _ = c.Get(b, "k", GetOpts{})
	t.Check("conditional PUT applied only when satisfied", string(g.Body) == "v2", "%q", g.Body)
}

func scIntegrity(t *T) {
	c := t.C
	b := t.Bucket("integ")
	body := Data(4, 100_000)
	_, err := c.Put(b, "md5-ok", body, PutOpts{ContentMD5: md5B64(body)})
	t.NoErr("PUT with correct Content-MD5", err)
	_, err = c.Put(b, "md5-bad", body, PutOpts{ContentMD5: md5B64([]byte("other"))})
	t.Status("PUT with wrong Content-MD5 is 400", err, 400)
	_, err = c.Head(b, "md5-bad")
	t.Status("rejected PUT stores nothing", err, 404)
	if !c.Supports("crc32") {
		t.skip("CRC32 checksum")
		return
	}
	_, err = c.Put(b, "crc-ok", body, PutOpts{CRC32: crc32B64(body)})
	t.NoErr("PUT with correct CRC32", err)
	_, err = c.Put(b, "crc-bad", body, PutOpts{CRC32: crc32B64([]byte("other"))})
	t.Status("PUT with wrong CRC32 is 400", err, 400)
	_, err = c.Head(b, "crc-bad")
	t.Status("CRC32-rejected PUT stores nothing", err, 404)
}

func scCopy(t *T) {
	c := t.C
	b, b2 := t.Bucket("copy"), t.Bucket("copy2")
	body := Data(5, 300_000)
	put(t, b, "src key+1", body, PutOpts{ContentType: "text/x-src", Meta: map[string]string{"a": "1"}})
	etag, err := c.Copy(b, "dst", b, "src key+1", nil)
	t.Check("CopyObject ETag", err == nil && etag == md5Hex(body), "%q %v", etag, err)
	g, err := c.Get(b, "dst", GetOpts{})
	t.Check("CopyObject body", err == nil && bytes.Equal(g.Body, body), "%v", err)
	t.Check("CopyObject COPY directive keeps metadata", g.Meta["a"] == "1" && g.ContentType == "text/x-src", "%v %q", g.Meta, g.ContentType)
	_, err = c.Copy(b2, "x/dst", b, "src key+1", &PutOpts{ContentType: "text/x-new", Meta: map[string]string{"b": "2"}})
	t.NoErr("CopyObject across buckets with REPLACE", err)
	g, _ = c.Get(b2, "x/dst", GetOpts{})
	t.Check("CopyObject REPLACE metadata", bytes.Equal(g.Body, body) && g.Meta["b"] == "2" && g.Meta["a"] == "" && g.ContentType == "text/x-new", "%v %q", g.Meta, g.ContentType)
	_, err = c.Copy(b, "dst2", b, "missing", nil)
	t.Status("CopyObject missing source is 404", err, 404)
}

func scList(t *T) {
	c := t.C
	b := t.Bucket("list")
	var all []string
	for i := 0; i < 25; i++ {
		all = append(all, fmt.Sprintf("p/%02d", i))
	}
	for i := 0; i < 5; i++ {
		all = append(all, fmt.Sprintf("q/%d", i))
	}
	all = append(all, "root", "sp ace/x", "plus+/y", "ü/z")
	for _, k := range all {
		put(t, b, k, []byte(k), PutOpts{})
	}
	var got []string
	tok, pages := "", 0
	for {
		pg, err := c.List(b, ListOpts{Prefix: "p/", Max: 7, Token: tok})
		if !t.NoErr("ListObjectsV2 page", err) {
			break
		}
		pages++
		got = append(got, pg.Keys...)
		if !pg.Truncated {
			break
		}
		if pg.Next == "" {
			t.fail("ListObjectsV2 pagination", "truncated page without NextContinuationToken")
			break
		}
		tok = pg.Next
	}
	t.Check("pagination returns all 25 keys in order, no duplicates", pages == 4 && len(got) == 25 && sort.StringsAreSorted(got) && got[0] == "p/00" && got[24] == "p/24", "pages %d keys %v", pages, got)
	pg, err := c.List(b, ListOpts{Delimiter: "/"})
	t.Check("delimiter groups common prefixes", err == nil && strings.Join(pg.Prefixes, ",") == "p/,plus+/,q/,sp ace/,ü/" && strings.Join(pg.Keys, ",") == "root", "%+v %v", pg, err)
	pg, err = c.List(b, ListOpts{Prefix: "sp ace/"})
	t.Check("prefix with space", err == nil && len(pg.Keys) == 1 && pg.Keys[0] == "sp ace/x", "%+v %v", pg, err)
	pg, err = c.List(b, ListOpts{Prefix: "plus+/"})
	t.Check("prefix with plus", err == nil && len(pg.Keys) == 1 && pg.Keys[0] == "plus+/y", "%+v %v", pg, err)
	_, err = c.List(b+"-missing", ListOpts{})
	t.Status("list missing bucket is 404", err, 404)
}

func scBatchDelete(t *T) {
	c := t.C
	b := t.Bucket("del")
	keys := []string{"a", "b&c", "d<e>", "sp ace", "pl+us", "ü/日本", "dir/x", "dir/y", "keep1", "keep2"}
	for _, k := range keys {
		put(t, b, k, []byte("v-"+k), PutOpts{})
	}
	victims := []string{"a", "b&c", "d<e>", "sp ace", "pl+us", "ü/日本", "missing-key"}
	deleted, failed, err := c.DeleteMany(b, victims, false)
	t.NoErr("DeleteObjects", err)
	t.Check("DeleteObjects reports no per-key errors", len(failed) == 0, "%v", failed)
	if deleted != nil { // clients that always request Quiet report none
		sort.Strings(deleted)
		want := append([]string(nil), victims...)
		sort.Strings(want)
		t.Check("DeleteObjects lists Deleted keys, missing key included", strings.Join(deleted, "\x00") == strings.Join(want, "\x00"), "%q", deleted)
	}
	for _, k := range victims[:6] {
		_, err := c.Head(b, k)
		t.Status("batch-deleted "+k+" is gone", err, 404)
	}
	deleted, failed, err = c.DeleteMany(b, []string{"dir/x", "dir/y", "never"}, true)
	t.Check("DeleteObjects Quiet=true", err == nil && len(deleted) == 0 && len(failed) == 0, "%v %v %v", deleted, failed, err)
	pg, _ := c.List(b, ListOpts{})
	t.Check("unlisted keys survive", strings.Join(pg.Keys, ",") == "keep1,keep2", "%v", pg.Keys)
	_, _, err = c.DeleteMany(b+"-missing", []string{"x"}, false)
	t.Status("DeleteObjects missing bucket is 404", err, 404)
}

func scMultipart(t *T) {
	c := t.C
	b := t.Bucket("mp")
	p1, p2, p3 := Data(6, 5<<20), Data(7, 5<<20), Data(8, 777)
	id, err := c.MPCreate(b, "big/obj", PutOpts{ContentType: "application/x-mp", Meta: map[string]string{"k": "v"}})
	if !t.NoErr("CreateMultipartUpload", err) {
		return
	}
	var parts []Part
	for i, p := range [][]byte{p1, p2, p3} {
		etag, err := c.MPPart(b, "big/obj", id, i+1, p)
		if !t.NoErr(fmt.Sprintf("UploadPart %d", i+1), err) {
			return
		}
		t.Check(fmt.Sprintf("UploadPart %d ETag is part MD5", i+1), etag == md5Hex(p), "%q", etag)
		parts = append(parts, Part{N: i + 1, ETag: etag})
	}
	lp, err := c.MPParts(b, "big/obj", id)
	t.Check("ListParts", err == nil && len(lp) == 3 && lp[0].N == 1 && lp[2].Size == 777 && lp[1].ETag == parts[1].ETag, "%+v %v", lp, err)
	ups, err := c.MPUploads(b)
	found := false
	for _, u := range ups {
		found = found || u.ID == id && u.Key == "big/obj"
	}
	t.Check("ListMultipartUploads lists the open upload", err == nil && found, "%+v %v", ups, err)
	_, err = c.Head(b, "big/obj")
	t.Status("incomplete upload is invisible", err, 404)

	id2, _ := c.MPCreate(b, "aborted", PutOpts{})
	t.NoErr("AbortMultipartUpload", c.MPAbort(b, "aborted", id2))
	ups, _ = c.MPUploads(b)
	gone := true
	for _, u := range ups {
		gone = gone && u.ID != id2
	}
	t.Check("aborted upload no longer listed", gone, "%+v", ups)
	t.Status("UploadPart after abort is 404", func() error { _, e := c.MPPart(b, "aborted", id2, 1, p3); return e }(), 404)

	_, err = c.MPComplete(b, "big/obj", id, []Part{{N: 1, ETag: "00000000000000000000000000000000"}, parts[1]})
	t.Status("Complete with a wrong part ETag is 400", err, 400)
	etag, err := c.MPComplete(b, "big/obj", id, parts)
	t.Check("CompleteMultipartUpload ETag has -3 suffix", err == nil && strings.HasSuffix(etag, "-3"), "%q %v", etag, err)
	want := append(append(append([]byte{}, p1...), p2...), p3...)
	g, err := c.Get(b, "big/obj", GetOpts{})
	t.Check("completed object is byte-exact", err == nil && bytes.Equal(g.Body, want), "err %v len %d", err, len(g.Body))
	t.Check("completed object keeps create-time metadata", g.Meta["k"] == "v" && g.ContentType == "application/x-mp", "%v %q", g.Meta, g.ContentType)
	g, err = c.Get(b, "big/obj", GetOpts{Range: "bytes=5242870-5242889"})
	t.Check("range across a part boundary", err == nil && bytes.Equal(g.Body, want[5242870:5242890]), "%v", err)
}

func scPresign(t *T) {
	c := t.C
	if !c.Supports("presign") {
		t.skip("presign")
		return
	}
	b := t.Bucket("psg")
	body := Data(9, 50_000)
	put(t, b, "p k+1", body, PutOpts{})
	u, err := c.PresignGet(b, "p k+1", time.Minute)
	if t.NoErr("presign GET", err) {
		code, got := t.fetch("GET", u, nil, nil)
		t.Check("presigned GET returns the object", code == 200 && bytes.Equal(got, body), "status %d", code)
		if tampered := strings.Replace(u, "%2B1", "%2B2", 1); tampered != u {
			code, _ = t.fetch("GET", tampered, nil, nil)
			t.Check("presigned GET for another key is rejected", code == 403, "status %d", code)
		}
	}
	pu, hdr, err := c.PresignPut(b, "uploaded/by presign", time.Minute)
	if t.NoErr("presign PUT", err) {
		up := Data(10, 12_345)
		code, _ := t.fetch("PUT", pu, up, hdr)
		g, gerr := c.Get(b, "uploaded/by presign", GetOpts{})
		t.Check("presigned PUT stores the object", code == 200 && gerr == nil && bytes.Equal(g.Body, up), "status %d err %v", code, gerr)
	}
	short, err := c.PresignGet(b, "p k+1", time.Second)
	if t.NoErr("presign GET short expiry", err) {
		time.Sleep(2200 * time.Millisecond)
		code, _ := t.fetch("GET", short, nil, nil)
		t.Check("expired presigned URL is rejected", code == 403, "status %d", code)
	}
	code, _ := t.fetch("GET", t.Cfg.Endpoint+"/"+b+"/p%20k%2B1", nil, nil)
	t.Check("anonymous GET is rejected", code == 403, "status %d", code)
}

func (t *T) fetch(method, url string, body []byte, hdr http.Header) (int, []byte) {
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil
	}
	for k, v := range hdr {
		if !strings.EqualFold(k, "host") {
			req.Header[k] = v
		}
	}
	resp, err := t.HTTP.Do(req.WithContext(context.Background()))
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// NewT starts a standalone scenario context for harnesses that compose their
// own checks from the same reporting (see harness/z2_consumer).
func NewT(c Client, cfg Config, scenario string) *T {
	return &T{C: c, Cfg: cfg, HTTP: &http.Client{Timeout: 60 * time.Second}, scenario: scenario,
		rep: &Report{Profile: Profile, Client: c.Name(), Endpoint: cfg.Endpoint}}
}

// Report returns the results recorded so far.
func (t *T) Report() Report { return *t.rep }

// Fetch performs one plain (unsigned unless the URL is presigned) HTTP request.
func (t *T) Fetch(method, url string, body []byte, hdr http.Header) (int, []byte) {
	return t.fetch(method, url, body, hdr)
}

// MD5Hex is the S3 single-part ETag of body.
func MD5Hex(b []byte) string { return md5Hex(b) }
