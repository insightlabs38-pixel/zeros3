package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"zeros3-z3/frontier"
)

const (
	sqBlock   = 131072
	unitCap   = 128 << 10 // native data units never exceed one compression cluster
	erBlock   = 4096
	buildTime = "1700000000"
	fsUUID    = "11111111-1111-1111-1111-111111111111" // mkfs.erofs draws a random UUID otherwise
)

// ictx is the per-revision input shared by all image lanes.
type ictx struct {
	rev     string
	tree    *frontier.Tree
	dir     string
	tarPath string
	work    string
	T       tools
	root    string
	objs    string // composefs object store (persistent across revisions)
}

type clsRegion struct {
	Off, Len int64
	Class    string
}

type unit struct {
	ID    [32]byte
	Len   int64
	Class string
}

// scanned is what a lane learns about one produced image.
type scanned struct {
	regions []clsRegion
	units   []unit
	info    rec
	reads   rec
}

type ilane struct {
	name, fam string
	revs      []string // nil = every revision
	build     func(c *ictx, out string) error
	chain     *frontier.Chain
	seen      map[[32]byte]bool
	feat      frontier.Features
}

func (l *ilane) runs(rev string) bool {
	if l.revs == nil {
		return true
	}
	for _, r := range l.revs {
		if r == rev {
			return true
		}
	}
	return false
}

func runIn(env []string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(), env...)
	var eb bytes.Buffer
	cmd.Stderr = &eb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %v: %w\n%s", name, args, err, eb.String())
	}
	return nil
}

func sqBuild(level string, extra ...string) func(c *ictx, out string) error {
	return func(c *ictx, out string) error {
		a := []string{c.dir, out, "-noappend", "-no-progress", "-quiet", "-b", fmt.Sprint(sqBlock), "-mkfs-time", buildTime}
		if level == "raw" {
			a = append(a, "-noI", "-noD", "-noF", "-noX")
		} else {
			a = append(a, "-comp", "zstd", "-Xcompression-level", level)
		}
		a = append(a, extra...)
		if c.tree.Order == "rev" { // producer-order perturbation: reversed data priorities
			sf := filepath.Join(c.work, "sortfile")
			var sb strings.Builder
			n := 0
			for _, e := range c.tree.Entries {
				if e.Kind == frontier.KFile {
					n++
				}
			}
			i := 0
			for _, e := range c.tree.Entries {
				if e.Kind == frontier.KFile {
					fmt.Fprintf(&sb, "%s %d\n", e.Path, i-n/2) // later files get higher priority -> stored first
					i++
				}
			}
			if err := os.WriteFile(sf, []byte(sb.String()), 0o644); err != nil {
				return err
			}
			a = append(a, "-sort", sf)
		}
		return runIn(nil, "mksquashfs", a...)
	}
}

func erBuild(opts ...string) func(c *ictx, out string) error {
	return func(c *ictx, out string) error {
		a := append([]string{"-T" + buildTime, "--mkfs-time", "--quiet", "-U", fsUUID}, opts...)
		a = append(a, out, c.dir)
		return runIn(nil, c.root+"/tools/bin/mkfs.erofs", a...)
	}
}

func erTarBuild(opts ...string) func(c *ictx, out string) error {
	return func(c *ictx, out string) error {
		a := append([]string{"-T" + buildTime, "--mkfs-time", "--quiet", "-U", fsUUID, "--tar=f", "--sort=none"}, opts...)
		a = append(a, out, c.tarPath)
		return runIn(nil, c.root+"/tools/bin/mkfs.erofs", a...)
	}
}

func cfsBuild(c *ictx, out string) error {
	return runIn([]string{"LD_LIBRARY_PATH=" + c.root + "/tools/lib/x86_64-linux-gnu"}, c.root+"/tools/bin/mkcomposefs", "--digest-store="+c.objs, c.dir, out)
}

var allRevs = frontier.Revisions

func imageLanes() []*ilane {
	full := frontier.Features{Mtime: true, DirMtime: true, Xattr: true, Hard: true, Mode: true}
	mk := func(name, fam string, revs []string, b func(c *ictx, out string) error) *ilane {
		return &ilane{name: name, fam: fam, revs: revs, build: b, chain: frontier.NewChain(), seen: map[[32]byte]bool{}, feat: full}
	}
	tails := []string{"R0", "R6", "R8"}
	ls := []*ilane{
		mk("sq", "sqfs", nil, sqBuild("3")),
		mk("er", "erofs", nil, erBuild("-zzstd,level=3", "-C131072", "-Efragments")),
		mk("sqraw", "sqfs", nil, sqBuild("raw")),
		mk("erraw", "erofs", nil, erBuild()),
		mk("erchunk", "erofs", nil, erBuild("--chunksize=131072")),
		mk("cfs", "cfs", nil, cfsBuild),
		mk("sqnf", "sqfs", tails, sqBuild("3", "-no-fragments")),
		mk("sqtail", "sqfs", tails, sqBuild("3", "-tailends")),
		mk("ernf", "erofs", tails, erBuild("-zzstd,level=3", "-C131072")),
		mk("erall", "erofs", tails, erBuild("-zzstd,level=3", "-C131072", "-Eall-fragments")),
		mk("sq9", "sqfs", []string{"R0", "R1"}, sqBuild("9")),
		mk("er9", "erofs", []string{"R0", "R1"}, erBuild("-zzstd,level=9", "-C131072", "-Efragments")),
		mk("ertar", "erofs", []string{"R0", "R10"}, erTarBuild("-zzstd,level=3", "-C131072", "-Efragments")),
	}
	for _, l := range ls {
		if l.name == "ertar" { // tar input does not carry directory attributes through mkfs.erofs
			l.feat.NoDirAttrs = true
		}
	}
	return ls
}

func classOf(kind string) string {
	switch kind {
	case "data":
		return "data"
	case "frag", "tail":
		return "tail"
	case "inode":
		return "inode"
	case "dir":
		return "dir"
	case "super", "fragtab", "lookup", "idtab", "xattr":
		return "index"
	}
	return "pad"
}

func hashAt(b []byte, off, n int64) [32]byte {
	if off < 0 || off > int64(len(b)) {
		return [32]byte{}
	}
	return sha256.Sum256(b[off:min(off+n, int64(len(b)))])
}

// scanImage classifies and slices one image into regions and native units.
func (c *ictx) scanImage(l *ilane, out string, b []byte) (*scanned, error) {
	f, err := os.Open(out)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	size := int64(len(b))
	sc := &scanned{info: rec{}, reads: rec{}}
	switch l.fam {
	case "sqfs":
		im, err := frontier.ScanSquashfs(f, size, zstdDecompress, 1<<20)
		if err != nil {
			return nil, err
		}
		for _, bl := range im.Blocks {
			cl := classOf(bl.Kind)
			sc.regions = append(sc.regions, clsRegion{bl.Off, bl.Len, cl})
			sc.units = append(sc.units, unit{hashAt(b, bl.Off, bl.Len), bl.Len, cl})
		}
		var hb int
		for _, fl := range im.Files {
			hb += fl.Holes
		}
		sc.info["files"], sc.info["dirs"], sc.info["frag_blocks"], sc.info["hole_blocks"] = len(im.Files), im.Dirs, len(im.FragBlk), hb
		if sp := im.Files["big/sparse.img"]; sp != nil {
			var phys int64
			for _, bl := range sp.Blocks {
				phys += bl.Len
			}
			sc.info["sparse"] = rec{"logical": sp.Size, "phys": phys, "hole_blocks": sp.Holes, "data_blocks": len(sp.Blocks)}
		}
		thr := rec{}
		for _, n := range []string{"thr/t0_0.dat", "thr/t3_0.dat", "thr/t4_0.dat", "thr/t6_0.dat", "thr/t3_1.dat"} {
			if fl := im.Files[n]; fl != nil {
				var phys int64
				for _, bl := range fl.Blocks {
					phys += bl.Len
				}
				thr[n] = rec{"size": fl.Size, "frag": fl.Frag >= 0, "blocks": len(fl.Blocks), "phys": phys}
			}
		}
		sc.info["thr"] = thr
		if c.rev == "R0" {
			sc.reads = c.sqReads(im, b)
		}
	case "erofs":
		im, err := frontier.ScanErofs(f, size, erofsExtents(out, c.root+"/tools/bin/dump.erofs"), 1<<20)
		if err != nil {
			return nil, err
		}
		type pr struct{ off, n int64 }
		var data []pr
		for _, g := range im.Regions {
			cl := classOf(g.Kind)
			sc.regions = append(sc.regions, clsRegion{g.Off, g.Len, cl})
			if g.Kind == "data" || g.Kind == "frag" {
				for o := int64(0); o < g.Len; o += unitCap {
					n := min(unitCap, g.Len-o)
					sc.units = append(sc.units, unit{hashAt(b, g.Off+o, n), n, cl})
					data = append(data, pr{g.Off + o, n})
				}
			}
		}
		// everything else is addressed as 4 KiB blocks (inodes, dirs, tails share blocks)
		prio := map[string]int{"tail": 4, "dir": 3, "inode": 2, "index": 1, "pad": 0}
		blkClass := map[int64]string{}
		for _, g := range im.Regions {
			if g.Kind == "data" || g.Kind == "frag" {
				continue
			}
			cl := classOf(g.Kind)
			for bl := g.Off / erBlock; bl <= (g.Off+g.Len-1)/erBlock; bl++ {
				if old, ok := blkClass[bl]; !ok || prio[cl] > prio[old] {
					blkClass[bl] = cl
				}
			}
		}
		blks := make([]int64, 0, len(blkClass))
		for k := range blkClass {
			blks = append(blks, k)
		}
		sort.Slice(blks, func(i, j int) bool { return blks[i] < blks[j] })
		for _, bl := range blks {
			sc.units = append(sc.units, unit{hashAt(b, bl*erBlock, erBlock), min(erBlock, size-bl*erBlock), blkClass[bl]})
		}
		sc.info["files"], sc.info["dirs"], sc.info["compressed"], sc.info["hole_bytes"] = len(im.Files), im.Dirs, im.Compress, im.HoleBytes
		if sp := im.Files["big/sparse.img"]; sp != nil {
			var phys int64
			for _, e := range sp.Extents {
				phys += e.Len
			}
			sc.info["sparse"] = rec{"logical": sp.Size, "phys": phys, "holes": sp.Holes, "extents": len(sp.Extents), "layout": sp.Layout}
		}
		thr := rec{}
		for _, n := range []string{"thr/t0_0.dat", "thr/t3_0.dat", "thr/t4_0.dat", "thr/t6_0.dat", "thr/t3_1.dat"} {
			if fl := im.Files[n]; fl != nil {
				var phys int64
				for _, e := range fl.Extents {
					phys += e.Len
				}
				thr[n] = rec{"size": fl.Size, "layout": fl.Layout, "tail": fl.Tail.Len, "extents": len(fl.Extents), "phys": phys}
			}
		}
		sc.info["thr"] = thr
		if c.rev == "R0" {
			sc.reads = c.erReads(im, b)
		}
	}
	sort.Slice(sc.regions, func(i, j int) bool { return sc.regions[i].Off < sc.regions[j].Off })
	return sc, nil
}

func uniqSpans(sp []frontier.Span) []frontier.Span {
	seen := map[frontier.Span]bool{}
	var out []frontier.Span
	for _, s := range sp {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func readRec(b []byte, meta, data []frontier.Span) rec {
	meta, data = uniqSpans(meta), uniqSpans(data)
	var m, d int64
	for _, s := range meta {
		m += s.Len
	}
	for _, s := range data {
		d += s.Len
	}
	all := append(append([]frontier.Span(nil), meta...), data...)
	r := cover(b, all)
	r["meta_bytes"], r["data_bytes"] = m, d
	return r
}

func (c *ictx) sqReads(im *frontier.SqImage, b []byte) rec {
	out := rec{}
	super := frontier.Span{Off: 0, Len: 96}
	tiny := im.Files[tinyPath]
	meta := append([]frontier.Span{super, im.FragIdx}, tiny.Meta...)
	if tiny.Frag >= 0 {
		meta = append(meta, frontier.Span{Off: im.FragMD[tiny.Frag/512].Off, Len: im.FragMD[tiny.Frag/512].Len})
		fb := im.FragBlk[tiny.Frag]
		out["read_tiny"] = readRec(b, meta, []frontier.Span{{Off: fb.Off, Len: fb.Len}})
	} else {
		var d []frontier.Span
		for _, bl := range tiny.Blocks {
			d = append(d, frontier.Span{Off: bl.Off, Len: bl.Len})
		}
		out["read_tiny"] = readRec(b, meta, d)
	}
	large := im.Files[largePath]
	var d []frontier.Span
	for _, bl := range large.Blocks {
		if int64(bl.Idx) == largeOff/sqBlock {
			d = append(d, frontier.Span{Off: bl.Off, Len: bl.Len})
		}
	}
	out["read_large"] = readRec(b, append([]frontier.Span{super}, large.Meta...), d)
	out["read_dir"] = readRec(b, append([]frontier.Span{super}, im.DirMeta["tiny/d05/s03"]...), nil)
	return out
}

func blockOf(s frontier.Span) frontier.Span {
	o := s.Off / erBlock * erBlock
	return frontier.Span{Off: o, Len: (s.Off+s.Len+erBlock-1)/erBlock*erBlock - o}
}

func (c *ictx) erResolve(im *frontier.ErImage, path string) []frontier.Span {
	sp := []frontier.Span{{Off: 0, Len: 1152}} // superblock
	parts := strings.Split(path, "/")
	sp = append(sp, blockOf(im.InodeAt[""]))
	cur := ""
	for _, p := range parts {
		if cur == "" {
			cur = p
		} else {
			cur += "/" + p
		}
		sp = append(sp, blockOf(im.EntryAt[cur]), blockOf(im.InodeAt[cur]))
	}
	return sp
}

func (c *ictx) erReads(im *frontier.ErImage, b []byte) rec {
	out := rec{}
	tiny := im.Files[tinyPath]
	meta := c.erResolve(im, tinyPath)
	var d []frontier.Span
	if tiny.Tail.Len > 0 {
		meta = append(meta, blockOf(tiny.Tail))
	}
	d = append(d, tiny.Extents...)
	if len(tiny.Extents) == 0 && tiny.Tail.Len == 0 && im.Packed != nil && (tiny.Layout == 1 || tiny.Layout == 3) {
		// -Efragments: the file lives in the compressed packed inode. The z_erofs map header (8-byte aligned after
		// the inode and xattrs) carries its logical offset there (h_fragmentoff; advise bit 0x20, or the
		// whole-file fragment-inode bit 7 of h_clusterbits).
		if pos := (tiny.Meta.Off + tiny.Meta.Len + 7) &^ 7; pos+8 <= int64(len(b)) && (binary.LittleEndian.Uint16(b[pos+4:])&0x20 != 0 || b[pos+7]&0x80 != 0) {
			fo := int64(binary.LittleEndian.Uint32(b[pos:]))
			for _, e := range im.Packed.Logical {
				if fo < e.LOff+e.LLen && fo+tiny.Size > e.LOff {
					d = append(d, frontier.Span{Off: e.POff, Len: e.PLen})
				}
			}
		}
	}
	out["read_tiny"] = readRec(b, meta, d)
	large := im.Files[largePath]
	var ld []frontier.Span
	if len(large.Logical) > 0 {
		for _, e := range large.Logical {
			if largeOff >= e.LOff && largeOff < e.LOff+e.LLen {
				ld = append(ld, frontier.Span{Off: e.POff, Len: e.PLen})
			}
		}
	} else {
		for _, e := range large.Extents { // plain/chunk-based: the 4 KiB block (or its chunk) holding the offset
			ld = append(ld, frontier.Span{Off: e.Off + largeOff%e.Len/erBlock*erBlock, Len: erBlock})
			break
		}
	}
	out["read_large"] = readRec(b, c.erResolve(im, largePath), ld)
	dm := c.erResolve(im, "tiny/d05/s03")
	for _, s := range im.DirData["tiny/d05/s03"] {
		dm = append(dm, blockOf(s))
	}
	out["read_dir"] = readRec(b, dm, nil)
	return out
}

// attribute spreads a new chunk's pack-rule bytes over the classes of the regions it overlaps.
func attribute(regs []clsRegion, off, n, packed int64, into map[string]float64) {
	i := sort.Search(len(regs), func(i int) bool { return regs[i].Off+regs[i].Len > off })
	for ; i < len(regs) && regs[i].Off < off+n; i++ {
		lo, hi := max(off, regs[i].Off), min(off+n, regs[i].Off+regs[i].Len)
		into[regs[i].Class] += float64(packed) * float64(hi-lo) / float64(n)
	}
}

func (l *ilane) nativeCost(units []unit) rec {
	by := map[string]int64{}
	tot := map[string]int64{}
	var newB, all int64
	var nNew int
	for _, u := range units {
		tot[u.Class] += u.Len
		all += u.Len
		if !l.seen[u.ID] {
			l.seen[u.ID] = true
			nNew++
			newB += u.Len
			by[u.Class] += u.Len
		}
	}
	refs := frontier.NativeRefBytes * int64(len(units))
	return rec{"units": len(units), "new_units": nNew, "total_bytes": all, "new_bytes": newB, "refs": refs, "incr": newB + refs, "new_by_class": by, "total_by_class": tot}
}

func (c *ictx) extractVerify(l *ilane, out string, src map[string]frontier.Snap) rec {
	x := filepath.Join(c.work, l.name+"-x")
	os.RemoveAll(x)
	defer os.RemoveAll(x)
	var err error
	switch l.fam {
	case "sqfs":
		err = runIn(nil, "unsquashfs", "-d", x, "-no-progress", "-quiet", out)
	case "erofs":
		err = runIn(nil, c.root+"/tools/bin/fsck.erofs", "--extract="+x, "--xattrs", "--overwrite", "--force", out)
	}
	if err != nil {
		return rec{"ok": false, "err": err.Error()}
	}
	got, err := frontier.Snapshot(x, true)
	if err != nil {
		return rec{"ok": false, "err": err.Error()}
	}
	patched := 0
	if l.fam == "erofs" { // fsck.erofs does not extend a trailing hole: confirm such files with dump.erofs --cat
		for p, s := range src {
			if g, ok := got[p]; ok && s.Type == "f" && g.SHA != s.SHA {
				cat, cerr := exec.Command(c.root+"/tools/bin/dump.erofs", "--cat", "--path=/"+p, out).Output()
				if cerr == nil && fmt.Sprintf("%x", sha256.Sum256(cat)) == s.SHA {
					g.SHA, g.Size = s.SHA, s.Size
					got[p] = g
					patched++
				}
			}
		}
	}
	n, sample := frontier.Diff(src, got, l.feat)
	return rec{"ok": n == 0, "mismatch": n, "sample": sample, "cat_verified": patched}
}

// cfsVerify checks a composefs image against the source: every regular file's
// object must exist in the store with identical content, plus size/mode/mtime/xattrs.
func (c *ictx) cfsVerify(out string, src map[string]frontier.Snap) (rec, map[string]bool, error) {
	env := []string{"LD_LIBRARY_PATH=" + c.root + "/tools/lib/x86_64-linux-gnu"}
	cmd := exec.Command(c.root+"/tools/bin/composefs-info", "dump", out)
	cmd.Env = append(os.Environ(), env...)
	dump, err := cmd.Output()
	if err != nil {
		return nil, nil, err
	}
	objs := map[string]bool{}
	bad := 0
	var sample []string
	note := func(f string, a ...any) {
		if bad++; len(sample) < 6 {
			sample = append(sample, fmt.Sprintf(f, a...))
		}
	}
	seen := map[string]bool{}
	hashed := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(dump)), "\n") {
		f := strings.Split(line, " ")
		if len(f) < 10 {
			continue
		}
		p := strings.TrimPrefix(string(unescapeDump(f[0])), "/")
		if p == "" {
			continue
		}
		s, ok := src[p]
		if !ok {
			note("extra %s", p)
			continue
		}
		seen[p] = true
		mode, _ := strconv.ParseInt(f[2], 8, 64)
		mt, _, _ := strings.Cut(f[7], ".")
		if s.Type == "f" {
			size, _ := strconv.ParseInt(f[1], 10, 64)
			if size != s.Size || uint32(mode&0o7777) != s.Mode {
				note("%s: size/mode", p)
			}
			if mtv, _ := strconv.ParseInt(mt, 10, 64); mtv != s.Mtime {
				note("%s: mtime", p)
			}
			if f[8] != "-" {
				objs[f[8]] = true
				h, ok := hashed[f[8]]
				if !ok {
					b, err := os.ReadFile(filepath.Join(c.objs, f[8]))
					if err != nil {
						note("%s: object missing", p)
						continue
					}
					sum := sha256.Sum256(b)
					h = fmt.Sprintf("%x", sum)
					hashed[f[8]] = h
				}
				if h != s.SHA {
					note("%s: object content differs", p)
				}
			} else if f[9] != "-" { // content small enough for composefs to inline (escaped in the dump)
				if in := unescapeDump(f[9]); int64(len(in)) != s.Size || fmt.Sprintf("%x", sha256.Sum256(in)) != s.SHA {
					note("%s: inline content differs", p)
				}
			} else if s.Size != 0 {
				note("%s: no object", p)
			}
			xa := ""
			if len(f) > 11 {
				xa = f[11]
			}
			if xa != s.Xattr {
				note("%s: xattr %q != %q", p, xa, s.Xattr)
			}
		}
	}
	for p := range src {
		if !seen[p] {
			note("missing %s", p)
		}
	}
	return rec{"ok": bad == 0, "mismatch": bad, "sample": sample, "scope": "paths, sizes, modes, mtimes, xattrs, object content (hardlinks not asserted)"}, objs, nil
}

// unescapeDump decodes composefs-info dump escapes (\n, \t, \\, \xHH).
func unescapeDump(s string) []byte {
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			out = append(out, s[i])
			continue
		}
		switch s[i+1] {
		case 'n':
			out, i = append(out, '\n'), i+1
		case 't':
			out, i = append(out, '\t'), i+1
		case 'r':
			out, i = append(out, '\r'), i+1
		case '\\':
			out, i = append(out, '\\'), i+1
		case 'x':
			if i+3 < len(s) {
				v, err := strconv.ParseUint(s[i+2:i+4], 16, 8)
				if err == nil {
					out, i = append(out, byte(v)), i+3
					continue
				}
			}
			out = append(out, s[i])
		default:
			out = append(out, s[i])
		}
	}
	return out
}

func cmdI24(args []string) {
	fs := flag.NewFlagSet("i24", flag.ExitOnError)
	work := fs.String("work", "/root/z3scratch/i24", "scratch directory")
	out := fs.String("out", "/root/z3scratch/i24.jsonl", "results JSONL")
	tl := fs.String("tools", "/root/z3cache", "external tools root")
	revs := fs.String("revs", strings.Join(frontier.Revisions, ","), "revisions")
	only := fs.String("lanes", "", "comma list of lanes (default all)")
	verify := fs.Bool("verify", true, "extract and compare every image")
	realDirs := fs.String("real", "", "comma list rev=dir pairs (e.g. A=/x/go1,B=/x/go2): use existing trees instead of the synthetic corpus")
	full := fs.Bool("full", false, "run every selected lane on every revision (default limits tail/level/tar lanes)")
	fs.Parse(args)
	must(os.MkdirAll(*work, 0o755))
	of, err := os.Create(*out)
	must(err)
	defer of.Close()
	var omu sync.Mutex
	emit := func(r rec) {
		omu.Lock()
		defer omu.Unlock()
		b, _ := json.Marshal(r)
		of.Write(append(b, '\n'))
	}
	lanes := imageLanes()
	if *only != "" {
		var keep []*ilane
		for _, l := range lanes {
			for _, n := range strings.Split(*only, ",") {
				if n == l.name {
					keep = append(keep, l)
				}
			}
		}
		lanes = keep
	}
	if *full {
		for _, l := range lanes {
			l.revs = nil
		}
	}
	floor := frontier.NewFloor()
	cfsObjs := map[string]bool{}
	objChain := frontier.NewChain()
	base := frontier.Base()
	treeDir := filepath.Join(*work, "tree")
	var realMap map[string]string
	if *realDirs != "" {
		realMap = map[string]string{}
		var order []string
		for _, kv := range strings.Split(*realDirs, ",") {
			k, v, _ := strings.Cut(kv, "=")
			realMap[k] = v
			order = append(order, k)
		}
		*revs = strings.Join(order, ",")
	}
	objs := filepath.Join(*work, "objs")
	os.RemoveAll(objs)
	for _, rev := range strings.Split(*revs, ",") {
		t0 := time.Now()
		var tree *frontier.Tree
		treeDir := treeDir
		if realMap != nil {
			treeDir = realMap[rev]
			var err error
			tree, err = frontier.ScanTree(treeDir)
			must(err)
		} else {
			tree = frontier.Revision(base, rev)
			must(frontier.Materialize(tree, treeDir))
		}
		src, err := frontier.Snapshot(treeDir, true)
		must(err)
		fc, err := floor.Add(tree, treeDir)
		must(err)
		emit(rec{"rev": rev, "lane": "floor", "file_raw": fc.FileRaw, "file_packed": fc.FilePacked, "manifest": fc.Manifest, "total": fc.Total, "obj_new": fc.ObjNew, "obj_bytes": fc.ObjBytes, "snapshot": SnapDigest(src)})
		c := &ictx{rev: rev, tree: tree, dir: treeDir, work: *work, T: findTools(*tl), root: *tl, objs: objs, tarPath: filepath.Join(*work, "r.tar")}
		if realMap == nil {
			f, err := os.Create(c.tarPath)
			must(err)
			must(frontier.WriteTar(f, tree, treeDir))
			must(f.Close())
		}
		var wg sync.WaitGroup
		for _, l := range lanes {
			if !l.runs(rev) {
				continue
			}
			wg.Add(1)
			go func(l *ilane) {
				defer wg.Done()
				img := filepath.Join(*work, l.name+".img")
				t1 := time.Now()
				if err := l.build(c, img); err != nil {
					emit(rec{"rev": rev, "lane": l.name, "error": err.Error()})
					return
				}
				b, err := os.ReadFile(img)
				must(err)
				r := rec{"rev": rev, "lane": l.name, "size": len(b), "build_ms": time.Since(t1).Milliseconds()}
				if rev == "R0" || rev == "R10" || l.name == "ertar" {
					r["sha"] = fmt.Sprintf("%x", sha256.Sum256(b))
				}
				if rev == "R0" { // reproducibility: same producer, same input
					img2 := img + "2"
					if cerr := l.build(c, img2); cerr == nil {
						b2, _ := os.ReadFile(img2)
						r["repro"] = bytes.Equal(b, b2)
						os.Remove(img2)
					}
				}
				var sc *scanned
				if l.fam != "cfs" {
					sc, err = c.scanImage(l, img, b)
					if err != nil {
						r["scan_error"] = err.Error()
						sc = &scanned{info: rec{}, reads: rec{}}
					}
				} else {
					sc = &scanned{info: rec{}, reads: rec{}}
				}
				cls := map[string]float64{}
				l.chain.OnNew = func(off, n, packed int64) { attribute(sc.regions, off, n, packed, cls) }
				cost, err := l.chain.AddStream(bytes.NewReader(b))
				must(err)
				r["cdc"], r["cdc_class"] = cost, cls
				if sc.units != nil {
					r["native"] = l.nativeCost(sc.units)
				}
				for k, v := range sc.info {
					r[k] = v
				}
				for k, v := range sc.reads {
					r[k] = v
				}
				switch {
				case l.fam == "cfs":
					vr, objSet, err := c.cfsVerify(img, src)
					if err != nil {
						r["verify"] = rec{"ok": false, "err": err.Error()}
					} else {
						r["verify"] = vr
						var nNew int
						var newB, newAlloc int64
						var newObjs []string
						omu.Lock()
						for o := range objSet {
							if !cfsObjs[o] {
								cfsObjs[o] = true
								newObjs = append(newObjs, o)
							}
						}
						omu.Unlock()
						sort.Strings(newObjs)
						var oc frontier.Cost
						for _, o := range newObjs {
							st, err := os.Stat(filepath.Join(objs, o))
							must(err)
							nNew++
							newB += st.Size()
							newAlloc += st.Size()
							oc2, err := objChain.AddFile(filepath.Join(objs, o))
							must(err)
							oc.Packed += oc2.Packed
							oc.NewBytes += oc2.NewBytes
							oc.NewChunks += oc2.NewChunks
							oc.Chunks += oc2.Chunks
						}
						r["objects"] = len(objSet)
						r["obj_new"], r["obj_new_bytes"] = nNew, newB
						r["obj_cdc_packed"], r["obj_cdc_new_bytes"], r["obj_chunks"] = oc.Packed, oc.NewBytes, oc.Chunks
					}
				case *verify:
					r["verify"] = c.extractVerify(l, img, src)
				}
				emit(r)
				os.Remove(img)
			}(l)
		}
		wg.Wait()
		fmt.Fprintf(os.Stderr, "i24 %s done in %v\n", rev, time.Since(t0).Round(time.Second))
	}
	if realMap == nil {
		os.RemoveAll(treeDir)
	}
	os.RemoveAll(objs)
	os.Remove(filepath.Join(*work, "r.tar"))
}
