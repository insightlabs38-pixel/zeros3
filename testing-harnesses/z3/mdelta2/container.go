package mdelta2

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"zeros3-z3/simdelta"
)

// A container is one stored model state. Every used base is named by SHA-256 and length;
// every base tensor reference carries a 4-byte tag; the decoder checks the final SHA-256.

const (
	magic       = "ZB5C1"
	ModeCoded   = 0
	ModeCopy    = 1
	ModeQuant   = 2
	MaxBases    = 3
	MaxDepth    = 4
	maxTensors  = 1 << 20
	maxSegs     = 1 << 16
	maxOutBytes = 16 << 30
	maxGapBytes = 1 << 28
)

var (
	ErrFormat   = errors.New("mdelta2: malformed container")
	ErrDepth    = errors.New("mdelta2: dependency depth exceeded")
	ErrCycle    = errors.New("mdelta2: dependency cycle")
	ErrMissing  = errors.New("mdelta2: missing dependency")
	ErrIdentity = errors.New("mdelta2: base tensor identity mismatch")
	ErrDigest   = errors.New("mdelta2: output digest mismatch")
)

// BaseRef points a segment at (a range of) one tensor of one named base.
type BaseRef struct {
	Slot uint8
	TIdx uint32
	Tag  uint32
	Off  int64 // element offset into the base tensor (signed)
}

// Seg is a contiguous element range of one tensor with one prediction recipe.
type Seg struct {
	Elems  int64
	Mode   uint8
	Narrow DType // coding dtype (0 = the tensor's own dtype)
	Params *Params
	Refs   []BaseRef
	Coder  uint8
	Stream []byte
	Q      []byte // ModeQuant: typed quantized-lane stream (see quantplan.go)
}

// TRec describes one tensor payload.
type TRec struct {
	DType     DType
	Shape     []int64
	Len       int64
	GapBefore int64
	Segs      []Seg
}

type BaseInfo struct {
	SHA SHA
	Len int64
}

// Container is the parsed form.
type Container struct {
	FileSHA SHA
	FileLen int64
	Bases   []BaseInfo
	Gaps    []byte // concatenated header/padding bytes (resolved)
	GapsLen int64
	GapSlot uint8  // base slot whose gaps predict ours (GapWire != nil)
	GapWire []byte // simdelta program over the base's gaps
	TailGap int64
	T       []TRec
}

func tag(b []byte) uint32 { h := sha256.Sum256(b); return binary.LittleEndian.Uint32(h[:4]) }

// TagOf is the 4-byte identity tag of a tensor payload.
func TagOf(b []byte) uint32 { return tag(b) }

func (c *Container) Marshal() []byte {
	b := []byte(magic)
	b = append(b, c.FileSHA[:]...)
	b = binary.AppendUvarint(b, uint64(c.FileLen))
	b = append(b, byte(len(c.Bases)))
	for _, x := range c.Bases {
		b = append(b, x.SHA[:]...)
		b = binary.AppendUvarint(b, uint64(x.Len))
	}
	gz := Deflate(c.Gaps)
	if c.GapWire != nil {
		b = append(b, 2)
		b = binary.AppendUvarint(b, uint64(c.GapsLen))
		b = append(b, c.GapSlot)
		b = binary.AppendUvarint(b, uint64(len(c.GapWire)))
		b = append(b, c.GapWire...)
	} else if len(gz) >= len(c.Gaps) {
		b = append(b, 0)
		b = binary.AppendUvarint(b, uint64(len(c.Gaps)))
		b = append(b, c.Gaps...)
	} else {
		b = append(b, 1)
		b = binary.AppendUvarint(b, uint64(len(c.Gaps)))
		b = binary.AppendUvarint(b, uint64(len(gz)))
		b = append(b, gz...)
	}
	b = binary.AppendUvarint(b, uint64(c.TailGap))
	b = binary.AppendUvarint(b, uint64(len(c.T)))
	for _, t := range c.T {
		b = append(b, byte(t.DType))
		b = binary.AppendUvarint(b, uint64(len(t.Shape)))
		for _, s := range t.Shape {
			b = binary.AppendUvarint(b, uint64(s))
		}
		b = binary.AppendUvarint(b, uint64(t.Len))
		b = binary.AppendUvarint(b, uint64(t.GapBefore))
		b = binary.AppendUvarint(b, uint64(len(t.Segs)))
		for _, s := range t.Segs {
			b = append(b, s.Mode|byte(s.Narrow)<<2)
			if len(t.Segs) > 1 {
				b = binary.AppendUvarint(b, uint64(s.Elems))
			}
			b = append(b, byte(len(s.Refs)))
			for _, r := range s.Refs {
				b = append(b, r.Slot)
				b = binary.AppendUvarint(b, uint64(r.TIdx))
				b = binary.LittleEndian.AppendUint32(b, r.Tag)
				b = binary.AppendVarint(b, r.Off)
			}
			if s.Mode == ModeQuant {
				b = binary.AppendUvarint(b, uint64(len(s.Q)))
				b = append(b, s.Q...)
			}
			if s.Mode == ModeCoded {
				pm := s.Params.Marshal()
				b = binary.AppendUvarint(b, uint64(len(pm)))
				b = append(b, pm...)
				b = append(b, s.Coder)
				b = binary.AppendUvarint(b, uint64(len(s.Stream)))
				b = append(b, s.Stream...)
			}
		}
	}
	return b
}

type rd struct {
	b   []byte
	pos int
	err error
}

func (r *rd) u() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 {
		r.err = ErrFormat
		return 0
	}
	r.pos += n
	return v
}
func (r *rd) s() int64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Varint(r.b[r.pos:])
	if n <= 0 {
		r.err = ErrFormat
		return 0
	}
	r.pos += n
	return v
}
func (r *rd) raw(n uint64) []byte {
	if r.err != nil || n > uint64(len(r.b)-r.pos) {
		r.err = ErrFormat
		return nil
	}
	v := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return v
}
func (r *rd) byte1() byte {
	v := r.raw(1)
	if v == nil {
		return 0
	}
	return v[0]
}

// ParseContainer validates structure and every length against the remaining bytes.
func ParseContainer(b []byte) (*Container, error) {
	if len(b) < len(magic) || string(b[:len(magic)]) != magic {
		return nil, ErrFormat
	}
	r := &rd{b: b, pos: len(magic)}
	c := &Container{}
	copy(c.FileSHA[:], r.raw(32))
	c.FileLen = int64(r.u())
	nb := int(r.byte1())
	if nb > MaxBases {
		return nil, ErrFormat
	}
	for i := 0; i < nb; i++ {
		var x BaseInfo
		copy(x.SHA[:], r.raw(32))
		x.Len = int64(r.u())
		c.Bases = append(c.Bases, x)
	}
	flag := r.byte1()
	gl := r.u()
	if gl > maxGapBytes {
		return nil, ErrFormat
	}
	if flag == 0 {
		c.Gaps = append([]byte(nil), r.raw(gl)...)
	} else if flag == 1 {
		cl := r.u()
		z := r.raw(cl)
		if r.err == nil {
			g, err := inflateLimit(z, int(gl))
			if err != nil || uint64(len(g)) != gl {
				return nil, ErrFormat
			}
			c.Gaps = g
		}
	} else if flag == 2 {
		c.GapsLen = int64(gl)
		c.GapSlot = r.byte1()
		if int(c.GapSlot) >= nb {
			return nil, ErrFormat
		}
		c.GapWire = append([]byte(nil), r.raw(r.u())...)
	} else {
		return nil, ErrFormat
	}
	if flag != 2 {
		c.GapsLen = int64(len(c.Gaps))
	}
	c.TailGap = int64(r.u())
	nt := r.u()
	if r.err != nil || nt > maxTensors || nt > uint64(len(b)) {
		return nil, ErrFormat
	}
	var total int64
	gapSum := c.TailGap
	for i := uint64(0); i < nt; i++ {
		var t TRec
		t.DType = DType(r.byte1())
		if t.DType > DQOther {
			return nil, ErrFormat
		}
		ns := r.u()
		if ns > 16 {
			return nil, ErrFormat
		}
		elems := int64(1)
		for j := uint64(0); j < ns; j++ {
			d := r.u()
			if d > 1<<40 {
				return nil, ErrFormat
			}
			t.Shape = append(t.Shape, int64(d))
			if d != 0 && elems > (1<<46)/int64(d) {
				return nil, ErrFormat
			}
			elems *= int64(d)
		}
		t.Len = int64(r.u())
		t.GapBefore = int64(r.u())
		if t.Len < 0 || t.Len > maxOutBytes || t.GapBefore < 0 || t.GapBefore > maxGapBytes {
			return nil, ErrFormat
		}
		nseg := r.u()
		if nseg > maxSegs || nseg > uint64(len(b)) {
			return nil, ErrFormat
		}
		var segElems int64
		for j := uint64(0); j < nseg; j++ {
			var s Seg
			m := r.byte1()
			s.Mode, s.Narrow = m&3, DType(m>>2)
			if s.Mode > ModeQuant || s.Narrow > DQOther {
				return nil, ErrFormat
			}
			if nseg > 1 {
				s.Elems = int64(r.u())
			} else {
				s.Elems = -1 // resolved below
			}
			nr := int(r.byte1())
			if nr > MaxBases {
				return nil, ErrFormat
			}
			for k := 0; k < nr; k++ {
				var br BaseRef
				br.Slot = r.byte1()
				br.TIdx = uint32(r.u())
				bt := r.raw(4)
				if bt != nil {
					br.Tag = binary.LittleEndian.Uint32(bt)
				}
				br.Off = r.s()
				if int(br.Slot) >= nb {
					return nil, ErrFormat
				}
				s.Refs = append(s.Refs, br)
			}
			if s.Mode == ModeQuant {
				s.Q = r.raw(r.u())
			}
			if s.Mode == ModeCoded {
				pl := r.u()
				pb := r.raw(pl)
				if r.err != nil {
					return nil, r.err
				}
				p, used, err := ParseParams(pb, len(pb))
				if err != nil || used != len(pb) {
					return nil, ErrFormat
				}
				s.Params = p
				s.Coder = r.byte1()
				if s.Coder >= numCoder {
					return nil, ErrFormat
				}
				sl := r.u()
				s.Stream = r.raw(sl)
			}
			t.Segs = append(t.Segs, s)
		}
		_ = total
		if r.err != nil {
			return nil, r.err
		}
		// single-segment tensors cover the whole tensor
		w := int64(t.DType.Width())
		if nseg == 1 {
			t.Segs[0].Elems = t.Len / w
		}
		for _, s := range t.Segs {
			if s.Elems < 0 {
				return nil, ErrFormat
			}
			segElems += s.Elems
		}
		if t.Len%w != 0 || segElems*w != t.Len || (ns > 0 && t.DType.IsFloat() && elems*w != t.Len) {
			return nil, ErrFormat
		}
		gapSum += t.GapBefore
		total += t.Len
		c.T = append(c.T, t)
	}
	if r.pos != len(b) {
		return nil, ErrFormat
	}
	if c.GapsLen != gapSum || total+gapSum != c.FileLen || c.FileLen > maxOutBytes {
		return nil, ErrFormat
	}
	return c, nil
}

// Resolver supplies decoded tensor payloads of named bases (file dtype, whole tensor).
type Resolver interface {
	Tensor(base SHA, idx int) (DType, []byte, error)
	Gaps(base SHA) ([]byte, error)
	// Window returns bytes [lo,hi) of base tensor idx, decoding only what overlaps (range reads).
	Window(base SHA, idx int, lo, hi int64) (DType, []byte, error)
}

// resolveGaps materializes delta-coded header bytes from the named base's gaps.
func (c *Container) resolveGaps(rv Resolver) error {
	if c.GapWire == nil || c.Gaps != nil {
		return nil
	}
	bg, err := rv.Gaps(c.Bases[c.GapSlot].SHA)
	if err != nil {
		return err
	}
	g, err := simdelta.Decode(c.GapWire, func(id [32]byte) ([]byte, error) {
		if sum(bg) != id {
			return nil, ErrIdentity
		}
		return bg, nil
	})
	if err != nil || int64(len(g)) != c.GapsLen {
		return ErrFormat
	}
	c.Gaps = g
	return nil
}

// DecodeTensor reconstructs payload i of the container.
func (c *Container) DecodeTensor(i int, rv Resolver) ([]byte, error) {
	if i < 0 || i >= len(c.T) {
		return nil, ErrFormat
	}
	t := &c.T[i]
	w := t.DType.Width()
	out := make([]byte, 0, t.Len)
	for si := range t.Segs {
		s := &t.Segs[si]
		bytes, err := c.decodeSeg(t, s, rv)
		if err != nil {
			return nil, fmt.Errorf("tensor %d seg %d: %w", i, si, err)
		}
		if int64(len(bytes)) != s.Elems*int64(w) {
			return nil, ErrFormat
		}
		out = append(out, bytes...)
	}
	return out, nil
}

func (c *Container) baseView(s *Seg, k int, elems int64, tdt DType, rv Resolver, win bool) (Base, error) {
	r := s.Refs[k]
	if win { // range mode: no whole-tensor tag check (identity is established by full decodes)
		dt, _, err := rv.Window(c.Bases[r.Slot].SHA, int(r.TIdx), 0, 0)
		if err != nil {
			return Base{}, err
		}
		bw := int64(dt.Width())
		_, data, err := rv.Window(c.Bases[r.Slot].SHA, int(r.TIdx), r.Off*bw, (r.Off+elems)*bw)
		if err != nil {
			return Base{}, err
		}
		return Base{DT: dt, Data: data}, nil
	}
	dt, data, err := rv.Tensor(c.Bases[r.Slot].SHA, int(r.TIdx))
	if err != nil {
		return Base{}, err
	}
	if tag(data) != r.Tag {
		return Base{}, ErrIdentity
	}
	w := int64(dt.Width())
	// the segment starts at the element offset implied by its position: callers pass it in Off
	lo := r.Off * w
	hi := lo + elems*w
	if dt.Width() == 0 || lo < 0 || hi > int64(len(data)) || hi < lo {
		return Base{}, ErrFormat
	}
	return Base{DT: dt, Data: data[lo:hi]}, nil
}

func (c *Container) decodeSeg(t *TRec, s *Seg, rv Resolver) ([]byte, error) {
	return c.decodeSegW(t, s, rv, false)
}

func (c *Container) decodeSegW(t *TRec, s *Seg, rv Resolver, win bool) ([]byte, error) {
	cd := t.DType
	if s.Narrow != 0 {
		cd = s.Narrow
		if t.DType != DF32 || (cd != DF16 && cd != DBF16) {
			return nil, ErrFormat
		}
	}
	w := cd.Width()
	n := int(s.Elems)
	if s.Mode == ModeCopy {
		if len(s.Refs) != 1 {
			return nil, ErrFormat
		}
		fw := t.DType.Width()
		bv, err := c.baseView(s, 0, s.Elems, t.DType, rv, win)
		if err != nil {
			return nil, err
		}
		if bv.DT != t.DType {
			return nil, ErrIdentity
		}
		_ = fw
		return append([]byte(nil), bv.Data...), nil
	}
	if s.Mode == ModeQuant {
		return c.decodeQuant(t, s, rv)
	}
	p := s.Params
	if p.Kind != KNone && int(p.NB) != len(s.Refs) {
		return nil, ErrFormat
	}
	var bases []Base
	for k := range s.Refs {
		bv, err := c.baseView(s, k, s.Elems, cd, rv, win)
		if err != nil {
			return nil, err
		}
		bases = append(bases, bv)
	}
	shape := segShape(t.Shape, s.Elems)
	pred, err := Predict(p, cd, shape, bases)
	if err != nil {
		return nil, err
	}
	resid, err := DecodeResidual(int(s.Coder), cd, p.Res, s.Stream, pred, n)
	if err != nil {
		return nil, err
	}
	val := ApplyResidual(p.Res, w, resid, pred)
	if s.Narrow != 0 {
		return Widen(val, cd), nil
	}
	return val, nil
}

// segShape gives a segment the tensor's last dimension (rows = elems/cols) so row/matrix
// predictors address the same columns.
func segShape(shape []int64, elems int64) []int64 {
	if len(shape) == 0 {
		return []int64{elems}
	}
	cols := shape[len(shape)-1]
	if cols <= 0 || elems%cols != 0 {
		return []int64{elems}
	}
	return []int64{elems / cols, cols}
}

// DecodeTo reconstructs the whole file into w and verifies length and SHA-256.
func (c *Container) DecodeTo(w io.Writer, rv Resolver) error {
	if err := c.resolveGaps(rv); err != nil {
		return err
	}
	h := sha256.New()
	var n int64
	emit := func(b []byte) error {
		if len(b) == 0 {
			return nil
		}
		k, err := w.Write(b)
		if err == nil && k != len(b) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return err
		}
		h.Write(b)
		n += int64(len(b))
		return nil
	}
	gp := 0
	for i := range c.T {
		if g := c.T[i].GapBefore; g > 0 {
			if err := emit(c.Gaps[gp : gp+int(g)]); err != nil {
				return err
			}
			gp += int(g)
		}
		b, err := c.DecodeTensor(i, rv)
		if err != nil {
			return err
		}
		if err := emit(b); err != nil {
			return err
		}
	}
	if err := emit(c.Gaps[gp:]); err != nil {
		return err
	}
	var sum SHA
	copy(sum[:], h.Sum(nil))
	if n != c.FileLen || sum != c.FileSHA {
		return ErrDigest
	}
	return nil
}

// Decode returns the verified file bytes.
func Decode(container []byte, rv Resolver) ([]byte, error) {
	c, err := ParseContainer(container)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Grow(int(c.FileLen))
	if err := c.DecodeTo(&buf, rv); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadRange writes bytes [off, off+n) of the reconstructed file, decoding only the segments
// that overlap the range. It verifies segment boundaries, not the whole-file digest (a digest
// needs a sequential pass), and reports the payload bytes it had to reconstruct.
func (c *Container) ReadRange(w io.Writer, off, n int64, rv Resolver) (decoded int64, err error) {
	if off < 0 || n < 0 || off+n > c.FileLen {
		return 0, ErrFormat
	}
	if err := c.resolveGaps(rv); err != nil {
		return 0, err
	}
	pos, gp := int64(0), int64(0)
	end := off + n
	emit := func(b []byte, at int64) error {
		lo, hi := off-at, end-at
		if lo < 0 {
			lo = 0
		}
		if hi > int64(len(b)) {
			hi = int64(len(b))
		}
		if hi <= lo {
			return nil
		}
		k, err := w.Write(b[lo:hi])
		if err == nil && int64(k) != hi-lo {
			err = io.ErrShortWrite
		}
		return err
	}
	for i := range c.T {
		t := &c.T[i]
		g := t.GapBefore
		if g > 0 {
			if pos < end && pos+g > off {
				if err := emit(c.Gaps[gp:gp+g], pos); err != nil {
					return decoded, err
				}
			}
			pos += g
			gp += g
		}
		tw := int64(t.DType.Width())
		sp := pos
		for si := range t.Segs {
			s := &t.Segs[si]
			sl := s.Elems * tw
			if sp < end && sp+sl > off {
				b, err := c.decodeSegW(t, s, rv, true)
				if err != nil {
					return decoded, err
				}
				decoded += int64(len(b))
				if err := emit(b, sp); err != nil {
					return decoded, err
				}
			}
			sp += sl
		}
		pos += t.Len
	}
	if pos < end && gp < int64(len(c.Gaps)) {
		if err := emit(c.Gaps[gp:], pos); err != nil {
			return decoded, err
		}
	}
	return decoded, nil
}

// DecodeWindow reconstructs bytes [lo,hi) of tensor idx, decoding only overlapping segments.
func (c *Container) DecodeWindow(idx int, lo, hi int64, rv Resolver) ([]byte, error) {
	if idx < 0 || idx >= len(c.T) {
		return nil, ErrFormat
	}
	t := &c.T[idx]
	if lo < 0 || hi < lo || hi > t.Len {
		return nil, ErrFormat
	}
	w := int64(t.DType.Width())
	out := make([]byte, 0, hi-lo)
	pos := int64(0)
	for si := range t.Segs {
		s := &t.Segs[si]
		sl := s.Elems * w
		if pos < hi && pos+sl > lo && hi > lo {
			b, err := c.decodeSegW(t, s, rv, true)
			if err != nil {
				return nil, err
			}
			a, z := lo-pos, hi-pos
			if a < 0 {
				a = 0
			}
			if z > sl {
				z = sl
			}
			out = append(out, b[a:z]...)
		}
		pos += sl
	}
	return out, nil
}
