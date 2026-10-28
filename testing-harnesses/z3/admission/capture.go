package admission

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"
	"time"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// Capture prices only objects in a proved serialized dependency closure. The
// all-candidate generation store is not policy state; missing dependencies are
// charged from scratch during replay even if generation used an earlier root.
type Observation struct {
	Case, Stage, Class, Variant string
	Data                        []byte
	Store                       *repz3.MemStore
	Reach                       *repz3.Reach
}

type Capture struct {
	Case, Stage, Class string
	Observe            func(Observation) error
	Corpus             Corpus
	Store              *repz3.MemStore
	index              map[repz3.SHA]int
	limits             repz3.Limits
}

func NewCapture() *Capture {
	return &Capture{Corpus: Corpus{Version: 1, ObjectOverhead: 64}, Store: repz3.NewMemStore(), index: map[repz3.SHA]int{}, limits: repz3.Limits{MaxDepth: 4, MaxWhole: 128 << 20, MaxObjects: 1 << 20, MaxRepBytes: 32 << 20}}
}
func (c *Capture) ResetStore() { c.Store = repz3.NewMemStore() }
func (c *Capture) Variant(name string, data []byte, b *kinds.Bundle, start time.Time) (Candidate, error) {
	var out Candidate
	root, added, e := b.Persist(c.Store)
	if e != nil {
		return out, e
	}
	env := kinds.NewEnvLimits(c.Store, c.limits)
	if _, e = env.Account(root, added); e != nil {
		return out, e
	}
	c.Store.ResetReads()
	var rebuilt bytes.Buffer
	n, sum, e := env.Reconstruct(root, &rebuilt)
	if e != nil {
		return out, e
	}
	want := sha256.Sum256(data)
	if n != int64(len(data)) || sum != want || !bytes.Equal(rebuilt.Bytes(), data) {
		return out, fmt.Errorf("admission capture: serialized proof mismatch")
	}
	_, reads := c.Store.Touched()
	var readBytes int64
	for _, n := range reads {
		readBytes += n
	}
	reach, e := env.Reachable(root)
	if e != nil {
		return out, e
	}
	var sample []string
	for _, id := range reach.Order {
		role := reach.Role[id]
		index, ok := c.index[id]
		if !ok {
			raw := c.Store.Bytes(id)
			if sha256.Sum256(raw) != id {
				return out, fmt.Errorf("admission capture: addressed object changed")
			}
			index = len(c.Corpus.Objects)
			c.index[id] = index
			c.Corpus.Objects = append(c.Corpus.Objects, Object{SHA: fmt.Sprintf("%x", id), Raw: int64(len(raw)), Packed: lab.PackedSize(raw), Role: fmt.Sprint(role)})
		}
		out.Objects = append(out.Objects, index)
		if role != repz3.DepRep && role != repz3.DepIndex {
			sample = append(sample, fmt.Sprintf("%x", id))
		}
	}
	sort.Ints(out.Objects)
	out.Name = name
	out.Root = fmt.Sprintf("%x", root)
	out.ProofSHA = fmt.Sprintf("%x", want)
	out.Sample = Sample(sample)
	if c.Observe != nil {
		if e := c.Observe(Observation{c.Case, c.Stage, c.Class, name, data, c.Store, reach}); e != nil {
			return out, e
		}
	}
	out.AnalysisNS = time.Since(start).Nanoseconds()
	out.ReadBytes = readBytes
	return out, nil
}
func CDC(data []byte) (*kinds.Bundle, error) {
	r := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(data))}
	var objects [][]byte
	e := lab.ChunkCDC(bytes.NewReader(data), func(_ int64, b []byte) error {
		copyData := append([]byte(nil), b...)
		objects = append(objects, copyData)
		r.Members = append(r.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(copyData), Size: int64(len(copyData))})
		return nil
	})
	if e != nil {
		return nil, e
	}
	return kinds.NewBundle(r, objects)
}
