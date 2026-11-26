package main

// FB-02 (M-A3) ingest CPU evidence. Each benchmark reports the process CPU
// (user+system, every thread) spent inside the server's handling of each PUT,
// so fsync waits and the client's own digests do not count. The file
// uses only APIs that predate FB-02, so it runs unchanged on the FB-01R head
// for the before/after comparison in testing-harnesses/foundational/FB-02.
//
//   go test -run '^$' -bench '^BenchmarkFB02' -benchtime 40x -count 8 .

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func fb02CPUTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func fb02BenchPut(b *testing.B, size int, unsigned bool, hdr func([]byte) (string, string)) {
	store, err := OpenStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	if err := store.CreateBucket("b"); err != nil {
		b.Fatal(err)
	}
	creds := Credentials{AccessKeyID: "AK", SecretAccessKey: "SK"}
	srv := NewServer(store, creds, "us-east-1")
	body := genRandomBytes(int64(size), size)
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	if unsigned {
		hash = "UNSIGNED-PAYLOAD"
	}
	var hk, hv string
	if hdr != nil {
		hk, hv = hdr(body)
	}
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	var cpu time.Duration
	for i := 0; i < b.N; i++ {
		// A fresh key and a changed first byte keep every PUT a new object
		// with new chunks, as an ordinary ingest is.
		binary.BigEndian.PutUint64(body, uint64(i))
		if !unsigned {
			sum = sha256.Sum256(body)
			hash = hex.EncodeToString(sum[:])
		}
		if hdr != nil {
			hk, hv = hdr(body)
		}
		req := httptest.NewRequest(http.MethodPut, "/b/k"+strconv.Itoa(i), bytes.NewReader(body))
		req.ContentLength = int64(size)
		if hk != "" {
			req.Header.Set(hk, hv)
		}
		if err := signSigV4Request(req, creds, "us-east-1", hash, time.Now()); err != nil {
			b.Fatal(err)
		}
		rec := httptest.NewRecorder()
		// Only the server's handling counts: the client-side digests and
		// signing above are not ingest cost.
		cpu0 := fb02CPUTime()
		srv.ServeHTTP(rec, req)
		cpu += fb02CPUTime() - cpu0
		if rec.Code != 200 {
			b.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
		}
	}
	b.ReportMetric(float64(cpu)/float64(b.N), "cpu-ns/op")
}

func fb02CRC32Header(body []byte) (string, string) {
	return "x-amz-checksum-crc32", base64.StdEncoding.EncodeToString(binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(body)))
}

func BenchmarkFB02PutNoChecksum8MiB(b *testing.B)         { fb02BenchPut(b, 8<<20, false, nil) }
func BenchmarkFB02PutNoChecksum64KiB(b *testing.B)        { fb02BenchPut(b, 64<<10, false, nil) }
func BenchmarkFB02PutUnsignedNoChecksum8MiB(b *testing.B) { fb02BenchPut(b, 8<<20, true, nil) }
func BenchmarkFB02PutCRC32_8MiB(b *testing.B)             { fb02BenchPut(b, 8<<20, false, fb02CRC32Header) }
