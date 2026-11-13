package numcore

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// ZN7 is the windowed model container: every tensor is cut into independent windows (a bounded
// number of elements), each with its own recipe, payload and 128-bit digest, so encode, replay and
// range reads touch a bounded amount of data. Layout of one object:
//
//	"ZN7\x01" | gap payload | window payloads (file order) | directory | trailer
//
// The directory (tensor table, per-window entries, base list, file digest) is written last so the
// encoder streams; readers find it through the fixed trailer and address any window directly.

const (
	zMagic     = "ZN7\x01"
	zTrailer   = "ZN7T"
	zTrailLen  = 8 + 8 + 4
	MaxDepth7  = 4 // objects on a dependency path, the decoded object included
	MaxHops7   = MaxDepth7 - 1
	MaxBases7  = 16
	maxTensor7 = 1 << 20
	maxWindows = 1 << 24
	maxDir7    = 1 << 28
	MaxGap7    = 1 << 28
	maxWinByte = 1 << 28 // decoded bytes of one window
	maxName7   = 1 << 12
)

// Window modes.
const (
	MRaw    = 0 // payload is the bytes (stored or DEFLATE)
	MDirect = 1 // float words without a base (FPE / byte planes)
	MPred   = 2 // residual against a prediction from the base tensor
	MCopy   = 3 // equals the (dtype-converted, permuted) base tensor range; no payload
	MQuant  = 4 // ggml quantization of the float base rows plus sparse block patches
)

// Payload coders.
const (
	CStore  = 0
	CFlate  = 1
	CFPE    = 2
	CNRC    = 3
	CPlanes = 4
)

var (
	ErrFormat7  = errors.New("numcore: malformed ZN7 object")
	ErrDepth7   = errors.New("numcore: dependency depth exceeded")
	ErrMissing7 = errors.New("numcore: missing dependency")
	ErrAuth7    = errors.New("numcore: window digest mismatch")
	ErrDigest7  = errors.New("numcore: object digest mismatch")
	ErrLayout7  = errors.New("numcore: invalid tensor layout")
	ErrRange7   = errors.New("numcore: range outside object")
)

// ID is the SHA-256 of an original file (object identity).
type ID [32]byte

// Tensor is one payload span of a model file. Cols is the row length in elements for
// row-structured operations (0 = flat).
type Tensor struct {
	Name     string
	DT       DType
	Off, Len int64
	Cols     int64
}

// Layout is the tensor table of a file; everything outside the spans is header/padding ("gaps").
type Layout struct {
	Size int64
	T    []Tensor // ascending Off, disjoint
}

// Validate checks spans, ordering and block geometry with overflow-safe arithmetic.
func (l *Layout) Validate() error {
	if l == nil || l.Size < 0 || len(l.T) > maxTensor7 {
		return ErrLayout7
	}
	prev := int64(0)
	for i := range l.T {
		t := &l.T[i]
		if t.Off < prev || t.Len < 0 || t.Len > l.Size-t.Off || t.Cols < 0 || len(t.Name) > maxName7 {
			return ErrLayout7
		}
		if u := unitOf(t.DT, true); t.Len%int64(u) != 0 {
			return ErrLayout7
		}
		prev = t.Off + t.Len
	}
	return nil
}

// GapLen is the number of bytes outside tensor spans.
func (l *Layout) GapLen() int64 {
	n := l.Size
	for i := range l.T {
		n -= l.T[i].Len
	}
	return n
}

// PermHint is a row permutation applied when a base tensor is read (the converter's layout
// change). Kind 1: llama.cpp head interleave over rows with Heads attention heads.
type PermHint struct {
	Kind  uint8
	Heads uint16
}

func (p PermHint) srcRow(r, rows int64) int64 {
	if p.Kind != 1 || p.Heads == 0 {
		return r
	}
	hd := rows / int64(p.Heads) / 2
	if hd == 0 || rows != hd*2*int64(p.Heads) {
		return r
	}
	h, in := r/(2*hd), r%(2*hd)
	return h*2*hd + (in%2)*hd + in/2
}

// Ref names the base tensor of a derived tensor.
type Ref struct {
	Slot uint8
	TIdx uint32
	Perm PermHint
}

// QParams describes a quantization derivation: which quantizer path reproduced the producer.
type QParams struct {
	Arith  uint8  // qcore.Arith
	ViaF16 bool   // source values pass through float16 first (producer quantized an F16 file)
	Aux    int    // index into Object.Aux of the importance matrix, -1 = reference quantizer
	Name   string // imatrix entry name
}

// WDir is the directory entry of one window.
type WDir struct {
	Mode, Coder uint8
	Narrow      DType // coding dtype when it differs from the tensor dtype (exact F32 narrowing)
	PLen        int64
	Sum         [16]byte // SHA-256/128 of the decoded window
}

// TDir is the directory entry of one tensor.
type TDir struct {
	DT       DType
	Off, Len int64
	Cols     int64
	Win      int64 // units per window: elements (float), blocks (quant derivation), bytes (opaque)
	Ref      *Ref
	P        *Params // structured predictor (nil = identity)
	Q        *QParams
	W        []WDir
	pOff     int64 // payload offset of window 0
}

type BaseInfo struct {
	ID  ID
	Len int64
}

// unitOf is the byte size of one window unit. derived selects the quant-block unit.
func unitOf(d DType, derived bool) int {
	if d.IsFloat() {
		return d.Width()
	}
	if derived {
		if q, ok := d.QType(); ok {
			_, by := qBlock(q)
			return by
		}
	}
	return 1
}

func (t *TDir) unit() int { return unitOf(t.DT, t.Q != nil) }

// winBytes is the decoded byte span of window k relative to the tensor.
func (t *TDir) winBytes(k int64) (off, n int64) {
	wb := t.Win * int64(t.unit())
	off = k * wb
	n = t.Len - off
	if n > wb {
		n = wb
	}
	return off, n
}

func numWindows(length, win int64, unit int) (int64, bool) {
	if win <= 0 || unit <= 0 || win > maxWinByte/int64(unit) {
		return 0, false
	}
	wb := win * int64(unit)
	n := (length + wb - 1) / wb
	if n > maxWindows {
		return 0, false
	}
	return n, true
}

// Object is a parsed ZN7 directory over a ReaderAt.
type Object struct {
	r       io.ReaderAt
	size    int64
	FileLen int64
	FileSHA ID
	Bases   []BaseInfo
	Aux     []ID
	T       []TDir
	GapMode uint8 // 0 DEFLATE, 1 dcore delta against Bases[GapSlot] gaps
	GapSlot uint8
	GapLen  int64
	GapPLen int64
	GapSum  [16]byte
	DirLen  int64
}

func sum16(b []byte) (s [16]byte) {
	h := sha256.Sum256(b)
	copy(s[:], h[:16])
	return
}

func readFull(r io.ReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// ---- directory wire form ----

type dw struct{ b []byte }

func (w *dw) u(v uint64)   { w.b = binary.AppendUvarint(w.b, v) }
func (w *dw) raw(p []byte) { w.b = append(w.b, p...) }

type dr struct {
	b   []byte
	p   int
	bad bool
}

func (r *dr) u() uint64 {
	v, n := binary.Uvarint(r.b[r.p:])
	if n <= 0 {
		r.bad = true
		return 0
	}
	r.p += n
	return v
}

// i returns a non-negative int64 bounded by max.
func (r *dr) i(max int64) int64 {
	v := r.u()
	if v > uint64(max) {
		r.bad = true
		return 0
	}
	return int64(v)
}

func (r *dr) raw(n int) []byte {
	if r.bad || n < 0 || n > len(r.b)-r.p {
		r.bad = true
		return nil
	}
	out := r.b[r.p : r.p+n]
	r.p += n
	return out
}

func (o *Object) marshalDir() []byte {
	w := &dw{}
	w.u(uint64(o.FileLen))
	w.raw(o.FileSHA[:])
	w.u(uint64(len(o.Bases)))
	for _, b := range o.Bases {
		w.raw(b.ID[:])
		w.u(uint64(b.Len))
	}
	w.u(uint64(len(o.Aux)))
	for _, a := range o.Aux {
		w.raw(a[:])
	}
	w.u(uint64(o.GapMode))
	w.u(uint64(o.GapSlot))
	w.u(uint64(o.GapLen))
	w.u(uint64(o.GapPLen))
	w.raw(o.GapSum[:])
	w.u(uint64(len(o.T)))
	for i := range o.T {
		t := &o.T[i]
		w.u(uint64(t.DT))
		w.u(uint64(t.Off))
		w.u(uint64(t.Len))
		w.u(uint64(t.Cols))
		w.u(uint64(t.Win))
		fl := uint64(0)
		if t.Ref != nil {
			fl |= 1
		}
		if t.P != nil {
			fl |= 2
		}
		if t.Q != nil {
			fl |= 4
		}
		w.u(fl)
		if t.Ref != nil {
			w.u(uint64(t.Ref.Slot))
			w.u(uint64(t.Ref.TIdx))
			w.u(uint64(t.Ref.Perm.Kind))
			w.u(uint64(t.Ref.Perm.Heads))
		}
		if t.P != nil {
			pb := t.P.Marshal()
			w.u(uint64(len(pb)))
			w.raw(pb)
		}
		if t.Q != nil {
			w.u(uint64(t.Q.Arith))
			v := uint64(0)
			if t.Q.ViaF16 {
				v = 1
			}
			w.u(v)
			w.u(uint64(t.Q.Aux + 1))
			w.u(uint64(len(t.Q.Name)))
			w.raw([]byte(t.Q.Name))
		}
		for k := range t.W {
			wd := &t.W[k]
			w.raw([]byte{wd.Mode | wd.Coder<<3, byte(wd.Narrow)})
			w.u(uint64(wd.PLen))
			w.raw(wd.Sum[:])
		}
	}
	return w.b
}

// OpenObject parses and validates the directory of a ZN7 object. Every length, count and offset
// is bounded by the object size before anything is allocated from it.
func OpenObject(r io.ReaderAt, size int64) (*Object, error) {
	if size < int64(len(zMagic))+zTrailLen {
		return nil, ErrFormat7
	}
	var head [4]byte
	var tr [zTrailLen]byte
	if err := readFull(r, head[:], 0); err != nil {
		return nil, err
	}
	if err := readFull(r, tr[:], size-zTrailLen); err != nil {
		return nil, err
	}
	if string(head[:]) != zMagic || string(tr[16:]) != zTrailer {
		return nil, ErrFormat7
	}
	dirOff, dirLen := binary.LittleEndian.Uint64(tr[0:]), binary.LittleEndian.Uint64(tr[8:])
	body := uint64(size - zTrailLen)
	if dirLen > maxDir7 || dirOff < 4 || dirOff > body || dirLen != body-dirOff {
		return nil, ErrFormat7
	}
	db := make([]byte, dirLen)
	if err := readFull(r, db, int64(dirOff)); err != nil {
		return nil, err
	}
	d := &dr{b: db}
	o := &Object{r: r, size: size, DirLen: int64(dirLen)}
	o.FileLen = d.i(math.MaxInt64 / 2)
	copy(o.FileSHA[:], d.raw(32))
	nb := d.i(MaxBases7)
	for i := int64(0); i < nb && !d.bad; i++ {
		var b BaseInfo
		copy(b.ID[:], d.raw(32))
		b.Len = d.i(math.MaxInt64 / 2)
		o.Bases = append(o.Bases, b)
	}
	na := d.i(MaxBases7)
	for i := int64(0); i < na && !d.bad; i++ {
		var a ID
		copy(a[:], d.raw(32))
		o.Aux = append(o.Aux, a)
	}
	o.GapMode = uint8(d.i(1))
	o.GapSlot = uint8(d.i(MaxBases7))
	o.GapLen = d.i(MaxGap7)
	o.GapPLen = d.i(int64(dirOff))
	copy(o.GapSum[:], d.raw(16))
	nt := d.i(maxTensor7)
	if d.bad || (o.GapMode == 1 && int(o.GapSlot) >= len(o.Bases)) || nt > int64(len(db)) {
		return nil, ErrFormat7
	}
	pos := int64(4) + o.GapPLen
	prev, tensorBytes := int64(0), int64(0)
	o.T = make([]TDir, nt)
	for i := range o.T {
		t := &o.T[i]
		t.DT = DType(d.i(255))
		t.Off = d.i(o.FileLen)
		t.Len = d.i(o.FileLen)
		t.Cols = d.i(math.MaxInt64 / 2)
		t.Win = d.i(maxWinByte)
		fl := d.i(7)
		if d.bad || dtNames[t.DT] == "" || t.Off < prev || t.Len > o.FileLen-t.Off {
			return nil, ErrFormat7
		}
		prev = t.Off + t.Len
		tensorBytes += t.Len
		if fl&1 != 0 {
			t.Ref = &Ref{Slot: uint8(d.i(MaxBases7)), TIdx: uint32(d.i(maxTensor7))}
			t.Ref.Perm = PermHint{Kind: uint8(d.i(1)), Heads: uint16(d.i(1 << 15))}
			if int(t.Ref.Slot) >= len(o.Bases) {
				return nil, ErrFormat7
			}
		}
		if fl&2 != 0 {
			pl := d.i(int64(len(db)))
			pb := d.raw(int(pl))
			if d.bad || !t.DT.IsFloat() || t.Ref == nil {
				return nil, ErrFormat7
			}
			p, used, err := ParseParams(pb, len(pb))
			if err != nil || used != len(pb) || (p.Kind != KAff && p.Kind != KLow) {
				return nil, ErrFormat7
			}
			t.P = p
		}
		if fl&4 != 0 {
			q := &QParams{Arith: uint8(d.i(1))}
			q.ViaF16 = d.i(1) == 1
			q.Aux = int(d.i(int64(len(o.Aux)))) - 1
			q.Name = string(d.raw(int(d.i(maxName7))))
			if _, ok := t.DT.QType(); !ok || t.Ref == nil || t.Cols <= 0 {
				return nil, ErrFormat7
			}
			t.Q = q
		}
		u := t.unit()
		nw, ok := numWindows(t.Len, t.Win, u)
		if d.bad || !ok || t.Len%int64(u) != 0 || nw > int64(len(db)-d.p)/19 {
			return nil, ErrFormat7
		}
		if t.Q != nil { // windows are whole rows of whole blocks
			el, _ := qBlock(mustQ(t.DT))
			if t.Cols%int64(el) != 0 || t.Win%(t.Cols/int64(el)) != 0 {
				return nil, ErrFormat7
			}
		}
		t.pOff = pos
		t.W = make([]WDir, nw)
		for k := range t.W {
			wd := &t.W[k]
			hb := d.raw(2)
			if d.bad {
				return nil, ErrFormat7
			}
			wd.Mode, wd.Coder, wd.Narrow = hb[0]&7, hb[0]>>3, DType(hb[1])
			wd.PLen = d.i(int64(dirOff))
			copy(wd.Sum[:], d.raw(16))
			if d.bad || wd.PLen > int64(dirOff)-pos || !wd.valid(t) {
				return nil, ErrFormat7
			}
			pos += wd.PLen
		}
	}
	if d.bad || d.p != len(db) || pos != int64(dirOff) || o.GapLen != o.FileLen-tensorBytes {
		return nil, ErrFormat7
	}
	return o, nil
}

func (wd *WDir) valid(t *TDir) bool {
	switch wd.Mode {
	case MRaw:
		return wd.Coder <= CFlate && wd.Narrow == 0
	case MDirect:
		return t.DT.IsFloat() && (wd.Coder == CFPE || wd.Coder == CPlanes || wd.Coder == CStore) && wd.narrowOK(t)
	case MPred:
		return t.DT.IsFloat() && t.Ref != nil && (wd.Coder == CNRC || wd.Coder == CPlanes) && wd.narrowOK(t)
	case MCopy:
		return t.Ref != nil && wd.PLen == 0 && wd.Narrow == 0 && t.Q == nil
	case MQuant:
		return t.Q != nil && wd.Coder <= CFlate && wd.Narrow == 0
	}
	return false
}

func (wd *WDir) narrowOK(t *TDir) bool {
	return wd.Narrow == 0 || (t.DT == DF32 && (wd.Narrow == DF16 || wd.Narrow == DBF16))
}

// Layout rebuilds the tensor table of the original file (names are not stored).
func (o *Object) Layout() *Layout {
	l := &Layout{Size: o.FileLen, T: make([]Tensor, len(o.T))}
	for i := range o.T {
		l.T[i] = Tensor{DT: o.T[i].DT, Off: o.T[i].Off, Len: o.T[i].Len, Cols: o.T[i].Cols}
	}
	return l
}

// ---- small codecs ----

func deflateBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

// inflateExact inflates exactly n bytes (bounded: a longer stream is an error, not an allocation).
func inflateExact(b []byte, n int) ([]byte, error) {
	out := make([]byte, n)
	zr := flate.NewReader(bytes.NewReader(b))
	if _, err := io.ReadFull(zr, out); err != nil {
		return nil, ErrFormat7
	}
	var one [1]byte
	if k, _ := zr.Read(one[:]); k != 0 {
		return nil, ErrFormat7
	}
	return out, nil
}

func planesPack(words []byte, w int) []byte {
	n := len(words) / w
	pl := make([]byte, len(words))
	for k := 0; k < w; k++ {
		row := pl[k*n : (k+1)*n]
		for i := range row {
			row[i] = words[i*w+(w-1-k)]
		}
	}
	return deflateBytes(pl)
}

func planesUnpack(payload []byte, w, n int) ([]byte, error) {
	pl, err := inflateExact(payload, n*w)
	if err != nil {
		return nil, err
	}
	out := make([]byte, n*w)
	for k := 0; k < w; k++ {
		row := pl[k*n : (k+1)*n]
		for i, v := range row {
			out[i*w+(w-1-k)] = v
		}
	}
	return out, nil
}

// predWindow turns base element bytes into predicted target words (dtype cd) for target elements
// [lo, lo+n). p == nil is the identity (dtype conversion only).
func predWindow(p *Params, cd DType, b Base, lo int64, n int, cols int64) ([]byte, error) {
	w := cd.Width()
	if b.n() != n {
		return nil, ErrFormat7
	}
	out := make([]byte, n*w)
	if p == nil {
		if b.DT == cd {
			copy(out, b.Data)
			return out, nil
		}
		for i := 0; i < n; i++ {
			PutLE(out, w, i, idPred(cd, b, i))
		}
		return out, nil
	}
	switch p.Kind {
	case KAff:
		if p.Gran != GTensor || len(p.Coef) != 2 {
			return nil, ErrParams
		}
		a, c := float64(p.Coef[0]), float64(p.Coef[1])
		for i := 0; i < n; i++ {
			bv := b.at(i)
			pv := float64(a*bv) + c
			if !Finite(pv) || !Finite(bv) {
				PutLE(out, w, i, idPred(cd, b, i))
				continue
			}
			PutLE(out, w, i, FromF64(cd, pv))
		}
	case KLow:
		r := int64(p.Rank)
		if r <= 0 || cols <= 0 || int64(len(p.Coef))%r != 0 {
			return nil, ErrParams
		}
		rows := int64(len(p.Coef))/r - cols
		if rows <= 0 || lo < 0 || lo+int64(n) > rows*cols {
			return nil, ErrParams
		}
		U, V := p.Coef[:rows*r], p.Coef[rows*r:]
		for i := 0; i < n; i++ {
			e := lo + int64(i)
			ui, vj := U[(e/cols)*r:(e/cols+1)*r], V[(e%cols)*r:(e%cols+1)*r]
			var s float64
			for k := range ui {
				s += float64(float64(ui[k]) * float64(vj[k]))
			}
			bv := b.at(i)
			pv := bv + s
			if !Finite(pv) || !Finite(bv) {
				PutLE(out, w, i, idPred(cd, b, i))
				continue
			}
			PutLE(out, w, i, FromF64(cd, pv))
		}
	default:
		return nil, ErrParams
	}
	return out, nil
}

// baseFn supplies base tensor elements [lo, lo+n) of the window's reference (permutation applied).
type baseFn func(ctx context.Context, lo int64, n int) (Base, error)

// rebuild reconstructs one window from its payload. It is the single decode path used by replay,
// range reads and the encoder's own verification. dst has the window's decoded size.
func rebuild(ctx context.Context, t *TDir, wd *WDir, k int64, payload []byte, dst []byte, base baseFn, aux auxFn) error {
	if int64(len(payload)) != wd.PLen {
		return ErrFormat7
	}
	u := t.unit()
	n := len(dst) / u
	switch wd.Mode {
	case MRaw:
		if wd.Coder == CStore {
			if len(payload) != len(dst) {
				return ErrFormat7
			}
			copy(dst, payload)
			return nil
		}
		out, err := inflateExact(payload, len(dst))
		if err != nil {
			return err
		}
		copy(dst, out)
		return nil
	case MDirect, MPred, MCopy:
		if !t.DT.IsFloat() {
			if wd.Mode != MCopy { // opaque copy: identical bytes of the base tensor
				return ErrFormat7
			}
			b, err := base(ctx, k*t.Win, len(dst))
			if err != nil {
				return err
			}
			if len(b.Data) != len(dst) {
				return ErrFormat7
			}
			copy(dst, b.Data)
			return nil
		}
		cd := t.DT
		if wd.Narrow != 0 {
			cd = wd.Narrow
		}
		w := cd.Width()
		var words []byte
		var err error
		if wd.Mode == MDirect {
			switch wd.Coder {
			case CFPE:
				words, err = FPEDecode(cd, payload, n)
			case CPlanes:
				words, err = planesUnpack(payload, w, n)
			default:
				if len(payload) != n*w {
					return ErrFormat7
				}
				words = payload
			}
		} else {
			b, e := base(ctx, k*t.Win, n)
			if e != nil {
				return e
			}
			pred, e := predWindow(t.P, cd, b, k*t.Win, n, t.Cols)
			if e != nil {
				return e
			}
			if wd.Mode == MCopy {
				words = pred
			} else {
				var resid []byte
				if wd.Coder == CNRC {
					resid, err = NRCDecode(cd, payload, pred, n)
				} else {
					resid, err = planesUnpack(payload, w, n)
				}
				if err == nil {
					words = ApplyResidual(RKey, w, resid, pred)
				}
			}
		}
		if err != nil {
			return err
		}
		if len(words) != n*w {
			return ErrFormat7
		}
		if wd.Narrow != 0 {
			words = Widen(words, cd)
		}
		copy(dst, words)
		return nil
	case MQuant:
		return rebuildQuant(ctx, t, wd, k, payload, dst, base, aux)
	}
	return ErrFormat7
}
