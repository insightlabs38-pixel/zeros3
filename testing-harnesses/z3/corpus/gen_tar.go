package corpus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Z3-11D: controlled tar families. Each revision rewrites the archive with
// archive/tar from a tree of nodes; one mutation per revision, so ground truth
// says exactly which member payloads are unchanged. Truth keys: n_members,
// n_payload_unchanged (members whose content digest also exists in the parent,
// by path-independent identity), plus changed / added / removed / renamed
// samples.

type tnode struct {
	path  string
	typ   byte
	body  []byte
	link  string
	mode  int64
	uid   int
	gid   int
	mtime int64
	pax   map[string]string
}

const tarEpoch = 1_700_000_000

func tarBytes(nodes []tnode, format tar.Format) []byte {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, n := range nodes {
		h := &tar.Header{Name: n.path, Typeflag: n.typ, Linkname: n.link, Mode: n.mode, Uid: n.uid, Gid: n.gid,
			ModTime: time.Unix(n.mtime, 0), Format: format}
		if format == tar.FormatPAX && n.pax != nil {
			h.PAXRecords = n.pax
		}
		if n.typ == tar.TypeReg {
			h.Size = int64(len(n.body))
		}
		if err := w.WriteHeader(h); err != nil {
			panic(err)
		}
		if h.Size > 0 {
			w.Write(n.body)
		}
	}
	w.Close()
	return buf.Bytes()
}

// tarTree builds the base tree: mostly small files, some medium ones, one large member.
func tarTree(r *RNG, size int) []tnode {
	var nodes []tnode
	total := 0
	dirs := 0
	big := size / 4
	files := 0
	for total < size || files < 12 {
		d := fmt.Sprintf("usr/lib/pkg%03d", dirs)
		nodes = append(nodes, tnode{path: d + "/", typ: tar.TypeDir, mode: 0o755, mtime: tarEpoch})
		dirs++
		for k := 0; k < 6 && (total < size || files < 12); k++ {
			var n int
			switch x := r.Intn(10); {
			case x < 6:
				n = 100 + r.Intn(3000)
			case x < 9:
				n = 4096 + r.Intn(28000)
			default:
				n = 40000 + r.Intn(60000)
			}
			n = min(n, size/8)
			if files == 3 { // the large member
				n = big
			}
			files++
			nodes = append(nodes, tnode{path: fmt.Sprintf("%s/f%02d.dat", d, k), typ: tar.TypeReg, body: Text(r, n), mode: 0o644, mtime: tarEpoch + int64(r.Intn(1000)), uid: 0})
			total += n
		}
		if dirs%4 == 0 {
			nodes = append(nodes, tnode{path: d + "/link", typ: tar.TypeSymlink, link: "f00.dat", mode: 0o777, mtime: tarEpoch})
		}
	}
	return nodes
}

func cloneNodes(n []tnode) []tnode {
	out := make([]tnode, len(n))
	copy(out, n)
	return out
}

func regs(n []tnode) []int {
	var ix []int
	for i := range n {
		if n[i].typ == tar.TypeReg && len(n[i].body) > 0 {
			ix = append(ix, i)
		}
	}
	return ix
}

func tarTruth(prev, cur []tnode, extra map[string][]string) map[string][]string {
	have := map[string]int{}
	for _, n := range prev {
		if n.typ == tar.TypeReg && len(n.body) > 0 {
			have[string(n.body)]++
		}
	}
	unchanged, members := 0, 0
	for _, n := range cur {
		if n.typ == tar.TypeReg && len(n.body) > 0 {
			members++
			if have[string(n.body)] > 0 {
				unchanged++
			}
		}
	}
	t := map[string][]string{"n_payload_members": {fmt.Sprint(members)}, "n_payload_unchanged": {fmt.Sprint(unchanged)}}
	for k, v := range extra {
		t[k] = v
	}
	return t
}

func init() {
	type mut func(c *Ctx, r *RNG, nodes []tnode, i int) ([]tnode, string, map[string][]string)
	add := func(name string, format tar.Format, gz bool, m mut) {
		f, ext := "tar", ".tar"
		if gz {
			f, ext = "gzip", ".tar.gz"
		}
		register(Family{Name: name, Format: f, Workload: "tar-layer", Ext: ext, ToolDependent: gz,
			gen: func(c *Ctx, emit func(Artifact) error) error {
				r := c.RNG("data")
				var nodes, prev []tnode
				return c.chain(emit, func(i int) ([]byte, string, map[string][]string) {
					var desc string
					var extra map[string][]string
					if i == 0 {
						nodes = tarTree(r, c.size)
						desc = "base tree"
					} else {
						prev = nodes
						nodes = cloneNodes(prev)
						nodes, desc, extra = m(c, r, nodes, i)
					}
					b := tarBytes(nodes, format)
					if gz {
						var buf bytes.Buffer
						w, _ := gzip.NewWriterLevel(&buf, 6)
						w.Write(b)
						w.Close()
						b = buf.Bytes()
					}
					return b, desc, tarTruth(prev, nodes, extra)
				})
			}})
	}
	mtime := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		for k := range n {
			n[k].mtime += int64(86400 * i)
		}
		return n, "every mtime shifted (payloads unchanged)", nil
	}
	own := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		for k := range n {
			n[k].uid, n[k].gid = 1000+i, 1000+i
		}
		return n, "uid/gid of every member changed", nil
	}
	mode := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		for _, k := range regs(n) {
			if k%10 == 0 {
				n[k].mode ^= 0o111
			}
		}
		return n, "mode of 10% of files toggled", nil
	}
	modOne := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		ix := regs(n)
		k := ix[r.Intn(len(ix))]
		n[k].body = Text(r, len(n[k].body))
		return n, "one file rewritten", map[string][]string{"changed": {n[k].path}}
	}
	addOne := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		d := fmt.Sprintf("usr/lib/pkg%03d", r.Intn(8))
		nn := tnode{path: fmt.Sprintf("%s/added%02d.dat", d, i), typ: tar.TypeReg, body: Text(r, 2000+r.Intn(20000)), mode: 0o644, mtime: tarEpoch}
		at := r.Intn(len(n))
		n = append(n[:at], append([]tnode{nn}, n[at:]...)...)
		return n, "one file added", map[string][]string{"added": {nn.path}}
	}
	rmOne := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		ix := regs(n)
		k := ix[r.Intn(len(ix))]
		p := n[k].path
		n = append(n[:k], n[k+1:]...)
		return n, "one file removed", map[string][]string{"removed": {p}}
	}
	rename := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		ix := regs(n)
		k := ix[r.Intn(len(ix))]
		old := n[k].path
		n[k].path = strings.Replace(old, "/f", fmt.Sprintf("/renamed%d_f", i), 1)
		return n, "one file renamed (same payload)", map[string][]string{"renamed": {old + "->" + n[k].path}}
	}
	renameDir := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		var dirs []string
		for k := range n {
			if n[k].typ == tar.TypeDir {
				dirs = append(dirs, strings.TrimSuffix(n[k].path, "/"))
			}
		}
		d := dirs[r.Intn(len(dirs))]
		nd := fmt.Sprintf("opt/moved%d_%s", i, d[strings.LastIndex(d, "/")+1:])
		for k := range n {
			if strings.HasPrefix(n[k].path, d+"/") {
				n[k].path = nd + n[k].path[len(d):]
			}
		}
		sort.SliceStable(n, func(a, b int) bool { return n[a].path < n[b].path })
		return n, "one directory renamed (re-sorted)", map[string][]string{"renamed": {d + "->" + nd}}
	}
	reorder := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		p := r.Perm(len(n))
		out := make([]tnode, len(n))
		for k, j := range p {
			out[k] = n[j]
		}
		return out, "member order permuted", nil
	}
	largeEdit := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		k := 0
		for _, j := range regs(n) {
			if len(n[j].body) > len(n[k].body) {
				k = j
			}
		}
		b := n[k].body
		at := len(b)/2 + r.Intn(1000)
		n[k].body = append(append(append([]byte(nil), b[:at]...), Text(r, 200)...), b[at:]...)
		return n, "200 B inserted in the middle of the largest member", map[string][]string{"changed": {n[k].path}}
	}
	links := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		var first string
		for _, j := range regs(n) {
			if first == "" {
				first = n[j].path
				continue
			}
			if j%25 == 0 { // some regular files become hardlinks to the first (payload dropped from the archive)
				n[j].typ, n[j].body, n[j].link = tar.TypeLink, nil, first
			}
		}
		for j := range n {
			if n[j].typ == tar.TypeSymlink {
				n[j].link = fmt.Sprintf("f%02d.dat", i%6)
			}
		}
		return n, "symlink targets retargeted, some files turned into hardlinks", nil
	}
	pax := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		for k := range n {
			n[k].pax = map[string]string{"comment": fmt.Sprintf("build %d", i), "mtime": fmt.Sprintf("%d.%06d", n[k].mtime, i*1000)}
		}
		return n, "PAX records of every member changed", nil
	}
	release := func(c *Ctx, r *RNG, n []tnode, i int) ([]tnode, string, map[string][]string) {
		n, _, _ = mtime(c, r, n, 1) // release engineering stamps everything again
		n, _, _ = modOne(c, r, n, i)
		n, _, e := addOne(c, r, n, i)
		return n, "release-like: global mtime shift, one file modified, one added", e
	}
	fmts := map[string]tar.Format{"": tar.FormatUnknown}
	_ = fmts
	add("tar-meta-mtime", tar.FormatUnknown, false, mtime)
	add("tar-meta-ownership", tar.FormatUnknown, false, own)
	add("tar-meta-mode", tar.FormatUnknown, false, mode)
	add("tar-modify-one", tar.FormatUnknown, false, modOne)
	add("tar-add-one", tar.FormatUnknown, false, addOne)
	add("tar-remove-one", tar.FormatUnknown, false, rmOne)
	add("tar-rename-one", tar.FormatUnknown, false, rename)
	add("tar-rename-dir", tar.FormatUnknown, false, renameDir)
	add("tar-reorder", tar.FormatUnknown, false, reorder)
	add("tar-large-edit", tar.FormatUnknown, false, largeEdit)
	add("tar-links", tar.FormatUnknown, false, links)
	add("tar-pax-meta", tar.FormatPAX, false, pax)
	add("tar-gnu-mtime", tar.FormatGNU, false, mtime)
	add("tar-release", tar.FormatUnknown, false, release)
	add("targz-meta-mtime", tar.FormatUnknown, true, mtime)
	add("targz-modify-one", tar.FormatUnknown, true, modOne)
	add("targz-release", tar.FormatUnknown, true, release)
}
