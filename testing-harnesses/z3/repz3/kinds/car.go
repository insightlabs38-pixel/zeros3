package kinds

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"zeros3-z3/carz3"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
)

// CAR as a block set plus an order (the Z3-14 set layout): the sorted digest table
// of the distinct blocks is cut with production CDC into STORED members, the
// blocks hang off that table (VIA: the table names them, the representation does
// not), and the opaque recipe is the carz3 head (header, CARv2 wrapper, CID
// prefixes) and tail (per-section order) as
//
//	uvarint(len head) | head | tail
//
// CIDs and the CARv2 index are derived from the digests when carz3 verified that
// they are; the native index section is therefore omitted (CID -> block is a
// search of the table by the digest inside the CID).
const maxTable = 256 << 20

// EncodeCAR analyses a CARv1/v2 archive.
func EncodeCAR(src io.ReaderAt, size int64) (*Bundle, *carz3.Stats, error) {
	rep, st, err := carz3.Build(src, size, carz3.Options{Limits: carz3.DefaultLimits()})
	if err != nil {
		return nil, nil, err
	}
	head, table, tail, _ := rep.MarshalParts(true)
	out := &repz3.Rep{Kind: repz3.KindCAR, Size: size}
	var objs [][]byte
	err = lab.ChunkCDC(bytes.NewReader(table), func(_ int64, c []byte) error {
		cp := append([]byte(nil), c...)
		out.Members = append(out.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(cp), Size: int64(len(cp))})
		objs = append(objs, cp)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	seen := map[[32]byte]bool{}
	for i := range rep.Refs {
		r := &rep.Refs[i]
		if r.Inline != nil || seen[r.Sum] {
			continue
		}
		seen[r.Sum] = true
		b := make([]byte, r.Len)
		if _, err := src.ReadAt(b, r.SrcOff); err != nil && !(err == io.EOF && int64(len(b)) > 0) {
			return nil, nil, err
		}
		objs = append(objs, b)
	}
	out.Recipe = binary.AppendUvarint(nil, uint64(len(head)))
	out.Recipe = append(append(out.Recipe, head...), tail...)
	b, err := newBundle(out, objs)
	return b, st, err
}

type carDecoder struct{}

func carRep(c *repz3.Call) (*carz3.Rep, error) {
	hl, n := binary.Uvarint(c.Rep.Recipe)
	if n <= 0 || hl > uint64(len(c.Rep.Recipe)-n) {
		return nil, fmt.Errorf("%w: CAR recipe head", repz3.ErrCorrupt)
	}
	head, tail := c.Rep.Recipe[n:n+int(hl)], c.Rep.Recipe[n+int(hl):]
	buf := append([]byte(nil), head...)
	for i := range c.Rep.Members {
		b, err := c.Member(i)
		if err != nil {
			return nil, err
		}
		if len(buf)+len(b)+len(tail) > maxTable {
			return nil, fmt.Errorf("%w: CAR block table over %d bytes", repz3.ErrLimit, maxTable)
		}
		buf = append(buf, b...)
	}
	return carz3.Unmarshal(append(buf, tail...))
}

func (carDecoder) Decode(c *repz3.Call) error {
	rep, err := carRep(c)
	if err != nil {
		return err
	}
	return rep.Reconstruct(c.W, func(s [32]byte) ([]byte, error) { return c.Get(s) })
}

func (carDecoder) Expand(c *repz3.Call, emit func(repz3.SHA)) error {
	rep, err := carRep(c)
	if err != nil {
		return err
	}
	for i := range rep.Refs {
		if rep.Refs[i].Inline == nil {
			emit(rep.Refs[i].Sum)
		}
	}
	return nil
}
