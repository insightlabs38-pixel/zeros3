package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	satReplaySlots   = 2
	satReplayWaiters = 128
	satFollowers     = 64
)

type satReplayKey struct {
	bucket, key, manifest string
	rep                   [32]byte
}

type satFlight struct {
	key      satReplayKey
	done     chan struct{}
	cancel   context.CancelFunc
	refs     int
	finished bool
	data     []byte
	err      error
}

type satReplayStats struct {
	Producers, Joined, WaitersHigh, ActiveHigh, PlaysCanceled int
	AdmissionNS                                               int64
}

type satReplay struct {
	mu              sync.Mutex
	merge           bool
	changed         chan struct{}
	jobs            map[satReplayKey]*satFlight
	admission       chan struct{} // FIFO control uses the Go channel's blocked senders
	active, waiting int
	stats           satReplayStats
}

func (s *satReplay) retireLocked() {
	s.active--
	if s.admission != nil {
		<-s.admission
	}
}

var errSatQueue = errors.New("experimental replay queue is full")

func newSatReplay(merge bool) *satReplay {
	return &satReplay{merge: merge, changed: make(chan struct{}), jobs: make(map[satReplayKey]*satFlight)}
}

func (s *satReplay) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *satReplay) acquire(ctx context.Context, key satReplayKey, work func(context.Context) ([]byte, error)) (*satFlight, error) {
	s.mu.Lock()
	if s.waiting >= satReplayWaiters {
		s.mu.Unlock()
		return nil, errSatQueue
	}
	s.waiting++
	s.stats.WaitersHigh = max(s.stats.WaitersHigh, s.waiting)
	started := time.Now()
	defer func() {
		s.mu.Lock()
		s.waiting--
		s.stats.AdmissionNS += time.Since(started).Nanoseconds()
		s.mu.Unlock()
	}()
	if s.admission != nil {
		s.mu.Unlock()
		select {
		case s.admission <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		s.mu.Lock()
	}
	for {
		if err := ctx.Err(); err != nil {
			if s.admission != nil {
				<-s.admission
			}
			s.mu.Unlock()
			return nil, err
		}
		if f := s.jobs[key]; s.merge && f != nil && f.refs < satFollowers {
			f.refs++
			s.stats.Joined++
			s.mu.Unlock()
			return f, nil
		}
		if s.active < satReplaySlots && (!s.merge || s.jobs[key] == nil) {
			runCtx, cancel := context.WithCancel(context.Background())
			f := &satFlight{key: key, done: make(chan struct{}), cancel: cancel, refs: 1}
			s.active++
			s.stats.Producers++
			s.stats.ActiveHigh = max(s.stats.ActiveHigh, s.active)
			if s.merge {
				s.jobs[key] = f
			}
			s.signalLocked()
			s.mu.Unlock()
			go s.run(runCtx, f, work)
			return f, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
			s.mu.Lock()
		}
	}
}

func (s *satReplay) run(ctx context.Context, f *satFlight, work func(context.Context) ([]byte, error)) {
	data, err := work(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	f.data, f.err, f.finished = data, err, true
	f.cancel()
	if s.jobs[f.key] == f {
		delete(s.jobs, f.key)
	}
	if f.refs == 0 {
		s.retireLocked()
	}
	close(f.done)
	s.signalLocked()
}

func (s *satReplay) release(f *satFlight) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f.refs--
	if f.refs == 0 {
		if s.jobs[f.key] == f {
			delete(s.jobs, f.key)
		}
		if f.finished {
			s.retireLocked()
		} else {
			f.cancel()
			s.stats.PlaysCanceled++
		}
	}
	s.signalLocked()
}

type satReplayReader struct {
	f      *satFlight
	replay *satReplay
	pos    int
}

func (r *satReplayReader) next() ([]byte, error) {
	if r.pos == len(r.f.data) {
		return nil, io.EOF
	}
	at := r.pos
	r.pos = min(at+repStreamPiece, len(r.f.data))
	return r.f.data[at:r.pos], nil
}
func (r *satReplayReader) close() {
	if r.f != nil {
		r.replay.release(r.f)
		r.f = nil
	}
}

func (s *satReplay) open(ctx context.Context, key satReplayKey, work func(context.Context) ([]byte, error)) (*satReplayReader, error) {
	f, err := s.acquire(ctx, key, work)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		s.release(f)
		return nil, ctx.Err()
	case <-f.done:
		if err := ctx.Err(); err != nil {
			s.release(f)
			return nil, err
		}
		if f.err != nil {
			s.release(f)
			return nil, f.err
		}
		return &satReplayReader{f: f, replay: s}, nil
	}
}

func (s *satReplay) serve(store *Store, ctx context.Context, bucket, key string, man *servedManifest) (objectReader, error) {
	if man.man.TotalLength <= 0 || man.man.TotalLength > bitPatchMaxObject || !store.authMay() {
		return nil, nil
	}
	id, err := decodeHexSHA256(man.man.ObjectSHA256)
	if err != nil {
		return nil, nil
	}
	rec, err := store.authLoad(id)
	if err != nil || rec == nil || rec.Form != authFormRep || rec.Root == nil || len(rec.Bases) != 0 ||
		rec.Root.ObjSHA != id || rec.Size != man.man.TotalLength || rec.Root.ObjSize != rec.Size {
		return nil, nil
	}
	root := rec.Root
	rep := store.repCache().getRep(root.Rep)
	if rep == nil {
		src := store.newRepSource(root.Descs, nil)
		env := &repEnv{Src: src, Limits: repProdLimits, Cache: store.repCache()}
		call, err := env.open(root.Rep)
		src.close()
		if err != nil {
			return nil, nil
		}
		rep = call.Rep
	}
	if rep.Kind != repKindDeflateTranslate || rep.Size != rec.Size {
		return nil, nil
	}
	return s.open(ctx, satReplayKey{bucket, key, man.man.ManifestUUID, root.Rep}, func(ctx context.Context) ([]byte, error) {
		src := store.newRepSource(root.Descs, nil)
		defer src.close()
		env := &repEnv{Src: src, Limits: repProdLimits, Cache: store.repCache(), replayCtx: ctx}
		var data bytes.Buffer
		data.Grow(int(root.ObjSize))
		n, sha, err := env.reconstruct(root.Rep, &data)
		if err != nil {
			return nil, err
		}
		if n != root.ObjSize || sha != root.ObjSHA {
			return nil, fmt.Errorf("%w: reconstructed replay differs", errRepMismatch)
		}
		return data.Bytes(), nil
	})
}
