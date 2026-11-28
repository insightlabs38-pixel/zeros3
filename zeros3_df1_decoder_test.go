package main

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"
)

func df1EagerDecode(d *repDFdecoder, br *repDFbitReader) (int, error) {
	br.fill()
	if e := d.fast[br.acc&(1<<repDFfastBits-1)]; e != 0 {
		l := uint(e & 15)
		if l > br.nacc {
			return 0, repDFErrTruncated
		}
		br.acc >>= l
		br.nacc -= l
		return int(e >> 4), nil
	}
	if d.empty {
		return 0, repDFcorrupt(br.bitPos(), "symbol from empty code")
	}
	code, first, index := 0, 0, 0
	acc := br.acc
	for l := uint(1); l <= repDFmaxCodeLen; l++ {
		if l > br.nacc {
			return 0, repDFErrTruncated
		}
		code |= int(acc & 1)
		acc >>= 1
		c := int(d.count[l])
		if code-c < first {
			br.acc >>= l
			br.nacc -= l
			return int(d.sym[index+(code-first)]), nil
		}
		index += c
		first += c
		first <<= 1
		code <<= 1
	}
	return 0, repDFcorrupt(br.bitPos(), "invalid Huffman code")
}

func TestDF1HuffmanRefillEquivalent(t *testing.T) {
	repDFinitFixed()
	deep := []uint8{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 15}
	rng := rand.New(rand.NewPCG(697, 454))
	for _, lengths := range [][]uint8{repDFfixedLitL, repDFfixedDstL, deep, {1}, nil} {
		decoder, ok := repDFbuildDecoder(lengths)
		if !ok {
			t.Fatal("invalid reference codebook")
		}
		for size := range 64 {
			raw := make([]byte, size)
			for i := range raw {
				raw[i] = byte(rng.Uint32())
			}
			for offset := range 8 {
				for _, streaming := range []bool{false, true} {
					makeReader := func() repDFbitReader {
						if streaming {
							return repDFbitReader{data: make([]byte, 0, 7), src: bytes.NewReader(raw)}
						}
						return repDFbitReader{data: raw}
					}
					got, want := makeReader(), makeReader()
					got.bits(uint(offset))
					want.bits(uint(offset))
					for step := range 100 {
						sym, err := decoder.decode(&got)
						reference, oldErr := df1EagerDecode(decoder, &want)
						if sym != reference || fmt.Sprint(err) != fmt.Sprint(oldErr) || got.bitPos() != want.bitPos() {
							t.Fatalf("decoder differs at size %d offset %d step %d streaming=%t: %v %v", size, offset, step, streaming, err, oldErr)
						}
						if err != nil {
							break
						}
						extra := uint(step % 14)
						v, err := got.bits(extra)
						w, oldErr := want.bits(extra)
						if v != w || err != oldErr || got.bitPos() != want.bitPos() {
							t.Fatal("interleaved extra-bit reads differ")
						}
					}
				}
			}
		}
	}
}

func BenchmarkDF1HuffmanDecode(b *testing.B) {
	repDFinitFixed()
	rng := rand.New(rand.NewPCG(8191, 10))
	wire := make([]byte, 4096)
	for i := range wire {
		wire[i] = byte(rng.Uint32())
	}
	d, ok := repDFbuildDecoder(repDFfixedLitL)
	if !ok {
		b.Fatal("invalid fixed table")
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(wire)))
	for b.Loop() {
		r := repDFbitReader{data: wire}
		for r.bitPos()+15 < int64(len(wire))*8 {
			if _, err := d.decode(&r); err != nil {
				b.Fatal(err)
			}
		}
	}
}
