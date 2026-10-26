package frontier

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
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

const (
	zstdMagic   = 0xFD2FB528
	zstdSkipLo  = 0x184D2A50
	zstdSkipMsk = 0xFFFFFFF0
)

var ErrZstd = errors.New("zstd: malformed stream")

// ScanZstd walks frame and block headers without decoding any block. It is
// bounded: maxFrames/maxBlocks cap work, every length is checked against size.
func ScanZstd(r io.ReaderAt, size int64, maxFrames, maxBlocks int) ([]ZFrame, error) {
	var frames []ZFrame
	var nblocks int
	var hdr [18]byte
	for off := int64(0); off < size; {
		if len(frames) >= maxFrames {
			return frames, fmt.Errorf("%w: more than %d frames", ErrZstd, maxFrames)
		}
		if size-off < 4 {
			return frames, fmt.Errorf("%w: truncated magic at %d", ErrZstd, off)
		}
		if _, err := r.ReadAt(hdr[:4], off); err != nil {
			return frames, err
		}
		magic := binary.LittleEndian.Uint32(hdr[:4])
		if magic&zstdSkipMsk == zstdSkipLo {
			if size-off < 8 {
				return frames, fmt.Errorf("%w: truncated skippable at %d", ErrZstd, off)
			}
			if _, err := r.ReadAt(hdr[4:8], off+4); err != nil {
				return frames, err
			}
			n := int64(binary.LittleEndian.Uint32(hdr[4:8]))
			if n > size-off-8 {
				return frames, fmt.Errorf("%w: skippable frame at %d overruns", ErrZstd, off)
			}
			frames = append(frames, ZFrame{Off: off, Len: 8 + n, Skippable: true})
			off += 8 + n
			continue
		}
		if magic != zstdMagic {
			return frames, fmt.Errorf("%w: bad magic %#x at %d", ErrZstd, magic, off)
		}
		n := int64(len(hdr))
		if n > size-off {
			n = size - off
		}
		if _, err := r.ReadAt(hdr[:n], off); err != nil && err != io.EOF {
			return frames, err
		}
		if n < 6 {
			return frames, fmt.Errorf("%w: truncated header at %d", ErrZstd, off)
		}
		fhd := hdr[4]
		fcsFlag, single, cksum, dictFlag := fhd>>6, fhd>>5&1 == 1, fhd>>2&1 == 1, fhd&3
		hl := int64(5)
		if !single {
			hl++
		}
		hl += [4]int64{0, 1, 2, 4}[dictFlag]
		switch fcsFlag {
		case 0:
			if single {
				hl++
			}
		case 1:
			hl += 2
		case 2:
			hl += 4
		case 3:
			hl += 8
		}
		if hl > size-off {
			return frames, fmt.Errorf("%w: header overruns at %d", ErrZstd, off)
		}
		f := ZFrame{Off: off, Checksum: cksum}
		pos := off + hl
		for {
			if size-pos < 3 {
				return frames, fmt.Errorf("%w: truncated block header at %d", ErrZstd, pos)
			}
			var bh [3]byte
			if _, err := r.ReadAt(bh[:], pos); err != nil {
				return frames, err
			}
			v := uint32(bh[0]) | uint32(bh[1])<<8 | uint32(bh[2])<<16
			last, typ, bs := v&1 == 1, uint8(v>>1&3), int64(v>>3)
			if typ == 3 {
				return frames, fmt.Errorf("%w: reserved block type at %d", ErrZstd, pos)
			}
			content := bs
			b := ZBlock{Off: pos, Type: typ}
			switch typ {
			case 0:
				b.Dec = bs
			case 1:
				content, b.Dec = 1, bs
			}
			if content > size-pos-3 {
				return frames, fmt.Errorf("%w: block at %d overruns", ErrZstd, pos)
			}
			b.Len = 3 + content
			if nblocks++; nblocks > maxBlocks {
				return frames, fmt.Errorf("%w: more than %d blocks", ErrZstd, maxBlocks)
			}
			f.Blocks = append(f.Blocks, b)
			pos += b.Len
			if last {
				break
			}
		}
		if cksum {
			if size-pos < 4 {
				return frames, fmt.Errorf("%w: truncated checksum at %d", ErrZstd, pos)
			}
			pos += 4
		}
		f.Len = pos - off
		frames = append(frames, f)
		off = pos
	}
	return frames, nil
}
