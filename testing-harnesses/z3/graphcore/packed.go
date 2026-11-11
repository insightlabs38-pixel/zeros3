package graphcore

import (
	"compress/flate"
	"io"
	"sync"
)

var flatePool = sync.Pool{New: func() any { w, _ := flate.NewWriter(io.Discard, flate.DefaultCompression); return w }}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// packedLen is the deflate-based pack-size estimate used to choose between raw and
// delta-coded columns (a pure function of the bytes; the writer pool is only an allocation cache).
func packedLen(b []byte) int64 {
	if len(b) == 0 {
		return 0
	}
	var cw countWriter
	fw := flatePool.Get().(*flate.Writer)
	fw.Reset(&cw)
	fw.Write(b)
	fw.Close()
	flatePool.Put(fw)
	if cw.n <= int64(len(b)-max(1, len(b)/16)) {
		return cw.n
	}
	return int64(len(b))
}
