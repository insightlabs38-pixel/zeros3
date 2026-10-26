package zstdz3

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Synth builds one valid Zstandard frame over plain: a greedy 4-byte match finder,
// raw literals and the predefined sequence tables, one block per 128 KiB, with a
// content checksum. It is a laboratory producer, not a compressor anyone should ship:
// it exists so hermetic tests and the representation-DAG lab can create genuinely
// compressed (match-bearing) Zstandard outers without an external zstd binary. The
// stream is an ordinary frame (libzstd 1.5.7 decodes it); this package's Parse and
// Replay treat it like any producer's output.
func Synth(plain []byte) ([]byte, error) {
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, uint32(magicStd))
	out.Write([]byte{4, 0x58}) // content checksum, 2 MiB window
	var st codingState
	var tab [1 << 15]int32 // 4-byte hash -> position+1
	hash := func(p int) uint32 { return binary.LittleEndian.Uint32(plain[p:]) * 2654435761 >> 17 }
	if len(plain) == 0 {
		out.Write(blockHeader(true, BlockRaw, 0))
	}
	for start := 0; start < len(plain); start += maxBlock {
		end := min(start+maxBlock, len(plain))
		var trip []seqRec
		litFrom := start
		for p := start; p < end; {
			ml, off := 0, 0
			if p+4 <= end {
				h := hash(p)
				if c := int(tab[h]) - 1; c >= 0 && p-c <= 1<<21 && p-c > 0 {
					for ml < end-p && plain[c+ml] == plain[p+ml] {
						ml++
					}
					off = p - c
				}
				tab[h] = int32(p + 1)
			}
			if ml < 4 {
				p++
				continue
			}
			trip = append(trip, seqRec{ll: uint32(p - litFrom), ml: uint32(ml), of: uint32(off + 3)})
			for q := p + 1; q < p+ml && q+4 <= end; q += 3 { // index interior positions for later matches
				tab[hash(q)] = int32(q + 1)
			}
			p += ml
			litFrom = p
		}
		var all []byte
		pos := start
		for _, s := range trip {
			all = append(all, plain[pos:pos+int(s.ll)]...)
			pos += int(s.ll) + int(s.ml)
		}
		all = append(all, plain[pos:end]...)
		rec := BlockRec{Type: BlockCompressed, Last: end == len(plain), DecLen: uint32(end - start), Regen: uint32(len(all)), NSeq: uint32(len(trip)), LitMode: litRaw}
		switch n := len(all); {
		case n <= 31:
			rec.LitForm = 0
		case n <= 4095:
			rec.LitForm = 1
		default:
			rec.LitForm = 3
		}
		content, err := buildCompressed(&st, &rec, plain[start:end], trip)
		if err == nil {
			_, err = scanBlock(&st, content, maxBlock)
		}
		if err != nil || len(content) >= end-start {
			if err != nil && len(trip) > 0 {
				return nil, fmt.Errorf("zstdz3: synth block at %d: %w", start, err)
			}
			out.Write(blockHeader(end == len(plain), BlockRaw, end-start))
			out.Write(plain[start:end])
			continue
		}
		out.Write(blockHeader(end == len(plain), BlockCompressed, len(content)))
		out.Write(content)
	}
	x := newXXH64()
	x.write(plain)
	out.Write(binary.LittleEndian.AppendUint32(nil, uint32(x.sum())))
	return out.Bytes(), nil
}
