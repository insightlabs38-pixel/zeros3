// Package b6cost is the B6 ranking cost model: a content-addressed object store charged with
// the production pack rule (DEFLATE kept only if it saves 1/16), 96 B per new object and 36 B
// per reference (the B1/B3/B5 convention). It is a laboratory estimator, not a store.
package b6cost

import (
	"crypto/sha256"
	"sort"
	"zeros3-z3/lab"
)

const (
	ObjectBytes = 96
	RefBytes    = 36
)

// Chunker names a splitting policy.
type Chunker struct {
	Name           string
	Min, Tgt, Max  int // zero Tgt = whole object
}

var (
	Whole    = Chunker{Name: "whole"}
	ProdCDC  = Chunker{Name: "cdc16/64/256", Min: 16 << 10, Tgt: 64 << 10, Max: 256 << 10}
	FineCDC  = Chunker{Name: "cdc1/4/16", Min: 1 << 10, Tgt: 4 << 10, Max: 16 << 10}
	Fine2CDC = Chunker{Name: "cdc256/1K/4K", Min: 256, Tgt: 1 << 10, Max: 4 << 10}
)

func (c Chunker) Split(b []byte) [][]byte {
	if c.Tgt == 0 || len(b) == 0 {
		return [][]byte{b}
	}
	return lab.ChunkScaled(b, c.Min, c.Tgt, c.Max)
}

// Store accumulates cost over a retained history.
type Store struct {
	seen map[[32]byte]struct{}
	Objs int
	Pack int64 // packed bytes of objects + object records
	Refs int
	Raw  int64
	Own  map[string]int64 // optional extra charges (recipes etc.)
}

func NewStore() *Store { return &Store{seen: map[[32]byte]struct{}{}, Own: map[string]int64{}} }

// Add stores b under chunker c and returns the incremental charge (objects + refs).
func (s *Store) Add(c Chunker, b []byte) int64 {
	var inc int64
	s.Raw += int64(len(b))
	for _, ch := range c.Split(b) {
		h := sha256.Sum256(ch)
		s.Refs++
		inc += RefBytes
		if _, ok := s.seen[h]; ok {
			continue
		}
		s.seen[h] = struct{}{}
		s.Objs++
		inc += ObjectBytes + lab.PackedLen(ch)
	}
	s.Pack += inc
	return inc
}

// Charge adds an explicit cost (recipes, indexes) under a label.
func (s *Store) Charge(label string, n int64) { s.Own[label] += n; s.Pack += n }

func (s *Store) Total() int64 { return s.Pack }

func SortedKeys(m map[string]int64) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}
