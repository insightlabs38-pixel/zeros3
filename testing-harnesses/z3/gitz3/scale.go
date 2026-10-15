package gitz3

import (
	"bytes"
	"fmt"
	"io"
	"runtime"
	"sync"
	"time"

	"zeros3-z3/lab"
)

// WriteScaleStream emits a fast-import stream for the scaling families.
//
//	size   nFiles text files of `size` bytes, then revs commits each editing ~10% of them
//	count  the same with tiny files (size is the mean)
//	depth  one `size`-byte file edited once per commit for revs commits (delta chains)
func WriteScaleStream(w io.Writer, kind string, nFiles, size, revs int, seed uint64) error {
	r := NewRng(seed)
	mark := 1
	files := make([][]byte, nFiles)
	emitBlob := func(b []byte) int {
		m := mark
		mark++
		fmt.Fprintf(w, "blob\nmark :%d\ndata %d\n", m, len(b))
		w.Write(b)
		fmt.Fprint(w, "\n")
		return m
	}
	name := func(i int) string { return fmt.Sprintf("d%03d/f%06d.txt", i%251, i) }
	prev := 0
	commit := func(n int, mods map[int]int) {
		cm := mark
		mark++
		ts := 1700000000 + 3600*n
		msg := fmt.Sprintf("rev %d\n", n)
		fmt.Fprintf(w, "commit refs/heads/main\nmark :%d\nauthor Z3 Lab <z3@example.invalid> %d +0000\ncommitter Z3 Lab <z3@example.invalid> %d +0000\ndata %d\n%s", cm, ts, ts, len(msg), msg)
		if prev != 0 {
			fmt.Fprintf(w, "from :%d\n", prev)
		}
		for i, m := range mods {
			fmt.Fprintf(w, "M 100644 :%d %s\n", m, name(i))
		}
		fmt.Fprint(w, "\n")
		prev = cm
	}
	if kind == "depth" {
		files = make([][]byte, 1)
		files[0] = TextFile(r, size)
		commit(0, map[int]int{0: emitBlob(files[0])})
		for n := 1; n <= revs; n++ {
			files[0] = EditText(r, files[0], float64(r.Intn(100))/100, 1)
			commit(n, map[int]int{0: emitBlob(files[0])})
		}
		_, err := fmt.Fprint(w, "done\n")
		return err
	}
	mods := map[int]int{}
	for i := range files {
		sz := size
		if kind == "count" {
			sz = size/2 + r.Intn(size)
		}
		files[i] = TextFile(r, sz)
		mods[i] = emitBlob(files[i])
		if len(mods) >= 2000 { // keep commit lines bounded: a first commit per 2000 files
			commit(0, mods)
			mods = map[int]int{}
		}
	}
	commit(0, mods)
	for n := 1; n <= revs; n++ {
		mods = map[int]int{}
		for k := 0; k < max(1, nFiles/10); k++ {
			i := r.Intn(nFiles)
			files[i] = EditText(r, files[i], float64(r.Intn(100))/100, 1)
			mods[i] = emitBlob(files[i])
		}
		commit(n, mods)
	}
	_, err := fmt.Fprint(w, "done\n")
	return err
}

// ScalePoint is one measured pack.
type ScalePoint struct {
	Label                          string
	PackBytes, IdxBytes            int64
	Entries, Deltas, MaxDepth      int
	ResolvedBytes, InflatedB       int64
	ParseMs, ResolveMs             float64
	AnalyzeMs, SeqPlanMs           float64
	SetPlanMs, RebuildMs           float64
	CDCMs                          float64
	PeakParseMiB, PeakResolveMiB   float64
	PeakAnalyzeMiB, PeakRebuildMiB float64
	ExactOK                        bool
	NewBytesSet, NewBytesCDC       int64
	RecipeSet                      int64
	Err                            string
}

type heapSampler struct {
	stop chan struct{}
	wg   sync.WaitGroup
	peak uint64
}

func startSampler() *heapSampler {
	runtime.GC()
	s := &heapSampler{stop: make(chan struct{})}
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	s.peak = base.HeapAlloc
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(2 * time.Millisecond)
		defer t.Stop()
		var m runtime.MemStats
		for {
			select {
			case <-s.stop:
				return
			case <-t.C:
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > s.peak {
					s.peak = m.HeapAlloc
				}
			}
		}
	}()
	return s
}

// finish returns the peak heap growth over the baseline in MiB.
func (s *heapSampler) finish(base uint64) float64 {
	close(s.stop)
	s.wg.Wait()
	if s.peak < base {
		return 0
	}
	return float64(s.peak-base) / (1 << 20)
}

func heapNow() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

func since(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

// MeasureScale ingests one pack/idx pair through every phase, timing each and
// sampling peak heap growth (the input buffers are included in the baseline).
func MeasureScale(label string, a Algo, pack, idx []byte, doIR bool) ScalePoint {
	sp := ScalePoint{Label: label, PackBytes: int64(len(pack)), IdxBytes: int64(len(idx))}
	ReplayMemoOff = true
	defer func() { ReplayMemoOff = false }()
	base := heapNow()
	smp := startSampler()
	t0 := time.Now()
	p, err := ParsePack(pack, a, Limits{})
	sp.ParseMs = since(t0)
	sp.PeakParseMiB = smp.finish(base)
	if err != nil {
		sp.Err = err.Error()
		return sp
	}
	smp = startSampler()
	t0 = time.Now()
	rs, err := p.Resolve()
	sp.ResolveMs = since(t0)
	sp.PeakResolveMiB = smp.finish(base)
	if err != nil {
		sp.Err = err.Error()
		return sp
	}
	sp.Entries, sp.Deltas, sp.MaxDepth = len(p.Entries), rs.Delta, rs.MaxDepth
	sp.InflatedB = rs.Inflated
	for i := range p.Entries {
		sp.ResolvedBytes += p.Entries[i].RSize
	}
	t0 = time.Now()
	_ = lab.Reps // CDC time for reference
	cdc, _ := baseline("cdc", File{Name: "x.pack", Kind: "pack", Data: pack})
	sp.CDCMs = since(t0)
	if cdc != nil {
		sp.NewBytesCDC = int64(len(pack))
	}
	if !doIR {
		return sp
	}
	smp = startSampler()
	t0 = time.Now()
	an, err := AnalyzePack(p)
	sp.AnalyzeMs = since(t0)
	sp.PeakAnalyzeMiB = smp.finish(base)
	if err != nil {
		sp.Err = err.Error()
		return sp
	}
	o := RepOpts{Mode: "plain"} // plaintext tier everywhere: the replay path is what is being measured
	t0 = time.Now()
	an.SeqPlan(o)
	sp.SeqPlanMs = since(t0)
	t0 = time.Now()
	pl, ss := an.SetPlan(o)
	sp.SetPlanMs = since(t0)
	ac := lab.NewAcct(96, false)
	acc, err := ac.Add(pl)
	if err != nil {
		sp.Err = err.Error()
		return sp
	}
	sp.NewBytesSet, sp.RecipeSet = acc.NewBytes, acc.Layouts["table"]+pl.MetaBytes
	_ = ss
	smp = startSampler()
	t0 = time.Now()
	out := &EqWriter{Want: pack} // the rebuilt pack is compared as it streams out, never held
	err = pl.Rebuild(out, ac.Get)
	sp.RebuildMs = since(t0)
	sp.PeakRebuildMiB = smp.finish(base)
	sp.ExactOK = err == nil && out.Equal()
	if err != nil {
		sp.Err = err.Error()
	}
	return sp
}

// EqWriter compares the bytes written to it against Want as they arrive, so exact
// reconstruction of a large object is checked without a second copy of it.
type EqWriter struct {
	Want []byte
	n    int
	bad  bool
}

func (e *EqWriter) Write(b []byte) (int, error) {
	if e.n+len(b) > len(e.Want) || !bytes.Equal(b, e.Want[e.n:e.n+len(b)]) {
		e.bad = true
	}
	e.n += len(b)
	return len(b), nil
}

// Equal reports whether exactly Want, in full, was written.
func (e *EqWriter) Equal() bool { return !e.bad && e.n == len(e.Want) }
