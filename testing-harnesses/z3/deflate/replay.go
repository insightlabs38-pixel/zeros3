package deflate

import (
	"bytes"
	"fmt"
	"io"
)

// Bytes replays the stream into memory.
func (s *Stream) Bytes() ([]byte, error) {
	var b bytes.Buffer
	if err := s.Replay(&b); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Replay writes the exact original DEFLATE bytes to w, in chunks of at most
// about 32 KiB of working buffer.
func (s *Stream) Replay(w io.Writer) error {
	initFixed()
	bw := &bitWriter{flush: func(b []byte) error { _, err := w.Write(b); return err }}
	if len(s.Blocks) == 0 || !s.Blocks[len(s.Blocks)-1].Final {
		return fmt.Errorf("%w: no final block", ErrInvalidIR)
	}
	for i := range s.Blocks {
		b := &s.Blocks[i]
		if b.Final != (i == len(s.Blocks)-1) {
			return fmt.Errorf("%w: block %d final flag", ErrInvalidIR, i)
		}
		if b.PlainOff < 0 || b.PlainLen < 0 || b.PlainOff+b.PlainLen > len(s.Plain) {
			return fmt.Errorf("%w: block %d plaintext span", ErrInvalidIR, i)
		}
		hdr := uint32(b.Kind) << 1
		if b.Final {
			hdr |= 1
		}
		bw.put(hdr, 3)
		var err error
		switch b.Kind {
		case Stored:
			err = replayStored(bw, b, s.Plain[b.PlainOff:b.PlainOff+b.PlainLen])
		case Fixed:
			err = replayTokens(bw, b, fixedLitE, fixedDstE)
		case Dynamic:
			var le, de *encoder
			if le, de, err = replayDynamicHeader(bw, b); err == nil {
				err = replayTokens(bw, b, le, de)
			}
		default:
			err = fmt.Errorf("%w: block kind %d", ErrInvalidIR, b.Kind)
		}
		if err != nil {
			return err
		}
		if bw.err != nil {
			return bw.err
		}
	}
	if want := (8 - bw.nacc%8) % 8; uint(s.TailPadLen) != want {
		return fmt.Errorf("%w: tail padding %d bits, need %d", ErrInvalidIR, s.TailPadLen, want)
	}
	bw.put(uint32(s.TailPadBits), uint(s.TailPadLen))
	if bw.nacc != 0 {
		return fmt.Errorf("%w: unaligned tail", ErrInvalidIR)
	}
	bw.drain()
	return bw.err
}

func replayStored(bw *bitWriter, b *Block, data []byte) error {
	if len(data) > 0xffff {
		return fmt.Errorf("%w: stored block of %d bytes", ErrInvalidIR, len(data))
	}
	if b.PadLen != uint8((8-bw.nacc%8)%8) {
		return fmt.Errorf("%w: stored padding length %d", ErrInvalidIR, b.PadLen)
	}
	bw.put(uint32(b.PadBits), uint(b.PadLen))
	n := uint32(len(data))
	bw.put(n|(^n&0xffff)<<16, 32)
	bw.bytes(data)
	return nil
}

func replayDynamicHeader(bw *bitWriter, b *Block) (le, de *encoder, err error) {
	nlit, ndist, nclen := int(b.HLIT)+257, int(b.HDIST)+1, int(b.HCLEN)+4
	if b.HLIT > 29 || b.HDIST > 29 || b.HCLEN > 15 || len(b.LitLens) != nlit || len(b.DistLens) != ndist {
		return nil, nil, fmt.Errorf("%w: dynamic header sizes", ErrInvalidIR)
	}
	bw.put(uint32(b.HLIT)|uint32(b.HDIST)<<5|uint32(b.HCLEN)<<10, 14)
	for i := 0; i < nclen; i++ {
		l := b.CLLens[clOrder[i]]
		if l > 7 {
			return nil, nil, fmt.Errorf("%w: code-length code length %d", ErrInvalidIR, l)
		}
		bw.put(uint32(l), 3)
	}
	for i := nclen; i < 19; i++ {
		if b.CLLens[clOrder[i]] != 0 {
			return nil, nil, fmt.Errorf("%w: untransmitted code-length code length set", ErrInvalidIR)
		}
	}
	ce := buildEncoder(b.CLLens[:])
	total := 0
	for _, cs := range b.CLSyms {
		if cs.Sym > 18 || ce.len[cs.Sym] == 0 {
			return nil, nil, fmt.Errorf("%w: code-length symbol %d has no code", ErrInvalidIR, cs.Sym)
		}
		bw.put(uint32(ce.code[cs.Sym]), uint(ce.len[cs.Sym]))
		switch {
		case cs.Sym < 16:
			total++
		case cs.Sym == 16:
			bw.put(uint32(cs.Extra), 2)
			total += 3 + int(cs.Extra)
		case cs.Sym == 17:
			bw.put(uint32(cs.Extra), 3)
			total += 3 + int(cs.Extra)
		default:
			bw.put(uint32(cs.Extra), 7)
			total += 11 + int(cs.Extra)
		}
	}
	if total != nlit+ndist {
		return nil, nil, fmt.Errorf("%w: code-length sequence covers %d of %d lengths", ErrInvalidIR, total, nlit+ndist)
	}
	return buildEncoder(b.LitLens), buildEncoder(b.DistLens), nil
}

func replayTokens(bw *bitWriter, b *Block, le, de *encoder) error {
	for _, t := range b.Tokens {
		sym := t.Sym()
		if sym > 285 || sym == 256 || sym >= len(le.len) || le.len[sym] == 0 {
			return fmt.Errorf("%w: token symbol %d has no code", ErrInvalidIR, sym)
		}
		bw.put(uint32(le.code[sym]), uint(le.len[sym]))
		if sym < 256 {
			continue
		}
		li := sym - 257
		bw.put(t.LenExtra(), uint(lenExtra[li]))
		dc := t.DistCode()
		if dc > 29 || dc >= len(de.len) || de.len[dc] == 0 {
			return fmt.Errorf("%w: distance code %d has no code", ErrInvalidIR, dc)
		}
		bw.put(uint32(de.code[dc]), uint(de.len[dc]))
		bw.put(t.DistExtra(), uint(distExtr[dc]))
	}
	if le.len[256] == 0 {
		return fmt.Errorf("%w: no end-of-block code", ErrInvalidIR)
	}
	bw.put(uint32(le.code[256]), uint(le.len[256]))
	return nil
}
