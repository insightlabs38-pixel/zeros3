package zstdz3

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
)

// Checkpoints make the compressed object randomly readable. Replay of a block needs only
// the block's plaintext, its unit, the tables it inherits and, for model-coded
// sequences, the preceding window of plaintext. A checkpoint records where a block
// sits in the compressed stream, the plaintext and the unit stream, and the active
// tables (as their verbatim descriptions), so replay can start there without any
// earlier block; the plaintext window comes from the semantic child by ranged reads.

// TableRef is an active table as introduced by the stream.
type TableRef struct {
	Kind uint8 // tabNone, tabPredefined, tabRLE or tabFSE
	Desc []byte
}

const (
	tabNone = 3 // no table yet (a Repeat mode here would be invalid)
)

// Checkpoint is the replay state at the start of one block.
type Checkpoint struct {
	Frame, Block int   // frame index in the skeleton; block index within the frame
	FramePlain   int64 // stream plaintext offset of the frame start
	PlainOff     int64 // stream plaintext offset of the block
	CompOff      int64 // stream offset of the block header
	UnitOff      int64 // offset of the block's unit in the unit stream
	SkelOff      int   // offset of the block record in the skeleton
	Tree         []byte
	Tabs         [3]TableRef
}

func (c *Checkpoint) snapshot(st *codingState) {
	if st.huff != nil {
		c.Tree = append([]byte(nil), st.huff.desc...)
	}
	for k, t := range st.fse {
		if t == nil {
			c.Tabs[k] = TableRef{Kind: tabNone}
			continue
		}
		c.Tabs[k] = TableRef{Kind: t.kind, Desc: append([]byte(nil), t.desc...)}
	}
}

// state rebuilds the inherited coding state.
func (c *Checkpoint) state() (st codingState, err error) {
	if len(c.Tree) > 0 {
		t, n, err := readHuffTree(c.Tree)
		if err != nil || n != len(c.Tree) {
			return st, badIR("checkpoint tree")
		}
		st.huff = t
	}
	for k, r := range c.Tabs {
		switch r.Kind {
		case tabNone:
		case 0:
			st.fse[k] = predTables[k]
		case 1:
			if len(r.Desc) != 1 || int(r.Desc[0]) > maxSym[k] {
				return st, badIR("checkpoint rle table")
			}
			st.fse[k] = rleTable(r.Desc[0])
		case 2:
			t, n, err := readFSEDesc(r.Desc, maxAcc[k], maxSym[k])
			if err != nil || n != len(r.Desc) {
				return st, badIR("checkpoint fse table")
			}
			st.fse[k] = t
		default:
			return st, badIR("checkpoint table kind %d", r.Kind)
		}
	}
	return st, nil
}

// Marshal is the canonical serialization of a checkpoint (bytes charged to the index).
func (c *Checkpoint) Marshal() []byte {
	var b []byte
	for _, v := range []uint64{uint64(c.Frame), uint64(c.Block), uint64(c.FramePlain), uint64(c.PlainOff), uint64(c.CompOff), uint64(c.UnitOff), uint64(c.SkelOff)} {
		b = binary.AppendUvarint(b, v)
	}
	b = binary.AppendUvarint(b, uint64(len(c.Tree)))
	b = append(b, c.Tree...)
	for _, t := range c.Tabs {
		b = append(b, t.Kind)
		if t.Kind == 1 || t.Kind == 2 {
			b = binary.AppendUvarint(b, uint64(len(t.Desc)))
			b = append(b, t.Desc...)
		}
	}
	return b
}

// UnmarshalCheckpoint parses and validates a checkpoint, consuming b exactly.
func UnmarshalCheckpoint(b []byte) (*Checkpoint, error) {
	r := &skelReader{b: b}
	var v [7]uint64
	for i := range v {
		x, err := r.uv()
		if err != nil {
			return nil, err
		}
		v[i] = x
	}
	if v[3] > 1<<62 || v[4] > 1<<62 || v[5] > 1<<62 || v[6] > 1<<31 || v[2] > v[3] {
		return nil, badIR("checkpoint position")
	}
	c := &Checkpoint{Frame: int(v[0]), Block: int(v[1]), FramePlain: int64(v[2]), PlainOff: int64(v[3]), CompOff: int64(v[4]), UnitOff: int64(v[5]), SkelOff: int(v[6])}
	n, err := r.count()
	if err != nil {
		return nil, err
	}
	if c.Tree, err = r.take(n); err != nil {
		return nil, err
	}
	if n == 0 {
		c.Tree = nil
	}
	for k := range c.Tabs {
		kind, err := r.byte()
		if err != nil {
			return nil, err
		}
		c.Tabs[k].Kind = kind
		if kind == 1 || kind == 2 {
			n, err := r.count()
			if err != nil {
				return nil, err
			}
			if c.Tabs[k].Desc, err = r.take(n); err != nil {
				return nil, err
			}
		} else if kind != 0 && kind != tabNone {
			return nil, badIR("checkpoint table kind %d", kind)
		}
	}
	if r.p != len(b) {
		return nil, badIR("%d trailing bytes", len(b)-r.p)
	}
	if _, err := c.state(); err != nil {
		return nil, err
	}
	return c, nil
}

// RangeStats says what a ranged replay touched.
type RangeStats struct {
	Blocks      int   // blocks replayed
	Out         int64 // compressed bytes produced (from the checkpoint on)
	UnitBytes   int64 // unit-stream bytes consumed
	PlainBytes  int64 // plaintext read for the blocks themselves
	WindowBytes int64 // plaintext read for the model's window
	SkelBytes   int   // skeleton bytes of the replayed block records
}

// ReplayFrom writes the compressed stream from the start of the checkpoint's block until
// at least stop bytes of the stream (an absolute compressed offset) have been produced
// or the stream ends. units must be positioned at cp.UnitOff; plain serves the semantic
// plaintext at stream offsets (ReadAt must return len(p) bytes or an error).
func ReplayFrom(w io.Writer, s *Skeleton, cp *Checkpoint, units io.Reader, plain io.ReaderAt, stop int64) (RangeStats, error) {
	var rs RangeStats
	if cp.Frame < 0 || cp.Frame >= len(s.Frames) || s.Frames[cp.Frame].Skippable || cp.Block < 0 || cp.Block >= len(s.Frames[cp.Frame].Blocks) {
		return rs, badIR("checkpoint outside the skeleton")
	}
	st, err := cp.state()
	if err != nil {
		return rs, err
	}
	ub := bufio.NewReaderSize(&countReader{r: units, n: &rs.UnitBytes}, 1<<12)
	pos := cp.CompOff
	var buf []byte
	readPlain := func(off int64, n int) ([]byte, error) {
		b := make([]byte, n)
		if _, err := plain.ReadAt(b, off); err != nil {
			return nil, fmt.Errorf("%w: semantic plaintext at %d: %v", ErrMismatch, off, err)
		}
		return b, nil
	}
	for fi := cp.Frame; fi < len(s.Frames) && pos < stop; fi++ {
		f := &s.Frames[fi]
		bi, fplain := 0, cp.FramePlain
		if fi != cp.Frame {
			var err error
			if fplain, err = planEnd(s, fi); err != nil {
				return rs, err
			}
		} else {
			bi = cp.Block
		}
		if f.Skippable {
			var h [8]byte
			binary.LittleEndian.PutUint32(h[:], magicSkip|uint32(f.SkipLow))
			binary.LittleEndian.PutUint32(h[4:], uint32(len(f.Skip)))
			if err := writeAll(w, &pos, h[:], f.Skip); err != nil {
				return rs, err
			}
			continue
		}
		fh, err := parseFrameHeader(f.Header, Limits{MaxWindow: 1 << 62, MaxFrameDecoded: 1 << 62})
		if err != nil {
			return rs, err
		}
		rf := &replayFrame{bmax: min(fh.blockMax(), maxBlock), hist: history{window: fh.window}}
		poff := fplain
		for j := 0; j < bi; j++ {
			poff += int64(f.Blocks[j].DecLen)
		}
		if fi == cp.Frame {
			rf.st = st
		} else {
			var m [4]byte
			binary.LittleEndian.PutUint32(m[:], magicStd)
			if err := writeAll(w, &pos, m[:], f.Header); err != nil {
				return rs, err
			}
		}
		for j := bi; j < len(f.Blocks); j++ {
			if f.Blocks[j].Model {
				rf.model = newMatchModel(fh.window)
				break
			}
		}
		if rf.model != nil && poff > fplain {
			lo := max(fplain, poff-fh.window)
			wb, err := readPlain(lo, int(poff-lo))
			if err != nil {
				return rs, err
			}
			rs.WindowBytes += int64(len(wb))
			rf.hist.buf, rf.hist.total = wb, poff-fplain
		} else {
			rf.hist.total = poff - fplain
		}
		for j := bi; j < len(f.Blocks) && pos < stop; j++ {
			k := &f.Blocks[j]
			buf, err = readPlain(poff, int(k.DecLen))
			if err != nil {
				return rs, err
			}
			rs.PlainBytes += int64(k.DecLen)
			rs.SkelBytes += len(k.appendTo(nil))
			content, typ, err := rf.rebuildBlock(k, buf, ub)
			if err != nil {
				return rs, fmt.Errorf("frame %d block %d: %w", fi, j, err)
			}
			size := len(content)
			if typ == BlockRLE {
				size = int(k.DecLen)
			}
			if err := writeAll(w, &pos, blockHeader(k.Last, typ, size), content); err != nil {
				return rs, err
			}
			rs.Blocks++
			poff += int64(k.DecLen)
			if k.Last && fh.checksum {
				var c [4]byte
				binary.LittleEndian.PutUint32(c[:], f.Checksum)
				if err := writeAll(w, &pos, c[:]); err != nil {
					return rs, err
				}
			}
		}
	}
	rs.Out = pos - cp.CompOff
	rs.UnitBytes -= int64(ub.Buffered())
	return rs, nil
}

type countReader struct {
	r io.Reader
	n *int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

func writeAll(w io.Writer, pos *int64, parts ...[]byte) error {
	for _, p := range parts {
		n, err := w.Write(p)
		*pos += int64(n)
		if err != nil {
			return err
		}
	}
	return nil
}

// planEnd is the stream plaintext offset at which frame fi starts.
func planEnd(s *Skeleton, fi int) (int64, error) {
	var off int64
	for i := 0; i < fi; i++ {
		for j := range s.Frames[i].Blocks {
			off += int64(s.Frames[i].Blocks[j].DecLen)
		}
	}
	return off, nil
}
