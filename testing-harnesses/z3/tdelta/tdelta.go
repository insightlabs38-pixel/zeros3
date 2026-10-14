// Package tdelta holds the simple, exact tensor-delta baselines of Z3-09H/I.
// Every codec turns (base, target) bytes of one aligned tensor into a payload
// and back, losslessly and without any floating-point arithmetic: the signal
// is read from XOR of bit patterns, never from values. These are
// characterisation tools, not a tensor codec.
package tdelta

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
)

// Codec identifies a representation.
type Codec uint8

const (
	XOR          Codec = iota + 1 // raw XOR bytes
	XORZ                          // DEFLATE(XOR)
	ByteDelta                     // DEFLATE(target - base, byte-wise mod 256)
	ZeroRLE                       // zero-run encoding of XOR (no entropy coder)
	Plane                         // DEFLATE of XOR split into byte planes
	Sparse                        // bitmap of changed elements + changed target elements, both DEFLATEd
	FloatSplit                    // sign / exponent / mantissa XOR streams, each DEFLATEd (F32, F16, BF16)
	Replace                       // DEFLATE(target) alone (the no-base control)
	ReplacePlane                  // DEFLATE of the byte-plane-split target alone (no-base control for the plane codecs)
	NumCodecs
)

var Names = [...]string{"", "xor", "xor+z", "bytedelta+z", "zero-rle", "plane-xor+z", "sparse", "float-split+z", "replace+z", "plane+z alone"}

func (c Codec) String() string {
	if int(c) < len(Names) {
		return Names[c]
	}
	return fmt.Sprintf("codec%d", c)
}

var ErrCorrupt = errors.New("tdelta: corrupt payload")

func deflateBytes(b []byte) []byte {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

func inflateBytes(b []byte, limit int) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(b)), int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(out) > limit {
		return nil, ErrCorrupt
	}
	return out, nil
}

func xor(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// width falls back to 1 when n is not a multiple of w.
func width(n, w int) int {
	if w < 1 || n%w != 0 {
		return 1
	}
	return w
}

// Encode returns the payload of target relative to base (same length).
func Encode(c Codec, base, target []byte, w int) ([]byte, error) {
	if len(base) != len(target) && c != Replace && c != ReplacePlane {
		return nil, fmt.Errorf("tdelta: base %d bytes, target %d", len(base), len(target))
	}
	n := len(target)
	w = width(n, w)
	switch c {
	case XOR:
		return xor(base, target), nil
	case XORZ:
		return deflateBytes(xor(base, target)), nil
	case ByteDelta:
		d := make([]byte, n)
		for i := range d {
			d[i] = target[i] - base[i]
		}
		return deflateBytes(d), nil
	case ZeroRLE:
		return zeroRLE(xor(base, target)), nil
	case Plane:
		x := xor(base, target)
		p := make([]byte, 0, n)
		for k := 0; k < w; k++ {
			for j := k; j < n; j += w {
				p = append(p, x[j])
			}
		}
		return deflateBytes(p), nil
	case Sparse:
		ne := n / w
		bm := make([]byte, (ne+7)/8)
		var vals []byte
		for e := 0; e < ne; e++ {
			if !bytes.Equal(base[e*w:e*w+w], target[e*w:e*w+w]) {
				bm[e>>3] |= 1 << (e & 7)
				vals = append(vals, target[e*w:e*w+w]...)
			}
		}
		bz, vz := deflateBytes(bm), deflateBytes(vals)
		out := binary.AppendUvarint(nil, uint64(len(bz)))
		out = append(out, bz...)
		return append(out, vz...), nil
	case FloatSplit:
		return nil, errors.New("tdelta: float-split needs the dtype, use EncodeFor")
	case Replace:
		return deflateBytes(target), nil
	case ReplacePlane:
		return deflateBytes(planes(target, w)), nil
	}
	return nil, fmt.Errorf("tdelta: unknown codec %d", c)
}

// planes lays out byte k of every w-byte element contiguously, k = 0..w-1.
func planes(b []byte, w int) []byte {
	p := make([]byte, 0, len(b))
	for k := 0; k < w; k++ {
		for j := k; j < len(b); j += w {
			p = append(p, b[j])
		}
	}
	return p
}

// Decode rebuilds the target (n bytes) from base and the payload.
func Decode(c Codec, base, payload []byte, n, w int) ([]byte, error) {
	w = width(n, w)
	if c != Replace && c != ReplacePlane && len(base) != n {
		return nil, fmt.Errorf("tdelta: base %d bytes, want %d", len(base), n)
	}
	switch c {
	case XOR:
		if len(payload) != n {
			return nil, ErrCorrupt
		}
		return xor(base, payload), nil
	case XORZ:
		x, err := inflateBytes(payload, n)
		if err != nil || len(x) != n {
			return nil, ErrCorrupt
		}
		return xor(base, x), nil
	case ByteDelta:
		d, err := inflateBytes(payload, n)
		if err != nil || len(d) != n {
			return nil, ErrCorrupt
		}
		out := make([]byte, n)
		for i := range d {
			out[i] = base[i] + d[i]
		}
		return out, nil
	case ZeroRLE:
		x, err := unZeroRLE(payload, n)
		if err != nil {
			return nil, err
		}
		return xor(base, x), nil
	case Plane:
		p, err := inflateBytes(payload, n)
		if err != nil || len(p) != n {
			return nil, ErrCorrupt
		}
		x := make([]byte, n)
		ne := n / w
		for k := 0; k < w; k++ {
			for e := 0; e < ne; e++ {
				x[e*w+k] = p[k*ne+e]
			}
		}
		return xor(base, x), nil
	case Sparse:
		l, k := binary.Uvarint(payload)
		if k <= 0 || uint64(len(payload)-k) < l {
			return nil, ErrCorrupt
		}
		ne := n / w
		bm, err := inflateBytes(payload[k:k+int(l)], (ne+7)/8)
		if err != nil || len(bm) != (ne+7)/8 {
			return nil, ErrCorrupt
		}
		vals, err := inflateBytes(payload[k+int(l):], n)
		if err != nil {
			return nil, ErrCorrupt
		}
		out := append([]byte(nil), base...)
		vi := 0
		for e := 0; e < ne; e++ {
			if bm[e>>3]>>(e&7)&1 == 1 {
				if vi+w > len(vals) {
					return nil, ErrCorrupt
				}
				copy(out[e*w:], vals[vi:vi+w])
				vi += w
			}
		}
		if vi != len(vals) {
			return nil, ErrCorrupt
		}
		return out, nil
	case FloatSplit:
		return nil, errors.New("tdelta: float-split needs the dtype, use DecodeFor")
	case Replace:
		t, err := inflateBytes(payload, n)
		if err != nil || len(t) != n {
			return nil, ErrCorrupt
		}
		return t, nil
	case ReplacePlane:
		p, err := inflateBytes(payload, n)
		if err != nil || len(p) != n {
			return nil, ErrCorrupt
		}
		out := make([]byte, n)
		ne := n / w
		for k := 0; k < w; k++ {
			for e := 0; e < ne; e++ {
				out[e*w+k] = p[k*ne+e]
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("tdelta: unknown codec %d", c)
}

// zeroRLE: repeated (zero run, literal run, literal bytes), runs as varints.
func zeroRLE(x []byte) []byte {
	var out []byte
	for i := 0; i < len(x); {
		z := i
		for z < len(x) && x[z] == 0 {
			z++
		}
		l := z
		for l < len(x) && x[l] != 0 {
			l++
		}
		out = binary.AppendUvarint(out, uint64(z-i))
		out = binary.AppendUvarint(out, uint64(l-z))
		out = append(out, x[z:l]...)
		i = l
	}
	return out
}

func unZeroRLE(p []byte, n int) ([]byte, error) {
	out := make([]byte, 0, n)
	for len(p) > 0 {
		z, k := binary.Uvarint(p)
		if k <= 0 {
			return nil, ErrCorrupt
		}
		p = p[k:]
		l, k := binary.Uvarint(p)
		if k <= 0 {
			return nil, ErrCorrupt
		}
		p = p[k:]
		if z > uint64(n-len(out)) || l > uint64(len(p)) || l > uint64(n-len(out))-z {
			return nil, ErrCorrupt
		}
		out = append(out, make([]byte, z)...)
		out = append(out, p[:l]...)
		p = p[l:]
	}
	if len(out) != n {
		return nil, ErrCorrupt
	}
	return out, nil
}

// EncodeFor is Encode with dtype awareness (float-split needs the bit layout).
func EncodeFor(c Codec, dtype string, base, target []byte, w int) ([]byte, error) {
	if c == FloatSplit {
		return encodeFloatSplit(dtype, base, target, w)
	}
	return Encode(c, base, target, w)
}

// DecodeFor mirrors EncodeFor.
func DecodeFor(c Codec, dtype string, base, payload []byte, n, w int) ([]byte, error) {
	if c == FloatSplit {
		return decodeFloatSplit(dtype, base, payload, n, w)
	}
	return Decode(c, base, payload, n, w)
}

func dtypeLayout(dtype string) (s, e, m, w int, ok bool) {
	switch dtype {
	case "F32":
		return 1, 8, 23, 4, true
	case "F16":
		return 1, 5, 10, 2, true
	case "BF16":
		return 1, 8, 7, 2, true
	}
	return 0, 0, 0, 0, false
}

func encodeFloatSplit(dtype string, base, target []byte, w int) ([]byte, error) {
	s, e, m, ww, ok := dtypeLayout(dtype)
	if !ok || len(base) != len(target) || len(target)%ww != 0 {
		return nil, fmt.Errorf("tdelta: float-split does not apply to %s", dtype)
	}
	return floatSplitEncode(xor(base, target), ww, s, e, m), nil
}

func decodeFloatSplit(dtype string, base, payload []byte, n, w int) ([]byte, error) {
	s, e, m, ww, ok := dtypeLayout(dtype)
	if !ok || n%ww != 0 || len(base) != n {
		return nil, ErrCorrupt
	}
	x, err := floatSplitDecode(payload, n, ww, s, e, m)
	if err != nil {
		return nil, err
	}
	return xor(base, x), nil
}

// element word, little-endian
func word(b []byte, w int) uint64 {
	switch w {
	case 2:
		return uint64(binary.LittleEndian.Uint16(b))
	case 4:
		return uint64(binary.LittleEndian.Uint32(b))
	}
	return uint64(b[0])
}

func putWord(b []byte, w int, v uint64) {
	switch w {
	case 2:
		binary.LittleEndian.PutUint16(b, uint16(v))
	case 4:
		binary.LittleEndian.PutUint32(b, uint32(v))
	}
}

// floatSplitEncode splits each XOR word into sign (bit-packed), exponent
// (one byte per element) and mantissa (little-endian bytes) streams.
func floatSplitEncode(x []byte, w, sb, eb, mb int) []byte {
	ne := len(x) / w
	sign := make([]byte, (ne+7)/8)
	exp := make([]byte, ne)
	mw := (mb + 7) / 8
	mant := make([]byte, 0, ne*mw)
	for e := 0; e < ne; e++ {
		v := word(x[e*w:], w)
		mantV := v & (1<<uint(mb) - 1)
		expV := v >> uint(mb) & (1<<uint(eb) - 1)
		signV := v >> uint(mb+eb) & 1
		sign[e>>3] |= byte(signV) << (e & 7)
		exp[e] = byte(expV)
		for k := 0; k < mw; k++ {
			mant = append(mant, byte(mantV>>(8*uint(k))))
		}
	}
	var out []byte
	for _, s := range [][]byte{deflateBytes(sign), deflateBytes(exp), deflateBytes(mant)} {
		out = binary.AppendUvarint(out, uint64(len(s)))
		out = append(out, s...)
	}
	return out
}

func floatSplitDecode(p []byte, n, w, sb, eb, mb int) ([]byte, error) {
	ne := n / w
	mw := (mb + 7) / 8
	limits := []int{(ne + 7) / 8, ne, ne * mw}
	var parts [3][]byte
	for i := range parts {
		l, k := binary.Uvarint(p)
		if k <= 0 || uint64(len(p)-k) < l {
			return nil, ErrCorrupt
		}
		d, err := inflateBytes(p[k:k+int(l)], limits[i])
		if err != nil || len(d) != limits[i] {
			return nil, ErrCorrupt
		}
		parts[i] = d
		p = p[k+int(l):]
	}
	if len(p) != 0 {
		return nil, ErrCorrupt
	}
	out := make([]byte, n)
	for e := 0; e < ne; e++ {
		var mantV uint64
		for k := 0; k < mw; k++ {
			mantV |= uint64(parts[2][e*mw+k]) << (8 * uint(k))
		}
		if mantV>>uint(mb) != 0 || uint64(parts[1][e])>>uint(eb) != 0 {
			return nil, ErrCorrupt
		}
		v := uint64(parts[0][e>>3]>>(e&7)&1)<<uint(mb+eb) | uint64(parts[1][e])<<uint(mb) | mantV
		putWord(out[e*w:], w, v)
	}
	return out, nil
}

// ---- measurements (no payload) ---------------------------------------------

// Stats are the bit-level facts about one aligned tensor pair.
type Stats struct {
	Bytes, Elems    int64
	EqualBytes      int64 // bytes identical
	EqualElems      int64 // elements bit-identical
	ZeroXorWords    int64 // = EqualElems for width>=1
	XorPopcount     int64
	SignChanged     int64 // elements whose sign bit changed
	ExpChanged      int64 // elements whose exponent bits changed
	MantChanged     int64 // elements whose mantissa bits changed
	ExpBitsFlipped  int64
	MantBitsFlipped int64
	SignBitsFlipped int64
	BlockChanged4K  int64 // number of 4 KiB blocks with any change
	Blocks4K        int64
	AdjChangedPairs int64 // consecutive element pairs both changed
	ChangedElems    int64
	DistLog2        [34]int64 // histogram of log2(1+|ulp distance|) for changed elements (width 2/4)
	ColOrRowChanged int64
}

// Compare measures base vs target (both n bytes, element width w, dtype for
// the bit layout of floats: F32, F16, BF16; other dtypes get the generic parts).
func Compare(dtype string, base, target []byte, w int, shape []int64) Stats {
	n := len(target)
	w = width(n, w)
	var s Stats
	s.Bytes, s.Elems = int64(n), int64(n/w)
	for i := 0; i < n; i++ {
		if base[i] == target[i] {
			s.EqualBytes++
		}
	}
	sb, eb, mb, _, isFloat := dtypeLayout(dtype)
	_ = sb
	prevChanged := false
	for e := 0; e < n/w; e++ {
		bw, tw := word(base[e*w:], w), word(target[e*w:], w)
		x := bw ^ tw
		if x == 0 {
			s.EqualElems++
			prevChanged = false
			continue
		}
		s.ChangedElems++
		if prevChanged {
			s.AdjChangedPairs++
		}
		prevChanged = true
		s.XorPopcount += int64(bits.OnesCount64(x))
		if isFloat {
			m := x & (1<<uint(mb) - 1)
			ex := x >> uint(mb) & (1<<uint(eb) - 1)
			sg := x >> uint(mb+eb) & 1
			if m != 0 {
				s.MantChanged++
				s.MantBitsFlipped += int64(bits.OnesCount64(m))
			}
			if ex != 0 {
				s.ExpChanged++
				s.ExpBitsFlipped += int64(bits.OnesCount64(ex))
			}
			if sg != 0 {
				s.SignChanged++
				s.SignBitsFlipped++
			}
		}
		if w == 2 || w == 4 {
			// monotone integer key of the bit pattern (sign-magnitude -> two's complement order)
			ka, kb := monoKey(bw, w, isFloat), monoKey(tw, w, isFloat)
			d := ka - kb
			if d < 0 {
				d = -d
			}
			s.DistLog2[bits.Len64(uint64(d))]++
		}
	}
	s.Blocks4K = int64((n + 4095) / 4096)
	for off := 0; off < n; off += 4096 {
		end := min(off+4096, n)
		if !bytes.Equal(base[off:end], target[off:end]) {
			s.BlockChanged4K++
		}
	}
	return s
}

// monoKey maps a w-byte pattern to an int64 whose order is the numeric order
// for floats (sign-magnitude) and the two's complement order otherwise.
func monoKey(v uint64, w int, float bool) int64 {
	bitsN := uint(8 * w)
	signBit := uint64(1) << (bitsN - 1)
	if float {
		if v&signBit != 0 {
			return -int64(v &^ signBit)
		}
		return int64(v)
	}
	if v&signBit != 0 {
		return int64(v) - int64(1)<<bitsN
	}
	return int64(v)
}

// Entropy is the order-0 Shannon entropy in bits per byte of b.
func Entropy(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	var h [256]int
	for _, c := range b {
		h[c]++
	}
	var e float64
	n := float64(len(b))
	for _, c := range h {
		if c > 0 {
			p := float64(c) / n
			e -= p * log2(p)
		}
	}
	return e
}
