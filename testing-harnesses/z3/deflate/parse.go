package deflate

import "fmt"

// Parse decodes the raw DEFLATE stream at the start of data.
func Parse(data []byte, opt Options) (*Stream, error) {
	opt = opt.norm()
	initFixed()
	p := &parser{br: bitReader{data: data}, opt: opt, s: &Stream{}}
	if err := p.run(); err != nil {
		return nil, err
	}
	return p.s, nil
}

type parser struct {
	br     bitReader
	opt    Options
	s      *Stream
	tokens int
}

func (p *parser) run() error {
	s := p.s
	for {
		if len(s.Blocks) >= p.opt.MaxBlocks {
			return fmt.Errorf("%w: more than %d blocks", ErrLimit, p.opt.MaxBlocks)
		}
		start := p.br.bitPos()
		hdr, err := p.br.bits(3)
		if err != nil {
			return err
		}
		b := Block{Final: hdr&1 == 1, Kind: BlockKind(hdr >> 1), StartBit: start, PlainOff: len(s.Plain)}
		switch b.Kind {
		case Stored:
			err = p.stored(&b)
		case Fixed:
			err = p.huffBlock(&b, fixedLit, fixedDist)
		case Dynamic:
			var lit, dist *decoder
			if lit, dist, err = p.dynamicHeader(&b); err == nil {
				err = p.huffBlock(&b, lit, dist)
			}
		default:
			return corrupt(start, "reserved block type 3")
		}
		if err != nil {
			return err
		}
		b.EndBit = p.br.bitPos()
		b.PlainLen = len(s.Plain) - b.PlainOff
		s.Blocks = append(s.Blocks, b)
		if b.Final {
			break
		}
	}
	n := uint((8 - p.br.bitPos()%8) % 8)
	v, err := p.br.bits(n)
	if err != nil {
		return err
	}
	s.TailPadBits, s.TailPadLen = uint8(v), uint8(n)
	s.Consumed = int((p.br.bitPos() + 7) / 8)
	if !p.opt.AllowTrailing && s.Consumed != len(p.br.data) {
		return fmt.Errorf("%w: %d bytes", ErrTrailing, len(p.br.data)-s.Consumed)
	}
	return nil
}

func (p *parser) stored(b *Block) error {
	n := uint((8 - p.br.bitPos()%8) % 8)
	v, err := p.br.bits(n)
	if err != nil {
		return err
	}
	b.PadBits, b.PadLen = uint8(v), uint8(n)
	hdr, err := p.br.readBytes(4)
	if err != nil {
		return err
	}
	ln := int(hdr[0]) | int(hdr[1])<<8
	nln := int(hdr[2]) | int(hdr[3])<<8
	if ln != ^nln&0xffff {
		return corrupt(p.br.bitPos(), "stored LEN/NLEN mismatch")
	}
	if len(p.s.Plain)+ln > p.opt.MaxOutput {
		return fmt.Errorf("%w: output over %d bytes", ErrLimit, p.opt.MaxOutput)
	}
	raw, err := p.br.readBytes(ln)
	if err != nil {
		return err
	}
	p.s.Plain = append(p.s.Plain, raw...)
	return nil
}

func (p *parser) dynamicHeader(b *Block) (lit, dist *decoder, err error) {
	br := &p.br
	hs := br.bitPos()
	f, err := br.bits(14)
	if err != nil {
		return nil, nil, err
	}
	b.HLIT, b.HDIST, b.HCLEN = uint8(f&31), uint8(f>>5&31), uint8(f>>10&15)
	nlit, ndist, nclen := int(b.HLIT)+257, int(b.HDIST)+1, int(b.HCLEN)+4
	if nlit > 286 || ndist > 30 {
		return nil, nil, corrupt(hs, "too many length or distance symbols")
	}
	for i := 0; i < nclen; i++ {
		v, err := br.bits(3)
		if err != nil {
			return nil, nil, err
		}
		b.CLLens[clOrder[i]] = uint8(v)
	}
	cl, ok := buildDecoder(b.CLLens[:])
	if !ok || cl.empty {
		return nil, nil, corrupt(hs, "invalid code-length code")
	}
	lens := make([]uint8, 0, nlit+ndist)
	for len(lens) < nlit+ndist {
		sym, err := cl.decode(br)
		if err != nil {
			return nil, nil, err
		}
		cs := CLSym{Sym: uint8(sym)}
		if sym < 16 {
			lens = append(lens, uint8(sym))
			b.CLSyms = append(b.CLSyms, cs)
			continue
		}
		var base, nb uint
		var val uint8
		switch sym {
		case 16:
			if len(lens) == 0 {
				return nil, nil, corrupt(br.bitPos(), "repeat with no previous length")
			}
			base, nb, val = 3, 2, lens[len(lens)-1]
		case 17:
			base, nb = 3, 3
		case 18:
			base, nb = 11, 7
		}
		x, err := br.bits(nb)
		if err != nil {
			return nil, nil, err
		}
		cs.Extra = uint8(x)
		rep := int(base) + int(x)
		if len(lens)+rep > nlit+ndist {
			return nil, nil, corrupt(br.bitPos(), "repeat overruns code lengths")
		}
		for j := 0; j < rep; j++ {
			lens = append(lens, val)
		}
		b.CLSyms = append(b.CLSyms, cs)
	}
	b.LitLens, b.DistLens = lens[:nlit:nlit], lens[nlit:]
	if b.LitLens[256] == 0 {
		return nil, nil, corrupt(hs, "no end-of-block code")
	}
	var ok1, ok2 bool
	if lit, ok1 = buildDecoder(b.LitLens); !ok1 || lit.empty {
		return nil, nil, corrupt(hs, "invalid literal/length code")
	}
	if dist, ok2 = buildDecoder(b.DistLens); !ok2 {
		return nil, nil, corrupt(hs, "invalid distance code")
	}
	b.HeaderBits = int(br.bitPos() - hs)
	return lit, dist, nil
}

func (p *parser) huffBlock(b *Block, lit, dist *decoder) error {
	br := &p.br
	s := p.s
	for {
		sym, err := lit.decode(br)
		if err != nil {
			return err
		}
		if sym == 256 {
			return nil
		}
		if p.tokens >= p.opt.MaxTokens {
			return fmt.Errorf("%w: more than %d tokens", ErrLimit, p.opt.MaxTokens)
		}
		if sym < 256 {
			if len(s.Plain) >= p.opt.MaxOutput {
				return fmt.Errorf("%w: output over %d bytes", ErrLimit, p.opt.MaxOutput)
			}
			s.Plain = append(s.Plain, byte(sym))
			b.Tokens = append(b.Tokens, Literal(byte(sym)))
			p.tokens++
			continue
		}
		if sym > 285 {
			return corrupt(br.bitPos(), "invalid length symbol")
		}
		li := sym - 257
		le, err := br.bits(uint(lenExtra[li]))
		if err != nil {
			return err
		}
		dc, err := dist.decode(br)
		if err != nil {
			return err
		}
		if dc > 29 {
			return corrupt(br.bitPos(), "invalid distance symbol")
		}
		de, err := br.bits(uint(distExtr[dc]))
		if err != nil {
			return err
		}
		t := Match(sym, le, dc, de)
		length, d := t.Length(), t.Distance()
		if d > len(s.Plain) {
			return corrupt(br.bitPos(), "distance beyond start of output")
		}
		if len(s.Plain)+length > p.opt.MaxOutput {
			return fmt.Errorf("%w: output over %d bytes", ErrLimit, p.opt.MaxOutput)
		}
		s.Plain = appendMatch(s.Plain, d, length)
		b.Tokens = append(b.Tokens, t)
		p.tokens++
	}
}

// appendMatch copies length bytes from distance d back, handling overlap.
func appendMatch(plain []byte, d, length int) []byte {
	from := len(plain) - d
	if d >= length {
		return append(plain, plain[from:from+length]...)
	}
	for i := 0; i < length; i++ {
		plain = append(plain, plain[from+i])
	}
	return plain
}
