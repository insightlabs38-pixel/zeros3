// Package semcas is the Z4-B3 research substrate: typed semantic components with
// explicit identity, a small set of exact derivation primitives (permute, byte swap,
// float convert, Q8_0 quantize, slice, concat) each followed by an exact sparse residual,
// and file recipes made only of literals and component references. Nothing here executes
// foreign code; every derived byte is authenticated by SHA-256 before use.
package semcas

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
)

// Semantic kinds. Two descriptors of different kind never share a CID.
const (
	KindTensor  = 1 // n-d numeric array, canonical C order, little endian
	KindBytes   = 2 // opaque ordered bytes with no further semantics
	KindFile    = 3 // complete file/blob content (Git blob, tar member, ...)
	KindColumn  = 4 // ordered column of values (nulls separate)
	KindValid   = 5 // validity bitmap of a column (1 = present)
	KindLengths = 6 // per-value byte lengths of a binary column
)

// Data types. Floats and integers are IEEE/two's complement little endian; GGML block
// types are DQuantBase+ggml id and are distinct values, never convertible by identity.
const (
	DBool  = 1
	DU8    = 2
	DI8    = 3
	DU16   = 4
	DI16   = 5
	DU32   = 6
	DI32   = 7
	DU64   = 8
	DI64   = 9
	DF16   = 10
	DBF16  = 11
	DF32   = 12
	DF64   = 13
	DQBase = 100 // + ggml type id
)

// DTypeName returns a stable short name.
func DTypeName(d int) string {
	names := map[int]string{DBool: "BOOL", DU8: "U8", DI8: "I8", DU16: "U16", DI16: "I16", DU32: "U32",
		DI32: "I32", DU64: "U64", DI64: "I64", DF16: "F16", DBF16: "BF16", DF32: "F32", DF64: "F64"}
	if n, ok := names[d]; ok {
		return n
	}
	if d >= DQBase {
		return fmt.Sprintf("GGML%d", d-DQBase)
	}
	return fmt.Sprintf("D%d", d)
}

// ElemBytes is the width of a scalar dtype (0 for block or unknown types).
func ElemBytes(d int) int {
	switch d {
	case DBool, DU8, DI8:
		return 1
	case DU16, DI16, DF16, DBF16:
		return 2
	case DU32, DI32, DF32:
		return 4
	case DU64, DI64, DF64:
		return 8
	}
	return 0
}

// IsFloat reports IEEE binary16/bfloat16/32/64.
func IsFloat(d int) bool { return d == DF16 || d == DBF16 || d == DF32 || d == DF64 }

// Desc is the semantic description that, with the payload digest, forms a component
// identity. Version is part of the identity so a changed canonical form never aliases.
type Desc struct {
	Kind    uint8
	Version uint8
	DType   uint16
	Shape   []uint64 // tensor dims or [count]; empty for scalars and bytes
	// Ordered is true when element order is semantic (always for tensors and columns).
	Ordered bool
	// Extra carries kind-specific canonical attributes (schema, nullability, dictionary).
	Extra []byte
}

// Elems is the element count (product of Shape), with overflow reported.
func (d *Desc) Elems() (uint64, error) {
	n := uint64(1)
	for _, s := range d.Shape {
		hi, lo := bits.Mul64(n, s)
		if hi != 0 || lo > 1<<62 {
			return 0, errors.New("semcas: element count overflow")
		}
		n = lo
	}
	return n, nil
}

// Marshal is the canonical descriptor encoding used for identity.
func (d *Desc) Marshal() []byte {
	b := []byte{d.Kind, d.Version}
	b = binary.AppendUvarint(b, uint64(d.DType))
	if d.Ordered {
		b = append(b, 1)
	} else {
		b = append(b, 0)
	}
	b = binary.AppendUvarint(b, uint64(len(d.Shape)))
	for _, s := range d.Shape {
		b = binary.AppendUvarint(b, s)
	}
	b = binary.AppendUvarint(b, uint64(len(d.Extra)))
	return append(b, d.Extra...)
}

// ParseDesc inverts Marshal and returns the number of bytes consumed.
func ParseDesc(b []byte) (Desc, int, error) {
	var d Desc
	if len(b) < 3 {
		return d, 0, errShort
	}
	d.Kind, d.Version = b[0], b[1]
	pos := 2
	u := func() (uint64, error) {
		v, n := binary.Uvarint(b[pos:])
		if n <= 0 {
			return 0, errShort
		}
		pos += n
		return v, nil
	}
	dt, err := u()
	if err != nil || dt > 1<<16-1 {
		return d, 0, errShort
	}
	d.DType = uint16(dt)
	if pos >= len(b) {
		return d, 0, errShort
	}
	d.Ordered = b[pos] == 1
	if b[pos] > 1 {
		return d, 0, errors.New("semcas: bad ordered flag")
	}
	pos++
	nd, err := u()
	if err != nil || nd > 32 {
		return d, 0, errors.New("semcas: bad rank")
	}
	for i := uint64(0); i < nd; i++ {
		s, err := u()
		if err != nil {
			return d, 0, err
		}
		d.Shape = append(d.Shape, s)
	}
	el, err := u()
	if err != nil || el > 1<<20 || uint64(len(b)-pos) < el {
		return d, 0, errors.New("semcas: bad extra")
	}
	d.Extra = append([]byte(nil), b[pos:pos+int(el)]...)
	pos += int(el)
	if d.Version != 1 || d.Kind == 0 || d.Kind > KindLengths {
		return d, 0, errors.New("semcas: unsupported descriptor kind or version")
	}
	return d, pos, nil
}

var errShort = errors.New("semcas: truncated")

// ID is a 32-byte SHA-256 (payload digest or component identity).
type ID [32]byte

func (i ID) String() string { return fmt.Sprintf("%x", i[:]) }

// Short is a 12-hex prefix for reports.
func (i ID) Short() string { return fmt.Sprintf("%x", i[:6]) }

// PayloadSHA is the byte identity (level 1).
func PayloadSHA(p []byte) ID { return sha256.Sum256(p) }

// CID is the canonical semantic identity (level 2): descriptor, payload length and
// payload digest under a domain separator. Equal CIDs imply equal typed content;
// equal payload digests with different descriptors are byte-identical but NOT
// semantically identical and must not be treated as the same component.
func CID(d *Desc, payloadLen int64, payload ID) ID {
	h := sha256.New()
	h.Write([]byte("zs3.semcas.cid.v1\x00"))
	db := d.Marshal()
	h.Write(binary.AppendUvarint(nil, uint64(len(db))))
	h.Write(db)
	var l [8]byte
	binary.LittleEndian.PutUint64(l[:], uint64(payloadLen))
	h.Write(l[:])
	h.Write(payload[:])
	var out ID
	h.Sum(out[:0])
	return out
}

// PayloadLen is the canonical payload byte length implied by the descriptor, or -1
// when the kind does not determine it (bytes/file/column variable data).
func (d *Desc) PayloadLen() int64 {
	if d.Kind != KindTensor {
		return -1
	}
	n, err := d.Elems()
	if err != nil {
		return -1
	}
	if w := ElemBytes(int(d.DType)); w > 0 {
		hi, lo := bits.Mul64(n, uint64(w))
		if hi != 0 || lo > 1<<62 {
			return -1
		}
		return int64(lo)
	}
	if g, ok := blockGeom(int(d.DType)); ok && n%uint64(g[0]) == 0 {
		return int64(n / uint64(g[0]) * uint64(g[1]))
	}
	return -1
}

// blockGeom returns {elements, bytes} per block for supported GGML block types.
func blockGeom(d int) ([2]int, bool) {
	switch d - DQBase {
	case 2: // Q4_0
		return [2]int{32, 18}, true
	case 8: // Q8_0
		return [2]int{32, 34}, true
	}
	return [2]int{}, false
}
