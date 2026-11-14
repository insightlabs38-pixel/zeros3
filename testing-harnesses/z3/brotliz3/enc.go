// Package brotliz3 is an exact Brotli substrate (standard library only): a bounded RFC 7932
// decoder and a faithful port of the google/brotli reference encoder's one-shot path for
// qualities 5-9 and windows of 2^17..2^24 bytes (BrotliEncoderCompress, generic mode). The port
// reproduces the reference output bit for bit, which turns "re-encode and compare" into a
// replay recipe: a consumer stores the decoded bytes plus (quality, lgwin) and regenerates the
// producer's compressed bytes. Outputs are always verified by the caller against stored bytes.
package brotliz3

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
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

type encoder struct {
	quality, lgwin, lgblock int
	sizeHint                int
	buf                     []byte
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
}

// MaxInput is the largest input the port accepts for a window: beyond it the reference's ring
// buffer wraps, which is not modelled.
func MaxInput(lgwin int) int { return 1<<uint(lgwin+1) - 64 }

// Encode returns BrotliEncoderCompress(quality, lgwin, BROTLI_MODE_GENERIC, input) of the
// reference implementation (v1.0.9 .. v1.2.0 produce the same bytes on this path).
func Encode(input []byte, quality, lgwin int) ([]byte, error) {
	if quality < 5 || quality > 9 || lgwin < 17 || lgwin > 24 || len(input) > MaxInput(lgwin) {
		return nil, ErrUnsupported
	}
	d := theDict.Load()
	if d == nil {
		return nil, ErrDictionary
	}
	if len(input) == 0 {
		return []byte{6}, nil
	}
	e := &encoder{quality: quality, lgwin: lgwin, lgblock: 16, sizeHint: len(input)}
	if quality >= 9 {
		e.lgblock = min(18, lgwin)
	}
	e.buf = make([]byte, len(input)+16)
	e.distCache = [16]int32{4, 11, 15, 16}
	e.savedDist = [4]int32{4, 11, 15, 16}
	e.h = newHasher(quality, lgwin, e.sizeHint, d)
	defer e.h.release()
	// EncodeWindowBits
	if lgwin == 17 {
		e.w.write(7, 1)
	} else {
		e.w.write(4, uint64((lgwin-17)<<1|1))
	}
	block := 1 << uint(e.lgblock)
	for off := 0; off < len(input); off += block {
		end := min(off+block, len(input))
		copy(e.buf[off:end], input[off:end])
		e.inputPos = end
		e.encodeData(end == len(input))
	}
	out := e.w.buf
	// BrotliEncoderCompress falls back to a stored stream when the result exceeds the bound
	if n := len(input); len(out) > n+2+4*(n>>14)+3+1 {
		return uncompressedStream(input), nil
	}
	return out, nil
}

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

func (e *encoder) encodeData(isLast bool) {
	bytes := e.inputPos - e.lastProcessed
	pos := e.lastProcessed
	// InitOrStitchToPreviousBlock
	if bytes >= e.h.typeLen()-1 && pos >= 3 {
		e.h.store(e.buf, pos-3)
		e.h.store(e.buf, pos-2)
		e.h.store(e.buf, pos-1)
	}
	if len(e.cmds) > 0 && e.lastInsertLen == 0 {
		e.extendLastCommand(&bytes, &pos)
	}
	e.createBackwardReferences(bytes, pos)
	maxLength := 1 << uint(min(1+max(e.lgwin, e.lgblock), 24))
	processed := e.inputPos - e.lastFlush
	if !isLast && processed+1<<uint(e.lgblock) <= maxLength && e.numLiterals < maxLength/8 && len(e.cmds) < maxLength/8 {
		e.lastProcessed = e.inputPos
		return
	}
	if e.lastInsertLen > 0 {
		e.cmds = append(e.cmds, command{})
		e.cmds[len(e.cmds)-1].initInsert(uint32(e.lastInsertLen))
		e.numLiterals += e.lastInsertLen
		e.lastInsertLen = 0
	}
	e.writeMetaBlock(e.inputPos-e.lastFlush, isLast)
	e.lastFlush, e.lastProcessed = e.inputPos, e.inputPos
	if e.lastFlush > 0 {
		e.prev1 = e.buf[e.lastFlush-1]
	}
	if e.lastFlush > 1 {
		e.prev2 = e.buf[e.lastFlush-2]
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
			for *bytes != 0 && e.buf[*pos] == e.buf[*pos-int(cmdDist)] {
				last.copyLen++
				*bytes--
				*pos++
			}
		}
		last.cmdPrefix = lengthCode(last.insertLen, uint32(int32(last.copyLen&0x1FFFFFF)+int32(last.copyLen>>25)), last.distPrefix&0x3FF == 0)
	}
}

func (e *encoder) shouldCompress(bytes int) bool {
	if bytes <= 2 {
		return false
	}
	if len(e.cmds) < bytes>>8+2 && float64(e.numLiterals) > float64(0.99*float64(bytes)) {
		var histo [256]uint32
		t := (bytes + 12) / 13
		pos := e.lastFlush
		for i := 0; i < t; i++ {
			histo[e.buf[pos]]++
			pos += 13
		}
		if bitsEntropy(histo[:]) > float64(float64(float64(bytes)*7.92)*(1.0/13.0)) {
			return false
		}
	}
	return true
}

// writeMetaBlock is WriteMetaBlockInternal for qualities 4..9.
func (e *encoder) writeMetaBlock(bytes int, isLast bool) {
	w := &e.w
	if bytes == 0 {
		w.write(2, 3)
		w.align()
		return
	}
	data := e.buf[e.lastFlush : e.lastFlush+bytes]
	if !e.shouldCompress(bytes) {
		copy(e.distCache[:4], e.savedDist[:])
		w.uncompressedMetaBlock(isLast, data)
		return
	}
	m := w.mark()
	nctx, ctxMap := decideLiteralContexts(e.buf, e.lastFlush, bytes, e.quality, e.sizeHint)
	mb := buildMetaBlockGreedy(e.buf, e.lastFlush, e.prev1, e.prev2, nctx, ctxMap, e.cmds)
	w.storeMetaBlock(e.buf, e.lastFlush, bytes, e.prev1, e.prev2, isLast, e.cmds, mb)
	if bytes+4 < w.bitsSince(m)>>3 {
		copy(e.distCache[:4], e.savedDist[:])
		w.reset(m)
		w.uncompressedMetaBlock(isLast, data)
	}
}
