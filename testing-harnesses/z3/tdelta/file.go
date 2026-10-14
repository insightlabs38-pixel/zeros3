package tdelta

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"zeros3-z3/st"
)

// Whole-file Safetensors container (Z3-10 laboratory form, not a persistent format).
//
//	byte      flags: bit0 = a base file is referenced
//	[32]byte  SHA-256 of the base file (when bit0)
//	uvarint   total target file length
//	header    mode 0: identical to the base header bytes
//	          mode 1: uvarint length + raw header bytes
//	          mode 2: uvarint length of the DEFLATE of (header XOR base header), same length
//	tensors   one wire record per tensor in file order (see tensor.go)
//
// The 8-byte length prefix is implied by the header length. Exactness is
// checked by the caller against the target length and SHA-256.

// ElemWidth is the element width in bytes used for byte-plane coding.
func ElemWidth(t *st.Tensor) int {
	if t.Bits >= 8 && t.Bits%8 == 0 && t.Bits <= 64 {
		return t.Bits / 8
	}
	return 1
}

// TensorRep reports one tensor of an encoded file.
type TensorRep struct {
	Name, DType string
	Size        int
	Mode        Mode
	Wire        int
	HasBase     bool
	Cands       []Cand
}

// FileRep reports an encoded file.
type FileRep struct {
	Fixed   int // flags + base ref + length
	Header  int // header section incl. mode byte
	Tensors []TensorRep
	Total   int
}

func baseTensor(bf *st.File, base []byte, t *st.Tensor) []byte {
	if bf == nil {
		return nil
	}
	b, ok := bf.Lookup(t.Name)
	if !ok || b.DType != t.DType || len(b.Shape) != len(t.Shape) || b.Size() != t.Size() {
		return nil
	}
	for i := range b.Shape {
		if b.Shape[i] != t.Shape[i] {
			return nil
		}
	}
	return base[bf.DataStart+b.Begin : bf.DataStart+b.End]
}

func tensorSpec(t *st.Tensor) Spec { return Spec{Shape: t.Shape, W: ElemWidth(t)} }

func headerSection(tf, bf *st.File) []byte {
	if bf != nil && bytes.Equal(tf.Header, bf.Header) {
		return []byte{0}
	}
	best := binary.AppendUvarint([]byte{1}, uint64(len(tf.Header)))
	best = append(best, tf.Header...)
	if bf != nil && len(bf.Header) == len(tf.Header) {
		z := deflateBytes(xor(bf.Header, tf.Header))
		c := binary.AppendUvarint([]byte{2}, uint64(len(z)))
		c = append(c, z...)
		if len(c) < len(best) {
			best = c
		}
	}
	return best
}

// EncodeFile encodes target (parsed as tf) against base (parsed as bf; both may be nil).
func EncodeFile(target []byte, tf *st.File, base []byte, bf *st.File, opt Options) ([]byte, *FileRep, error) {
	rep := &FileRep{}
	var out []byte
	if bf != nil {
		out = append(out, 1)
		h := sha256.Sum256(base)
		out = append(out, h[:]...)
	} else {
		out = append(out, 0)
	}
	out = binary.AppendUvarint(out, uint64(len(target)))
	rep.Fixed = len(out)
	hs := headerSection(tf, bf)
	out = append(out, hs...)
	rep.Header = len(hs)
	for i := range tf.Tensors {
		t := &tf.Tensors[i]
		tgt := target[tf.DataStart+t.Begin : tf.DataStart+t.End]
		rec, cands, err := EncodeTensor(tensorSpec(t), baseTensor(bf, base, t), tgt, opt)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, rec...)
		rep.Tensors = append(rep.Tensors, TensorRep{Name: t.Name, DType: t.DType, Size: len(tgt), Mode: Mode(rec[0]), Wire: len(rec), HasBase: baseTensor(bf, base, t) != nil, Cands: cands})
	}
	rep.Total = len(out)
	return out, rep, nil
}

// BaseRef returns the SHA-256 of the referenced base file, if the container has one.
func BaseRef(cont []byte) ([32]byte, bool) {
	var h [32]byte
	if len(cont) < 1 || cont[0]&1 == 0 || len(cont) < 33 {
		return h, false
	}
	copy(h[:], cont[1:33])
	return h, true
}

type prefixReader struct{ b []byte }

func (p prefixReader) ReadAt(q []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(p.b)) {
		return 0, io.EOF
	}
	n := copy(q, p.b[off:])
	if n < len(q) {
		return n, io.EOF
	}
	return n, nil
}

// maxFile bounds the reconstructed size the decoder will allocate.
var maxFile uint64 = 1 << 32

// DecodeFile rebuilds the target file. base/bf must match the container's reference.
func DecodeFile(cont []byte, base []byte, bf *st.File) ([]byte, error) {
	if len(cont) < 1 || cont[0] > 1 {
		return nil, ErrCorrupt
	}
	has := cont[0]&1 != 0
	cont = cont[1:]
	if has {
		if len(cont) < 32 || bf == nil {
			return nil, ErrCorrupt
		}
		if h := sha256.Sum256(base); !bytes.Equal(h[:], cont[:32]) {
			return nil, errors.New("tdelta: base does not match the container reference")
		}
		cont = cont[32:]
	} else {
		bf = nil
	}
	total, cont, ok := uv(cont)
	if !ok || total < 8 || total > maxFile {
		return nil, ErrCorrupt
	}
	if len(cont) < 1 {
		return nil, ErrCorrupt
	}
	var hdr []byte
	switch cont[0] {
	case 0:
		if bf == nil {
			return nil, ErrCorrupt
		}
		hdr = bf.Header
		cont = cont[1:]
	case 1, 2:
		mode := cont[0]
		l, r, ok := uv(cont[1:])
		if !ok || l > uint64(len(r)) {
			return nil, ErrCorrupt
		}
		if mode == 1 {
			hdr = r[:l]
		} else {
			if bf == nil {
				return nil, ErrCorrupt
			}
			x, err := inflateBytes(r[:l], len(bf.Header))
			if err != nil || len(x) != len(bf.Header) {
				return nil, ErrCorrupt
			}
			hdr = xor(bf.Header, x)
		}
		cont = r[l:]
	default:
		return nil, ErrCorrupt
	}
	pre := binary.LittleEndian.AppendUint64(nil, uint64(len(hdr)))
	tf, err := st.Parse(prefixReader{append(pre, hdr...)}, int64(total), st.Options{AllowUnknownDType: true})
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, total)
	out = append(out, pre...)
	out = append(out, hdr...)
	for i := range tf.Tensors {
		t := &tf.Tensors[i]
		m, payload, rest, err := ParseWire(cont, int(t.Size()))
		if err != nil {
			return nil, err
		}
		cont = rest
		b, err := DecodeTensor(tensorSpec(t), baseTensor(bf, base, t), m, payload, int(t.Size()))
		if err != nil {
			return nil, fmt.Errorf("tensor %q: %w", t.Name, err)
		}
		out = append(out, b...)
	}
	if len(cont) != 0 || int64(len(out)) != int64(total) {
		return nil, ErrCorrupt
	}
	return out, nil
}
