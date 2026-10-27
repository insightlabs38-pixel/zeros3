package kinds

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"zeros3-z3/parquetz3"
	"zeros3-z3/repz3"
)

// Parquet is the Z3-31 lab kind. The representation is
//
//	Members  the stored components the recipe names: the root nodes of every column's value
//	         and level streams, opaque framing regions, explicit dictionaries, explicit
//	         compression recipes and the payloads of opaque chunks, all STORED
//	Recipe   the parquetz3 head: column table, the file as an ordered segment list (raw page
//	         headers and footer inline, per-page layout flags and compression ids) and, for
//	         every stream, the item count behind each root node
//
// The leaf groups hang off the nodes (VIA, named only inside the stored nodes), so a
// changed value changes one group, one node and the recipe, and liveness is ordinary
// reachability: Expand reads the nodes. Nothing is a closure and nothing needs a Parquet
// library: the decoder is parquetz3.Decode over the serialized bytes.

// EncodeParquet analyses one Parquet file. An error wrapping parquetz3.ErrUnsupported (or
// ErrCorrupt/ErrLimit) means "use CDC".
func EncodeParquet(src io.ReaderAt, size int64, opt parquetz3.Options) (*Bundle, parquetz3.Stats, parquetz3.Sizes, error) {
	sizes := map[repz3.SHA]int64{}
	var objs [][]byte
	rec, sz, st, err := parquetz3.Encode(src, size, opt, func(_ string, id parquetz3.ID, b []byte) error {
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
	pr, err := parquetz3.ParseRecipe(rec, opt.Limits)
	if err != nil {
		return nil, st, sz, err
	}
	head := len(rec) - 32*len(pr.Digests)
	out := &repz3.Rep{Kind: repz3.KindParquet, Size: size, Recipe: rec[:head:head]}
	for _, d := range pr.Digests {
		out.Members = append(out.Members, repz3.Member{Role: repz3.Stored, SHA: d, Size: sizes[d]})
	}
	b, err := newBundle(out, objs)
	return b, st, sz, err
}

type parquetDecoder struct{}

func parquetRecipe(c *repz3.Call) ([]byte, error) {
	rec := append([]byte(nil), c.Rep.Recipe...)
	for i := range c.Rep.Members {
		if c.Rep.Members[i].Role != repz3.Stored {
			return nil, fmt.Errorf("%w: Parquet member %d is not a stored component", repz3.ErrCorrupt, i)
		}
		rec = append(rec, c.Rep.Members[i].SHA[:]...)
	}
	return rec, nil
}

func (parquetDecoder) Decode(c *repz3.Call) error {
	rec, err := parquetRecipe(c)
	if err != nil {
		return err
	}
	return parquetz3.Decode(c.W, rec, func(id parquetz3.ID) ([]byte, error) { return c.Get(id) }, parquetz3.Limits{})
}

// Expand names the leaf groups (the members are listed; the groups are only in the nodes).
func (parquetDecoder) Expand(c *repz3.Call, emit func(repz3.SHA)) error {
	rec, err := parquetRecipe(c)
	if err != nil {
		return err
	}
	return parquetz3.Components(rec, func(id parquetz3.ID) ([]byte, error) { return c.Get(id) }, parquetz3.Limits{}, func(id parquetz3.ID) { emit(id) })
}

// ReadParquetRange returns bytes [off, off+n) of an opened Parquet representation,
// rebuilding only the segments (pages) that overlap the range.
func ReadParquetRange(c *repz3.Call, off, n int64) ([]byte, parquetz3.RangeStats, error) {
	if c.Rep.Kind != repz3.KindParquet {
		return nil, parquetz3.RangeStats{}, errors.New("kinds: not a Parquet representation")
	}
	rec, err := parquetRecipe(c)
	if err != nil {
		return nil, parquetz3.RangeStats{}, err
	}
	var out bytes.Buffer
	st, err := parquetz3.DecodeRange(&out, rec, func(id parquetz3.ID) ([]byte, error) { return c.Get(id) }, off, n, parquetz3.Limits{})
	return out.Bytes(), st, err
}
