package dcore

import (
	"encoding/binary"
	"hash/crc32"
	"io"
	"slices"
	"sort"
	"sync"
)

// Posting is (feature, object id): 16 bytes resident, 12 serialized by B1.
type Posting struct {
	Feature uint64
	ID      uint32
}

func cmpPosting(a, b Posting) int {
	switch {
	case a.Feature < b.Feature:
		return -1
	case a.Feature > b.Feature:
		return 1
	case a.ID < b.ID:
		return -1
	case a.ID > b.ID:
		return 1
	}
	return 0
}

// SortReflect is the B1 baseline: sort.Slice (reflection-based swapper).
func SortReflect(p []Posting) {
	sort.Slice(p, func(i, j int) bool { return cmpPosting(p[i], p[j]) < 0 })
}

// SortFunc is the generic comparison sort.
func SortFunc(p []Posting) { slices.SortFunc(p, cmpPosting) }

// SortBuckets sorts by (Feature, ID) with one scatter pass on the top 16
// feature bits into a scratch slice followed by cache-resident local sorts,
// parallel across workers. It returns the sorted slice, which aliases a new
// allocation; the input contents are unspecified afterwards.
func SortBuckets(p []Posting, workers int) []Posting {
	n := len(p)
	if n < 1<<12 || workers < 1 {
		SortFunc(p)
		return p
	}
	var cnt [65537]int
	for i := range p {
		cnt[p[i].Feature>>48+1]++
	}
	for i := 1; i < len(cnt); i++ {
		cnt[i] += cnt[i-1]
	}
	out := make([]Posting, n)
	pos := cnt // copy
	for i := range p {
		b := p[i].Feature >> 48
		out[pos[b]] = p[i]
		pos[b]++
	}
	var wg sync.WaitGroup
	jobs := make(chan int, 256)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range jobs {
				if lo, hi := cnt[b], cnt[b+1]; hi-lo > 1 {
					slices.SortFunc(out[lo:hi], cmpPosting)
				}
			}
		}()
	}
	for b := 0; b < 65536; b++ {
		jobs <- b
	}
	close(jobs)
	wg.Wait()
	return out
}

// --- immutable sorted run ("ZSR1") -----------------------------------------
//
// Layout: magic[4] | generation u64 | count u64 | dir[65537] u32 | entries
// count x (feature low 48 bits LE 6B, id u32 LE) | crc32c of everything before.
// dir[b] is the index of the first entry whose feature>>48 == b, so a lookup is
// one directory read plus ONE ReadAt of the (small) bucket. 10 bytes per
// posting versus 12 for the flat table. The run is derived state: a corrupt or
// missing run only lowers recall; every candidate is re-verified against real
// bytes by the delta codec, and no deletion decision ever consults a run.

const (
	runMagic  = "ZSR1"
	runHeader = 4 + 8 + 8
	runDirLen = 65537 * 4
	runEntry  = 10
)

// ErrRunCorrupt reports a run that failed validation.
var ErrRunCorrupt = errCorrupt{}

type errCorrupt struct{}

func (errCorrupt) Error() string { return "dcore: corrupt similarity run" }

// WriteRun writes sorted postings (already sorted by (Feature, ID), unique) as a run.
func WriteRun(w io.Writer, sorted []Posting, generation uint64) (int64, error) {
	var counts [65536]uint32
	for i := range sorted {
		counts[sorted[i].Feature>>48]++
	}
	i := 0
	return WriteRunStream(w, int64(len(sorted)), &counts, generation, func() (Posting, bool) {
		if i >= len(sorted) {
			return Posting{}, false
		}
		i++
		return sorted[i-1], true
	})
}

// WriteRunStream writes a run of exactly count postings produced in sorted
// order by next. counts[b] must be the number of postings whose feature>>48
// equals b (a first pass computes it); a mismatch is an error.
func WriteRunStream(w io.Writer, count int64, counts *[65536]uint32, generation uint64, next func() (Posting, bool)) (int64, error) {
	if count < 0 || count >= 1<<32 {
		return 0, ErrTooLarge
	}
	h := crc32.New(crcTab)
	mw := io.MultiWriter(w, h)
	var hdr [runHeader]byte
	copy(hdr[:], runMagic)
	binary.LittleEndian.PutUint64(hdr[4:], generation)
	binary.LittleEndian.PutUint64(hdr[12:], uint64(count))
	if _, err := mw.Write(hdr[:]); err != nil {
		return 0, err
	}
	dir := make([]byte, runDirLen)
	var acc uint64
	for b := 0; b < 65536; b++ {
		binary.LittleEndian.PutUint32(dir[b*4:], uint32(acc))
		acc += uint64(counts[b])
	}
	if int64(acc) != count {
		return 0, ErrParams
	}
	binary.LittleEndian.PutUint32(dir[65536*4:], uint32(acc))
	if _, err := mw.Write(dir); err != nil {
		return 0, err
	}
	buf := make([]byte, 0, 1<<16)
	var n int64
	var prev Posting
	for {
		p, ok := next()
		if !ok {
			break
		}
		if n > 0 && cmpPosting(prev, p) >= 0 {
			return 0, ErrParams
		}
		prev = p
		n++
		var e [runEntry]byte
		for k := 0; k < 6; k++ {
			e[k] = byte(p.Feature >> (8 * k))
		}
		binary.LittleEndian.PutUint32(e[6:], p.ID)
		buf = append(buf, e[:]...)
		if len(buf) >= 1<<16-runEntry {
			if _, err := mw.Write(buf); err != nil {
				return 0, err
			}
			buf = buf[:0]
		}
	}
	if n != count {
		return 0, ErrParams
	}
	if len(buf) > 0 {
		if _, err := mw.Write(buf); err != nil {
			return 0, err
		}
	}
	var sum [4]byte
	binary.LittleEndian.PutUint32(sum[:], h.Sum32())
	if _, err := w.Write(sum[:]); err != nil {
		return 0, err
	}
	return runHeader + runDirLen + count*runEntry + 4, nil
}

// Run is an opened immutable run. Lookups are safe for concurrent use.
type Run struct {
	r     io.ReaderAt
	size  int64
	Gen   uint64
	Count int64
	dir   []uint32
	base  int64
}

// OpenRun validates the header, directory and total size. verify additionally
// streams the whole file against its CRC-32C.
func OpenRun(r io.ReaderAt, size int64, verify bool) (*Run, error) {
	if size < runHeader+runDirLen+4 {
		return nil, ErrRunCorrupt
	}
	buf := make([]byte, runHeader+runDirLen)
	if _, err := readFull(r, buf, 0); err != nil || string(buf[:4]) != runMagic {
		return nil, ErrRunCorrupt
	}
	run := &Run{r: r, size: size, Gen: binary.LittleEndian.Uint64(buf[4:]), Count: int64(binary.LittleEndian.Uint64(buf[12:]))}
	if run.Count < 0 || run.Count >= 1<<32 || size != runHeader+runDirLen+run.Count*runEntry+4 {
		return nil, ErrRunCorrupt
	}
	run.dir = make([]uint32, 65537)
	prev := uint32(0)
	for i := range run.dir {
		v := binary.LittleEndian.Uint32(buf[runHeader+i*4:])
		if v < prev || int64(v) > run.Count {
			return nil, ErrRunCorrupt
		}
		run.dir[i], prev = v, v
	}
	if run.dir[65536] != uint32(run.Count) {
		return nil, ErrRunCorrupt
	}
	run.base = runHeader + runDirLen
	if verify {
		if err := run.Verify(); err != nil {
			return nil, err
		}
	}
	return run, nil
}

// Verify streams the file and checks the trailing CRC-32C.
func (r *Run) Verify() error {
	h := crc32.New(crcTab)
	buf := make([]byte, 1<<20)
	for off := int64(0); off < r.size-4; {
		n := min(int64(len(buf)), r.size-4-off)
		if _, err := readFull(r.r, buf[:n], off); err != nil {
			return err
		}
		h.Write(buf[:n])
		off += n
	}
	var sum [4]byte
	if _, err := readFull(r.r, sum[:], r.size-4); err != nil || binary.LittleEndian.Uint32(sum[:]) != h.Sum32() {
		return ErrRunCorrupt
	}
	return nil
}

func entryAt(b []byte, i int) (uint64, uint32) {
	var f uint64
	for k := 0; k < 6; k++ {
		f |= uint64(b[i*runEntry+k]) << (8 * k)
	}
	return f, binary.LittleEndian.Uint32(b[i*runEntry+6:])
}

// Lookup returns up to max of the NEWEST (highest) ids of the feature, ascending.
// The bucket is validated for order; a violation returns ErrRunCorrupt.
func (r *Run) Lookup(feature uint64, max int) ([]uint32, error) {
	b := feature >> 48
	lo, hi := int(r.dir[b]), int(r.dir[b+1])
	if lo == hi {
		return nil, nil
	}
	buf := make([]byte, (hi-lo)*runEntry)
	if _, err := readFull(r.r, buf, r.base+int64(lo)*runEntry); err != nil {
		return nil, err
	}
	want := feature & (1<<48 - 1)
	var pf uint64
	var pid uint32
	var out []uint32
	for i := 0; i < hi-lo; i++ {
		f, id := entryAt(buf, i)
		if i > 0 && (f < pf || (f == pf && id <= pid)) {
			return nil, ErrRunCorrupt
		}
		pf, pid = f, id
		if f == want {
			out = append(out, id)
		}
	}
	if max > 0 && len(out) > max {
		out = out[len(out)-max:]
	}
	return out, nil
}

// RunIter streams a run's postings in order (for compaction merges).
type RunIter struct {
	r      *Run
	i      int64
	bucket int
	buf    []byte
	bi, bn int
	Cur    Posting
	Err    error
}

func (r *Run) Iter() *RunIter { return &RunIter{r: r} }

// Next advances; false at end or error.
func (it *RunIter) Next() bool {
	if it.Err != nil || it.i >= it.r.Count {
		return false
	}
	if it.bi >= it.bn {
		n := min(int64(8192), it.r.Count-it.i)
		if int(n)*runEntry > cap(it.buf) {
			it.buf = make([]byte, int(n)*runEntry)
		}
		it.buf = it.buf[:int(n)*runEntry]
		if _, err := readFull(it.r.r, it.buf, it.r.base+it.i*runEntry); err != nil {
			it.Err = err
			return false
		}
		it.bi, it.bn = 0, int(n)
	}
	f, id := entryAt(it.buf, it.bi)
	for it.bucket < 65536 && int64(it.r.dir[it.bucket+1]) <= it.i {
		it.bucket++
	}
	it.Cur = Posting{uint64(it.bucket)<<48 | f, id}
	it.bi++
	it.i++
	return true
}
