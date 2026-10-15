package kinds

import (
	"fmt"
	"io"

	"zeros3-z3/repz3"
	"zeros3-z3/sqlitez3"
)

// SQLite is the Z3-18 page tree. The sqlitez3 recipe ends with the 32-byte digest
// of every node; those digests are the representation's STORED members (the
// generic layer owns every SHA-256 reference) and the rest of the recipe is the
// opaque Recipe. The pages hang off the nodes (VIA).

// EncodeSQLite analyses a SQLite database file.
func EncodeSQLite(src io.ReaderAt, size int64) (*Bundle, sqlitez3.Stats, error) {
	sizes := map[sqlitez3.ID]int64{}
	var objs [][]byte
	rec, st, err := sqlitez3.Encode(src, size, sqlitez3.Options{}, func(id sqlitez3.ID, b []byte) error {
		if _, ok := sizes[id]; ok {
			return nil
		}
		sizes[id] = int64(len(b))
		objs = append(objs, append([]byte(nil), b...))
		return nil
	})
	if err != nil {
		return nil, st, err
	}
	pr, err := sqlitez3.ParseRecipe(rec, sqlitez3.Limits{})
	if err != nil {
		return nil, st, err
	}
	nn := pr.NodeCount()
	headLen := len(rec) - 32*nn
	out := &repz3.Rep{Kind: repz3.KindSQLite, Size: size, Recipe: rec[:headLen:headLen]}
	for k := 0; k < nn; k++ {
		var id sqlitez3.ID
		copy(id[:], rec[headLen+32*k:])
		out.Members = append(out.Members, repz3.Member{Role: repz3.Stored, SHA: id, Size: sizes[id]})
	}
	b, err := newBundle(out, objs)
	return b, st, err
}

type sqliteDecoder struct{}

func sqliteRecipe(c *repz3.Call) ([]byte, error) {
	rec := append([]byte(nil), c.Rep.Recipe...)
	for i := range c.Rep.Members {
		if c.Rep.Members[i].Role != repz3.Stored {
			return nil, fmt.Errorf("%w: SQLite member %d is not a stored node", repz3.ErrCorrupt, i)
		}
		rec = append(rec, c.Rep.Members[i].SHA[:]...)
	}
	return rec, nil
}

func (sqliteDecoder) Decode(c *repz3.Call) error {
	rec, err := sqliteRecipe(c)
	if err != nil {
		return err
	}
	return sqlitez3.Decode(c.W, rec, func(id sqlitez3.ID) ([]byte, error) { return c.Get(id) }, sqlitez3.Limits{})
}

func (sqliteDecoder) Expand(c *repz3.Call, emit func(repz3.SHA)) error {
	rec, err := sqliteRecipe(c)
	if err != nil {
		return err
	}
	return sqlitez3.Components(rec, func(id sqlitez3.ID) ([]byte, error) { return c.Get(id) }, sqlitez3.Limits{}, func(id sqlitez3.ID) { emit(id) })
}
