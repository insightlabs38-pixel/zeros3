package frontier

import (
	"errors"
	"fmt"
	"io"

	"zeros3-z3/zstdz3"
)

// ZBlock is one zstd block as laid out in the compressed stream.
type ZBlock struct {
	Off  int64 // offset of the 3-byte block header
	Len  int64 // header + content
	Type uint8 // 0 raw, 1 RLE, 2 compressed
	Dec  int64 // regenerated size if known from the header (raw/RLE), else 0
}

// ZFrame is a zstd or skippable frame.
type ZFrame struct {
	Off, Len  int64
	Skippable bool
	Blocks    []ZBlock
	Checksum  bool
}

var ErrZstd = errors.New("zstd: malformed stream")

// ScanZstd walks frame and block headers without decoding any block. It is a thin
// view over zstdz3.Scan, the single strict frame parser (bounded: maxFrames/maxBlocks
// cap work, every length is checked against size).
func ScanZstd(r io.ReaderAt, size int64, maxFrames, maxBlocks int) ([]ZFrame, error) {
	fs, err := zstdz3.Scan(r, size, zstdz3.Limits{MaxFrames: maxFrames, MaxBlocks: maxBlocks})
	out := make([]ZFrame, 0, len(fs))
	for _, f := range fs {
		z := ZFrame{Off: f.Off, Len: f.Len, Skippable: f.Skippable, Checksum: f.Checksum}
		for _, b := range f.Blocks {
			z.Blocks = append(z.Blocks, ZBlock{Off: b.Off, Len: b.Len, Type: uint8(b.Type), Dec: b.Dec})
		}
		out = append(out, z)
	}
	if err != nil {
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			err = fmt.Errorf("%w: %v", ErrZstd, err)
		}
		return out, err
	}
	return out, nil
}
