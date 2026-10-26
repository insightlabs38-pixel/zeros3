package dagz3

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"runtime"
	"time"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// tiny persists a one-component concat representation of a 16-byte payload that depends only
// on i, and returns its digest and payload.
func tiny(st *repz3.MemStore, i int) (repz3.SHA, []byte) {
	h := sha256.Sum256(binary.LittleEndian.AppendUint64(nil, uint64(i)))
	p := h[:16]
	cid, _ := st.Put(p)
	rep := &repz3.Rep{Kind: repz3.KindConcat, Size: 16, Members: []repz3.Member{{Role: repz3.Stored, SHA: cid, Size: 16}}}
	raw, _ := rep.Marshal()
	id, _ := st.Put(raw)
	return id, p
}

// WideRoot persists a root representation with n DERIVED members, each over its own tiny
// child representation: width, not depth.
func WideRoot(st *repz3.MemStore, n int) (repz3.SHA, []byte, error) {
	root := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(16 * n)}
	var out []byte
	for i := 0; i < n; i++ {
		cid, p := tiny(st, i)
		root.Members = append(root.Members, repz3.Member{Role: repz3.Derived, SHA: sha256.Sum256(p), Size: 16, Child: cid})
		out = append(out, p...)
	}
	raw, err := root.Marshal()
	if err != nil {
		return repz3.SHA{}, nil, err
	}
	id, _ := st.Put(raw)
	return id, out, nil
}

// FanIn persists one child representation and n distinct parents that derive it.
func FanIn(st *repz3.MemStore, n int) (child repz3.SHA, roots []repz3.SHA, err error) {
	child, p := tiny(st, 1<<20)
	for i := 0; i < n; i++ {
		head := binary.LittleEndian.AppendUint64(nil, uint64(i))
		hid, _ := st.Put(head)
		framed := p
		rep := &repz3.Rep{Kind: repz3.KindConcat, Size: 8 + 16, Members: []repz3.Member{
			{Role: repz3.Stored, SHA: hid, Size: 8},
			{Role: repz3.Derived, SHA: sha256.Sum256(framed), Size: 16, Child: child}}}
		raw, err := rep.Marshal()
		if err != nil {
			return child, nil, err
		}
		id, _ := st.Put(raw)
		roots = append(roots, id)
	}
	return child, roots, nil
}

// ScaleStats is one wide or fan-in measurement.
type ScaleStats struct {
	N                  int
	Objects            int
	RootBytes          int64
	Marshal, Unmarshal time.Duration
	Reach, SumReach    time.Duration
	Reconstruct        time.Duration
	ReachAlloc         uint64 // bytes allocated by the union walk
	ReachReads         int    // source reads of the union walk
	SumReads           int    // source reads of per-root walks
}

func alloc() uint64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}

// MeasureWide builds a root with n tiny children and times each stage.
func MeasureWide(n int) (ScaleStats, error) {
	st := repz3.NewMemStore()
	id, want, err := WideRoot(st, n)
	if err != nil {
		return ScaleStats{}, err
	}
	env := kinds.NewEnvLimits(st, Lim)
	s := ScaleStats{N: n, Objects: st.Len()}
	s.RootBytes, _ = st.Size(id)
	rep, err := env.LoadRep(id)
	if err != nil {
		return s, err
	}
	t := time.Now()
	raw, _ := rep.Marshal()
	s.Marshal = time.Since(t)
	t = time.Now()
	if _, err := repz3.Unmarshal(raw, repz3.Limits{}); err != nil {
		return s, err
	}
	s.Unmarshal = time.Since(t)
	st.ResetReads()
	a0 := alloc()
	t = time.Now()
	reach, err := env.Reachable(id)
	if err != nil {
		return s, err
	}
	s.Reach = time.Since(t)
	s.ReachAlloc = alloc() - a0
	s.ReachReads, _ = st.Touched()
	if len(reach.Order) != st.Len() {
		return s, io.ErrUnexpectedEOF
	}
	t = time.Now()
	if err := Exact(env, id, want); err != nil {
		return s, err
	}
	s.Reconstruct = time.Since(t)
	return s, nil
}

// MeasureFanIn builds one child with n parents and compares the union walk with n separate walks.
func MeasureFanIn(n int) (ScaleStats, error) {
	st := repz3.NewMemStore()
	_, roots, err := FanIn(st, n)
	if err != nil {
		return ScaleStats{}, err
	}
	env := kinds.NewEnvLimits(st, Lim)
	s := ScaleStats{N: n, Objects: st.Len()}
	st.ResetReads()
	a0 := alloc()
	t := time.Now()
	all, err := env.ReachableAll(roots)
	if err != nil {
		return s, err
	}
	s.Reach = time.Since(t)
	s.ReachAlloc = alloc() - a0
	s.ReachReads, _ = st.Touched()
	if len(all.Order) != st.Len() {
		return s, io.ErrUnexpectedEOF
	}
	st.ResetReads()
	t = time.Now()
	for _, r := range roots {
		if _, err := env.Reachable(r); err != nil {
			return s, err
		}
	}
	s.SumReach = time.Since(t)
	s.SumReads, _ = st.Touched()
	return s, nil
}
