package eraez3

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

// A minimal Snappy: block decoder, framing decoder, and a port of the
// reference (golang/snappy) block encoder + buffered framing writer. The
// encoder exists so that a framed entry can be *proven* canonical: if the
// reference encoder reproduces the stored bytes from the plaintext, the
// plaintext alone is enough to rebuild them. It is tested against real EraE
// files; any entry it does not reproduce is kept as opaque bytes.

var errSnappy = errors.New("eraez3: bad snappy data")

const (
	tagLiteral = 0x00
	tagCopy1   = 0x01
	tagCopy2   = 0x02
	tagCopy4   = 0x03

	maxBlockSize = 65536
	// MaxPlain bounds one entry's decoded size.
	MaxPlain = 64 << 20
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

func maskedCRC(b []byte) uint32 {
	c := crc32.Update(0, crcTable, b)
	return c>>15 | c<<17 + 0xa282ead8
}

// blockDecode decodes one Snappy block (uvarint length + elements).
func blockDecode(src []byte, limit int) ([]byte, error) {
	n, k := binary.Uvarint(src)
	if k <= 0 || n > uint64(limit) {
		return nil, errSnappy
	}
	dst := make([]byte, 0, n)
	s := k
	for s < len(src) {
		tag := src[s]
		var length, offset int
		switch tag & 3 {
		case tagLiteral:
			x := int(tag >> 2)
			switch {
			case x < 60:
				s++
			case x == 60:
				if s+2 > len(src) {
					return nil, errSnappy
				}
				x = int(src[s+1])
				s += 2
			case x == 61:
				if s+3 > len(src) {
					return nil, errSnappy
				}
				x = int(src[s+1]) | int(src[s+2])<<8
				s += 3
			case x == 62:
				if s+4 > len(src) {
					return nil, errSnappy
				}
				x = int(src[s+1]) | int(src[s+2])<<8 | int(src[s+3])<<16
				s += 4
			default:
				if s+5 > len(src) {
					return nil, errSnappy
				}
				x = int(src[s+1]) | int(src[s+2])<<8 | int(src[s+3])<<16 | int(src[s+4])<<24
				s += 5
			}
			length = x + 1
			if length <= 0 || length > len(src)-s || len(dst)+length > int(n) {
				return nil, errSnappy
			}
			dst = append(dst, src[s:s+length]...)
			s += length
			continue
		case tagCopy1:
			if s+2 > len(src) {
				return nil, errSnappy
			}
			length = 4 + int(tag>>2)&7
			offset = int(tag&0xe0)<<3 | int(src[s+1])
			s += 2
		case tagCopy2:
			if s+3 > len(src) {
				return nil, errSnappy
			}
			length = 1 + int(tag>>2)
			offset = int(src[s+1]) | int(src[s+2])<<8
			s += 3
		default:
			if s+5 > len(src) {
				return nil, errSnappy
			}
			length = 1 + int(tag>>2)
			offset = int(src[s+1]) | int(src[s+2])<<8 | int(src[s+3])<<16 | int(src[s+4])<<24
			s += 5
		}
		if offset <= 0 || offset > len(dst) || len(dst)+length > int(n) {
			return nil, errSnappy
		}
		for i := 0; i < length; i++ { // overlapping copies are legal
			dst = append(dst, dst[len(dst)-offset])
		}
	}
	if uint64(len(dst)) != n {
		return nil, errSnappy
	}
	return dst, nil
}

var streamID = []byte{0xff, 0x06, 0x00, 0x00, 's', 'N', 'a', 'P', 'p', 'Y'}

// FrameDecode decodes a Snappy framing stream made only of a stream identifier
// and compressed / uncompressed data chunks, with every checksum verified.
func FrameDecode(b []byte) ([]byte, error) {
	if len(b) < len(streamID) || string(b[:len(streamID)]) != string(streamID) {
		return nil, errSnappy
	}
	p := len(streamID)
	var out []byte
	for p < len(b) {
		if len(b)-p < 4 {
			return nil, errSnappy
		}
		typ := b[p]
		l := int(b[p+1]) | int(b[p+2])<<8 | int(b[p+3])<<16
		p += 4
		if l < 4 || l > len(b)-p {
			return nil, errSnappy
		}
		crc := binary.LittleEndian.Uint32(b[p:])
		body := b[p+4 : p+l]
		var plain []byte
		switch typ {
		case 0x00:
			var err error
			if plain, err = blockDecode(body, maxBlockSize); err != nil {
				return nil, err
			}
		case 0x01:
			if len(body) > maxBlockSize {
				return nil, errSnappy
			}
			plain = body
		default:
			return nil, errSnappy
		}
		if maskedCRC(plain) != crc {
			return nil, errSnappy
		}
		if len(out)+len(plain) > MaxPlain {
			return nil, errSnappy
		}
		out = append(out, plain...)
		p += l
	}
	return out, nil
}

// ---- reference encoder (port of golang/snappy encode_other.go) ---------------

func load32(b []byte, i int) uint32 {
	return uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16 | uint32(b[i+3])<<24
}

func load64(b []byte, i int) uint64 {
	return uint64(load32(b, i)) | uint64(load32(b, i+4))<<32
}

func hash(u, shift uint32) uint32 { return (u * 0x1e35a7bd) >> shift }

func emitLiteral(dst, lit []byte) int {
	i, n := 0, uint(len(lit)-1)
	switch {
	case n < 60:
		dst[0] = uint8(n)<<2 | tagLiteral
		i = 1
	case n < 1<<8:
		dst[0] = 60<<2 | tagLiteral
		dst[1] = uint8(n)
		i = 2
	default:
		dst[0] = 61<<2 | tagLiteral
		dst[1] = uint8(n)
		dst[2] = uint8(n >> 8)
		i = 3
	}
	return i + copy(dst[i:], lit)
}

func emitCopy(dst []byte, offset, length int) int {
	i := 0
	for length >= 68 {
		dst[i+0] = 63<<2 | tagCopy2
		dst[i+1] = uint8(offset)
		dst[i+2] = uint8(offset >> 8)
		i += 3
		length -= 64
	}
	if length > 64 {
		dst[i+0] = 59<<2 | tagCopy2
		dst[i+1] = uint8(offset)
		dst[i+2] = uint8(offset >> 8)
		i += 3
		length -= 60
	}
	if length >= 12 || offset >= 2048 {
		dst[i+0] = uint8(length-1)<<2 | tagCopy2
		dst[i+1] = uint8(offset)
		dst[i+2] = uint8(offset >> 8)
		return i + 3
	}
	dst[i+0] = uint8(offset>>8)<<5 | uint8(length-4)<<2 | tagCopy1
	dst[i+1] = uint8(offset)
	return i + 2
}

func encodeBlock(dst, src []byte) (d int) {
	const (
		inputMargin            = 16 - 1
		minNonLiteralBlockSize = 1 + 1 + inputMargin
		maxTableSize           = 1 << 14
		tableMask              = maxTableSize - 1
	)
	if len(src) < minNonLiteralBlockSize {
		return emitLiteral(dst, src)
	}
	shift := uint32(32 - 8)
	for tableSize := 1 << 8; tableSize < maxTableSize && tableSize < len(src); tableSize *= 2 {
		shift--
	}
	var table [maxTableSize]uint16
	sLimit := len(src) - inputMargin
	nextEmit := 0
	s := 1
	nextHash := hash(load32(src, s), shift)
	for {
		skip := 32
		nextS := s
		candidate := 0
		for {
			s = nextS
			bytesBetweenHashLookups := skip >> 5
			nextS = s + bytesBetweenHashLookups
			skip += bytesBetweenHashLookups
			if nextS > sLimit {
				goto emitRemainder
			}
			candidate = int(table[nextHash&tableMask])
			table[nextHash&tableMask] = uint16(s)
			nextHash = hash(load32(src, nextS), shift)
			if load32(src, s) == load32(src, candidate) {
				break
			}
		}
		d += emitLiteral(dst[d:], src[nextEmit:s])
		for {
			base := s
			s += 4
			for i := candidate + 4; s < len(src) && src[i] == src[s]; i, s = i+1, s+1 {
			}
			d += emitCopy(dst[d:], base-candidate, s-base)
			nextEmit = s
			if s >= sLimit {
				goto emitRemainder
			}
			x := load64(src, s-1)
			prevHash := hash(uint32(x>>0), shift)
			table[prevHash&tableMask] = uint16(s - 1)
			currHash := hash(uint32(x>>8), shift)
			candidate = int(table[currHash&tableMask])
			table[currHash&tableMask] = uint16(s)
			if uint32(x>>8) != load32(src, candidate) {
				nextHash = hash(uint32(x>>16), shift)
				s++
				break
			}
		}
	}
emitRemainder:
	if nextEmit < len(src) {
		d += emitLiteral(dst[d:], src[nextEmit:])
	}
	return d
}

// blockEncode is snappy.Encode.
func blockEncode(src []byte) []byte {
	dst := make([]byte, binary.MaxVarintLen32+len(src)+len(src)/6+32)
	d := binary.PutUvarint(dst, uint64(len(src)))
	d += encodeBlock(dst[d:], src)
	return dst[:d]
}

// FrameEncodeRef is the buffered framing writer of the reference
// implementation: identifier, then per 64 KiB block a compressed chunk unless
// compression saves less than 1/8, in which case an uncompressed chunk.
func FrameEncodeRef(plain []byte) []byte {
	out := append([]byte(nil), streamID...)
	for len(plain) > 0 {
		blk := plain[:min(len(plain), maxBlockSize)]
		plain = plain[len(blk):]
		comp := blockEncode(blk)
		typ, body := byte(0x00), comp
		if len(comp) >= len(blk)-len(blk)/8 {
			typ, body = 0x01, blk
		}
		l := 4 + len(body)
		out = append(out, typ, byte(l), byte(l>>8), byte(l>>16))
		out = binary.LittleEndian.AppendUint32(out, maskedCRC(blk))
		out = append(out, body...)
	}
	return out
}

// FrameEncodeRaw frames plain using only uncompressed chunks (a valid stream
// another implementation might write).
func FrameEncodeRaw(plain []byte) []byte {
	out := append([]byte(nil), streamID...)
	for len(plain) > 0 {
		blk := plain[:min(len(plain), maxBlockSize)]
		plain = plain[len(blk):]
		l := 4 + len(blk)
		out = append(out, 0x01, byte(l), byte(l>>8), byte(l>>16))
		out = binary.LittleEndian.AppendUint32(out, maskedCRC(blk))
		out = append(out, blk...)
	}
	return out
}
