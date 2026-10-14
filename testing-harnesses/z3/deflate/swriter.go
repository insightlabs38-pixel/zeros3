package deflate

import (
	"fmt"
	"io"
)

// StreamWriter writes the exact DEFLATE bits of a stream block by block,
// without holding the stream: the streaming counterpart of Stream.Replay.
type StreamWriter struct {
	bw     *bitWriter
	le, de *encoder
	kind   BlockKind
	open   bool
	blocks int
	bytes  int64 // bytes handed to the sink or buffered
}

// NewStreamWriter writes to w.
func NewStreamWriter(w io.Writer) *StreamWriter {
	initFixed()
	s := &StreamWriter{}
	s.bw = &bitWriter{flush: func(b []byte) error { s.bytes += int64(len(b)); _, err := w.Write(b); return err }}
	return s
}

// NewStreamWriterAt writes as if startBit bits of the stream had already been
// written (they read as zero placeholders): a range materializer's entry point.
func NewStreamWriterAt(w io.Writer, startBit int64) *StreamWriter {
	s := NewStreamWriter(w)
	s.bytes = startBit / 8
	s.bw.nacc = uint(startBit % 8)
	return s
}

// Resume continues a block whose header lies before the starting position; b
// supplies its code tables (Kind, and for dynamic blocks LitLens/DistLens).
func (s *StreamWriter) Resume(b *Block) error {
	if s.open {
		return fmt.Errorf("%w: block already open", ErrInvalidIR)
	}
	s.kind = b.Kind
	switch b.Kind {
	case Fixed:
		s.le, s.de = fixedLitE, fixedDstE
	case Dynamic:
		if len(b.LitLens) < 257 || len(b.DistLens) == 0 {
			return fmt.Errorf("%w: dynamic tables missing", ErrInvalidIR)
		}
		s.le, s.de = buildEncoder(b.LitLens), buildEncoder(b.DistLens)
	default:
		return fmt.Errorf("%w: cannot resume a %s block", ErrInvalidIR, b.Kind)
	}
	s.open = true
	return nil
}

// Drain hands every completed byte written so far to the sink.
func (s *StreamWriter) Drain() error {
	s.bw.drain()
	return s.bw.err
}

// BitPos is the number of bits written so far.
func (s *StreamWriter) BitPos() int64 { return (s.bytes+int64(len(s.bw.buf)))*8 + int64(s.bw.nacc) }

// Begin writes the block header. For a dynamic block b must carry the exact
// header fields (HLIT..CLSyms, LitLens, DistLens); for a stored block the
// payload follows in Stored. b.Final selects BFINAL.
func (s *StreamWriter) Begin(b *Block) error {
	if s.open {
		return fmt.Errorf("%w: block already open", ErrInvalidIR)
	}
	hdr := uint32(b.Kind) << 1
	if b.Final {
		hdr |= 1
	}
	s.bw.put(hdr, 3)
	var err error
	s.kind = b.Kind
	switch b.Kind {
	case Stored:
		return nil // closed by Stored
	case Fixed:
		s.le, s.de = fixedLitE, fixedDstE
	case Dynamic:
		s.le, s.de, err = replayDynamicHeader(s.bw, b)
	default:
		err = fmt.Errorf("%w: block kind %d", ErrInvalidIR, b.Kind)
	}
	s.open = err == nil
	return err
}

// Stored writes a whole stored block's padding, length and payload (after Begin).
func (s *StreamWriter) Stored(b *Block, data []byte) error {
	if s.kind != Stored {
		return fmt.Errorf("%w: not a stored block", ErrInvalidIR)
	}
	if err := replayStored(s.bw, b, data); err != nil {
		return err
	}
	s.blocks++
	return s.bw.err
}

// Token writes one literal or match of the open block.
func (s *StreamWriter) Token(t Token) error {
	if !s.open {
		return fmt.Errorf("%w: no open block", ErrInvalidIR)
	}
	le, de := s.le, s.de
	sym := t.Sym()
	if sym > 285 || sym == 256 || sym >= len(le.len) || le.len[sym] == 0 {
		return fmt.Errorf("%w: token symbol %d has no code", ErrInvalidIR, sym)
	}
	s.bw.put(uint32(le.code[sym]), uint(le.len[sym]))
	if sym >= 256 {
		s.bw.put(t.LenExtra(), uint(lenExtra[sym-257]))
		dc := t.DistCode()
		if dc > 29 || dc >= len(de.len) || de.len[dc] == 0 {
			return fmt.Errorf("%w: distance code %d has no code", ErrInvalidIR, dc)
		}
		s.bw.put(uint32(de.code[dc]), uint(de.len[dc]))
		s.bw.put(t.DistExtra(), uint(distExtr[dc]))
	}
	return s.bw.err
}

// End writes the end-of-block code.
func (s *StreamWriter) End() error {
	if !s.open {
		return fmt.Errorf("%w: no open block", ErrInvalidIR)
	}
	if s.le.len[256] == 0 {
		return fmt.Errorf("%w: no end-of-block code", ErrInvalidIR)
	}
	s.bw.put(uint32(s.le.code[256]), uint(s.le.len[256]))
	s.open = false
	s.blocks++
	return s.bw.err
}

// Finish writes the tail padding and flushes. The padding length must be the
// one that byte-aligns the stream.
func (s *StreamWriter) Finish(padBits, padLen uint8) error {
	if s.open {
		return fmt.Errorf("%w: block still open", ErrInvalidIR)
	}
	if want := (8 - s.bw.nacc%8) % 8; uint(padLen) != want {
		return fmt.Errorf("%w: tail padding %d bits, need %d", ErrInvalidIR, padLen, want)
	}
	s.bw.put(uint32(padBits), uint(padLen))
	s.bw.drain()
	return s.bw.err
}
