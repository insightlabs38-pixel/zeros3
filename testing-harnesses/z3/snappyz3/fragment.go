package snappyz3

// Z3-31: the producers' encoders (Go golang/snappy, C++ snappy) cut their input into
// 64 KiB fragments, compress each fragment independently (fresh hash table, copy offsets
// never cross a fragment) and concatenate the outputs behind ONE uvarint of the total
// decoded length. EncodeRef, EncodeCPP and EncodeCPP15 reproduce a single fragment (the
// SST blocks of Z3-30 are below 64 KiB); a Parquet page is typically 0.1-1 MiB, so these
// wrappers add the fragmenting loop and nothing else. Like the single-fragment
// functions they are predictors only: the caller compares their output with the stored
// bytes and falls back to explicit commands when it differs.

import (
	"encoding/binary"
	"hash/crc32"
)

// FragmentSize is the fragment size of both encoders.
const FragmentSize = 1 << 16

// EncodeFragments compresses src fragment by fragment with enc (one of EncodeRef,
// EncodeCPP, EncodeCPP15; each returns a complete block with its own length prefix,
// which is replaced by one prefix for the whole input).
func EncodeFragments(src []byte, enc func([]byte) []byte) []byte {
	out := binary.AppendUvarint(make([]byte, 0, len(src)/2+16), uint64(len(src)))
	for len(src) > 0 {
		n := min(len(src), FragmentSize)
		b := enc(src[:n])
		_, k := binary.Uvarint(b)
		out = append(out, b[k:]...)
		src = src[n:]
	}
	return out
}

// EncodeCPPBits is EncodeCPP with an explicit hash width (14 and 15 are the producers
// seen so far); it exists so a predictor sweep can find the width a producer used.
func EncodeCPPBits(top uint) func([]byte) []byte {
	return func(src []byte) []byte { return encodeCPP(src, top) }
}

// EncodeCPPCRC is the C++ snappy 1.2.x encoder as built with SSE4.2/CRC32 hashing
// (SNAPPY_HAVE_X86_CRC32: the table index is CRC32C(mask, bytes) instead of the
// multiplicative hash); top is kMaxHashTableBits (15 in 1.2.x).
func EncodeCPPCRC(top uint) func([]byte) []byte {
	return func(src []byte) []byte {
		return encodeCPPHash(src, top, func(u, mask uint32) uint32 {
			var b [4]byte
			b[0], b[1], b[2], b[3] = byte(u), byte(u>>8), byte(u>>16), byte(u>>24)
			raw := ^crc32.Update(^(2 * mask), castagnoli, b[:])
			return (raw >> 1) & mask
		})
	}
}

var castagnoli = crc32.MakeTable(crc32.Castagnoli)
