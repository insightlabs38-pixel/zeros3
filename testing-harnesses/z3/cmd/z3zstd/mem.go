package main

import (
	"bufio"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"zeros3-z3/zstdz3"
)

// peak samples the Go heap while a phase runs and reads the kernel's high-water RSS.
type peak struct {
	heap atomic.Uint64
	stop chan struct{}
}

func startPeak() *peak {
	p := &peak{stop: make(chan struct{})}
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-p.stop:
				return
			case <-time.After(5 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > p.heap.Load() {
					p.heap.Store(m.HeapAlloc)
				}
			}
		}
	}()
	return p
}

func (p *peak) finish() (heapMiB, rssMiB float64) {
	close(p.stop)
	rss := 0.0
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			var kb float64
			if _, err := fmt.Sscanf(l, "VmHWM: %f kB", &kb); err == nil {
				rss = kb / 1024
			}
		}
	}
	return float64(p.heap.Load()) / (1 << 20), rss
}

// cmdMemGen writes a deterministic, compressible stream of the given size: text, JSON
// lines and fixed-width records with some incompressible runs, so blocks of every type occur.
func cmdMemGen(args []string) {
	fs := flag.NewFlagSet("memgen", flag.ExitOnError)
	mib := fs.Int("mib", 64, "plaintext MiB")
	out := fs.String("out", "", "output file")
	fs.Parse(args)
	f, err := os.Create(*out)
	must(err)
	w := bufio.NewWriterSize(f, 1<<20)
	r := rand.New(rand.NewSource(25))
	words := make([]string, 4000)
	for i := range words {
		b := make([]byte, 3+r.Intn(8))
		for j := range b {
			b[j] = byte('a' + r.Intn(26))
		}
		words[i] = string(b)
	}
	target := int64(*mib) << 20
	var n int64
	rnd := make([]byte, 64<<10)
	for n < target {
		switch k := r.Intn(10); {
		case k < 5:
			for i := 0; i < 400; i++ {
				fmt.Fprintf(w, "%s %s %s, ", words[r.Intn(len(words))], words[r.Intn(len(words))], words[r.Intn(len(words))])
			}
		case k < 8:
			for i := 0; i < 200; i++ {
				fmt.Fprintf(w, "{\"id\":%d,\"k\":\"%s\",\"v\":%d}\n", r.Intn(1<<20), words[r.Intn(len(words))], r.Intn(1000))
			}
		case k < 9:
			for i := 0; i < 300; i++ {
				fmt.Fprintf(w, "REC%09d|%-10s|%08d|XXXX\n", r.Intn(1<<30), words[r.Intn(50)], i)
			}
		default:
			r.Read(rnd[:4096+r.Intn(len(rnd)-4096)])
			w.Write(rnd[:4096+r.Intn(len(rnd)-4096)])
		}
		n = 0
		if st, err := f.Stat(); err == nil {
			n = st.Size() + int64(w.Buffered())
		}
	}
	must(w.Flush())
	must(f.Close())
}

// cmdMemParse parses one file with the streaming API and reports peak memory.
func cmdMemParse(args []string) {
	fs := flag.NewFlagSet("memparse", flag.ExitOnError)
	in := fs.String("in", "", "zstd file")
	pl := fs.String("plain", "", "plaintext output (empty: hash only)")
	un := fs.String("units", "", "unit stream output")
	sk := fs.String("skel", "", "skeleton output")
	nomodel := fs.Bool("nomodel", false, "explicit sequences")
	fs.Parse(args)
	f, err := os.Open(*in)
	must(err)
	st, _ := f.Stat()
	var plain io.Writer = io.Discard
	var pf, uf *os.File
	var pw, uw *bufio.Writer
	h := sha256.New()
	if *pl != "" {
		pf, err = os.Create(*pl)
		must(err)
		pw = bufio.NewWriterSize(pf, 1<<20)
		plain = io.MultiWriter(pw, h)
	} else {
		plain = h
	}
	var units io.Writer = io.Discard
	if *un != "" {
		uf, err = os.Create(*un)
		must(err)
		uw = bufio.NewWriterSize(uf, 1<<20)
		units = uw
	}
	pk := startPeak()
	t0 := time.Now()
	res, err := zstdz3.Parse(f, plain, units, zstdz3.Options{NoSeqModel: *nomodel})
	must(err)
	el := time.Since(t0)
	heap, rss := pk.finish()
	if pw != nil {
		must(pw.Flush())
		must(pf.Close())
	}
	if uw != nil {
		must(uw.Flush())
		must(uf.Close())
	}
	if *sk != "" {
		must(os.WriteFile(*sk, res.Raw, 0o644))
	}
	s := res.Stats
	fmt.Printf("parse %s: %.0f MiB zstd -> %.0f MiB plain (%x) in %v = %.1f MiB/s of plaintext; peak heap %.1f MiB, peak RSS %.1f MiB; IR %.2f MiB (skeleton %d units %d), blocks %v\n",
		*in, float64(st.Size())/(1<<20), float64(s.PlainBytes)/(1<<20), h.Sum(nil)[:6], el.Round(time.Millisecond), float64(s.PlainBytes)/(1<<20)/el.Seconds(), heap, rss, float64(s.IRBytes())/(1<<20), s.SkeletonBytes, s.UnitBytes, s.Blocks)
}

// cmdMemReplay replays from the skeleton, unit stream and plaintext files.
func cmdMemReplay(args []string) {
	fs := flag.NewFlagSet("memreplay", flag.ExitOnError)
	in := fs.String("in", "", "original zstd file (compared by SHA-256 only)")
	pl := fs.String("plain", "", "plaintext")
	un := fs.String("units", "", "unit stream")
	sk := fs.String("skel", "", "skeleton")
	fs.Parse(args)
	skel, err := os.ReadFile(*sk)
	must(err)
	pf, err := os.Open(*pl)
	must(err)
	uf, err := os.Open(*un)
	must(err)
	h := sha256.New()
	pk := startPeak()
	t0 := time.Now()
	n, err := zstdz3.Replay(h, skel, bufio.NewReaderSize(uf, 1<<20), bufio.NewReaderSize(pf, 1<<20), zstdz3.Limits{})
	must(err)
	el := time.Since(t0)
	heap, rss := pk.finish()
	want := fileSHA(*in)
	got := fmt.Sprintf("%x", h.Sum(nil))
	st, _ := pf.Stat()
	fmt.Printf("replay %s: %.0f MiB zstd (sha %s) in %v = %.1f MiB/s of plaintext, %.1f MiB/s of output; peak heap %.1f MiB, peak RSS %.1f MiB\n",
		*in, float64(n)/(1<<20), map[bool]string{true: "EXACT", false: "DIFFERENT"}[got == want], el.Round(time.Millisecond),
		float64(st.Size())/(1<<20)/el.Seconds(), float64(n)/(1<<20)/el.Seconds(), heap, rss)
	if got != want {
		os.Exit(1)
	}
}
