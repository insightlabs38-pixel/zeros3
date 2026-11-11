package semcas

import (
	"crypto/sha256"
	"os"
	"path/filepath"

	"zeros3-z3/lab"
)

// Physical cost constants follow the B1 model: 96 bytes of record+locator per physical
// object and 36 bytes per root reference; blobs use the production pack compressor rule.
const (
	ObjOverhead = 96
	RootRef     = 36
	IndexPerObj = 44 // 12B posting + 32B object entry of the rebuildable payload index
)

// Cost is a charged physical size of a store.
type Cost struct {
	Blobs, Comps, Recipes  int // object counts
	BlobRaw, BlobPacked    int64
	CompBytes, RecipeBytes int64
	Index                  int64
	DerivedComps, RawComps int
}

// Total is the standalone physical size (without the rebuildable index).
func (c Cost) Total() int64 {
	return c.BlobPacked + int64(c.Blobs)*ObjOverhead + c.CompBytes + int64(c.Comps)*ObjOverhead +
		c.RecipeBytes + int64(c.Recipes)*(ObjOverhead+RootRef)
}

// WithIndex adds the discovery index (hypothetical authoritative cost).
func (c Cost) WithIndex() int64 { return c.Total() + c.Index }

// Cost measures the store as it exists on disk right now.
func CostOf(s *Store, dir string) (Cost, error) {
	var c Cost
	ents, err := os.ReadDir(filepath.Join(dir, "blobs"))
	if err != nil {
		return c, err
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(dir, "blobs", e.Name()))
		if err != nil {
			return c, err
		}
		c.Blobs++
		c.BlobRaw += int64(len(b))
		c.BlobPacked += lab.PackedLen(b)
	}
	for _, r := range s.All() {
		c.Comps++
		c.CompBytes += int64(len(r.Marshal()))
		if r.Derived() {
			c.DerivedComps++
		} else {
			c.RawComps++
		}
	}
	c.Index = int64(c.Comps) * IndexPerObj
	rec, err := os.ReadDir(filepath.Join(dir, "recipes"))
	if err != nil {
		return c, err
	}
	for _, e := range rec {
		fi, err := e.Info()
		if err != nil {
			return c, err
		}
		c.Recipes++
		c.RecipeBytes += fi.Size()
	}
	return c, nil
}

// CDC is a content-defined-chunk store model used for the production baseline.
type CDC struct {
	Min, Tgt, Max int
	seen          map[[32]byte]bool
	Total         int64
	Chunks        int
}

// ProdCDC returns the production 16/64/256 KiB chunker model; FineCDC the 2/8/32 KiB one.
func ProdCDC() *CDC {
	return &CDC{Min: 16 << 10, Tgt: 64 << 10, Max: 256 << 10, seen: map[[32]byte]bool{}}
}
func FineCDC() *CDC {
	return &CDC{Min: 2 << 10, Tgt: 8 << 10, Max: 32 << 10, seen: map[[32]byte]bool{}}
}

// Add stores one object and returns its incremental cost (chunk refs + new chunks).
func (c *CDC) Add(data []byte) int64 {
	var inc int64
	for _, ch := range lab.ChunkScaled(data, c.Min, c.Tgt, c.Max) {
		h := sha256.Sum256(ch)
		inc += RootRef
		c.Chunks++
		if !c.seen[h] {
			c.seen[h] = true
			inc += lab.PackedLen(ch) + ObjOverhead
		}
	}
	c.Total += inc
	return inc
}
