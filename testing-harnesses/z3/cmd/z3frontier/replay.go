package main

import (
	"bytes"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"zeros3-z3/deflate"
	"zeros3-z3/frontier"
)

// cmdEstargzReplay is the Z3-23 lab-only feasibility check for exact eStargz
// member reconstruction: (1) the existing exact DEFLATE parse/replay reproduces
// every member byte-for-byte; (2) the Go stdlib encoder, given only the member's
// plaintext and a compression level, regenerates the member exactly.
func cmdEstargzReplay(args []string) {
	fs := flag.NewFlagSet("estargz-replay", flag.ExitOnError)
	work := fs.String("work", "/root/z3scratch/c23", "scratch directory")
	tl := fs.String("tools", "/root/z3cache", "tools root")
	level := fs.Int("level", 0, "estargz producer level (0 = library default)")
	fs.Parse(args)
	T := findTools(*tl)
	must(os.MkdirAll(*work, 0o755))
	tree := frontier.Base()
	dir := filepath.Join(*work, "tree")
	must(frontier.Materialize(tree, dir))
	tarPath := filepath.Join(*work, "r.tar")
	f, err := os.Create(tarPath)
	must(err)
	must(frontier.WriteTar(f, tree, dir))
	must(f.Close())
	p := filepath.Join(*work, "r.estargz")
	a := []string{"estargz", "-in", tarPath, "-out", p, "-chunk", "1048576"}
	if *level != 0 {
		a = append(a, "-level", fmt.Sprint(*level))
	}
	_, err = run(T.cprod, a...)
	must(err)
	b, err := os.ReadFile(p)
	must(err)
	t0 := time.Now()
	spans, err := frontier.GzipMembers(b, 1<<22)
	must(err)
	gzs, err := deflate.ParseGzipAll(b, deflate.Options{}, false)
	must(err)
	var rebuilt bytes.Buffer
	for _, g := range gzs {
		mb, err := g.Bytes()
		must(err)
		rebuilt.Write(mb)
	}
	fmt.Printf("members=%d scanner==parser:%v exact deflate parse/replay of whole blob: %v (%v)\n", len(spans), len(spans) == len(gzs), bytes.Equal(rebuilt.Bytes(), b), time.Since(t0).Round(time.Millisecond))
	levels := []int{gzip.DefaultCompression, 1, 5, 6, 9}
	ok := map[int]int{}
	var inBytes int64
	for _, s := range spans {
		m := b[s.Off : s.Off+s.Len]
		zr, err := gzip.NewReader(bytes.NewReader(m))
		must(err)
		pt, err := io.ReadAll(zr)
		must(err)
		inBytes += s.Len
		for _, lv := range levels {
			var o bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&o, lv)
			zw.Write(pt)
			zw.Close()
			if bytes.Equal(o.Bytes(), m) {
				ok[lv]++
			}
		}
	}
	fmt.Printf("stdlib re-encode of %d members (%d B) matched per level: %v\n", len(spans), inBytes, ok)
	os.Remove(p)
	os.Remove(p + ".toc.json")
	os.Remove(tarPath)
	os.RemoveAll(dir)
}
