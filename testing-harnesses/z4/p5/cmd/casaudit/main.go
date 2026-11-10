// Command casaudit inventories every production function that touches CDC
// chunk bytes or chunk presence directly, and (given the B2 draft source)
// whether the draft's version of that function is authority-aware. It is a
// source-level audit aid: classification is by mechanical rules stated in the
// output, not a proof of coverage.
//
// usage: casaudit [-draft zeros3_b2.go] zeros3.go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
)

var prims = map[string]string{
	"casRead": "read-chunk", "casReadExcluding": "read-chunk", "casReadH": "read-chunk", "casStat": "stat-chunk", "casStatLocked": "stat-chunk",
	"casReadAt": "read-chunk", "casReadChunkInto": "read-chunk", "chunkReadVerified": "read-chunk",
	"newServedReader": "read-object", "newManifestReaderAt": "read-object", "manifestBytes": "read-object",
	"openObjectReader": "read-object",
}

type row struct {
	Func       string   `json:"func"`
	Line       int      `json:"line"`
	Prims      []string `json:"calls"`
	Category   string   `json:"operation"`
	WalksChunk bool     `json:"iterates_manifest_chunks"`
	DraftAware string   `json:"b2_draft"`
}

func category(fn string) string {
	l := strings.ToLower(fn)
	for _, k := range []struct{ sub, cat string }{
		{"uploadpartcopy", "UploadPartCopy"}, {"copyobject", "CopyObject"}, {"copy", "copy"}, {"multipart", "multipart"}, {"upload", "multipart/upload"},
		{"bundle", "bundle"}, {"sync", "sync"}, {"replic", "replication"}, {"repair", "repair"}, {"snapshot", "snapshot"}, {"history", "history/versions"}, {"version", "history/versions"},
		{"repack", "repack"}, {"compact", "compact/repack"}, {"gc", "gc"}, {"reach", "gc/reachability"}, {"verify", "verify"}, {"stats", "stats"}, {"doctor", "verify"},
		{"rep", "representation"}, {"range", "range read"}, {"serve", "GET/HEAD/range"}, {"get", "GET"}, {"head", "HEAD"}, {"read", "read"}, {"stat", "stat"}, {"tier", "tier"}, {"migrat", "migrate"}, {"restore", "restore"},
		{"ingest", "PUT/ingest"}, {"put", "PUT"}, {"pack", "pack/storage"}, {"seg", "P1C segment"}, {"cas", "CAS primitive"},
	} {
		if strings.Contains(l, k.sub) {
			return k.cat
		}
	}
	return "other"
}

func collect(path string, withDraft map[string]string) (rows []row, bodies map[string]string) {
	fset := token.NewFileSet()
	src, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		panic(err)
	}
	bodies = map[string]string{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		name := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			t := fd.Recv.List[0].Type
			if st, ok := t.(*ast.StarExpr); ok {
				t = st.X
			}
			if id, ok := t.(*ast.Ident); ok {
				name = id.Name + "." + name
			}
		}
		bodies[name] = string(src[fset.Position(fd.Body.Pos()).Offset:fset.Position(fd.Body.End()).Offset])
		seen := map[string]bool{}
		walks := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				var id string
				switch fn := x.Fun.(type) {
				case *ast.SelectorExpr:
					id = fn.Sel.Name
				case *ast.Ident:
					id = fn.Name
				}
				if _, ok := prims[id]; ok && !strings.HasPrefix(name, "Store."+id) {
					seen[id] = true
				}
			case *ast.RangeStmt:
				if se, ok := x.X.(*ast.SelectorExpr); ok && se.Sel.Name == "Chunks" {
					walks = true
				}
			}
			return true
		})
		if len(seen) == 0 && !walks {
			continue
		}
		r := row{Func: name, Line: fset.Position(fd.Pos()).Line, Category: category(fd.Name.Name), WalksChunk: walks}
		for k := range seen {
			r.Prims = append(r.Prims, k)
		}
		sort.Strings(r.Prims)
		rows = append(rows, r)
	}
	return
}

func main() {
	draft := flag.String("draft", "", "B2 draft zeros3.go")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: casaudit [-draft draft.go] zeros3.go")
		os.Exit(2)
	}
	rows, _ := collect(flag.Arg(0), nil)
	var dbodies map[string]string
	if *draft != "" {
		_, dbodies = collect(*draft, nil)
	}
	awareMarks := []string{"authCtx", "authRead", "openObjectReader", "authLoad", "authBody", "authVirtual", "virtualChunk", "AuthLive", "authPresent", "actx.", "authStat"}
	for i := range rows {
		rows[i].DraftAware = "n/a"
		if dbodies != nil {
			b, ok := dbodies[rows[i].Func]
			switch {
			case !ok:
				rows[i].DraftAware = "function absent in draft (post-B2 code: P1-P4)"
			default:
				rows[i].DraftAware = "draft body unchanged by authority hooks"
				for _, m := range awareMarks {
					if strings.Contains(b, m) {
						rows[i].DraftAware = "draft body references authority (" + m + ")"
						break
					}
				}
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Line < rows[j].Line })
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", " ")
	_ = enc.Encode(rows)
}
