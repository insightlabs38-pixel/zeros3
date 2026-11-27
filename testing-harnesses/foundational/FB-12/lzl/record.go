package lzl

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
)

// MaxRecordBytes matches the frozen CDC v1 record limit used by the engine.
const MaxRecordBytes = 256 << 10
const MaxDictionaryBytes = 32 << 10

const recordVersion byte = 1
const recordHeaderBytes = 5
const recordDictionary byte = 0x80

var recordCRC = crc32.MakeTable(crc32.Castagnoli)

var ErrEncodedRecordTooLarge = errors.New("lzl: encoded record exceeds storage bound")

func recordChecksum(header byte, dictionary, payload []byte) uint32 {
	if header == recordVersion {
		return crc32.Checksum(payload, recordCRC)
	}
	seed := crc32.Update(crc32.Checksum([]byte{header}, recordCRC), recordCRC, dictionary)
	return crc32.Update(seed, recordCRC, payload)
}

// EncodeRecord wraps the audit's independent range stream in one bounded
// revision and a checked payload. The surrounding pack binds decoded length
// and SHA-256, so those values are not repeated here.
func (z *Encoder) EncodeRecord(dst, src []byte) ([]byte, error) {
	return z.EncodeDictionaryRecord(dst, src, nil)
}

func (z *Encoder) EncodeDictionaryRecord(dst, src, dictionary []byte) ([]byte, error) {
	if len(src) == 0 || len(src) > MaxRecordBytes || len(dictionary) > MaxDictionaryBytes || z.P.Model < 0 || z.P.Model >= len(coderModels) {
		return nil, ErrCorrupt
	}
	header := recordVersion | byte(z.P.Model)<<4
	if len(dictionary) != 0 {
		z.joined = append(z.joined[:0], dictionary...)
		z.joined = append(z.joined, src...)
		src = z.joined
		header |= recordDictionary
	}
	raw := z.encodeStream(dst[:0], src, len(dictionary))
	if len(raw) > MaxRecordBytes+16 {
		return nil, ErrEncodedRecordTooLarge
	}
	raw = append(raw, make([]byte, recordHeaderBytes)...)
	copy(raw[recordHeaderBytes:], raw[:len(raw)-recordHeaderBytes])
	raw[0] = header
	binary.LittleEndian.PutUint32(raw[1:5], recordChecksum(raw[0], dictionary, raw[recordHeaderBytes:]))
	return raw, nil
}

// DecodeRecord checks framing before decoding and validates the exact bytes
// independently against the surrounding record's digest.
func (z *Decoder) DecodeRecord(dst, src []byte, n int, want [32]byte) ([]byte, error) {
	return z.DecodeDictionaryRecord(dst, src, n, want, nil)
}

func (z *Decoder) DecodeDictionaryRecord(dst, src []byte, n int, want [32]byte, dictionary []byte) ([]byte, error) {
	if len(src) < recordHeaderBytes || len(src) > MaxRecordBytes+16+recordHeaderBytes ||
		src[0]&15 != recordVersion || int(src[0]>>4&7) >= len(coderModels) || n < 1 || n > MaxRecordBytes ||
		len(dictionary) > MaxDictionaryBytes || (src[0]&recordDictionary != 0) != (len(dictionary) != 0) {
		return nil, ErrCorrupt
	}
	data := src[recordHeaderBytes:]
	if recordChecksum(src[0], dictionary, data) != binary.LittleEndian.Uint32(src[1:5]) {
		return nil, ErrCorrupt
	}
	out, err := z.decode(dst, data, n, coderModels[src[0]>>4&7], dictionary)
	if err != nil || sha256.Sum256(out) != want {
		return nil, ErrCorrupt
	}
	return out, nil
}
