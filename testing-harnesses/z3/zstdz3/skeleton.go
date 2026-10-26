package zstdz3

import (
	"bytes"
	"encoding/binary"
)

// The skeleton is the small, canonical, sequential part of the reconstruction
// description. Wire format (experimental, local version byte only):
//
//	"ZS" 0x01 | uvarint frames
//	frame:    0x00 | header length (1 B) | header | uvarint blocks | blocks | checksum (4 B, if flagged)
//	          0x01 | skippable magic low nibble (1 B) | uvarint length | payload
//	block:    tag (1 B: bit0 last, bits1-2 type, bit3 model-coded sequences) | uvarint decoded length |
//	          RLE: byte | Fallback: uvarint compressed length (the bytes are in the unit stream) |
//	          Compressed: literals (1 B: mode | form<<2) | uvarint regenerated literals |
//	              [mode 2: uvarint len | tree description] | uvarint sequences |
//	              [n > 0: modes (1 B) | uvarint len | table descriptions | 3 x uvarint rank]
//
// The unit stream is separate: in block order, each Compressed block with sequences
// contributes its sequence decisions (3 x uvarint per sequence: literal length, match
// length - 3, offset value) and each Fallback block its original compressed bytes.
// Varints must be minimal and the skeleton consumed exactly.

// BlockType is how a block is represented.
type BlockType uint8

const (
	BlockRaw BlockType = iota
	BlockRLE
	BlockCompressed // transformed: literal and sequence entropy streams are regenerated
	BlockFallback   // original compressed bytes kept as a unit
)

func (t BlockType) String() string {
	return [...]string{"raw", "rle", "compressed", "fallback"}[t&3]
}

// BlockRec is one block of the skeleton.
type BlockRec struct {
	Last     bool
	Type     BlockType
	DecLen   uint32
	CompLen  uint32 // fallback: bytes of the original compressed block in the unit stream
	Model    bool   // sequences are coded by the match model (see model.go), not as explicit varints
	RLE      byte
	LitMode  uint8
	LitForm  uint8
	Regen    uint32
	TreeDesc []byte
	NSeq     uint32
	Modes    byte
	TabBytes []byte
	Rank     [3]uint32
}

// HasUnit reports whether the block consumes a unit.
func (b *BlockRec) HasUnit() bool {
	return b.Type == BlockFallback || (b.Type == BlockCompressed && b.NSeq > 0)
}

// FrameRec is one frame of the skeleton.
type FrameRec struct {
	Skippable bool
	SkipLow   uint8
	Skip      []byte // skippable payload, opaque and verbatim
	Header    []byte // standard frame header, verbatim
	Blocks    []BlockRec
	Checksum  uint32 // content checksum, verified at parse and at replay
}

// Skeleton is the decoded skeleton.
type Skeleton struct{ Frames []FrameRec }

const skelVersion = 1

// UnitCount is the number of blocks that contribute to the unit stream.
func (s *Skeleton) UnitCount() (n int) {
	for i := range s.Frames {
		for j := range s.Frames[i].Blocks {
			if s.Frames[i].Blocks[j].HasUnit() {
				n++
			}
		}
	}
	return
}

func appendUv(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

// Marshal returns the canonical serialization.
func (s *Skeleton) Marshal() []byte { return s.marshal(nil) }

// marshal serializes the skeleton; at, if set, receives the offset of every block record.
func (s *Skeleton) marshal(at func(frame, block, off int)) []byte {
	b := []byte{'Z', 'S', skelVersion}
	b = appendUv(b, uint64(len(s.Frames)))
	for i := range s.Frames {
		f := &s.Frames[i]
		if f.Skippable {
			b = append(b, 1, f.SkipLow)
			b = appendUv(b, uint64(len(f.Skip)))
			b = append(b, f.Skip...)
			continue
		}
		b = append(b, 0, byte(len(f.Header)))
		b = append(b, f.Header...)
		b = appendUv(b, uint64(len(f.Blocks)))
		for j := range f.Blocks {
			if at != nil {
				at(i, j, len(b))
			}
			b = f.Blocks[j].appendTo(b)
		}
		if len(f.Header) > 0 && f.Header[0]>>2&1 == 1 {
			b = binary.LittleEndian.AppendUint32(b, f.Checksum)
		}
	}
	return b
}

func (k *BlockRec) appendTo(b []byte) []byte {
	tag := byte(k.Type) << 1
	if k.Last {
		tag |= 1
	}
	if k.Model {
		tag |= 8
	}
	b = append(b, tag)
	b = appendUv(b, uint64(k.DecLen))
	switch k.Type {
	case BlockRLE:
		b = append(b, k.RLE)
	case BlockFallback:
		b = appendUv(b, uint64(k.CompLen))
	case BlockCompressed:
		b = append(b, k.LitMode|k.LitForm<<2)
		b = appendUv(b, uint64(k.Regen))
		if k.LitMode == litComp {
			b = appendUv(b, uint64(len(k.TreeDesc)))
			b = append(b, k.TreeDesc...)
		}
		b = appendUv(b, uint64(k.NSeq))
		if k.NSeq > 0 {
			b = append(b, k.Modes)
			b = appendUv(b, uint64(len(k.TabBytes)))
			b = append(b, k.TabBytes...)
			for _, r := range k.Rank {
				b = appendUv(b, uint64(r))
			}
		}
	}
	return b
}

type skelReader struct {
	b []byte
	p int
}

func (r *skelReader) byte() (byte, error) {
	if r.p >= len(r.b) {
		return 0, badIR("truncated")
	}
	r.p++
	return r.b[r.p-1], nil
}

func (r *skelReader) uv() (uint64, error) {
	v, n := binary.Uvarint(r.b[r.p:])
	if n <= 0 {
		return 0, badIR("bad varint")
	}
	if n != len(binary.AppendUvarint(nil, v)) {
		return 0, badIR("non-minimal varint")
	}
	r.p += n
	return v, nil
}

// count reads a varint that must not exceed the bytes left (each counted item takes at least one).
func (r *skelReader) count() (int, error) {
	v, err := r.uv()
	if err != nil {
		return 0, err
	}
	if v > uint64(len(r.b)-r.p) {
		return 0, badIR("count %d exceeds the remaining %d bytes", v, len(r.b)-r.p)
	}
	return int(v), nil
}

func (r *skelReader) take(n int) ([]byte, error) {
	if n < 0 || n > len(r.b)-r.p {
		return nil, badIR("truncated")
	}
	r.p += n
	return r.b[r.p-n : r.p], nil
}

// Unmarshal parses and validates a skeleton: canonical encoding, enum ranges, header
// validity, table descriptions that parse to exactly their length, per-frame
// coverage of Frame_Content_Size, and no trailing bytes. Sub-slices alias b.
func Unmarshal(b []byte, lim Limits) (*Skeleton, error) {
	lim = lim.fill()
	r := &skelReader{b: b}
	if len(b) < 3 || b[0] != 'Z' || b[1] != 'S' || b[2] != skelVersion {
		return nil, badIR("bad magic or version")
	}
	r.p = 3
	nf, err := r.count()
	if err != nil {
		return nil, err
	}
	if nf > lim.MaxFrames {
		return nil, badIR("%d frames", nf)
	}
	s := &Skeleton{Frames: make([]FrameRec, nf)}
	var nblocks int
	for i := range s.Frames {
		f := &s.Frames[i]
		kind, err := r.byte()
		if err != nil {
			return nil, err
		}
		switch kind {
		case 1:
			low, err := r.byte()
			if err != nil || low > 15 {
				return nil, badIR("skippable magic")
			}
			n, err := r.count()
			if err != nil {
				return nil, err
			}
			f.Skippable, f.SkipLow = true, low
			f.Skip, _ = r.take(n)
		case 0:
			hl, err := r.byte()
			if err != nil {
				return nil, err
			}
			if f.Header, err = r.take(int(hl)); err != nil {
				return nil, err
			}
			fh, err := parseFrameHeader(f.Header, lim)
			if err != nil {
				return nil, badIR("frame header: %v", err)
			}
			n, err := r.count()
			if err != nil {
				return nil, err
			}
			if n == 0 {
				return nil, badIR("frame without blocks")
			}
			if nblocks += n; nblocks > lim.MaxBlocks {
				return nil, badIR("%d blocks", nblocks)
			}
			f.Blocks = make([]BlockRec, n)
			var total int64
			for j := range f.Blocks {
				k := &f.Blocks[j]
				if err := r.block(k, fh.blockMax()); err != nil {
					return nil, err
				}
				if k.Last != (j == n-1) {
					return nil, badIR("last-block flag")
				}
				if total += int64(k.DecLen); total > lim.MaxFrameDecoded {
					return nil, badIR("frame decodes past the limit")
				}
			}
			if fh.hasFCS && uint64(total) != fh.fcs {
				return nil, badIR("blocks cover %d bytes, frame header says %d", total, fh.fcs)
			}
			if fh.checksum {
				c, err := r.take(4)
				if err != nil {
					return nil, err
				}
				f.Checksum = binary.LittleEndian.Uint32(c)
			}
		default:
			return nil, badIR("frame kind %d", kind)
		}
	}
	if r.p != len(b) {
		return nil, badIR("%d trailing bytes", len(b)-r.p)
	}
	return s, nil
}

func (r *skelReader) block(k *BlockRec, bmax int) error {
	tag, err := r.byte()
	if err != nil {
		return err
	}
	if tag>>4 != 0 {
		return badIR("block tag %#x", tag)
	}
	k.Last, k.Type, k.Model = tag&1 == 1, BlockType(tag>>1&3), tag&8 != 0
	if k.Model && k.Type != BlockCompressed {
		return badIR("model flag on a %v block", k.Type)
	}
	dl, err := r.uv()
	if err != nil {
		return err
	}
	if dl > uint64(bmax) {
		return badIR("block of %d bytes in a window allowing %d", dl, bmax)
	}
	k.DecLen = uint32(dl)
	switch k.Type {
	case BlockRaw:
		return nil
	case BlockRLE:
		k.RLE, err = r.byte()
		return err
	case BlockFallback:
		cl, err := r.uv()
		if err != nil {
			return err
		}
		if cl == 0 || cl > uint64(bmax) {
			return badIR("fallback block of %d bytes", cl)
		}
		k.CompLen = uint32(cl)
		return nil
	}
	lit, err := r.byte()
	if err != nil {
		return err
	}
	k.LitMode, k.LitForm = lit&3, lit>>2
	if lit>>4 != 0 || (k.LitMode <= litRLE && k.LitForm > 2) {
		return badIR("literals mode byte %#x", lit)
	}
	rg, err := r.uv()
	if err != nil {
		return err
	}
	if rg > dl {
		return badIR("%d literals in a block of %d", rg, dl)
	}
	k.Regen = uint32(rg)
	if k.LitMode == litComp {
		n, err := r.count()
		if err != nil {
			return err
		}
		k.TreeDesc, _ = r.take(n)
		if _, used, err := readHuffTree(k.TreeDesc); err != nil || used != len(k.TreeDesc) {
			return badIR("huffman tree description")
		}
	}
	ns, err := r.uv()
	if err != nil {
		return err
	}
	if ns > uint64(bmax/3) {
		return badIR("%d sequences", ns)
	}
	k.NSeq = uint32(ns)
	if k.Model && ns == 0 {
		return badIR("model flag on a block without sequences")
	}
	if ns == 0 {
		if k.Regen != k.DecLen {
			return badIR("block without sequences regenerates %d of %d bytes", k.Regen, k.DecLen)
		}
		return nil
	}
	if k.Modes, err = r.byte(); err != nil {
		return err
	}
	if k.Modes&3 != 0 {
		return badIR("reserved sequence mode bits")
	}
	n, err := r.count()
	if err != nil {
		return err
	}
	k.TabBytes, _ = r.take(n)
	prev := [3]*fseTable{predTables[0], predTables[1], predTables[2]}
	if _, used, err := parseSeqTables(k.Modes, k.TabBytes, prev); err != nil || used != len(k.TabBytes) {
		return badIR("sequence table descriptions")
	}
	for i := range k.Rank {
		v, err := r.uv()
		if err != nil {
			return err
		}
		if v > 1<<12 {
			return badIR("rank %d", v)
		}
		k.Rank[i] = uint32(v)
	}
	return nil
}

// Equal reports whether two skeletons serialize identically (used by tests).
func (s *Skeleton) Equal(o *Skeleton) bool { return bytes.Equal(s.Marshal(), o.Marshal()) }
