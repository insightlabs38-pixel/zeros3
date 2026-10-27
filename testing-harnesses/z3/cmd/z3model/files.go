package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/modelz3"
)

// file is one artifact: a pinned corpus entry ("group/name") or a locally derived re-export
// ("derived/<name>", produced by realworld/derive_models.sh with the pinned producer).
type file struct {
	Name string
	Path string
	Size int64
	f    *os.File
	src  *modelz3.Src
	lay  *modelz3.Layout
	cuts map[string]modelz3.CompList
}

type store struct {
	c       *modelz3.Corpus
	derived string
	files   map[string]*file
}

func newStore(c *modelz3.Corpus, derived string) *store {
	return &store{c: c, derived: derived, files: map[string]*file{}}
}

func (s *store) get(name string) *file {
	if f, ok := s.files[name]; ok {
		return f
	}
	var path string
	if d, ok := strings.CutPrefix(name, "derived/"); ok {
		path = filepath.Join(s.derived, d)
	} else {
		g, n, _ := strings.Cut(name, "/")
		for _, e := range s.c.Group(g) {
			if e.Name == n {
				path = s.c.Path(e)
			}
		}
	}
	if path == "" {
		fatalf("unknown artifact %q", name)
	}
	fh, err := os.Open(path)
	check(err)
	st, err := fh.Stat()
	check(err)
	f := &file{Name: name, Path: path, Size: st.Size(), f: fh, cuts: map[string]modelz3.CompList{}}
	f.src = &modelz3.Src{R: fh, Size: f.Size, Name: name}
	s.files[name] = f
	return f
}

func (f *file) cut(mode string) modelz3.CompList {
	if l, ok := f.cuts[mode]; ok {
		return l
	}
	l, err := modelz3.Cut(mode, f.src, f.lay)
	check(err)
	f.cuts[mode] = l
	return l
}

func fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "z3model: "+f+"\n", a...)
	os.Exit(1)
}
