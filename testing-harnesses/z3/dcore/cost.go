package dcore

import (
	"compress/flate"
	"encoding/binary"
	"io"
	"math"
	"sync"
)

// This file holds the COPY/ADD program cost model: the exact (deflated)
// envelope size used as the oracle, the exact raw program length, and the
// cheap staged estimator used to decide which candidates deserve an exact
// encoding. The program syntax mirrors the production envelope's frames:
// ADD = 0 uvarint(n) bytes; COPY = 1 uvarint(base) uvarint(off) uvarint(n).

const (
	// LitSplit bounds one literal op, as the envelope does.
	LitSplit = 4 << 20
	// EnvelopeFixed approximates the fixed header: magic, version, size,
	// SHA-256, base count, frame count (bases and frames are charged below).
	EnvelopeFixed = 4 + 1 + 5 + 32 + 1 + 3
	BaseRefBytes  = 32 + 4
	// RecordBytes is the independent-record header + locator charge of B1.
	RecordBytes = 96
)

var flatePool = sync.Pool{New: func() any { w, _ := flate.NewWriter(io.Discard, 6); return w }}
var fastPool = sync.Pool{New: func() any { w, _ := flate.NewWriter(io.Discard, 1); return w }}

func deflateLen(pool *sync.Pool, b []byte) int {
	var cw countWriter
	w := pool.Get().(*flate.Writer)
	w.Reset(&cw)
	_, _ = w.Write(b)
	_ = w.Close()
	w.Reset(io.Discard)
	pool.Put(w)
	return cw.n
}

type countWriter struct{ n int }

func (c *countWriter) Write(p []byte) (int, error) { c.n += len(p); return len(p), nil }

func uvLen(v uint64) int {
	var b [binary.MaxVarintLen64]byte
	return binary.PutUvarint(b[:], v)
}

// usedBases returns first-use renumbering of the bases referenced by ops.
func usedBases(ops []Op) (remap map[int]int, order []int) {
	remap = map[int]int{}
	for _, o := range ops {
		if o.Base >= 0 {
			if _, ok := remap[o.Base]; !ok {
				remap[o.Base] = len(order)
				order = append(order, o.Base)
			}
		}
	}
	return
}

// Program serializes ops in the envelope's program syntax (not framed or
// compressed) and returns the base indexes it references in first-use order.
func Program(ops []Op) (prog []byte, bases []int) {
	remap, order := usedBases(ops)
	for _, o := range ops {
		if o.Base < 0 {
			for l := o.Lit; len(l) > 0; {
				n := min(len(l), LitSplit)
				prog = append(prog, 0)
				prog = binary.AppendUvarint(prog, uint64(n))
				prog = append(prog, l[:n]...)
				l = l[n:]
			}
			continue
		}
		prog = append(prog, 1)
		prog = binary.AppendUvarint(prog, uint64(remap[o.Base]))
		prog = binary.AppendUvarint(prog, uint64(o.Off))
		prog = binary.AppendUvarint(prog, uint64(o.N))
	}
	return prog, order
}

// ProgStats are exact structural counts of a program.
type ProgStats struct {
	Copies, Lits        int
	CopyBytes, LitBytes int64
	CopyProg, LitProg   int64 // raw program bytes of COPY ops / ADD ops (headers + literals)
	Bases               int
}

// Stats computes ProgStats without serializing.
func Stats(ops []Op) ProgStats {
	remap, order := usedBases(ops)
	var s ProgStats
	s.Bases = len(order)
	for _, o := range ops {
		if o.Base < 0 {
			for n := o.N; n > 0; {
				k := min(n, LitSplit)
				s.Lits++
				s.LitBytes += k
				s.LitProg += 1 + int64(uvLen(uint64(k))) + k
				n -= k
			}
			continue
		}
		s.Copies++
		s.CopyBytes += o.N
		s.CopyProg += 1 + int64(uvLen(uint64(remap[o.Base]))+uvLen(uint64(o.Off))+uvLen(uint64(o.N)))
	}
	return s
}

// RawProgramBytes is the exact uncompressed program length (an upper bound on
// any compressed frame payload).
func (s ProgStats) RawProgramBytes() int64 { return s.CopyProg + s.LitProg }

// EnvelopeSize is the exact oracle: the program is cut into frames covering at
// most frameSpan target bytes, each independently deflated (level 6) only when
// that is smaller, plus header, base references and frame table. This mirrors
// the production envelope's accounting without producing the bytes.
func EnvelopeSize(ops []Op, frameSpan int64) int64 {
	if frameSpan <= 0 {
		frameSpan = 128 << 10
	}
	prog, order := Program(ops)
	_ = prog
	remap := map[int]int{}
	for i, b := range order {
		remap[b] = i
	}
	total := int64(EnvelopeFixed) + int64(len(order))*BaseRefBytes
	var frame []byte
	var span int64
	frames := 0
	closeFrame := func() {
		if span == 0 {
			return
		}
		n := int64(len(frame))
		if z := int64(deflateLen(&flatePool, frame)); z < n {
			n = z
		}
		total += n + 1 + int64(uvLen(uint64(span))) + int64(uvLen(uint64(n)))
		frame, span = frame[:0], 0
		frames++
	}
	for _, o := range ops {
		if o.Base < 0 {
			for l := o.Lit; len(l) > 0; {
				n := min(len(l), LitSplit)
				frame = append(frame, 0)
				frame = binary.AppendUvarint(frame, uint64(n))
				frame = append(frame, l[:n]...)
				span += int64(n)
				l = l[n:]
				if span >= frameSpan || len(frame) >= LitSplit {
					closeFrame()
				}
			}
			continue
		}
		frame = append(frame, 1)
		frame = binary.AppendUvarint(frame, uint64(remap[o.Base]))
		frame = binary.AppendUvarint(frame, uint64(o.Off))
		frame = binary.AppendUvarint(frame, uint64(o.N))
		span += o.N
		if span >= frameSpan {
			closeFrame()
		}
	}
	closeFrame()
	return total + int64(uvLen(uint64(frames)))
}

// Estimate is the cheap staged cost estimate of a program.
type Estimate struct {
	Stats ProgStats
	// Upper is a hard upper bound: the raw program inside an uncompressed
	// envelope. The exact size can never exceed it.
	Upper int64
	// Est is the point estimate; Lower is a heuristic lower confidence value
	// (NOT a proof): it assumes the sampled literal ratio is Conf times better.
	Est, Lower int64
}

// EstimateOpts tunes the sampled literal-ratio estimator.
type EstimateOpts struct {
	SampleBytes int     // total literal bytes compressed for the sample (default 32 KiB)
	Slices      int     // evenly spaced slices of the literal stream (default 8)
	CopyRatio   float64 // expected deflate ratio of COPY program bytes (default 0.78)
	Conf        float64 // lower-bound factor on the literal ratio (default 0.6)
	Entropy     bool    // use order-0 entropy of the sample instead of deflate
}

func (o *EstimateOpts) defaults() {
	if o.SampleBytes <= 0 {
		o.SampleBytes = 32 << 10
	}
	if o.Slices <= 0 {
		o.Slices = 8
	}
	if o.CopyRatio <= 0 {
		o.CopyRatio = 0.78
	}
	if o.Conf <= 0 {
		o.Conf = 0.6
	}
}

// sampleLiterals concatenates evenly spaced slices of the literal stream.
func sampleLiterals(ops []Op, total int64, o EstimateOpts) []byte {
	if total <= int64(o.SampleBytes) {
		out := make([]byte, 0, total)
		for _, op := range ops {
			if op.Base < 0 {
				out = append(out, op.Lit...)
			}
		}
		return out
	}
	per := o.SampleBytes / o.Slices
	step := total / int64(o.Slices)
	out := make([]byte, 0, o.SampleBytes)
	var pos, next int64
	for _, op := range ops {
		if op.Base >= 0 {
			continue
		}
		end := pos + op.N
		for next < end && len(out) < o.SampleBytes {
			if next >= pos {
				from := next - pos
				out = append(out, op.Lit[from:min(from+int64(per), op.N)]...)
			}
			next += step
		}
		pos = end
	}
	return out
}

func entropyRatio(b []byte) float64 {
	if len(b) == 0 {
		return 1
	}
	var c [256]int
	for _, v := range b {
		c[v]++
	}
	var bitsum float64
	for _, n := range c {
		if n > 0 {
			p := float64(n) / float64(len(b))
			bitsum -= float64(n) * math.Log2(p)
		}
	}
	return math.Min(1, bitsum/8/float64(len(b))+0.01)
}

// EstimateProgram costs ops without producing a deflated envelope.
func EstimateProgram(ops []Op, o EstimateOpts) Estimate {
	o.defaults()
	s := Stats(ops)
	fixed := int64(EnvelopeFixed) + int64(s.Bases)*BaseRefBytes + 8
	e := Estimate{Stats: s, Upper: fixed + s.RawProgramBytes()}
	ratio := 1.0
	if s.LitBytes > 64 {
		smp := sampleLiterals(ops, s.LitBytes, o)
		if o.Entropy {
			ratio = entropyRatio(smp)
		} else if len(smp) > 0 {
			ratio = math.Min(1, float64(deflateLen(&fastPool, smp))/float64(len(smp)))
		}
	}
	lit := float64(s.LitProg-s.LitBytes) + float64(s.LitBytes)*ratio
	cp := float64(s.CopyProg) * o.CopyRatio
	e.Est = fixed + int64(lit+cp)
	e.Lower = fixed + int64(float64(s.LitProg-s.LitBytes)*0.5+float64(s.LitBytes)*ratio*o.Conf+cp*0.8*o.Conf/0.78)
	if e.Lower > e.Est {
		e.Lower = e.Est
	}
	if e.Est > e.Upper {
		e.Est = e.Upper
	}
	return e
}
