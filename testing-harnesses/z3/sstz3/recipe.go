package sstz3

import (
	"encoding/binary"
	"math"

	"zeros3-z3/snappyz3"
)

// ID is the SHA-256 identity of a component.
type ID = [32]byte

// Form selects how logical records are spread over components.
type Form uint8

const (
	// FormBlocks keeps each data block's exact decoded bytes as one component (the
	// structural baseline: no entry-level detachment).
	FormBlocks Form = 0
	// FormUKV: one component per group of records, user key + value; the internal-key
	// trailers (sequence/type) live in the recipe.
	FormUKV Form = 1
	// FormFull: one component per group, user key + trailer + value ("full-entry").
	FormFull Form = 2
	// FormKV: a key component and a value component per group; trailers in the recipe.
	FormKV Form = 3
	// FormKTV: key+trailer component and value component per group.
	FormKTV Form = 4
	// FormKVT: key, value and trailer components per group.
	FormKVT Form = 5
	maxForm      = FormKVT
)

func (l Form) String() string {
	return [...]string{"blocks", "ukv", "full", "kv", "ktv", "kvt"}[l]
}

// streams reports how many components a group of the layout occupies.
func (l Form) streams() int {
	switch l {
	case FormKV, FormKTV:
		return 2
	case FormKVT:
		return 3
	}
	return 1
}

// trailersInRecipe reports whether the sequence/type words are recipe bytes.
func (l Form) trailersInRecipe() bool { return l == FormUKV || l == FormKV }

// Snappy predictors: deterministic reference encoders whose output is compared with the stored block.
const (
	PredGo    = 1 // snappyz3.EncodeRef
	PredCPP   = 2 // snappyz3.EncodeCPP (snappy 1.1.10)
	PredCPP15 = 3 // snappyz3.EncodeCPP15 (RocksDB 9.8.4 bundled snappy)
)

const recipeVersion = 1

// BlockRec is the per-block physical framing.
type BlockRec struct {
	Zstd     []byte // exact zstd skeleton/units and original size prefix
	CSize    uint64 // stored payload bytes (without the trailer)
	Trailer  [trailerLen]byte
	Entries  uint32 // logical layouts
	Decoded  uint64 // FormBlocks: decoded block length
	Explicit *Explicit
	Cmds     []byte // snappyz3 recipe (Snappy blocks that no reference encoder reproduces)
	Pred     uint8  // Snappy: 0 explicit commands, PredGo or PredCPP when that reference encoder reproduces the block
}

// Explicit is a block layout that does not follow the table's derived framing.
type Explicit struct {
	Restarts []uint32 // entry index of every restart
	Shared   []uint32 // shared prefix length of every entry
}

// Recipe is the decoded serialized description of one table.
type Recipe struct {
	Form            Form
	Size            int64
	RestartInterval uint32
	Blocks          []BlockRec
	Groups          []uint32 // records per group
	Trailers        []uint64 // one per record when trailersInRecipe
	TailLen         int64
	Digests         []ID // groups x streams..., then the tail
}

// Sizes is the byte accounting of a serialized recipe.
type Sizes struct {
	Head, Framing, Snappy, Zstd, Explicit, Groups, Trailers, Digests int64
}

func (s Sizes) Total() int64 {
	return s.Head + s.Framing + s.Snappy + s.Zstd + s.Explicit + s.Groups + s.Trailers + s.Digests
}

func appendUv(b []byte, v uint64) []byte { return binary.AppendUvarint(b, v) }

// Marshal serialises the recipe: head fields then the digests.
func (r *Recipe) Marshal() ([]byte, Sizes) {
	var s Sizes
	b := []byte{recipeVersion, byte(r.Form)}
	b = appendUv(b, uint64(r.Size))
	b = appendUv(b, uint64(r.RestartInterval))
	b = appendUv(b, uint64(len(r.Blocks)))
	s.Head = int64(len(b))
	for i := range r.Blocks {
		bl := &r.Blocks[i]
		n0 := len(b)
		b = appendUv(b, bl.CSize)
		b = append(b, bl.Trailer[:]...)
		if r.Form == FormBlocks {
			b = appendUv(b, bl.Decoded)
		} else {
			e := uint64(bl.Entries) << 1
			if bl.Explicit != nil {
				e |= 1
			}
			b = appendUv(b, e)
		}
		s.Framing += int64(len(b) - n0)
		if bl.Explicit != nil {
			n1 := len(b)
			b = appendUv(b, uint64(len(bl.Explicit.Restarts)))
			prev := uint32(0)
			for _, x := range bl.Explicit.Restarts {
				b = appendUv(b, uint64(x-prev))
				prev = x
			}
			for _, x := range bl.Explicit.Shared {
				b = appendUv(b, uint64(x))
			}
			s.Explicit += int64(len(b) - n1)
		}
		if bl.Trailer[0] == CompZstd {
			n := len(b)
			b = appendUv(b, uint64(len(bl.Zstd)))
			b = append(b, bl.Zstd...)
			s.Zstd += int64(len(b) - n)
		}
		if bl.Trailer[0] == CompSnappy {
			n1 := len(b)
			if bl.Pred != 0 {
				b = appendUv(b, uint64(bl.Pred-1))
			} else {
				b = appendUv(b, uint64(len(bl.Cmds))+3)
				b = append(b, bl.Cmds...)
			}
			s.Snappy += int64(len(b) - n1)
		}
	}
	if r.Form != FormBlocks {
		n0 := len(b)
		b = appendUv(b, uint64(len(r.Groups)))
		for _, g := range r.Groups {
			b = appendUv(b, uint64(g))
		}
		s.Groups = int64(len(b) - n0)
	}
	if r.Form.trailersInRecipe() {
		n0 := len(b)
		b = appendTrailers(b, r.Trailers)
		s.Trailers = int64(len(b) - n0)
	}
	n0 := len(b)
	b = appendUv(b, uint64(r.TailLen))
	s.Framing += int64(len(b) - n0)
	for _, d := range r.Digests {
		b = append(b, d[:]...)
	}
	s.Digests = int64(len(r.Digests) * 32)
	return b, s
}

// appendTrailers codes the sequence/type words: first the type bytes as (value, run)
// pairs, then the sequence numbers as zigzag deltas.
func appendTrailers(b []byte, tr []uint64) []byte {
	var runs [][2]uint64
	for _, t := range tr {
		k := t & 0xff
		if n := len(runs); n > 0 && runs[n-1][0] == k {
			runs[n-1][1]++
		} else {
			runs = append(runs, [2]uint64{k, 1})
		}
	}
	b = appendUv(b, uint64(len(runs)))
	for _, r := range runs {
		b = append(b, byte(r[0]))
		b = appendUv(b, r[1])
	}
	prev := int64(0)
	for _, t := range tr {
		s := int64(t >> 8)
		d := s - prev
		b = appendUv(b, uint64(d<<1)^uint64(d>>63))
		prev = s
	}
	return b
}

type rd struct {
	b   []byte
	pos int
	err error
}

func (r *rd) fail(f string, a ...any) {
	if r.err == nil {
		r.err = recipeErr(f, a...)
	}
}
func (r *rd) uv(max uint64) uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 || n != uvLen(v) {
		r.fail("varint at %d", r.pos)
		return 0
	}
	if v > max {
		r.fail("value %d above %d at %d", v, max, r.pos)
		return 0
	}
	r.pos += n
	return v
}
func (r *rd) take(n uint64) []byte {
	if r.err != nil {
		return nil
	}
	if n > uint64(len(r.b)-r.pos) {
		r.fail("%d bytes at %d exceed the recipe", n, r.pos)
		return nil
	}
	s := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return s
}

func uvLen(v uint64) int {
	var b [binary.MaxVarintLen64]byte
	return binary.PutUvarint(b[:], v)
}

// ParseRecipe parses and bounds-checks a serialized recipe (head and digests). It
// allocates nothing from a declared count beyond what the remaining bytes could hold.
func ParseRecipe(b []byte, lim Limits) (*Recipe, error) {
	lim = lim.norm()
	if int64(len(b)) > lim.MaxRecipe {
		return nil, ErrLimit
	}
	if len(b) < 2 || b[0] != recipeVersion {
		return nil, recipeErr("version")
	}
	if Form(b[1]) > maxForm {
		return nil, recipeErr("layout %d", b[1])
	}
	r := &rd{b: b, pos: 2}
	rec := &Recipe{Form: Form(b[1])}
	rec.Size = int64(r.uv(math.MaxInt64))
	rec.RestartInterval = uint32(r.uv(1 << 20))
	nb := r.uv(uint64(lim.MaxBlocks))
	if r.err == nil && nb > uint64(len(b)-r.pos)/7 {
		r.fail("%d blocks cannot fit in the recipe", nb)
	}
	if r.err == nil {
		rec.Blocks = make([]BlockRec, 0, nb)
	}
	var totalEntries, totalSize uint64
	for i := uint64(0); i < nb && r.err == nil; i++ {
		var bl BlockRec
		bl.CSize = r.uv(uint64(lim.MaxBlockBytes))
		copy(bl.Trailer[:], r.take(trailerLen))
		if rec.Form == FormBlocks {
			bl.Decoded = r.uv(uint64(lim.MaxDecoded))
		} else {
			e := r.uv(uint64(lim.MaxEntries)<<1 | 1)
			bl.Entries = uint32(e >> 1)
			totalEntries += e >> 1
			if e&1 != 0 {
				ex := &Explicit{}
				nr := r.uv(uint64(bl.Entries) + 1)
				if r.err == nil && nr > uint64(len(b)-r.pos) {
					r.fail("restart list")
				}
				prev := uint64(0)
				for k := uint64(0); k < nr && r.err == nil; k++ {
					prev += r.uv(uint64(bl.Entries))
					ex.Restarts = append(ex.Restarts, uint32(prev))
				}
				if r.err == nil && uint64(bl.Entries) > uint64(len(b)-r.pos) {
					r.fail("shared list")
				}
				for k := uint32(0); k < bl.Entries && r.err == nil; k++ {
					ex.Shared = append(ex.Shared, uint32(r.uv(uint64(lim.MaxRecordBytes))))
				}
				bl.Explicit = ex
			}
		}
		if bl.Trailer[0] == CompSnappy {
			n := r.uv(uint64(len(b)) + 3)
			if n < 3 {
				bl.Pred = uint8(n) + 1
			} else {
				bl.Cmds = r.take(n - 3)
			}
		} else if bl.Trailer[0] == CompZstd {
			bl.Zstd = r.take(r.uv(uint64(lim.MaxRecipe)))
		} else if bl.Trailer[0] != CompNone {
			r.fail("compression type %d", bl.Trailer[0])
		}
		totalSize += bl.CSize + trailerLen
		rec.Blocks = append(rec.Blocks, bl)
	}
	if r.err != nil {
		return nil, r.err
	}
	if rec.Form != FormBlocks {
		ng := r.uv(totalEntries)
		if r.err == nil && ng > uint64(len(b)-r.pos) {
			r.fail("%d groups cannot fit in the recipe", ng)
		}
		var sum uint64
		for i := uint64(0); i < ng && r.err == nil; i++ {
			g := r.uv(totalEntries)
			if g == 0 {
				r.fail("empty group")
			}
			sum += g
			rec.Groups = append(rec.Groups, uint32(g))
		}
		if r.err == nil && sum != totalEntries {
			r.fail("groups hold %d records, blocks hold %d", sum, totalEntries)
		}
		if rec.Form.trailersInRecipe() && r.err == nil {
			rec.Trailers = readTrailers(r, int(totalEntries))
		}
	}
	rec.TailLen = int64(r.uv(uint64(lim.MaxTail)))
	if r.err != nil {
		return nil, r.err
	}
	if totalSize > uint64(math.MaxInt64) || int64(totalSize)+rec.TailLen != rec.Size {
		return nil, recipeErr("blocks (%d) and tail (%d) do not add up to the size %d", totalSize, rec.TailLen, rec.Size)
	}
	n := rec.members()
	if uint64(len(b)-r.pos) != 32*uint64(n) {
		return nil, recipeErr("%d digest bytes, expected %d", len(b)-r.pos, 32*n)
	}
	rec.Digests = make([]ID, n)
	for i := range rec.Digests {
		copy(rec.Digests[i][:], b[r.pos+32*i:])
	}
	return rec, nil
}

func readTrailers(r *rd, n int) []uint64 {
	nr := r.uv(uint64(n))
	if r.err == nil && nr > uint64(len(r.b)-r.pos)/2 {
		r.fail("trailer runs")
		return nil
	}
	kinds := make([]byte, 0, n)
	var tot uint64
	for i := uint64(0); i < nr && r.err == nil; i++ {
		k := r.take(1)
		run := r.uv(uint64(n))
		if r.err != nil {
			return nil
		}
		tot += run
		if tot > uint64(n) || run == 0 {
			r.fail("trailer run")
			return nil
		}
		for j := uint64(0); j < run; j++ {
			kinds = append(kinds, k[0])
		}
	}
	if r.err == nil && tot != uint64(n) {
		r.fail("trailer runs cover %d of %d records", tot, n)
	}
	out := make([]uint64, 0, n)
	prev := int64(0)
	for i := 0; i < n && r.err == nil; i++ {
		z := r.uv(math.MaxUint64)
		d := int64(z>>1) ^ -int64(z&1)
		prev += d
		if prev < 0 || prev >= 1<<56 {
			r.fail("sequence number")
			return nil
		}
		out = append(out, uint64(prev)<<8|uint64(kinds[i]))
	}
	return out
}

// members is the number of digests the recipe lists.
func (r *Recipe) members() int {
	if r.Form == FormBlocks {
		return len(r.Blocks) + 1
	}
	return len(r.Groups)*r.Form.streams() + 1
}

// snappyCmds decodes a block's Snappy command recipe for a block of the given decoded length.
func snappyCmds(bl *BlockRec, decoded int64, lim Limits) ([]snappyz3.Cmd, error) {
	return snappyz3.Unmarshal(bl.Cmds, decoded, snappyz3.Limits{MaxDecoded: lim.norm().MaxDecoded})
}
