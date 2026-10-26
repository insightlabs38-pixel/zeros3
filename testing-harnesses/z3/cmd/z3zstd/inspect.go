package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"zeros3-z3/lab"

	"zeros3-z3/zstdz3"
)

type hashWriter struct {
	h interface{ Write([]byte) (int, error) }
	n int64
}

func (w *hashWriter) Write(p []byte) (int, error) { w.n += int64(len(p)); return w.h.Write(p) }

// cmdInspect parses one stream and prints the exact-replay accounting.
func cmdInspect(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	in := fs.String("in", "", "zstd file(s), comma-separated")
	audit := fs.Bool("audit", false, "one metadata-audit row per file")
	nomodel := fs.Bool("nomodel", false, "explicit varint sequences")
	fs.Parse(args)
	if *audit {
		fmt.Printf("%-34s %11s | %9s %9s %9s | %10s %7s | %10s %7s\n", "stream", "compressed", "frame/tbl", "seq units", "residual", "IR raw", "IR %", "IR packed", "IR %")
		for _, p := range strings.Split(*in, ",") {
			auditRow(p, *nomodel)
		}
		return
	}
	f, err := os.Open(*in)
	must(err)
	defer f.Close()
	st, _ := f.Stat()
	h := sha256.New()
	hw := &hashWriter{h: h}
	t0 := time.Now()
	res, err := zstdz3.Parse(f, hw, nil, zstdz3.Options{NoSeqModel: *nomodel})
	must(err)
	s := res.Stats
	fmt.Printf("%s: %d bytes -> %d plain (%x) in %v\n", *in, st.Size(), hw.n, h.Sum(nil)[:6], since(t0))
	fmt.Printf("blocks raw/rle/transformed/fallback %v, stream bytes %v\n", s.Blocks, s.StreamBytes)
	fmt.Printf("skeleton %d (tree %d tab %d rank %d frame %d) units %d fallback %d => IR %d = %.1f%% of stream\n",
		s.SkeletonBytes, s.TreeBytes, s.TableBytes, s.RankBytes, s.FrameBytes, s.UnitBytes, s.FallbackBytes, s.IRBytes(), 100*float64(s.IRBytes())/float64(st.Size()))
	fmt.Printf("literals %d sequences %d; lit modes %v streams(1,4) %d/%d huff direct/fse %d/%d; seq modes LL %v OF %v ML %v; offcodes %v\n",
		s.Literals, s.Sequences, s.LitModes, s.LitStreams[1], s.LitStreams[4], s.HuffDirect, s.HuffFSE, s.SeqModes[0], s.SeqModes[1], s.SeqModes[2], s.RepeatCodes)
}

func auditRow(path string, nomodel bool) {
	b, err := os.ReadFile(path)
	must(err)
	var units bytes.Buffer
	res, err := zstdz3.Parse(bytes.NewReader(b), io.Discard, &units, zstdz3.Options{NoSeqModel: nomodel})
	must(err)
	s := res.Stats
	frame := s.FrameBytes + s.TreeBytes + s.TableBytes + s.SkipBytes
	packed := lab.PackedLen(res.Raw) + lab.PackedLen(units.Bytes())
	fmt.Printf("%-34s %11d | %9d %9d %9d | %10d %6.1f%% | %10d %6.1f%%  [%d fallback blocks]\n", filepath.Base(path), len(b), frame, s.UnitBytes+s.FallbackBytes, s.RankBytes, s.IRBytes(), 100*float64(s.IRBytes())/float64(len(b)), packed, 100*float64(packed)/float64(len(b)), s.Blocks[zstdz3.BlockFallback])
}
