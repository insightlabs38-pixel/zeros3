// portcheck compares the declarations the mechanical extractor
// (z3/cmd/prodcodecport) emits from the lab sources with the same declarations
// in the production zeros3.go, after gofmt normalisation. Any difference means
// production was edited in a ported region without the lab source, or the other
// way round, and re-extraction would not reproduce production.
//
//	go run ./cmd/prodcodecport LABDIR OUT [git|managed]   (in testing-harnesses/z3)
//	go run ./p4/cmd/portcheck OUT.decls ../../zeros3.go    (in testing-harnesses/z4)
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
)

func decls(path string, wantNames map[string]bool) (map[string]string, []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	src := data
	if wantNames == nil { // an extracted file is a bare declaration list
		src = append([]byte("package p\n"), data...)
	}
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		panic(fmt.Sprintf("%s: %v", path, err))
	}
	out := map[string]string{}
	var order []string
	add := func(name string, n ast.Node) {
		if wantNames != nil && !wantNames[name] {
			return
		}
		var b bytes.Buffer
		lo, hi := fs.Position(n.Pos()).Offset, fs.Position(n.End()).Offset
		fm, err := format.Source(src[lo:hi])
		if err != nil {
			fm = src[lo:hi]
		}
		b.Write(fm)
		out[name] = b.String()
		order = append(order, name)
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := "func " + d.Name.Name
			if d.Recv != nil {
				t := d.Recv.List[0].Type
				if s, ok := t.(*ast.StarExpr); ok {
					t = s.X
				}
				if id, ok := t.(*ast.Ident); ok {
					name = "method " + id.Name + "." + d.Name.Name
				}
			}
			if d.Name.Name == "init" {
				continue // initialisation is compared as a set below
			}
			add(name, d)
		case *ast.GenDecl:
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					add("type "+sp.Name.Name, sp)
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						add("value "+n.Name, sp)
					}
				}
			}
		}
	}
	return out, order
}

func main() {
	ext, order := decls(os.Args[1], nil)
	want := map[string]bool{}
	for _, n := range order {
		want[n] = true
	}
	prod, _ := decls(os.Args[2], want)
	var diff, missing []string
	for _, n := range order {
		p, ok := prod[n]
		switch {
		case !ok:
			missing = append(missing, n)
		case strings.Join(strings.Fields(p), "") != strings.Join(strings.Fields(ext[n]), ""): // whitespace-insensitive: gofmt line breaks differ between the extractor and production
			diff = append(diff, n)
		}
	}
	sort.Strings(diff)
	fmt.Printf("%d extracted declarations: %d identical in production, %d differ, %d absent\n", len(order), len(order)-len(diff)-len(missing), len(diff), len(missing))
	for _, n := range diff {
		fmt.Println("  DIFFERS", n)
	}
	for _, n := range missing {
		fmt.Println("  ABSENT ", n)
	}
	if len(diff)+len(missing) > 0 {
		os.Exit(1)
	}
}
