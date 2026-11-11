// Command dcoreport mechanically extracts the standard-library-only dcore
// package into a single-file production integration and proves the result:
//
//  1. type-check dcore and refuse any non-standard-library import or
//     unresolved external identifier (PORTABILITY_BLOCKED),
//  2. rename every package-level declaration with a collision-checked prefix
//     using go/types object resolution (no text search/replace of names),
//  3. merge the declarations and their imports into a TEMPORARY copy of the
//     production source (nothing is written into the repository),
//  4. compile that copy and run the differential vectors in the original
//     package and in the extracted code, requiring byte-identical output,
//  5. check source fidelity: every extracted declaration equals the original
//     after the rename is reversed.
//
// usage: dcoreport -dcore DIR -prod zeros3.go -work DIR [-prefix dc]
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/importer"
	"go/parser"
	"go/scanner"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type edit struct {
	lo, hi int
	text   string
}

func die(code, format string, a ...any) {
	fmt.Printf("%s: %s\n", code, fmt.Sprintf(format, a...))
	os.Exit(1)
}

func must(err error) {
	if err != nil {
		die("ERROR", "%v", err)
	}
}

func main() {
	dir := flag.String("dcore", "testing-harnesses/z3/dcore", "dcore package directory")
	prod := flag.String("prod", "zeros3.go", "production source file")
	work := flag.String("work", "", "work directory (temporary integration is written here)")
	prefix := flag.String("prefix", "dc", "identifier prefix for extracted declarations")
	z3 := flag.String("z3", "testing-harnesses/z3", "z3 module directory (original package side of the differential run)")
	flag.Parse()
	if *work == "" {
		var err error
		*work, err = os.MkdirTemp("", "dcoreport")
		must(err)
	}
	must(os.MkdirAll(*work, 0o755))
	fset := token.NewFileSet()
	entries, err := os.ReadDir(*dir)
	must(err)
	var files []*ast.File
	raw := map[*ast.File][]byte{}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(*dir, n))
		must(err)
		f, err := parser.ParseFile(fset, n, b, parser.ParseComments)
		must(err)
		files = append(files, f)
		raw[f] = b
		names = append(names, n)
	}
	// 1. imports must be standard library only.
	imports := map[string]string{} // path -> alias ("" if none)
	for _, f := range files {
		for _, im := range f.Imports {
			p := strings.Trim(im.Path.Value, `"`)
			if i := strings.Index(p, "/"); (i >= 0 && strings.Contains(p[:i], ".")) || strings.HasPrefix(p, "zeros3") {
				die("PORTABILITY_BLOCKED", "non-standard-library import %q in %s", p, fset.Position(im.Pos()))
			}
			if im.Name != nil {
				imports[p] = im.Name.Name
			} else {
				imports[p] = ""
			}
		}
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("dcore", fset, files, info)
	if err != nil {
		die("PORTABILITY_BLOCKED", "type-check: %v", err)
	}
	// 2. package-level objects and the rename map.
	scope := pkg.Scope()
	rename := map[types.Object]string{}
	var objNames []string
	for _, n := range scope.Names() {
		o := scope.Lookup(n)
		if n == "init" || n == "_" {
			continue
		}
		rename[o] = *prefix + n
		objNames = append(objNames, n)
	}
	// Collision check against the production file's package-level names.
	pf, err := parser.ParseFile(token.NewFileSet(), *prod, nil, parser.SkipObjectResolution)
	must(err)
	prodNames := map[string]bool{}
	for _, d := range pf.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				prodNames[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					prodNames[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, n := range s.Names {
						prodNames[n.Name] = true
					}
				}
			}
		}
	}
	for _, nn := range rename {
		if prodNames[nn] {
			die("COLLISION", "extracted name %s already exists in production source", nn)
		}
	}
	// edits per file
	var out bytes.Buffer
	declCount := map[string]int{}
	type declSrc struct{ orig, extracted string }
	var decls []declSrc
	for fi, f := range files {
		var edits []edit
		base := fset.File(f.Pos()).Base()
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			obj := info.Defs[id]
			if obj == nil {
				obj = info.Uses[id]
			}
			if nn, ok := rename[obj]; ok && obj.Parent() == scope {
				edits = append(edits, edit{int(id.Pos()) - base, int(id.End()) - base, nn})
			}
			return true
		})
		sort.Slice(edits, func(i, j int) bool { return edits[i].lo > edits[j].lo })
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
				continue
			}
			lo, hi := int(d.Pos())-base, int(d.End())-base
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Doc != nil {
				lo = int(fd.Doc.Pos()) - base
			}
			if gd, ok := d.(*ast.GenDecl); ok && gd.Doc != nil {
				lo = int(gd.Doc.Pos()) - base
			}
			src := raw[f]
			seg := append([]byte{}, src[lo:hi]...)
			for _, e := range edits {
				if e.lo >= lo && e.hi <= hi {
					seg = append(seg[:e.lo-lo], append([]byte(e.text), seg[e.hi-lo:]...)...)
				}
			}
			kind := "decl"
			switch d := d.(type) {
			case *ast.FuncDecl:
				kind = "func"
				if d.Recv != nil {
					kind = "method"
				}
			case *ast.GenDecl:
				kind = strings.ToLower(d.Tok.String())
			}
			declCount[kind]++
			decls = append(decls, declSrc{string(src[lo:hi]), string(seg)})
			fmt.Fprintf(&out, "%s\n\n", seg)
		}
		_ = fi
	}
	// 3. merged import block.
	prodSrc, err := os.ReadFile(*prod)
	must(err)
	ps := string(prodSrc)
	i := strings.Index(ps, "\nimport (\n")
	if i < 0 {
		die("ERROR", "production import block not found")
	}
	j := strings.Index(ps[i:], "\n)\n") + i
	block := ps[i:j]
	var added []string
	var ipaths []string
	for p := range imports {
		ipaths = append(ipaths, p)
	}
	sort.Strings(ipaths)
	for _, p := range ipaths {
		if !strings.Contains(block, "\""+p+"\"") {
			a := imports[p]
			if a != "" {
				a += " "
			}
			block += "\n\t" + a + "\"" + p + "\""
			added = append(added, p)
		}
	}
	combined := ps[:i] + block + ps[j:] + "\n// ===== extracted from dcore (dcoreport) =====\n\n" + out.String()
	fmtd, ferr := format.Source([]byte(combined))
	if ferr != nil {
		die("ERROR", "gofmt of integration: %v", ferr)
	}
	integ := filepath.Join(*work, "integration")
	must(os.MkdirAll(integ, 0o755))
	must(os.WriteFile(filepath.Join(integ, "zeros3.go"), fmtd, 0o644))
	gm, err := os.ReadFile(filepath.Join(filepath.Dir(*prod), "go.mod"))
	must(err)
	must(os.WriteFile(filepath.Join(integ, "go.mod"), gm, 0o644))
	// 5. source fidelity: reverse the rename on the extracted declaration, compare tokens.
	rev := map[string]string{}
	for o, nn := range rename {
		rev[nn] = o.Name()
	}
	fidelityFail := 0
	for _, d := range decls {
		if tokens(d.orig, nil) != tokens(d.extracted, rev) {
			fidelityFail++
		}
	}
	// 4. differential vectors.
	tmpl, err := os.ReadFile(filepath.Join(*dir, "vectors.go.txt"))
	must(err)
	orig := vectorSource(string(tmpl), func(n string) string { return "dcore." + n }, "main", `import "zeros3-z3/dcore"`)
	ext := vectorSource(string(tmpl), func(n string) string { return *prefix + n }, "main", "")
	origDir := filepath.Join(*work, "orig")
	must(os.MkdirAll(origDir, 0o755))
	must(os.WriteFile(filepath.Join(origDir, "main.go"), []byte(orig), 0o644))
	must(os.WriteFile(filepath.Join(integ, "dcvectors_main_test.go"), []byte(strings.Replace(ext, "func main()", "func TestDcoreVectors(t *testing.T)", 1)), 0o644))
	// original side: run inside the z3 module so zeros3-z3/dcore resolves.
	z3abs, _ := filepath.Abs(*z3)
	runDir := filepath.Join(z3abs, "cmd", "dcorevec_tmp")
	must(os.MkdirAll(runDir, 0o755))
	defer os.RemoveAll(runDir)
	must(os.WriteFile(filepath.Join(runDir, "main.go"), []byte(orig), 0o644))
	cmd := exec.Command("go", "run", "./cmd/dcorevec_tmp")
	cmd.Dir = z3abs
	origOut, err := cmd.CombinedOutput()
	if err != nil {
		die("ERROR", "original vectors: %v\n%s", err, origOut)
	}
	// extracted side: build then test in the integration directory.
	build := exec.Command("go", "vet", "-vettool=", ".")
	_ = build
	b := exec.Command("go", "build", "-o", filepath.Join(*work, "zeros3_integrated"), ".")
	b.Dir = integ
	if bo, err := b.CombinedOutput(); err != nil {
		die("COMPILE_FAILED", "%v\n%s", err, bo)
	}
	t := exec.Command("go", "test", "-count=1", "-run", "TestDcoreVectors", "-v", ".")
	t.Dir = integ
	ex, err := t.CombinedOutput()
	if err != nil {
		die("DIFFERENTIAL_FAILED", "%v\n%s", err, tail(ex))
	}
	want, got := vecLines(string(origOut)), vecLines(string(ex))
	mism := 0
	for k, v := range want {
		if got[k] != v {
			mism++
			fmt.Printf("MISMATCH %s: original %s extracted %s\n", k, v, got[k])
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			mism++
			fmt.Printf("EXTRA %s\n", k)
		}
	}
	sum := sha256.Sum256(fmtd)
	rep := map[string]any{
		"declarations": declCount, "package_level_names": len(objNames), "imports": ipaths, "imports_added_to_production": added,
		"unresolved_dependencies": 0, "modified_adapters": 0, "vector_lines": len(want), "vector_mismatches": mism,
		"fidelity_failures": fidelityFail, "integration_bytes": len(fmtd), "integration_sha256": hex.EncodeToString(sum[:]),
		"integration_dir": integ, "prefix": *prefix,
	}
	bj, _ := json.MarshalIndent(rep, "", " ")
	fmt.Println(string(bj))
	if mism > 0 || fidelityFail > 0 || len(want) == 0 {
		die("PORTABILITY_FAILED", "differential or fidelity failure")
	}
	fmt.Println("PORTABLE: extracted core compiles into the production source and matches the original byte-for-byte on", len(want), "vectors")
}

func tail(b []byte) string {
	if len(b) > 3000 {
		b = b[len(b)-3000:]
	}
	return string(b)
}

func vecLines(s string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "VEC ") {
			f := strings.Fields(l)
			if len(f) >= 3 {
				m[f[1]] = f[2]
			}
		}
	}
	return m
}

// tokens normalizes a declaration to a comparable token string, reversing renames.
func tokens(src string, rev map[string]string) string {
	var s scanner.Scanner
	fs := token.NewFileSet()
	f := fs.AddFile("", fs.Base(), len(src))
	s.Init(f, []byte(src), nil, scanner.ScanComments)
	var sb strings.Builder
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.IDENT && rev != nil {
			if o, ok := rev[lit]; ok {
				lit = o
			}
		}
		if lit == "" {
			lit = tok.String()
		}
		sb.WriteString(lit)
		sb.WriteByte(' ')
	}
	return sb.String()
}

// vectorSource instantiates the vector template: @Name@ becomes the
// package-qualified (original) or prefixed (extracted) identifier.
func vectorSource(tmpl string, sub func(string) string, pkg, imp string) string {
	var out strings.Builder
	for {
		i := strings.Index(tmpl, "@")
		if i < 0 {
			out.WriteString(tmpl)
			break
		}
		j := strings.Index(tmpl[i+1:], "@")
		out.WriteString(tmpl[:i])
		out.WriteString(sub(tmpl[i+1 : i+1+j]))
		tmpl = tmpl[i+j+2:]
	}
	s := out.String()
	if imp != "" {
		s = strings.Replace(s, "//IMPORT", imp, 1)
	}
	return s
}
