// z2_aws_chunked checks aws-chunked streaming SigV4 ingestion against an
// independent client: minio-go v7, which signs plain-HTTP PutObject and
// UploadPart bodies as STREAMING-AWS4-HMAC-SHA256-PAYLOAD. A recording
// proxy between client and server proves the streaming mode was really on
// the wire, and lets one run flip a byte in flight to show that a body
// whose chunk signatures no longer verify never publishes an object.
//
// Usage:
//
//	ZEROS3_BIN=/path/to/zeros3 go run ./harness/z2_aws_chunked
package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const (
	accessKeyID     = "AKIAZEROS3EXAMPLE01"
	secretAccessKey = "zeros3exampleSecretKeyForM1TestingOnly01"
	region          = "us-east-1"
	streamingMode   = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
)

var failed bool

func check(name string, ok bool, detail string, args ...any) {
	if ok {
		fmt.Printf("PASS: %s\n", name)
		return
	}
	failed = true
	fmt.Printf("FAIL: %s: %s\n", name, fmt.Sprintf(detail, args...))
}

// tamperProxy forwards to the server, recording each PUT's payload mode
// and flipping one body byte for requests that carry the tamper marker.
type tamperProxy struct {
	mu    sync.Mutex
	modes map[string]string
}

func (p *tamperProxy) record(r *http.Request) {
	if r.Method != http.MethodPut {
		return
	}
	p.mu.Lock()
	p.modes[r.URL.Path+"?"+r.URL.RawQuery] = r.Header.Get("X-Amz-Content-Sha256")
	p.mu.Unlock()
}

func (p *tamperProxy) mode(prefix string) (string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, mode := 0, ""
	for k, v := range p.modes {
		if strings.HasPrefix(k, prefix) {
			n++
			mode = v
		}
	}
	return mode, n
}

func flipAt(body io.ReadCloser, off int64) io.ReadCloser {
	return struct {
		io.Reader
		io.Closer
	}{&flipReader{r: body, off: off}, body}
}

type flipReader struct {
	r   io.Reader
	off int64
	pos int64
}

func (f *flipReader) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if f.off >= f.pos && f.off < f.pos+int64(n) {
		p[f.off-f.pos] ^= 0x01
	}
	f.pos += int64(n)
	return n, err
}

func main() {
	bin := os.Getenv("ZEROS3_BIN")
	if bin == "" {
		fmt.Println("FAIL: ZEROS3_BIN is required")
		os.Exit(2)
	}
	storeDir, err := os.MkdirTemp("", "zeros3-z2-chunked-")
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(storeDir)

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	srv := exec.Command(bin, "serve", "-store", storeDir, "-addr", addr)
	srv.Stdout, srv.Stderr = os.Stdout, os.Stderr
	if err := srv.Start(); err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	defer srv.Process.Kill()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		} else if time.Now().After(deadline) {
			fmt.Println("FAIL: zeros3 did not start")
			os.Exit(2)
		}
	}

	target, _ := url.Parse("http://" + addr)
	rec := &tamperProxy{modes: map[string]string{}}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.Director = func(r *http.Request) {
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		rec.record(r)
		if r.Header.Get("X-Amz-Meta-Tamper") != "" && r.Body != nil {
			r.Body = flipAt(r.Body, 100000)
		}
	}
	proxy := httptest.NewServer(rp)
	defer proxy.Close()

	ctx := context.Background()
	cli, err := minio.New(strings.TrimPrefix(proxy.URL, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4(accessKeyID, secretAccessKey, ""), Region: region,
	})
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(2)
	}
	check("MakeBucket", cli.MakeBucket(ctx, "z2chunk", minio.MakeBucketOptions{Region: region}) == nil, "")

	rnd := rand.New(rand.NewSource(7))
	random := func(n int) []byte { b := make([]byte, n); rnd.Read(b); return b }
	cases := []struct {
		name string
		body []byte
		opts minio.PutObjectOptions
	}{
		{"empty", nil, minio.PutObjectOptions{}},
		{"tiny", []byte("hello aws-chunked"), minio.PutObjectOptions{ContentType: "text/plain"}},
		{"one-chunk-boundary", random(64 << 10), minio.PutObjectOptions{}},
		{"multi-chunk", random(3<<20 + 12345), minio.PutObjectOptions{UserMetadata: map[string]string{"origin": "minio-go"}}},
		{"large-single-put", random(40 << 20), minio.PutObjectOptions{DisableMultipart: true}},
		{"multipart", random(17<<20 + 5), minio.PutObjectOptions{PartSize: 5 << 20}},
	}
	for _, c := range cases {
		_, err := cli.PutObject(ctx, "z2chunk", c.name, bytes.NewReader(c.body), int64(len(c.body)), c.opts)
		check("PutObject "+c.name, err == nil, "%v", err)
		mode, n := rec.mode("/z2chunk/" + c.name)
		check("wire mode for "+c.name+" is aws-chunked SigV4", n > 0 && mode == streamingMode,
			"%d PUT request(s), x-amz-content-sha256=%q", n, mode)

		obj, err := cli.GetObject(ctx, "z2chunk", c.name, minio.GetObjectOptions{})
		var got []byte
		if err == nil {
			got, err = io.ReadAll(obj)
			obj.Close()
		}
		check("GetObject "+c.name+" byte-exact", err == nil && bytes.Equal(got, c.body), "%v (%d of %d bytes)", err, len(got), len(c.body))
		st, err := cli.StatObject(ctx, "z2chunk", c.name, minio.StatObjectOptions{})
		if c.name != "multipart" && err == nil {
			sum := md5.Sum(c.body)
			check("ETag for "+c.name+" is the MD5 of the decoded body", st.ETag == hex.EncodeToString(sum[:]), "etag %q", st.ETag)
		}
	}

	tampered := random(2 << 20)
	_, err = cli.PutObject(ctx, "z2chunk", "tampered", bytes.NewReader(tampered), int64(len(tampered)),
		minio.PutObjectOptions{UserMetadata: map[string]string{"tamper": "1"}, DisableMultipart: true})
	var resp minio.ErrorResponse
	if err != nil {
		resp = minio.ToErrorResponse(err)
	}
	check("in-flight byte flip is rejected", err != nil && resp.Code == "SignatureDoesNotMatch", "err=%v code=%q", err, resp.Code)
	_, err = cli.StatObject(ctx, "z2chunk", "tampered", minio.StatObjectOptions{})
	check("tampered upload never published an object", minio.ToErrorResponse(err).Code == "NoSuchKey", "stat err=%v", err)

	srv.Process.Signal(os.Interrupt)
	_ = srv.Wait()
	out, err := exec.Command(bin, "verify", "-deep", "-store", storeDir).CombinedOutput()
	check("zeros3 verify -deep after the uploads", err == nil, "%v\n%s", err, out)

	if failed {
		os.Exit(1)
	}
}
