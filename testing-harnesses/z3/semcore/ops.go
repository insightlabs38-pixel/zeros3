package semcore

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"sync"
)

// Derivation operation kinds.
const (
	OpPerm         = 1  // reshape input to Dims then transpose by Axes; params: elem, dims, axes
	OpBswap        = 2  // reverse bytes within each Width-byte element
	OpConvert      = 3  // float/int element conversion From -> To
	OpQ8_0         = 4  // GGML Q8_0 quantization of F32 input
	OpSlice        = 5  // bytes [Off, Off+Len) of the single input
	OpConcat       = 6  // inputs appended in order
	OpQ4_0         = 7  // GGML Q4_0 quantization of F32 input
	OpGather       = 8  // output row i = input row Idx[i]; rows are Elem bytes
	OpDeflate      = 9  // raw DEFLATE stream replayed from the parent plaintext and Aux residual
	OpRecipe       = 10 // output = replay of the Recipe held in Aux (components referenced by CID)
	OpPrepend      = 11 // Aux literal followed by the parent payload
	OpPlainOffsets = 12 // length-prefixed value stream -> Arrow int32 offsets (n+1)
	OpPlainData    = 13 // length-prefixed value stream -> concatenated value bytes
	OpParquet      = 14 // exact Parquet replay (kind-10 codec) of Aux recipe over the parent components
)

// MaxOutput bounds any materialized derived payload (budget for hostile records).
const MaxOutput = 1 << 31

// Op is one pipeline step. Fields beyond Kind are used per kind.
type Op struct {
	Kind     uint8
	Elem     uint32   // Perm: element byte width; Bswap: width
	Dims     []uint64 // Perm: input grid
	Axes     []uint8  // Perm: output axis i = input axis Axes[i]
	From, To uint16   // Convert
	Off, Len uint64   // Slice
	Idx      []uint32 // Gather row index
	Aux      []byte   // Deflate residual, Recipe bytes, or Prepend literal
}

func (o *Op) String() string {
	switch o.Kind {
	case OpPerm:
		return fmt.Sprintf("perm%v%v", o.Dims, o.Axes)
	case OpBswap:
		return fmt.Sprintf("bswap%d", o.Elem)
	case OpConvert:
		return fmt.Sprintf("convert(%s>%s)", DTypeName(int(o.From)), DTypeName(int(o.To)))
	case OpQ8_0:
		return "q8_0"
	case OpQ4_0:
		return "q4_0"
	case OpGather:
		return fmt.Sprintf("gather(%d rows x%dB)", len(o.Idx), o.Elem)
	case OpSlice:
		return fmt.Sprintf("slice[%d+%d]", o.Off, o.Len)
	case OpConcat:
		return "concat"
	case OpPlainOffsets:
		return "plain>offsets"
	case OpPlainData:
		return "plain>data"
	case OpParquet:
		return fmt.Sprintf("parquet(recipe %dB)", len(o.Aux))
	case OpDeflate:
		return fmt.Sprintf("deflate(res %dB)", len(o.Aux))
	case OpRecipe:
		return fmt.Sprintf("recipe(%dB)", len(o.Aux))
	case OpPrepend:
		return fmt.Sprintf("prepend(%dB)", len(o.Aux))
	}
	return "?"
}

func (o *Op) marshal(b []byte) []byte {
	b = append(b, o.Kind)
	switch o.Kind {
	case OpPerm:
		b = binary.AppendUvarint(b, uint64(o.Elem))
		b = binary.AppendUvarint(b, uint64(len(o.Dims)))
		for _, d := range o.Dims {
			b = binary.AppendUvarint(b, d)
		}
		b = append(b, o.Axes...)
	case OpBswap:
		b = binary.AppendUvarint(b, uint64(o.Elem))
	case OpConvert:
		b = binary.AppendUvarint(b, uint64(o.From))
		b = binary.AppendUvarint(b, uint64(o.To))
	case OpSlice:
		b = binary.AppendUvarint(b, o.Off)
		b = binary.AppendUvarint(b, o.Len)
	case OpDeflate, OpRecipe, OpPrepend, OpParquet:
		b = binary.AppendUvarint(b, uint64(len(o.Aux)))
		b = append(b, o.Aux...)
	case OpGather:
		b = binary.AppendUvarint(b, uint64(o.Elem))
		b = binary.AppendUvarint(b, uint64(len(o.Idx)))
		for _, x := range o.Idx {
			b = binary.AppendUvarint(b, uint64(x))
		}
	}
	return b
}

func parseOp(b []byte) (Op, int, error) {
	var o Op
	if len(b) < 1 {
		return o, 0, errShort
	}
	o.Kind = b[0]
	pos := 1
	u := func() (uint64, error) {
		v, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return 0, errShort
		}
		pos += n
		return v, nil
	}
	var err error
	var v uint64
	switch o.Kind {
	case OpPerm:
		if v, err = u(); err != nil || v == 0 || v > 1<<30 {
			return o, 0, errors.New("semcore: bad perm element width")
		}
		o.Elem = uint32(v)
		if v, err = u(); err != nil || v == 0 || v > 16 {
			return o, 0, errors.New("semcore: bad perm rank")
		}
		for i := uint64(0); i < v; i++ {
			d, err := u()
			if err != nil {
				return o, 0, err
			}
			o.Dims = append(o.Dims, d)
		}
		if len(b)-pos < len(o.Dims) {
			return o, 0, errShort
		}
		o.Axes = append([]uint8(nil), b[pos:pos+len(o.Dims)]...)
		pos += len(o.Dims)
		if !validAxes(o.Axes) {
			return o, 0, errors.New("semcore: axes are not a permutation")
		}
	case OpBswap:
		if v, err = u(); err != nil || v < 2 || v > 8 {
			return o, 0, errors.New("semcore: bad bswap width")
		}
		o.Elem = uint32(v)
	case OpConvert:
		if v, err = u(); err != nil || v > 1<<16-1 {
			return o, 0, errShort
		}
		o.From = uint16(v)
		if v, err = u(); err != nil || v > 1<<16-1 {
			return o, 0, errShort
		}
		o.To = uint16(v)
	case OpSlice:
		if o.Off, err = u(); err != nil {
			return o, 0, err
		}
		if o.Len, err = u(); err != nil {
			return o, 0, err
		}
	case OpDeflate, OpRecipe, OpPrepend, OpParquet:
		if v, err = u(); err != nil || v > uint64(len(b)-pos) {
			return o, 0, errors.New("semcore: bad aux length")
		}
		o.Aux = append([]byte(nil), b[pos:pos+int(v)]...)
		pos += int(v)
	case OpGather:
		if v, err = u(); err != nil || v == 0 || v > 1<<30 {
			return o, 0, errors.New("semcore: bad gather row width")
		}
		o.Elem = uint32(v)
		if v, err = u(); err != nil || v > uint64(len(b)-pos) {
			return o, 0, errors.New("semcore: bad gather count")
		}
		for i := uint64(0); i < v; i++ {
			x, err := u()
			if err != nil || x > 1<<32-1 {
				return o, 0, errShort
			}
			o.Idx = append(o.Idx, uint32(x))
		}
	case OpQ8_0, OpQ4_0, OpConcat, OpPlainOffsets, OpPlainData:
	default:
		return o, 0, fmt.Errorf("semcore: unknown op %d", o.Kind)
	}
	return o, pos, nil
}

func validAxes(a []uint8) bool {
	seen := make([]bool, len(a))
	for _, x := range a {
		if int(x) >= len(a) || seen[x] {
			return false
		}
		seen[x] = true
	}
	return true
}

// InvertPerm returns the Perm that undoes p.
func InvertPerm(p Op) Op {
	n := len(p.Dims)
	out := Op{Kind: OpPerm, Elem: p.Elem, Dims: make([]uint64, n), Axes: make([]uint8, n)}
	for i, a := range p.Axes {
		out.Dims[i] = p.Dims[a]
		out.Axes[a] = uint8(i)
	}
	return out
}

// Ext supplies the implementation of operation kinds that are format-specific and therefore
// not part of the portable core (exact DEFLATE replay, exact Parquet replay). A store carries
// its Ext explicitly; there is no global registry.
type Ext interface {
	ApplyExt(o *Op, in [][]byte) ([]byte, error)
}

// ErrNoExt is returned for an extension op when no Ext handles it.
var ErrNoExt = errors.New("semcore: operation needs a format extension that is not installed")

// Apply runs one core op over its inputs and returns the produced bytes.
func (o *Op) Apply(in [][]byte) ([]byte, error) { return o.ApplyWith(nil, in) }

// ApplyWith is Apply with an extension for format-specific kinds.
func (o *Op) ApplyWith(x Ext, in [][]byte) ([]byte, error) {
	if o.Kind == OpConcat {
		var n int64
		for _, b := range in {
			n += int64(len(b))
			if n > MaxOutput {
				return nil, errBudget
			}
		}
		out := make([]byte, 0, n)
		for _, b := range in {
			out = append(out, b...)
		}
		return out, nil
	}
	if o.Kind == OpRecipe {
		return nil, errors.New("semcore: recipe op is resolved by the store")
	}
	if o.Kind == OpParquet {
		if x == nil {
			return nil, ErrNoExt
		}
		return x.ApplyExt(o, in)
	}
	if len(in) != 1 {
		return nil, fmt.Errorf("semcore: op %s needs one input", o)
	}
	b := in[0]
	switch o.Kind {
	case OpPrepend:
		if int64(len(b))+int64(len(o.Aux)) > MaxOutput {
			return nil, errBudget
		}
		return append(append([]byte(nil), o.Aux...), b...), nil
	case OpDeflate:
		if x == nil {
			return nil, ErrNoExt
		}
		return x.ApplyExt(o, in)
	case OpPlainOffsets, OpPlainData:
		return plainSplit(b, o.Kind == OpPlainOffsets)
	case OpPerm:
		return permute(b, o.Elem, o.Dims, o.Axes)
	case OpBswap:
		if len(b)%int(o.Elem) != 0 {
			return nil, errors.New("semcore: bswap length")
		}
		out := make([]byte, len(b))
		w := int(o.Elem)
		for i := 0; i < len(b); i += w {
			for j := 0; j < w; j++ {
				out[i+j] = b[i+w-1-j]
			}
		}
		return out, nil
	case OpConvert:
		return convert(b, int(o.From), int(o.To))
	case OpQ8_0:
		return quantQ8(b)
	case OpQ4_0:
		return quantQ4(b)
	case OpGather:
		rb := int(o.Elem)
		if rb == 0 || len(b)%rb != 0 || int64(len(o.Idx))*int64(rb) > MaxOutput {
			return nil, errors.New("semcore: gather shape")
		}
		rows := len(b) / rb
		out := make([]byte, len(o.Idx)*rb)
		for i, x := range o.Idx {
			if int(x) >= rows {
				return nil, errors.New("semcore: gather index out of range")
			}
			copy(out[i*rb:], b[int(x)*rb:int(x)*rb+rb])
		}
		return out, nil
	case OpSlice:
		if o.Off > uint64(len(b)) || o.Len > uint64(len(b))-o.Off {
			return nil, errors.New("semcore: slice out of range")
		}
		return append([]byte(nil), b[o.Off:o.Off+o.Len]...), nil
	}
	return nil, fmt.Errorf("semcore: unknown op %d", o.Kind)
}

var errBudget = errors.New("semcore: materialization budget exceeded")

func permute(b []byte, elem uint32, dims []uint64, axes []uint8) ([]byte, error) {
	n := uint64(1)
	for _, d := range dims {
		hi, lo := bits.Mul64(n, d)
		if hi != 0 || lo > MaxOutput {
			return nil, errBudget
		}
		n = lo
	}
	if n*uint64(elem) != uint64(len(b)) {
		return nil, fmt.Errorf("semcore: perm grid %v x%d != %d bytes", dims, elem, len(b))
	}
	r := len(dims)
	// source strides in elements
	sstr := make([]uint64, r)
	s := uint64(1)
	for i := r - 1; i >= 0; i-- {
		sstr[i] = s
		s *= dims[i]
	}
	odims := make([]uint64, r)
	ostr := make([]uint64, r) // source stride walked by each output axis
	for i, a := range axes {
		odims[i] = dims[a]
		ostr[i] = sstr[a]
	}
	out := make([]byte, len(b))
	e := int(elem)
	if n == 0 {
		return out, nil
	}
	idx := make([]uint64, r)
	src := uint64(0)
	last := r - 1
	inner, istr := odims[last], ostr[last]
	for dst := uint64(0); dst < n; {
		if e == 2 {
			for k := uint64(0); k < inner; k++ {
				p := (src + k*istr) * 2
				q := (dst + k) * 2
				out[q], out[q+1] = b[p], b[p+1]
			}
		} else if e == 4 {
			for k := uint64(0); k < inner; k++ {
				p := (src + k*istr) * 4
				q := (dst + k) * 4
				copy(out[q:q+4], b[p:p+4])
			}
		} else {
			for k := uint64(0); k < inner; k++ {
				p := (src + k*istr) * uint64(e)
				q := (dst + k) * uint64(e)
				copy(out[q:q+uint64(e)], b[p:p+uint64(e)])
			}
		}
		dst += inner
		// advance outer axes
		j := last - 1
		for ; j >= 0; j-- {
			idx[j]++
			src += ostr[j]
			if idx[j] < odims[j] {
				break
			}
			src -= ostr[j] * odims[j]
			idx[j] = 0
		}
		if j < 0 {
			break
		}
	}
	return out, nil
}

// ---- float conversion (bit exact, deterministic) ----

func f16to32(h uint16) uint32 {
	s := uint32(h>>15) << 31
	e := uint32(h>>10) & 31
	m := uint32(h) & 1023
	switch {
	case e == 0:
		if m == 0 {
			return s
		}
		sh := uint32(bits.LeadingZeros32(m)) - 21 // normalize
		m = (m << sh) & 1023
		return s | (113-sh)<<23 | m<<13
	case e == 31:
		return s | 0x7f800000 | m<<13
	}
	return s | (e+112)<<23 | m<<13
}

// f32to16 rounds to nearest even; NaNs keep the top mantissa bits and are made quiet.
func f32to16(f uint32) uint16 {
	s := uint16(f>>16) & 0x8000
	e := int32(f>>23) & 255
	m := f & 0x7fffff
	if e == 255 {
		if m == 0 {
			return s | 0x7c00
		}
		return s | 0x7e00 | uint16(m>>13)
	}
	ne := e - 127 + 15
	if ne >= 31 {
		return s | 0x7c00
	}
	if ne <= 0 {
		if ne < -10 {
			return s
		}
		m |= 0x800000
		sh := uint32(14 - ne)
		h := m >> sh
		rem := m & (1<<sh - 1)
		half := uint32(1) << (sh - 1)
		if rem > half || (rem == half && h&1 == 1) {
			h++
		}
		return s | uint16(h)
	}
	h := uint32(ne)<<10 | m>>13
	rem := m & 0x1fff
	if rem > 0x1000 || (rem == 0x1000 && h&1 == 1) {
		h++
	}
	return s | uint16(h)
}

func f32toBF16(f uint32) uint16 {
	if f&0x7fffffff > 0x7f800000 {
		return uint16(f>>16) | 0x40
	}
	f += 0x7fff + (f>>16)&1
	return uint16(f >> 16)
}

func toF32bits(b []byte, dt int, i int) (uint32, bool) {
	switch dt {
	case DF16:
		return f16to32(binary.LittleEndian.Uint16(b[2*i:])), true
	case DBF16:
		return uint32(binary.LittleEndian.Uint16(b[2*i:])) << 16, true
	case DF32:
		return binary.LittleEndian.Uint32(b[4*i:]), true
	}
	return 0, false
}

var (
	f16Once sync.Once
	f16Tab  [65536]uint32
)

// f16Table is the binary16 -> binary32 map (exact; built from f16to32 once).
func f16Table() *[65536]uint32 {
	f16Once.Do(func() {
		for i := range f16Tab {
			f16Tab[i] = f16to32(uint16(i))
		}
	})
	return &f16Tab
}

func convert(b []byte, from, to int) ([]byte, error) {
	fw, tw := ElemBytes(from), ElemBytes(to)
	if fw == 0 || tw == 0 || len(b)%fw != 0 {
		return nil, fmt.Errorf("semcore: convert %s>%s unsupported", DTypeName(from), DTypeName(to))
	}
	n := len(b) / fw
	if int64(n)*int64(tw) > MaxOutput {
		return nil, errBudget
	}
	out := make([]byte, n*tw)
	switch {
	case from == to:
		copy(out, b)
	case from == DBF16 && to == DF32:
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint32(out[4*i:], uint32(binary.LittleEndian.Uint16(b[2*i:]))<<16)
		}
	case from == DF16 && to == DF32:
		tab := f16Table()
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint32(out[4*i:], tab[binary.LittleEndian.Uint16(b[2*i:])])
		}
	case from == DF32 && to == DBF16:
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint16(out[2*i:], f32toBF16(binary.LittleEndian.Uint32(b[4*i:])))
		}
	case from == DF32 && to == DF16:
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint16(out[2*i:], f32to16(binary.LittleEndian.Uint32(b[4*i:])))
		}
	case IsFloat(from) && IsFloat(to) && from != DF64 && to != DF64:
		for i := 0; i < n; i++ {
			f, _ := toF32bits(b, from, i)
			switch to {
			case DF32:
				binary.LittleEndian.PutUint32(out[4*i:], f)
			case DF16:
				binary.LittleEndian.PutUint16(out[2*i:], f32to16(f))
			case DBF16:
				binary.LittleEndian.PutUint16(out[2*i:], f32toBF16(f))
			}
		}
	case from == DF32 && to == DF64:
		for i := 0; i < n; i++ {
			x := math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
			binary.LittleEndian.PutUint64(out[8*i:], math.Float64bits(float64(x)))
		}
	case from == DF64 && to == DF32:
		for i := 0; i < n; i++ {
			x := math.Float64frombits(binary.LittleEndian.Uint64(b[8*i:]))
			binary.LittleEndian.PutUint32(out[4*i:], math.Float32bits(float32(x)))
		}
	case !IsFloat(from) && !IsFloat(to) && tw >= fw:
		signed := from == DI8 || from == DI16 || from == DI32 || from == DI64
		for i := 0; i < n; i++ {
			var v uint64
			switch fw {
			case 1:
				v = uint64(b[i])
				if signed {
					v = uint64(int64(int8(b[i])))
				}
			case 2:
				v = uint64(binary.LittleEndian.Uint16(b[2*i:]))
				if signed {
					v = uint64(int64(int16(v)))
				}
			case 4:
				v = uint64(binary.LittleEndian.Uint32(b[4*i:]))
				if signed {
					v = uint64(int64(int32(v)))
				}
			default:
				v = binary.LittleEndian.Uint64(b[8*i:])
			}
			switch tw {
			case 1:
				out[i] = byte(v)
			case 2:
				binary.LittleEndian.PutUint16(out[2*i:], uint16(v))
			case 4:
				binary.LittleEndian.PutUint32(out[4*i:], uint32(v))
			default:
				binary.LittleEndian.PutUint64(out[8*i:], v)
			}
		}
	default:
		return nil, fmt.Errorf("semcore: convert %s>%s unsupported", DTypeName(from), DTypeName(to))
	}
	return out, nil
}

// quantQ8 is GGML quantize_row_q8_0_ref over a little-endian F32 input.
func quantQ8(b []byte) ([]byte, error) {
	if len(b)%128 != 0 {
		return nil, errors.New("semcore: q8_0 input must be whole 32-element f32 blocks")
	}
	nb := len(b) / 128
	if int64(nb)*34 > MaxOutput {
		return nil, errBudget
	}
	out := make([]byte, nb*34)
	var x [32]float32
	for i := 0; i < nb; i++ {
		var amax float32
		for j := 0; j < 32; j++ {
			x[j] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*128+4*j:]))
			if a := float32(math.Abs(float64(x[j]))); a > amax {
				amax = a
			}
		}
		d := amax / 127
		var id float32
		if d != 0 {
			id = 1 / d
		}
		binary.LittleEndian.PutUint16(out[i*34:], f32to16(math.Float32bits(d)))
		for j := 0; j < 32; j++ {
			out[i*34+2+j] = byte(int8(math.Round(float64(x[j] * id))))
		}
	}
	return out, nil
}

// quantQ4 is GGML quantize_row_q4_0_ref.
func quantQ4(b []byte) ([]byte, error) {
	if len(b)%128 != 0 {
		return nil, errors.New("semcore: q4_0 input must be whole 32-element f32 blocks")
	}
	nb := len(b) / 128
	if int64(nb)*18 > MaxOutput {
		return nil, errBudget
	}
	out := make([]byte, nb*18)
	var x [32]float32
	for i := 0; i < nb; i++ {
		var amax, mx float32
		for j := 0; j < 32; j++ {
			x[j] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*128+4*j:]))
			if a := float32(math.Abs(float64(x[j]))); a > amax {
				amax, mx = a, x[j]
			}
		}
		d := mx / -8
		var id float32
		if d != 0 {
			id = 1 / d
		}
		binary.LittleEndian.PutUint16(out[i*18:], f32to16(math.Float32bits(d)))
		for j := 0; j < 16; j++ {
			x0 := x[j] * id
			x1 := x[16+j] * id
			q := func(v float32) byte {
				t := int32(v + 8.5)
				if t > 15 {
					t = 15
				}
				return byte(t)
			}
			out[i*18+2+j] = q(x0) | q(x1)<<4
		}
	}
	return out, nil
}

// ---- exact sparse residual ----

// Patch replaces Data at Off in the produced payload.
type Patch struct {
	Off  uint64
	Data []byte
}

// DiffPatches returns the minimal-ish patch list turning pred into want (equal length).
// Differing runs closer than mergeGap bytes are coalesced.
func DiffPatches(pred, want []byte, mergeGap int) []Patch {
	var ps []Patch
	n := len(want)
	i := 0
	for i < n {
		if pred[i] == want[i] {
			// skip equal in 8-byte steps
			for i+8 <= n && binary.LittleEndian.Uint64(pred[i:]) == binary.LittleEndian.Uint64(want[i:]) {
				i += 8
			}
			for i < n && pred[i] == want[i] {
				i++
			}
			continue
		}
		start := i
		end := i + 1
		for j := end; j < n && j-end <= mergeGap; j++ {
			if pred[j] != want[j] {
				end = j + 1
			}
		}
		i = end
		ps = append(ps, Patch{uint64(start), append([]byte(nil), want[start:end]...)})
	}
	return ps
}

// DiffPatchesMax is DiffPatches with an exact early exit: it reports ok=false as soon as the
// serialized patch size (bytes + 6 per patch, the planner's model) reaches limit, which is
// the point where the planner would reject the derivation anyway. Equal inputs and limits
// give the same patches as DiffPatches whenever ok is true.
func DiffPatchesMax(pred, want []byte, mergeGap int, limit int) ([]Patch, bool) {
	var ps []Patch
	n := len(want)
	// Exact quick reject: every differing byte lies in some patch, so the patch bytes alone
	// are at least the number of differing bytes (plus 6 for the first patch).
	if d := diffCount(pred, want, limit); d > 0 && d >= limit-6 {
		return nil, false
	}
	pb := 0
	i := 0
	for i < n {
		if pred[i] == want[i] {
			for i+8 <= n && binary.LittleEndian.Uint64(pred[i:]) == binary.LittleEndian.Uint64(want[i:]) {
				i += 8
			}
			for i < n && pred[i] == want[i] {
				i++
			}
			continue
		}
		start := i
		end := i + 1
		for j := end; j < n && j-end <= mergeGap; j++ {
			if pred[j] != want[j] {
				end = j + 1
			}
		}
		i = end
		pb += end - start + 6
		if pb >= limit {
			return nil, false
		}
		ps = append(ps, Patch{uint64(start), append([]byte(nil), want[start:end]...)})
	}
	return ps, true
}

// diffCount counts differing bytes of equal-length slices, stopping once it exceeds stop.
func diffCount(a, b []byte, stop int) int {
	n, c := len(a), 0
	i := 0
	for ; i+8 <= n; i += 8 {
		x := binary.LittleEndian.Uint64(a[i:]) ^ binary.LittleEndian.Uint64(b[i:])
		if x == 0 {
			continue
		}
		// bytes that are non-zero: fold each byte's bits into its low bit
		x |= x >> 4
		x |= x >> 2
		x |= x >> 1
		c += bits.OnesCount64(x & 0x0101010101010101)
		if c > stop {
			return c
		}
	}
	for ; i < n; i++ {
		if a[i] != b[i] {
			c++
		}
	}
	return c
}

// ApplyPatches mutates out in place; patches must be sorted and in range.
func ApplyPatches(out []byte, ps []Patch) error {
	prev := uint64(0)
	for _, p := range ps {
		if p.Off < prev || p.Off > uint64(len(out)) || uint64(len(p.Data)) > uint64(len(out))-p.Off {
			return errors.New("semcore: bad patch range")
		}
		copy(out[p.Off:], p.Data)
		prev = p.Off + uint64(len(p.Data))
	}
	return nil
}

func marshalPatches(ps []Patch) []byte {
	b := binary.AppendUvarint(nil, uint64(len(ps)))
	prev := uint64(0)
	for _, p := range ps {
		b = binary.AppendUvarint(b, p.Off-prev)
		b = binary.AppendUvarint(b, uint64(len(p.Data)))
		b = append(b, p.Data...)
		prev = p.Off + uint64(len(p.Data))
	}
	return b
}

func parsePatches(b []byte, outLen uint64) ([]Patch, int, error) {
	pos := 0
	u := func() (uint64, error) {
		v, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return 0, errShort
		}
		pos += n
		return v, nil
	}
	cnt, err := u()
	if err != nil || cnt > uint64(len(b)) {
		return nil, 0, errors.New("semcore: bad patch count")
	}
	var ps []Patch
	prev := uint64(0)
	for i := uint64(0); i < cnt; i++ {
		gap, err := u()
		if err != nil {
			return nil, 0, err
		}
		l, err := u()
		if err != nil || l > uint64(len(b)-pos) || gap > outLen || prev+gap > outLen || l > outLen-prev-gap {
			return nil, 0, errors.New("semcore: bad patch")
		}
		ps = append(ps, Patch{prev + gap, append([]byte(nil), b[pos:pos+int(l)]...)})
		pos += int(l)
		prev = prev + gap + l
	}
	if !sort.SliceIsSorted(ps, func(i, j int) bool { return ps[i].Off < ps[j].Off }) {
		return nil, 0, errors.New("semcore: unsorted patches")
	}
	return ps, pos, nil
}

// plainSplit decodes a Parquet PLAIN byte-array stream (u32 length + bytes per value).
func plainSplit(b []byte, offsets bool) ([]byte, error) {
	var out []byte
	if offsets {
		out = binary.LittleEndian.AppendUint32(out, 0)
	}
	pos, total := 0, uint64(0)
	for pos < len(b) {
		if len(b)-pos < 4 {
			return nil, errors.New("semcore: truncated byte-array stream")
		}
		n := int(binary.LittleEndian.Uint32(b[pos:]))
		pos += 4
		if n > len(b)-pos {
			return nil, errors.New("semcore: byte-array value past stream end")
		}
		if offsets {
			total += uint64(n)
			if total > math.MaxInt32 {
				return nil, errors.New("semcore: offsets exceed int32")
			}
			out = binary.LittleEndian.AppendUint32(out, uint32(total))
		} else {
			out = append(out, b[pos:pos+n]...)
		}
		pos += n
		if int64(len(out)) > MaxOutput {
			return nil, errBudget
		}
	}
	return out, nil
}
