// Package sqfsz3 is the Z4-B6 SquashFS 4.0 exact decoded-block representation for LZ4 images
// (laboratory; stdlib + lz4z3). Data and fragment blocks are decoded; everything else (header,
// compressor options, inode/directory/fragment/id tables, padding) is kept verbatim. The image is
//
//	verbatim regions + compressed blocks,  block = size word + lz4z3 recipe,
//
// and the decoded blocks, concatenated in image order, are the payload that content-defined
// chunking can share across image revisions. Decode needs no lz4 library or mksquashfs.
package sqfsz3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"

	"zeros3-z3/lz4z3"
)

var (
	ErrCorrupt     = errors.New("sqfsz3: corrupt image")
	ErrUnsupported = errors.New("sqfsz3: unsupported image")
	ErrRecipe      = errors.New("sqfsz3: bad recipe")
	ErrLimit       = errors.New("sqfsz3: limit exceeded")
)

// Limits bound every parse and replay.
type Limits struct {
	MaxImage   int64 // default 1 GiB
	MaxBlocks  int   // default 1<<22
	MaxInodes  int   // default 1<<24
	MaxDecoded int64 // total decoded payload, default 4 GiB
}

func (l Limits) norm() Limits {
	if l.MaxImage <= 0 {
		l.MaxImage = 1 << 30
	}
	if l.MaxBlocks <= 0 {
		l.MaxBlocks = 1 << 22
	}
	if l.MaxInodes <= 0 {
		l.MaxInodes = 1 << 24
	}
	if l.MaxDecoded <= 0 {
		l.MaxDecoded = 4 << 30
	}
	return l
}

func bad(f string, a ...any) error { return fmt.Errorf("%w: "+f, append([]any{ErrCorrupt}, a...)...) }

type super struct {
	inodeCount, blockSize, fragCount uint32
	comp, flags, idCount             uint16
	blockLog                         uint16
	bytesUsed                        uint64
	idTable, xattrTable, inodeTable  uint64
	dirTable, fragTable, lookupTable uint64
}

const (
	superSize = 96
	compLZ4   = 5
	flagOpts  = 0x400
	uncomp24  = 1 << 24
)

func parseSuper(img []byte) (s super, err error) {
	if len(img) < superSize || binary.LittleEndian.Uint32(img) != 0x73717368 {
		return s, bad("not a SquashFS image")
	}
	le := binary.LittleEndian
	s.inodeCount, s.blockSize, s.fragCount = le.Uint32(img[4:]), le.Uint32(img[12:]), le.Uint32(img[16:])
	s.comp, s.blockLog, s.flags, s.idCount = le.Uint16(img[20:]), le.Uint16(img[22:]), le.Uint16(img[24:]), le.Uint16(img[26:])
	if le.Uint16(img[28:]) != 4 {
		return s, ErrUnsupported
	}
	s.bytesUsed = le.Uint64(img[40:])
	s.idTable, s.xattrTable, s.inodeTable = le.Uint64(img[48:]), le.Uint64(img[56:]), le.Uint64(img[64:])
	s.dirTable, s.fragTable, s.lookupTable = le.Uint64(img[72:]), le.Uint64(img[80:]), le.Uint64(img[88:])
	if s.comp != compLZ4 {
		return s, fmt.Errorf("%w: compressor %d", ErrUnsupported, s.comp)
	}
	if s.blockSize < 4096 || s.blockSize > 1<<20 || s.blockSize != 1<<s.blockLog {
		return s, bad("block size %d log %d", s.blockSize, s.blockLog)
	}
	return s, nil
}

// metaBlocks reads a run of metadata blocks starting at off and returns the concatenated
// decoded bytes and the end offset; stop when off reaches limit.
func metaBlocks(img []byte, off, limit uint64, lim lz4z3.Limits) ([]byte, uint64, error) {
	var out []byte
	for off < limit {
		if off+2 > uint64(len(img)) {
			return nil, 0, bad("metadata header past image")
		}
		h := binary.LittleEndian.Uint16(img[off:])
		n := uint64(h & 0x7FFF)
		off += 2
		if n == 0 || n > 8192 || off+n > uint64(len(img)) {
			return nil, 0, bad("metadata block of %d bytes", n)
		}
		if h&0x8000 != 0 {
			out = append(out, img[off:off+n]...)
		} else {
			d, _, err := lz4z3.Decode(img[off:off+n], -1, lim)
			if err != nil {
				return nil, 0, bad("metadata lz4: %v", err)
			}
			out = append(out, d...)
		}
		off += n
	}
	return out, off, nil
}

type block struct {
	off  uint64
	word uint32
}

func (b block) size() uint64 { return uint64(b.word &^ uncomp24) }

// scanBlocks returns the sorted unique data and fragment blocks referenced by the image.
func scanBlocks(img []byte, s super, lim Limits) ([]block, error) {
	ll := lz4z3.Limits{MaxDecoded: 1 << 20}
	if s.inodeTable >= s.dirTable || s.dirTable > uint64(len(img)) {
		return nil, bad("table order")
	}
	itab, _, err := metaBlocks(img, s.inodeTable, s.dirTable, ll)
	if err != nil {
		return nil, err
	}
	seen := map[uint64]block{}
	add := func(off uint64, word uint32) error {
		sz := uint64(word &^ uncomp24)
		if sz == 0 {
			return nil
		}
		if off+sz > s.inodeTable {
			return bad("data block %d+%d beyond the data area %d", off, sz, s.inodeTable)
		}
		if p, ok := seen[off]; ok && p.word != word {
			return bad("two size words at %d", off)
		}
		seen[off] = block{off, word}
		return nil
	}
	le := binary.LittleEndian
	pos, n := 0, 0
	for pos < len(itab) {
		if n++; n > lim.MaxInodes {
			return nil, ErrLimit
		}
		if pos+16 > len(itab) {
			return nil, bad("inode header")
		}
		typ := le.Uint16(itab[pos:])
		b := itab[pos+16:]
		need := func(k int) error {
			if len(b) < k {
				return bad("inode type %d truncated", typ)
			}
			return nil
		}
		var body int
		switch typ {
		case 1:
			body = 16
		case 2, 9:
			hdr := 16
			if typ == 9 {
				hdr = 40
			}
			if err := need(hdr); err != nil {
				return nil, err
			}
			var start, fsz uint64
			var frag uint32
			if typ == 2 {
				start, frag, fsz = uint64(le.Uint32(b)), le.Uint32(b[4:]), uint64(le.Uint32(b[12:]))
			} else {
				start, fsz, frag = le.Uint64(b), le.Uint64(b[8:]), le.Uint32(b[28:])
			}
			cnt := fsz / uint64(s.blockSize)
			if frag == 0xFFFFFFFF && fsz%uint64(s.blockSize) != 0 {
				cnt++
			}
			if cnt > uint64(len(b)-hdr)/4 {
				return nil, bad("file block list of %d", cnt)
			}
			off := start
			for i := uint64(0); i < cnt; i++ {
				w := le.Uint32(b[hdr+int(4*i):])
				if err := add(off, w); err != nil {
					return nil, err
				}
				off += uint64(w &^ uncomp24)
			}
			body = hdr + int(4*cnt)
		case 3, 10:
			if err := need(8); err != nil {
				return nil, err
			}
			body = 8 + int(le.Uint32(b[4:]))
			if typ == 10 {
				body += 4
			}
		case 4, 5:
			body = 8
		case 11, 12:
			body = 12
		case 6, 7:
			body = 4
		case 13, 14:
			body = 8
		case 8:
			if err := need(24); err != nil {
				return nil, err
			}
			ic := int(le.Uint16(b[16:]))
			body = 24
			for i := 0; i < ic; i++ {
				if err := need(body + 12); err != nil {
					return nil, err
				}
				body += 12 + int(le.Uint32(b[body+8:])) + 1
			}
		default:
			return nil, bad("inode type %d", typ)
		}
		if body < 0 || len(b) < body {
			return nil, bad("inode type %d body %d", typ, body)
		}
		pos += 16 + body
	}
	// fragment blocks
	if s.fragCount > 0 && s.fragTable != 0xFFFFFFFFFFFFFFFF {
		nIdx := (uint64(s.fragCount) + 511) / 512
		if s.fragTable+8*nIdx > uint64(len(img)) {
			return nil, bad("fragment index")
		}
		for i := uint64(0); i < nIdx; i++ {
			mo := le.Uint64(img[s.fragTable+8*i:])
			d, _, err := metaBlocks(img, mo, mo+2, ll)
			if err != nil {
				return nil, err
			}
			for j := 0; j+16 <= len(d) && i*512+uint64(j/16) < uint64(s.fragCount); j += 16 {
				if err := add(le.Uint64(d[j:]), le.Uint32(d[j+8:])); err != nil {
					return nil, err
				}
			}
		}
	}
	out := make([]block, 0, len(seen))
	for _, b := range seen {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].off < out[j].off })
	for i := 1; i < len(out); i++ {
		if out[i].off < out[i-1].off+out[i-1].size() {
			return nil, bad("overlapping blocks at %d", out[i].off)
		}
	}
	if len(out) > lim.MaxBlocks {
		return nil, ErrLimit
	}
	return out, nil
}

// Stats summarizes an encoding.
type Stats struct {
	Blocks, Stored, Predicted, Hybrid, Explicit int
	VerbatimBytes, CompressedBlockBytes         int64
	Decoded                                     int64
	RecipeBytes                                 int
}

// Encode returns the serialized recipe and the concatenated decoded payload of the image.
// Every block recipe is verified; the whole image is rebuilt from the output before it returns.
func Encode(img []byte, lim Limits) (recipe, plain []byte, st Stats, err error) {
	lim = lim.norm()
	if int64(len(img)) > lim.MaxImage {
		return nil, nil, st, ErrLimit
	}
	s, err := parseSuper(img)
	if err != nil {
		return nil, nil, st, err
	}
	blocks, err := scanBlocks(img, s, lim)
	if err != nil {
		return nil, nil, st, err
	}
	ll := lz4z3.Limits{MaxDecoded: int(s.blockSize)}
	var pb bytes.Buffer
	rec := binary.AppendUvarint(nil, uint64(len(img)))
	rec = binary.AppendUvarint(rec, uint64(s.blockSize))
	rec = binary.AppendUvarint(rec, uint64(len(blocks)))
	var lit []byte
	pos := uint64(0)
	for _, b := range blocks {
		gap := img[pos:b.off]
		rec = binary.AppendUvarint(rec, uint64(len(gap)))
		lit = append(lit, gap...)
		raw := img[b.off : b.off+b.size()]
		if b.word&uncomp24 != 0 {
			rec = append(rec, 1)
			rec = binary.AppendUvarint(rec, b.size())
			pb.Write(raw)
			st.Stored++
		} else {
			p, seqs, err := lz4z3.Decode(raw, -1, ll)
			if err != nil {
				return nil, nil, st, bad("block at %d: %v", b.off, err)
			}
			rc := lz4z3.Analyze(p, seqs, true)
			if rep, e := rc.Replay(nil, p, ll); e != nil || !bytes.Equal(rep, raw) {
				return nil, nil, st, bad("recipe does not reproduce block at %d", b.off)
			}
			switch rc.Kind {
			case lz4z3.KindPredicted:
				st.Predicted++
			case lz4z3.KindHybrid:
				st.Hybrid++
			default:
				st.Explicit++
			}
			rec = append(rec, 0)
			rec = binary.AppendUvarint(rec, uint64(len(p)))
			rec = append(rec, rc.Marshal()...)
			pb.Write(p)
			st.CompressedBlockBytes += int64(len(raw))
		}
		st.Blocks++
		pos = b.off + b.size()
		if int64(pb.Len()) > lim.MaxDecoded {
			return nil, nil, st, ErrLimit
		}
	}
	tail := img[pos:]
	rec = binary.AppendUvarint(rec, uint64(len(tail)))
	rec = append(rec, lit...)
	rec = append(rec, tail...)
	st.VerbatimBytes = int64(len(lit) + len(tail))
	st.Decoded = int64(pb.Len())
	st.RecipeBytes = len(rec)
	var out bytes.Buffer
	if err := Decode(&out, rec, pb.Bytes(), lim); err != nil || !bytes.Equal(out.Bytes(), img) {
		return nil, nil, st, fmt.Errorf("%w: self-check failed (%v)", ErrRecipe, err)
	}
	return rec, pb.Bytes(), st, nil
}

func put(w io.Writer, b []byte) error {
	n, err := w.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}

// Decode rebuilds the image from the recipe and the decoded payload.
func Decode(w io.Writer, rec, plain []byte, lim Limits) error {
	lim = lim.norm()
	i := 0
	uv := func(max uint64) (uint64, error) {
		v, n := binary.Uvarint(rec[i:])
		if n <= 0 || v > max {
			return 0, fmt.Errorf("%w: integer at %d", ErrRecipe, i)
		}
		i += n
		return v, nil
	}
	total, err := uv(uint64(lim.MaxImage))
	if err != nil {
		return err
	}
	bs, err := uv(1 << 20)
	if err != nil {
		return err
	}
	nb, err := uv(uint64(lim.MaxBlocks))
	if err != nil {
		return err
	}
	// first pass: gap lengths and literal total, to locate the verbatim section at the end
	type ent struct {
		gap, tag, n uint64
		rc          lz4z3.Recipe
	}
	ents := make([]ent, nb)
	var litTotal uint64
	for k := range ents {
		e := &ents[k]
		if e.gap, err = uv(total); err != nil {
			return err
		}
		litTotal += e.gap
		t := byte(0)
		if i >= len(rec) {
			return fmt.Errorf("%w: truncated", ErrRecipe)
		}
		t = rec[i]
		i++
		if t > 1 {
			return fmt.Errorf("%w: block tag %d", ErrRecipe, t)
		}
		e.tag = uint64(t)
		if e.n, err = uv(bs); err != nil {
			return err
		}
		if t == 0 {
			rc, n, err := lz4z3.Unmarshal(rec[i:], lz4z3.Limits{MaxDecoded: int(bs)})
			if err != nil {
				return fmt.Errorf("%w: %v", ErrRecipe, err)
			}
			i += n
			e.rc = rc
		}
	}
	tl, err := uv(total)
	if err != nil {
		return err
	}
	if litTotal+tl != uint64(len(rec)-i) {
		return fmt.Errorf("%w: verbatim section is %d bytes, header says %d", ErrRecipe, len(rec)-i, litTotal+tl)
	}
	litp := rec[i:]
	pp := uint64(0)
	var written uint64
	for _, e := range ents {
		if err := put(w, litp[:e.gap]); err != nil {
			return err
		}
		litp = litp[e.gap:]
		written += e.gap
		if pp+e.n > uint64(len(plain)) {
			return fmt.Errorf("%w: payload exhausted", ErrRecipe)
		}
		p := plain[pp : pp+e.n]
		pp += e.n
		if e.tag == 1 {
			if err := put(w, p); err != nil {
				return err
			}
			written += e.n
			continue
		}
		blk, err := e.rc.Replay(nil, p, lz4z3.Limits{MaxDecoded: int(bs)})
		if err != nil {
			return fmt.Errorf("%w: %v", ErrRecipe, err)
		}
		if err := put(w, blk); err != nil {
			return err
		}
		written += uint64(len(blk))
	}
	if err := put(w, litp); err != nil {
		return err
	}
	written += uint64(len(litp))
	if written != total || pp != uint64(len(plain)) {
		return fmt.Errorf("%w: wrote %d of %d bytes, used %d of %d payload bytes", ErrRecipe, written, total, pp, len(plain))
	}
	return nil
}
