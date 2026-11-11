package dcore

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"sync"
	"sync/atomic"
)

// Windowed (bounded-memory) delta: the base is indexed by a streaming pass
// into a fixed-size anchor table (memory depends on MaxAnchors, not on the
// base size), the target is read, matched and emitted in independent windows,
// and a stream of frames with per-frame SHA-256 is produced. A frame refers to
// the base only through absolute 64-bit offsets, so frames are independently
// decodable (range-addressable, parallelizable) and a match never refers to a
// region another window owns. Every COPY is verified against the base bytes
// before it is emitted. The research format is "ZW1".

var (
	ErrWindowFormat = errors.New("dcore: invalid windowed delta")
	ErrWindowAuth   = errors.New("dcore: windowed delta authentication failed")
)

const (
	wMagic        = "ZW1\x00"
	wTrailerMagic = "ZW1T"
	wTrailerLen   = 4 + 8 + 8 + 4 + 8
	// WMaxFrames bounds the frame table a hostile record can request.
	WMaxFrames = 1 << 22
)

// WParams configures windowed encoding.
type WParams struct {
	Anchor     Params
	Window     int64 // target bytes per independent frame
	PageSize   int   // base read granularity (bytes)
	CachePages int   // base pages cached per worker
	Workers    int
}

// DefaultWParams: 4 MiB frames, 64 KiB pages x 256 (16 MiB) per worker, a 4 Mi
// anchor budget (64 MiB table at load 0.5) and one worker per CPU is chosen
// by the caller.
func DefaultWParams() WParams {
	a := DefaultParams()
	a.MaxAnchors = 4 << 20
	return WParams{Anchor: a, Window: 4 << 20, PageSize: 64 << 10, CachePages: 256, Workers: 1}
}

func (p WParams) valid() bool {
	return p.Anchor.valid() && p.Window >= 1<<10 && p.Window <= 1<<30 && p.PageSize >= 512 && p.PageSize&(p.PageSize-1) == 0 && p.CachePages >= 2 && p.Workers >= 1 && p.Workers <= 256
}

// WIndex is the immutable anchor table over one ReaderAt base. Safe for
// concurrent use; per-worker page caches are created by the encoder.
type WIndex struct {
	p      Params
	size   int64
	nbits  uint
	pow    uint64
	table  []uint64
	tmask  uint64
	tshift uint
	// SHA is the base's SHA-256 computed during the build pass.
	SHA [32]byte
	// BuildReads counts bytes read from the base while indexing.
	BuildReads int64
	Anchors    int64
}

// BuildWIndex indexes base with `workers` parallel range scanners and hashes
// it in one concurrent sequential pass. Peak memory: the table plus
// workers*2*chunk bytes of read buffers.
func BuildWIndex(base io.ReaderAt, size int64, p Params, workers int) (*WIndex, error) {
	if !p.valid() || workers < 1 || workers > 256 {
		return nil, ErrParams
	}
	if size < 0 || size > maxTotal {
		return nil, ErrTooLarge
	}
	x := &WIndex{p: p, size: size}
	for x.nbits = uint(p.MinSampleBits); size>>x.nbits > int64(p.MaxAnchors); x.nbits++ {
	}
	pow := uint64(1)
	for i := 0; i < p.Window-1; i++ {
		pow *= hashP
	}
	x.pow = pow
	est := int(size>>x.nbits) + 64
	tbits := uint(4)
	for 1<<tbits < 2*est {
		tbits++
	}
	x.table = make([]uint64, 1<<tbits)
	x.tmask = 1<<tbits - 1
	x.tshift = 64 - tbits

	const chunk = 4 << 20
	var wg sync.WaitGroup
	var firstErr atomic.Value
	var reads atomic.Int64
	var anchors atomic.Int64
	nchunks := (size + chunk - 1) / chunk
	var next atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, chunk+p.Window-1)
			for {
				c := next.Add(1) - 1
				if c >= nchunks || firstErr.Load() != nil {
					return
				}
				start := c * chunk
				// Positions i in [start, min(start+chunk, size-Window+1)) own their window.
				end := min(start+chunk+int64(p.Window)-1, size)
				b := buf[:end-start]
				if _, err := readFull(base, b, start); err != nil {
					firstErr.CompareAndSwap(nil, err)
					return
				}
				reads.Add(int64(len(b)))
				if len(b) < p.Window {
					continue
				}
				var h uint64
				for _, v := range b[:p.Window] {
					h = h*hashP + uint64(v) + 1
				}
				owned := min(int64(chunk), size-int64(p.Window)+1-start)
				for i := int64(0); i < owned; i++ {
					if x.sampled(h) && x.insertAtomic(h, start+i) {
						anchors.Add(1)
					}
					if i+1 < owned {
						h = (h-(uint64(b[i])+1)*x.pow)*hashP + uint64(b[i+int64(p.Window)]) + 1
					}
				}
			}
		}()
	}
	// Sequential hashing pass, concurrent with indexing.
	wg.Add(1)
	go func() {
		defer wg.Done()
		hh := sha256.New()
		buf := make([]byte, 1<<20)
		for off := int64(0); off < size; {
			n := int64(len(buf))
			if size-off < n {
				n = size - off
			}
			if _, err := readFull(base, buf[:n], off); err != nil {
				firstErr.CompareAndSwap(nil, err)
				return
			}
			hh.Write(buf[:n])
			off += n
		}
		copy(x.SHA[:], hh.Sum(nil))
	}()
	wg.Wait()
	if e := firstErr.Load(); e != nil {
		return nil, e.(error)
	}
	x.BuildReads = reads.Load()
	x.Anchors = anchors.Load()
	return x, nil
}

func readFull(r io.ReaderAt, b []byte, off int64) (int, error) {
	n, err := r.ReadAt(b, off)
	if n == len(b) {
		return n, nil
	}
	if err == nil || err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (x *WIndex) sampled(h uint64) bool {
	return x.nbits == 0 || (h*sampleK)>>(64-x.nbits) == 0
}

func (x *WIndex) insertAtomic(h uint64, off int64) bool {
	m := mix64(h)
	slot := m >> x.tshift
	tag := (m >> 8) & 0xffffff
	n := 0
	for probe := 0; probe < x.p.MaxProbe; probe++ {
		e := atomic.LoadUint64(&x.table[slot])
		if e == 0 {
			if n >= x.p.Slots {
				return false
			}
			if atomic.CompareAndSwapUint64(&x.table[slot], 0, tag<<posBits|uint64(off+1)) {
				return true
			}
			e = atomic.LoadUint64(&x.table[slot])
		}
		if e>>posBits == tag {
			n++
			if n >= x.p.Slots {
				return false
			}
		}
		slot = (slot + 1) & x.tmask
	}
	return false
}

// pageView is a per-worker bounded cache of base pages.
type pageView struct {
	r     io.ReaderAt
	size  int64
	psz   int64
	pages map[int64][]byte
	order []int64
	cap   int
	Reads int64 // bytes read from the base
	err   error
}

func newPageView(r io.ReaderAt, size int64, psz, cap int) *pageView {
	return &pageView{r: r, size: size, psz: int64(psz), pages: map[int64][]byte{}, cap: cap}
}

func (v *pageView) page(n int64) []byte {
	if p, ok := v.pages[n]; ok {
		return p
	}
	off := n * v.psz
	if off >= v.size || v.err != nil {
		return nil
	}
	var p []byte
	if len(v.order) >= v.cap { // evict oldest, reuse its buffer
		old := v.order[0]
		v.order = v.order[1:]
		p = v.pages[old]
		delete(v.pages, old)
	} else {
		p = make([]byte, v.psz)
	}
	p = p[:min(v.psz, v.size-off)]
	if _, err := readFull(v.r, p, off); err != nil {
		v.err = err
		return nil
	}
	v.Reads += int64(len(p))
	v.pages[n] = p
	v.order = append(v.order, n)
	return p
}

// forward returns the longest common prefix of base[off:] and t.
func (v *pageView) forward(off int64, t []byte) int {
	f := 0
	for f < len(t) && off+int64(f) < v.size {
		o := off + int64(f)
		p := v.page(o / v.psz)
		if p == nil {
			return f
		}
		s := p[o%v.psz:]
		k := commonPrefix(s, t[f:])
		f += k
		if k < len(s) {
			return f
		}
	}
	return f
}

// backward returns the longest common suffix of base[:off] and t.
func (v *pageView) backward(off int64, t []byte) int {
	f := 0
	for f < len(t) && off-int64(f) > 0 {
		o := off - int64(f) // exclusive end
		p := v.page((o - 1) / v.psz)
		if p == nil {
			return f
		}
		s := p[:(o-1)%v.psz+1]
		k := commonSuffix(s, t[:len(t)-f])
		f += k
		if k < len(s) {
			return f
		}
	}
	return f
}

// matchWindow produces ops for one target window (offsets are absolute in the base).
func (x *WIndex) matchWindow(v *pageView, t []byte) ([]Op, error) {
	w := x.p.Window
	var ops []Op
	start := 0
	emitLit := func(end int) {
		if end > start {
			ops = append(ops, Op{TPos: int64(start), N: int64(end - start), Base: -1, Lit: t[start:end]})
		}
	}
	if len(t) >= w {
		var h uint64
		for _, c := range t[:w] {
			h = h*hashP + uint64(c) + 1
		}
		for i := 0; i+w <= len(t); {
			if x.sampled(h) {
				n, back, off := x.best(v, t, i, start, h)
				if v.err != nil {
					return nil, v.err
				}
				if n >= x.p.MinCopy {
					s := i - back
					emitLit(s)
					if k := len(ops); k > 0 && ops[k-1].Base == 0 && ops[k-1].Off+ops[k-1].N == off && start == s {
						ops[k-1].N += int64(n)
					} else {
						ops = append(ops, Op{TPos: int64(s), N: int64(n), Base: 0, Off: off})
					}
					i = s + n
					start = i
					if i+w <= len(t) {
						h = 0
						for _, c := range t[i : i+w] {
							h = h*hashP + uint64(c) + 1
						}
					}
					continue
				}
			}
			if i+w >= len(t) {
				break
			}
			h = (h-(uint64(t[i])+1)*x.pow)*hashP + uint64(t[i+w]) + 1
			i++
		}
	}
	emitLit(len(t))
	return ops, nil
}

func (x *WIndex) best(v *pageView, t []byte, i, lo int, h uint64) (n, back int, off int64) {
	m := mix64(h)
	slot := m >> x.tshift
	tag := (m >> 8) & 0xffffff
	for probe := 0; probe < x.p.MaxProbe; probe++ {
		e := x.table[slot]
		if e == 0 {
			break
		}
		if e>>posBits == tag {
			o := int64(e&posMask) - 1
			f := v.forward(o, t[i:])
			if f >= x.p.Window {
				bk := min(int64(i-lo), o)
				b := v.backward(o, t[i-int(bk):i])
				if f+b > n {
					n, back, off = f+b, b, o-int64(b)
				}
			}
		}
		slot = (slot + 1) & x.tmask
	}
	return
}

// WStats reports encoder resources.
type WStats struct {
	Frames        int
	TargetBytes   int64
	RecordBytes   int64
	LiteralBytes  int64
	CopyBytes     int64
	BaseReadBytes int64 // index pass + verification reads
	IndexAnchors  int64
	PeakWindows   int
}

type frameRec struct {
	tlen int64
	mode byte
	plen int64
	sum  [32]byte
}

// WEncode writes a windowed delta of target against base to dst. idx may be
// shared across several targets (it carries the base identity).
func WEncode(dst io.Writer, target io.ReaderAt, tsize int64, base io.ReaderAt, bsize int64, idx *WIndex, wp WParams) (WStats, error) {
	var st WStats
	if !wp.valid() || idx == nil || idx.size != bsize || tsize < 0 {
		return st, ErrParams
	}
	if _, err := dst.Write([]byte(wMagic)); err != nil {
		return st, err
	}
	offset := int64(len(wMagic))
	type job struct {
		n    int
		buf  []byte
		done chan struct{}
		rec  frameRec
		out  []byte
		lit  int64
		cp   int64
		rd   int64
		err  error
	}
	nwin := (tsize + wp.Window - 1) / wp.Window
	if nwin > WMaxFrames {
		return st, ErrTooLarge
	}
	inflight := wp.Workers * 2
	jobs := make(chan *job, inflight)
	order := make(chan *job, inflight)
	pool := sync.Pool{New: func() any { return make([]byte, wp.Window) }}
	var wg sync.WaitGroup
	for w := 0; w < wp.Workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v := newPageView(base, bsize, wp.PageSize, wp.CachePages)
			for j := range jobs {
				before := v.Reads
				ops, err := idx.matchWindow(v, j.buf[:j.n])
				if err != nil {
					j.err = err
					close(j.done)
					continue
				}
				prog := buildFrameProgram(ops)
				for _, o := range ops {
					if o.Base < 0 {
						j.lit += o.N
					} else {
						j.cp += o.N
					}
				}
				j.rec = frameRec{tlen: int64(j.n), sum: sha256.Sum256(j.buf[:j.n])}
				j.rec.mode, j.out = 0, prog
				if z := deflateBytes(prog); len(z) < len(prog) {
					j.rec.mode, j.out = 1, z
				}
				j.rec.plen = int64(len(j.out))
				j.rd = v.Reads - before
				close(j.done)
			}
		}()
	}
	var wrErr error
	var recs []frameRec
	whole := sha256.New()
	var wwg sync.WaitGroup
	wwg.Add(1)
	go func() { // ordered writer
		defer wwg.Done()
		for j := range order {
			<-j.done
			if wrErr == nil && j.err != nil {
				wrErr = j.err
			}
			if wrErr == nil {
				if _, err := dst.Write(j.out); err != nil {
					wrErr = err
				} else {
					offset += int64(len(j.out))
					recs = append(recs, j.rec)
					st.LiteralBytes += j.lit
					st.CopyBytes += j.cp
					st.BaseReadBytes += j.rd
				}
			}
			pool.Put(j.buf)
		}
	}()
	var readErr error
	for w := int64(0); w < nwin && readErr == nil; w++ {
		j := &job{done: make(chan struct{})}
		j.buf = pool.Get().([]byte)
		j.n = int(min(wp.Window, tsize-w*wp.Window))
		if _, err := readFull(target, j.buf[:j.n], w*wp.Window); err != nil {
			readErr = err
			pool.Put(j.buf)
			break
		}
		whole.Write(j.buf[:j.n])
		order <- j
		jobs <- j
	}
	close(jobs)
	close(order)
	wg.Wait()
	wwg.Wait()
	if readErr != nil {
		return st, readErr
	}
	if wrErr != nil {
		return st, wrErr
	}
	// Table: base identity, target identity, frames.
	var tbl []byte
	tbl = append(tbl, idx.SHA[:]...)
	tbl = binary.AppendUvarint(tbl, uint64(bsize))
	tbl = binary.AppendUvarint(tbl, uint64(tsize))
	tbl = append(tbl, whole.Sum(nil)...)
	tbl = binary.AppendUvarint(tbl, uint64(len(recs)))
	for _, r := range recs {
		tbl = binary.AppendUvarint(tbl, uint64(r.tlen))
		tbl = append(tbl, r.mode)
		tbl = binary.AppendUvarint(tbl, uint64(r.plen))
		tbl = append(tbl, r.sum[:]...)
	}
	var tr [wTrailerLen]byte
	copy(tr[:], wTrailerMagic)
	binary.LittleEndian.PutUint64(tr[4:], uint64(offset))
	binary.LittleEndian.PutUint64(tr[12:], uint64(len(tbl)))
	binary.LittleEndian.PutUint32(tr[20:], crc32.Checksum(tbl, crcTab))
	binary.LittleEndian.PutUint64(tr[24:], uint64(len(recs)))
	if _, err := dst.Write(tbl); err != nil {
		return st, err
	}
	if _, err := dst.Write(tr[:]); err != nil {
		return st, err
	}
	st.Frames = len(recs)
	st.TargetBytes = tsize
	st.RecordBytes = offset + int64(len(tbl)) + wTrailerLen
	st.IndexAnchors = idx.Anchors
	st.BaseReadBytes += idx.BuildReads
	return st, nil
}

var crcTab = crc32.MakeTable(crc32.Castagnoli)

func buildFrameProgram(ops []Op) []byte {
	var p []byte
	for _, o := range ops {
		if o.Base < 0 {
			for l := o.Lit; len(l) > 0; {
				n := min(len(l), LitSplit)
				p = append(p, 0)
				p = binary.AppendUvarint(p, uint64(n))
				p = append(p, l[:n]...)
				l = l[n:]
			}
			continue
		}
		p = append(p, 1)
		p = binary.AppendUvarint(p, uint64(o.Off))
		p = binary.AppendUvarint(p, uint64(o.N))
	}
	return p
}

func deflateBytes(b []byte) []byte {
	var out bytes.Buffer
	w := flatePool.Get().(*flate.Writer)
	w.Reset(&out)
	_, _ = w.Write(b)
	_ = w.Close()
	w.Reset(io.Discard)
	flatePool.Put(w)
	return out.Bytes()
}

// WRecord is a parsed windowed record (trailer + table).
type WRecord struct {
	r         io.ReaderAt
	BaseSHA   [32]byte
	BaseSize  int64
	Size      int64
	SHA       [32]byte
	frames    []frameRec
	tstart    []int64 // target start per frame
	pstart    []int64 // payload offset per frame
	maxWindow int64
}

// OpenWRecord parses and validates the trailer and frame table.
func OpenWRecord(r io.ReaderAt, size int64) (*WRecord, error) {
	if size < int64(len(wMagic))+wTrailerLen {
		return nil, ErrWindowFormat
	}
	var head [4]byte
	if _, err := readFull(r, head[:], 0); err != nil || string(head[:]) != wMagic {
		return nil, ErrWindowFormat
	}
	var tr [wTrailerLen]byte
	if _, err := readFull(r, tr[:], size-wTrailerLen); err != nil || string(tr[:4]) != wTrailerMagic {
		return nil, ErrWindowFormat
	}
	toff := binary.LittleEndian.Uint64(tr[4:])
	tlen := binary.LittleEndian.Uint64(tr[12:])
	count := binary.LittleEndian.Uint64(tr[24:])
	if count > WMaxFrames || toff < uint64(len(wMagic)) || toff > uint64(size) || tlen > uint64(size)-toff-wTrailerLen || toff+tlen+wTrailerLen != uint64(size) {
		return nil, ErrWindowFormat
	}
	tbl := make([]byte, tlen)
	if _, err := readFull(r, tbl, int64(toff)); err != nil {
		return nil, err
	}
	if crc32.Checksum(tbl, crcTab) != binary.LittleEndian.Uint32(tr[20:]) {
		return nil, ErrWindowFormat
	}
	rec := &WRecord{r: r}
	c := cursor{b: tbl}
	copy(rec.BaseSHA[:], c.take(32))
	rec.BaseSize = int64(c.uv())
	rec.Size = int64(c.uv())
	copy(rec.SHA[:], c.take(32))
	n := c.uv()
	if c.err != nil || n != count || rec.BaseSize < 0 || rec.Size < 0 {
		return nil, ErrWindowFormat
	}
	var tpos int64
	ppos := int64(len(wMagic))
	for i := uint64(0); i < n; i++ {
		var f frameRec
		f.tlen = int64(c.uv())
		mb := c.take(1)
		f.plen = int64(c.uv())
		copy(f.sum[:], c.take(32))
		if c.err != nil || len(mb) != 1 || mb[0] > 1 || f.tlen <= 0 || f.tlen > 1<<30 || f.plen < 0 {
			return nil, ErrWindowFormat
		}
		f.mode = mb[0]
		if tpos > rec.Size-f.tlen || ppos > int64(toff)-f.plen {
			return nil, ErrWindowFormat
		}
		rec.frames = append(rec.frames, f)
		rec.tstart = append(rec.tstart, tpos)
		rec.pstart = append(rec.pstart, ppos)
		tpos += f.tlen
		ppos += f.plen
		rec.maxWindow = max(rec.maxWindow, f.tlen)
	}
	if len(c.b) != 0 || tpos != rec.Size || ppos != int64(toff) {
		return nil, ErrWindowFormat
	}
	return rec, nil
}

// Frames returns the frame count.
func (w *WRecord) Frames() int { return len(w.frames) }

type cursor struct {
	b   []byte
	err error
}

func (c *cursor) take(n int) []byte {
	if c.err != nil || n < 0 || n > len(c.b) {
		c.err = ErrWindowFormat
		return nil
	}
	v := c.b[:n]
	c.b = c.b[n:]
	return v
}

func (c *cursor) uv() uint64 {
	v, n := binary.Uvarint(c.b)
	if c.err != nil || n <= 0 {
		c.err = ErrWindowFormat
		return 0
	}
	c.b = c.b[n:]
	return v
}

// decodeFrame reconstructs one frame into out (len == frame target length)
// and authenticates it against the frame SHA-256.
func (w *WRecord) decodeFrame(i int, base *pageView, out []byte) error {
	f := w.frames[i]
	payload := make([]byte, f.plen)
	if _, err := readFull(w.r, payload, w.pstart[i]); err != nil {
		return err
	}
	prog := payload
	if f.mode == 1 {
		limit := 8*f.tlen + 64<<10
		zr := flate.NewReader(bytes.NewReader(payload))
		var err error
		prog, err = io.ReadAll(io.LimitReader(zr, limit+1))
		if cerr := zr.Close(); err != nil || cerr != nil || int64(len(prog)) > limit {
			return ErrWindowFormat
		}
	}
	c := cursor{b: prog}
	pos := int64(0)
	for len(c.b) > 0 {
		tag := c.take(1)
		if c.err != nil {
			return ErrWindowFormat
		}
		switch tag[0] {
		case 0:
			n := c.uv()
			if c.err != nil || n == 0 || n > uint64(f.tlen-pos) {
				return ErrWindowFormat
			}
			s := c.take(int(n))
			if c.err != nil {
				return ErrWindowFormat
			}
			copy(out[pos:], s)
			pos += int64(n)
		case 1:
			off, n := c.uv(), c.uv()
			if c.err != nil || n == 0 || n > uint64(f.tlen-pos) || off > uint64(w.BaseSize) || n > uint64(w.BaseSize)-off {
				return ErrWindowFormat
			}
			if err := base.copyTo(int64(off), out[pos:pos+int64(n)]); err != nil {
				return err
			}
			pos += int64(n)
		default:
			return ErrWindowFormat
		}
	}
	if pos != f.tlen || sha256.Sum256(out) != f.sum {
		return ErrWindowAuth
	}
	return nil
}

func (v *pageView) copyTo(off int64, dst []byte) error {
	for len(dst) > 0 {
		p := v.page(off / v.psz)
		if p == nil {
			if v.err != nil {
				return v.err
			}
			return ErrWindowFormat
		}
		k := copy(dst, p[off%v.psz:])
		dst = dst[k:]
		off += int64(k)
	}
	return nil
}

// WDecodeStats reports decoder resources.
type WDecodeStats struct {
	Frames        int
	BaseReadBytes int64
	PayloadBytes  int64
}

// Decode streams the whole target to dst, authenticating every frame and then
// the whole object (length and SHA-256). base must return authenticated bytes
// (or its identity must be verified by the caller); frames additionally catch
// any wrong base byte they touch.
func (w *WRecord) Decode(dst io.Writer, base io.ReaderAt, wp WParams) (WDecodeStats, error) {
	var st WDecodeStats
	if !wp.valid() {
		return st, ErrParams
	}
	v := newPageView(base, w.BaseSize, wp.PageSize, wp.CachePages)
	whole := sha256.New()
	buf := make([]byte, w.maxWindow)
	for i := range w.frames {
		out := buf[:w.frames[i].tlen]
		if err := w.decodeFrame(i, v, out); err != nil {
			return st, err
		}
		whole.Write(out)
		if _, err := dst.Write(out); err != nil {
			return st, err
		}
		st.Frames++
		st.PayloadBytes += w.frames[i].plen
	}
	st.BaseReadBytes = v.Reads
	var sum [32]byte
	copy(sum[:], whole.Sum(nil))
	if sum != w.SHA {
		return st, ErrWindowAuth
	}
	return st, nil
}

// DecodeRange returns target[off:off+n] by decoding only the covering frames.
func (w *WRecord) DecodeRange(base io.ReaderAt, wp WParams, off, n int64) ([]byte, WDecodeStats, error) {
	var st WDecodeStats
	if !wp.valid() || off < 0 || n < 0 || off > w.Size || n > w.Size-off {
		return nil, st, ErrParams
	}
	v := newPageView(base, w.BaseSize, wp.PageSize, wp.CachePages)
	out := make([]byte, 0, n)
	// first frame containing off
	lo, hi := 0, len(w.frames)
	for lo < hi {
		m := (lo + hi) / 2
		if w.tstart[m]+w.frames[m].tlen <= off {
			lo = m + 1
		} else {
			hi = m
		}
	}
	buf := make([]byte, w.maxWindow)
	for i := lo; i < len(w.frames) && int64(len(out)) < n; i++ {
		fb := buf[:w.frames[i].tlen]
		if err := w.decodeFrame(i, v, fb); err != nil {
			return nil, st, err
		}
		from := max(off-w.tstart[i], 0)
		to := min(int64(len(fb)), off+n-w.tstart[i])
		out = append(out, fb[from:to]...)
		st.Frames++
		st.PayloadBytes += w.frames[i].plen
	}
	st.BaseReadBytes = v.Reads
	return out, st, nil
}
