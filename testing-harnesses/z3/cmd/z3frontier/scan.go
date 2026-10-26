package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"zeros3-z3/frontier"
)

// zstdDecompress shells out to the zstd CLI: SquashFS metadata blocks are
// independent zstd frames, and the lab only needs them as an oracle.
func zstdDecompress(src []byte, max int) ([]byte, error) {
	cmd := exec.Command("zstd", "-dc", "-q")
	cmd.Stdin = bytes.NewReader(src)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	if out.Len() > max {
		return nil, fmt.Errorf("decompressed %d > %d", out.Len(), max)
	}
	return out.Bytes(), nil
}

var dumpExtRE = regexp.MustCompile(`^\s*\d+:\s+(\d+)\.\.\s*\d+\s*\|\s*(\d+)\s*:\s+(\d+)\.\.\s*\d+\s*\|\s*(\d+)\s*$`)

// erofsExtents asks dump.erofs for a compressed inode's physical clusters.
func erofsExtents(img string, bin string) frontier.ExtentFn {
	return func(nid uint64, path string) ([]frontier.ErExtent, error) {
		out, err := exec.Command(bin, fmt.Sprintf("--nid=%d", nid), "-e", img).Output()
		if err != nil {
			return nil, err
		}
		var sp []frontier.ErExtent
		for _, l := range strings.Split(string(out), "\n") {
			if m := dumpExtRE.FindStringSubmatch(l); m != nil {
				v := [4]int64{}
				for i := range v {
					v[i], _ = strconv.ParseInt(m[i+1], 10, 64)
				}
				sp = append(sp, frontier.ErExtent{LOff: v[0], LLen: v[1], POff: v[2], PLen: v[3]})
			}
		}
		return sp, nil
	}
}

func cmdScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	img := fs.String("img", "", "image file")
	kind := fs.String("fmt", "sqfs", "sqfs|erofs")
	tl := fs.String("tools", "/root/z3cache", "tools root")
	fs.Parse(args)
	f, err := os.Open(*img)
	must(err)
	st, _ := f.Stat()
	sum := map[string]int64{}
	switch *kind {
	case "sqfs":
		im, err := frontier.ScanSquashfs(f, st.Size(), zstdDecompress, 1<<20)
		must(err)
		cnt := map[string]int{}
		for _, b := range im.Blocks {
			sum[b.Kind] += b.Len
			cnt[b.Kind]++
		}
		fmt.Printf("squashfs %d B files=%d dirs=%d frags=%d super=%+v\n", im.Size, len(im.Files), im.Dirs, len(im.FragBlk), im.Super)
		printKinds(sum, cnt)
	case "erofs":
		im, err := frontier.ScanErofs(f, st.Size(), erofsExtents(*img, *tl+"/tools/bin/dump.erofs"), 1<<20)
		must(err)
		cnt := map[string]int{}
		for _, g := range im.Regions {
			sum[g.Kind] += g.Len
			cnt[g.Kind]++
		}
		fmt.Printf("erofs %d B files=%d dirs=%d compressed=%d holeBytes=%d super=%+v\n", im.Size, len(im.Files), im.Dirs, im.Compress, im.HoleBytes, im.Super)
		printKinds(sum, cnt)
	}
}

func printKinds(sum map[string]int64, cnt map[string]int) {
	var ks []string
	for k := range sum {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		fmt.Printf("  %-8s %12d B  %6d regions\n", k, sum[k], cnt[k])
	}
}

// cmdVerify compares an extracted tree with the source tree (Z3-24 exactness check).
func cmdVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	src := fs.String("src", "", "source directory")
	got := fs.String("got", "", "extracted directory")
	fs.Parse(args)
	a, err := frontier.Snapshot(*src, true)
	must(err)
	b, err := frontier.Snapshot(*got, true)
	must(err)
	feat := frontier.Features{Mtime: true, DirMtime: true, Xattr: true, Hard: true, Mode: true}
	n, sample := frontier.Diff(a, b, feat)
	fmt.Printf("mismatches=%d %v\n", n, sample)
}
