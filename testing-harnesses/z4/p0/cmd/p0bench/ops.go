package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Obj struct {
	Bucket, Key string
	C           Content
	SHA         string
	Data        []byte // pregenerated body (nil = stream)
}

type S3 struct {
	c    *http.Client
	base string
}

func (s *S3) do(req *http.Request) (*http.Response, error) { return s.c.Do(req) }

func (s *S3) CreateBucket(b string) error {
	req, _ := http.NewRequest("PUT", s.base+"/"+b, nil)
	sign(req, "UNSIGNED-PAYLOAD")
	r, err := s.do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != 200 && r.StatusCode != 409 {
		return fmt.Errorf("create bucket: %s", r.Status)
	}
	return nil
}

type PutOpt struct {
	Mode     string // single | multipart | chunked
	Payload  string // unsigned | signed
	PartSize int64
	PartConc int
}

func (o *Obj) body() io.Reader {
	if o.Data != nil {
		return bytes.NewReader(o.Data)
	}
	return o.C.NewReader()
}

func (s *S3) Put(o *Obj, opt PutOpt) error {
	switch opt.Mode {
	case "multipart":
		return s.putMultipart(o, opt)
	case "chunked":
		return s.putChunked(o)
	}
	req, _ := http.NewRequest("PUT", s.base+"/"+o.Bucket+"/"+o.Key, o.body())
	req.ContentLength = o.C.Size
	payload := "UNSIGNED-PAYLOAD"
	if opt.Payload == "signed" {
		payload = o.SHA
	}
	sign(req, payload)
	return s.finish(req)
}

func (s *S3) finish(req *http.Request) error {
	r, err := s.do(req)
	if err != nil {
		return err
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s %s", req.Method, req.URL.Path, r.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// aws-chunked with STREAMING-AWS4-HMAC-SHA256-PAYLOAD (per-chunk signatures), the SDK streaming mode.
type chunkedReader struct {
	src      io.Reader
	buf      []byte
	cur      []byte
	done     bool
	si       sigInfo
	prev     string
	emptySHA string
}

func (c *chunkedReader) frame(data []byte) []byte {
	ds := sha256.Sum256(data)
	sts := "AWS4-HMAC-SHA256-PAYLOAD\n" + c.si.amz + "\n" + c.si.scope + "\n" + c.prev + "\n" + c.emptySHA + "\n" + hex.EncodeToString(ds[:])
	sig := hex.EncodeToString(hm(c.si.key, sts))
	c.prev = sig
	out := []byte(fmt.Sprintf("%x;chunk-signature=%s\r\n", len(data), sig))
	out = append(out, data...)
	return append(out, '\r', '\n')
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	for len(c.cur) == 0 {
		if c.done {
			return 0, io.EOF
		}
		n, err := io.ReadFull(c.src, c.buf)
		if n > 0 {
			c.cur = c.frame(c.buf[:n])
		}
		if err != nil {
			c.done = true
			c.cur = append(c.cur, c.frame(nil)...)
		}
	}
	n := copy(p, c.cur)
	c.cur = c.cur[n:]
	return n, nil
}

const chunkData = 64 << 10

func encodedLen(size int64) int64 {
	hdr := func(n int64) int64 {
		return int64(len(fmt.Sprintf("%x", n))) + int64(len(";chunk-signature=")) + 64 + 2 + n + 2
	}
	full, rem := size/chunkData, size%chunkData
	t := full * hdr(chunkData)
	if rem > 0 {
		t += hdr(rem)
	}
	return t + hdr(0)
}

func (s *S3) putChunked(o *Obj) error {
	req, _ := http.NewRequest("PUT", s.base+"/"+o.Bucket+"/"+o.Key, nil)
	req.Header.Set("Content-Encoding", "aws-chunked")
	req.Header.Set("x-amz-decoded-content-length", fmt.Sprint(o.C.Size))
	si := signFull(req, "STREAMING-AWS4-HMAC-SHA256-PAYLOAD", "content-encoding", "x-amz-decoded-content-length")
	e := sha256.Sum256(nil)
	req.Body = io.NopCloser(&chunkedReader{src: o.body(), buf: make([]byte, chunkData), si: si, prev: si.seed, emptySHA: hex.EncodeToString(e[:])})
	req.ContentLength = encodedLen(o.C.Size)
	return s.finish(req)
}

func (s *S3) putMultipart(o *Obj, opt PutOpt) error {
	u := s.base + "/" + o.Bucket + "/" + o.Key
	req, _ := http.NewRequest("POST", u+"?uploads", nil)
	sign(req, "UNSIGNED-PAYLOAD")
	r, err := s.do(req)
	if err != nil {
		return err
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 {
		return fmt.Errorf("initiate: %s %s", r.Status, b)
	}
	id := between(string(b), "<UploadId>", "</UploadId>")
	ps := opt.PartSize
	if ps < 5<<20 {
		ps = 5 << 20
	}
	nparts := int((o.C.Size + ps - 1) / ps)
	etags := make([]string, nparts)
	errs := make(chan error, nparts)
	sem := make(chan struct{}, max(1, opt.PartConc))
	var wg sync.WaitGroup
	for i := 0; i < nparts; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			off := int64(i) * ps
			n := min(ps, o.C.Size-off)
			var body io.Reader
			if o.Data != nil {
				body = bytes.NewReader(o.Data[off : off+n])
			} else {
				body = io.NewSectionReader(&o.C, off, n)
			}
			rq, _ := http.NewRequest("PUT", fmt.Sprintf("%s?partNumber=%d&uploadId=%s", u, i+1, id), body)
			rq.ContentLength = n
			sign(rq, "UNSIGNED-PAYLOAD")
			rs, err := s.do(rq)
			if err != nil {
				errs <- err
				return
			}
			io.Copy(io.Discard, rs.Body)
			rs.Body.Close()
			if rs.StatusCode != 200 {
				errs <- fmt.Errorf("part %d: %s", i+1, rs.Status)
				return
			}
			etags[i] = rs.Header.Get("ETag")
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		return e
	}
	var x strings.Builder
	x.WriteString("<CompleteMultipartUpload>")
	for i, e := range etags {
		fmt.Fprintf(&x, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", i+1, e)
	}
	x.WriteString("</CompleteMultipartUpload>")
	body := []byte(x.String())
	rq, _ := http.NewRequest("POST", u+"?uploadId="+id, bytes.NewReader(body))
	rq.ContentLength = int64(len(body))
	sum := sha256.Sum256(body)
	sign(rq, hex.EncodeToString(sum[:]))
	return s.finish(rq)
}

func between(s, a, b string) string {
	i := strings.Index(s, a)
	if i < 0 {
		return ""
	}
	s = s[i+len(a):]
	j := strings.Index(s, b)
	if j < 0 {
		return ""
	}
	return s[:j]
}

type GetRes struct {
	Lat, TTFB float64
	N         int64
	Err       error
	OK        bool
}

// Get fetches [off, off+n) (n<0 = whole object) and verifies against content.
func (s *S3) Get(o *Obj, off, n int64) GetRes {
	req, _ := http.NewRequest("GET", s.base+"/"+o.Bucket+"/"+o.Key, nil)
	whole := n < 0
	if !whole {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+n-1))
	}
	sign(req, "UNSIGNED-PAYLOAD")
	t0 := time.Now()
	r, err := s.do(req)
	if err != nil {
		return GetRes{Err: err}
	}
	defer r.Body.Close()
	if r.StatusCode/100 != 2 {
		b, _ := io.ReadAll(r.Body)
		return GetRes{Err: fmt.Errorf("GET %s: %s %s", o.Key, r.Status, b)}
	}
	var res GetRes
	buf := make([]byte, 256<<10)
	h := sha256.New()
	var rangeBuf []byte
	if !whole {
		rangeBuf = make([]byte, 0, n)
	}
	first := true
	for {
		m, err := r.Body.Read(buf)
		if m > 0 && first {
			res.TTFB = time.Since(t0).Seconds()
			first = false
		}
		if m > 0 {
			res.N += int64(m)
			if whole {
				h.Write(buf[:m])
			} else {
				rangeBuf = append(rangeBuf, buf[:m]...)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			res.Err = err
			return res
		}
	}
	res.Lat = time.Since(t0).Seconds()
	if whole {
		res.OK = res.N == o.C.Size && hex.EncodeToString(h.Sum(nil)) == o.SHA
	} else {
		want := make([]byte, n)
		o.C.ReadAt(want, off)
		res.OK = bytes.Equal(want, rangeBuf)
	}
	return res
}

// pool runs fn(i) for i in [0,n) over conc goroutines; first error aborts.
func pool(n, conc int, fn func(i int) error) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	next := 0
	for w := 0; w < conc; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				mu.Lock()
				i := next
				next++
				bad := first != nil
				mu.Unlock()
				if i >= n || bad {
					return
				}
				if err := fn(i); err != nil {
					mu.Lock()
					if first == nil {
						first = err
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	return first
}

// VerifyAll reads every object back (untimed, fresh process) and checks bytes.
func VerifyAll(s *S3, objs []*Obj, conc int) (int, error) {
	bad := 0
	var mu sync.Mutex
	err := pool(len(objs), conc, func(i int) error {
		r := s.Get(objs[i], 0, -1)
		if r.Err != nil {
			return r.Err
		}
		if !r.OK {
			mu.Lock()
			bad++
			mu.Unlock()
		}
		return nil
	})
	return bad, err
}
