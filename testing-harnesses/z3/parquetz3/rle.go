package parquetz3

import (
	"encoding/binary"
	"math/bits"
)

func appendUv(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

// readUv decodes a minimal uvarint; k <= 0 reports a short, overlong or non-minimal one.
func readUv(b []byte) (uint64, int) {
	v, k := binary.Uvarint(b)
	if k > 0 && k != uvLen(v) {
		return 0, -1
	}
	return v, k
}

func uvLen(v uint64) int { return (bits.Len64(v|1) + 6) / 7 }

// ---- the RLE / bit-packed hybrid (levels, dictionary indices, RLE booleans) ----------

// hybridDecode decodes up to n values of bit width bw from b. It returns the values, the
// run headers exactly as stored (so an unusual layout can be replayed) and the bytes used.
// The final bit-packed run may carry padding values past n (a producer may pad to a full
// block with stale data, not only to a group of eight); they are returned in pad.
func hybridDecode(b []byte, bw, n int) (vals []uint32, hdrs []uint64, pad []uint32, used int, err error) {
	if bw < 0 || bw > 32 {
		return nil, nil, nil, 0, corrupt("bit width %d", bw)
	}
	vals = make([]uint32, 0, min(n, 1<<16))
	vb := (bw + 7) / 8
	for len(vals) < n {
		h, k := readUv(b[used:])
		if k <= 0 {
			return nil, nil, nil, 0, corrupt("run header")
		}
		if h > 1<<32-1 {
			return nil, nil, nil, 0, corrupt("run header %d", h)
		}
		used += k
		hdrs = append(hdrs, h)
		if h&1 == 1 {
			groups := int(h >> 1)
			if groups == 0 {
				return nil, nil, nil, 0, corrupt("empty bit-packed run")
			}
			need := groups * bw
			if need > len(b)-used {
				return nil, nil, nil, 0, corrupt("bit-packed run of %d groups in %d bytes", groups, len(b)-used)
			}
			p := b[used : used+need]
			used += need
			total := groups * 8
			if bw == 0 { // zero-width values carry no bytes: nothing to loop over beyond n
				for len(vals) < n && total > 0 {
					vals = append(vals, 0)
					total--
				}
				continue
			}
			for i := 0; i < total; i++ {
				v := unpack(p, i, bw)
				if len(vals) < n {
					vals = append(vals, v)
				} else {
					pad = append(pad, v)
				}
			}
			continue
		}
		cnt := int(h >> 1)
		if cnt == 0 {
			return nil, nil, nil, 0, corrupt("empty RLE run")
		}
		if vb > len(b)-used {
			return nil, nil, nil, 0, corrupt("RLE run value")
		}
		var v uint32
		for i := 0; i < vb; i++ {
			v |= uint32(b[used+i]) << (8 * uint(i))
		}
		if bw < 32 && v>>uint(bw) != 0 {
			return nil, nil, nil, 0, corrupt("RLE value %d exceeds %d bits", v, bw)
		}
		used += vb
		take := min(cnt, n-len(vals))
		for i := 0; i < take; i++ {
			vals = append(vals, v)
		}
		if cnt > take {
			return nil, nil, nil, 0, corrupt("RLE run overshoots the value count")
		}
	}
	return vals, hdrs, pad, used, nil
}

// unpack reads value i of width bw (LSB-first) from p.
func unpack(p []byte, i, bw int) uint32 {
	var v uint64
	bit := i * bw
	for j := 0; j < bw; {
		by := p[(bit+j)>>3]
		off := (bit + j) & 7
		take := min(8-off, bw-j)
		v |= uint64((by>>uint(off))&(1<<uint(take)-1)) << uint(j)
		j += take
	}
	return uint32(v)
}

// bitWriter appends values LSB-first.
type bitWriter struct {
	out  []byte
	acc  uint64
	nacc int
}

func (w *bitWriter) put(v uint32, bw int) {
	w.acc |= uint64(v) << uint(w.nacc)
	w.nacc += bw
	for w.nacc >= 8 {
		w.out = append(w.out, byte(w.acc))
		w.acc >>= 8
		w.nacc -= 8
	}
}

func (w *bitWriter) flush() {
	if w.nacc > 0 {
		w.out = append(w.out, byte(w.acc))
		w.acc, w.nacc = 0, 0
	}
}

// rleEnc is the parquet-cpp/Arrow RleEncoder: values are buffered eight at a time, a run
// of at least eight equal values is written RLE, everything else bit-packed in literal
// runs of at most 63 groups behind a one-byte indicator. It is a PREDICTOR: a stream is
// recorded as "standard" only when this encoder reproduced it exactly.
type rleEnc struct {
	bw       int
	w        bitWriter
	buf      [8]uint32
	nbuf     int
	cur      uint32
	repeat   int
	literal  int
	indicate int // index in w.out of the reserved indicator byte, -1 if none
}

func newRLEEnc(bw int) *rleEnc { return &rleEnc{bw: bw, indicate: -1} }

func (e *rleEnc) put(v uint32) {
	if e.cur == v {
		e.repeat++
		if e.repeat > 8 {
			return
		}
	} else {
		if e.repeat >= 8 {
			e.flushRepeated()
		}
		e.repeat = 1
		e.cur = v
	}
	e.buf[e.nbuf] = v
	e.nbuf++
	if e.nbuf == 8 {
		e.flushBuffered(false)
	}
}

func (e *rleEnc) flushBuffered(done bool) {
	if e.repeat >= 8 {
		e.nbuf = 0
		if e.literal != 0 {
			e.flushLiteral(true)
		}
		return
	}
	e.literal += e.nbuf
	groups := e.literal / 8
	if groups+1 >= 1<<6 {
		e.flushLiteral(true)
	} else {
		e.flushLiteral(done)
	}
	e.repeat = 0
}

func (e *rleEnc) flushLiteral(update bool) {
	if e.indicate < 0 {
		e.indicate = len(e.w.out)
		e.w.out = append(e.w.out, 0)
	}
	for i := 0; i < e.nbuf; i++ {
		e.w.put(e.buf[i], e.bw)
	}
	e.nbuf = 0
	if update {
		groups := (e.literal + 7) / 8
		e.w.out[e.indicate] = byte(groups<<1 | 1)
		e.indicate = -1
		e.literal = 0
	}
}

func (e *rleEnc) flushRepeated() {
	e.w.out = binary.AppendUvarint(e.w.out, uint64(e.repeat)<<1)
	vb := (e.bw + 7) / 8
	for i := 0; i < vb; i++ {
		e.w.out = append(e.w.out, byte(e.cur>>(8*uint(i))))
	}
	e.nbuf = 0
	e.repeat = 0
}

func (e *rleEnc) finish() []byte {
	if e.literal > 0 || e.repeat > 0 || e.nbuf > 0 {
		allRepeat := e.literal == 0 && (e.repeat == e.nbuf || e.nbuf == 0)
		if e.repeat > 0 && allRepeat {
			e.flushRepeated()
		} else {
			for ; e.nbuf != 0 && e.nbuf < 8; e.nbuf++ {
				e.buf[e.nbuf] = 0
			}
			e.literal += e.nbuf
			e.flushLiteral(true)
			e.repeat = 0
		}
	}
	e.w.flush()
	return e.w.out
}

// hybridEncodeStd encodes vals with the standard predictor.
func hybridEncodeStd(vals []uint32, bw int) []byte {
	e := newRLEEnc(bw)
	for _, v := range vals {
		e.put(v)
	}
	return e.finish()
}

// hybridEncodeRuns encodes vals under explicit run headers (as returned by hybridDecode):
// a bit-packed header takes groups*8 values, then pad values, then zeros; an RLE header
// repeats the next value. It returns ok=false when the headers do not fit vals and pad.
func hybridEncodeRuns(vals []uint32, bw int, hdrs []uint64, pad []uint32) ([]byte, bool) {
	var w bitWriter
	vb := (bw + 7) / 8
	pos, pp := 0, 0
	for _, h := range hdrs {
		w.out = binary.AppendUvarint(w.out, h)
		if h&1 == 1 {
			total := int(h>>1) * 8
			if bw == 0 { // zero-width values occupy no bytes: only the count matters
				pos += min(total, len(vals)-pos)
				continue
			}
			for i := 0; i < total; i++ {
				v := uint32(0)
				switch {
				case pos < len(vals):
					v = vals[pos]
					pos++
				case pp < len(pad):
					v = pad[pp]
					pp++
				}
				w.put(v, bw)
			}
			w.flush()
			continue
		}
		cnt := int(h >> 1)
		if pos+cnt > len(vals) {
			return nil, false
		}
		v := vals[pos]
		for i := 1; i < cnt; i++ {
			if vals[pos+i] != v {
				return nil, false
			}
		}
		pos += cnt
		for i := 0; i < vb; i++ {
			w.out = append(w.out, byte(v>>(8*uint(i))))
		}
	}
	return w.out, pos == len(vals) && pp == len(pad)
}
