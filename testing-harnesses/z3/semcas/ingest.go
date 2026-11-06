package semcas

import (
	"fmt"
	"time"

	"zeros3-z3/lab"
)

// LitChunkMin: literal regions at least this long are split by content-defined chunking
// into shared Bytes components (headers, vocabularies and graph metadata recur across
// formats and revisions); shorter literals stay inline in the recipe.
const LitChunkMin = 16 << 10

// FileStats are per-file ingest accounting figures.
type FileStats struct {
	Name, Format         string
	Size                 int64
	LitBytes, LitNew     int64
	Tensors              int
	TensorBytes          int64
	How                  map[string]int
	HowBytes             map[string]int64 // tensor payload bytes by placement outcome
	NewBlobBytes         int64
	RecBytes, PatchBytes int64
	RecipeBytes          int64
	Search, Total        time.Duration
}

// Ingest tiles a parsed document into a recipe, placing every tensor span through the
// planner. The returned recipe is authenticated against the original bytes.
func (p *Planner) Ingest(doc *Doc) (*Recipe, *FileStats, error) {
	start := time.Now()
	fs := &FileStats{Name: doc.Name, Format: doc.Format, Size: int64(len(doc.Data)), How: map[string]int{}, HowBytes: map[string]int64{}}
	var b Builder
	for _, sg := range doc.Segs {
		raw := doc.Data[sg.Off : sg.Off+sg.Len]
		if sg.T == nil {
			fs.LitBytes += sg.Len
			if len(raw) < LitChunkMin {
				b.Lit(raw)
				continue
			}
			w0 := p.S.Written
			for _, ch := range lab.ChunkScaled(raw, 2<<10, 8<<10, 32<<10) {
				d := Desc{Kind: KindBytes, Version: 1, DType: DU8, Shape: []uint64{uint64(len(ch))}, Ordered: true}
				r, err := p.S.PutRaw(d, ch)
				if err != nil {
					return nil, nil, err
				}
				b.Ref(r.CID, "lit")
			}
			fs.LitNew += p.S.Written - w0
			continue
		}
		pl, err := p.Place(sg.T, raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: tensor %q: %w", doc.Name, sg.T.Name, err)
		}
		b.Ref(pl.CID, sg.T.Name)
		fs.Tensors++
		fs.TensorBytes += sg.Len
		fs.How[pl.How]++
		fs.HowBytes[pl.How] += sg.Len
		fs.NewBlobBytes += pl.NewBytes
		fs.RecBytes += pl.RecBytes
		fs.PatchBytes += pl.PatchBytes
		fs.Search += pl.Search
	}
	b.R.Size = int64(len(doc.Data))
	b.R.SHA = PayloadSHA(doc.Data)
	rec := b.R
	fs.RecipeBytes = int64(len(rec.Marshal()))
	fs.Total = time.Since(start)
	return &rec, fs, nil
}
