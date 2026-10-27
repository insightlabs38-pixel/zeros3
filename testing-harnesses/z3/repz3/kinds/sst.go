package kinds

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"

	"zeros3-z3/repz3"
	"zeros3-z3/sstz3"
)

// SST is the Z3-30 lab kind for LevelDB, RocksDB BlockBasedTable and Pebble tables. The
// representation is
//
//	Members  every component of the table, in the order sstz3 lists them: the record groups
//	         (one to three components each, depending on the form) and, last, the opaque
//	         tail (filters, properties, index, footer), all STORED
//	Recipe   the sstz3 head: per-block framing (stored size, 5-byte trailer, entry count,
//	         Snappy predictor or explicit commands, layout exceptions), group record counts
//	         and, for forms that keep them there, the sequence/type words
//
// The generic layer owns every SHA-256 (the members); sstz3 never sees a digest it was
// not given. Everything a component holds is reachable from the members, so liveness is
// ordinary reachability: no VIA components, no counts, no catalog.

// EncodeSST analyses one table. The returned stats describe the table; an error wrapping
// sstz3.ErrUnsupported means "use CDC".
func EncodeSST(src io.ReaderAt, size int64, opt sstz3.Options) (*Bundle, sstz3.Stats, sstz3.Sizes, error) {
	sizes := map[sstz3.ID]int64{}
	var objs [][]byte
	rec, sz, st, err := sstz3.Encode(src, size, opt, func(_ string, id sstz3.ID, b []byte) error {
		if _, ok := sizes[id]; ok {
			return nil
		}
		sizes[id] = int64(len(b))
		objs = append(objs, append([]byte(nil), b...))
		return nil
	})
	if err != nil {
		return nil, st, sz, err
	}
	pr, err := sstz3.ParseRecipe(rec, opt.Limits)
	if err != nil {
		return nil, st, sz, err
	}
	head := len(rec) - 32*len(pr.Digests)
	out := &repz3.Rep{Kind: repz3.KindSST, Size: size, Recipe: rec[:head:head]}
	for _, d := range pr.Digests {
		out.Members = append(out.Members, repz3.Member{Role: repz3.Stored, SHA: d, Size: sizes[d]})
	}
	b, err := newBundle(out, objs)
	return b, st, sz, err
}

type sstDecoder struct{}

func sstRecipe(c *repz3.Call) ([]byte, error) {
	rec := append([]byte(nil), c.Rep.Recipe...)
	for i := range c.Rep.Members {
		if c.Rep.Members[i].Role != repz3.Stored {
			return nil, fmt.Errorf("%w: SST member %d is not a stored component", repz3.ErrCorrupt, i)
		}
		rec = append(rec, c.Rep.Members[i].SHA[:]...)
	}
	return rec, nil
}

func (sstDecoder) Decode(c *repz3.Call) error {
	rec, err := sstRecipe(c)
	if err != nil {
		return err
	}
	return sstz3.Decode(c.W, rec, func(id sstz3.ID) ([]byte, error) { return c.Get(id) }, sstz3.Limits{})
}

// Expand: every component is a listed member, so there is nothing to discover.
func (sstDecoder) Expand(c *repz3.Call, emit func(repz3.SHA)) error { return nil }

// ReadSSTRange returns bytes [off, off+n) of an opened SST representation, rebuilding only
// the data blocks that overlap the range from the groups holding their records.
func ReadSSTRange(c *repz3.Call, off, n int64) ([]byte, sstz3.RangeStats, error) {
	if c.Rep.Kind != repz3.KindSST {
		return nil, sstz3.RangeStats{}, errors.New("kinds: not an SST representation")
	}
	rec, err := sstRecipe(c)
	if err != nil {
		return nil, sstz3.RangeStats{}, err
	}
	var out bytes.Buffer
	st, err := sstz3.DecodeRange(&out, rec, func(id sstz3.ID) ([]byte, error) { return c.Get(id) }, off, n, sstz3.Limits{})
	return out.Bytes(), st, err
}

// tailReader serves the table's file offsets at and beyond dataEnd from the tail component
// with ranged reads, so a lookup reads the footer, metaindex and index but not the filter.
type tailReader struct {
	c       *repz3.Call
	id      repz3.SHA
	dataEnd int64
	size    int64
	bytes   int64
	reads   int
}

func (t *tailReader) ReadAt(p []byte, off int64) (int, error) {
	if off < t.dataEnd || off+int64(len(p)) > t.size {
		return 0, fmt.Errorf("kinds: lookup read [%d,%d) outside the tail [%d,%d)", off, off+int64(len(p)), t.dataEnd, t.size)
	}
	t.reads++
	t.bytes += int64(len(p))
	n, err := t.c.Source().ReadAt(t.id, p, off-t.dataEnd)
	return n, err
}

// LookupStats reports what a point lookup fetched.
type LookupStats struct {
	TailBytes, TailReads int64
	Block                int
	Groups               sstz3.RangeStats
}

// LookupSST locates the data block that may hold userKey through the table's own index (a
// ranged read of the tail), rebuilds that one block and returns its decoded bytes with the
// numbers fetched. Searching the block for the key is left to the caller.
func LookupSST(c *repz3.Call, userKey []byte) ([]byte, LookupStats, error) {
	var ls LookupStats
	rec, err := sstRecipe(c)
	if err != nil {
		return nil, ls, err
	}
	pr, err := sstz3.ParseRecipe(rec, sstz3.Limits{})
	if err != nil {
		return nil, ls, err
	}
	tr := &tailReader{c: c, id: pr.Digests[len(pr.Digests)-1], dataEnd: pr.Size - pr.TailLen, size: pr.Size}
	idx, err := sstz3.OpenKeys(tr, pr.Size, sstz3.Limits{})
	ls.TailBytes, ls.TailReads = tr.bytes, int64(tr.reads)
	if err != nil {
		return nil, ls, err
	}
	i := sort.Search(len(idx.IndexKeys), func(i int) bool { return bytes.Compare(userPart(idx, i), userKey) >= 0 })
	if i >= len(idx.Blocks) {
		return nil, ls, repz3.ErrMissing
	}
	ls.Block = i
	h := idx.Blocks[i]
	var out bytes.Buffer
	st, err := sstz3.DecodeRange(&out, rec, func(id sstz3.ID) ([]byte, error) { return c.Get(id) }, int64(h.Off), int64(h.Size)+5, sstz3.Limits{})
	ls.Groups = st
	return out.Bytes(), ls, err
}

// userPart is index key i without its 8-byte trailer when the table's index keys are
// internal keys (LevelDB, Pebble) and unchanged when they are user keys (RocksDB).
func userPart(t *sstz3.Table, i int) []byte {
	k := t.IndexKeys[i]
	if t.IndexUserKeys || len(k) < 8 {
		return k
	}
	return k[:len(k)-8]
}
