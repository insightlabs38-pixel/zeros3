package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"slices"
	"testing"
)

func df1ReferencePut(w *repDFbitWriter, v uint32, n uint) {
	w.acc |= uint64(v) << w.nacc
	w.nacc += n
	for w.nacc >= 8 {
		w.buf = append(w.buf, byte(w.acc))
		w.acc >>= 8
		w.nacc -= 8
	}
	if len(w.buf) >= repDFflushAt {
		w.drain()
	}
}

func TestDF1BitWriterEquivalent(t *testing.T) {
	rng := rand.New(rand.NewPCG(819, 2))
	for start := range 8 {
		for _, fail := range []bool{false, true} {
			var output, reference bytes.Buffer
			var calls, referenceCalls []int
			stopped := errors.New("sink rejected output")
			makeSink := func(buf *bytes.Buffer, counts *[]int) func([]byte) error {
				return func(p []byte) error {
					*counts = append(*counts, len(p))
					if fail && len(*counts) == 2 {
						return stopped
					}
					_, err := buf.Write(p)
					return err
				}
			}
			w := repDFbitWriter{nacc: uint(start), flush: makeSink(&output, &calls)}
			referenceWriter := repDFbitWriter{nacc: uint(start), flush: makeSink(&reference, &referenceCalls)}
			for i := range 40000 {
				n := uint(i % 33)
				v := rng.Uint32() & (uint32(1)<<n - 1)
				w.put(v, n)
				df1ReferencePut(&referenceWriter, v, n)
				if w.acc != referenceWriter.acc || w.nacc != referenceWriter.nacc || w.err != referenceWriter.err ||
					len(w.buf) != len(referenceWriter.buf) {
					t.Fatalf("writer state differs at bit offset %d step %d", start, i)
				}
			}
			w.drain()
			referenceWriter.drain()
			if !bytes.Equal(output.Bytes(), reference.Bytes()) || !slices.Equal(calls, referenceCalls) {
				t.Fatalf("wire or flush boundaries differ at bit offset %d", start)
			}
		}
	}
}

func BenchmarkDF1BitWriter(b *testing.B) {
	widths := []uint{8, 9, 6, 7, 5, 0, 12, 8, 15, 4, 13, 8, 32}
	sink := func([]byte) error { return nil }
	w := repDFbitWriter{buf: make([]byte, 0, repDFflushAt+8), flush: sink}
	b.ReportAllocs()
	for b.Loop() {
		for _, width := range widths {
			w.put((1<<width-1)&365435, width)
		}
	}
}

func TestDF1PackedTokensEquivalent(t *testing.T) {
	rng := rand.New(rand.NewPCG(541, 12))
	const count = 24000
	wire := binary.AppendUvarint(nil, count<<1)
	tokens := make([]repDFToken, count)
	for i := range tokens {
		if i%7 == 0 {
			tokens[i] = repDFLiteral(byte(rng.Uint32()))
		} else {
			sym := 257 + rng.IntN(29)
			dist := rng.IntN(30)
			lengthExtra := rng.Uint32() & (1<<repDFlenExtra[sym-257] - 1)
			distExtra := rng.Uint32() & (1<<repDFdistExtr[dist] - 1)
			tokens[i] = repDFMatch(sym, lengthExtra, dist, distExtra)
		}
		wire = binary.AppendUvarint(wire, uint64(tokens[i]))
	}
	for offset := range 8 {
		var want bytes.Buffer
		block := repDFBlock{Kind: repDFFixed, Final: true}
		w := repDFNewStreamWriterAt(&want, int64(offset))
		mustOK(t, w.Begin(&block))
		for _, token := range tokens {
			mustOK(t, w.Token(token))
		}
		mustOK(t, w.End())
		bitEnd := int(w.BitPos())
		mustOK(t, w.Finish(0, uint8((8-bitEnd%8)%8)))
		frame := translateFrame{Start: offset, End: bitEnd,
			Header: repIRBlockMeta{Kind: repDFFixed, Final: true, NTokens: count}}
		got, err := translateReplayFrame(wire, frame, nil, nil, false)
		mustOK(t, err)
		if !bytes.Equal(got, want.Bytes()) {
			t.Fatalf("translated token sequence differs at bit offset %d", offset)
		}
	}
}
