// Package frontier holds the shared Z3-23/Z3-24 laboratory pieces: one
// deterministic source-tree revision corpus (R0..R11), its materialisation and
// logical snapshot/verification, CDC chain accounting over produced artifacts,
// and small bounded scanners for native container/image structure. Producers
// (tar, zstd, mksquashfs, mkfs.erofs, mkcomposefs, nydus-image, estargz) are
// external and live outside Git; nothing here is a production contract.
package frontier

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
)

type Kind uint8

const (
	KDir Kind = iota
	KFile
	KSymlink
	KHard // hard link to Link (an earlier regular file path)
)

// Extent is one data region of a sparse file; everything else is a hole.
// Zero extents are written as literal (allocated) zero bytes.
type Extent struct {
	Off  int64
	Len  int64
	Seed uint64
	Zero bool
}

// Patch overwrites Len bytes at Off with PRNG bytes after the body is generated.
type Patch struct {
	Off  int64
	Len  int
	Seed uint64
}

// Body describes deterministic file content.
type Body struct {
	Gen     string // text json log rand zeroish records
	Seed    uint64
	Size    int64
	Patches []Patch
	Sparse  bool // data only in Extents, holes elsewhere (Gen/Seed unused)
	Extents []Extent
}

type Entry struct {
	Path  string
	Kind  Kind
	Mode  uint32
	Mtime int64
	Xattr map[string]string
	Link  string
	Body  *Body
}

func (e *Entry) clone() *Entry {
	c := *e
	if e.Xattr != nil {
		c.Xattr = map[string]string{}
		for k, v := range e.Xattr {
			c.Xattr[k] = v
		}
	}
	if e.Body != nil {
		b := *e.Body
		b.Patches = append([]Patch(nil), e.Body.Patches...)
		b.Extents = append([]Extent(nil), e.Body.Extents...)
		c.Body = &b
	}
	return &c
}

// Tree is a logical filesystem tree: entries sorted by path-segment order.
type Tree struct {
	Rev     string
	Order   string // "" = sorted traversal; "rev" = producers see the reverse order (R10)
	Entries []*Entry
}

const baseMtime = 1_700_000_000

func pathMtime(p string) int64 {
	h := fnv.New64a()
	h.Write([]byte(p))
	return baseMtime + int64(h.Sum64()%1_000_000)
}

// Less orders paths segment-wise (what a depth-first sorted walk yields).
func Less(a, b string) bool {
	as, bs := strings.Split(a, "/"), strings.Split(b, "/")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

func (t *Tree) sort() {
	sort.SliceStable(t.Entries, func(i, j int) bool { return Less(t.Entries[i].Path, t.Entries[j].Path) })
}

func (t *Tree) Clone(rev string) *Tree {
	n := &Tree{Rev: rev}
	for _, e := range t.Entries {
		n.Entries = append(n.Entries, e.clone())
	}
	return n
}

func (t *Tree) index() map[string]*Entry {
	m := make(map[string]*Entry, len(t.Entries))
	for _, e := range t.Entries {
		m[e.Path] = e
	}
	return m
}

func (t *Tree) remove(pred func(*Entry) bool) {
	out := t.Entries[:0]
	for _, e := range t.Entries {
		if !pred(e) {
			out = append(out, e)
		}
	}
	t.Entries = out
}

// ensureDirs adds missing parent directories.
func (t *Tree) ensureDirs() {
	have := map[string]bool{}
	for _, e := range t.Entries {
		if e.Kind == KDir {
			have[e.Path] = true
		}
	}
	for _, e := range append([]*Entry(nil), t.Entries...) {
		p := e.Path
		for {
			i := strings.LastIndexByte(p, '/')
			if i < 0 {
				break
			}
			p = p[:i]
			if !have[p] {
				have[p] = true
				t.Entries = append(t.Entries, &Entry{Path: p, Kind: KDir, Mode: 0o755, Mtime: pathMtime(p)})
			}
		}
	}
	t.sort()
}

// Stats summarises logical size.
type Stats struct {
	Files, Dirs, Symlinks, Hard int
	Logical, Data               int64
}

func (t *Tree) Stats() Stats {
	var s Stats
	for _, e := range t.Entries {
		switch e.Kind {
		case KDir:
			s.Dirs++
		case KSymlink:
			s.Symlinks++
		case KHard:
			s.Hard++
		case KFile:
			s.Files++
			s.Logical += e.Body.Size
			if e.Body.Sparse {
				for _, x := range e.Body.Extents {
					s.Data += x.Len
				}
			} else {
				s.Data += e.Body.Size
			}
		}
	}
	return s
}

func (s Stats) String() string {
	return fmt.Sprintf("files=%d dirs=%d symlinks=%d hardlinks=%d logical=%.1fMiB data=%.1fMiB",
		s.Files, s.Dirs, s.Symlinks, s.Hard, float64(s.Logical)/(1<<20), float64(s.Data)/(1<<20))
}
