// Package brotliz3 is an exact Brotli substrate (standard library only): a bounded RFC 7932
// decoder and a faithful port of the google/brotli reference encoder for qualities 5-9 and windows
// of 2^17..2^24 bytes (generic mode): the one-shot BrotliEncoderCompress and the streaming
// BrotliEncoderCompressStream (process, flush, finish) with its ring buffer, size hint and block
// size. The port reproduces the reference output bit for bit, which turns "re-encode and compare"
// into a replay recipe: a consumer stores the decoded bytes plus the producer's parameters and
// regenerates the producer's compressed bytes. Outputs are always verified by the caller against
// stored bytes.
package brotliz3

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"sync/atomic"
)

var (
	ErrUnsupported = errors.New("brotliz3: parameters outside the ported encoder path")
	ErrDictionary  = errors.New("brotliz3: static dictionary not installed or wrong")
	ErrCorrupt     = errors.New("brotliz3: corrupt stream")
	ErrLimit       = errors.New("brotliz3: output limit exceeded")
)

type encDict struct {
	data        []byte
	hashWords   [32768]uint16
	hashLengths [32768]uint8
	offsets     [32]uint32
	sizeBits    [32]uint8
}

var (
	dictOffsets  = [32]uint32{0, 0, 0, 0, 0, 4096, 9216, 21504, 35840, 44032, 53248, 63488, 74752, 87040, 93696, 100864, 104704, 106752, 108928, 113536, 115968, 118528, 119872, 121280, 122016, 122784, 122784, 122784, 122784, 122784, 122784, 122784}
	dictSizeBits = [32]uint8{0, 0, 0, 0, 10, 10, 11, 11, 10, 10, 10, 10, 10, 9, 9, 8, 7, 7, 8, 7, 7, 6, 6, 5, 5}
	theDict      atomic.Pointer[encDict]
)

// SetDictionary installs the RFC 7932 static dictionary (122784 bytes, DictionarySHA256). The
// core carries no data; brotliz3/brdict embeds the file for programs that want it linked in.
func SetDictionary(b []byte) error {
	sum := sha256.Sum256(b)
	if len(b) != 122784 || hex.EncodeToString(sum[:]) != DictionarySHA256 {
		return ErrDictionary
	}
	d := &encDict{data: append([]byte(nil), b...), offsets: dictOffsets, sizeBits: dictSizeBits}
	// BrotliEncoderInitDictionaryHash
	global := 0
	for l := 24; l >= 4; l-- {
		lt8 := uint32(0)
		if l < 8 {
			lt8 = 1
		}
		n := 1 << d.sizeBits[l]
		words := d.data[d.offsets[l]:]
		for i := 0; i < n; i++ {
			j := n - 1 - i
			key := (binary.LittleEndian.Uint32(words[l*j:]) * hashMul32) >> 18
			idx := key<<1 + lt8
			if d.hashLengths[idx]&0x80 == 0 {
				d.hashWords[idx] = uint16(j)
				v := uint8(l)
				if dictFrozenIdx[global/8]&(1<<uint(global%8)) != 0 {
					v |= 0x80
				}
				d.hashLengths[idx] = v
			}
			global++
		}
	}
	for i := range d.hashLengths {
		d.hashLengths[i] &= 0x7F
	}
	theDict.Store(d)
	return nil
}

// maxStream is the longest stream the port models: positions stay below 2^30, where the reference
// does not wrap them (WrapPosition is the identity).
const maxStream = 1 << 30

// Params are the encoder parameters a streaming producer sets (BrotliEncoderSetParameter).
type Params struct {
	Quality  int    // 5..9
	LgWin    int    // 17..24
	LgBlock  int    // 0 = automatic, else 16..24
	SizeHint uint32 // 0 = derived from the first encoded block as the reference does
	Legacy   bool   // libbrotli <= 1.1.0; false: 1.2.0. They differ where a probe reaches the end of the ring
}

// Op is a BrotliEncoderOperation.
type Op uint8

const (
	OpProcess Op = iota
	OpFlush
	OpFinish
)

// ringBuf is the reference RingBuffer: the last two bytes, 1<<window_bits data bytes, a copy of the
// beginning as a tail, and seven bytes of slack. Positions are absolute, bytes live at pos&mask.
type ringBuf struct {
	size, mask, tail, total, cur uint32
	pos                          uint32
	data                         []byte
	buf                          []byte // data[2:]
}

func (r *ringBuf) initBuffer(n uint32) {
	nd := make([]byte, 2+int(n)+16)
	if r.data != nil {
		copy(nd, r.data[:min(len(r.data), 2+int(r.cur)+7)])
	}
	r.data, r.cur, r.buf = nd, n, nd[2:]
	nd[0], nd[1] = 0, 0
	for i := 0; i < 7; i++ {
		r.buf[int(n)+i] = 0
	}
}

func (r *ringBuf) write(b []byte) {
	n := uint32(len(b))
	if r.pos == 0 && n < r.tail {
		r.pos = n
		r.initBuffer(n)
		copy(r.buf, b)
		return
	}
	if r.cur < r.total {
		// Inside the first lap nothing past the data is ever read (probes stop at the end of the block), so the
		// buffer grows with the data instead of being allocated at the full window; the bytes beyond are zero
		// either way.
		want := r.total
		if r.pos&(1<<31) == 0 && uint64(r.pos)+uint64(n) < uint64(r.size) {
			want = min(r.total, (r.pos+n+1<<16-1)&^(1<<16-1)+1<<16)
		}
		if want > r.cur {
			r.initBuffer(want)
			if want == r.total {
				r.buf[r.size-2], r.buf[r.size-1] = 0, 0
				r.buf[r.size] = 241
				// the tail mirrors the first block(s) written so far (a full-size ring keeps it current)
				copy(r.buf[r.size:], r.buf[:min(r.tail, r.pos)])
			}
		}
	}
	masked := r.pos & r.mask
	if masked < r.tail && r.cur == r.total { // RingBufferWriteTail (a ring still growing has no tail yet)
		copy(r.buf[r.size+masked:], b[:min(n, r.tail-masked)])
	}
	if masked+n <= r.size {
		copy(r.buf[masked:], b)
	} else {
		copy(r.buf[masked:], b[:min(n, r.total-masked)])
		copy(r.buf[0:], b[r.size-masked:])
	}
	notFirst := r.pos&(1<<31) != 0
	if r.cur == r.total { // the two bytes before the ring mirror its last two; a ring still growing has zeros there
		r.data[0], r.data[1] = r.buf[r.size-2], r.buf[r.size-1]
	}
	r.pos = r.pos&(1<<31-1) + n&(1<<31-1)
	if notFirst {
		r.pos |= 1 << 31
	}
}

type encoder struct {
	quality, lgwin, lgblock int
	legacy                  bool
	hint                    uint32
	rb                      ringBuf
	h                       *hasher
	distCache               [16]int32
	savedDist               [4]int32
	cmds                    []command
	numLiterals             int
	lastInsertLen           int
	inputPos, lastFlush     int
	lastProcessed           int
	prev1, prev2            byte
	w                       bitWriter
	lin                     []byte // contiguous copy of the metablock: two bytes of left context, then the data
	failed                  bool
	stored                  bool // the last meta-block was written uncompressed: its commands are not in the stream
	probe                   *cmdProbe
}

// MaxInput is the largest one-shot input accepted (kept for callers that size buffers by window).
func MaxInput(lgwin int) int { return 1<<uint(lgwin+1) - 64 }

func (p Params) valid() bool {
	return p.Quality >= 5 && p.Quality <= 9 && p.LgWin >= 17 && p.LgWin <= 24 && (p.LgBlock == 0 || p.LgBlock >= 16 && p.LgBlock <= 24)
}

// lgBlock is ComputeLgBlock for qualities 5..9.
func (p Params) lgBlock() int {
	if p.LgBlock == 0 {
		if p.Quality >= 9 && p.LgWin > 16 {
			return min(18, p.LgWin)
		}
		return 16
	}
	return p.LgBlock
}

func newEncoder(p Params) *encoder {
	e := &encoder{quality: p.Quality, lgwin: p.LgWin, lgblock: p.lgBlock(), hint: p.SizeHint, legacy: p.Legacy}
	bits := uint(1 + max(e.lgwin, e.lgblock))
	e.rb.size, e.rb.mask, e.rb.tail = 1<<bits, 1<<bits-1, 1<<uint(e.lgblock)
	e.rb.total = e.rb.size + e.rb.tail
	e.distCache = [16]int32{4, 11, 15, 16}
	e.savedDist = [4]int32{4, 11, 15, 16}
	// EncodeWindowBits
	if e.lgwin == 17 {
		e.w.write(7, 1)
	} else {
		e.w.write(4, uint64((e.lgwin-17)<<1|1))
	}
	return e
}

func (e *encoder) block() int { return 1 << uint(e.lgblock) }

func (e *encoder) unprocessed() int { return e.inputPos - e.lastProcessed }

func (e *encoder) remainingBlock() int {
	if d := e.unprocessed(); d < e.block() {
		return e.block() - d
	}
	return 0
}

func (e *encoder) copyInput(b []byte) {
	e.rb.write(b)
	e.inputPos += len(b)
	if int(e.rb.pos) <= int(e.rb.mask) { // first lap: the seven bytes after the data are defined zeros
		for i := 0; i < 7; i++ {
			e.rb.buf[int(e.rb.pos)+i] = 0
		}
	}
}

// updateSizeHint is UpdateSizeHint: a producer that set no hint gets the pending input as its hint.
func (e *encoder) updateSizeHint(availableIn int) {
	if e.hint == 0 {
		const limit = 1 << 30
		d, t := e.unprocessed(), availableIn
		if d >= limit || t >= limit || d+t >= limit {
			e.hint = limit
		} else {
			e.hint = uint32(d + t)
		}
	}
}

// Stream is BrotliEncoderCompressStream: input is accepted in any chunking, output is written to the sink
// as complete bytes. Memory is bounded by the window (ring buffer, hash tables and one meta-block).
type Stream struct {
	e    *encoder
	out  io.Writer
	err  error
	done bool
	n    int64
}

// NewStream starts a stream; the sink receives the compressed bytes as they become complete.
func NewStream(p Params, out io.Writer) (*Stream, error) {
	if !p.valid() {
		return nil, ErrUnsupported
	}
	if theDict.Load() == nil {
		return nil, ErrDictionary
	}
	return &Stream{e: newEncoder(p), out: out}, nil
}

// Close releases the hash tables; the stream cannot be used afterwards.
func (s *Stream) Close() {
	if s.e.h != nil {
		s.e.h.release()
		s.e.h = nil
	}
}

func (s *Stream) emit() {
	if s.err != nil || len(s.e.w.buf) == 0 {
		return
	}
	_, s.err = s.out.Write(s.e.w.buf)
	s.e.w.buf = s.e.w.buf[:0]
}

// Process is OpProcess, Flush OpFlush, Finish OpFinish of the reference.
func (s *Stream) Process(b []byte) error { return s.run(OpProcess, b) }
func (s *Stream) Flush() error           { return s.run(OpFlush, nil) }
func (s *Stream) Finish() error          { return s.run(OpFinish, nil) }

// Write feeds a chunk with the given operation (the chunk is consumed as one BrotliEncoderCompressStream call).
func (s *Stream) Write(op Op, b []byte) error { return s.run(op, b) }

func (s *Stream) run(op Op, in []byte) error {
	e := s.e
	if s.err != nil {
		return s.err
	}
	if s.done {
		if len(in) != 0 || op != OpFinish {
			return ErrUnsupported
		}
		return nil
	}
	if int64(e.inputPos)+int64(len(in)) >= maxStream {
		return ErrUnsupported
	}
	for {
		rem := e.remainingBlock()
		if rem != 0 && len(in) != 0 {
			n := min(rem, len(in))
			e.copyInput(in[:n])
			in = in[n:]
			continue
		}
		if rem == 0 || op != OpProcess {
			isLast := len(in) == 0 && op == OpFinish
			force := len(in) == 0 && op == OpFlush
			e.updateSizeHint(len(in))
			e.encodeData(isLast, force)
			if e.failed {
				s.err = errMismatch
				return s.err
			}
			s.emit()
			if force && e.w.nacc != 0 { // InjectBytePaddingBlock: an empty metadata block pads to a byte boundary
				e.w.write(6, 6)
				e.w.align()
				s.emit()
			}
			if isLast {
				s.done = true
				return s.err
			}
			if force {
				return s.err
			}
			continue
		}
		return s.err
	}
}

// Encode returns BrotliEncoderCompress(quality, lgwin, BROTLI_MODE_GENERIC, input) of the
// reference implementation (v1.0.9 .. v1.2.0 produce the same bytes on this path).
func Encode(input []byte, quality, lgwin int) ([]byte, error) {
	p := Params{Quality: quality, LgWin: lgwin, SizeHint: uint32(len(input))}
	if !p.valid() || len(input) >= maxStream {
		return nil, ErrUnsupported
	}
	if theDict.Load() == nil {
		return nil, ErrDictionary
	}
	if len(input) == 0 {
		return []byte{6}, nil
	}
	var sink sliceWriter
	s, err := NewStream(p, &sink)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if err := s.Write(OpFinish, input); err != nil {
		return nil, err
	}
	out := sink.b
	// BrotliEncoderCompress falls back to a stored stream when the result exceeds the bound
	if n := len(input); len(out) > n+2+4*(n>>14)+3+1 {
		return uncompressedStream(input), nil
	}
	return out, nil
}

type sliceWriter struct{ b []byte }

func (w *sliceWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }

// uncompressedStream is MakeUncompressedStream.
func uncompressedStream(in []byte) []byte {
	out := []byte{0x21, 0x03}
	for len(in) > 0 {
		chunk := min(len(in), 1<<24)
		nib := uint32(0)
		if chunk > 1<<16 {
			nib = 1
			if chunk > 1<<20 {
				nib = 2
			}
		}
		bits := nib<<1 | uint32(chunk-1)<<3 | 1<<(19+4*nib)
		out = append(out, byte(bits), byte(bits>>8), byte(bits>>16))
		if nib == 2 {
			out = append(out, byte(bits>>24))
		}
		out = append(out, in[:chunk]...)
		in = in[chunk:]
	}
	return append(out, 3)
}

// encodeData is EncodeData for qualities 5..9: it processes the unprocessed input as one block and writes a
// meta-block when the reference would (always on flush or finish).
func (e *encoder) encodeData(isLast, forceFlush bool) {
	bytes := e.unprocessed()
	pos := e.lastProcessed
	if bytes == 0 { // no new input; still might want to flush or finish
		if e.rb.data == nil {
			if isLast { // complete finalized stream
				e.w.write(2, 3)
				e.w.align()
			}
			return
		}
		if !isLast && !forceFlush {
			return
		}
	}
	if e.h == nil {
		e.h = newHasher(e.quality, e.lgwin, int(e.hint), int(e.rb.mask), e.legacy, theDict.Load())
	}
	// InitOrStitchToPreviousBlock
	if bytes >= e.h.typeLen()-1 && pos >= 3 {
		e.h.store(e.rb.buf, pos-3)
		e.h.store(e.rb.buf, pos-2)
		e.h.store(e.rb.buf, pos-1)
	}
	if len(e.cmds) > 0 && e.lastInsertLen == 0 {
		e.extendLastCommand(&bytes, &pos)
	}
	e.createBackwardReferences(bytes, pos)
	if e.probe != nil && !e.probe.check(e.cmds, e.lastFlush) {
		e.failed = true
		return
	}
	maxLength := 1 << uint(min(1+max(e.lgwin, e.lgblock), 24))
	processed := e.inputPos - e.lastFlush
	if !isLast && !forceFlush && processed+e.block() <= maxLength && e.numLiterals < maxLength/8 && len(e.cmds) < maxLength/8 {
		e.lastProcessed = e.inputPos // merge with the next input block
		return
	}
	if e.lastInsertLen > 0 {
		e.cmds = append(e.cmds, command{})
		e.cmds[len(e.cmds)-1].initInsert(uint32(e.lastInsertLen))
		e.numLiterals += e.lastInsertLen
		e.lastInsertLen = 0
	}
	if !isLast && e.inputPos == e.lastFlush {
		return // nothing new and the stream continues
	}
	e.writeMetaBlock(e.inputPos-e.lastFlush, isLast)
	if e.probe != nil {
		if e.stored {
			e.probe.advance(0)
		} else {
			e.probe.advance(len(e.cmds))
		}
	}
	e.lastFlush, e.lastProcessed = e.inputPos, e.inputPos
	if e.lastFlush > 0 {
		e.prev1 = e.rb.buf[(e.lastFlush-1)&int(e.rb.mask)]
	}
	if e.lastFlush > 1 {
		e.prev2 = e.rb.buf[(e.lastFlush-2)&int(e.rb.mask)]
	}
	e.cmds = e.cmds[:0]
	e.numLiterals = 0
	copy(e.savedDist[:], e.distCache[:4])
}

func (e *encoder) extendLastCommand(bytes, pos *int) {
	last := &e.cmds[len(e.cmds)-1]
	maxBackward := uint64(1)<<uint(e.lgwin) - windowGap
	lastCopy := uint64(last.copyLen & 0x1FFFFFF)
	lastProcessed := uint64(e.lastProcessed) - lastCopy
	maxDistance := min(lastProcessed, maxBackward)
	cmdDist := uint64(int64(e.distCache[0]))
	code := last.restoreDistanceCode()
	if code < numDistShort || uint64(code-(numDistShort-1)) == cmdDist {
		if cmdDist <= maxDistance {
			m, b := int(e.rb.mask), e.rb.buf
			for *bytes != 0 && b[*pos&m] == b[(*pos-int(cmdDist))&m] {
				last.copyLen++
				*bytes--
				*pos++
			}
		}
		last.cmdPrefix = lengthCode(last.insertLen, uint32(int32(last.copyLen&0x1FFFFFF)+int32(last.copyLen>>25)), last.distPrefix&0x3FF == 0)
	}
}

func (e *encoder) shouldCompress(bytes int, data []byte) bool {
	if bytes <= 2 {
		return false
	}
	if len(e.cmds) < bytes>>8+2 && float64(e.numLiterals) > float64(0.99*float64(bytes)) {
		var histo [256]uint32
		t := (bytes + 12) / 13
		pos := 0
		for i := 0; i < t; i++ {
			histo[data[pos]]++
			pos += 13
		}
		if bitsEntropy(histo[:]) > float64(float64(float64(bytes)*7.92)*(1.0/13.0)) {
			return false
		}
	}
	return true
}

// linearize copies the meta-block bytes out of the ring: two bytes of left context, then the data.
func (e *encoder) linearize(bytes int) []byte {
	need := 2 + bytes + 16
	if cap(e.lin) < need {
		e.lin = make([]byte, need)
	}
	l := e.lin[:need]
	l[0], l[1] = e.prev2, e.prev1
	m := int(e.rb.mask)
	start := e.lastFlush & m
	if start+bytes <= int(e.rb.size) {
		copy(l[2:], e.rb.buf[start:start+bytes])
	} else {
		n := int(e.rb.size) - start
		copy(l[2:], e.rb.buf[start:start+n])
		copy(l[2+n:], e.rb.buf[:bytes-n])
	}
	return l
}

// writeMetaBlock is WriteMetaBlockInternal for qualities 4..9.
func (e *encoder) writeMetaBlock(bytes int, isLast bool) {
	w := &e.w
	e.stored = true
	if bytes == 0 {
		w.write(2, 3)
		w.align()
		return
	}
	lin := e.linearize(bytes)
	data := lin[2 : 2+bytes]
	if !e.shouldCompress(bytes, data) {
		copy(e.distCache[:4], e.savedDist[:])
		w.uncompressedMetaBlock(isLast, data)
		return
	}
	m := w.mark()
	nctx, ctxMap := decideLiteralContexts(lin, 2, bytes, e.quality, int(e.hint))
	mb := buildMetaBlockGreedy(lin, 2, e.prev1, e.prev2, nctx, ctxMap, e.cmds)
	w.storeMetaBlock(lin, 2, bytes, e.prev1, e.prev2, isLast, e.cmds, mb)
	e.stored = false
	if bytes+4 < w.bitsSince(m)>>3 {
		e.stored = true
		copy(e.distCache[:4], e.savedDist[:])
		w.reset(m)
		w.uncompressedMetaBlock(isLast, data)
	}
}
