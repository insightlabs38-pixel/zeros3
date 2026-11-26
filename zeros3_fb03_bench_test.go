package main

// FB-03 ingest benchmarks (M-A2 failure criterion: no throughput regression
// above 5%). Only APIs that predate FB-03 are used, so the file also runs on
// the FB-02 head for the interleaved before/after comparison in
// testing-harnesses/foundational/FB-03. Each PUT goes through the server
// handler; cpu-ns/op is the process CPU spent inside it.
//
//   go test -run '^$' -bench '^BenchmarkFB03Ingest' -benchtime 60x -count 1 .

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// fb03BenchPut PUTs size-byte objects. New content changes the first bytes
// of every body (new chunks); dedup re-puts one stored body under new keys.
func fb03BenchPut(b *testing.B, size int, dedup bool) {
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
	body := genRandomBytes(int64(size)+3, size)
	put := func(key string) {
		sum := sha256.Sum256(body)
		req := httptest.NewRequest(http.MethodPut, "/b/"+key, bytes.NewReader(body))
		req.ContentLength = int64(size)
		if err := signSigV4Request(req, creds, "us-east-1", hex.EncodeToString(sum[:]), time.Now()); err != nil {
			b.Fatal(err)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != 200 {
			b.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
		}
	}
	if dedup {
		put("seed")
	}
	b.SetBytes(int64(size))
	b.ReportAllocs()
	b.ResetTimer()
	var cpu time.Duration
	for i := 0; i < b.N; i++ {
		if !dedup {
			binary.BigEndian.PutUint64(body, uint64(i)+1)
		}
		cpu0 := fb02CPUTime()
		put("k" + strconv.Itoa(i))
		cpu += fb02CPUTime() - cpu0
	}
	b.ReportMetric(float64(cpu)/float64(b.N), "cpu-ns/op")
}

func BenchmarkFB03IngestNew16KiB(b *testing.B)   { fb03BenchPut(b, 16<<10, false) }
func BenchmarkFB03IngestNew200KB(b *testing.B)   { fb03BenchPut(b, 200_000, false) }
func BenchmarkFB03IngestNew8MiB(b *testing.B)    { fb03BenchPut(b, 8<<20, false) }
func BenchmarkFB03IngestDedup200KB(b *testing.B) { fb03BenchPut(b, 200_000, true) }
func BenchmarkFB03IngestDedup8MiB(b *testing.B)  { fb03BenchPut(b, 8<<20, true) }
