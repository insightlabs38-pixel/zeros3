package numcore

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"io"
	"sync"

	"zeros3-z3/dcore"
	"zeros3-z3/qcore"
)

// auxFn returns the importance vector of one tensor from auxiliary object idx.
type auxFn func(ctx context.Context, idx int, name string, cols int) ([]float32, error)

// Stored is how a dependency is available: the original bytes with their tensor table (Raw,
// Lay) or a ZN7 object (Obj). Storage adapters return it; the core never touches a store.
type Stored struct {
	Raw io.ReaderAt
	Lay *Layout
	Obj *Object
}

// Resolver is the storage interface of the core.
type Resolver interface {
	Open(ctx context.Context, id ID) (Stored, error)
	Aux(ctx context.Context, id ID) ([]byte, error)
}

// Source reads decoded tensor payload bytes and the header/padding bytes of one object.
type Source interface {
	Info(ti int) (dt DType, size, cols int64, ok bool)
	ReadTensor(ctx context.Context, ti int, off int64, dst []byte) error
	Gaps(ctx context.Context) ([]byte, error)
}

// RawSource serves an original file.
type RawSource struct {
	R io.ReaderAt
	L *Layout
}

func (s RawSource) Info(ti int) (DType, int64, int64, bool) {
	if ti < 0 || ti >= len(s.L.T) {
		return 0, 0, 0, false
	}
	t := &s.L.T[ti]
	return t.DT, t.Len, t.Cols, true
}

func (s RawSource) ReadTensor(ctx context.Context, ti int, off int64, dst []byte) error {
	if ti < 0 || ti >= len(s.L.T) {
		return ErrRange7
	}
	t := &s.L.T[ti]
	if off < 0 || int64(len(dst)) > t.Len-off {
		return ErrRange7
	}
	return readFull(s.R, dst, t.Off+off)
}

func (s RawSource) Gaps(ctx context.Context) ([]byte, error) {
	n := s.L.GapLen()
	if n < 0 || n > MaxGap7 {
		return nil, ErrLayout7
	}
	out := make([]byte, n)
	p, prev := int64(0), int64(0)
	for i := 0; i <= len(s.L.T); i++ {
		end := s.L.Size
		if i < len(s.L.T) {
			end = s.L.T[i].Off
		}
		if end > prev {
			if err := readFull(s.R, out[p:p+end-prev], prev); err != nil {
				return nil, err
			}
			p += end - prev
		}
		if i < len(s.L.T) {
			prev = s.L.T[i].Off + s.L.T[i].Len
		}
	}
	return out, nil
}

// WinCache is a byte-bounded LRU of decoded base windows shared by chained decodes.
type WinCache struct {
	mu    sync.Mutex
	max   int64
	used  int64
	ll    *list.List
	m     map[winKey]*list.Element
	Hits  int64
	Miss  int64
	Bytes int64 // decoded bytes produced on misses
}

type winKey struct {
	id ID
	ti int
	k  int64
}

type winEnt struct {
	key  winKey
	data []byte
}

func NewWinCache(maxBytes int64) *WinCache {
	return &WinCache{max: maxBytes, ll: list.New(), m: map[winKey]*list.Element{}}
}

func (c *WinCache) get(k winKey) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[k]; ok {
		c.ll.MoveToFront(e)
		c.Hits++
		return e.Value.(*winEnt).data
	}
	c.Miss++
	return nil
}

func (c *WinCache) put(k winKey, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Bytes += int64(len(data))
	if int64(len(data)) > c.max {
		return
	}
	if _, ok := c.m[k]; ok {
		return
	}
	c.m[k] = c.ll.PushFront(&winEnt{k, data})
	c.used += int64(len(data))
	for c.used > c.max {
		e := c.ll.Back()
		en := e.Value.(*winEnt)
		c.ll.Remove(e)
		delete(c.m, en.key)
		c.used -= int64(len(en.data))
	}
}

// session is one decode context: resolver, bounded window cache, opened dependencies.
type session struct {
	rv    Resolver
	cache *WinCache
	mu    sync.Mutex
	open  map[ID]Stored
	aux   map[ID]*qcore.IMatrix
	gaps  map[ID][]byte
	// accounting for range reads
	payloadRead int64
	rawRead     int64
	windows     int64
}

func newSession(rv Resolver, cacheBytes int64) *session {
	if cacheBytes <= 0 {
		cacheBytes = 64 << 20
	}
	return &session{rv: rv, cache: NewWinCache(cacheBytes), open: map[ID]Stored{}, aux: map[ID]*qcore.IMatrix{}, gaps: map[ID][]byte{}}
}

func (s *session) stored(ctx context.Context, id ID) (Stored, error) {
	s.mu.Lock()
	st, ok := s.open[id]
	s.mu.Unlock()
	if ok {
		return st, nil
	}
	if s.rv == nil {
		return Stored{}, ErrMissing7
	}
	st, err := s.rv.Open(ctx, id)
	if err != nil {
		return Stored{}, err
	}
	if st.Obj == nil {
		if st.Raw == nil || st.Lay.Validate() != nil {
			return Stored{}, ErrMissing7
		}
	} else if st.Obj.FileSHA != id {
		return Stored{}, ErrMissing7
	}
	s.mu.Lock()
	s.open[id] = st
	s.mu.Unlock()
	return st, nil
}

// source returns the tensor source of a dependency at chain depth `depth` (1 = direct base).
func (s *session) source(ctx context.Context, id ID, depth int) (Source, error) {
	if depth >= MaxDepth7 {
		return nil, ErrDepth7
	}
	st, err := s.stored(ctx, id)
	if err != nil {
		return nil, err
	}
	if st.Obj != nil {
		return &objSource{s: s, o: st.Obj, depth: depth}, nil
	}
	return countingRaw{RawSource{st.Raw, st.Lay}, s}, nil
}

type countingRaw struct {
	raw RawSource
	s   *session
}

func (c countingRaw) Info(ti int) (DType, int64, int64, bool)  { return c.raw.Info(ti) }
func (c countingRaw) Gaps(ctx context.Context) ([]byte, error) { return c.raw.Gaps(ctx) }

func (c countingRaw) ReadTensor(ctx context.Context, ti int, off int64, dst []byte) error {
	c.s.mu.Lock()
	c.s.rawRead += int64(len(dst))
	c.s.mu.Unlock()
	return c.raw.ReadTensor(ctx, ti, off, dst)
}

func (s *session) imatrix(ctx context.Context, o *Object, idx int, name string, cols int) ([]float32, error) {
	if idx < 0 || idx >= len(o.Aux) {
		return nil, ErrFormat7
	}
	id := o.Aux[idx]
	s.mu.Lock()
	im, ok := s.aux[id]
	s.mu.Unlock()
	if !ok {
		if s.rv == nil {
			return nil, ErrMissing7
		}
		b, err := s.rv.Aux(ctx, id)
		if err != nil {
			return nil, err
		}
		if ID(sha256.Sum256(b)) != id {
			return nil, ErrMissing7
		}
		if im, err = qcore.ParseIMatrix(b); err != nil {
			return nil, ErrMissing7
		}
		s.mu.Lock()
		s.aux[id] = im
		s.mu.Unlock()
	}
	w := im.For(name, cols)
	if w == nil {
		return nil, ErrMissing7
	}
	return w, nil
}

// objSource decodes tensor ranges of a ZN7 object on demand, window by window, through the cache.
type objSource struct {
	s     *session
	o     *Object
	depth int
}

func (x *objSource) Info(ti int) (DType, int64, int64, bool) {
	if ti < 0 || ti >= len(x.o.T) {
		return 0, 0, 0, false
	}
	t := &x.o.T[ti]
	return t.DT, t.Len, t.Cols, true
}

func (x *objSource) ReadTensor(ctx context.Context, ti int, off int64, dst []byte) error {
	if ti < 0 || ti >= len(x.o.T) {
		return ErrRange7
	}
	t := &x.o.T[ti]
	if off < 0 || int64(len(dst)) > t.Len-off {
		return ErrRange7
	}
	wb := t.Win * int64(t.unit())
	for len(dst) > 0 {
		k := off / wb
		win, err := x.s.window(ctx, x.o, x.depth, ti, k)
		if err != nil {
			return err
		}
		n := copy(dst, win[off-k*wb:])
		dst, off = dst[n:], off+int64(n)
	}
	return nil
}

func (x *objSource) Gaps(ctx context.Context) ([]byte, error) { return x.s.gapsOf(ctx, x.o, x.depth) }

// window returns the verified decoded bytes of one window of o (cached).
func (s *session) window(ctx context.Context, o *Object, depth, ti int, k int64) ([]byte, error) {
	key := winKey{o.FileSHA, ti, k}
	if b := s.cache.get(key); b != nil {
		return b, nil
	}
	t := &o.T[ti]
	_, n := t.winBytes(k)
	out := make([]byte, n)
	if err := s.decodeWindow(ctx, o, depth, ti, k, out); err != nil {
		return nil, err
	}
	s.cache.put(key, out)
	return out, nil
}

// decodeWindow reads, rebuilds and authenticates window k of tensor ti into dst.
func (s *session) decodeWindow(ctx context.Context, o *Object, depth, ti int, k int64, dst []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t := &o.T[ti]
	wd := &t.W[k]
	pos := t.pOff
	for i := int64(0); i < k; i++ {
		pos += t.W[i].PLen
	}
	payload := make([]byte, wd.PLen)
	if err := readFull(o.r, payload, pos); err != nil {
		return err
	}
	s.mu.Lock()
	s.payloadRead += wd.PLen
	s.windows++
	s.mu.Unlock()
	base := func(ctx context.Context, lo int64, n int) (Base, error) {
		src, err := s.source(ctx, o.Bases[t.Ref.Slot].ID, depth+1)
		if err != nil {
			return Base{}, err
		}
		return readRef(ctx, src, t.Ref, lo, n, t.DT, t.Q != nil)
	}
	aux := func(ctx context.Context, idx int, name string, cols int) ([]float32, error) {
		return s.imatrix(ctx, o, idx, name, cols)
	}
	if err := rebuild(ctx, t, wd, k, payload, dst, base, aux); err != nil {
		return err
	}
	if sum16(dst) != wd.Sum {
		return ErrAuth7
	}
	return nil
}

// readRef reads base elements [lo, lo+n) of ref through src. For float targets the base must be
// a float tensor with the same element count semantics; for opaque copies lo/n are bytes of an
// identical-dtype tensor; for quant derivations the base is the float source.
func readRef(ctx context.Context, src Source, ref *Ref, lo int64, n int, tdt DType, quant bool) (Base, error) {
	dt, size, cols, ok := src.Info(int(ref.TIdx))
	if !ok {
		return Base{}, ErrMissing7
	}
	w := int64(1)
	switch {
	case tdt.IsFloat() || quant:
		if !dt.IsFloat() {
			return Base{}, ErrFormat7
		}
		w = int64(dt.Width())
	case dt != tdt:
		return Base{}, ErrFormat7
	}
	if lo < 0 || n < 0 || int64(n) > (size/w)-lo {
		return Base{}, ErrFormat7
	}
	buf := make([]byte, int64(n)*w)
	if ref.Perm.Kind == 0 {
		if err := src.ReadTensor(ctx, int(ref.TIdx), lo*w, buf); err != nil {
			return Base{}, err
		}
		return Base{DT: dt, Data: buf}, nil
	}
	if cols <= 0 || lo%cols != 0 || int64(n)%cols != 0 || (size/w)%cols != 0 {
		return Base{}, ErrFormat7
	}
	rows := size / w / cols
	rb := cols * w
	for r := int64(0); r < int64(n)/cols; r++ {
		sr := ref.Perm.srcRow(lo/cols+r, rows)
		if err := src.ReadTensor(ctx, int(ref.TIdx), sr*rb, buf[r*rb:(r+1)*rb]); err != nil {
			return Base{}, err
		}
	}
	return Base{DT: dt, Data: buf}, nil
}

// gapsOf decodes and authenticates the header/padding bytes of o.
func (s *session) gapsOf(ctx context.Context, o *Object, depth int) ([]byte, error) {
	s.mu.Lock()
	g, ok := s.gaps[o.FileSHA]
	s.mu.Unlock()
	if ok {
		return g, nil
	}
	payload := make([]byte, o.GapPLen)
	if err := readFull(o.r, payload, 4); err != nil {
		return nil, err
	}
	var out []byte
	var err error
	if o.GapMode == 0 {
		out, err = inflateExact(payload, int(o.GapLen))
	} else {
		var src Source
		if src, err = s.source(ctx, o.Bases[o.GapSlot].ID, depth+1); err != nil {
			return nil, err
		}
		var bg []byte
		if bg, err = src.Gaps(ctx); err != nil {
			return nil, err
		}
		out, err = gapUndelta(payload, bg, o.GapLen)
	}
	if err != nil {
		return nil, err
	}
	if int64(len(out)) != o.GapLen || sum16(out) != o.GapSum {
		return nil, ErrAuth7
	}
	s.mu.Lock()
	s.gaps[o.FileSHA] = out
	s.mu.Unlock()
	return out, nil
}

// Gap deltas reuse the P5 windowed delta core (dcore) over in-memory readers.
func gapWParams() dcore.WParams {
	wp := dcore.DefaultWParams()
	wp.Anchor.MaxAnchors = 1 << 18
	return wp
}

func gapDelta(gaps, base []byte) ([]byte, bool) {
	if len(base) == 0 || len(gaps) == 0 {
		return nil, false
	}
	wp := gapWParams()
	idx, err := dcore.BuildWIndex(bytes.NewReader(base), int64(len(base)), wp.Anchor, 1)
	if err != nil {
		return nil, false
	}
	var buf bytes.Buffer
	if _, err := dcore.WEncode(&buf, bytes.NewReader(gaps), int64(len(gaps)), bytes.NewReader(base), int64(len(base)), idx, wp); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func gapUndelta(rec, base []byte, n int64) ([]byte, error) {
	w, err := dcore.OpenWRecord(bytes.NewReader(rec), int64(len(rec)))
	if err != nil || w.Size != n || w.BaseSize != int64(len(base)) {
		return nil, ErrFormat7
	}
	var buf bytes.Buffer
	buf.Grow(int(n))
	if _, err := w.Decode(&buf, bytes.NewReader(base), gapWParams()); err != nil {
		return nil, ErrFormat7
	}
	return buf.Bytes(), nil
}
