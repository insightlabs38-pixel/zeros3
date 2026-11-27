package lzl

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
)

// MaxRecordBytes matches the frozen CDC v1 record limit used by the engine.
const MaxRecordBytes = 256 << 10

const recordVersion byte = 1
const recordHeaderBytes = 5

var recordCRC = crc32.MakeTable(crc32.Castagnoli)

var ErrEncodedRecordTooLarge = errors.New("lzl: encoded record exceeds storage bound")

// EncodeRecord wraps the audit's independent range stream in one bounded
// revision and a checked payload. The surrounding pack binds decoded length
// and SHA-256, so those values are not repeated here.
func (z *Encoder) EncodeRecord(dst, src []byte) ([]byte, error) {
	if len(src) == 0 || len(src) > MaxRecordBytes {
		return nil, ErrCorrupt
	}
	raw := z.Encode(dst[:0], src)
	if len(raw) > MaxRecordBytes+16 {
		return nil, ErrEncodedRecordTooLarge
	}
	raw = append(raw, make([]byte, recordHeaderBytes)...)
	copy(raw[recordHeaderBytes:], raw[:len(raw)-recordHeaderBytes])
	raw[0] = recordVersion
	binary.LittleEndian.PutUint32(raw[1:5], crc32.Checksum(raw[recordHeaderBytes:], recordCRC))
	return raw, nil
}

// DecodeRecord checks framing before decoding and validates the exact bytes
// independently against the surrounding record's digest.
func (z *Decoder) DecodeRecord(dst, src []byte, n int, want [32]byte) ([]byte, error) {
	if len(src) < recordHeaderBytes || len(src) > MaxRecordBytes+16+recordHeaderBytes ||
		src[0] != recordVersion || n < 1 || n > MaxRecordBytes {
		return nil, ErrCorrupt
	}
	data := src[recordHeaderBytes:]
	if crc32.Checksum(data, recordCRC) != binary.LittleEndian.Uint32(src[1:5]) {
		return nil, ErrCorrupt
	}
	out, err := z.Decode(dst, data, n)
	if err != nil || sha256.Sum256(out) != want {
		return nil, ErrCorrupt
	}
	return out, nil
}
