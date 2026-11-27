package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"testing"
	"time"
)

const gf2MaxPlain = 64 << 20

// This witness preserves actual match/literal boundaries. Only the distance
// field is predicted, in constant time from previously witnessed occurrences.
// A table is reset at each compressed block, so seeks do not need its history.
type gf2DistancePredictor [1024]uint32

func (p *gf2DistancePredictor) key(plain []byte, start, sym int) int {
	a, b, c := uint32(plain[start]), uint32(plain[start+1]), uint32(plain[start+2])
	hash := (a | b<<8 | c<<16) ^ uint32(sym)*0x9e3779b9
	return int(hash * 0x9e3779b9 >> 22)
}

type gf2Witness struct {
	plain, payload []byte
}

func gf2SplitGzip(raw []byte) (int, *repDFStream, error) {
	off, err := gzipHeaderLen(raw)
	if err != nil || len(raw) < off+8 || len(raw) > gf2MaxObject {
		return 0, nil, gf2BadProgram
	}
	repDFinitFixed()
	st := &repDFStream{}
	p := repDFparser{br: repDFbitReader{data: raw[off : len(raw)-8]}, opt: repDFOptions{MaxOutput: gf2MaxPlain, MaxBlocks: 4096}.norm(), s: st}
	if err := p.run(); err != nil {
		return 0, nil, err
	}
	trailer := raw[len(raw)-8:]
	if crc32.ChecksumIEEE(st.Plain) != binary.LittleEndian.Uint32(trailer) ||
		uint32(len(st.Plain)) != binary.LittleEndian.Uint32(trailer[4:]) {
		return 0, nil, gf2BadProgram
	}
	return off, st, nil
}

func gf2WitnessBlock(block repDFBlock, plain []byte, predicted bool) []byte {
	literals, matches, distances := []byte{}, []byte{}, []byte{}
	count, at, run := 0, 0, 0
	var pred gf2DistancePredictor
	for _, token := range block.Tokens {
		if token.IsLiteral() {
			run++
			at++
			continue
		}
		literals = binary.AppendUvarint(literals, uint64(run))
		matches = binary.AppendUvarint(matches, uint64(uint32(token)&16383)-257)
		dist := uint32(token) >> 14
		residual := dist
		if predicted {
			key := pred.key(plain, at, token.Sym())
			residual ^= pred[key]
			pred[key] = dist
		}
		distances = binary.AppendUvarint(distances, uint64(residual))
		at += token.Length()
		count++
		run = 0
	}
	out := binary.AppendUvarint(nil, uint64(len(plain)))
	out = binary.AppendUvarint(out, uint64(block.EndBit-block.StartBit))
	out = binary.AppendUvarint(out, uint64(count))
	out = gf2Field(out, literals)
	out = gf2Field(out, matches)
	return gf2Field(out, distances)
}

func gf2Field(dst, value []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

func gf2CompileWitness(raw []byte, predicted bool) (gf2Witness, error) {
	start, st, err := gf2SplitGzip(raw)
	if err != nil {
		return gf2Witness{}, err
	}
	sk := repIRSkeleton{
		Codec: repIRE1, TailPadLen: st.TailPadLen, TailPadBits: st.TailPadBits,
		StreamLen: st.Consumed, CRC: crc32.ChecksumIEEE(raw[start : len(raw)-8]),
	}
	var all []byte
	for _, b := range st.Blocks {
		m := repIRBlockMeta{Final: b.Final, Kind: b.Kind, PadBits: b.PadBits, PadLen: b.PadLen, NTokens: len(b.Tokens),
			HLIT: b.HLIT, HDIST: b.HDIST, HCLEN: b.HCLEN, CLLens: b.CLLens, CLSyms: b.CLSyms}
		if b.Kind == repDFStored {
			m.StoredLen = b.PlainLen
		}
		if len(b.Tokens) != 0 {
			sk.Units = append(sk.Units, repIRUnitIdx{PlainLen: b.PlainLen, NTokens: len(b.Tokens)})
		}
		sk.Blocks = append(sk.Blocks, m)
		plan := gf2WitnessBlock(b, st.Plain[b.PlainOff:b.PlainOff+b.PlainLen], predicted)
		all = gf2Field(all, plan)
	}
	out := []byte{0}
	if predicted {
		out[0] = 1
	}
	out = gf2Field(out, raw[:start])
	out = append(out, raw[len(raw)-8:]...)
	out = gf2Field(out, sk.Marshal())
	return gf2Witness{st.Plain, append(out, all...)}, nil
}

type gf2Cursor struct {
	data []byte
	bad  bool
}

func (c *gf2Cursor) value(limit int) int {
	v, n := binary.Uvarint(c.data)
	if n <= 0 || v > uint64(limit) {
		c.bad = true
		return 0
	}
	c.data = c.data[n:]
	return int(v)
}
func (c *gf2Cursor) field() []byte {
	n := c.value(len(c.data))
	if n > len(c.data) {
		c.bad = true
		return nil
	}
	v := c.data[:n]
	c.data = c.data[n:]
	return v
}

func gf2ReplayWitnessBlock(w *repDFStreamWriter, b *repDFBlock, plain []byte, tokens int, columns [3]gf2Cursor, predicted bool, n int, start int) error {
	if err := w.Begin(b); err != nil {
		return err
	}
	if b.Kind == repDFStored {
		if n != 0 || tokens != 0 || len(columns[0].data)+len(columns[1].data)+len(columns[2].data) != 0 {
			return gf2BadProgram
		}
		return w.Stored(b, plain)
	}
	var pred gf2DistancePredictor
	pos := 0
	for range n {
		lits := columns[0].value(len(plain) - pos)
		if columns[0].bad || len(plain)-pos-lits < 3 || lits >= tokens {
			return gf2BadProgram
		}
		for _, literal := range plain[pos : pos+lits] {
			if err := w.Token(repDFLiteral(literal)); err != nil {
				return err
			}
		}
		pos += lits
		tokens -= lits + 1
		match := columns[1].value(16383-257) + 257
		dist := columns[2].value((1 << 18) - 1)
		sym := match & 511
		if columns[1].bad || columns[2].bad || sym < 257 || sym > 285 {
			return gf2BadProgram
		}
		if predicted {
			key := pred.key(plain, pos, sym)
			dist ^= int(pred[key])
			pred[key] = uint32(dist)
		}
		token := repDFToken(uint32(match) | uint32(dist)<<14)
		dc := token.DistCode()
		if dc >= 30 || token.LenExtra() >= (1<<repDFlenExtra[sym-257]) ||
			token.DistExtra() >= (1<<repDFdistExtr[dc]) {
			return gf2BadProgram
		}
		length := token.Length()
		if length > len(plain)-pos || token.Distance() > start+pos {
			return gf2BadProgram
		}
		if err := w.Token(token); err != nil {
			return err
		}
		pos += length
	}
	for _, col := range columns {
		if col.bad || len(col.data) != 0 {
			return gf2BadProgram
		}
	}
	if len(plain)-pos != tokens {
		return gf2BadProgram
	}
	for _, literal := range plain[pos:] {
		if err := w.Token(repDFLiteral(literal)); err != nil {
			return err
		}
	}
	return w.End()
}

func gf2BlockMeta(m repIRBlockMeta) repDFBlock {
	b := repDFBlock{Final: m.Final, Kind: m.Kind, PadBits: m.PadBits, PadLen: m.PadLen,
		HLIT: m.HLIT, HDIST: m.HDIST, HCLEN: m.HCLEN, CLLens: m.CLLens, CLSyms: m.CLSyms}
	if m.Kind == repDFDynamic {
		b.LitLens, b.DistLens = repIRExpandLengths(&m)
	}
	return b
}

func gf2ReplayWitness(plain, payload []byte) ([]byte, error) {
	if len(payload) < 2 || payload[0] > 1 || len(plain) > gf2MaxPlain {
		return nil, gf2BadProgram
	}
	predicted := payload[0] == 1
	c := gf2Cursor{data: payload[1:]}
	header := c.field()
	if c.bad || len(c.data) < 8 {
		return nil, gf2BadProgram
	}
	if off, err := gzipHeaderLen(header); err != nil || off != len(header) {
		return nil, gf2BadProgram
	}
	trailer := c.data[:8]
	c.data = c.data[8:]
	if crc32.ChecksumIEEE(plain) != binary.LittleEndian.Uint32(trailer) ||
		uint32(len(plain)) != binary.LittleEndian.Uint32(trailer[4:]) {
		return nil, gf2BadProgram
	}
	sk, err := repIRParseSkeleton(c.field(), len(plain))
	if c.bad || err != nil || sk.Codec != repIRE1 || len(sk.Blocks) == 0 ||
		len(sk.Blocks) > 4096 || sk.StreamLen > gf2MaxObject {
		return nil, gf2BadProgram
	}
	var out bytes.Buffer
	out.Grow(len(header) + sk.StreamLen + 8)
	out.Write(header)
	w := repDFNewStreamWriter(&out)
	at := 0
	for _, m := range sk.Blocks {
		if m.Pred || m.AutoPad {
			return nil, gf2BadProgram
		}
		pc := gf2Cursor{data: c.field()}
		count := pc.value(len(plain) - at)
		bits := pc.value(sk.StreamLen * 8)
		n := pc.value(m.NTokens)
		cols := [3]gf2Cursor{{data: pc.field()}, {data: pc.field()}, {data: pc.field()}}
		if c.bad || pc.bad || len(pc.data) != 0 || (m.Kind == repDFStored && count != m.StoredLen) {
			return nil, gf2BadProgram
		}
		b := gf2BlockMeta(m)
		before := w.BitPos()
		if err := gf2ReplayWitnessBlock(w, &b, plain[at:at+count], m.NTokens, cols, predicted, n, at); err != nil {
			return nil, err
		}
		if w.BitPos()-before != int64(bits) {
			return nil, gf2BadProgram
		}
		at += count
	}
	if len(c.data) != 0 || at != len(plain) {
		return nil, gf2BadProgram
	}
	if err := w.Finish(sk.TailPadBits, sk.TailPadLen); err != nil {
		return nil, err
	}
	if out.Len() != len(header)+sk.StreamLen || crc32.ChecksumIEEE(out.Bytes()[len(header):]) != sk.CRC {
		return nil, gf2BadProgram
	}
	out.Write(trailer)
	return out.Bytes(), nil
}

func TestGF2FastWitness(t *testing.T) {
	plain := gf2TarFixture(t, false)
	for _, level := range []int{-2, 0, 1, 6, 9} {
		for _, predict := range []bool{false, true} {
			raw := gf2GzipFixture(t, plain, level, 131<<10, "independent.tar")
			x, err := gf2CompileWitness(raw, predict)
			mustOK(t, err)
			output, err := gf2ReplayWitness(x.plain, x.payload)
			mustOK(t, err)
			if !bytes.Equal(output, raw) {
				t.Fatal("witness altered bytes")
			}
			if _, err := gf2ReplayWitness(x.plain, x.payload[:len(x.payload)-1]); err == nil {
				t.Fatal("accepted truncated witness")
			}
			for _, n := range []int{1, 97, 4096} {
				n = min(n, len(raw))
				for i := range 11 {
					start := (len(raw) - n) * i / 10
					load := func(off, count int) ([]byte, error) { return x.plain[off : off+count], nil }
					got, err := gf2ReplayWitnessRange(len(x.plain), x.payload, start, n, load)
					if err != nil {
						t.Fatalf("witness range level=%d offset=%d size=%d: %v", level, start, n, err)
					}
					if !bytes.Equal(got, raw[start:start+n]) {
						t.Fatalf("witness range differs at %d + %d", start, n)
					}
				}
			}
		}
	}
}

func TestGF2WitnessPanel(t *testing.T) {
	root, families := gf0Corpus(t)
	for _, fam := range families {
		if fam.Name != "typescript-gzip" {
			continue
		}
		for _, predict := range []bool{false, true} {
			t.Run(fmt.Sprintf("predicted-%t", predict), func(t *testing.T) {
				l := newAuthLab(t)
				l.s.writeBinaryManifests = true
				var raw [][]byte
				var witnessBytes int
				begin, cpu := time.Now(), fb13CPU()
				for i := range fam.Paths {
					source, _ := gf0ReadFixture(t, root, fam, i)
					raw = append(raw, source)
					x, err := gf2CompileWitness(source, predict)
					mustOK(t, err)
					l.put(fmt.Sprintf("plain/%d", i), x.plain)
					l.put(fmt.Sprintf("witness/%d", i), x.payload)
					witnessBytes += len(x.payload)
				}
				ctx := context.Background()
				_, err := l.s.finishAuthorityArchive(ctx, 0)
				mustOK(t, err)
				mustOK(t, l.s.ensureFeature(agFeature))
				result, err := l.s.AuthorityOptimize(ctx, OptimizePolicy{AuthPolicy: AuthPolicy{
					Prefix: "plain/", Apply: true, Order: "oldest", AllowDelta: true, MaxDepth: 2}, Profile: "archive"})
				mustOK(t, err)
				_, err = l.s.finishAuthorityArchive(ctx, 0)
				mustOK(t, err)
				buildWall, buildCPU := time.Since(begin).Nanoseconds(), int64(fb13CPU()-cpu)
				l.close()
				l.open()
				times := make([]int64, 0, 12)
				cpu = fb13CPU()
				for range 6 {
					for i, source := range raw {
						begin = time.Now()
						pKey, wKey := fmt.Sprintf("plain/%d", i), fmt.Sprintf("witness/%d", i)
						pSize, wSize := l.man(pKey).TotalLength, l.man(wKey).TotalLength
						p, w := l.read(pKey, byteRange{0, pSize - 1}), l.read(wKey, byteRange{0, wSize - 1})
						got, err := gf2ReplayWitness(p, w)
						mustOK(t, err)
						if sha256.Sum256(got) != sha256.Sum256(source) || !bytes.Equal(got, source) {
							t.Fatal("inexact gzip")
						}
						times = append(times, time.Since(begin).Nanoseconds())
					}
				}
				readCPU := int64(fb13CPU() - cpu)
				var rangeTimes []int64
				cpu = fb13CPU()
				for i, source := range raw {
					pKey, wKey := fmt.Sprintf("plain/%d", i), fmt.Sprintf("witness/%d", i)
					pSize, wSize := l.man(pKey).TotalLength, l.man(wKey).TotalLength
					w := l.read(wKey, byteRange{0, wSize - 1})
					load := func(off, count int) ([]byte, error) {
						if count == 0 {
							return nil, nil
						}
						return l.read(pKey, byteRange{int64(off), int64(off + count - 1)}), nil
					}
					for k := range 16 {
						at := (len(source) - 4096) * k / 15
						begin = time.Now()
						got, err := gf2ReplayWitnessRange(int(pSize), w, at, 4096, load)
						mustOK(t, err)
						rangeTimes = append(rangeTimes, time.Since(begin).Nanoseconds())
						if !bytes.Equal(got, source[at:at+4096]) {
							t.Fatal("inexact gzip range")
						}
					}
				}
				rangeCPU := int64(fb13CPU() - cpu)
				l.verifyOK("GF2 fast witness component store")
				l.close()
				total, pack, _ := fb13Allocation(t, l.dir)
				row, err := json.Marshal(map[string]any{"family": fam.Name, "predicted_distances": predict, "witness_raw_bytes": witnessBytes,
					"allocated": total, "pack_allocated": pack, "build_wall_ns": buildWall, "build_cpu_ns": buildCPU,
					"whole_p50_p95_p99_ns": gf0Percentiles(times), "whole_cpu_ns": readCPU, "plain_rewrites": result.Applied, "range_p50_p95_p99_ns": gf0Percentiles(rangeTimes), "range_cpu_ns": rangeCPU})
				mustOK(t, err)
				fmt.Printf("GF2WITNESS %s\n", row)
			})
		}
	}
}

// Replay one requested byte interval. Block geometry advances from the
// immutable witness table. Only intersecting blocks read their plaintext.
// A block's first/last byte can share bits with adjacent blocks.
func gf2ReplayWitnessRange(plainSize int, payload []byte, start, length int, load func(int, int) ([]byte, error)) ([]byte, error) {
	if len(payload) < 2 || payload[0] > 1 || plainSize < 0 || plainSize > gf2MaxPlain ||
		start < 0 || length < 0 || start > gf2MaxObject || length > gf2MaxObject-start {
		return nil, gf2BadProgram
	}
	predicted := payload[0] == 1
	c := gf2Cursor{data: payload[1:]}
	header := c.field()
	if c.bad || len(c.data) < 8 {
		return nil, gf2BadProgram
	}
	if off, err := gzipHeaderLen(header); err != nil || off != len(header) {
		return nil, gf2BadProgram
	}
	trailer := c.data[:8]
	c.data = c.data[8:]
	sk, err := repIRParseSkeleton(c.field(), plainSize)
	if c.bad || err != nil || sk.Codec != repIRE1 || len(sk.Blocks) == 0 ||
		len(sk.Blocks) > 4096 || sk.StreamLen > gf2MaxObject || start+length > len(header)+sk.StreamLen+8 ||
		binary.LittleEndian.Uint32(trailer[4:]) != uint32(plainSize) {
		return nil, gf2BadProgram
	}
	out := make([]byte, length)
	copyBytes := func(pos int, data []byte) {
		lo, hi := max(start, pos), min(start+length, pos+len(data))
		if lo < hi {
			copy(out[lo-start:hi-start], data[lo-pos:hi-pos])
		}
	}
	copyBytes(0, header)
	copyBytes(len(header)+sk.StreamLen, trailer)
	var bitPos, plainPos int
	from, to := (start-len(header))*8, (start+length-len(header))*8
	for _, m := range sk.Blocks {
		if m.Pred || m.AutoPad {
			return nil, gf2BadProgram
		}
		pc := gf2Cursor{data: c.field()}
		count := pc.value(plainSize - plainPos)
		bits := pc.value(sk.StreamLen*8 - bitPos)
		n := pc.value(m.NTokens)
		if c.bad || pc.bad || bits < 3 || (m.Kind == repDFStored && count != m.StoredLen) {
			return nil, gf2BadProgram
		}
		if bitPos < to && bitPos+bits > from {
			cols := [3]gf2Cursor{{data: pc.field()}, {data: pc.field()}, {data: pc.field()}}
			if pc.bad || len(pc.data) != 0 {
				return nil, gf2BadProgram
			}
			plain, err := load(plainPos, count)
			if err != nil {
				return nil, err
			}
			if len(plain) != count {
				return nil, gf2BadProgram
			}
			b := gf2BlockMeta(m)
			var block bytes.Buffer
			offset := bitPos % 8
			w := repDFNewStreamWriterAt(&block, int64(offset))
			if err := gf2ReplayWitnessBlock(w, &b, plain, m.NTokens, cols, predicted, n, plainPos); err != nil {
				return nil, err
			}
			if w.BitPos() != int64(offset+bits) {
				return nil, gf2BadProgram
			}
			pad := (8 - (offset+bits)%8) % 8
			if err := w.Finish(0, uint8(pad)); err != nil {
				return nil, err
			}
			// The artificial leading and trailing bits are zero. OR joins
			// independent writes meeting in a byte without changing either.
			base := len(header) + bitPos/8
			for i := max(0, start-base); i < min(block.Len(), start+length-base); i++ {
				out[base+i-start] |= block.Bytes()[i]
			}
		}
		bitPos += bits
		plainPos += count
	}
	if len(c.data) != 0 || plainPos != plainSize || bitPos+int(sk.TailPadLen) != sk.StreamLen*8 {
		return nil, gf2BadProgram
	}
	if sk.TailPadLen > 0 {
		last := len(header) + sk.StreamLen - 1 - start
		if last >= 0 && last < len(out) {
			out[last] |= sk.TailPadBits << (bitPos % 8)
		}
	}
	return out, nil
}
