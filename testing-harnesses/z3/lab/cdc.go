package lab

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// CDC v1 as implemented by zeros3.go (section 3). This is a verbatim copy of
// the production algorithm so that "current ZeroS3" in the laboratory means
// the real chunker. TestCDCMatchesProduction compiles zeros3.go itself and
// compares chunk boundaries and constants, so drift in either direction fails
// a test rather than silently skewing results.
const (
	cdcMinChunkSize    = 16 * 1024
	cdcTargetChunkSize = 64 * 1024
	cdcMaxChunkSize    = 256 * 1024
	cdcMaskS           = (1 << 16) - 1
	cdcMaskL           = (1 << 15) - 1
	cdcGearSeed        = "ZeroS3/CDCv1/GearTable"
)

var gearTable = buildGearTable()

func buildGearTable() [256]uint64 {
	var t [256]uint64
	for i := 0; i < 256; i++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", cdcGearSeed, i)))
		t[i] = binary.BigEndian.Uint64(h[:8])
	}
	return t
}

func findCDCBoundary(data []byte, eof bool) int {
	n := len(data)
	limit := n
	if limit > cdcMaxChunkSize {
		limit = cdcMaxChunkSize
	}
	var hash uint64
	for i := 0; i < limit; i++ {
		hash = (hash << 1) + gearTable[data[i]]
		size := i + 1
		if size >= cdcMaxChunkSize {
			return size
		}
		if size < cdcMinChunkSize {
			continue
		}
		mask := uint64(cdcMaskL)
		if size < cdcTargetChunkSize {
			mask = cdcMaskS
		}
		if hash&mask == 0 {
			return size
		}
	}
	return n
}

type cdcChunker struct {
	r          io.Reader
	buf        []byte
	start, end int
	eof        bool
}

var chunkBufs = sync.Pool{New: func() any { return make([]byte, 2*cdcMaxChunkSize) }}

func newCDCChunker(r io.Reader) *cdcChunker {
	return &cdcChunker{r: r, buf: chunkBufs.Get().([]byte)}
}

// release returns the read buffer; the chunker must not be used afterwards.
func (c *cdcChunker) release() {
	chunkBufs.Put(c.buf)
	c.buf = nil
}

func (c *cdcChunker) fill() error {
	if c.end-c.start >= cdcMaxChunkSize || c.eof {
		return nil
	}
	if len(c.buf)-c.end < cdcMaxChunkSize-(c.end-c.start) {
		c.end = copy(c.buf, c.buf[c.start:c.end])
		c.start = 0
	}
	for !c.eof && c.end-c.start < cdcMaxChunkSize {
		n, err := c.r.Read(c.buf[c.end:])
		c.end += n
		if err == io.EOF {
			c.eof = true
		} else if err != nil {
			return err
		}
	}
	return nil
}

// nextView returns the next chunk as a view valid until the following call.
func (c *cdcChunker) nextView() ([]byte, error) {
	if err := c.fill(); err != nil {
		return nil, err
	}
	if c.end == c.start {
		return nil, io.EOF
	}
	window := c.buf[c.start:c.end]
	if len(window) > cdcMaxChunkSize {
		window = window[:cdcMaxChunkSize]
	}
	n := findCDCBoundary(window, c.eof)
	chunk := c.buf[c.start : c.start+n : c.start+n]
	c.start += n
	return chunk, nil
}

// CDCSizes returns the chunk lengths production CDC v1 produces for r.
func CDCSizes(r io.Reader) ([]int, error) {
	c := newCDCChunker(r)
	defer c.release()
	var out []int
	for {
		ch, err := c.nextView()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, len(ch))
	}
}

// forEachChunk streams r through CDC calling fn with each chunk (a view valid
// only during the call) and its offset relative to the start of r.
func forEachChunk(r io.Reader, fn func(off int64, chunk []byte) error) error {
	c := newCDCChunker(r)
	defer c.release()
	var off int64
	for {
		ch, err := c.nextView()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(off, ch); err != nil {
			return err
		}
		off += int64(len(ch))
	}
}

// forEachFixed splits r into fixed-size blocks (last may be short).
func forEachFixed(r io.Reader, size int, fn func(off int64, blk []byte) error) error {
	buf := make([]byte, size)
	var off int64
	for {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			if e := fn(off, buf[:n]); e != nil {
				return e
			}
			off += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
