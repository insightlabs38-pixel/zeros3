package sstz3

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"math"

	"zeros3-z3/snappyz3"
)

// Cutter decides group boundaries from the records alone: Cut is called after every
// record with its user key and value length and returns true to close the group after
// it. A cutter must depend on nothing else (no position in the file, no block
// boundary), so an inserted or removed record moves no other boundary.
type Cutter interface {
	Cut(ukey []byte, vlen int) bool
}

// HashCutter closes a group after a record with probability (record bytes)/Target,
// decided by a hash of the user key, and unconditionally once the group reaches Max.
type HashCutter struct {
	Target, Max int
	cur         int
}

// Cut implements Cutter.
func (c *HashCutter) Cut(ukey []byte, vlen int) bool {
	sz := len(ukey) + vlen + 4
	c.cur += sz
	if c.cur >= c.Max || sz >= c.Target {
		c.cur = 0
		return true
	}
	h := uint64(14695981039346656037)
	for _, b := range ukey {
		h = (h ^ uint64(b)) * 1099511628211
	}
	h ^= h >> 29
	h *= 0xbf58476d1ce4e5b9
	h ^= h >> 32
	if h < uint64(sz)*(math.MaxUint64/uint64(c.Target)) {
		c.cur = 0
		return true
	}
	return false
}

// RecordCutter puts every record in its own group (the object-count extreme).
type RecordCutter struct{}

// Cut implements Cutter.
func (RecordCutter) Cut([]byte, int) bool { return true }

// AtCutter closes groups after the listed record indexes (ascending); an adapter that
// chunks the canonical stream with another algorithm supplies the positions.
type AtCutter struct {
	After []int
	i, n  int
}

// Cut implements Cutter.
func (c *AtCutter) Cut([]byte, int) bool {
	c.n++
	if c.i < len(c.After) && c.After[c.i] == c.n-1 {
		c.i++
		return true
	}
	return false
}

// Options configures Encode.
type Options struct {
	Form   Form
	Cutter Cutter // nil: one group per data block
	Limits Limits
	// NoPredict forces explicit Snappy command recipes (measurement only).
	NoPredict bool
}

// Emit receives every component Encode produces, once per occurrence (the caller
// deduplicates by id). class is "block", "group", "keys", "vals", "trl" or "tail". The
// bytes are only valid during the call.
type Emit func(class string, id ID, data []byte) error

// Stats describes one encoding.
type Stats struct {
	Blocks, ExplicitBlocks      int
	CompBlocks                  [8]int
	Records                     int64
	Groups                      int
	KeyBytes, ValueBytes        int64 // user keys, values
	TrailerBytes                int64 // 8 per record
	DataBytes, TailBytes        int64
	DecodedBytes                int64
	SnappyCmds                  int64
	PredBlocks                  [4]int // Snappy blocks: explicit, Go-, C++1.1.10-, C++15-predicted
	Footer                      Footer
	RestartInterval             int
	Components, ComponentsBytes int64
}

// Records calls fn for every record of the table in file order with the user key,
// internal-key trailer word and value (valid only during the call).
func Records(r io.ReaderAt, size int64, lim Limits, fn func(ukey []byte, trailer uint64, val []byte) error) error {
	lim = lim.norm()
	t, err := Open(r, size, lim)
	if err != nil {
		return err
	}
	if err := t.Reject(); err != nil {
		return err
	}
	for _, h := range t.Blocks {
		rb, err := ReadBlock(r, size, h, t.Footer.Checksum, false, lim)
		if err != nil {
			return err
		}
		dec, _, err := DecodeBlock(rb, lim)
		if err != nil {
			return err
		}
		_, err = WalkBlock(dec, lim, func(_ int, key, val []byte) error {
			if len(key) < 8 {
				return unsupported("key of %d bytes has no internal-key trailer", len(key))
			}
			return fn(key[:len(key)-8], binary.LittleEndian.Uint64(key[len(key)-8:]), val)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

type groupBuilder struct {
	form Form
	emit Emit
	buf  [3][]byte
	n    uint32
	rec  *Recipe
	st   *Stats
}

func (g *groupBuilder) add(ukey []byte, trailer uint64, val []byte) {
	var t [8]byte
	binary.LittleEndian.PutUint64(t[:], trailer)
	switch g.form {
	case FormUKV:
		g.buf[0] = appendRec(g.buf[0], ukey, nil, val)
	case FormFull:
		g.buf[0] = appendRec(g.buf[0], ukey, t[:], val)
	case FormKV:
		g.buf[0] = appendRec(g.buf[0], ukey, nil, nil)
		g.buf[1] = appendRec(g.buf[1], nil, nil, val)
	case FormKTV:
		g.buf[0] = appendRec(g.buf[0], ukey, t[:], nil)
		g.buf[1] = appendRec(g.buf[1], nil, nil, val)
	case FormKVT:
		g.buf[0] = appendRec(g.buf[0], ukey, nil, nil)
		g.buf[1] = appendRec(g.buf[1], nil, nil, val)
		g.buf[2] = append(g.buf[2], t[:]...)
	}
	if g.form.trailersInRecipe() {
		g.rec.Trailers = append(g.rec.Trailers, trailer)
	}
	g.n++
}

// appendRec appends uvarint(len key) key trailer uvarint(len val) val, leaving out the
// parts that are nil (key nil: no key part; val nil: no value part).
func appendRec(b []byte, key, trailer, val []byte) []byte {
	if key != nil {
		b = binary.AppendUvarint(b, uint64(len(key)))
		b = append(b, key...)
		b = append(b, trailer...)
	}
	if val != nil || key == nil {
		b = binary.AppendUvarint(b, uint64(len(val)))
		b = append(b, val...)
	}
	return b
}

func (g *groupBuilder) close() error {
	if g.n == 0 {
		return nil
	}
	names := [...]string{"group", "", ""}
	switch g.form {
	case FormKV, FormKTV:
		names = [3]string{"keys", "vals", ""}
	case FormKVT:
		names = [3]string{"keys", "vals", "trl"}
	}
	for s := 0; s < g.form.streams(); s++ {
		id := sha256.Sum256(g.buf[s])
		if err := g.emit(names[s], id, g.buf[s]); err != nil {
			return err
		}
		g.st.Components++
		g.st.ComponentsBytes += int64(len(g.buf[s]))
		g.rec.Digests = append(g.rec.Digests, id)
		g.buf[s] = g.buf[s][:0]
	}
	g.rec.Groups = append(g.rec.Groups, g.n)
	g.st.Groups++
	g.n = 0
	return nil
}

// Encode analyses one table and produces its serialized recipe plus components (via
// emit). The table is read through r only; at most one data block, one group and the
// recipe are held in memory. A table or feature the representation does not express
// returns an error wrapping ErrUnsupported (the caller keeps ordinary CDC): there is
// never a partial result.
func Encode(r io.ReaderAt, size int64, opt Options, emit Emit) ([]byte, Sizes, Stats, error) {
	lim := opt.Limits.norm()
	var st Stats
	t, err := Open(r, size, lim)
	if err != nil {
		return nil, Sizes{}, st, err
	}
	if err := t.Reject(); err != nil {
		return nil, Sizes{}, st, err
	}
	if opt.Form > maxForm {
		return nil, Sizes{}, st, unsupported("form %d", opt.Form)
	}
	st.Footer = t.Footer
	rec := &Recipe{Form: opt.Form, Size: size, TailLen: size - t.DataEnd}
	if rec.TailLen > lim.MaxTail {
		return nil, Sizes{}, st, ErrLimit
	}
	logical := opt.Form != FormBlocks
	if logical {
		ri, err := restartInterval(r, size, t, lim)
		if err != nil {
			return nil, Sizes{}, st, err
		}
		rec.RestartInterval = uint32(ri)
		st.RestartInterval = ri
	}
	gb := &groupBuilder{form: opt.Form, emit: emit, rec: rec, st: &st}
	var (
		keys  []byte
		koff  []int
		vals  [][]byte
		bw    BlockWriter
		check bytes.Buffer
	)
	for _, h := range t.Blocks {
		rb, err := ReadBlock(r, size, h, t.Footer.Checksum, true, lim)
		if err != nil {
			return nil, Sizes{}, st, err
		}
		dec, cmds, err := DecodeBlock(rb, lim)
		if err != nil {
			return nil, Sizes{}, st, err
		}
		bl := BlockRec{CSize: h.Size, Trailer: rb.Trailer}
		st.Blocks++
		st.CompBlocks[rb.Comp()&7]++
		st.DataBytes += int64(h.Size) + trailerLen
		st.DecodedBytes += int64(len(dec))
		if rb.Comp() == CompSnappy {
			st.SnappyCmds += int64(len(cmds))
			if !opt.NoPredict && len(dec) <= snappyz3.MaxPredict {
				for p, f := range [...]func([]byte) []byte{PredGo: snappyz3.EncodeRef, PredCPP: snappyz3.EncodeCPP, PredCPP15: snappyz3.EncodeCPP15} {
					if f != nil && bytes.Equal(f(dec), rb.Payload) {
						bl.Pred = uint8(p)
						break
					}
				}
			}
			switch {
			case bl.Pred != 0:
			default:
				bl.Cmds = snappyz3.Marshal(cmds)
				check.Reset()
				if err := snappyz3.Replay(&check, dec, cmds); err != nil || !bytes.Equal(check.Bytes(), rb.Payload) {
					return nil, Sizes{}, st, unsupported("snappy replay of block at %d is not exact", h.Off)
				}
			}
			st.PredBlocks[bl.Pred]++
		}
		if !logical {
			bl.Decoded = uint64(len(dec))
			id := sha256.Sum256(dec)
			if err := emit("block", id, dec); err != nil {
				return nil, Sizes{}, st, err
			}
			st.Components++
			st.ComponentsBytes += int64(len(dec))
			rec.Digests = append(rec.Digests, id)
			rec.Blocks = append(rec.Blocks, bl)
			continue
		}
		keys, koff, vals = keys[:0], koff[:0], vals[:0]
		lay, err := WalkBlock(dec, lim, func(_ int, key, val []byte) error {
			if len(key) < 8 {
				return unsupported("key of %d bytes has no internal-key trailer", len(key))
			}
			koff = append(koff, len(keys))
			keys = append(keys, key...)
			vals = append(vals, val)
			return nil
		})
		if err != nil {
			return nil, Sizes{}, st, err
		}
		koff = append(koff, len(keys))
		keyAt := func(i int) []byte { return keys[koff[i]:koff[i+1]] }
		bl.Entries = uint32(lay.Entries)
		derived := lay.derivesFrom(int(rec.RestartInterval), keyAt)
		if derived {
			bw.Reset()
			for i := 0; i < lay.Entries; i++ {
				if bw.Add(keyAt(i), vals[i], i%int(rec.RestartInterval) == 0, -1) != nil {
					derived = false
					break
				}
			}
			derived = derived && bytes.Equal(bw.Finish(), dec)
		}
		if !derived {
			ex := &Explicit{}
			for _, x := range lay.RestartIdx {
				ex.Restarts = append(ex.Restarts, uint32(x))
			}
			for _, x := range lay.Shared {
				ex.Shared = append(ex.Shared, uint32(x))
			}
			bw.Reset()
			ri := 0
			for i := 0; i < lay.Entries; i++ {
				isR := ri < len(lay.RestartIdx) && lay.RestartIdx[ri] == i
				if isR {
					ri++
				}
				if bw.Add(keyAt(i), vals[i], isR, lay.Shared[i]) != nil {
					return nil, Sizes{}, st, unsupported("block at %d cannot be rebuilt", h.Off)
				}
			}
			if !bytes.Equal(bw.Finish(), dec) {
				return nil, Sizes{}, st, unsupported("block at %d is not canonically encoded", h.Off)
			}
			bl.Explicit = ex
			st.ExplicitBlocks++
		}
		for i := 0; i < lay.Entries; i++ {
			k := keyAt(i)
			uk, tr := k[:len(k)-8], binary.LittleEndian.Uint64(k[len(k)-8:])
			gb.add(uk, tr, vals[i])
			st.Records++
			st.KeyBytes += int64(len(uk))
			st.ValueBytes += int64(len(vals[i]))
			st.TrailerBytes += 8
			if opt.Cutter != nil && opt.Cutter.Cut(uk, len(vals[i])) {
				if err := gb.close(); err != nil {
					return nil, Sizes{}, st, err
				}
			}
		}
		if opt.Cutter == nil {
			if err := gb.close(); err != nil {
				return nil, Sizes{}, st, err
			}
		}
		rec.Blocks = append(rec.Blocks, bl)
	}
	if err := gb.close(); err != nil {
		return nil, Sizes{}, st, err
	}
	tail := make([]byte, rec.TailLen)
	if err := readFull(r, tail, t.DataEnd); err != nil {
		return nil, Sizes{}, st, err
	}
	id := sha256.Sum256(tail)
	if err := emit("tail", id, tail); err != nil {
		return nil, Sizes{}, st, err
	}
	st.Components++
	st.ComponentsBytes += int64(len(tail))
	st.TailBytes = int64(len(tail))
	rec.Digests = append(rec.Digests, id)
	b, sz := rec.Marshal()
	if _, err := ParseRecipe(b, lim); err != nil {
		return nil, Sizes{}, st, err
	}
	return b, sz, st, nil
}

// restartInterval finds the table's restart interval: the entry distance between the
// first two restarts of the first block that has two. Tables whose blocks all have one
// restart use the LevelDB default of 16 (such blocks conform trivially or are stored
// explicitly).
func restartInterval(r io.ReaderAt, size int64, t *Table, lim Limits) (int, error) {
	for _, h := range t.Blocks {
		rb, err := ReadBlock(r, size, h, t.Footer.Checksum, false, lim)
		if err != nil {
			return 0, err
		}
		dec, _, err := DecodeBlock(rb, lim)
		if err != nil {
			return 0, err
		}
		lay, err := WalkBlock(dec, lim, nil)
		if err != nil {
			return 0, err
		}
		if len(lay.RestartIdx) >= 2 {
			return lay.RestartIdx[1] - lay.RestartIdx[0], nil
		}
	}
	return 16, nil
}
