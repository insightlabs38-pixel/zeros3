package main

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"zeros3-z3/carz3"
)

// heapPeak samples HeapAlloc until stop() and returns the maximum seen.
type heapPeak struct {
	max  atomic.Uint64
	done chan struct{}
	fin  chan struct{}
}

func startPeak() *heapPeak {
	h := &heapPeak{done: make(chan struct{}), fin: make(chan struct{})}
	go func() {
		defer close(h.fin)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > h.max.Load() {
				h.max.Store(m.HeapAlloc)
			}
			select {
			case <-h.done:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	return h
}

func (h *heapPeak) stop() uint64 { close(h.done); <-h.fin; return h.max.Load() }

func genCAR(path string, mib int, block int) (n int, err error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	rng := rand.New(rand.NewPCG(99, 14))
	buf := make([]byte, block)
	total := int64(mib) << 20
	var written int64
	hdr := carz3.HeaderBody([][]byte{carz3.CIDv1(carz3.CodecRaw, carz3.MhSHA2_256, []byte("root"))}, true)
	err = carz3.WriteV1(f, hdr, func() (carz3.Block, bool) {
		if written >= total {
			return carz3.Block{}, false
		}
		for i := 0; i+8 <= len(buf); i += 8 {
			v := rng.Uint64()
			for k := 0; k < 8; k++ {
				buf[i+k] = byte(v >> (8 * k))
			}
		}
		written += int64(block)
		n++
		return carz3.NewBlock(carz3.CodecRaw, buf), true
	})
	return n, err
}

// cmdStream shows that header + block iteration + component emission use
// memory independent of the archive size (apart from the per-section ref list
// when a recipe is retained).
func cmdStream(args []string) error {
	fs := flag.NewFlagSet("stream", flag.ExitOnError)
	mib := fs.Int("mib", 512, "archive size to generate (MiB)")
	block := fs.Int("block", 256<<10, "block size")
	dir := fs.String("dir", os.TempDir(), "scratch directory for the generated CAR")
	fs.Parse(args)
	path := filepath.Join(*dir, fmt.Sprintf("z3car-stream-%d-%d.car", *mib, *block))
	defer os.Remove(path)
	t0 := time.Now()
	n, err := genCAR(path, *mib, *block)
	if err != nil {
		return err
	}
	st, _ := os.Stat(path)
	fmt.Printf("generated %s: %d MiB, %d blocks of %d B in %v\n\n", filepath.Base(path), st.Size()>>20, n, *block, time.Since(t0).Round(time.Millisecond))
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	row := func(name string, bytes int64, peak uint64, d time.Duration, extra string) {
		fmt.Printf("| %s | %.0f | %.1f | %.2f | %s |\n", name, float64(bytes)/(1<<20)/d.Seconds(), float64(peak)/(1<<20), float64(peak)/float64(bytes)*100, extra)
	}
	fmt.Printf("| phase | MiB/s | peak heap MiB | peak heap / archive %% | note |\n|---|--:|--:|--:|---|\n")

	// 1. header + stream blocks, hash each, emit (sum, len) to a counting sink; no set.
	runtime.GC()
	pk := startPeak()
	t0 = time.Now()
	rd, err := carz3.NewReader(io.NewSectionReader(f, 0, st.Size()), carz3.DefaultLimits())
	if err != nil {
		return err
	}
	var emitted int
	var blockBytes int64
	for {
		e, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		_ = sha256.Sum256(e.Data)
		emitted++
		blockBytes += int64(len(e.Data))
	}
	row("stream: header, sections, SHA-256, emit refs (no retention)", st.Size(), pk.stop(), time.Since(t0), fmt.Sprintf("%d refs emitted", emitted))

	// 2. build the retained recipe (refs only).
	runtime.GC()
	pk = startPeak()
	t0 = time.Now()
	bo := carz3.DefaultOptions()
	bo.NoDupStat = true
	rep, bst, err := carz3.Build(f, st.Size(), bo)
	if err != nil {
		return err
	}
	_, sz := rep.Marshal()
	row("Build: retained recipe (one Ref per section)", st.Size(), pk.stop(), time.Since(t0), fmt.Sprintf("%d refs, recipe %d B (%.4f%% of archive), %d derived CIDs", len(rep.Refs), sz.Total, 100*float64(sz.Total)/float64(st.Size()), bst.Derived))

	// 3. streaming reconstruct to a hash, components read back from the source file.
	where := map[[32]byte][2]int64{}
	for _, r := range rep.Refs {
		where[r.Sum] = [2]int64{r.SrcOff, r.Len}
	}
	get := func(s [32]byte) ([]byte, error) {
		w := where[s]
		b := make([]byte, w[1])
		_, err := f.ReadAt(b, w[0])
		return b, err
	}
	runtime.GC()
	pk = startPeak()
	t0 = time.Now()
	h := sha256.New()
	if err := rep.Reconstruct(h, get); err != nil {
		return err
	}
	got := h.Sum(nil)
	row("Reconstruct: recipe + components -> hash", st.Size(), pk.stop(), time.Since(t0), "")
	// compare with a straight hash of the file
	h2 := sha256.New()
	f.Seek(0, 0)
	io.Copy(h2, f)
	if string(got) != string(h2.Sum(nil)) {
		return fmt.Errorf("streamed reconstruction differs from the file")
	}
	fmt.Printf("\nreconstructed SHA-256 equals the file's SHA-256 (%x...)\n", got[:6])
	return nil
}
