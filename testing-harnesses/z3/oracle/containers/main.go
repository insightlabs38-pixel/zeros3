// Command z3cprod is the Z3-23 external producer/inspector for eStargz and
// zstd:chunked, built from the official libraries (containerd/stargz-snapshotter
// estargz, containers/storage pkg/chunked/compressor). It lives in its own Go
// module so the Z3 module stays stdlib-only; it is never a production dependency.
//
//	z3cprod estargz    -in TAR -out BLOB [-chunk N] [-level L]
//	z3cprod zstdchunked -in TAR -out BLOB [-level L]
//	z3cprod zstd       -in TAR -out FILE [-level 1..4]   (Z3-25: plain klauspost/compress stream,
//	                   single encoder goroutine; 1 fastest, 2 default, 3 better, 4 best)
//
// Each writes BLOB and BLOB.toc.json (the decompressed TOC / manifest) and prints
// one JSON line of producer facts.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/containerd/stargz-snapshotter/estargz"
	"github.com/containers/storage/pkg/chunked/compressor"
	"github.com/klauspost/compress/zstd"
)

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3cprod:", err)
		os.Exit(1)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3cprod estargz|zstdchunked -in TAR -out BLOB")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	in := fs.String("in", "", "input tar")
	out := fs.String("out", "", "output blob")
	chunk := fs.Int("chunk", 1<<20, "estargz chunk size")
	level := fs.Int("level", 0, "compression level (0 = library default)")
	fs.Parse(os.Args[2:])
	switch os.Args[1] {
	case "estargz":
		die(runEstargz(*in, *out, *chunk, *level))
	case "zstdchunked":
		die(runZstdChunked(*in, *out, *level))
	case "zstd":
		die(runZstdPlain(*in, *out, *level))
	default:
		die(fmt.Errorf("unknown mode %s", os.Args[1]))
	}
}

func runEstargz(in, out string, chunk, level int) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	opts := []estargz.Option{estargz.WithChunkSize(chunk)}
	if level != 0 {
		opts = append(opts, estargz.WithCompressionLevel(level))
	}
	blob, err := estargz.Build(io.NewSectionReader(f, 0, st.Size()), opts...)
	if err != nil {
		return err
	}
	defer blob.Close()
	o, err := os.Create(out)
	if err != nil {
		return err
	}
	if _, err := io.Copy(o, blob); err != nil {
		return err
	}
	if err := o.Close(); err != nil {
		return err
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	tocOff, footer, err := estargz.OpenFooter(io.NewSectionReader(bytes.NewReader(raw), 0, int64(len(raw))))
	if err != nil {
		return err
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw[tocOff : int64(len(raw))-footer]))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	if _, err := tr.Next(); err != nil {
		return err
	}
	toc, err := io.ReadAll(tr)
	if err != nil {
		return err
	}
	if err := os.WriteFile(out+".toc.json", toc, 0o644); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"mode": "estargz", "blob": len(raw), "toc_offset": tocOff, "footer": footer, "toc_json": len(toc),
		"toc_digest": blob.TOCDigest().String(), "diff_id": blob.DiffID(), "chunk": chunk, "lib": "stargz-snapshotter/estargz v0.16.3",
	})
}

func runZstdChunked(in, out string, level int) error {
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	o, err := os.Create(out)
	if err != nil {
		return err
	}
	meta := map[string]string{}
	var lv *int
	if level != 0 {
		lv = &level
	}
	w, err := compressor.ZstdCompressor(o, meta, lv)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, f); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := o.Close(); err != nil {
		return err
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return err
	}
	var pos string
	for k, v := range meta {
		if strings.HasSuffix(k, "manifest-position") {
			pos = v
		}
	}
	p := strings.Split(pos, ":")
	if len(p) != 4 {
		return fmt.Errorf("unexpected manifest position %q (meta %v)", pos, meta)
	}
	off, _ := strconv.ParseInt(p[0], 10, 64)
	clen, _ := strconv.ParseInt(p[1], 10, 64)
	dec, _ := zstd.NewReader(nil)
	toc, err := dec.DecodeAll(raw[off:off+clen], nil)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	if err := os.WriteFile(out+".toc.json", toc, 0o644); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"mode": "zstdchunked", "blob": len(raw), "meta": meta, "manifest_offset": off, "manifest_clen": clen, "toc_json": len(toc),
		"lib": "containers/storage v1.59.1 pkg/chunked/compressor",
	})
}

// runZstdPlain writes a plain Zstandard stream with klauspost/compress, the encoder
// containers/storage uses, as an independent producer for the exact-replay tests.
func runZstdPlain(in, out string, level int) error {
	if level == 0 {
		level = int(zstd.SpeedDefault)
	}
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	o, err := os.Create(out)
	if err != nil {
		return err
	}
	w, err := zstd.NewWriter(o, zstd.WithEncoderLevel(zstd.EncoderLevel(level)), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, f); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if err := o.Close(); err != nil {
		return err
	}
	st, _ := os.Stat(out)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"mode": "zstd", "level": level, "size": st.Size(), "lib": "klauspost/compress v1.18.0"})
}
