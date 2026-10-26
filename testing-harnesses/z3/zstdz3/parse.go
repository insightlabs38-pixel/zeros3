package zstdz3

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
)

// Stats accounts for what a Parse found and what the description costs, per class.
type Stats struct {
	Frames, Skippable int
	Blocks            [4]int   // by BlockType: raw, rle, transformed, fallback
	StreamBytes       [4]int64 // compressed stream bytes (block header included) per type
	PlainBytes        int64
	TotalBytes        int64 // the whole input
	Literals          int64
	Sequences         int64

	// Description bytes by class.
	SkeletonBytes int // whole skeleton
	TreeBytes     int // verbatim Huffman tree descriptions
	TableBytes    int // verbatim FSE/RLE table descriptions and mode bytes
	RankBytes     int // residual final-state ranks
	SkipBytes     int // skippable payloads
	FrameBytes    int // everything else in the skeleton: headers, block records, counts
	UnitBytes     int // sequence-decision units
	FallbackBytes int // bytes of fallback units (original compressed blocks)

	// Coverage of coding modes among compressed blocks (transformed or fallback).
	LitModes     [4]int
	LitStreams   [5]int // index 1 or 4
	HuffDirect   int
	HuffFSE      int
	SeqModes     [3][4]int // LL, OF, ML by mode
	RepeatCodes  [4]int    // offset values 1,2,3 and >3
	ZeroLLRepeat int       // repeat-offset codes with a zero literal length
	Empty        int       // compressed blocks without sequences
}

// IRBytes is the full description: skeleton plus units.
func (s *Stats) IRBytes() int { return s.SkeletonBytes + s.UnitBytes + s.FallbackBytes }

// Options configure Parse.
type Options struct {
	Limits
	// NoSeqModel keeps every block's sequences as explicit varints instead of the
	// plaintext-derived match model (model.go).
	NoSeqModel bool
	// CheckpointEvery records a Checkpoint at every n-th block of each frame (0: none).
	CheckpointEvery int
}

// Result is the outcome of Parse.
type Result struct {
	Skeleton  *Skeleton
	Raw       []byte   // Skeleton.Marshal()
	UnitSizes []uint32 // bytes each unit contributed to the unit stream, in block order
	// Checkpoints are the requested replay starting points, in stream order.
	Checkpoints []Checkpoint
	Stats       Stats
}

// Parse decodes the Zstandard stream r, writes the plaintext to plain (progressively,
// block by block), writes the unit stream to units (nil to discard) and returns the
// skeleton. The stream must be
// dictionary-free; every frame is verified (content size and checksum when present).
// Compressed blocks are replayed against the original as they are parsed, so a
// returned BlockCompressed record is known to rebuild byte for byte; anything else
// becomes a BlockFallback and is counted in Stats.
func Parse(r io.Reader, plain io.Writer, units io.Writer, opt Options) (*Result, error) {
	lim := opt.Limits.fill()
	p := &parser{br: bufio.NewReaderSize(r, 1<<20), plain: plain, units: units, lim: lim, noModel: opt.NoSeqModel, ckEvery: opt.CheckpointEvery}
	if err := p.run(); err != nil {
		return nil, err
	}
	res := &Result{Skeleton: &p.skel, Stats: p.st, UnitSizes: p.usizes}
	if len(p.cps) > 0 {
		next := 0
		res.Raw = p.skel.marshal(func(fi, bi, off int) {
			if next < len(p.cps) && p.cps[next].Frame == fi && p.cps[next].Block == bi {
				p.cps[next].SkelOff = off
				next++
			}
		})
		res.Checkpoints = p.cps
	} else {
		res.Raw = p.skel.Marshal()
	}
	res.Stats.SkeletonBytes = len(res.Raw)
	res.Stats.FrameBytes = len(res.Raw) - res.Stats.TreeBytes - res.Stats.TableBytes - res.Stats.RankBytes - res.Stats.SkipBytes
	if int64(len(res.Raw))+int64(p.st.UnitBytes+p.st.FallbackBytes) > lim.MaxIRBytes {
		return nil, limit("description of %d bytes", len(res.Raw))
	}
	return res, nil
}

type parser struct {
	br      *bufio.Reader
	plain   io.Writer
	units   io.Writer
	usizes  []uint32
	noModel bool
	ckEvery int
	cps     []Checkpoint
	model   *matchModel // created on the first block that can use it
	window  int64
	lim     Limits
	skel    Skeleton
	st      Stats

	blocks    int
	decoded   int64
	consumed  int64
	cbuf      []byte
	lbuf      []byte
	hist      history
	irUnitSum int64
}

func (p *parser) readFull(b []byte, what string) error {
	n, err := io.ReadFull(p.br, b)
	p.consumed += int64(n)
	p.st.TotalBytes += int64(n)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return corrupt("truncated %s", what)
	}
	return err
}

func (p *parser) run() error {
	for {
		var m [4]byte
		n, err := io.ReadFull(p.br, m[:])
		p.consumed += int64(n)
		p.st.TotalBytes += int64(n)
		if err == io.EOF && len(p.skel.Frames) == 0 {
			return corrupt("empty stream")
		}
		if err == io.EOF {
			return nil
		}
		if err == io.ErrUnexpectedEOF {
			return corrupt("truncated frame magic")
		}
		if err != nil {
			return err
		}
		if len(p.skel.Frames) >= p.lim.MaxFrames {
			return limit("more than %d frames", p.lim.MaxFrames)
		}
		magic := binary.LittleEndian.Uint32(m[:])
		switch {
		case magic == magicStd:
			if err := p.frame(); err != nil {
				return err
			}
		case magic&magicSkipM == magicSkip:
			if err := p.skippable(uint8(magic & 15)); err != nil {
				return err
			}
		case magic >= 0xFD2FB522 && magic <= 0xFD2FB527 || magic == 0x1EB52FFD:
			return unsupported("legacy zstd frame %#x", magic)
		default:
			return corrupt("bad frame magic %#x", magic)
		}
	}
}

func (p *parser) skippable(low uint8) error {
	var sz [4]byte
	if err := p.readFull(sz[:], "skippable frame size"); err != nil {
		return err
	}
	n := int64(binary.LittleEndian.Uint32(sz[:]))
	if n > p.lim.MaxSkippable {
		return limit("skippable frame of %d bytes", n)
	}
	var buf bytes.Buffer
	got, err := io.Copy(&buf, io.LimitReader(p.br, n))
	p.consumed += got
	p.st.TotalBytes += got
	if err != nil {
		return err
	}
	if got != n {
		return corrupt("truncated skippable frame")
	}
	p.skel.Frames = append(p.skel.Frames, FrameRec{Skippable: true, SkipLow: low, Skip: buf.Bytes()})
	p.st.Skippable++
	p.st.SkipBytes += int(n)
	return nil
}

func (p *parser) frame() error {
	fhd, err := p.br.Peek(1)
	if err != nil {
		return corrupt("truncated frame header")
	}
	raw := make([]byte, fhdLen(fhd[0]))
	if err := p.readFull(raw, "frame header"); err != nil {
		return err
	}
	fh, err := parseFrameHeader(raw, p.lim)
	if err != nil {
		return err
	}
	f := FrameRec{Header: raw}
	p.st.Frames++
	bmax := fh.blockMax()
	p.hist.window = fh.window
	p.hist.buf = p.hist.buf[:0]
	p.hist.total = 0
	p.model, p.window = nil, fh.window
	framePlain, fidx := p.st.PlainBytes, len(p.skel.Frames)
	var st codingState
	rep := [3]uint32{1, 4, 8}
	sum := newXXH64()
	var total int64
	for last := false; !last; {
		if p.blocks++; p.blocks > p.lim.MaxBlocks {
			return limit("more than %d blocks", p.lim.MaxBlocks)
		}
		if p.ckEvery > 0 && len(f.Blocks)%p.ckEvery == 0 {
			cp := Checkpoint{Frame: fidx, Block: len(f.Blocks), FramePlain: framePlain, PlainOff: p.st.PlainBytes, CompOff: p.st.TotalBytes, UnitOff: p.irUnitSum}
			cp.snapshot(&st)
			p.cps = append(p.cps, cp)
		}
		var bh [3]byte
		if err := p.readFull(bh[:], "block header"); err != nil {
			return err
		}
		v := uint32(bh[0]) | uint32(bh[1])<<8 | uint32(bh[2])<<16
		var typ BlockType
		var size int
		last, typ, size = v&1 == 1, BlockType(v>>1&3), int(v>>3)
		if typ == 3 {
			return corrupt("reserved block type")
		}
		if size > bmax {
			return corrupt("block of %d bytes, at most %d", size, bmax)
		}
		clen := size
		if typ == BlockRLE {
			clen = 1
		}
		p.cbuf = fitBuf(p.cbuf, clen)
		if err := p.readFull(p.cbuf, "block"); err != nil {
			return err
		}
		content := p.cbuf
		p.hist.reserve(bmax)
		start := len(p.hist.buf)
		blockTotal := p.hist.total
		rec := BlockRec{Last: last, Type: typ}
		var unit []byte
		var trip []seqRec
		switch typ {
		case BlockRaw:
			p.hist.buf = append(p.hist.buf, content...)
		case BlockRLE:
			rec.RLE = content[0]
			for i := 0; i < size; i++ {
				p.hist.buf = append(p.hist.buf, content[0])
			}
		default:
			rec, trip, unit, err = p.compressed(&st, &rep, content, bmax, last)
			if err != nil {
				return err
			}
		}
		out := p.hist.buf[start:]
		rec.DecLen = uint32(len(out))
		if rec.Type == BlockFallback {
			rec.DecLen = uint32(len(out))
		}
		total += int64(len(out))
		p.decoded += int64(len(out))
		if total > p.lim.MaxFrameDecoded || p.decoded > p.lim.MaxTotalDecoded {
			return limit("decoded size")
		}
		if fh.hasFCS && total > int64(fh.fcs) {
			return corrupt("frame decodes past its declared content size %d", fh.fcs)
		}
		if p.consumed < 1<<40 && p.decoded > p.lim.MaxRatio*p.consumed+ratioSlack {
			return limit("expansion ratio: %d bytes from %d", p.decoded, p.consumed)
		}
		sum.write(out)
		if _, err := p.plain.Write(out); err != nil {
			return err
		}
		p.hist.total += int64(len(out))
		p.st.PlainBytes += int64(len(out))
		p.st.Blocks[rec.Type]++
		p.st.StreamBytes[rec.Type] += int64(3 + clen)
		if rec.HasUnit() {
			if rec.Type == BlockCompressed {
				unit = p.encodeUnit(&rec, trip, start, blockTotal)
			}
			if err := p.emit(rec, unit); err != nil {
				return err
			}
		}
		f.Blocks = append(f.Blocks, rec)
	}
	if fh.hasFCS && uint64(total) != fh.fcs {
		return corrupt("frame decodes %d bytes, header says %d", total, fh.fcs)
	}
	if fh.checksum {
		var c [4]byte
		if err := p.readFull(c[:], "content checksum"); err != nil {
			return err
		}
		f.Checksum = binary.LittleEndian.Uint32(c[:])
		if f.Checksum != uint32(sum.sum()) {
			return corrupt("content checksum mismatch")
		}
	}
	p.skel.Frames = append(p.skel.Frames, f)
	return nil
}

// encodeUnit codes a transformed block's sequences, with the match model when it is
// available and proves it decodes back; otherwise as explicit varints.
func (p *parser) encodeUnit(rec *BlockRec, trip []seqRec, start int, total int64) []byte {
	if p.model == nil && !p.noModel {
		p.model = newMatchModel(p.window)
	}
	if p.model != nil {
		end := total + int64(len(p.hist.buf)-start)
		v := &blockView{buf: p.hist.buf, base: total - int64(start), start: total, end: end}
		if u, ok := p.model.encode(v, trip); ok {
			rec.Model = true
			return u
		}
	}
	return encodeTriples(trip)
}

func equalSeqs(a, b []seqRec) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (p *parser) emit(rec BlockRec, unit []byte) error {
	if rec.Type == BlockFallback {
		p.st.FallbackBytes += len(unit)
	} else {
		p.st.UnitBytes += len(unit)
	}
	if p.irUnitSum += int64(len(unit)); p.irUnitSum > p.lim.MaxIRBytes {
		return limit("units of %d bytes", p.irUnitSum)
	}
	p.usizes = append(p.usizes, uint32(len(unit)))
	if p.units != nil {
		if _, err := p.units.Write(unit); err != nil {
			return err
		}
	}
	return nil
}

// compressed decodes one compressed block into p.hist, builds its record and unit and
// proves the record by rebuilding the original bytes.
func (p *parser) compressed(st *codingState, rep *[3]uint32, content []byte, bmax int, last bool) (BlockRec, []seqRec, []byte, error) {
	pre := *st
	h, err := scanBlock(st, content, bmax)
	if err != nil {
		return BlockRec{}, nil, nil, err
	}
	if p.st.Literals += int64(h.lit.regen); p.st.Literals > p.lim.MaxLiterals {
		return BlockRec{}, nil, nil, limit("literals")
	}
	if p.st.Sequences += int64(h.seq.n); p.st.Sequences > p.lim.MaxSequences {
		return BlockRec{}, nil, nil, limit("sequences")
	}
	lits, err := decodeLiterals(&h.lit, p.lbuf)
	if err != nil {
		return BlockRec{}, nil, nil, err
	}
	if h.lit.mode != litRaw { // raw literals alias the block buffer
		p.lbuf = lits
	}
	var trip []seqRec
	var rank [3]uint32
	if h.seq.n > 0 {
		if trip, rank, err = decodeSeqs(&h.seq); err != nil {
			return BlockRec{}, nil, nil, err
		}
	}
	start := len(p.hist.buf)
	if err := p.hist.exec(trip, lits, rep, bmax); err != nil {
		return BlockRec{}, nil, nil, err
	}
	out := p.hist.buf[start:]

	// coverage counters
	p.st.LitModes[h.lit.mode]++
	if h.lit.mode >= litComp {
		p.st.LitStreams[h.lit.streams]++
		if h.lit.mode == litComp {
			if h.lit.treeDesc[0] >= 128 {
				p.st.HuffDirect++
			} else {
				p.st.HuffFSE++
			}
		}
	}
	if h.seq.n == 0 {
		p.st.Empty++
	} else {
		for k := 0; k < 3; k++ {
			p.st.SeqModes[k][h.seq.modes>>uint(6-2*k)&3]++
		}
		for _, t := range trip {
			if t.of > 3 {
				p.st.RepeatCodes[3]++
			} else {
				p.st.RepeatCodes[t.of-1]++
				if t.ll == 0 {
					p.st.ZeroLLRepeat++
				}
			}
		}
	}

	rec := BlockRec{Last: last, Type: BlockCompressed, LitMode: h.lit.mode, LitForm: h.lit.form,
		Regen: uint32(h.lit.regen), TreeDesc: h.lit.treeDesc, NSeq: uint32(h.seq.n),
		Modes: h.seq.modes, TabBytes: h.seq.tabBytes, Rank: rank}
	re, err := buildCompressed(&pre, &rec, out, trip)
	if err == nil && bytes.Equal(re, content) {
		p.st.TreeBytes += len(rec.TreeDesc)
		if rec.NSeq > 0 {
			p.st.TableBytes += 1 + len(rec.TabBytes)
			for _, r := range rec.Rank {
				p.st.RankBytes += len(binary.AppendUvarint(nil, uint64(r)))
			}
		}
		// the record keeps views into the shared block buffer; give it its own bytes
		rec.TreeDesc = append([]byte(nil), rec.TreeDesc...)
		rec.TabBytes = append([]byte(nil), rec.TabBytes...)
		return rec, trip, nil, nil
	}
	if err != nil && !errors.Is(err, ErrMismatch) && !errors.Is(err, ErrIR) {
		return BlockRec{}, nil, nil, err
	}
	return BlockRec{Last: last, Type: BlockFallback, CompLen: uint32(len(content))}, nil, append([]byte(nil), content...), nil
}

// ParseAt is Parse over a ReaderAt of known size; the object is read sequentially in
// bounded pieces and never held whole.
func ParseAt(r io.ReaderAt, size int64, plain io.Writer, units io.Writer, opt Options) (*Result, error) {
	return Parse(io.NewSectionReader(r, 0, size), plain, units, opt)
}
