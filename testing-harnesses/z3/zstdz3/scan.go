package zstdz3

import (
	"encoding/binary"
	"io"
)

// BlockSpan locates one block (header included) in the compressed stream.
type BlockSpan struct {
	Off, Len int64
	Type     BlockType // BlockRaw, BlockRLE or BlockCompressed
	Dec      int64     // regenerated size when the header gives it (raw, RLE), else 0
}

// FrameSpan locates one frame.
type FrameSpan struct {
	Off, Len  int64
	Skippable bool
	Checksum  bool
	DictID    uint32
	Window    int64
	Blocks    []BlockSpan
}

// Scan walks frame and block headers without decoding any block. It validates the
// structure the parser validates (reserved bits, block types, sizes against the
// window, overruns, truncation, limits) but not block contents, and reports a nonzero
// dictionary ID instead of refusing it. Work is bounded by lim and size.
func Scan(r io.ReaderAt, size int64, lim Limits) ([]FrameSpan, error) {
	lim = lim.fill()
	var frames []FrameSpan
	var nblocks int
	var hdr [18]byte
	for off := int64(0); off < size; {
		if len(frames) >= lim.MaxFrames {
			return frames, limit("more than %d frames", lim.MaxFrames)
		}
		if size-off < 4 {
			return frames, corrupt("truncated magic at %d", off)
		}
		if _, err := r.ReadAt(hdr[:4], off); err != nil {
			return frames, err
		}
		magic := binary.LittleEndian.Uint32(hdr[:4])
		if magic&magicSkipM == magicSkip {
			if size-off < 8 {
				return frames, corrupt("truncated skippable frame at %d", off)
			}
			if _, err := r.ReadAt(hdr[4:8], off+4); err != nil {
				return frames, err
			}
			n := int64(binary.LittleEndian.Uint32(hdr[4:8]))
			if n > size-off-8 {
				return frames, corrupt("skippable frame at %d overruns", off)
			}
			frames = append(frames, FrameSpan{Off: off, Len: 8 + n, Skippable: true})
			off += 8 + n
			continue
		}
		if magic != magicStd {
			return frames, corrupt("bad magic %#x at %d", magic, off)
		}
		if size-off < 5 {
			return frames, corrupt("truncated header at %d", off)
		}
		if _, err := r.ReadAt(hdr[:1], off+4); err != nil {
			return frames, err
		}
		hl := int64(fhdLen(hdr[0]))
		if hl > size-off-4 {
			return frames, corrupt("header overruns at %d", off)
		}
		if _, err := r.ReadAt(hdr[:hl], off+4); err != nil {
			return frames, err
		}
		fh, err := parseFrameHeaderX(append([]byte(nil), hdr[:hl]...), lim, true)
		if err != nil {
			return frames, err
		}
		f := FrameSpan{Off: off, Checksum: fh.checksum, DictID: fh.dictID, Window: fh.window}
		pos := off + 4 + hl
		bmax := int64(fh.blockMax())
		for last := false; !last; {
			if size-pos < 3 {
				return frames, corrupt("truncated block header at %d", pos)
			}
			var bh [3]byte
			if _, err := r.ReadAt(bh[:], pos); err != nil {
				return frames, err
			}
			v := uint32(bh[0]) | uint32(bh[1])<<8 | uint32(bh[2])<<16
			typ, bs := BlockType(v>>1&3), int64(v>>3)
			last = v&1 == 1
			if typ == 3 {
				return frames, corrupt("reserved block type at %d", pos)
			}
			if bs > bmax {
				return frames, corrupt("block of %d bytes at %d, at most %d", bs, pos, bmax)
			}
			content := bs
			b := BlockSpan{Off: pos, Type: typ}
			switch typ {
			case BlockRaw:
				b.Dec = bs
			case BlockRLE:
				content, b.Dec = 1, bs
			}
			if content > size-pos-3 {
				return frames, corrupt("block at %d overruns", pos)
			}
			b.Len = 3 + content
			if nblocks++; nblocks > lim.MaxBlocks {
				return frames, limit("more than %d blocks", lim.MaxBlocks)
			}
			f.Blocks = append(f.Blocks, b)
			pos += b.Len
		}
		if fh.checksum {
			if size-pos < 4 {
				return frames, corrupt("truncated checksum at %d", pos)
			}
			pos += 4
		}
		f.Len = pos - off
		frames = append(frames, f)
		off = pos
	}
	return frames, nil
}
