package sstz3

import (
	"bytes"
	"encoding/binary"
	"io"
	"sort"

	"zeros3-z3/snappyz3"
)

// Getter fetches a component by digest. The caller is responsible for checking the
// bytes against the digest and the declared length before returning them.
type Getter func(id ID) ([]byte, error)

// RangeStats reports what a ranged decode touched.
type RangeStats struct {
	Blocks     int // data blocks rebuilt
	Components int // component fetches
	Bytes      int64
	Records    int64 // records parsed (including those skipped to reach the first block)
}

type cursor struct {
	rec      *Recipe
	get      Getter
	lim      Limits
	S        int
	g        int    // current group
	left     uint32 // records left in the group
	comp     [3][]byte
	rix      int64 // global record index
	st       *RangeStats
	started  bool
	nextAuto bool
}

func (c *cursor) load(g int) error {
	if g >= len(c.rec.Groups) {
		return recipeErr("records beyond the last group")
	}
	for s := 0; s < c.S; s++ {
		b, err := c.get(c.rec.Digests[g*c.S+s])
		if err != nil {
			return err
		}
		if int64(len(b)) > c.lim.MaxDecoded {
			return ErrLimit
		}
		c.comp[s] = b
		c.st.Components++
		c.st.Bytes += int64(len(b))
	}
	c.g, c.left, c.started = g, c.rec.Groups[g], true
	return nil
}

func takeLen(b *[]byte, max int64) ([]byte, error) {
	n, k := binary.Uvarint(*b)
	if k <= 0 || n > uint64(max) || n > uint64(len(*b)-k) {
		return nil, corrupt("record length in a component")
	}
	v := (*b)[k : k+int(n)]
	*b = (*b)[k+int(n):]
	return v, nil
}

// next returns the next record's user key, trailer and value.
func (c *cursor) next() (ukey []byte, trailer uint64, val []byte, err error) {
	if !c.started || c.left == 0 {
		if c.started {
			for s := 0; s < c.S; s++ {
				if len(c.comp[s]) != 0 {
					return nil, 0, nil, corrupt("group %d has trailing bytes", c.g)
				}
			}
		}
		ng := 0
		if c.started {
			ng = c.g + 1
		}
		if err = c.load(ng); err != nil {
			return
		}
	}
	m := c.lim.MaxRecordBytes
	f := c.rec.Form
	switch f {
	case FormUKV, FormFull:
		if ukey, err = takeLen(&c.comp[0], m); err != nil {
			return
		}
		if f == FormFull {
			if len(c.comp[0]) < 8 {
				return nil, 0, nil, corrupt("truncated trailer")
			}
			trailer = binary.LittleEndian.Uint64(c.comp[0])
			c.comp[0] = c.comp[0][8:]
		}
		val, err = takeLen(&c.comp[0], m)
	case FormKV, FormKTV, FormKVT:
		if ukey, err = takeLen(&c.comp[0], m); err != nil {
			return
		}
		switch f {
		case FormKTV:
			if len(c.comp[0]) < 8 {
				return nil, 0, nil, corrupt("truncated trailer")
			}
			trailer = binary.LittleEndian.Uint64(c.comp[0])
			c.comp[0] = c.comp[0][8:]
		case FormKVT:
			if len(c.comp[2]) < 8 {
				return nil, 0, nil, corrupt("truncated trailer")
			}
			trailer = binary.LittleEndian.Uint64(c.comp[2])
			c.comp[2] = c.comp[2][8:]
		}
		val, err = takeLen(&c.comp[1], m)
	}
	if err != nil {
		return
	}
	if c.rec.Form.trailersInRecipe() {
		if c.rix >= int64(len(c.rec.Trailers)) {
			return nil, 0, nil, recipeErr("trailer index")
		}
		trailer = c.rec.Trailers[c.rix]
	}
	c.rix++
	c.left--
	c.st.Records++
	return
}

// Decode rebuilds the original table from the serialized recipe (head and digests) and
// the components, writing it to w. It reads components lazily in file order and holds
// one group (a few components) and one data block.
func Decode(w io.Writer, recipe []byte, get Getter, lim Limits) error {
	_, err := DecodeRange(w, recipe, get, 0, -1, lim)
	return err
}

// DecodeRange writes bytes [off, off+n) of the original table (n < 0: to the end). Only
// the data blocks overlapping the range are rebuilt, and only the groups holding their
// records are fetched; the tail component is fetched only if the range reaches it.
func DecodeRange(w io.Writer, recipe []byte, get Getter, off, n int64, lim Limits) (RangeStats, error) {
	var st RangeStats
	lim = lim.norm()
	rec, err := ParseRecipe(recipe, lim)
	if err != nil {
		return st, err
	}
	if off < 0 || off > rec.Size {
		return st, ErrLimit
	}
	if n < 0 || n > rec.Size-off {
		n = rec.Size - off
	}
	if n == 0 {
		return st, nil
	}
	end := off + n
	dataEnd := rec.Size - rec.TailLen
	// physical offsets of the blocks
	starts := make([]int64, len(rec.Blocks)+1)
	for i := range rec.Blocks {
		starts[i+1] = starts[i] + int64(rec.Blocks[i].CSize) + trailerLen
	}
	first := sort.Search(len(rec.Blocks), func(i int) bool { return starts[i+1] > off })
	cur := &cursor{rec: rec, get: get, lim: lim, S: rec.Form.streams(), st: &st}
	var bw BlockWriter
	var key []byte
	var payload bytes.Buffer
	if rec.Form != FormBlocks && first < len(rec.Blocks) && off < dataEnd && n > 0 {
		var r0 int64
		for i := 0; i < first; i++ {
			r0 += int64(rec.Blocks[i].Entries)
		}
		// locate the group of record r0 and skip to it
		var acc int64
		g := 0
		for g < len(rec.Groups) && acc+int64(rec.Groups[g]) <= r0 {
			acc += int64(rec.Groups[g])
			g++
		}
		if g < len(rec.Groups) {
			if err := cur.load(g); err != nil {
				return st, err
			}
			cur.rix = acc
			for ; acc < r0; acc++ {
				if _, _, _, err := cur.next(); err != nil {
					return st, err
				}
			}
		}
	}
	var written int64
	pos := starts[first]
	lastBlock := -1
	for bi := first; bi < len(rec.Blocks) && pos < end && pos < dataEnd; bi++ {
		bl := &rec.Blocks[bi]
		var raw []byte
		if rec.Form == FormBlocks {
			b, err := get(rec.Digests[bi])
			if err != nil {
				return st, err
			}
			st.Components++
			st.Bytes += int64(len(b))
			if uint64(len(b)) != bl.Decoded {
				return st, corrupt("block %d component has %d bytes, recipe says %d", bi, len(b), bl.Decoded)
			}
			raw = b
		} else {
			bw.Reset()
			r := int(rec.RestartInterval)
			ri := 0
			for i := 0; i < int(bl.Entries); i++ {
				uk, tr, val, err := cur.next()
				if err != nil {
					return st, err
				}
				key = append(append(key[:0], uk...), 0, 0, 0, 0, 0, 0, 0, 0)
				binary.LittleEndian.PutUint64(key[len(uk):], tr)
				restart, shared := false, -1
				if ex := bl.Explicit; ex != nil {
					if ri < len(ex.Restarts) && int(ex.Restarts[ri]) == i {
						restart = true
						ri++
					}
					shared = int(ex.Shared[i])
				} else {
					if r == 0 {
						return st, recipeErr("restart interval 0")
					}
					restart = i%r == 0
				}
				if err := bw.Add(key, val, restart, shared); err != nil {
					return st, err
				}
			}
			raw = bw.Finish()
		}
		st.Blocks++
		payload.Reset()
		switch bl.Trailer[0] {
		case CompNone:
			payload.Write(raw)
		case CompSnappy:
			switch {
			case bl.Pred == PredGo && len(raw) <= snappyz3.MaxPredict:
				payload.Write(snappyz3.EncodeRef(raw))
			case bl.Pred == PredCPP && len(raw) <= snappyz3.MaxPredict:
				payload.Write(snappyz3.EncodeCPP(raw))
			case bl.Pred == PredCPP15 && len(raw) <= snappyz3.MaxPredict:
				payload.Write(snappyz3.EncodeCPP15(raw))
			case bl.Pred != 0:
				return st, recipeErr("block %d predicted at %d bytes", bi, len(raw))
			default:
				cmds, err := snappyCmds(bl, int64(len(raw)), lim)
				if err != nil {
					return st, err
				}
				if err := snappyz3.Replay(&payload, raw, cmds); err != nil {
					return st, err
				}
			}
		default:
			return st, recipeErr("compression type %d", bl.Trailer[0])
		}
		if uint64(payload.Len()) != bl.CSize {
			return st, corrupt("block %d rebuilt to %d bytes, recipe says %d", bi, payload.Len(), bl.CSize)
		}
		payload.Write(bl.Trailer[:])
		b := payload.Bytes()
		lo, hi := max(off, pos)-pos, min(end, pos+int64(len(b)))-pos
		if hi > lo {
			if _, err := w.Write(b[lo:hi]); err != nil {
				return st, err
			}
			written += hi - lo
		}
		pos += int64(len(b))
		lastBlock = bi
	}
	if rec.Form != FormBlocks && lastBlock == len(rec.Blocks)-1 && len(rec.Blocks) > 0 && cur.started {
		// the whole data region was rebuilt: every record of every group must be consumed
		if cur.left != 0 || cur.g != len(rec.Groups)-1 {
			return st, corrupt("%d records left over", cur.left)
		}
		for s := 0; s < cur.S; s++ {
			if len(cur.comp[s]) != 0 {
				return st, corrupt("last group has trailing bytes")
			}
		}
	}
	if end > dataEnd && n > 0 {
		t, err := get(rec.Digests[len(rec.Digests)-1])
		if err != nil {
			return st, err
		}
		st.Components++
		st.Bytes += int64(len(t))
		if int64(len(t)) != rec.TailLen {
			return st, corrupt("tail component has %d bytes, recipe says %d", len(t), rec.TailLen)
		}
		lo := max(off, dataEnd) - dataEnd
		if _, err := w.Write(t[lo : end-dataEnd]); err != nil {
			return st, err
		}
		written += end - dataEnd - lo
	}
	if written != n {
		return st, corrupt("wrote %d of %d bytes", written, n)
	}
	return st, nil
}
