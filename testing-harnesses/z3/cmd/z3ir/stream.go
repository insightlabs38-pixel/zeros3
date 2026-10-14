package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"zeros3-z3/corpus"
	"zeros3-z3/deflate"
	"zeros3-z3/ir"
)

// streamCmd: Z3-08G bounded-memory proof. A deterministic gzip stream of any
// size is produced on the fly (a base file's segments recombined, compressed by
// a pipe), encoded by ir.EncodeStream into on-disk plaintext/unit files and
// rebuilt by ir.ReconstructStream; neither side ever holds the object.
func streamCmd(args []string) {
	fs := flag.NewFlagSet("stream", flag.ExitOnError)
	base := fs.String("base", "", "base plaintext file the stream is assembled from (required; first 16 MiB used)")
	sizes := fs.String("mib", "64,256", "plaintext sizes in MiB, comma separated")
	prod := fs.String("producer", "gzip6", "gzip1..gzip9 (GNU gzip, external) or go1..go9 (compress/gzip)")
	profile := fs.Int("profile", int(ir.AutoProfile), "predictor profile (255 = adaptive)")
	target := fs.Int("target", 16<<10, "unit target bytes")
	tmp := fs.String("tmp", os.TempDir(), "scratch directory for the plaintext and unit files")
	fs.Parse(args)
	if *base == "" {
		fatal(fmt.Errorf("-base is required"))
	}
	bb, err := os.ReadFile(*base)
	if err != nil {
		fatal(err)
	}
	if len(bb) > 16<<20 {
		bb = bb[:16<<20]
	}
	fmt.Println("| plaintext MiB | producer | compressed MiB | encode MiB/s | peak heap in use | peak RSS | decode MiB/s | peak heap in use (decode) | ring | skeleton | units | unit bytes | blocks | exact |")
	fmt.Println("|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|")
	for _, ss := range split(*sizes) {
		mib, _ := strconv.Atoi(ss)
		runStream(bb, mib, *prod, uint8(*profile), *target, *tmp)
	}
}

// genReader yields n bytes assembled from random segments of base, with fresh
// text spliced in so that not everything is a long-range copy.
type genReader struct {
	base []byte
	left int64
	r    *corpus.RNG
	seg  []byte
	n    uint64
	sum  hash.Hash
}

func (g *genReader) Read(p []byte) (int, error) {
	if g.left <= 0 {
		return 0, io.EOF
	}
	if len(g.seg) == 0 {
		switch g.r.Intn(8) {
		case 0:
			g.n++
			g.seg = []byte(fmt.Sprintf("// generated block %d %x %x\n", g.n, g.r.U64(), g.r.U64()))
		default:
			ln := 4096 + g.r.Intn(120<<10)
			off := g.r.Intn(len(g.base) - ln)
			g.seg = g.base[off : off+ln]
		}
	}
	n := copy(p, g.seg)
	n = int(min(int64(n), g.left))
	g.seg = g.seg[n:]
	g.left -= int64(n)
	g.sum.Write(p[:n])
	return n, nil
}

type memMeter struct {
	mu        sync.Mutex
	heapInuse uint64
	sys       uint64
	stop      chan struct{}
	done      chan struct{}
}

func startMeter() *memMeter {
	m := &memMeter{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(m.done)
		t := time.NewTicker(40 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
				var s runtime.MemStats
				runtime.ReadMemStats(&s)
				m.mu.Lock()
				m.heapInuse = max(m.heapInuse, s.HeapInuse)
				m.sys = max(m.sys, s.Sys)
				m.mu.Unlock()
			}
		}
	}()
	return m
}

func (m *memMeter) finish() (heap, sys uint64) {
	close(m.stop)
	<-m.done
	return m.heapInuse, m.sys
}

func vmHWM() string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "n/a"
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				kb, _ := strconv.Atoi(f[1])
				return fmt.Sprintf("%.1f MiB", float64(kb)/1024)
			}
		}
	}
	return "n/a"
}

func mb(v uint64) string { return fmt.Sprintf("%.1f MiB", float64(v)/(1<<20)) }

type fileSink struct {
	plain *bufio.Writer
	units *bufio.Writer
	psum  hash.Hash
	pcrc  uint32
	pn    int64
}

func (f *fileSink) Plain(p []byte) error {
	f.psum.Write(p)
	f.pcrc = crc32.Update(f.pcrc, crc32.IEEETable, p)
	f.pn += int64(len(p))
	_, err := f.plain.Write(p)
	return err
}

func (f *fileSink) Unit(i int, d []byte) error {
	var l [4]byte
	binary.LittleEndian.PutUint32(l[:], uint32(len(d)))
	f.units.Write(l[:])
	_, err := f.units.Write(d)
	return err
}

func runStream(base []byte, mib int, prod string, profile uint8, target int, tmp string) {
	dir, err := os.MkdirTemp(tmp, "z3stream-")
	if err != nil {
		fatal(err)
	}
	defer os.RemoveAll(dir)
	gen := &genReader{base: base, left: int64(mib) << 20, r: corpus.NewRNG(7, "stream"), sum: sha256.New()}
	var comp io.Reader
	var cmd *exec.Cmd
	csum := sha256.New()
	switch {
	case strings.HasPrefix(prod, "gzip"):
		cmd = exec.Command("gzip", "-n", "-"+prod[4:], "-c")
		cmd.Stdin = gen
		out, err := cmd.StdoutPipe()
		if err != nil {
			fatal(err)
		}
		if err := cmd.Start(); err != nil {
			fatal(err)
		}
		comp = out
	case strings.HasPrefix(prod, "go"):
		lv, _ := strconv.Atoi(prod[2:])
		pr, pw := io.Pipe()
		go func() {
			zw, _ := gzip.NewWriterLevel(pw, lv)
			_, err := io.Copy(zw, gen)
			if err == nil {
				err = zw.Close()
			}
			pw.CloseWithError(err)
		}()
		comp = pr
	default:
		fatal(fmt.Errorf("unknown producer %q", prod))
	}
	tee := io.TeeReader(comp, csum)
	br := bufio.NewReaderSize(tee, 1<<16)

	pf, err := os.Create(filepath.Join(dir, "plain"))
	if err != nil {
		fatal(err)
	}
	uf, err := os.Create(filepath.Join(dir, "units"))
	if err != nil {
		fatal(err)
	}
	sink := &fileSink{plain: bufio.NewWriterSize(pf, 1<<16), units: bufio.NewWriterSize(uf, 1<<16), psum: sha256.New()}
	runtime.GC()
	m := startMeter()
	t0 := time.Now()
	hdr, err := deflate.GzipHeader(br)
	if err != nil {
		fatal(err)
	}
	st, rest, err := ir.EncodeStream(br, ir.StreamConfig{Target: target, Profile: profile}, sink)
	if err != nil {
		fatal(err)
	}
	// gzip trailer: crc32 + isize, from the read-ahead and the reader
	tr := append([]byte(nil), rest...)
	if len(tr) < 8 {
		b := make([]byte, 8-len(tr))
		if _, err := io.ReadFull(br, b); err != nil {
			fatal(err)
		}
		tr = append(tr, b...)
	}
	io.Copy(io.Discard, br)
	encDur := time.Since(t0)
	sink.plain.Flush()
	sink.units.Flush()
	pf.Close()
	uf.Close()
	encHeap, _ := m.finish()
	hwmEnc := vmHWM()
	if cmd != nil {
		cmd.Wait()
	}
	okTrailer := binary.LittleEndian.Uint32(tr[:4]) == sink.pcrc && binary.LittleEndian.Uint32(tr[4:8]) == uint32(sink.pn)
	okPlain := hex.EncodeToString(gen.sum.Sum(nil)) == hex.EncodeToString(sink.psum.Sum(nil))
	compBytes := int64(len(hdr)) + st.StreamLen + 8

	// reconstruct from the files
	runtime.GC()
	m2 := startMeter()
	t1 := time.Now()
	pr, _ := os.Open(filepath.Join(dir, "plain"))
	ur, _ := os.Open(filepath.Join(dir, "units"))
	ubuf := bufio.NewReaderSize(ur, 1<<16)
	rsum := sha256.New()
	rsum.Write(hdr)
	var rcrc countingHash
	w := io.MultiWriter(rsum, &rcrc)
	err = ir.ReconstructStream(w, st.Skeleton, func(i int) ([]byte, error) {
		var l [4]byte
		if _, err := io.ReadFull(ubuf, l[:]); err != nil {
			return nil, err
		}
		b := make([]byte, binary.LittleEndian.Uint32(l[:]))
		_, err := io.ReadFull(ubuf, b)
		return b, err
	}, bufio.NewReaderSize(pr, 1<<16), ir.StreamDecodeOptions{})
	if err != nil {
		fatal(err)
	}
	rsum.Write(tr)
	decDur := time.Since(t1)
	decHeap, _ := m2.finish()
	pr.Close()
	ur.Close()
	exact := hex.EncodeToString(rsum.Sum(nil)) == hex.EncodeToString(csum.Sum(nil)) && okTrailer && okPlain
	fmt.Printf("| %d | %s | %.1f | %.1f | %s | %s | %.1f | %s | %s | %d B | %d | %d B | %d | %v |\n", mib, prod, float64(compBytes)/(1<<20),
		float64(st.PlainLen)/(1<<20)/encDur.Seconds(), mb(encHeap), hwmEnc, float64(st.PlainLen)/(1<<20)/decDur.Seconds(), mb(decHeap),
		mb(uint64(st.RingBytes)), len(st.Skeleton), st.Units, st.UnitBytes, st.Blocks, exact)
	_ = bytes.MinRead
}

type countingHash struct{ n int64 }

func (c *countingHash) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
