// pubsim: Z4-P1B Checkpoint A design comparison. Simulates the durable
// publication of small objects (one loose chunk + one manifest + one journal
// frame per PUT) with the exact filesystem operations of zeros3's PUT path,
// parametrized by the synchronization design. It does not touch zeros3.go.
//
//	pubsim -dir DIR -c 8 -n 4000 -size 4096 -lock global|pending -dsync each|coalesce -journal serial|group|none -fsync file|syncfs
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ---- round-based coalescing syncer (group commit with no artificial delay) ----
type round struct {
	done chan struct{}
	err  error
}
type syncer struct {
	mu      sync.Mutex
	cur     *round // round still accepting joiners (not started)
	prev    *round // last round handed to a leader
	do      func() error
	started atomic.Int64
}

func (s *syncer) sync() error {
	s.mu.Lock()
	if s.cur != nil { // join the round that has not started yet
		r := s.cur
		s.mu.Unlock()
		<-r.done
		return r.err
	}
	r := &round{done: make(chan struct{})}
	s.cur = r
	prev := s.prev
	s.prev = r
	s.mu.Unlock()
	if prev != nil {
		<-prev.done // at most one sync in flight
	}
	s.mu.Lock()
	s.cur = nil // close the join window: later arrivals form the next round
	s.mu.Unlock()
	s.started.Add(1)
	r.err = s.do()
	close(r.done)
	return r.err
}

type dirSyncers struct {
	mu sync.Mutex
	m  map[string]*syncer
}

func (d *dirSyncers) get(dir string) *syncer {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.m[dir]
	if s == nil {
		s = &syncer{do: func() error { return fsyncPath(dir) }}
		d.m[dir] = s
	}
	return s
}

func fsyncPath(p string) error {
	if *nofsync {
		return nil
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

var syncfsFD int
var rootFile *os.File

func syncfs() error {
	_, _, e := syscall.Syscall(306 /* SYS_syncfs amd64 */, uintptr(syncfsFD), 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

var (
	dir       = flag.String("dir", "", "")
	conc      = flag.Int("c", 8, "")
	nops      = flag.Int("n", 4000, "")
	size      = flag.Int("size", 4096, "")
	lockMode  = flag.String("lock", "global", "global|pending")
	dsync     = flag.String("dsync", "each", "each|coalesce")
	jmode     = flag.String("journal", "serial", "serial|group|none")
	fsyncM    = flag.String("fsync", "file", "file|syncfs")
	predir    = flag.Bool("predirs", false, "pre-create all 256 aa dirs")
	nofsync   = flag.Bool("nofsync", false, "diagnostic: skip every fsync (syscall-cost ceiling; NOT durable)")
	tmpshards = flag.Int("tmpshards", 1, "number of tmp subdirectories")
	par       = flag.Bool("par", false, "run chunk chain and manifest chain in parallel")
)

var (
	casPubMu sync.RWMutex
	ds       = &dirSyncers{m: map[string]*syncer{}}
	jmu      sync.Mutex
	jf       *os.File
	joff     int64
	jsync    *syncer
	smu      sync.Mutex // stands in for Store.mu held across journal commit
	fsyncs   atomic.Int64
	mkdirs   atomic.Int64
)

func syncDir(d string) error {
	fsyncs.Add(1)
	if *dsync == "coalesce" {
		return ds.get(d).sync()
	}
	return fsyncPath(d)
}

func fileSync(f *os.File) error {
	if *fsyncM == "syncfs" {
		return barrier.sync() // data barrier before the rename (shared by the whole group)
	}
	fsyncs.Add(1)
	if *nofsync {
		return nil
	}
	return f.Sync()
}

var barrier = &syncer{do: syncfs}

func mk(d string) (bool, error) {
	if err := os.Mkdir(d, 0o755); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, err
	}
	mkdirs.Add(1)
	return true, nil
}

var tmpCtr atomic.Int64

func stage(tmpDir string, data []byte) (string, error) {
	if *tmpshards > 1 {
		tmpDir = filepath.Join(tmpDir, fmt.Sprintf("s%d", tmpCtr.Add(1)%int64(*tmpshards)))
	}
	f, err := os.CreateTemp(tmpDir, "cas-*.tmp")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	if err := fileSync(f); err != nil {
		return "", err
	}
	return f.Name(), f.Close()
}

var pending sync.Map // digest -> struct{} (publication in flight)

func publish(root, tmp string, sum [32]byte) error {
	h := hex.EncodeToString(sum[:])
	aa := filepath.Join(root, "chunks", h[:2])
	bb := filepath.Join(aa, h[2:4])
	final := filepath.Join(bb, h)
	if *lockMode == "global" {
		casPubMu.Lock()
		defer casPubMu.Unlock()
	}
	madeAA, _ := mk(aa)
	if madeAA {
		if err := syncDir(filepath.Join(root, "chunks")); err != nil {
			return err
		}
	}
	madeBB, err := mk(bb)
	if err != nil {
		return err
	}
	if madeBB {
		if err := syncDir(aa); err != nil {
			return err
		}
	}
	if *lockMode == "pending" {
		pending.Store(sum, struct{}{})
		defer pending.Delete(sum)
	}
	if err := os.Rename(tmp, final); err != nil {
		return err
	}
	if *fsyncM == "syncfs" {
		return barrier.sync() // dir-entry barrier after the rename
	}
	return syncDir(bb)
}

func manifest(root string, id string, data []byte) error {
	tmp, err := stage(filepath.Join(root, "tmp"), data)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(root, "manifests", id+".json")); err != nil {
		return err
	}
	if *fsyncM == "syncfs" {
		return barrier.sync()
	}
	return syncDir(filepath.Join(root, "manifests"))
}

func journalAppend(frame []byte) error {
	switch *jmode {
	case "none":
		return nil
	case "serial":
		smu.Lock()
		defer smu.Unlock()
		jmu.Lock()
		_, err := jf.WriteAt(frame, joff)
		joff += int64(len(frame))
		if err == nil {
			fsyncs.Add(1)
			if !*nofsync {
				err = jf.Sync()
			}
		}
		jmu.Unlock()
		return err
	default: // group: write under the lock, share the sync
		jmu.Lock()
		_, err := jf.WriteAt(frame, joff)
		joff += int64(len(frame))
		jmu.Unlock()
		if err != nil {
			return err
		}
		fsyncs.Add(1)
		return jsync.sync()
	}
}

func main() {
	flag.Parse()
	root := *dir
	for _, d := range []string{"chunks", "tmp", "manifests"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	for i := 0; i < *tmpshards; i++ {
		os.MkdirAll(filepath.Join(root, "tmp", fmt.Sprintf("s%d", i)), 0o755)
	}
	if *predir {
		for i := 0; i < 256; i++ {
			os.Mkdir(filepath.Join(root, "chunks", fmt.Sprintf("%02x", i)), 0o755)
		}
	}
	var err error
	jf, err = os.OpenFile(filepath.Join(root, "visibility.log"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		panic(err)
	}
	jsync = &syncer{do: func() error {
		if *nofsync {
			return nil
		}
		return jf.Sync()
	}}
	rootFile, _ = os.Open(root)
	syncfsFD = int(rootFile.Fd())
	lat := make([][]float64, *conc)
	var wg sync.WaitGroup
	var failed atomic.Int64
	t0 := time.Now()
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)*7919 + 1))
			for i := w; i < *nops; i += *conc {
				data := make([]byte, *size)
				rng.Read(data)
				sum := sha256.Sum256(data)
				st := time.Now()
				man := make([]byte, 700)
				rng.Read(man)
				chunkChain := func() error {
					tmp, err := stage(filepath.Join(root, "tmp"), data)
					if err == nil {
						err = publish(root, tmp, sum)
					}
					return err
				}
				manChain := func() error { return manifest(root, fmt.Sprintf("m-%d-%d", w, i), man) }
				var err error
				if *par {
					ch := make(chan error, 1)
					go func() { ch <- manChain() }()
					err = chunkChain()
					if e := <-ch; err == nil {
						err = e
					}
				} else {
					if err = chunkChain(); err == nil {
						err = manChain()
					}
				}
				if err == nil {
					err = journalAppend(make([]byte, 250))
				}
				if err != nil {
					failed.Add(1)
					fmt.Fprintln(os.Stderr, err)
					return
				}
				lat[w] = append(lat[w], time.Since(st).Seconds()*1000)
			}
		}(w)
	}
	wg.Wait()
	el := time.Since(t0).Seconds()
	var all []float64
	for _, l := range lat {
		all = append(all, l...)
	}
	sort.Float64s(all)
	p := func(q float64) float64 { return all[int(q*float64(len(all)-1))] }
	fmt.Printf("lock=%s dsync=%s journal=%s fsync=%s predirs=%v par=%v tmpshards=%d c=%d n=%d ops/s=%.0f p50=%.2f p99=%.2f fsync-calls/op=%.2f mkdir/op=%.2f failed=%d\n",
		*lockMode, *dsync, *jmode, *fsyncM, *predir, *par, *tmpshards, *conc, len(all), float64(len(all))/el, p(.5), p(.99),
		float64(fsyncs.Load())/float64(len(all)), float64(mkdirs.Load())/float64(len(all)), failed.Load())
}
