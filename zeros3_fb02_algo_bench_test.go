package main

// FB-02 (M-A3) per-algorithm evidence: checksum throughput and the PUT CPU
// cost of each newly supported algorithm. FB-02 only; the no-checksum
// before/after comparison lives in zeros3_fb02_bench_test.go.
//
//   go test -run '^$' -bench '^BenchmarkFB02(Algo|PutAlgo)' -count 5 .

import (
	"encoding/base64"
	"testing"
)

func fb02BenchAlgo(b *testing.B, name string) {
	algo, ok := checksumAlgoNamed(name)
	if !ok {
		b.Fatal(name)
	}
	data := genRandomBytes(1, 1<<20)
	h := algo.newHash()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Reset()
		h.Write(data)
		h.Sum(nil)
	}
}

func BenchmarkFB02AlgoCRC32(b *testing.B)     { fb02BenchAlgo(b, "crc32") }
func BenchmarkFB02AlgoCRC32C(b *testing.B)    { fb02BenchAlgo(b, "crc32c") }
func BenchmarkFB02AlgoCRC64NVME(b *testing.B) { fb02BenchAlgo(b, "crc64nvme") }
func BenchmarkFB02AlgoSHA1(b *testing.B)      { fb02BenchAlgo(b, "sha1") }
func BenchmarkFB02AlgoSHA256(b *testing.B)    { fb02BenchAlgo(b, "sha256") }

func fb02AlgoHeader(name string) func([]byte) (string, string) {
	return func(body []byte) (string, string) {
		return "x-amz-checksum-" + name, base64.StdEncoding.EncodeToString(fb02Sum(name, body))
	}
}

func BenchmarkFB02PutAlgoCRC32C_8MiB(b *testing.B) {
	fb02BenchPut(b, 8<<20, false, fb02AlgoHeader("crc32c"))
}
func BenchmarkFB02PutAlgoCRC64NVME_8MiB(b *testing.B) {
	fb02BenchPut(b, 8<<20, false, fb02AlgoHeader("crc64nvme"))
}
func BenchmarkFB02PutAlgoSHA1_8MiB(b *testing.B) {
	fb02BenchPut(b, 8<<20, false, fb02AlgoHeader("sha1"))
}
func BenchmarkFB02PutAlgoSHA256_8MiB(b *testing.B) {
	fb02BenchPut(b, 8<<20, false, fb02AlgoHeader("sha256"))
}
