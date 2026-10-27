package kinds

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"zeros3-z3/repz3"
)

// KindModel (Z3-29 laboratory kind 8) is a model artifact - a Safetensors, GGUF or torch ZIP file -
// as an exact list of operations over ordinary content-addressed members, with no format
// knowledge in the decoder: the encoder's parser found the framing and tensor spans, the decoder
// only concatenates and applies residuals. The recipe is
//
//	version (1) | uvarint n ops | ops | uvarint n names | names
//	COPY      0x00 uvarint m                              emit member m (a component, or a child's output)
//	XORPLANES 0x01 uvarint base, uvarint res, uvarint w, uvarint e
//	                                                      emit base XOR unplane(res); |base| == |res|
//	PLANES    0x02 uvarint res, uvarint w, uvarint e      emit unplane(res)
//	name      uvarint op index | uvarint len | bytes      a tensor-name hint for point reads
//
// A residual component is a sequence of chunks of e elements of w bytes; inside a chunk the bytes
// are separated by position within the element (all byte 0s, then all byte 1s, ...), the layout
// that DEFLATE under the ordinary pack rule compresses (Z3-10 T0) and that a sequential decoder
// can verify with one SHA-256 pass. For GGUF an "element" is one quantisation block of w bytes. The
// base member is exact bytes named by their SHA-256: a STORED component, or a DERIVED member whose
// child is another KindModel representation of those bytes (a bounded chain through the
// contract's depth limit). Nothing here names a bucket, key or version.
const (
	modelVer           = 1
	opCopy        byte = 0
	opXORPlanes   byte = 1
	opPlanes      byte = 2
	maxOps             = 1 << 21
	maxModelW          = 4096
	maxChunkBytes      = 16 << 20 // one residual chunk (E elements of W bytes): the only decode-side allocation unit
)

type modelOp struct {
	Tag          byte
	M, Base, Res int
	W, E         int
	Size         int64
}

type modelRecipe struct {
	Ops   []modelOp
	Names []modelName
}

type modelName struct {
	Op   int
	Name string
}

func uv(b []byte) (uint64, []byte, bool) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, b, false
	}
	return v, b[n:], true
}

func parseModelRecipe(rep *repz3.Rep) (*modelRecipe, error) {
	b := rep.Recipe
	bad := func(f string, a ...any) (*modelRecipe, error) {
		return nil, fmt.Errorf("%w: model recipe: %s", repz3.ErrCorrupt, fmt.Sprintf(f, a...))
	}
	if len(b) < 1 || b[0] != modelVer {
		return bad("version")
	}
	b = b[1:]
	n, b, ok := uv(b)
	if !ok || n > maxOps || n > uint64(len(b)) {
		return bad("op count")
	}
	r := &modelRecipe{Ops: make([]modelOp, 0, n)}
	nm := len(rep.Members)
	var total int64
	size := func(m int) int64 { return rep.Members[m].Size }
	for i := uint64(0); i < n; i++ {
		if len(b) < 1 {
			return bad("truncated op %d", i)
		}
		op := modelOp{Tag: b[0]}
		b = b[1:]
		get := func(max int) (int, bool) {
			var v uint64
			v, b, ok = uv(b)
			if !ok || v >= uint64(max) {
				return 0, false
			}
			return int(v), true
		}
		switch op.Tag {
		case opCopy:
			if op.M, ok = get(nm); !ok {
				return bad("op %d member", i)
			}
			op.Size = size(op.M)
		case opXORPlanes, opPlanes:
			if op.Tag == opXORPlanes {
				if op.Base, ok = get(nm); !ok {
					return bad("op %d base", i)
				}
			}
			if op.Res, ok = get(nm); !ok {
				return bad("op %d residual", i)
			}
			if op.W, ok = get(maxModelW + 1); !ok || op.W < 1 {
				return bad("op %d width", i)
			}
			if op.E, ok = get(1 << 28); !ok || op.E < 1 || int64(op.E)*int64(op.W) > maxChunkBytes {
				return bad("op %d chunk", i)
			}
			op.Size = size(op.Res)
			if op.Size%int64(op.W) != 0 {
				return bad("op %d residual %d bytes is not a multiple of %d", i, op.Size, op.W)
			}
			if op.Tag == opXORPlanes && size(op.Base) != op.Size {
				return bad("op %d base is %d bytes, residual %d", i, size(op.Base), op.Size)
			}
			if rep.Members[op.Res].Role != repz3.Stored {
				return bad("op %d residual is not a component", i)
			}
		default:
			return bad("op %d tag %d", i, op.Tag)
		}
		if total > 1<<62-op.Size {
			return bad("size overflow")
		}
		total += op.Size
		r.Ops = append(r.Ops, op)
	}
	if total != rep.Size {
		return bad("ops produce %d bytes, representation declares %d", total, rep.Size)
	}
	nn, b, ok := uv(b)
	if !ok || nn > n {
		return bad("name count")
	}
	for i := uint64(0); i < nn; i++ {
		var o, l uint64
		if o, b, ok = uv(b); !ok || o >= n {
			return bad("name %d op", i)
		}
		if l, b, ok = uv(b); !ok || l > 4096 || l > uint64(len(b)) {
			return bad("name %d length", i)
		}
		r.Names = append(r.Names, modelName{int(o), string(b[:l])})
		b = b[l:]
	}
	if len(b) != 0 {
		return bad("%d trailing bytes", len(b))
	}
	return r, nil
}

type modelDecoder struct{}

// Expand: every component is a listed member, so there is nothing named only inside another object.
func (modelDecoder) Expand(c *repz3.Call, emit func(repz3.SHA)) error {
	_, err := parseModelRecipe(c.Rep)
	return err
}

func (modelDecoder) Decode(c *repz3.Call) error {
	r, err := parseModelRecipe(c.Rep)
	if err != nil {
		return err
	}
	var bufs [3][]byte // residual, base and output chunks, reused by every op
	for i := range r.Ops {
		op := &r.Ops[i]
		if op.Tag == opCopy {
			if err := c.WriteMember(op.M, c.W); err != nil {
				return fmt.Errorf("op %d: %w", i, err)
			}
			continue
		}
		if err := applyResidual(c, op, c.W, &bufs); err != nil {
			return fmt.Errorf("op %d: %w", i, err)
		}
	}
	return nil
}

// memberStream reads member m sequentially; on EOF the length and SHA-256 are enforced.
func memberStream(c *repz3.Call, m int) io.ReadCloser {
	mem := &c.Rep.Members[m]
	if mem.Role == repz3.Stored {
		return &storedReader{src: c.Source(), id: mem.SHA, size: mem.Size, h: sha256.New()}
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(c.WriteMember(m, pw)) }()
	return pr
}

type storedReader struct {
	src  repz3.Source
	id   repz3.SHA
	size int64
	off  int64
	h    interface {
		io.Writer
		Sum([]byte) []byte
	}
	checked bool
}

func (s *storedReader) Close() error { return nil }

func (s *storedReader) Read(p []byte) (int, error) {
	if !s.checked {
		got, err := s.src.Size(s.id)
		if err != nil {
			return 0, err
		}
		if got != s.size {
			return 0, fmt.Errorf("%w: %x is %d bytes, recipe says %d", repz3.ErrMismatch, s.id[:6], got, s.size)
		}
		s.checked = true
	}
	if s.off == s.size {
		var sum repz3.SHA
		s.h.Sum(sum[:0])
		if sum != s.id {
			return 0, fmt.Errorf("%w: %x bytes do not hash to their address", repz3.ErrMismatch, s.id[:6])
		}
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), s.size-s.off))
	got, err := s.src.ReadAt(s.id, p[:n], s.off)
	if got != n {
		if err == nil || err == io.EOF {
			err = repz3.ErrShort
		}
		return 0, err
	}
	s.h.Write(p[:n])
	s.off += int64(n)
	return n, nil
}

func applyResidual(c *repz3.Call, op *modelOp, w io.Writer, bufs *[3][]byte) error {
	res := memberStream(c, op.Res)
	defer res.Close()
	var base io.ReadCloser
	if op.Tag == opXORPlanes {
		base = memberStream(c, op.Base)
		defer base.Close()
	}
	chunk := int64(op.E) * int64(op.W)
	n0 := int(min(chunk, op.Size))
	for i := range bufs {
		if cap(bufs[i]) < n0 {
			bufs[i] = make([]byte, n0)
		}
	}
	rbuf, bbuf, out := bufs[0][:n0], bufs[1][:n0], bufs[2][:n0]
	for done := int64(0); done < op.Size; {
		n := min(chunk, op.Size-done)
		if _, err := io.ReadFull(res, rbuf[:n]); err != nil {
			return err
		}
		unplane(out[:n], rbuf[:n], op.W)
		if base != nil {
			if _, err := io.ReadFull(base, bbuf[:n]); err != nil {
				return err
			}
			for i := int64(0); i < n; i++ {
				out[i] ^= bbuf[i]
			}
		}
		if _, err := w.Write(out[:n]); err != nil {
			return err
		}
		done += n
	}
	// both streams must now be exactly at their verified end
	var one [1]byte
	if _, err := res.Read(one[:]); err != io.EOF {
		return fmt.Errorf("%w: residual has trailing data", repz3.ErrMismatch)
	}
	if base != nil {
		if _, err := base.Read(one[:]); err != io.EOF {
			if err == nil {
				err = fmt.Errorf("%w: base has trailing data", repz3.ErrMismatch)
			}
			return err
		}
	}
	return nil
}

// unplane writes the interleaved form of one chunk (len(src) a multiple of w): src holds the
// w planes of e = len/w elements back to back.
func unplane(dst, src []byte, w int) {
	e := len(src) / w
	if w == 1 {
		copy(dst, src)
		return
	}
	for p := 0; p < w; p++ {
		col := src[p*e : (p+1)*e]
		for i, v := range col {
			dst[i*w+p] = v
		}
	}
}

// PlaneChunks is the encoder side of the residual layout: it separates b (a multiple of w long)
// chunk by chunk, e elements per chunk.
func PlaneChunks(b []byte, w, e int) []byte {
	out := make([]byte, len(b))
	if w == 1 {
		copy(out, b)
		return out
	}
	step := w * e
	for off := 0; off < len(b); off += step {
		n := min(step, len(b)-off)
		ne := n / w
		src := b[off : off+n]
		dst := out[off : off+n]
		for p := 0; p < w; p++ {
			col := dst[p*ne : (p+1)*ne]
			for i := range col {
				col[i] = src[i*w+p]
			}
		}
	}
	return out
}

// ModelOp is one operation in a recipe under construction.
type ModelOp struct {
	Tag          byte
	M, Base, Res int
	W, E         int
	Name         string
}

// BuildModelRecipe serialises ops (member indexes refer to the representation's Members).
func BuildModelRecipe(ops []ModelOp) []byte {
	b := []byte{modelVer}
	b = binary.AppendUvarint(b, uint64(len(ops)))
	for _, o := range ops {
		b = append(b, o.Tag)
		switch o.Tag {
		case opCopy:
			b = binary.AppendUvarint(b, uint64(o.M))
		case opXORPlanes:
			b = binary.AppendUvarint(b, uint64(o.Base))
			fallthrough
		case opPlanes:
			b = binary.AppendUvarint(b, uint64(o.Res))
			b = binary.AppendUvarint(b, uint64(o.W))
			b = binary.AppendUvarint(b, uint64(o.E))
		}
	}
	nn := 0
	for _, o := range ops {
		if o.Name != "" {
			nn++
		}
	}
	b = binary.AppendUvarint(b, uint64(nn))
	for i, o := range ops {
		if o.Name != "" {
			b = binary.AppendUvarint(b, uint64(i))
			b = binary.AppendUvarint(b, uint64(len(o.Name)))
			b = append(b, o.Name...)
		}
	}
	return b
}

// Op tags for encoders.
const (
	ModelCopy      = opCopy
	ModelXORPlanes = opXORPlanes
	ModelPlanes    = opPlanes
)

var errRange = errors.New("kinds: range outside the representation")

// ModelNames lists the tensor names the recipe carries, with the byte offset and length each op
// produces (the point-read index).
func ModelNames(c *repz3.Call) (map[string][2]int64, error) {
	r, err := parseModelRecipe(c.Rep)
	if err != nil {
		return nil, err
	}
	offs := make([]int64, len(r.Ops)+1)
	for i, op := range r.Ops {
		offs[i+1] = offs[i] + op.Size
	}
	out := map[string][2]int64{}
	for _, n := range r.Names {
		out[n.Name] = [2]int64{offs[n.Op], r.Ops[n.Op].Size}
	}
	return out, nil
}

// ReadModelRange returns bytes [off, off+n) of an opened KindModel representation touching only the
// ops (and chunks, and base-chain members) that overlap it. Component bytes are length-checked
// but not hash-verified on this path (a hash needs a sequential pass); the whole-file path
// verifies everything.
func ReadModelRange(c *repz3.Call, off, n int64) ([]byte, error) {
	if c.Rep.Kind != repz3.KindModel {
		return nil, errors.New("kinds: not a model representation")
	}
	if off < 0 || n < 0 || off > c.Rep.Size || n > c.Rep.Size-off {
		return nil, errRange
	}
	r, err := parseModelRecipe(c.Rep)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, n)
	pos := int64(0)
	for i := range r.Ops {
		op := &r.Ops[i]
		lo, hi := max(off, pos), min(off+n, pos+op.Size)
		if lo < hi {
			part, err := readOp(c, op, lo-pos, hi-lo)
			if err != nil {
				return nil, err
			}
			out = append(out, part...)
		}
		pos += op.Size
	}
	return out, nil
}

func readMemberRange(c *repz3.Call, m int, off, n int64) ([]byte, error) {
	mem := &c.Rep.Members[m]
	if mem.Role == repz3.Stored {
		buf := make([]byte, n)
		got, err := c.Source().ReadAt(mem.SHA, buf, off)
		if int64(got) != n {
			if err == nil || err == io.EOF {
				err = repz3.ErrShort
			}
			return nil, err
		}
		return buf, nil
	}
	ch, err := c.Child(m)
	if err != nil {
		return nil, err
	}
	return ReadModelRange(ch, off, n)
}

func readOp(c *repz3.Call, op *modelOp, off, n int64) ([]byte, error) {
	if op.Tag == opCopy {
		return readMemberRange(c, op.M, off, n)
	}
	w, e := int64(op.W), int64(op.E)
	chunk := w * e
	out := make([]byte, 0, n)
	for cs := off - off%chunk; cs < off+n; cs += chunk {
		cn := min(chunk, op.Size-cs)
		ne := cn / w
		lo, hi := max(off, cs), min(off+n, cs+cn)
		// elements [ea, eb) of this chunk cover the request; each plane is read for those only
		ea, eb := (lo-cs)/w, (hi-cs+w-1)/w
		plain := make([]byte, (eb-ea)*w)
		for p := int64(0); p < w; p++ {
			pl, err := readMemberRange(c, op.Res, cs+p*ne+ea, eb-ea)
			if err != nil {
				return nil, err
			}
			for i, v := range pl {
				plain[int64(i)*w+p] = v
			}
		}
		if op.Tag == opXORPlanes {
			bb, err := readMemberRange(c, op.Base, cs+ea*w, (eb-ea)*w)
			if err != nil {
				return nil, err
			}
			for i := range plain {
				plain[i] ^= bb[i]
			}
		}
		out = append(out, plain[lo-cs-ea*w:hi-cs-ea*w]...)
	}
	return out, nil
}

var _ = bytes.Equal
