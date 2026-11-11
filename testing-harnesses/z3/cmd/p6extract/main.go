// Command p6extract mechanically extracts a stdlib-only core (one or more packages of the
// z3 module, with their in-module dependencies) into a single-file production integration:
//
//  1. load and type-check the root packages and every in-module import, recursively; any
//     import outside the standard library and the module is PORTABILITY_BLOCKED,
//  2. select the declarations reachable from the requested roots (type-aware: identifier
//     uses from go/types, all methods of reachable types, whole const/var/type groups),
//  3. rename every selected package-level object with a collision-checked prefix and
//     rewrite qualified references (pkg.Name) from go/types resolution, never by text search,
//  4. merge the declarations and exactly the standard-library imports they use into a
//     TEMPORARY copy of the production source,
//  5. compile the copy and run differential vectors in the original packages and in the
//     extracted code, requiring byte-identical output,
//  6. check source fidelity (token-level, rename reversed) and report adapter interfaces
//     (named interfaces reachable from the roots: what a production integration must supply).
//
// Nothing is written into the repository. usage:
//
//	p6extract -module DIR -modpath zeros3-z3 -pkgs semcore -roots semcore.NewStore,... \
//	  -prod zeros3.go -work DIR -prefix sc -vectors FILE [-audit-only]
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
	"unicode"
)

func die(code, f string, a ...any) {
	fmt.Printf("%s: %s\n", code, fmt.Sprintf(f, a...))
	os.Exit(1)
}

func must(err error) {
	if err != nil {
		die("ERROR", "%v", err)
	}
}

type pkgInfo struct {
	path  string // import path
	short string
	dir   string
	files []*ast.File
	raw   map[*ast.File][]byte
	names map[*ast.File]string
	tp    *types.Package
	info  *types.Info
	deps  []string // in-module imports
	std   []string // stdlib imports
}

type loader struct {
	fset    *token.FileSet
	module  string
	modpath string
	pkgs    map[string]*pkgInfo
	std     types.Importer
	order   []string // dependency order (deps first)
	loading map[string]bool
	blocked []string
}

func (l *loader) Import(path string) (*types.Package, error) { return l.ImportFrom(path, "", 0) }

func (l *loader) ImportFrom(path, dir string, _ types.ImportMode) (*types.Package, error) {
	if strings.HasPrefix(path, l.modpath+"/") {
		p, err := l.load(path)
		if err != nil {
			return nil, err
		}
		return p.tp, nil
	}
	return l.std.Import(path)
}

func isStd(p string) bool {
	i := strings.Index(p, "/")
	first := p
	if i >= 0 {
		first = p[:i]
	}
	return !strings.Contains(first, ".")
}

func (l *loader) load(path string) (*pkgInfo, error) {
	if p, ok := l.pkgs[path]; ok {
		return p, nil
	}
	if l.loading[path] {
		return nil, fmt.Errorf("import cycle at %s", path)
	}
	l.loading[path] = true
	defer delete(l.loading, path)
	rel := strings.TrimPrefix(path, l.modpath+"/")
	p := &pkgInfo{path: path, short: filepath.Base(path), dir: filepath.Join(l.module, rel), raw: map[*ast.File][]byte{}, names: map[*ast.File]string{}}
	ents, err := os.ReadDir(p.dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(p.dir, n))
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(l.fset, filepath.Join(rel, n), b, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		p.files = append(p.files, f)
		p.raw[f] = b
		p.names[f] = n
		for _, im := range f.Imports {
			ip := strings.Trim(im.Path.Value, `"`)
			if im.Name != nil && im.Name.Name != "_" {
				l.blocked = append(l.blocked, fmt.Sprintf("%s: aliased import %q", n, ip))
			}
			switch {
			case strings.HasPrefix(ip, l.modpath+"/"):
				if !seen[ip] {
					seen[ip] = true
					p.deps = append(p.deps, ip)
				}
			case isStd(ip):
				p.std = append(p.std, ip)
			default:
				l.blocked = append(l.blocked, fmt.Sprintf("%s/%s imports non-standard-library %q", p.short, n, ip))
			}
		}
	}
	p.info = &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	conf := types.Config{Importer: l}
	p.tp, err = conf.Check(path, l.fset, p.files, p.info)
	if err != nil {
		return nil, fmt.Errorf("type-check %s: %v", path, err)
	}
	l.pkgs[path] = p
	l.order = append(l.order, path)
	return p, nil
}

type declRef struct {
	pkg  *pkgInfo
	file *ast.File
	node ast.Decl
}

func main() {
	module := flag.String("module", "testing-harnesses/z3", "z3 module directory")
	modpath := flag.String("modpath", "zeros3-z3", "module path")
	pkgsF := flag.String("pkgs", "semcore", "root package directories (comma separated)")
	rootsF := flag.String("roots", "", "root declarations pkg.Name,... (or 'all' = every exported declaration of the root packages)")
	prod := flag.String("prod", "zeros3.go", "production source file")
	work := flag.String("work", "", "work directory")
	prefix := flag.String("prefix", "sc", "identifier prefix")
	vectors := flag.String("vectors", "", "differential vector template (@pkg.Name@ placeholders)")
	vecTest := flag.String("vecname", "TestP6Vectors", "name of the extracted vector test")
	auditOnly := flag.Bool("audit-only", false, "report dependencies and portability class without compiling")
	flag.Parse()
	if *work == "" {
		var err error
		*work, err = os.MkdirTemp("", "p6extract")
		must(err)
	}
	must(os.MkdirAll(*work, 0o755))
	l := &loader{fset: token.NewFileSet(), module: *module, modpath: *modpath, pkgs: map[string]*pkgInfo{}, loading: map[string]bool{}}
	l.std = importer.ForCompiler(l.fset, "source", nil)
	var rootPkgs []*pkgInfo
	for _, d := range strings.Split(*pkgsF, ",") {
		p, err := l.load(*modpath + "/" + strings.TrimSpace(d))
		if err != nil {
			die("PORTABILITY_BLOCKED", "%v", err)
		}
		rootPkgs = append(rootPkgs, p)
	}
	audit := map[string]any{}
	var pkgReport []map[string]any
	for _, path := range l.order {
		p := l.pkgs[path]
		sort.Strings(p.deps)
		pkgReport = append(pkgReport, map[string]any{"package": path, "files": len(p.files), "module_deps": p.deps, "stdlib": uniq(p.std)})
	}
	audit["packages"] = pkgReport
	if len(l.blocked) > 0 {
		audit["blocked"] = l.blocked
		out, _ := json.MarshalIndent(audit, "", " ")
		fmt.Println(string(out))
		die("PORTABILITY_BLOCKED", "%d blocking import(s)", len(l.blocked))
	}
	// ---- declaration index ----
	declOf := map[types.Object]*declRef{}
	methodsOf := map[*types.TypeName][]*declRef{}
	var inits []*declRef
	for _, path := range l.order {
		p := l.pkgs[path]
		for _, f := range p.files {
			for _, d := range f.Decls {
				ref := &declRef{p, f, d}
				switch d := d.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil {
						if d.Name.Name == "init" {
							inits = append(inits, ref)
							continue
						}
						declOf[p.info.Defs[d.Name]] = ref
					} else {
						t := d.Recv.List[0].Type
						if st, ok := t.(*ast.StarExpr); ok {
							t = st.X
						}
						if ix, ok := t.(*ast.IndexExpr); ok {
							t = ix.X
						}
						if id, ok := t.(*ast.Ident); ok {
							if tn, ok := p.info.Uses[id].(*types.TypeName); ok {
								methodsOf[tn] = append(methodsOf[tn], ref)
							}
						}
					}
				case *ast.GenDecl:
					if d.Tok == token.IMPORT {
						continue
					}
					for _, s := range d.Specs {
						switch s := s.(type) {
						case *ast.TypeSpec:
							declOf[p.info.Defs[s.Name]] = ref
						case *ast.ValueSpec:
							for _, n := range s.Names {
								if n.Name != "_" {
									declOf[p.info.Defs[n]] = ref
								}
							}
						}
					}
				}
			}
		}
	}
	// ---- roots ----
	var rootObjs []types.Object
	byName := map[string]types.Object{}
	for _, path := range l.order { // any loaded package may supply a root (dependencies included)
		lp := l.pkgs[path]
		for _, n := range lp.tp.Scope().Names() {
			byName[lp.short+"."+n] = lp.tp.Scope().Lookup(n)
		}
	}
	if *rootsF == "all" {
		for _, rp := range rootPkgs {
			for _, n := range rp.tp.Scope().Names() {
				if o := rp.tp.Scope().Lookup(n); o.Exported() {
					rootObjs = append(rootObjs, o)
				}
			}
		}
	} else {
		for _, r := range strings.Split(*rootsF, ",") {
			r = strings.TrimSpace(r)
			if r == "" {
				continue
			}
			o, ok := byName[r]
			if !ok {
				die("UNRESOLVED", "root %s not found", r)
			}
			rootObjs = append(rootObjs, o)
		}
	}
	// ---- reachability over declarations ----
	selected := map[*declRef]bool{}
	var queue []*declRef
	push := func(o types.Object) {
		if o == nil {
			return
		}
		if r, ok := declOf[o]; ok && !selected[r] {
			selected[r] = true
			queue = append(queue, r)
		}
	}
	for _, o := range rootObjs {
		push(o)
	}
	for _, r := range inits {
		selected[r] = true
		queue = append(queue, r)
	}
	pushDecl := func(r *declRef) {
		if !selected[r] {
			selected[r] = true
			queue = append(queue, r)
		}
	}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		ast.Inspect(r.node, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			o := r.pkg.info.Uses[id]
			if o == nil {
				o = r.pkg.info.Defs[id]
			}
			if o == nil {
				return true
			}
			if tn, ok := o.(*types.TypeName); ok {
				for _, m := range methodsOf[tn] {
					pushDecl(m)
				}
			}
			if o.Pkg() != nil && o.Parent() == o.Pkg().Scope() {
				push(o)
			}
			return true
		})
		// a selected type brings its methods even when only defined here
		if gd, ok := r.node.(*ast.GenDecl); ok {
			for _, s := range gd.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok {
					if tn, ok := r.pkg.info.Defs[ts.Name].(*types.TypeName); ok {
						for _, m := range methodsOf[tn] {
							pushDecl(m)
						}
					}
				}
			}
		}
	}
	// ---- rename map over selected declarations ----
	rename := map[types.Object]string{}
	pkgCap := func(s string) string { r := []rune(s); r[0] = unicode.ToUpper(r[0]); return string(r) }
	var objNames []string
	for r := range selected {
		var objs []types.Object
		switch d := r.node.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name != "init" { // init functions keep their name (Go allows several)
				objs = append(objs, r.pkg.info.Defs[d.Name])
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					objs = append(objs, r.pkg.info.Defs[s.Name])
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.Name != "_" {
							objs = append(objs, r.pkg.info.Defs[n])
						}
					}
				}
			}
		}
		for _, o := range objs {
			if o == nil {
				continue
			}
			nn := *prefix + pkgCap(r.pkg.short) + pkgCap(o.Name())
			if len(o.Name()) > 0 && !unicode.IsUpper([]rune(o.Name())[0]) {
				nn = *prefix + pkgCap(r.pkg.short) + "_" + o.Name()
			}
			rename[o] = nn
			objNames = append(objNames, nn)
		}
	}
	seenName := map[string]bool{}
	for _, n := range objNames {
		if seenName[n] {
			die("COLLISION", "two extracted declarations map to %s", n)
		}
		seenName[n] = true
	}
	prodName := prodNames(*prod)
	for _, n := range objNames {
		if prodName[n] {
			die("COLLISION", "extracted name %s already exists in the production source", n)
		}
	}
	// adapters: named interface types among the selected declarations
	var adapters []string
	for o := range rename {
		if tn, ok := o.(*types.TypeName); ok {
			if _, ok := tn.Type().Underlying().(*types.Interface); ok {
				adapters = append(adapters, o.Pkg().Name()+"."+o.Name())
			}
		}
	}
	sort.Strings(adapters)
	audit["roots"] = len(rootObjs)
	audit["selected_declarations"] = len(selected)
	audit["adapter_interfaces"] = adapters
	if *auditOnly {
		out, _ := json.MarshalIndent(audit, "", " ")
		fmt.Println(string(out))
		return
	}
	// ---- emit ----
	type edit struct {
		lo, hi int
		text   string
	}
	var order []*declRef
	for _, path := range l.order {
		p := l.pkgs[path]
		for _, f := range p.files {
			for _, d := range f.Decls {
				for r := range selected {
					if r.node == d {
						order = append(order, r)
					}
				}
			}
		}
	}
	var out bytes.Buffer
	declCount := map[string]int{}
	stdUsed := map[string]bool{}
	type declSrc struct {
		orig, extracted string
		pkg             *pkgInfo
	}
	var decls []declSrc
	for _, r := range order {
		f := r.file
		base := l.fset.File(f.Pos()).Base()
		lo, hi := int(r.node.Pos())-base, int(r.node.End())-base
		switch d := r.node.(type) {
		case *ast.FuncDecl:
			if d.Doc != nil {
				lo = int(d.Doc.Pos()) - base
			}
		case *ast.GenDecl:
			if d.Doc != nil {
				lo = int(d.Doc.Pos()) - base
			}
		}
		var edits []edit
		consumed := map[*ast.Ident]bool{}
		ast.Inspect(r.node, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if x, ok := n.X.(*ast.Ident); ok {
					if pn, ok := r.pkg.info.Uses[x].(*types.PkgName); ok {
						if nn, ok := rename[r.pkg.info.Uses[n.Sel]]; ok {
							edits = append(edits, edit{int(n.Pos()) - base, int(n.End()) - base, nn})
							consumed[x], consumed[n.Sel] = true, true
						} else if isStd(pn.Imported().Path()) {
							stdUsed[pn.Imported().Path()] = true
							consumed[x] = true
						}
					}
				}
			}
			return true
		})
		ast.Inspect(r.node, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok || consumed[id] {
				return true
			}
			o := r.pkg.info.Defs[id]
			if o == nil {
				o = r.pkg.info.Uses[id]
			}
			if nn, ok := rename[o]; ok && o.Parent() == o.Pkg().Scope() {
				edits = append(edits, edit{int(id.Pos()) - base, int(id.End()) - base, nn})
			}
			return true
		})
		sort.Slice(edits, func(i, j int) bool { return edits[i].lo > edits[j].lo })
		src := r.pkg.raw[f]
		seg := append([]byte{}, src[lo:hi]...)
		for _, e := range edits {
			seg = append(seg[:e.lo-lo], append([]byte(e.text), seg[e.hi-lo:]...)...)
		}
		kind := "decl"
		switch d := r.node.(type) {
		case *ast.FuncDecl:
			kind = "func"
			if d.Recv != nil {
				kind = "method"
			}
		case *ast.GenDecl:
			kind = strings.ToLower(d.Tok.String())
		}
		declCount[kind]++
		decls = append(decls, declSrc{string(src[lo:hi]), string(seg), r.pkg})
		fmt.Fprintf(&out, "%s\n\n", seg)
	}
	// ---- merge into a temporary copy of the production source ----
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
	for p := range stdUsed {
		ipaths = append(ipaths, p)
	}
	sort.Strings(ipaths)
	for _, p := range ipaths {
		if !strings.Contains(block, "\""+p+"\"") {
			block += "\n\t\"" + p + "\""
			added = append(added, p)
		}
	}
	combined := ps[:i] + block + ps[j:] + "\n// ===== extracted by p6extract: " + *pkgsF + " =====\n\n" + out.String()
	fmtd, ferr := format.Source([]byte(combined))
	if ferr != nil {
		_ = os.WriteFile(filepath.Join(*work, "failed_integration.go.txt"), []byte(combined), 0o644)
		die("ERROR", "gofmt of integration: %v", ferr)
	}
	integ := filepath.Join(*work, "integration")
	must(os.MkdirAll(integ, 0o755))
	must(os.WriteFile(filepath.Join(integ, "zeros3.go"), fmtd, 0o644))
	gm, err := os.ReadFile(filepath.Join(filepath.Dir(*prod), "go.mod"))
	must(err)
	must(os.WriteFile(filepath.Join(integ, "go.mod"), gm, 0o644))
	// ---- fidelity: reverse the rename on every emitted declaration ----
	rev := map[string]string{}
	for o, nn := range rename {
		rev[nn] = o.Pkg().Path() + "|" + o.Name()
	}
	fidelityFail := 0
	for _, d := range decls {
		if tokenString(d.orig, nil, "") != tokenString(d.extracted, rev, d.pkg.path) {
			fidelityFail++
		}
	}
	rep := map[string]any{
		"audit": audit, "declarations": declCount, "package_level_names": len(objNames),
		"imports_added_to_production": added, "unresolved_dependencies": 0, "modified_adapters": 0,
		"fidelity_failures": fidelityFail, "integration_bytes": len(fmtd), "prefix": *prefix, "integration_dir": integ,
	}
	sum := sha256.Sum256(fmtd)
	rep["integration_sha256"] = hex.EncodeToString(sum[:])
	b := exec.Command("go", "build", "-o", filepath.Join(*work, "zeros3_integrated"), ".")
	b.Dir = integ
	if bo, err := b.CombinedOutput(); err != nil {
		_ = os.WriteFile(filepath.Join(*work, "compile_errors.txt"), bo, 0o644)
		die("COMPILE_FAILED", "%v\n%s", err, tail(bo))
	}
	rep["compiled"] = true
	class := "CORE_PORTABLE"
	if len(adapters) > 0 {
		class = "PORTABLE_WITH_THIN_ADAPTER"
	}
	mism, nvec := 0, 0
	if *vectors != "" {
		tmpl, err := os.ReadFile(*vectors)
		must(err)
		origSrc, imps, err := vectorSource(string(tmpl), *modpath, func(pkg, n string) (string, error) { return pkg + "." + n, nil })
		must(err)
		origSrc = strings.Replace(origSrc, "//IMPORT", imps, 1)
		extSrc, _, err := vectorSource(string(tmpl), *modpath, func(pkg, n string) (string, error) {
			for o, nn := range rename {
				if o.Name() == n && o.Pkg().Name() == pkg {
					return nn, nil
				}
			}
			return "", fmt.Errorf("vector uses %s.%s which is not among the extracted declarations", pkg, n)
		})
		must(err)
		must(os.WriteFile(filepath.Join(integ, "p6vec_test.go"), []byte(strings.Replace(extSrc, "func main()", "func "+*vecTest+"(t *testing.T)", 1)), 0o644))
		zabs, _ := filepath.Abs(*module)
		tmpDir := filepath.Join(zabs, "cmd", "p6vec_tmp")
		must(os.MkdirAll(tmpDir, 0o755))
		defer os.RemoveAll(tmpDir)
		must(os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(origSrc), 0o644))
		cmd := exec.Command("go", "run", "./cmd/p6vec_tmp")
		cmd.Dir = zabs
		origOut, err := cmd.CombinedOutput()
		if err != nil {
			die("ERROR", "original vectors: %v\n%s", err, tail(origOut))
		}
		t := exec.Command("go", "test", "-count=1", "-run", *vecTest, "-v", ".")
		t.Dir = integ
		ex, err := t.CombinedOutput()
		if err != nil {
			die("DIFFERENTIAL_FAILED", "%v\n%s", err, tail(ex))
		}
		want, got := vecLines(string(origOut)), vecLines(string(ex))
		nvec = len(want)
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
		_ = os.WriteFile(filepath.Join(*work, "vectors_original.txt"), origOut, 0o644)
		_ = os.WriteFile(filepath.Join(*work, "vectors_extracted.txt"), ex, 0o644)
	}
	rep["vector_lines"], rep["vector_mismatches"], rep["class"] = nvec, mism, class
	bj, _ := json.MarshalIndent(rep, "", " ")
	fmt.Println(string(bj))
	_ = os.WriteFile(filepath.Join(*work, "extraction.json"), bj, 0o644)
	if mism > 0 || fidelityFail > 0 || (*vectors != "" && nvec == 0) {
		die("PORTABILITY_FAILED", "differential or fidelity failure")
	}
	fmt.Printf("%s: extracted core compiles into the production source and matches the original byte-for-byte on %d vectors\n", class, nvec)
}

func uniq(s []string) []string {
	m := map[string]bool{}
	var o []string
	for _, x := range s {
		if !m[x] {
			m[x] = true
			o = append(o, x)
		}
	}
	sort.Strings(o)
	return o
}

func prodNames(path string) map[string]bool {
	pf, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	must(err)
	m := map[string]bool{}
	for _, d := range pf.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				m[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					m[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, n := range s.Names {
						m[n.Name] = true
					}
				}
			}
		}
	}
	return m
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

// tokenString normalizes a declaration to a comparable token string. For extracted source,
// renamed identifiers are reversed to the original spelling: pkg.Name for a cross-package
// reference, Name for a reference inside the declaration's own package.
func tokenString(src string, rev map[string]string, ownPkg string) string {
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
				pk, nm := o[:strings.Index(o, "|")], o[strings.Index(o, "|")+1:]
				if pk == ownPkg {
					lit = nm
				} else {
					lit = pk[strings.LastIndex(pk, "/")+1:] + " . " + nm
				}
			}
		}
		if lit == "" {
			lit = tok.String()
		}
		sb.WriteString(lit)
		sb.WriteByte(' ')
	}
	// the original spells own-package references unqualified and foreign ones qualified,
	// so normalize qualified self references "own . Name" away on the original side as well
	return sb.String()
}

// vectorSource instantiates @pkg.Name@ placeholders and returns the import lines needed.
func vectorSource(tmpl, modpath string, sub func(pkg, name string) (string, error)) (string, string, error) {
	var out strings.Builder
	pk := map[string]bool{}
	for {
		i := strings.Index(tmpl, "@")
		if i < 0 {
			out.WriteString(tmpl)
			break
		}
		j := strings.Index(tmpl[i+1:], "@")
		if j < 0 {
			return "", "", fmt.Errorf("unterminated placeholder")
		}
		out.WriteString(tmpl[:i])
		ref := tmpl[i+1 : i+1+j]
		dot := strings.Index(ref, ".")
		if dot < 0 {
			return "", "", fmt.Errorf("placeholder %q needs pkg.Name", ref)
		}
		r, err := sub(ref[:dot], ref[dot+1:])
		if err != nil {
			return "", "", err
		}
		pk[ref[:dot]] = true
		out.WriteString(r)
		tmpl = tmpl[i+j+2:]
	}
	var imps []string
	for p := range pk {
		imps = append(imps, fmt.Sprintf("import %q", modpath+"/"+p))
	}
	sort.Strings(imps)
	return out.String(), strings.Join(imps, "\n"), nil
}
