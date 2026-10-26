package frontier

import (
	"fmt"
	"path"
	"strings"
)

const MiB = 1 << 20

// Revisions lists the shared revision family in chain order.
var Revisions = []string{"R0", "R1", "R2", "R3", "R4", "R5", "R6", "R7", "R8", "R9", "R10", "R11"}

var RevDesc = map[string]string{
	"R0": "first sight", "R1": "tiny edit (40 B in 32 MiB file)", "R2": "6 sparse distant edits",
	"R3": "metadata only (mode/mtime/xattr)", "R4": "rename/move (dir + ~70 files)", "R5": "add 40 / remove 40",
	"R6": "tiny-file churn (~3.2k files)", "R7": "duplicate set rename/modify/delete", "R8": "tail/threshold growth (~700 files)",
	"R9": "sparse layout change", "R10": "producer order perturbation (same tree)", "R11": "mostly unrelated payloads",
}

func file(p string, mode uint32, gen string, seed uint64, size int64) *Entry {
	return &Entry{Path: p, Kind: KFile, Mode: mode, Mtime: pathMtime(p), Body: &Body{Gen: gen, Seed: seed, Size: size}}
}

// Base builds R0: ~215 MiB logical (~148 MiB data) from fixed seeds.
func Base() *Tree {
	t := &Tree{Rev: "R0"}
	r := newRNG(42)
	gens := []string{"text", "json", "text", "log"}
	exts := []string{"txt", "json", "go", "log"}
	// 6000 tiny files in 120 directories, 3-4 levels deep.
	idx := 0
	for d := 0; d < 12; d++ {
		for s := 0; s < 10; s++ {
			for f := 0; f < 50; f++ {
				k := idx % 4
				p := fmt.Sprintf("tiny/d%02d/s%02d/f%04d.%s", d, s, f, exts[k])
				e := file(p, 0o644, gens[k], uint64(1000+idx), int64(100+r.IntN(2900)))
				if idx%40 == 0 {
					e.Xattr = map[string]string{"user.z3.tag": fmt.Sprintf("t%d", idx)}
				}
				t.Entries = append(t.Entries, e)
				idx++
			}
		}
	}
	// 400 medium text/json/log files.
	idx = 0
	dirs := []string{"docs", "data", "logs"}
	mg := []string{"text", "json", "log"}
	me := []string{"md", "json", "log"}
	for i := 0; i < 400; i++ {
		k := i % 3
		area := (i / 3) % 4
		p := fmt.Sprintf("%s/area%d/m%03d.%s", dirs[k], area, i, me[k])
		e := file(p, 0o644, mg[k], uint64(5000+i), int64(4096+r.IntN(116*1024)))
		if i%10 == 0 {
			e.Xattr = map[string]string{"user.z3.tag": fmt.Sprintf("m%d", i)}
		}
		t.Entries = append(t.Entries, e)
	}
	// 8 random-ish binaries 1..8 MiB.
	for i := 0; i < 8; i++ {
		t.Entries = append(t.Entries, file(fmt.Sprintf("bin/b%d.dat", i), 0o755, "rand", uint64(9000+i), int64(i+1)*MiB))
	}
	// highly compressible big files.
	t.Entries = append(t.Entries,
		file("data/c0.log", 0o644, "log", 9100, 4*MiB), file("data/c1.txt", 0o644, "text", 9101, 5*MiB),
		file("data/c2.zz", 0o644, "zeroish", 9102, 6*MiB), file("data/c3.json", 0o644, "json", 9103, 8*MiB))
	// duplicates: 100 identical 128 KiB files; 3 unique 1 MiB random files x2.
	for i := 0; i < 100; i++ {
		t.Entries = append(t.Entries, file(fmt.Sprintf("dup/a/f%03d.txt", i), 0o644, "text", 9200, 128*1024))
	}
	for i := 0; i < 3; i++ {
		for c := 0; c < 2; c++ {
			t.Entries = append(t.Entries, file(fmt.Sprintf("dup/b/u%d_c%d.dat", i, c), 0o644, "rand", uint64(9300+i), MiB))
		}
	}
	// one large regular file, one large sparse file.
	t.Entries = append(t.Entries, file("big/large.dat", 0o644, "records", 7, 32*MiB))
	sp := file("big/sparse.img", 0o644, "rand", 0, 72*MiB)
	sp.Body.Sparse = true
	for i, x := range [][2]int64{{0, 256 << 10}, {4 << 20, 512 << 10}, {11 << 20, 1 << 20}, {20<<20 + 3*4096, 768 << 10},
		{33 << 20, 256 << 10}, {47 << 20, 1 << 20}, {60 << 20, 512 << 10}, {70 << 20, 1 << 20}} {
		sp.Body.Extents = append(sp.Body.Extents, Extent{Off: x[0], Len: x[1], Seed: uint64(8100 + i)})
	}
	t.Entries = append(t.Entries, sp)
	// hard links and symlinks.
	for i := 0; i < 20; i++ {
		a := fmt.Sprintf("links/h%02d_a.txt", i)
		t.Entries = append(t.Entries, file(a, 0o644, "text", uint64(9400+i), 20*1024))
		t.Entries = append(t.Entries, &Entry{Path: fmt.Sprintf("links/h%02d_b.txt", i), Kind: KHard, Link: a})
	}
	for i := 0; i < 30; i++ {
		p := fmt.Sprintf("links/s%02d", i)
		t.Entries = append(t.Entries, &Entry{Path: p, Kind: KSymlink, Mode: 0o777, Mtime: pathMtime(p), Link: fmt.Sprintf("../docs/area0/m%03d.md", (i*12)%396)})
	}
	// tail / block-threshold files: sizes just below and above 4K..512K block sizes.
	for i, sz := range []int64{4096 - 100, 4096 + 100, 8192 - 100, 65536 - 100, 131072 - 100, 131072 + 100, 262144 - 100, 524288 - 100} {
		for c := 0; c < 5; c++ {
			g := "text"
			if c%2 == 1 {
				g = "rand"
			}
			t.Entries = append(t.Entries, file(fmt.Sprintf("thr/t%d_%d.dat", i, c), 0o644, g, uint64(9500+i*10+c), sz))
		}
	}
	t.ensureDirs()
	t.fixLinks()
	return t
}

// fixLinks makes hard-link entries mirror their target's inode attributes.
func (t *Tree) fixLinks() {
	ix := t.index()
	for _, e := range t.Entries {
		if e.Kind == KHard {
			if tg := ix[e.Link]; tg != nil {
				e.Mode, e.Mtime, e.Xattr = tg.Mode, tg.Mtime, tg.Xattr
			}
		}
	}
}

func (t *Tree) files(pred func(*Entry) bool) []*Entry {
	var out []*Entry
	for _, e := range t.Entries {
		if e.Kind == KFile && (pred == nil || pred(e)) {
			out = append(out, e)
		}
	}
	return out
}

func prefix(p string) func(*Entry) bool {
	return func(e *Entry) bool { return strings.HasPrefix(e.Path, p) }
}

func (t *Tree) rename(oldPath, newPath string) {
	for _, e := range t.Entries {
		if e.Path == oldPath {
			e.Path = newPath
		} else if strings.HasPrefix(e.Path, oldPath+"/") {
			e.Path = newPath + e.Path[len(oldPath):]
		}
		if (e.Kind == KHard) && (e.Link == oldPath || strings.HasPrefix(e.Link, oldPath+"/")) {
			e.Link = newPath + e.Link[len(oldPath):]
		}
	}
}

// Revision derives revision rev (R1..R11) from the base tree.
func Revision(base *Tree, rev string) *Tree {
	t := base.Clone(rev)
	switch rev {
	case "R0":
	case "R1":
		ix := t.index()
		b := ix["big/large.dat"].Body
		b.Patches = append(b.Patches, Patch{Off: 17_000_003, Len: 40, Seed: 101})
	case "R2":
		b := t.index()["big/large.dat"].Body
		for i, off := range []int64{1_234_567, 6_100_001, 11_999_999, 19_000_123, 25_500_777, 31_000_001} {
			b.Patches = append(b.Patches, Patch{Off: off, Len: 16 + 9*i, Seed: uint64(200 + i)})
		}
	case "R3":
		for i, e := range t.files(nil) {
			if strings.HasPrefix(e.Path, "links/h") {
				continue
			}
			if i%20 == 0 {
				e.Mode ^= 0o044 // 0644 <-> 0600, 0755 <-> 0711
			}
			if i%15 == 7 {
				e.Mtime += 86400
			}
			if e.Xattr != nil {
				for k := range e.Xattr {
					e.Xattr[k] += "+"
				}
			} else if i%50 == 3 {
				e.Xattr = map[string]string{"user.z3.tag": "added"}
			}
		}
	case "R4":
		t.rename("tiny/d03", "tiny/d03_moved")
		n := 0
		for _, e := range t.files(prefix("docs/area1/")) {
			if n++; n <= 60 {
				t.rename(e.Path, "archive/area1/"+path.Base(e.Path))
			}
		}
		n = 0
		for _, e := range t.files(prefix("data/area")) {
			if n++; n <= 40 {
				ext := path.Ext(e.Path)
				t.rename(e.Path, strings.TrimSuffix(e.Path, ext)+"_v2"+ext)
			}
		}
	case "R5":
		for i := 0; i < 40; i++ {
			g := []string{"text", "json", "rand"}[i%3]
			t.Entries = append(t.Entries, file(fmt.Sprintf("added/n%03d.dat", i), 0o644, g, uint64(20000+i), int64(1024+i*7000)))
		}
		n := 0
		t.remove(func(e *Entry) bool {
			if e.Kind == KFile && strings.HasPrefix(e.Path, "docs/area2/") && n < 30 {
				n++
				return true
			}
			return false
		})
		m := 0
		t.remove(func(e *Entry) bool {
			if e.Kind == KFile && strings.HasPrefix(e.Path, "tiny/d05/s00/") && m < 10 {
				m++
				return true
			}
			return false
		})
	case "R6":
		for i, e := range t.files(prefix("tiny/")) {
			switch {
			case i%3 == 0:
				e.Body.Seed += 100000
			case i%5 == 1:
				e.Body.Size += 20
				e.Body.Patches = append(e.Body.Patches, Patch{Off: e.Body.Size - 20, Len: 20, Seed: uint64(i)})
			}
		}
	case "R7":
		for i := 0; i < 50; i++ {
			t.rename(fmt.Sprintf("dup/a/f%03d.txt", i), fmt.Sprintf("dup/renamed/f%03d.txt", i))
		}
		t.index()[fmt.Sprintf("dup/a/f%03d.txt", 50)].Body.Patches = []Patch{{Off: 1000, Len: 1, Seed: 7}}
		t.remove(func(e *Entry) bool {
			var n int
			_, err := fmt.Sscanf(e.Path, "dup/a/f%03d.txt", &n)
			return err == nil && n >= 75
		})
		for i := 0; i < 20; i++ {
			t.Entries = append(t.Entries, file(fmt.Sprintf("dup/c/x%02d.dat", i), 0o644, "rand", 9300, MiB))
		}
	case "R8":
		for i, e := range t.files(prefix("tiny/")) {
			if i%10 == 0 {
				e.Body.Patches = append(e.Body.Patches, Patch{Off: e.Body.Size - 37, Len: 37, Seed: uint64(300 + i)})
			}
		}
		for i, e := range t.files(prefix("docs/")) {
			if i%4 == 0 {
				e.Body.Size += 61
				e.Body.Patches = append(e.Body.Patches, Patch{Off: e.Body.Size - 37, Len: 37, Seed: uint64(400 + i)})
			}
		}
		for _, e := range t.files(prefix("thr/")) {
			if sz := e.Body.Size; sz == 4096-100 || sz == 8192-100 || sz == 65536-100 || sz == 131072-100 || sz == 262144-100 || sz == 524288-100 {
				e.Body.Size += 200
			}
		}
	case "R9":
		b := t.index()["big/sparse.img"].Body
		x := b.Extents
		next := []Extent{x[0], x[1] /* x[2] hole-punched */, x[3], {Off: x[4].Off + 1<<20, Len: x[4].Len, Seed: x[4].Seed}, x[5],
			{Off: x[6].Off, Len: x[6].Len, Zero: true}, x[7], {Off: 25 << 20, Len: 512 << 10, Seed: 8200}}
		b.Extents = next
	case "R10":
		t.Order = "rev"
	case "R11":
		for i, e := range t.files(nil) {
			if i%10 == 0 {
				continue
			}
			e.Body.Seed = e.Body.Seed*31 + 17
			e.Body.Patches = nil
			for j := range e.Body.Extents {
				e.Body.Extents[j].Seed = e.Body.Extents[j].Seed*31 + 17
			}
		}
	default:
		panic("frontier: unknown revision " + rev)
	}
	t.ensureDirs()
	t.fixLinks()
	return t
}
