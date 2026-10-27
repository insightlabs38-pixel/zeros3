// Package kinds is the finite in-code table of Z3-21 decoders plus the thin
// adapters that turn the existing CAR, SQLite, Git, Merkle and tar mechanisms
// into repz3 representations. A decoder reads only the serialized representation
// and the bounded source; an adapter (analysis side) may use any state it likes
// but hands storage nothing except a Bundle of real bytes.
//
// The package is the adapter layer: it may import the format packages (and,
// through gitz3, the laboratory); repz3 itself imports none of them.
package kinds

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"

	"zeros3-z3/lab"
	"zeros3-z3/repz3"
)

// Lookup is the kind table: a switch, so the serialized kind selects the decoder
// deterministically with no registration step.
func Lookup(k repz3.Kind) (repz3.Decoder, bool) {
	switch k {
	case repz3.KindCAR:
		return carDecoder{}, true
	case repz3.KindSQLite:
		return sqliteDecoder{}, true
	case repz3.KindGit:
		return gitDecoder{}, true
	case repz3.KindMerkle:
		return merkleDecoder{}, true
	case repz3.KindGzip:
		return gzipDecoder{}, true
	case repz3.KindZstd:
		return zstdDecoder{}, true
	case repz3.KindModel:
		return modelDecoder{}, true
	case repz3.KindSST:
		return sstDecoder{}, true
	}
	return nil, false
}

// NewEnv returns a reconstruction environment over src with the kind table.
func NewEnv(src repz3.Source) *repz3.Env { return &repz3.Env{Src: src, Table: Lookup} }

// NewEnvLimits is NewEnv under explicit limits (a depth-4 representation needs MaxDepth 4).
func NewEnvLimits(src repz3.Source, lim repz3.Limits) *repz3.Env {
	return &repz3.Env{Src: src, Table: Lookup, Limits: lim}
}

// Bundle is what an analyzer hands to storage for one representation: the
// serialized object, the component and index objects it needs that analysis
// produced, and the child bundles of its DERIVED members. Components already in
// the store are simply deduplicated by Persist.
type Bundle struct {
	Rep      *repz3.Rep
	Raw      []byte // Rep.Marshal()
	ID       repz3.SHA
	Objects  [][]byte
	Children []*Bundle
}

// NewBundle serialises rep and bundles it with the component objects it needs and the
// bundles of its DERIVED children (for adapters outside this package, e.g. the DAG lab's
// container splitters).
func NewBundle(rep *repz3.Rep, objs [][]byte, children ...*Bundle) (*Bundle, error) {
	return newBundle(rep, objs, children...)
}

func newBundle(rep *repz3.Rep, objs [][]byte, children ...*Bundle) (*Bundle, error) {
	raw, err := rep.Marshal()
	if err != nil {
		return nil, err
	}
	return &Bundle{Rep: rep, Raw: raw, ID: sha256.Sum256(raw), Objects: objs, Children: children}, nil
}

// Persist puts the bundle's objects (children first, the representation object
// last) and returns the representation's digest and the objects that were new.
func (b *Bundle) Persist(st *repz3.MemStore) (repz3.SHA, []repz3.SHA, error) {
	var added []repz3.SHA
	var put func(b *Bundle)
	put = func(b *Bundle) {
		for _, c := range b.Children {
			put(c)
		}
		for _, o := range b.Objects {
			if id, isNew := st.Put(o); isNew {
				added = append(added, id)
			}
		}
		if id, isNew := st.Put(b.Raw); isNew {
			added = append(added, id)
		}
	}
	put(b)
	return b.ID, added, nil
}

// Prove persists the bundle into a scratch copy of base (nil = empty), reconstructs
// it through the serialized form only and compares length, SHA-256 and bytes with
// want. It also checks the GC gate: every object the reconstruction read is in the
// set the contract's reachability keeps alive. It is the analysis-time exactness
// check; a failure means "use CDC instead".
func Prove(base *repz3.MemStore, b *Bundle, want []byte) error {
	return ProveLimits(base, b, want, repz3.Limits{})
}

// ProveLimits is Prove under explicit limits.
func ProveLimits(base *repz3.MemStore, b *Bundle, want []byte, lim repz3.Limits) error {
	scratch := repz3.NewMemStore()
	if base != nil {
		scratch = base.Clone()
	}
	if _, _, err := b.Persist(scratch); err != nil {
		return err
	}
	scratch.ResetReads()
	var out bytes.Buffer
	env := NewEnvLimits(scratch, lim)
	n, sum, err := env.Reconstruct(b.ID, &out)
	if err != nil {
		return err
	}
	if n != int64(len(want)) || sum != sha256.Sum256(want) || !bytes.Equal(out.Bytes(), want) {
		return errors.New("kinds: reconstruction differs from the input")
	}
	_, read := scratch.Touched()
	reach, err := env.Reachable(b.ID)
	if err != nil {
		return err
	}
	for id := range read {
		if _, ok := reach.Role[id]; !ok {
			return fmt.Errorf("kinds: reconstruction read %x, which the contract would not keep alive", id[:6])
		}
	}
	return nil
}

// CDC is the baseline and the fallback expressed in the same contract: the data
// cut with production CDC, each chunk a STORED member of a KindConcat
// representation. Its cost is real serialized bytes like every other candidate.
func CDC(data []byte) (*Bundle, error) {
	rep := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(data))}
	var objs [][]byte
	err := lab.ChunkCDC(bytes.NewReader(data), func(_ int64, c []byte) error {
		cp := append([]byte(nil), c...)
		rep.Members = append(rep.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(cp), Size: int64(len(cp))})
		objs = append(objs, cp)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return newBundle(rep, objs)
}

// Admit is the analysis-time choice: the candidate when it proves exact and is
// cheaper than CDC against the current store at the given per-object overhead,
// otherwise CDC. Decoding never makes this choice.
func Admit(st *repz3.MemStore, cand *Bundle, data []byte, perObject int64) (chosen *Bundle, usedCandidate bool, err error) {
	cdc, err := CDC(data)
	if err != nil {
		return nil, false, err
	}
	if cand == nil || Prove(st, cand, data) != nil {
		return cdc, false, nil
	}
	cc, err := dryCost(st, cand)
	if err != nil {
		return cdc, false, nil
	}
	dc, err := dryCost(st, cdc)
	if err != nil {
		return nil, false, err
	}
	if cc.Total(perObject) < dc.Total(perObject) {
		return cand, true, nil
	}
	return cdc, false, nil
}

func dryCost(st *repz3.MemStore, b *Bundle) (repz3.Cost, error) {
	c := st.Clone()
	id, added, err := b.Persist(c)
	if err != nil {
		return repz3.Cost{}, err
	}
	return NewEnv(c).Account(id, added)
}

// Commit persists the chosen bundle into st, makes its representation a live root
// of the native accelerator cat (nil = none; what a real store does at put time)
// and returns its digest and cost.
func Commit(st *repz3.MemStore, cat *repz3.Catalog, b *Bundle) (repz3.SHA, repz3.Cost, error) {
	id, added, err := b.Persist(st)
	if err != nil {
		return id, repz3.Cost{}, err
	}
	if cat != nil {
		if err := cat.Ingest(id); err != nil {
			return id, repz3.Cost{}, err
		}
	}
	c, err := NewEnv(st).Account(id, added)
	return id, c, err
}

func sha(b []byte) repz3.SHA { return sha256.Sum256(b) }
