package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"zeros3-z3/frontier"
	"zeros3-z3/lab"
	"zeros3-z3/zstdz3"
)

// cmdSqfs is the post-OCI consumer probe: build a SquashFS image of the R0 tree with
// zstd blocks, locate every compressed region with the Z3-24 scanner (using zstdz3 as the
// metadata decompressor), and ask what exact replay of each block would cost. Nothing
// here reconstructs SquashFS; each block is an independent Zstandard frame.
func cmdSqfs(args []string) {
	fs := flag.NewFlagSet("sqfs", flag.ExitOnError)
	dir := fs.String("dir", "/root/z3scratch/c25/sqfs", "scratch directory")
	rev := fs.String("rev", "R0", "revision")
	fs.Parse(args)
	must(os.MkdirAll(*dir, 0o755))
	tree := filepath.Join(*dir, "tree")
	img := filepath.Join(*dir, *rev+".sqfs")
	if _, err := os.Stat(img); err != nil {
		os.RemoveAll(tree)
		t := frontier.Revision(frontier.Base(), *rev)
		must(frontier.Materialize(t, tree))
		out, err := exec.Command("mksquashfs", tree, img, "-comp", "zstd", "-noappend", "-quiet", "-all-root", "-mkfs-time", "0", "-no-xattrs").CombinedOutput()
		if err != nil {
			must(fmt.Errorf("mksquashfs: %v\n%s", err, out))
		}
		os.RemoveAll(tree)
	}
	b, err := os.ReadFile(img)
	must(err)
	dec := func(src []byte, max int) ([]byte, error) {
		var out bytes.Buffer
		if _, err := zstdz3.Parse(bytes.NewReader(src), &out, nil, zstdz3.Options{Limits: zstdz3.Limits{MaxTotalDecoded: int64(max)}}); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	im, err := frontier.ScanSquashfs(bytes.NewReader(b), int64(len(b)), dec, 1<<20)
	must(err)
	type agg struct {
		n, fail, fb     int
		comp, ir, packd int64
		plain           int64
	}
	by := map[string]*agg{}
	for _, bl := range im.Blocks {
		if bl.Raw || bl.Kind == "super" || bl.Kind == "pad" || bl.Len < 8 {
			continue
		}
		a := by[bl.Kind]
		if a == nil {
			a = &agg{}
			by[bl.Kind] = a
		}
		blk := b[bl.Off : bl.Off+bl.Len]
		switch bl.Kind {
		case "dir", "inode", "fragtab", "idtab", "lookup", "xattr": // metadata blocks start with a 2-byte length word
			blk = blk[2:]
		}
		var plain, units bytes.Buffer
		res, err := zstdz3.Parse(bytes.NewReader(blk), &plain, &units, zstdz3.Options{})
		if err != nil {
			a.fail++
			continue
		}
		var re bytes.Buffer
		if _, err := zstdz3.Replay(&re, res.Raw, bytes.NewReader(units.Bytes()), bytes.NewReader(plain.Bytes()), zstdz3.Limits{}); err != nil || !bytes.Equal(re.Bytes(), blk) {
			a.fail++
			continue
		}
		a.n++
		a.comp += bl.Len
		a.plain += int64(plain.Len())
		a.ir += int64(res.Stats.IRBytes())
		a.packd += lab.PackedLen(res.Raw) + lab.PackedLen(units.Bytes())
		a.fb += res.Stats.Blocks[zstdz3.BlockFallback]
	}
	fmt.Printf("%s: %d bytes, %d regions; block size %d, compressor id %d\n", img, len(b), len(im.Blocks), im.Super.BlockSize, im.Super.Comp)
	kinds := make([]string, 0, len(by))
	for k := range by {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	fmt.Printf("%-8s %7s %6s %11s %11s %9s %9s %8s\n", "kind", "frames", "fail", "compressed", "plaintext", "IR", "IR+pack", "IR/comp")
	var tc, ti, tp int64
	for _, k := range kinds {
		a := by[k]
		fmt.Printf("%-8s %7d %6d %11d %11d %9d %9d %7.1f%%\n", k, a.n, a.fail, a.comp, a.plain, a.ir, a.packd, 100*float64(a.packd)/float64(max(a.comp, 1)))
		tc += a.comp
		ti += a.ir
		tp += a.packd
	}
	fmt.Printf("total    compressed %d, IR %d (%.1f%%), IR packed %d (%.1f%%)\n", tc, ti, 100*float64(ti)/float64(tc), tp, 100*float64(tp)/float64(tc))
}
