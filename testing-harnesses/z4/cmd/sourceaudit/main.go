// Inspect immutable draft declarations without modifying production sources.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
)

type decl struct {
	Name, SHA   string
	Line, Lines int
	Signals     map[string]int
}

func declarations(path string) map[string]decl {
	data, e := os.ReadFile(path)
	if e != nil {
		panic(e)
	}
	fs := token.NewFileSet()
	f, e := parser.ParseFile(fs, path, data, 0)
	if e != nil {
		panic(e)
	}
	out := map[string]decl{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fn.Name.Name
		if fn.Recv != nil {
			v := fn.Recv.List[0].Type
			if p, ok := v.(*ast.StarExpr); ok {
				v = p.X
			}
			if id, ok := v.(*ast.Ident); ok {
				name = id.Name + "." + name
			}
		}
		start, end := fs.Position(fn.Pos()), fs.Position(fn.End())
		body := data[start.Offset:end.Offset]
		sum := sha256.Sum256(body)
		signals := map[string]int{}
		for _, s := range []string{"io.ReadAll", "os.ReadFile", "make(map", "map[", "append(", "TODO", "FIXME", "unsafe.", "exec.Command"} {
			if n := strings.Count(string(body), s); n > 0 {
				signals[s] = n
			}
		}
		out[name] = decl{name, hex.EncodeToString(sum[:]), start.Line, end.Line - start.Line + 1, signals}
	}
	return out
}
func main() {
	base := declarations(os.Args[1])
	drafts := []map[string]decl{}
	added := [][]decl{}
	for _, p := range os.Args[2:] {
		m := declarations(p)
		drafts = append(drafts, m)
		var a []decl
		for k, v := range m {
			if _, ok := base[k]; !ok {
				a = append(a, v)
			}
		}
		sort.Slice(a, func(i, j int) bool { return a[i].Line < a[j].Line })
		added = append(added, a)
	}
	shared := []decl{}
	if len(drafts) == 2 {
		for k, a := range drafts[0] {
			if _, ok := base[k]; ok {
				continue
			}
			if b, ok := drafts[1][k]; ok && a.SHA == b.SHA {
				shared = append(shared, a)
			}
		}
	}
	sort.Slice(shared, func(i, j int) bool { return shared[i].Name < shared[j].Name })
	json.NewEncoder(os.Stdout).Encode(struct {
		Added                [][]decl
		IdenticalSharedAdded []decl
	}{added, shared})
}
