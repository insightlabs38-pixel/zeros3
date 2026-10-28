// Mechanical, type-aware extraction of the reachable lab codec declarations.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
)

type pkgMeta struct {
	ImportPath, Dir, Export string
	GoFiles                 []string
}
type source struct {
	file *ast.File
	raw  []byte
	meta pkgMeta
	info *types.Info
}
type decl struct {
	node ast.Decl
	src  *source
	deps []string
}
type edit struct {
	lo, hi int
	text   string
}

var prefixes = map[string]string{"zeros3-z3/deflate": "repDF", "zeros3-z3/ir": "repIR", "zeros3-z3/zstdz3": "repZS"}

func key(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil || prefixes[obj.Pkg().Path()] == "" {
		return ""
	}
	if obj.Parent() == obj.Pkg().Scope() {
		return obj.Pkg().Path() + ":" + obj.Name()
	}
	if f, ok := obj.(*types.Func); ok {
		if sig, ok := f.Type().(*types.Signature); ok && sig.Recv() != nil {
			return obj.Pkg().Path() + ":" + types.TypeString(sig.Recv().Type(), func(p *types.Package) string { return p.Path() }) + "." + obj.Name()
		}
	}
	return ""
}
func renamed(obj types.Object) string {
	if v, ok := obj.(*types.Var); ok && v.Embedded() && v.Pkg() != nil && prefixes[v.Pkg().Path()] != "" {
		return prefixes[v.Pkg().Path()] + v.Name()
	}
	if key(obj) == "" || obj.Parent() != obj.Pkg().Scope() {
		return ""
	}
	return prefixes[obj.Pkg().Path()] + obj.Name()
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	if len(os.Args) != 3 {
		panic("extract lab-directory output-prefix")
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "list", "-deps", "-export", "-json", "./deflate", "./ir", "./zstdz3")
	cmd.Dir = os.Args[1]
	data, err := cmd.Output()
	must(err)
	metas := map[string]pkgMeta{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for {
		var m pkgMeta
		err := decoder.Decode(&m)
		if err == io.EOF {
			break
		}
		must(err)
		metas[m.ImportPath] = m
	}
	fs := token.NewFileSet()
	imp := importer.ForCompiler(fs, "gc", func(path string) (io.ReadCloser, error) { return os.Open(metas[path].Export) })
	var sources []*source
	for _, path := range []string{"zeros3-z3/deflate", "zeros3-z3/ir", "zeros3-z3/zstdz3"} {
		m := metas[path]
		info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
		var files []*ast.File
		for _, name := range m.GoFiles {
			raw, err := os.ReadFile(filepath.Join(m.Dir, name))
			must(err)
			f, err := parser.ParseFile(fs, filepath.Join(m.Dir, name), raw, parser.ParseComments)
			must(err)
			files = append(files, f)
			sources = append(sources, &source{f, raw, m, info})
		}
		cfg := types.Config{Importer: imp}
		_, err := cfg.Check(path, fs, files, info)
		must(err)
	}
	owners := map[string]*decl{}
	var declarations []*decl
	for _, s := range sources {
		for _, n := range s.file.Decls {
			if g, ok := n.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
				continue
			}
			d := &decl{node: n, src: s}
			declarations = append(declarations, d)
			ast.Inspect(n, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				if k := key(s.info.Defs[id]); k != "" {
					owners[k] = d
				}
				if k := key(s.info.Uses[id]); k != "" {
					d.deps = append(d.deps, k)
				}
				return true
			})
		}
	}
	// Interface dispatch is implicit: retain a selected receiver type's methods.
	for k, d := range owners {
		if f, ok := d.node.(*ast.FuncDecl); ok && f.Recv != nil {
			obj, ok := d.src.info.Defs[f.Name].(*types.Func)
			if !ok {
				continue
			}
			rt := obj.Type().(*types.Signature).Recv().Type()
			if ptr, ok := rt.(*types.Pointer); ok {
				rt = ptr.Elem()
			}
			if named, ok := rt.(*types.Named); ok {
				if td := owners[key(named.Obj())]; td != nil {
					td.deps = append(td.deps, k)
				}
			}
		}
	}
	roots := []string{"zeros3-z3/ir:EncodeStream", "zeros3-z3/ir:ReconstructStream", "zeros3-z3/ir:MaterializeRange", "zeros3-z3/ir:ParseRangeIndex", "zeros3-z3/deflate:GzipHeader", "zeros3-z3/zstdz3:Parse", "zeros3-z3/zstdz3:Replay", "zeros3-z3/zstdz3:ReplayFrom"}
	keep := map[*decl]bool{}
	var visit func(string)
	visit = func(k string) {
		d := owners[k]
		if d == nil {
			panic("missing declaration " + k)
		}
		if keep[d] {
			return
		}
		keep[d] = true
		for _, dep := range d.deps {
			visit(dep)
		}
	}
	for _, r := range roots {
		visit(r)
	}
	imports := map[string]string{}
	var out bytes.Buffer
	fmt.Fprintln(&out, "// Mechanically extracted from zeros3-z3 codec sources; see provenance manifest.")
	for _, d := range declarations {
		if !keep[d] {
			continue
		}
		s := d.src
		lo, hi := fs.Position(d.node.Pos()).Offset, fs.Position(d.node.End()).Offset
		switch n := d.node.(type) {
		case *ast.GenDecl:
			if n.Doc != nil {
				lo = fs.Position(n.Doc.Pos()).Offset
			}
		case *ast.FuncDecl:
			if n.Doc != nil {
				lo = fs.Position(n.Doc.Pos()).Offset
			}
		}
		var edits []edit
		var covered []edit
		ast.Inspect(d.node, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			pn, ok := s.info.Uses[id].(*types.PkgName)
			if !ok {
				return true
			}
			if prefix := prefixes[pn.Imported().Path()]; prefix != "" {
				e := edit{fs.Position(sel.Pos()).Offset, fs.Position(sel.End()).Offset, prefix + sel.Sel.Name}
				edits = append(edits, e)
				covered = append(covered, e)
			} else {
				imports[pn.Imported().Path()] = pn.Name()
			}
			return true
		})
		ast.Inspect(d.node, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			at := fs.Position(id.Pos()).Offset
			for _, e := range covered {
				if at >= e.lo && at < e.hi {
					return true
				}
			}
			obj := s.info.Uses[id]
			if obj == nil {
				obj = s.info.Defs[id]
			}
			if name := renamed(obj); name != "" {
				edits = append(edits, edit{at, fs.Position(id.End()).Offset, name})
			}
			return true
		})
		sort.Slice(edits, func(i, j int) bool { return edits[i].lo > edits[j].lo })
		frag := string(s.raw[lo:hi])
		last := hi
		for _, e := range edits {
			if e.hi > last {
				panic("overlapping edit")
			}
			frag = frag[:e.lo-lo] + e.text + frag[e.hi-lo:]
			last = e.lo
		}
		fmt.Fprintf(&out, "\n// Port source: %s/%s\n%s\n", s.meta.ImportPath, filepath.Base(fs.Position(d.node.Pos()).Filename), frag)
	}
	must(os.WriteFile(os.Args[2]+".decls", out.Bytes(), 0600))
	var importText bytes.Buffer
	var paths []string
	for p := range imports {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintf(&importText, "%s %s\n", imports[p], strconv.Quote(p))
	}
	must(os.WriteFile(os.Args[2]+".imports", importText.Bytes(), 0600))
	fmt.Printf("extracted %d of %d declarations (%d lines)\n", len(keep), len(declarations), bytes.Count(out.Bytes(), []byte("\n")))
}
