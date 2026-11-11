package semcore

import (
	"crypto/sha256"
	"fmt"
	"time"
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
	// SourceBytes counts bytes read from the document; PeakBuf is the largest segment
	// buffer held; Oversize counts tensors stored through the bounded block path.
	SourceBytes, PeakBuf int64
	Oversize             int
}

// Streaming bounds. A tensor larger than Options.MaxTensor is stored as fixed-size
// exact Bytes components (no semantic search): its payload is never resident whole.
const (
	DefaultMaxTensor = 192 << 20
	bigBlock         = 32 << 20
	litWindow        = 64 << 20
)

// Ingest tiles a parsed document into a recipe, placing every tensor span through the
// planner. The document is read one segment at a time (a ReaderAt-backed Doc is never
// resident whole) and the original file's SHA-256 is computed incrementally over the
// segments in file order; the returned recipe is authenticated against it.
func (p *Planner) Ingest(doc *Doc) (*Recipe, *FileStats, error) {
	start := time.Now()
	fs := &FileStats{Name: doc.Name, Format: doc.Format, Size: doc.Len(), How: map[string]int{}, HowBytes: map[string]int64{}}
	var b Builder
	h := sha256.New()
	var buf []byte
	maxT := int64(p.Opt.MaxTensor)
	if maxT <= 0 {
		maxT = DefaultMaxTensor
	}
	for _, sg := range doc.Segs {
		if sg.T == nil {
			fs.LitBytes += sg.Len
			for off := int64(0); off < sg.Len; off += litWindow {
				wl := min(int64(litWindow), sg.Len-off)
				raw, err := doc.ReadSeg(Seg{Off: sg.Off + off, Len: wl}, buf)
				if err != nil {
					return nil, nil, err
				}
				buf = raw[:0]
				h.Write(raw)
				fs.SourceBytes += wl
				if len(raw) < LitChunkMin {
					b.Lit(raw)
					continue
				}
				w0 := p.S.Written
				for _, ch := range p.chunk(raw) {
					d := Desc{Kind: KindBytes, Version: 1, DType: DU8, Shape: []uint64{uint64(len(ch))}, Ordered: true}
					r, err := p.S.PutRaw(d, ch)
					if err != nil {
						return nil, nil, err
					}
					b.Ref(r.CID, "lit")
				}
				fs.LitNew += p.S.Written - w0
			}
			continue
		}
		if sg.Len > maxT {
			fs.Oversize++
			fs.Tensors++
			fs.TensorBytes += sg.Len
			fs.HowBytes["oversize"] += sg.Len
			fs.How["oversize"]++
			for off := int64(0); off < sg.Len; off += bigBlock {
				wl := min(int64(bigBlock), sg.Len-off)
				raw, err := doc.ReadSeg(Seg{Off: sg.Off + off, Len: wl}, buf)
				if err != nil {
					return nil, nil, err
				}
				buf = raw[:0]
				h.Write(raw)
				fs.SourceBytes += wl
				w0 := p.S.Written
				d := Desc{Kind: KindBytes, Version: 1, DType: DU8, Shape: []uint64{uint64(len(raw))}, Ordered: true}
				r, err := p.S.PutRaw(d, raw)
				if err != nil {
					return nil, nil, fmt.Errorf("%s: tensor %q: %w", doc.Name, sg.T.Name, err)
				}
				b.Ref(r.CID, sg.T.Name)
				fs.NewBlobBytes += p.S.Written - w0
			}
			continue
		}
		raw, err := doc.ReadSeg(sg, buf)
		if err != nil {
			return nil, nil, err
		}
		buf = raw[:0]
		h.Write(raw)
		fs.SourceBytes += sg.Len
		if int64(cap(buf)) > fs.PeakBuf {
			fs.PeakBuf = int64(cap(buf))
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
	b.R.Size = doc.Len()
	h.Sum(b.R.SHA[:0])
	rec := b.R
	fs.RecipeBytes = int64(len(rec.Marshal()))
	fs.Total = time.Since(start)
	return &rec, fs, nil
}

// chunk splits a literal region into shared components; the chunker is an adapter hook
// (production: the engine's CDC). Without one the region is a single component.
func (p *Planner) chunk(raw []byte) [][]byte {
	if p.Chunk == nil {
		return [][]byte{raw}
	}
	return p.Chunk(raw)
}
