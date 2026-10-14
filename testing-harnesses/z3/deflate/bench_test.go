package deflate

import (
	"bytes"
	"compress/flate"
	"io"
	"runtime"
	"testing"

	"zeros3-z3/corpus"
)

func benchStream(b *testing.B, level int) ([]byte, []byte) {
	plain := corpus.Text(corpus.NewRNG(1, "bench"), 16<<20)
	return plain, goDeflate(plain, level)
}

func BenchmarkParse(b *testing.B) {
	for _, lvl := range []int{1, 6, 9} {
		plain, comp := benchStream(b, lvl)
		b.Run("L"+string(rune('0'+lvl)), func(b *testing.B) {
			b.SetBytes(int64(len(plain)))
			b.ReportMetric(float64(len(comp))/1e6, "compMB")
			for i := 0; i < b.N; i++ {
				if _, err := Parse(comp, Options{MaxOutput: 64 << 20}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkReplay(b *testing.B) {
	for _, lvl := range []int{1, 6, 9} {
		plain, comp := benchStream(b, lvl)
		s, err := Parse(comp, Options{MaxOutput: 64 << 20})
		if err != nil {
			b.Fatal(err)
		}
		b.Run("L"+string(rune('0'+lvl)), func(b *testing.B) {
			b.SetBytes(int64(len(plain)))
			for i := 0; i < b.N; i++ {
				if err := s.Replay(io.Discard); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkStdlibInflate is the reference decoder speed on the same input.
func BenchmarkStdlibInflate(b *testing.B) {
	plain, comp := benchStream(b, 6)
	b.SetBytes(int64(len(plain)))
	for i := 0; i < b.N; i++ {
		io.Copy(io.Discard, flate.NewReader(bytes.NewReader(comp)))
	}
}

// BenchmarkParseLiveHeap reports the heap retained by a parsed stream
// (plaintext + token IR) relative to the input sizes.
func BenchmarkParseLiveHeap(b *testing.B) {
	plain, comp := benchStream(b, 6)
	for i := 0; i < b.N; i++ {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		s, err := Parse(comp, Options{MaxOutput: 64 << 20})
		if err != nil {
			b.Fatal(err)
		}
		runtime.GC()
		runtime.ReadMemStats(&m1)
		b.ReportMetric(float64(m1.HeapAlloc-m0.HeapAlloc)/(1<<20), "liveMiB")
		b.ReportMetric(float64(len(plain))/(1<<20), "plainMiB")
		b.ReportMetric(float64(len(comp))/(1<<20), "compMiB")
		b.ReportMetric(float64(s.Stats().Tokens)/1e6, "Mtokens")
	}
}
