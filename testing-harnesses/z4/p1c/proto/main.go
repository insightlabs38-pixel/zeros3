// p1cproto: Z4-P1C Checkpoint A end-to-end prototype of the small-object storage candidates.
//
// It is NOT the production code. Each PUT does the real work of the candidate layout: SHA-256 of the
// chunk(s), manifest serialization, the physical writes with every durability barrier, index insertion,
// a group-committed journal frame, and (after the run) read-back with re-verification of a sample plus
// an index rebuild ("open") from disk. Omitted: HTTP/SigV4, CDC (chunks are fixed 64 KiB pieces),
// multipart, GC/repack, history, the in-memory namespace. See ../../results/Z4_P1C_SMALL_OBJECT_STORAGE.md.
//
//	p1cproto -mode loose|pack|packinl|seg|seginl -dir DIR -c 8 -n 4000 -size 4096 [-segs 1] [-segmib 32]
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	nFsync, nCreate, nRename, nDirSync atomic.Int64
	nBytes                             atomic.Int64
)

func fsyncf(f *os.File) error { nFsync.Add(1); return f.Sync() }
func create(p string) (*os.File, error) {
	nCreate.Add(1)
	return os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
}
func rename(a, b string) error { nRename.Add(1); return os.Rename(a, b) }

// ---------------- round-based shared fsync (directory) ----------------
type dirSyncer struct {
	mu   sync.Mutex
	cur  *dround
	prev *dround
	path string
}
type dround struct {
	done chan struct{}
	err  error
}

func (s *dirSyncer) sync() error {
	s.mu.Lock()
	if s.cur != nil {
		r := s.cur
		s.mu.Unlock()
		<-r.done
		return r.err
	}
	r := &dround{done: make(chan struct{})}
	s.cur = r
	prev := s.prev
	s.prev = r
	s.mu.Unlock()
	if prev != nil {
		<-prev.done
	}
	s.mu.Lock()
	s.cur = nil
	s.mu.Unlock()
	nDirSync.Add(1)
	d, err := os.Open(s.path)
	if err == nil {
		err = fsyncf(d)
		d.Close()
	}
	r.err = err
	close(r.done)
	return err
}

// ---------------- generic group commit (leader hand-off, no timer) ----------------
type req struct {
	chunks []rec
	man    rec
	done   chan error
	lead   chan struct{}
	t0     time.Time
}
type rec struct {
	sum  [32]byte
	data []byte
	off  uint64 // filled by the stage that places it
	file string
}
type group struct {
	mu      sync.Mutex
	queue   []*req
	leading bool
	work    func(batch []*req) error
	batches atomic.Int64
	members atomic.Int64
}

func (g *group) submit(r *req) error {
	r.done = make(chan error, 1)
	r.lead = make(chan struct{}, 1)
	g.mu.Lock()
	g.queue = append(g.queue, r)
	if g.leading {
		g.mu.Unlock()
		select {
		case err := <-r.done:
			return err
		case <-r.lead:
		}
	} else {
		g.leading = true
		g.mu.Unlock()
	}
	g.mu.Lock()
	batch := g.queue
	g.queue = nil
	g.mu.Unlock()
	g.batches.Add(1)
	g.members.Add(int64(len(batch)))
	err := g.work(batch)
	for _, b := range batch {
		if b != r {
			b.done <- err
		}
	}
	g.mu.Lock()
	if len(g.queue) > 0 {
		g.queue[0].lead <- struct{}{} // hand leadership to the oldest waiter
	} else {
		g.leading = false
	}
	g.mu.Unlock()
	return err
}

// ---------------- index ----------------
type loc struct {
	file uint32
	off  uint64
	n    uint32
}
type index struct {
	sh [256]struct {
		mu sync.RWMutex
		m  map[[32]byte]loc
	}
}

func newIndex() *index {
	x := &index{}
	for i := range x.sh {
		x.sh[i].m = map[[32]byte]loc{}
	}
	return x
}
func (x *index) put(k [32]byte, l loc) {
	s := &x.sh[k[0]]
	s.mu.Lock()
	s.m[k] = l
	s.mu.Unlock()
}
func (x *index) get(k [32]byte) (loc, bool) {
	s := &x.sh[k[0]]
	s.mu.RLock()
	l, ok := s.m[k]
	s.mu.RUnlock()
	return l, ok
}

// ---------------- storage ----------------
type env struct {
	mode      string
	dir       string
	idx       *index
	midx      sync.Map // manifest uuid -> loc
	files     sync.Map // file id -> path
	fileSeq   atomic.Uint32
	journal   *os.File
	jgroup    *group
	jmu       sync.Mutex
	dsChunks  sync.Map // dir -> *dirSyncer
	dsMan     *dirSyncer
	dsPacks   *dirSyncer
	segs      []*segment
	segTarget int64
	packSeq   atomic.Uint64
}

func (e *env) dirSyncer(m *sync.Map, p string) *dirSyncer {
	v, _ := m.LoadOrStore(p, &dirSyncer{path: p})
	return v.(*dirSyncer)
}

const recHdr = 44 // pack-v1 record header
const idxEnt = 52

type segment struct {
	e       *env
	id      int
	g       *group
	f       *os.File
	path    string
	fid     uint32
	size    int64
	entries []entry
	h       hash.Hash
}
type entry struct {
	sum [32]byte
	off uint64
	n   uint32
}

func (e *env) newSegment(id int) (*segment, error) {
	s := &segment{e: e, id: id}
	s.g = &group{work: s.work}
	return s, s.open()
}

func (s *segment) open() error {
	e := s.e
	seq := e.packSeq.Add(1)
	s.path = filepath.Join(e.dir, "segments", fmt.Sprintf("%d-%d.open", s.id, seq))
	f, err := create(s.path)
	if err != nil {
		return err
	}
	hdr := make([]byte, 16)
	copy(hdr, "ZSPK")
	binary.LittleEndian.PutUint16(hdr[4:], 1)
	if _, err := f.Write(hdr); err != nil {
		return err
	}
	// the new segment's directory entry must be durable before an acknowledged op depends on it
	if err := fsyncf(f); err != nil {
		return err
	}
	if err := syncDirPath(filepath.Join(e.dir, "segments")); err != nil {
		return err
	}
	s.f, s.size, s.entries = f, 16, s.entries[:0]
	s.h = sha256.New()
	s.h.Write(hdr)
	s.fid = e.fileSeq.Add(1)
	e.files.Store(s.fid, s.path)
	return nil
}

func syncDirPath(p string) error {
	nDirSync.Add(1)
	d, err := os.Open(p)
	if err != nil {
		return err
	}
	defer d.Close()
	return fsyncf(d)
}

func putRec(buf []byte, r *rec) []byte {
	var h [recHdr]byte
	copy(h[:32], r.sum[:])
	binary.LittleEndian.PutUint32(h[32:], uint32(len(r.data)))
	binary.LittleEndian.PutUint32(h[36:], uint32(len(r.data)))
	return append(append(buf, h[:]...), r.data...)
}

// work: one group commit of the active segment: append, fsync, index, (roll)
func (s *segment) work(batch []*req) error {
	e := s.e
	var buf []byte
	pend := map[[32]byte]struct{}{}
	type placed struct {
		r   *rec
		off uint64
	}
	var pl []placed
	pos := s.size
	add := func(r *rec) {
		if _, ok := pend[r.sum]; ok && r.file != "man" {
			return
		}
		pend[r.sum] = struct{}{}
		start := len(buf)
		buf = putRec(buf, r)
		pl = append(pl, placed{r, uint64(pos) + uint64(start) + recHdr})
	}
	for _, q := range batch {
		for i := range q.chunks {
			if _, ok := e.idx.get(q.chunks[i].sum); ok {
				continue
			}
			add(&q.chunks[i])
		}
		if e.mode == "seginl" {
			q.man.file = "man"
			add(&q.man)
		}
	}
	if len(buf) > 0 {
		if _, err := s.f.WriteAt(buf, s.size); err != nil {
			return err
		}
		s.h.Write(buf)
		nBytes.Add(int64(len(buf)))
		if err := fsyncf(s.f); err != nil {
			return err
		}
		s.size += int64(len(buf))
		for _, p := range pl {
			s.entries = append(s.entries, entry{p.r.sum, p.off, uint32(len(p.r.data))})
		}
	}
	// durable: make visible
	for _, p := range pl {
		l := loc{s.fid, p.off, uint32(len(p.r.data))}
		if p.r.file == "man" {
			e.midx.Store(p.r.sum, l)
		} else {
			e.idx.put(p.r.sum, l)
		}
	}
	if s.size >= e.segTarget {
		return s.seal()
	}
	return nil
}

// seal appends the pack-v1 index+footer, fsyncs, renames to packs/, fsyncs packs/, opens a new segment
func (s *segment) seal() error {
	e := s.e
	sort.Slice(s.entries, func(i, j int) bool { return s.entries[i].off < s.entries[j].off })
	idx := make([]byte, 0, len(s.entries)*idxEnt)
	for _, en := range s.entries {
		var b [idxEnt]byte
		copy(b[:32], en.sum[:])
		binary.LittleEndian.PutUint64(b[32:], en.off)
		binary.LittleEndian.PutUint32(b[40:], en.n)
		binary.LittleEndian.PutUint32(b[44:], en.n)
		idx = append(idx, b[:]...)
	}
	s.h.Write(idx)
	var ft [64]byte
	copy(ft[:4], "ZSPF")
	binary.LittleEndian.PutUint64(ft[8:], uint64(len(s.entries)))
	binary.LittleEndian.PutUint64(ft[16:], uint64(s.size))
	copy(ft[24:56], s.h.Sum(nil))
	tail := append(idx, ft[:]...)
	if _, err := s.f.WriteAt(tail, s.size); err != nil {
		return err
	}
	if err := fsyncf(s.f); err != nil {
		return err
	}
	s.f.Close()
	final := filepath.Join(e.dir, "packs", hex.EncodeToString(ft[24:56])+".pack")
	if err := rename(s.path, final); err != nil {
		return err
	}
	if err := syncDirPath(filepath.Join(e.dir, "packs")); err != nil {
		return err
	}
	e.files.Store(s.fid, final)
	return s.open()
}

// pack mode: one new pack file per group
func (e *env) packWork(batch []*req) error {
	var buf = make([]byte, 16, 1<<16)
	copy(buf, "ZSPK")
	binary.LittleEndian.PutUint16(buf[4:], 1)
	pend := map[[32]byte]struct{}{}
	var ents []entry
	var recs []*rec
	add := func(r *rec) {
		if _, ok := pend[r.sum]; ok {
			return
		}
		pend[r.sum] = struct{}{}
		off := uint64(len(buf)) + recHdr
		buf = putRec(buf, r)
		ents = append(ents, entry{r.sum, off, uint32(len(r.data))})
		recs = append(recs, r)
	}
	for _, q := range batch {
		for i := range q.chunks {
			if _, ok := e.idx.get(q.chunks[i].sum); ok {
				continue
			}
			add(&q.chunks[i])
		}
		if e.mode == "packinl" {
			q.man.file = "man"
			add(&q.man)
		}
	}
	if len(ents) == 0 {
		return nil
	}
	h := sha256.New()
	h.Write(buf)
	idx := make([]byte, 0, len(ents)*idxEnt)
	for _, en := range ents {
		var b [idxEnt]byte
		copy(b[:32], en.sum[:])
		binary.LittleEndian.PutUint64(b[32:], en.off)
		binary.LittleEndian.PutUint32(b[40:], en.n)
		binary.LittleEndian.PutUint32(b[44:], en.n)
		idx = append(idx, b[:]...)
	}
	h.Write(idx)
	var ft [64]byte
	copy(ft[:4], "ZSPF")
	binary.LittleEndian.PutUint64(ft[8:], uint64(len(ents)))
	binary.LittleEndian.PutUint64(ft[16:], uint64(len(buf)))
	copy(ft[24:56], h.Sum(nil))
	buf = append(append(buf, idx...), ft[:]...)
	tmp := filepath.Join(e.dir, "tmp", fmt.Sprintf("pk-%d.tmp", e.packSeq.Add(1)))
	f, err := create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf); err != nil {
		return err
	}
	nBytes.Add(int64(len(buf)))
	if err := fsyncf(f); err != nil {
		return err
	}
	f.Close()
	final := filepath.Join(e.dir, "packs", hex.EncodeToString(ft[24:56])+".pack")
	if err := rename(tmp, final); err != nil {
		return err
	}
	if err := e.dsPacks.sync(); err != nil {
		return err
	}
	fid := e.fileSeq.Add(1)
	e.files.Store(fid, final)
	for i, en := range ents {
		l := loc{fid, en.off, en.n}
		if recs[i].file == "man" {
			e.midx.Store(en.sum, l)
		} else {
			e.idx.put(en.sum, l)
		}
	}
	return nil
}

func writeFileDurable(e *env, dir, name string, data []byte, ds *dirSyncer) error {
	tmp := filepath.Join(e.dir, "tmp", fmt.Sprintf("t-%d", e.packSeq.Add(1)))
	f, err := create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	nBytes.Add(int64(len(data)))
	if err := fsyncf(f); err != nil {
		return err
	}
	f.Close()
	if err := rename(tmp, filepath.Join(dir, name)); err != nil {
		return err
	}
	return ds.sync()
}

var shardMade sync.Map

func ensureShard(e *env, sum [32]byte) (string, error) {
	h := hex.EncodeToString(sum[:])
	d := filepath.Join(e.dir, "chunks", h[:2], h[2:4])
	if _, ok := shardMade.Load(d); ok {
		return d, nil
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", err
	}
	if err := syncDirPath(filepath.Dir(d)); err != nil {
		return "", err
	}
	if err := syncDirPath(filepath.Join(e.dir, "chunks")); err != nil {
		return "", err
	}
	shardMade.Store(d, true)
	return d, nil
}

func (e *env) put(r *req, i int) error {
	switch e.mode {
	case "loose":
		var wg sync.WaitGroup
		var errs [2]error
		wg.Add(1)
		go func() { // manifest chain overlaps the chunk chain, as in P1B
			defer wg.Done()
			errs[0] = writeFileDurable(e, filepath.Join(e.dir, "manifests"), hex.EncodeToString(r.man.sum[:])+".json", r.man.data, e.dsMan)
		}()
		for k := range r.chunks {
			c := &r.chunks[k]
			if _, ok := e.idx.get(c.sum); ok {
				continue
			}
			d, err := ensureShard(e, c.sum)
			if err != nil {
				errs[1] = err
				break
			}
			h := hex.EncodeToString(c.sum[:])
			if err := writeFileDurable(e, d, h, c.data, e.dirSyncer(&e.dsChunks, d)); err != nil {
				errs[1] = err
				break
			}
			e.idx.put(c.sum, loc{0, 0, uint32(len(c.data))})
		}
		wg.Wait()
		if errs[0] != nil {
			return errs[0]
		}
		if errs[1] != nil {
			return errs[1]
		}
	case "pack", "packinl":
		var wg sync.WaitGroup
		var merr error
		if e.mode == "pack" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				merr = writeFileDurable(e, filepath.Join(e.dir, "manifests"), hex.EncodeToString(r.man.sum[:])+".json", r.man.data, e.dsMan)
			}()
		}
		g := e.segs[0].g // single pack group
		err := g.submit(r)
		wg.Wait()
		if err != nil {
			return err
		}
		if merr != nil {
			return merr
		}
	case "seg", "seginl":
		var wg sync.WaitGroup
		var merr error
		if e.mode == "seg" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				merr = writeFileDurable(e, filepath.Join(e.dir, "manifests"), hex.EncodeToString(r.man.sum[:])+".json", r.man.data, e.dsMan)
			}()
		}
		err := e.segs[i%len(e.segs)].g.submit(r)
		wg.Wait()
		if err != nil {
			return err
		}
		if merr != nil {
			return merr
		}
	}
	// journal frame: group commit
	return e.jgroup.submit(&req{})
}

func (e *env) journalWork(batch []*req) error {
	e.jmu.Lock()
	defer e.jmu.Unlock()
	buf := make([]byte, 0, len(batch)*256)
	for range batch {
		fr := make([]byte, 256)
		copy(fr, "ZSJ1")
		buf = append(buf, fr...)
	}
	if _, err := e.journal.Write(buf); err != nil {
		return err
	}
	nBytes.Add(int64(len(buf)))
	return fsyncf(e.journal)
}

// ---------------- driver ----------------
type result struct {
	Mode, Note                              string
	C, N, Size, Segs                        int
	OpsPerSec, MiBPerSec                    float64
	P50, P95, P99, Max                      float64
	CPUSecPerObj                            float64
	FsyncPerObj, CreatePerObj, RenamePerObj float64
	DirSyncPerObj, WriteBytesPerLogical     float64
	AvgGroup                                float64
	Inodes, Packs, SegmentsOpen             int
	OpenMS                                  float64
	ReadOK                                  int
	ReadUSAvg                               float64
	RSSMiB                                  float64
	GoMaxProcs                              int
}

func cpuSec() float64 {
	var ru syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return float64(ru.Utime.Sec+ru.Stime.Sec) + float64(ru.Utime.Usec+ru.Stime.Usec)/1e6
}

func countInodes(dir string) int {
	n := 0
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error { n++; return nil })
	return n
}

func main() {
	mode := flag.String("mode", "loose", "loose|pack|packinl|seg|seginl")
	dir := flag.String("dir", "", "work dir (fresh)")
	c := flag.Int("c", 8, "concurrency")
	n := flag.Int("n", 4000, "objects")
	size := flag.Int("size", 4096, "object size")
	segs := flag.Int("segs", 1, "active segments")
	segmib := flag.Int("segmib", 32, "segment seal size MiB")
	note := flag.String("note", "", "")
	flag.Parse()
	os.RemoveAll(*dir)
	for _, d := range []string{"chunks", "manifests", "packs", "tmp", "segments"} {
		os.MkdirAll(filepath.Join(*dir, d), 0o755)
	}
	e := &env{mode: *mode, dir: *dir, idx: newIndex(), segTarget: int64(*segmib) << 20}
	e.dsMan = &dirSyncer{path: filepath.Join(*dir, "manifests")}
	e.dsPacks = &dirSyncer{path: filepath.Join(*dir, "packs")}
	jf, _ := os.OpenFile(filepath.Join(*dir, "visibility.log"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	e.journal = jf
	e.jgroup = &group{work: e.journalWork}
	switch *mode {
	case "seg", "seginl":
		for i := 0; i < *segs; i++ {
			s, err := e.newSegment(i)
			if err != nil {
				panic(err)
			}
			e.segs = append(e.segs, s)
		}
	case "pack", "packinl":
		e.segs = []*segment{{g: &group{work: e.packWork}}}
	}
	syncDirPath(*dir)

	lat := make([]float64, *n)
	mans := make([][32]byte, *n)
	var next atomic.Int64
	var wg sync.WaitGroup
	cpu0 := cpuSec()
	t0 := time.Now()
	var fail atomic.Int64
	for w := 0; w < *c; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= *n {
					return
				}
				r := genReq(i, *size)
				mans[i] = r.man.sum
				ts := time.Now()
				if err := e.put(r, i); err != nil {
					fmt.Fprintln(os.Stderr, "put:", err)
					fail.Add(1)
					return
				}
				lat[i] = float64(time.Since(ts).Microseconds()) / 1000
			}
		}()
	}
	wg.Wait()
	el := time.Since(t0).Seconds()
	cpu := cpuSec() - cpu0
	if fail.Load() > 0 {
		os.Exit(2)
	}
	var avg float64
	if e.jgroup.batches.Load() > 0 {
		avg = float64(e.jgroup.members.Load()) / float64(e.jgroup.batches.Load())
	}
	if len(e.segs) > 0 && e.segs[0].g.batches.Load() > 0 && (*mode == "pack" || *mode == "packinl") {
		avg = float64(e.segs[0].g.members.Load()) / float64(e.segs[0].g.batches.Load())
	}
	sorted := append([]float64(nil), lat...)
	sort.Float64s(sorted)
	pc := func(p float64) float64 { return sorted[int(float64(len(sorted)-1)*p)] }
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	rss := rssMiB()
	res := result{Mode: *mode, Note: *note, C: *c, N: *n, Size: *size, Segs: *segs,
		OpsPerSec: float64(*n) / el, MiBPerSec: float64(*n) * float64(*size) / (1 << 20) / el,
		P50: pc(.5), P95: pc(.95), P99: pc(.99), Max: sorted[len(sorted)-1],
		CPUSecPerObj: cpu / float64(*n), FsyncPerObj: float64(nFsync.Load()) / float64(*n),
		CreatePerObj: float64(nCreate.Load()) / float64(*n), RenamePerObj: float64(nRename.Load()) / float64(*n),
		DirSyncPerObj: float64(nDirSync.Load()) / float64(*n), WriteBytesPerLogical: float64(nBytes.Load()) / (float64(*n) * float64(*size+1)),
		AvgGroup: avg, RSSMiB: rss, GoMaxProcs: runtime.GOMAXPROCS(0)}
	res.Inodes = countInodes(*dir)
	pk, _ := filepath.Glob(filepath.Join(*dir, "packs", "*.pack"))
	res.Packs = len(pk)
	// read-back with re-verification of a sample
	rs := time.Now()
	ok := 0
	rng := rand.New(rand.NewPCG(1, 2))
	sample := min(*n, 2000)
	for k := 0; k < sample; k++ {
		i := rng.IntN(*n)
		r := genReq(i, *size)
		if readBack(e, r) == nil {
			ok++
		}
	}
	res.ReadOK = ok
	res.ReadUSAvg = float64(time.Since(rs).Microseconds()) / float64(sample)
	// reopen cost: rebuild the index from disk (pack footers + open-segment scan)
	os0 := time.Now()
	rebuilt := reopen(*dir)
	res.OpenMS = float64(time.Since(os0).Microseconds()) / 1000
	_ = rebuilt
	res.SegmentsOpen = len(e.segs)
	b, _ := json.Marshal(res)
	fmt.Println(string(b))
	if ok != sample {
		fmt.Fprintf(os.Stderr, "READ-BACK FAILED %d/%d\n", ok, sample)
		os.Exit(3)
	}
}

func rssMiB() float64 {
	b, _ := os.ReadFile("/proc/self/status")
	var kb float64
	for _, l := range splitLines(string(b)) {
		if len(l) > 6 && l[:6] == "VmHWM:" {
			fmt.Sscanf(l[6:], "%f", &kb)
		}
	}
	return kb / 1024
}
func splitLines(s string) []string {
	var out []string
	st := 0
	for i := range s {
		if s[i] == '\n' {
			out = append(out, s[st:i])
			st = i + 1
		}
	}
	return out
}

// genReq: deterministic object i: fixed 64 KiB chunks of pseudo-random bytes + ~700 B manifest
func genReq(i, size int) *req {
	rng := rand.New(rand.NewPCG(uint64(i)+1, 0x9e3779b97f4a7c15))
	data := make([]byte, size)
	for k := 0; k+8 <= len(data); k += 8 {
		binary.LittleEndian.PutUint64(data[k:], rng.Uint64())
	}
	r := &req{t0: time.Now()}
	for off := 0; off < size || (size == 0 && off == 0); off += 64 << 10 {
		end := min(off+64<<10, size)
		ch := data[off:end]
		r.chunks = append(r.chunks, rec{sum: sha256.Sum256(ch), data: ch})
		if size == 0 {
			break
		}
	}
	type cref struct {
		SHA256 string `json:"sha256"`
		Length int    `json:"length"`
	}
	man := struct {
		V      int    `json:"manifest_format_version"`
		UUID   string `json:"manifest_uuid"`
		Total  int    `json:"total_length"`
		Chunks []cref `json:"chunks"`
		Obj    string `json:"object_sha256"`
		ETag   string `json:"etag"`
		CT     string `json:"content_type"`
		Ver    string `json:"version_id"`
		Pad    string `json:"created_at"`
	}{V: 1, UUID: fmt.Sprintf("018f4d2e-6b1a-7c3d-9e2f-%012x", i), Total: size, Obj: hex.EncodeToString(r.chunks[0].sum[:]), ETag: "d41d8cd98f00b204e9800998ecf8427e", CT: "application/octet-stream", Ver: fmt.Sprintf("v%012x", i), Pad: "2026-11-09T00:00:00.000000000Z"}
	for _, c := range r.chunks {
		man.Chunks = append(man.Chunks, cref{hex.EncodeToString(c.sum[:]), len(c.data)})
	}
	mb, _ := json.Marshal(man)
	r.man = rec{sum: sha256.Sum256(mb), data: mb}
	return r
}

func readLoc(e *env, l loc) ([]byte, error) {
	v, ok := e.files.Load(l.file)
	if !ok {
		return nil, fmt.Errorf("no file %d", l.file)
	}
	f, err := os.Open(v.(string))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, l.n)
	_, err = f.ReadAt(b, int64(l.off))
	return b, err
}

func readBack(e *env, want *req) error {
	get := func(r *rec, man bool) error {
		var data []byte
		var err error
		if e.mode == "loose" || (man && (e.mode == "pack" || e.mode == "seg")) {
			h := hex.EncodeToString(r.sum[:])
			if man {
				data, err = os.ReadFile(filepath.Join(e.dir, "manifests", h+".json"))
			} else {
				data, err = os.ReadFile(filepath.Join(e.dir, "chunks", h[:2], h[2:4], h))
			}
		} else {
			var l loc
			var ok bool
			if man {
				var v any
				v, ok = e.midx.Load(r.sum)
				if ok {
					l = v.(loc)
				}
			} else {
				l, ok = e.idx.get(r.sum)
			}
			if !ok {
				return fmt.Errorf("index miss")
			}
			data, err = readLoc(e, l)
		}
		if err != nil {
			return err
		}
		if sha256.Sum256(data) != r.sum {
			return fmt.Errorf("hash mismatch")
		}
		return nil
	}
	if err := get(&want.man, true); err != nil {
		return err
	}
	for i := range want.chunks {
		if err := get(&want.chunks[i], false); err != nil {
			return err
		}
	}
	return nil
}

// reopen: rebuild a digest index from sealed pack footers and by scanning open segments
func reopen(dir string) int {
	n := 0
	pk, _ := filepath.Glob(filepath.Join(dir, "packs", "*.pack"))
	for _, p := range pk {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		st, _ := f.Stat()
		var ft [64]byte
		f.ReadAt(ft[:], st.Size()-64)
		cnt := binary.LittleEndian.Uint64(ft[8:])
		ioff := binary.LittleEndian.Uint64(ft[16:])
		idx := make([]byte, cnt*idxEnt)
		f.ReadAt(idx, int64(ioff))
		m := map[[32]byte]loc{}
		for k := uint64(0); k < cnt; k++ {
			var s [32]byte
			copy(s[:], idx[k*idxEnt:])
			m[s] = loc{0, binary.LittleEndian.Uint64(idx[k*idxEnt+32:]), 0}
		}
		n += len(m)
		f.Close()
	}
	op, _ := filepath.Glob(filepath.Join(dir, "segments", "*.open"))
	for _, p := range op {
		b, err := os.ReadFile(p) // scan + verify every record's SHA-256 (torn-tail detection)
		if err != nil {
			continue
		}
		off := 16
		for off+recHdr <= len(b) {
			ln := int(binary.LittleEndian.Uint32(b[off+32:]))
			if off+recHdr+ln > len(b) {
				break
			}
			var s [32]byte
			copy(s[:], b[off:off+32])
			if sha256.Sum256(b[off+recHdr:off+recHdr+ln]) != s {
				break
			}
			n++
			off += recHdr + ln
		}
	}
	return n
}
