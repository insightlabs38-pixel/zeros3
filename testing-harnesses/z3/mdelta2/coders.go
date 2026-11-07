package mdelta2

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"io"

	"zeros3-z3/tdelta"
)

// Residual coder identifiers (stored in the container).
const (
	CRaw     = 0 // residual words verbatim
	CFlate   = 1 // DEFLATE of byte planes (the production-style baseline)
	CNRC     = 2 // bit-length rANS with exponent conditioning (this work)
	CRice    = 3 // adaptive Golomb-Rice
	CBPRLE   = 4 // bitplane run-length
	CPXR     = 5 // existing adaptive range coder (only for XOR residuals)
	CFPE     = 6 // exponent rANS + raw sign/mantissa bits (first-sight coder for raw float words)
	numCoder = 7
)

var CoderNames = [...]string{"raw", "flate-planes", "nrc-rans", "rice", "bitplane-rle", "pxr", "fpe"}

func inflateLimit(b []byte, limit int) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(b)), int64(limit)+1))
	if err != nil || len(out) > limit {
		return nil, ErrCorrupt
	}
	return out, nil
}

// EncodeResidual codes residual words; pred is the (decoder-known) prediction for context coders
// and res says how the residual was formed (PXR needs XOR).
func EncodeResidual(c int, d DType, res Residual, resid, pred []byte) ([]byte, bool) {
	w := d.Width()
	switch c {
	case CRaw:
		return append([]byte(nil), resid...), true
	case CFlate:
		return Deflate(bytes.Join(Planes(resid, w), nil)), true
	case CNRC:
		return NRCEncode(d, resid, pred), true
	case CRice:
		return riceEncode(w, resid), true
	case CBPRLE:
		return bprleEncode(w, resid), true
	case CFPE:
		if res != RXor {
			return nil, false
		}
		tgt := make([]byte, len(resid))
		for i := range tgt {
			tgt[i] = resid[i] ^ pred[i]
		}
		return fpeEncode(d, tgt)
	case CPXR:
		if res != RXor {
			return nil, false
		}
		tgt := make([]byte, len(resid))
		for i := range tgt {
			tgt[i] = resid[i] ^ pred[i]
		}
		p, err := tdelta.EncodePXR(pred, tgt, w)
		return p, err == nil
	}
	return nil, false
}

// DecodeResidual inverts EncodeResidual for n elements.
func DecodeResidual(c int, d DType, res Residual, payload, pred []byte, n int) ([]byte, error) {
	w := d.Width()
	switch c {
	case CRaw:
		if len(payload) != n*w {
			return nil, ErrCorrupt
		}
		return append([]byte(nil), payload...), nil
	case CFlate:
		raw, err := inflateLimit(payload, n*w)
		if err != nil || len(raw) != n*w {
			return nil, ErrCorrupt
		}
		pl := make([][]byte, w)
		for k := range pl {
			pl[k] = raw[k*n : (k+1)*n]
		}
		return Unplanes(pl, w), nil
	case CNRC:
		return NRCDecode(d, payload, pred, n)
	case CRice:
		return riceDecode(w, payload, n)
	case CBPRLE:
		return bprleDecode(w, payload, n)
	case CFPE:
		if res != RXor {
			return nil, ErrCorrupt
		}
		t, err := fpeDecode(d, payload, n)
		if err != nil {
			return nil, err
		}
		for i := range t {
			t[i] ^= pred[i]
		}
		return t, nil
	case CPXR:
		if res != RXor {
			return nil, ErrCorrupt
		}
		t, err := tdelta.DecodePXR(pred, payload, n*w, w)
		if err != nil || len(t) != n*w {
			return nil, ErrCorrupt
		}
		for i := range t {
			t[i] ^= pred[i]
		}
		return t, nil
	}
	return nil, ErrCorrupt
}

// ---- adaptive Golomb-Rice: blocks of 64 words, 5-bit k per block, unary quotient with escape.

type bitW struct {
	b   []byte
	acc uint64
	n   uint
}

func (w *bitW) put(v uint64, nb uint) { // nb <= 32
	w.acc |= v << w.n
	w.n += nb
	for w.n >= 8 {
		w.b = append(w.b, byte(w.acc))
		w.acc >>= 8
		w.n -= 8
	}
}
func (w *bitW) flush() []byte {
	if w.n > 0 {
		w.b = append(w.b, byte(w.acc))
	}
	return w.b
}

type bitR struct {
	b   []byte
	pos uint64
}

func (r *bitR) get(nb uint) (uint64, bool) {
	if r.pos+uint64(nb) > uint64(len(r.b))*8 {
		return 0, false
	}
	var v uint64
	for i := uint(0); i < nb; {
		byteIdx, bit := r.pos>>3, uint(r.pos&7)
		take := 8 - bit
		if take > nb-i {
			take = nb - i
		}
		v |= uint64(r.b[byteIdx]>>bit&(1<<take-1)) << i
		i += take
		r.pos += uint64(take)
	}
	return v, true
}

const riceBlock = 64
const riceEsc = 24

func riceEncode(w int, resid []byte) []byte {
	n := len(resid) / w
	bw := &bitW{}
	for s := 0; s < n; s += riceBlock {
		e := s + riceBlock
		if e > n {
			e = n
		}
		var sum uint64
		for i := s; i < e; i++ {
			sum += uint64(le(resid, w, i))
		}
		mean := sum / uint64(e-s)
		k := uint(0)
		for mean>>k > 1 && k < 31 {
			k++
		}
		bw.put(uint64(k), 5)
		for i := s; i < e; i++ {
			v := le(resid, w, i)
			q := v >> k
			if q >= riceEsc {
				for j := 0; j < riceEsc; j++ {
					bw.put(1, 1)
				}
				bw.put(uint64(v), 32)
				continue
			}
			for j := uint32(0); j < q; j++ {
				bw.put(1, 1)
			}
			bw.put(0, 1)
			if k > 0 {
				bw.put(uint64(v&(1<<k-1)), k)
			}
		}
	}
	return bw.flush()
}

func riceDecode(w int, payload []byte, n int) ([]byte, error) {
	out := make([]byte, n*w)
	r := &bitR{b: payload}
	for s := 0; s < n; s += riceBlock {
		e := s + riceBlock
		if e > n {
			e = n
		}
		kk, ok := r.get(5)
		if !ok {
			return nil, ErrCorrupt
		}
		k := uint(kk)
		for i := s; i < e; i++ {
			q := uint32(0)
			for {
				bit, ok := r.get(1)
				if !ok {
					return nil, ErrCorrupt
				}
				if bit == 0 {
					break
				}
				q++
				if q == riceEsc {
					break
				}
			}
			var v uint64
			if q == riceEsc {
				v, ok = r.get(32)
				if !ok || v > uint64(mask(w)) {
					return nil, ErrCorrupt
				}
			} else {
				v = uint64(q) << k
				if k > 0 {
					lo, ok := r.get(k)
					if !ok {
						return nil, ErrCorrupt
					}
					v |= lo
				}
				if v > uint64(mask(w)) {
					return nil, ErrCorrupt
				}
			}
			putLE(out, w, i, uint32(v))
		}
	}
	return out, nil
}

// ---- bitplane run-length: per plane (MSB first) a run count, then alternating 0/1 run lengths.

func bprleEncode(w int, resid []byte) []byte {
	n := len(resid) / w
	var out []byte
	for p := 8*w - 1; p >= 0; p-- {
		var runs []uint64
		cur, run := uint32(0), uint64(0)
		for i := 0; i < n; i++ {
			b := le(resid, w, i) >> uint(p) & 1
			if b == cur {
				run++
				continue
			}
			runs = append(runs, run)
			cur, run = b, 1
		}
		runs = append(runs, run)
		out = binary.AppendUvarint(out, uint64(len(runs)))
		for _, r := range runs {
			out = binary.AppendUvarint(out, r)
		}
	}
	return out
}

func bprleDecode(w int, payload []byte, n int) ([]byte, error) {
	out := make([]byte, n*w)
	pos := 0
	for p := 8*w - 1; p >= 0; p-- {
		cnt, k := binary.Uvarint(payload[pos:])
		if k <= 0 || cnt > uint64(n)+1 {
			return nil, ErrCorrupt
		}
		pos += k
		cur, i := uint32(0), 0
		for r := uint64(0); r < cnt; r++ {
			run, k := binary.Uvarint(payload[pos:])
			if k <= 0 || run > uint64(n-i) {
				return nil, ErrCorrupt
			}
			pos += k
			if cur == 1 {
				for j := i; j < i+int(run); j++ {
					putLE(out, w, j, le(out, w, j)|1<<uint(p))
				}
			}
			i += int(run)
			cur ^= 1
		}
		if i != n {
			return nil, ErrCorrupt
		}
	}
	if pos != len(payload) {
		return nil, ErrCorrupt
	}
	return out, nil
}
