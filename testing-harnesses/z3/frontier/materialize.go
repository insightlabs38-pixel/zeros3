package frontier

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

func setMtimeNoFollow(p string, sec int64) error {
	ts := [2]syscall.Timespec{{Sec: sec}, {Sec: sec}}
	b, err := syscall.BytePtrFromString(p)
	if err != nil {
		return err
	}
	const atFDCWD, atSymlinkNoFollow = -100, 0x100
	_, _, e := syscall.Syscall6(syscall.SYS_UTIMENSAT, uintptr(atFDCWD&0xffffffffffffffff), uintptr(unsafe.Pointer(b)), uintptr(unsafe.Pointer(&ts[0])), atSymlinkNoFollow, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}

// Materialize writes the tree under dir (removed first). Sparse files get real holes.
func Materialize(t *Tree, dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ents := t.Entries
	if t.Order == "rev" {
		ents = append([]*Entry(nil), t.Entries...)
		for i, j := 0, len(ents)-1; i < j; i, j = i+1, j-1 {
			ents[i], ents[j] = ents[j], ents[i]
		}
	}
	abs := func(p string) string { return filepath.Join(dir, filepath.FromSlash(p)) }
	for _, e := range t.Entries { // dirs first, sorted parent-first
		if e.Kind == KDir {
			if err := os.MkdirAll(abs(e.Path), 0o755); err != nil {
				return err
			}
		}
	}
	for _, e := range ents {
		switch e.Kind {
		case KFile:
			if err := writeBody(abs(e.Path), e.Body); err != nil {
				return fmt.Errorf("%s: %w", e.Path, err)
			}
		case KSymlink:
			if err := os.Symlink(e.Link, abs(e.Path)); err != nil {
				return err
			}
		}
	}
	for _, e := range ents {
		if e.Kind == KHard {
			if err := os.Link(abs(e.Link), abs(e.Path)); err != nil {
				return err
			}
		}
	}
	for _, e := range t.Entries {
		if e.Kind == KDir || e.Kind == KHard {
			continue
		}
		p := abs(e.Path)
		if e.Kind != KSymlink {
			if err := os.Chmod(p, fs.FileMode(e.Mode)); err != nil {
				return err
			}
		}
		keys := make([]string, 0, len(e.Xattr))
		for k := range e.Xattr {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := syscall.Setxattr(p, k, []byte(e.Xattr[k]), 0); err != nil {
				return fmt.Errorf("setxattr %s: %w", e.Path, err)
			}
		}
		if err := setMtimeNoFollow(p, e.Mtime); err != nil {
			return err
		}
	}
	for i := len(t.Entries) - 1; i >= 0; i-- { // deepest dirs first
		if e := t.Entries[i]; e.Kind == KDir {
			p := abs(e.Path)
			if err := os.Chmod(p, fs.FileMode(e.Mode)); err != nil {
				return err
			}
			if err := setMtimeNoFollow(p, e.Mtime); err != nil {
				return err
			}
		}
	}
	return setMtimeNoFollow(dir, baseMtime)
}

func writeBody(p string, b *Body) error {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if !b.Sparse {
		_, err = f.Write(b.Bytes())
		return err
	}
	if err := f.Truncate(b.Size); err != nil {
		return err
	}
	for _, x := range b.Extents {
		if _, err := f.WriteAt(x.Bytes(), x.Off); err != nil {
			return err
		}
	}
	return nil
}

// Snap is the logical identity of one filesystem object.
type Snap struct {
	Type  string // d f l
	Mode  uint32
	Size  int64
	Mtime int64
	SHA   string
	Link  string // symlink target
	Hard  string // first path (sorted) sharing this inode, "" if nlink==1
	Xattr string
	Alloc int64 // allocated bytes (st_blocks*512) for regular files
}

// Snapshot walks dir and records every object (file content hashed).
func Snapshot(dir string, hash bool) (map[string]Snap, error) {
	out := map[string]Snap{}
	inos := map[uint64]string{}
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(paths, func(i, j int) bool { return Less(paths[i], paths[j]) })
	for _, p := range paths {
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		var st syscall.Stat_t
		if err := syscall.Lstat(p, &st); err != nil {
			return nil, err
		}
		s := Snap{Mode: st.Mode & 0o7777, Size: st.Size, Mtime: st.Mtim.Sec}
		switch st.Mode & syscall.S_IFMT {
		case syscall.S_IFDIR:
			s.Type, s.Size = "d", 0
		case syscall.S_IFLNK:
			s.Type = "l"
			s.Link, _ = os.Readlink(p)
			s.Mode = 0
		case syscall.S_IFREG:
			s.Type, s.Alloc = "f", st.Blocks*512
			if st.Nlink > 1 {
				if first, ok := inos[st.Ino]; ok {
					s.Hard = first
				} else {
					inos[st.Ino] = rel
					s.Hard = rel
				}
			}
			if hash {
				h, err := hashFile(p)
				if err != nil {
					return nil, err
				}
				s.SHA = h
			}
		default:
			return nil, fmt.Errorf("%s: unsupported type %o", rel, st.Mode&syscall.S_IFMT)
		}
		if s.Type != "l" {
			s.Xattr = readXattrs(p)
		}
		out[rel] = s
	}
	return out, nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readXattrs(p string) string {
	n, err := syscall.Listxattr(p, nil)
	if err != nil || n <= 0 {
		return ""
	}
	buf := make([]byte, n)
	n, err = syscall.Listxattr(p, buf)
	if err != nil {
		return ""
	}
	var parts []string
	for _, k := range strings.Split(strings.TrimRight(string(buf[:n]), "\x00"), "\x00") {
		if !strings.HasPrefix(k, "user.") {
			continue
		}
		v := make([]byte, 256)
		m, err := syscall.Getxattr(p, k, v)
		if err != nil {
			continue
		}
		parts = append(parts, k+"="+string(v[:m]))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// Features selects which attributes a format is expected to preserve.
type Features struct {
	Mtime, DirMtime, Xattr, Hard, Mode, Sparse bool
	NoDirAttrs                                 bool // skip mode/mtime/xattr of directories
}

// Diff compares got against want under feat and returns up to 8 mismatches.
func Diff(want, got map[string]Snap, feat Features) (n int, sample []string) {
	note := func(f string, a ...any) {
		if n++; len(sample) < 8 {
			sample = append(sample, fmt.Sprintf(f, a...))
		}
	}
	for p, w := range want {
		g, ok := got[p]
		if !ok {
			note("missing %s", p)
			continue
		}
		switch {
		case w.Type != g.Type:
			note("%s: type %s != %s", p, w.Type, g.Type)
			continue
		case w.Type == "l" && w.Link != g.Link:
			note("%s: link %q != %q", p, w.Link, g.Link)
		case w.Type == "f" && (w.Size != g.Size || w.SHA != g.SHA):
			note("%s: content differs", p)
		}
		if w.Type == "d" && feat.NoDirAttrs {
			continue
		}
		if feat.Mode && w.Type != "l" && w.Mode != g.Mode {
			note("%s: mode %o != %o", p, w.Mode, g.Mode)
		}
		if feat.Mtime && (w.Type == "f" || w.Type == "l" || (w.Type == "d" && feat.DirMtime)) && w.Mtime != g.Mtime {
			note("%s: mtime %d != %d", p, w.Mtime, g.Mtime)
		}
		if feat.Xattr && w.Xattr != g.Xattr {
			note("%s: xattr %q != %q", p, w.Xattr, g.Xattr)
		}
		if feat.Hard && w.Hard != g.Hard {
			note("%s: hardlink %q != %q", p, w.Hard, g.Hard)
		}
	}
	for p := range got {
		if _, ok := want[p]; !ok {
			note("extra %s", p)
		}
	}
	return
}

// ScanTree builds a Tree description of an existing directory (a real-world revision).
// Hard links become KHard entries pointing at the first path seen; file bodies are not
// described (consumers read contents from disk).
func ScanTree(dir string) (*Tree, error) {
	t := &Tree{Rev: filepath.Base(dir)}
	inos := map[uint64]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		var st syscall.Stat_t
		if err := syscall.Lstat(p, &st); err != nil {
			return err
		}
		e := &Entry{Path: rel, Mode: st.Mode & 0o7777, Mtime: st.Mtim.Sec}
		switch st.Mode & syscall.S_IFMT {
		case syscall.S_IFDIR:
			e.Kind = KDir
		case syscall.S_IFLNK:
			e.Kind = KSymlink
			e.Link, _ = os.Readlink(p)
		case syscall.S_IFREG:
			e.Kind = KFile
			if first, ok := inos[st.Ino]; ok && st.Nlink > 1 {
				e.Kind, e.Link = KHard, first
			} else if st.Nlink > 1 {
				inos[st.Ino] = rel
			}
			if e.Kind == KFile {
				e.Body = &Body{Size: st.Size}
			}
		default:
			return fmt.Errorf("%s: unsupported file type", rel)
		}
		t.Entries = append(t.Entries, e)
		return nil
	})
	t.sort()
	return t, err
}
