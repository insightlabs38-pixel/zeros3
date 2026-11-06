package graphnorm

import (
	"crypto/sha256"

	"zeros3-z3/lab"
)

// Physical cost model shared with B1/B3: 96 B record+locator per physical object, 36 B per
// reference from a root/recipe, blobs charged with the production pack compressor rule.
const (
	ObjOverhead = 96
	RefCost     = 36
)

// CAS is a deduplicating cost-accounting object store.
type CAS struct {
	seen    map[[32]byte]bool
	Total   int64
	Objects int
	Refs    int
	Raw     int64
}

func NewCAS() *CAS { return &CAS{seen: map[[32]byte]bool{}} }

// Put charges a blob once (packed + object overhead) and returns the incremental cost.
func (c *CAS) Put(b []byte) int64 {
	h := sha256.Sum256(b)
	if c.seen[h] {
		return 0
	}
	c.seen[h] = true
	n := lab.PackedLen(b) + ObjOverhead
	c.Total += n
	c.Objects++
	c.Raw += int64(len(b))
	return n
}

// Ref charges one reference.
func (c *CAS) Ref(n int) int64 {
	c.Refs += n
	c.Total += int64(n) * RefCost
	return int64(n) * RefCost
}

// Chunked stores data as CDC chunks (scaled gear chunker) with one reference per chunk.
func (c *CAS) Chunked(data []byte, min, tgt, max int) (inc int64) {
	for _, ch := range lab.ChunkScaled(data, min, tgt, max) {
		inc += c.Put(ch)
		inc += c.Ref(1)
	}
	return
}

// Whole stores one blob with one reference (a function template, a small table).
func (c *CAS) Whole(b []byte) int64 { return c.Put(b) + c.Ref(1) }

// packedLenOf is the pack-compressor size of b (column predictor selection criterion).
func packedLenOf(b []byte) int64 { return lab.PackedLen(b) }
