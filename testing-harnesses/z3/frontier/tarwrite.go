package frontier

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteTar emits a deterministic PAX tar of the materialised tree: uid/gid 0,
// no names, integer mtimes, xattrs as SCHILY.xattr records, hard links as link
// entries to the first-seen member, sparse files as full zeros (OCI layers have
// no sparse representation). Order follows the tree (reverse files for R10).
func WriteTar(w io.Writer, t *Tree, dir string) error {
	tw := tar.NewWriter(w)
	ents := append([]*Entry(nil), t.Entries...)
	if t.Order == "rev" {
		var dirs, rest []*Entry
		for _, e := range ents {
			if e.Kind == KDir {
				dirs = append(dirs, e)
			} else {
				rest = append(rest, e)
			}
		}
		for i, j := 0, len(rest)-1; i < j; i, j = i+1, j-1 {
			rest[i], rest[j] = rest[j], rest[i]
		}
		ents = append(dirs, rest...)
	}
	first := map[string]string{} // hard-link group (target path) -> first emitted path
	group := func(e *Entry) string {
		if e.Kind == KHard {
			return e.Link
		}
		return e.Path
	}
	hasLinks := map[string]bool{}
	for _, e := range t.Entries {
		if e.Kind == KHard {
			hasLinks[e.Link] = true
		}
	}
	for _, e := range ents {
		h := &tar.Header{Name: e.Path, Mode: int64(e.Mode), ModTime: time.Unix(e.Mtime, 0), Format: tar.FormatPAX}
		if len(e.Xattr) > 0 {
			h.PAXRecords = map[string]string{}
			for k, v := range e.Xattr {
				h.PAXRecords["SCHILY.xattr."+k] = v
			}
		}
		switch e.Kind {
		case KDir:
			h.Typeflag, h.Name = tar.TypeDir, e.Path+"/"
			h.Mode = int64(e.Mode)
		case KSymlink:
			h.Typeflag, h.Linkname, h.Mode = tar.TypeSymlink, e.Link, 0o777
		case KFile, KHard:
			g := group(e)
			if p, ok := first[g]; ok && (e.Kind == KHard || hasLinks[g]) {
				h.Typeflag, h.Linkname = tar.TypeLink, p
				if err := tw.WriteHeader(h); err != nil {
					return err
				}
				continue
			}
			first[g] = e.Path
			src := e.Path
			if e.Kind == KHard {
				src = e.Link
			}
			f, err := os.Open(filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(src, "/"))))
			if err != nil {
				return err
			}
			st, _ := f.Stat()
			h.Typeflag, h.Size = tar.TypeReg, st.Size()
			if err := tw.WriteHeader(h); err != nil {
				f.Close()
				return err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return err
			}
		}
	}
	return tw.Close()
}
