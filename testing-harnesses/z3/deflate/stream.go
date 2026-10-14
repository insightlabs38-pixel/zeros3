package deflate

import (
	"errors"
	"fmt"
	"io"
)

// Streaming parser (Z3-08G). It decodes a raw DEFLATE stream from an io.Reader
// with bounded memory: a power-of-two plaintext ring (the 32 KiB match window
// plus whatever the consumer wants to keep), the current block's Huffman
// tables, and a read buffer. Blocks and tokens are delivered to a Handler as
// they are decoded; the full parser (Parse) remains the reference oracle.

// Window is a power-of-two ring of the most recent plaintext. Positions are
// absolute stream offsets.
type Window struct {
	buf  []byte
	mask int64
	n    int64 // total bytes written
	tmp  []byte
}

// NewWindow returns a ring holding at least size bytes (rounded up to a power of two, minimum 32 KiB).
func NewWindow(size int) *Window {
	sz := 1 << 15
	for sz < size {
		sz <<= 1
	}
	return &Window{buf: make([]byte, sz), mask: int64(sz - 1)}
}

// Size is the capacity of the ring in bytes.
func (w *Window) Size() int { return len(w.buf) }

// Len is the total number of bytes written.
func (w *Window) Len() int64 { return w.n }

// Put appends one byte.
func (w *Window) Put(c byte) {
	w.buf[w.n&w.mask] = c
	w.n++
}

// Write appends p.
func (w *Window) Write(p []byte) {
	for len(p) > 0 {
		i := int(w.n & w.mask)
		k := copy(w.buf[i:], p)
		p = p[k:]
		w.n += int64(k)
	}
}

// Copy appends length bytes taken d bytes back (overlap allowed). d must not
// exceed Len() or the ring size.
func (w *Window) Copy(d, length int) {
	for i := 0; i < length; i++ {
		w.buf[w.n&w.mask] = w.buf[(w.n-int64(d))&w.mask]
		w.n++
	}
}

// Read returns the bytes [from, to) as one slice, valid until the next call.
// The range must lie inside the ring (to-from <= Size, from >= Len-Size).
func (w *Window) Read(from, to int64) []byte {
	if from < 0 || to < from || to > w.n || to-from > int64(len(w.buf)) || from < w.n-int64(len(w.buf)) {
		panic(fmt.Sprintf("deflate: window read [%d,%d) outside ring (len %d, size %d)", from, to, w.n, len(w.buf)))
	}
	n := int(to - from)
	i := int(from & w.mask)
	if i+n <= len(w.buf) {
		return w.buf[i : i+n]
	}
	if cap(w.tmp) < n {
		w.tmp = make([]byte, n)
	}
	w.tmp = w.tmp[:n]
	k := copy(w.tmp, w.buf[i:])
	copy(w.tmp[k:], w.buf[:n-k])
	return w.tmp
}

// Handler receives the decoded structure in stream order.
type Handler interface {
	// BeginBlock: Final, Kind and (dynamic) the exact header fields are set;
	// Tokens is nil. The pointer is only valid during the call.
	BeginBlock(b *Block) error
	// Token delivers one literal or match; its plaintext is already in the window.
	Token(t Token) error
	// StoredData delivers a chunk of a stored block's payload (already in the window).
	StoredData(p []byte) error
	// EndBlock: tokens is the number of tokens of a fixed/dynamic block.
	EndBlock(b *Block, tokens int) error
}

// StreamResult is what is known after a stream ends.
type StreamResult struct {
	Blocks                  int
	Tokens                  int64
	Plain                   int64
	TailPadBits, TailPadLen uint8
	Consumed                int64 // bytes of the DEFLATE stream (through the byte holding the final bit)
	Rest                    []byte
}

// StreamParse decodes the raw DEFLATE stream at the start of r. win receives
// the plaintext; its capacity must be at least 32 KiB. After the stream the
// bytes already read from r but not part of it are in Rest (read the remainder
// of r for the rest).
func StreamParse(r io.Reader, win *Window, h Handler, opt Options) (*StreamResult, error) {
	opt = opt.norm()
	initFixed()
	p := &parser{br: bitReader{src: r, data: make([]byte, 0, 64<<10)}, opt: opt}
	res := &StreamResult{}
	for {
		if res.Blocks >= opt.MaxBlocks {
			return nil, fmt.Errorf("%w: more than %d blocks", ErrLimit, opt.MaxBlocks)
		}
		start := p.br.bitPos()
		hdr, err := p.br.bits(3)
		if err != nil {
			return nil, streamErr(&p.br, err)
		}
		b := Block{Final: hdr&1 == 1, Kind: BlockKind(hdr >> 1), StartBit: start, PlainOff: int(win.Len())}
		ntok := 0
		switch b.Kind {
		case Stored:
			err = streamStored(p, win, h, &b)
		case Fixed:
			if err = h.BeginBlock(&b); err == nil {
				ntok, err = streamTokens(p, win, h, fixedLit, fixedDist, res)
			}
		case Dynamic:
			var lit, dist *decoder
			if lit, dist, err = p.dynamicHeader(&b); err == nil {
				if err = h.BeginBlock(&b); err == nil {
					ntok, err = streamTokens(p, win, h, lit, dist, res)
				}
			}
		default:
			return nil, corrupt(start, "reserved block type 3")
		}
		if err != nil {
			return nil, streamErr(&p.br, err)
		}
		b.EndBit = p.br.bitPos()
		b.PlainLen = int(win.Len()) - b.PlainOff
		if err := h.EndBlock(&b, ntok); err != nil {
			return nil, err
		}
		res.Blocks++
		if b.Final {
			break
		}
	}
	n := uint((8 - p.br.bitPos()%8) % 8)
	v, err := p.br.bits(n)
	if err != nil {
		return nil, streamErr(&p.br, err)
	}
	res.TailPadBits, res.TailPadLen = uint8(v), uint8(n)
	res.Consumed = (p.br.bitPos() + 7) / 8
	res.Plain = win.Len()
	res.Rest = p.br.leftover()
	return res, nil
}

func streamErr(br *bitReader, err error) error {
	if errors.Is(err, ErrTruncated) && br.rerr != nil && !errors.Is(br.rerr, io.EOF) {
		return br.rerr
	}
	return err
}

func streamStored(p *parser, win *Window, h Handler, b *Block) error {
	n := uint((8 - p.br.bitPos()%8) % 8)
	v, err := p.br.bits(n)
	if err != nil {
		return err
	}
	b.PadBits, b.PadLen = uint8(v), uint8(n)
	var hdr [4]byte
	k := 0
	if err := p.br.readStream(4, func(c []byte) error { k += copy(hdr[k:], c); return nil }); err != nil {
		return err
	}
	ln := int(hdr[0]) | int(hdr[1])<<8
	if nln := int(hdr[2]) | int(hdr[3])<<8; ln != ^nln&0xffff {
		return corrupt(p.br.bitPos(), "stored LEN/NLEN mismatch")
	}
	b.PlainLen = ln
	if err := h.BeginBlock(b); err != nil {
		return err
	}
	return p.br.readStream(ln, func(c []byte) error {
		win.Write(c)
		return h.StoredData(c)
	})
}

func streamTokens(p *parser, win *Window, h Handler, lit, dist *decoder, res *StreamResult) (int, error) {
	br := &p.br
	n := 0
	for {
		sym, err := lit.decode(br)
		if err != nil {
			return n, err
		}
		if sym == 256 {
			return n, nil
		}
		n++
		res.Tokens++
		if sym < 256 {
			win.Put(byte(sym))
			if err := h.Token(Literal(byte(sym))); err != nil {
				return n, err
			}
			continue
		}
		if sym > 285 {
			return n, corrupt(br.bitPos(), "invalid length symbol")
		}
		le, err := br.bits(uint(lenExtra[sym-257]))
		if err != nil {
			return n, err
		}
		dc, err := dist.decode(br)
		if err != nil {
			return n, err
		}
		if dc > 29 {
			return n, corrupt(br.bitPos(), "invalid distance symbol")
		}
		de, err := br.bits(uint(distExtr[dc]))
		if err != nil {
			return n, err
		}
		t := Match(sym, le, dc, de)
		d := t.Distance()
		if int64(d) > win.Len() {
			return n, corrupt(br.bitPos(), "distance beyond start of output")
		}
		win.Copy(d, t.Length())
		if err := h.Token(t); err != nil {
			return n, err
		}
	}
}

// GzipHeaderLen reads one gzip member header from r and returns its exact bytes.
func GzipHeader(r io.Reader) ([]byte, error) {
	var hdr [10]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	if hdr[0] != 0x1f || hdr[1] != 0x8b || hdr[2] != 8 {
		return nil, fmt.Errorf("%w: not a gzip member", ErrCorrupt)
	}
	out := append([]byte(nil), hdr[:]...)
	flg := hdr[3]
	if flg&0xe0 != 0 {
		return nil, fmt.Errorf("%w: reserved gzip flags", ErrCorrupt)
	}
	one := make([]byte, 1)
	readZ := func() error { // zero-terminated string, bounded
		for i := 0; i < 1<<16; i++ {
			if _, err := io.ReadFull(r, one); err != nil {
				return err
			}
			out = append(out, one[0])
			if one[0] == 0 {
				return nil
			}
		}
		return fmt.Errorf("%w: gzip header string too long", ErrLimit)
	}
	if flg&4 != 0 {
		var l [2]byte
		if _, err := io.ReadFull(r, l[:]); err != nil {
			return nil, err
		}
		out = append(out, l[:]...)
		x := make([]byte, int(l[0])|int(l[1])<<8)
		if _, err := io.ReadFull(r, x); err != nil {
			return nil, err
		}
		out = append(out, x...)
	}
	if flg&8 != 0 {
		if err := readZ(); err != nil {
			return nil, err
		}
	}
	if flg&16 != 0 {
		if err := readZ(); err != nil {
			return nil, err
		}
	}
	if flg&2 != 0 {
		var c [2]byte
		if _, err := io.ReadFull(r, c[:]); err != nil {
			return nil, err
		}
		out = append(out, c[:]...)
	}
	return out, nil
}
