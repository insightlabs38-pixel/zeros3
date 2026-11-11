package semcas

import (
	"zeros3-z3/lab"
	"zeros3-z3/semcore"
)

// Ext implements the format-specific op kinds of the research store: exact DEFLATE replay
// and exact Parquet replay. It is installed on every Store opened by this package.
type FormatExt struct{}

func (FormatExt) ApplyExt(o *semcore.Op, in [][]byte) ([]byte, error) {
	switch o.Kind {
	case semcore.OpParquet:
		return parquetReplay(o.Aux, in)
	case semcore.OpDeflate:
		if len(in) != 1 {
			return nil, semcore.ErrCorrupt
		}
		return inflateReplay(in[0], o.Aux)
	}
	return nil, semcore.ErrNoExt
}

// NewPlanner returns a tensor planner whose literal regions are chunked by the same
// scaled gear CDC the research store always used (so component identities are unchanged).
func NewPlanner(s *Store, o Options) *Planner {
	p := semcore.NewPlanner(s, o)
	p.Chunk = func(b []byte) [][]byte { return lab.ChunkScaled(b, 2<<10, 8<<10, 32<<10) }
	return p
}
