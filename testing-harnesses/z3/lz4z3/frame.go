package lz4z3

import (
	"bytes"
	"encoding/binary"
	"io"
)

const (
	magicFrame = 0x184D2204
	magicSkip  = 0x184D2A50 // ..0x184D2A5F
)

// FrameStats counts what AnalyzeFrames saw.
type FrameStats struct {
	Frames, Skippable, Blocks, Stored int
	Predicted, Hybrid, Explicit       int
	Linked                            int // frames with dependent blocks (explicit recipes only)
	RecipeBytes, ExplicitSeqs         int
	HeaderBytes, TailBytes            int
}

var blockMax = [8]int{0, 0, 0, 0, 64 << 10, 256 << 10, 1 << 20, 4 << 20}

// Stream recipe grammar (all integers uvarint):
//
//	stream  = { 1 frame | 2 len skippable-bytes } 0 taillen tail
//	frame   = hlen header nblocks { block } ccks [4 explicit-checksum]
//	block   = 0 declen recipe [4 explicit xxh32 if the frame has block checksums and it is not derived-flagged]
//	        | 1 storedlen
//
// Block tags carry bit 1 (value +2) when an explicit 4-byte block checksum follows.

// AnalyzeFrames parses a whole LZ4 frame stream (frames, skippable frames, opaque tail)
// into the concatenated payload and an exact recipe.
func AnalyzeFrames(data []byte, lim Limits) (plain, recipe []byte, st FrameStats, err error) {
	lim = lim.norm()
	var pb bytes.Buffer
	var rb []byte
	pos := 0
	for pos+4 <= len(data) {
		magic := binary.LittleEndian.Uint32(data[pos:])
		if magic&^0xF == magicSkip {
			if pos+8 > len(data) {
				break
			}
			n := int(binary.LittleEndian.Uint32(data[pos+4:]))
			if n < 0 || n > len(data)-pos-8 {
				return nil, nil, st, corrupt("skippable frame of %d bytes", n)
			}
			rb = append(rb, 2)
			rb = binary.AppendUvarint(rb, uint64(8+n))
			rb = append(rb, data[pos:pos+8+n]...)
			pos += 8 + n
			st.Skippable++
			continue
		}
		if magic != magicFrame {
			break
		}
		fr, np, e := analyzeFrame(data, pos, &pb, lim, &st)
		if e != nil {
			return nil, nil, st, e
		}
		rb = append(rb, 1)
		rb = append(rb, fr...)
		pos = np
		st.Frames++
	}
	rb = append(rb, 0)
	rb = binary.AppendUvarint(rb, uint64(len(data)-pos))
	rb = append(rb, data[pos:]...)
	st.TailBytes = len(data) - pos
	st.RecipeBytes = len(rb)
	return pb.Bytes(), rb, st, nil
}

type descriptor struct {
	indep, bcks, csize, ccks bool
	bmax, hl                 int
}

func parseDescriptor(h []byte) (d descriptor, err error) {
	if len(h) < 7 {
		return d, corrupt("truncated frame header")
	}
	flg, bd := h[4], h[5]
	if flg>>6 != 1 || flg&2 != 0 || bd&0x8F != 0 || blockMax[(bd>>4)&7] == 0 {
		return d, corrupt("frame descriptor %02x %02x", flg, bd)
	}
	if flg&1 != 0 {
		return d, ErrUnsupported // dictionary id: external dictionary needed
	}
	d = descriptor{indep: flg&0x20 != 0, bcks: flg&0x10 != 0, csize: flg&8 != 0, ccks: flg&4 != 0, bmax: blockMax[(bd>>4)&7], hl: 7}
	if d.csize {
		d.hl += 8
	}
	if len(h) < d.hl {
		return d, corrupt("truncated frame header")
	}
	return d, nil
}

func analyzeFrame(data []byte, pos int, pb *bytes.Buffer, lim Limits, st *FrameStats) ([]byte, int, error) {
	d, err := parseDescriptor(data[pos:])
	if err != nil {
		return nil, 0, err
	}
	hdr := data[pos : pos+d.hl]
	st.HeaderBytes += d.hl
	pos += d.hl
	fr := binary.AppendUvarint(nil, uint64(d.hl))
	fr = append(fr, hdr...)
	if !d.indep {
		st.Linked++
	}
	var content []byte // this frame's payload so far
	var blocks []byte
	nblocks := 0
	for {
		if pos+4 > len(data) {
			return nil, 0, corrupt("truncated block size")
		}
		w := binary.LittleEndian.Uint32(data[pos:])
		pos += 4
		if w == 0 {
			break
		}
		raw := w&0x80000000 != 0
		sz := int(w & 0x7FFFFFFF)
		if sz > d.bmax || sz > len(data)-pos {
			return nil, 0, corrupt("block of %d bytes (max %d, remaining %d)", sz, d.bmax, len(data)-pos)
		}
		if nblocks >= lim.MaxBlocks {
			return nil, 0, ErrLimit
		}
		nblocks++
		blk := data[pos : pos+sz]
		pos += sz
		var ext []byte
		if d.bcks {
			if pos+4 > len(data) {
				return nil, 0, corrupt("truncated block checksum")
			}
			if c := binary.LittleEndian.Uint32(data[pos:]); c != XXH32(blk, 0) {
				ext = data[pos : pos+4]
			}
			pos += 4
		}
		tag := byte(0)
		if raw {
			tag = 1
		}
		if ext != nil {
			tag |= 2
		}
		var payload []byte
		blocks = append(blocks, tag)
		if raw {
			payload = blk
			blocks = binary.AppendUvarint(blocks, uint64(sz))
			st.Stored++
		} else {
			var hist []byte
			if !d.indep {
				hist = content
				if len(hist) > maxOff {
					hist = hist[len(hist)-maxOff:]
				}
			}
			p, seqs, err := DecodeH(blk, hist, -1, lim)
			if err != nil {
				return nil, 0, err
			}
			if len(p) > d.bmax {
				return nil, 0, corrupt("block decodes to %d > %d", len(p), d.bmax)
			}
			rc := Analyze(p, seqs, d.indep)
			switch rc.Kind {
			case KindPredicted:
				st.Predicted++
			case KindHybrid:
				st.Hybrid++
			default:
				st.Explicit++
				st.ExplicitSeqs += len(seqs)
			}
			if rep, e := rc.Replay(hist, p, lim); e != nil || !bytes.Equal(rep, blk) {
				return nil, 0, recipeErr("recipe does not reproduce the block (%v)", e)
			}
			blocks = binary.AppendUvarint(blocks, uint64(len(p)))
			blocks = append(blocks, rc.Marshal()...)
			payload = p
		}
		blocks = append(blocks, ext...)
		st.Blocks++
		if len(content)+len(payload) > lim.MaxDecoded {
			return nil, 0, ErrLimit
		}
		content = append(content, payload...)
		pb.Write(payload)
	}
	fr = binary.AppendUvarint(fr, uint64(nblocks))
	fr = append(fr, blocks...)
	if !d.ccks {
		fr = append(fr, 0)
	} else {
		if pos+4 > len(data) {
			return nil, 0, corrupt("truncated content checksum")
		}
		if got := binary.LittleEndian.Uint32(data[pos:]); got == XXH32(content, 0) {
			fr = append(fr, 1)
		} else {
			fr = append(append(fr, 2), data[pos:pos+4]...)
		}
		pos += 4
	}
	if d.csize && binary.LittleEndian.Uint64(hdr[6:]) != uint64(len(content)) {
		// the declared content size is part of the verbatim header; a mismatch is the producer's
		// fact, not an error: the header bytes are reproduced as stored.
		_ = 0
	}
	return fr, pos, nil
}

func put(w io.Writer, b []byte) error {
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}

type rd struct {
	b []byte
	i int
}

func (r *rd) uv(max uint64) (uint64, error) {
	v, n := binary.Uvarint(r.b[r.i:])
	if n <= 0 || v > max {
		return 0, recipeErr("frame recipe integer at %d", r.i)
	}
	r.i += n
	return v, nil
}
func (r *rd) take(n uint64) ([]byte, error) {
	if n > uint64(len(r.b)-r.i) {
		return nil, recipeErr("frame recipe truncated at %d", r.i)
	}
	s := r.b[r.i : r.i+int(n)]
	r.i += int(n)
	return s, nil
}

// ReplayFrames rebuilds the exact frame stream from the recipe and the payload stream.
func ReplayFrames(w io.Writer, recipe []byte, plain io.Reader, lim Limits) error {
	lim = lim.norm()
	r := &rd{b: recipe}
	var total int64
	for {
		kind, err := r.take(1)
		if err != nil {
			return err
		}
		switch kind[0] {
		case 0:
			n, err := r.uv(1 << 40)
			if err != nil {
				return err
			}
			t, err := r.take(n)
			if err != nil {
				return err
			}
			if err := put(w, t); err != nil {
				return err
			}
			if r.i != len(r.b) {
				return recipeErr("trailing recipe bytes")
			}
			return nil
		case 2:
			n, err := r.uv(1 << 40)
			if err != nil {
				return err
			}
			t, err := r.take(n)
			if err != nil {
				return err
			}
			if err := put(w, t); err != nil {
				return err
			}
		case 1:
			if err := replayFrame(w, r, plain, lim, &total); err != nil {
				return err
			}
		default:
			return recipeErr("stream tag %d", kind[0])
		}
	}
}

func replayFrame(w io.Writer, r *rd, plain io.Reader, lim Limits, total *int64) error {
	hl, err := r.uv(64)
	if err != nil {
		return err
	}
	hdr, err := r.take(hl)
	if err != nil {
		return err
	}
	d, err := parseDescriptor(hdr)
	if err != nil || d.hl != len(hdr) {
		return recipeErr("frame header")
	}
	if err := put(w, hdr); err != nil {
		return err
	}
	nb, err := r.uv(uint64(lim.MaxBlocks))
	if err != nil {
		return err
	}
	var content []byte
	for i := uint64(0); i < nb; i++ {
		tag, err := r.take(1)
		if err != nil {
			return err
		}
		if tag[0] > 3 || (tag[0]&2 != 0 && !d.bcks) {
			return recipeErr("block tag %d", tag[0])
		}
		var n uint64
		var rc Recipe
		if n, err = r.uv(uint64(d.bmax)); err != nil {
			return err
		}
		if tag[0]&1 == 0 {
			var k int
			if rc, k, err = Unmarshal(r.b[r.i:], lim); err != nil {
				return err
			}
			r.i += k
		}
		if int64(len(content))+int64(n) > int64(lim.MaxDecoded) {
			return ErrLimit
		}
		p := make([]byte, n)
		if _, err := io.ReadFull(plain, p); err != nil {
			return recipeErr("payload ended inside a block: %v", err)
		}
		var blk []byte
		sz := uint32(n)
		if tag[0]&1 != 0 {
			blk, sz = p, uint32(n)|0x80000000
		} else {
			var hist []byte
			if !d.indep {
				hist = content
				if len(hist) > maxOff {
					hist = hist[len(hist)-maxOff:]
				}
			}
			if blk, err = rc.Replay(hist, p, lim); err != nil {
				return err
			}
			sz = uint32(len(blk))
			if len(blk) > d.bmax {
				return recipeErr("replayed block of %d > %d", len(blk), d.bmax)
			}
		}
		var hd [4]byte
		binary.LittleEndian.PutUint32(hd[:], sz)
		if err := put(w, hd[:]); err != nil {
			return err
		}
		if err := put(w, blk); err != nil {
			return err
		}
		if d.bcks {
			c := XXH32(blk, 0)
			if tag[0]&2 != 0 {
				e, err := r.take(4)
				if err != nil {
					return err
				}
				c = binary.LittleEndian.Uint32(e)
			}
			binary.LittleEndian.PutUint32(hd[:], c)
			if err := put(w, hd[:]); err != nil {
				return err
			}
		}
		content = append(content, p...)
		*total += int64(n)
	}
	if err := put(w, []byte{0, 0, 0, 0}); err != nil {
		return err
	}
	ck, err := r.take(1)
	if err != nil {
		return err
	}
	if (ck[0] != 0) != d.ccks || ck[0] > 2 {
		return recipeErr("content checksum flag")
	}
	var c uint32
	switch ck[0] {
	case 1:
		c = XXH32(content, 0)
	case 2:
		e, err := r.take(4)
		if err != nil {
			return err
		}
		c = binary.LittleEndian.Uint32(e)
	}
	if ck[0] != 0 {
		var b [4]byte
		binary.LittleEndian.PutUint32(b[:], c)
		err = put(w, b[:])
	}
	return err
}
