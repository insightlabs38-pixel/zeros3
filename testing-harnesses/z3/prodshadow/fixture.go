package prodshadow

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// Fixture is a Lab fed from a workload chain: stage i is PUT through the real S3
// path under Key and its shadow is built from the chain's analyser.
type Fixture struct {
	L     *Lab
	Chain *kinds.Chain
	Mem   *repz3.MemStore
	Cat   *repz3.Catalog
	Key   string
	Reps  []repz3.SHA // representation root per built stage (zero if none)
	Roots [][32]byte  // published root id per built stage
}

func NewFixture(b *Binaries, base string, ch *kinds.Chain, opt Options) (*Fixture, error) {
	l, err := NewLab(b, base, opt)
	if err != nil {
		return nil, err
	}
	if err := l.Start(); err != nil {
		return nil, err
	}
	f := &Fixture{L: l, Chain: ch, Mem: repz3.NewMemStore(), Key: "obj"}
	f.Cat = kinds.NewCatalog(f.Mem)
	return f, nil
}

// Put commits stage i authoritatively and, if shadow, builds and publishes its shadow.
func (f *Fixture) Put(i int, shadow bool, h Hooks) (*Staged, error) {
	data := f.Chain.Stages[i].Data
	if err := f.L.Br.S3Put(f.Key, data); err != nil {
		return nil, err
	}
	for len(f.Reps) <= i {
		f.Reps = append(f.Reps, repz3.SHA{})
		f.Roots = append(f.Roots, [32]byte{})
	}
	if !shadow {
		return nil, nil
	}
	return f.Shadow(i, h)
}

// Shadow builds the shadow of stage i (already committed).
func (f *Fixture) Shadow(i int, h Hooks) (*Staged, error) {
	data := f.Chain.Stages[i].Data
	cand, err := f.Chain.Encode(f.Mem, f.Cat, data)
	if err != nil {
		return nil, err
	}
	if err := kinds.Prove(f.Mem, cand, data); err != nil {
		return nil, err
	}
	if _, _, err := kinds.Commit(f.Mem, f.Cat, cand); err != nil {
		return nil, err
	}
	st, root, err := f.L.ShadowRevision(f.Key, data, cand, h)
	if err == nil {
		f.Reps[i], f.Roots[i] = cand.ID, root
	}
	return st, err
}

// ShadowOnlyChunks lists live shadow chunks that the authoritative CDC manifest of
// the current object does not reference, in digest order.
func (f *Fixture) ShadowOnlyChunks(live *Live) ([][32]byte, error) {
	cdc, err := f.CDCChunks()
	if err != nil {
		return nil, err
	}
	var out [][32]byte
	for c := range live.Chunks {
		if !cdc[c] {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i][:]) < string(out[j][:]) })
	return out, nil
}

// CDCChunks is the chunk set of the current authoritative object.
func (f *Fixture) CDCChunks() (map[[32]byte]bool, error) {
	ws, err := f.L.Br.Touch(f.Key, 0, 1<<40)
	if err != nil {
		return nil, err
	}
	m := map[[32]byte]bool{}
	for _, w := range ws {
		b, err := hex.DecodeString(w.SHA)
		if err != nil || len(b) != 32 {
			return nil, fmt.Errorf("bad sha %q", w.SHA)
		}
		var s [32]byte
		copy(s[:], b)
		m[s] = true
	}
	return m, nil
}

// ChunkPath is a loose chunk's file path in the store.
func ChunkPath(storeDir string, sha [32]byte) string {
	h := hex.EncodeToString(sha[:])
	return filepath.Join(storeDir, "chunks", h[0:2], h[2:4], h)
}

func shaOf(b []byte) [32]byte { return sha256.Sum256(b) }
