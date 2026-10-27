package eraez3

import (
	"encoding/binary"
	"errors"
	"hash/crc32"

	"zeros3-z3/snappyz3"
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

// ---- reference encoder: the Go snappy port now lives in snappyz3 (Z3-30) ----------

// blockEncode is snappy.Encode.
func blockEncode(src []byte) []byte { return snappyz3.EncodeRef(src) }

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

// BlockDecode decodes one raw Snappy block (not framed) of at most limit bytes.
func BlockDecode(src []byte, limit int) ([]byte, error) { return blockDecode(src, limit) }
