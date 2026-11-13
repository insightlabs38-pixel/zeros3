package numcore

import (
	"context"
	"crypto/sha256"
	"io"
	"sync"
)

// DecOpts bounds replay resources.
type DecOpts struct {
	Workers    int   // parallel window decoders (default 1)
	CacheBytes int64 // decoded base-window cache (default 64 MiB)
}

// DecStats reports what a decode touched.
type DecStats struct {
	Windows      int64
	PayloadBytes int64 // bytes read from ZN7 objects (this object and derived bases)
	RawBaseBytes int64 // bytes read from raw (original) bases
	DirBytes     int64
	CacheHits    int64
	CacheMiss    int64
}

func (s *session) stats(o *Object) DecStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return DecStats{Windows: s.windows, PayloadBytes: s.payloadRead, RawBaseBytes: s.rawRead, DirBytes: o.DirLen + zTrailLen + 4,
		CacheHits: s.cache.Hits, CacheMiss: s.cache.Miss}
}

// DecodeTo streams the original file to w. Every window and the gap section are authenticated
// before their bytes are written; the whole-file SHA-256 is checked at the end (ErrDigest7).
func (o *Object) DecodeTo(ctx context.Context, w io.Writer, rv Resolver, opt DecOpts) (DecStats, error) {
	s := newSession(rv, opt.CacheBytes)
	gaps, err := s.gapsOf(ctx, o, 0)
	if err != nil {
		return s.stats(o), err
	}
	h := sha256.New()
	emit := func(b []byte) error {
		h.Write(b)
		n, err := w.Write(b)
		if err == nil && n != len(b) {
			err = io.ErrShortWrite
		}
		return err
	}
	workers := opt.Workers
	if workers < 1 {
		workers = 1
	}
	type job struct {
		ti  int
		k   int64
		out []byte
		err error
		wg  sync.WaitGroup
	}
	jobs := make(chan *job, workers)
	order := make(chan *job, 2*workers)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				t := &o.T[j.ti]
				_, n := t.winBytes(j.k)
				j.out = make([]byte, n)
				j.err = s.decodeWindow(ctx, o, 0, j.ti, j.k, j.out)
				j.wg.Done()
			}
		}()
	}
	// producer: file order; gaps are interleaved by the writer below
	type item struct {
		gap []byte
		j   *job
	}
	items := make(chan item, 2*workers)
	go func() {
		defer close(items)
		defer close(jobs)
		gp, prev := int64(0), int64(0)
		for ti := range o.T {
			t := &o.T[ti]
			if g := t.Off - prev; g > 0 {
				items <- item{gap: gaps[gp : gp+g]}
				gp += g
			}
			prev = t.Off + t.Len
			for k := range t.W {
				j := &job{ti: ti, k: int64(k)}
				j.wg.Add(1)
				select {
				case jobs <- j:
				case <-ctx.Done():
					return
				}
				items <- item{j: j}
			}
		}
		if g := o.FileLen - prev; g > 0 {
			items <- item{gap: gaps[gp : gp+g]}
		}
	}()
	_ = order
	var first error
	for it := range items {
		if first != nil {
			continue
		}
		if it.j == nil {
			first = emit(it.gap)
		} else {
			it.j.wg.Wait()
			if first = it.j.err; first == nil {
				first = emit(it.j.out)
			}
		}
		if first != nil {
			cancel()
		}
	}
	wg.Wait()
	st := s.stats(o)
	if first != nil {
		return st, first
	}
	var got ID
	h.Sum(got[:0])
	if got != o.FileSHA {
		return st, ErrDigest7
	}
	return st, nil
}

// ReadRange returns file bytes [off, off+n) decoding only the covering windows (each verified).
func (o *Object) ReadRange(ctx context.Context, off, n int64, rv Resolver, opt DecOpts) ([]byte, DecStats, error) {
	s := newSession(rv, opt.CacheBytes)
	if off < 0 || n < 0 || n > o.FileLen-off {
		return nil, s.stats(o), ErrRange7
	}
	out := make([]byte, n)
	end := off + n
	var gaps []byte
	gp, prev := int64(0), int64(0)
	fill := func(lo, hi int64, src func(a, b int64) ([]byte, error)) error { // file span [lo,hi)
		a, b := lo, hi
		if a < off {
			a = off
		}
		if b > end {
			b = end
		}
		if a >= b {
			return nil
		}
		d, err := src(a-lo, b-lo)
		if err != nil {
			return err
		}
		copy(out[a-off:], d)
		return nil
	}
	gapSrc := func(base int64) func(a, b int64) ([]byte, error) {
		return func(a, b int64) ([]byte, error) {
			if gaps == nil {
				var err error
				if gaps, err = s.gapsOf(ctx, o, 0); err != nil {
					return nil, err
				}
				s.mu.Lock()
				s.payloadRead += o.GapPLen
				s.mu.Unlock()
			}
			return gaps[base+a : base+b], nil
		}
	}
	for ti := 0; ti <= len(o.T); ti++ {
		tOff := o.FileLen
		if ti < len(o.T) {
			tOff = o.T[ti].Off
		}
		if tOff > prev {
			if err := fill(prev, tOff, gapSrc(gp)); err != nil {
				return nil, s.stats(o), err
			}
			gp += tOff - prev
		}
		if ti == len(o.T) || tOff >= end {
			break
		}
		t := &o.T[ti]
		prev = t.Off + t.Len
		if prev <= off {
			continue
		}
		wb := t.Win * int64(t.unit())
		for k := range t.W {
			lo := t.Off + int64(k)*wb
			hi := lo + wb
			if hi > prev {
				hi = prev
			}
			if hi <= off || lo >= end {
				continue
			}
			err := fill(lo, hi, func(a, b int64) ([]byte, error) {
				win, err := s.window(ctx, o, 0, ti, int64(k))
				if err != nil {
					return nil, err
				}
				return win[a:b], nil
			})
			if err != nil {
				return nil, s.stats(o), err
			}
		}
	}
	return out, s.stats(o), nil
}
