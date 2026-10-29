// Insert timing defers into a private source copy, never production decode.
package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
)

type edit struct {
	at   int
	text string
}

func main() {
	raw, e := os.ReadFile(os.Args[1])
	if e != nil {
		panic(e)
	}
	fs := token.NewFileSet()
	file, e := parser.ParseFile(fs, os.Args[1], raw, 0)
	if e != nil {
		panic(e)
	}
	tags := map[string]string{"repSSOpen": "parse", "repPQOpen": "parse", "repSSEncode": "components", "repSSDecodeBlock": "semantic_codec", "repSSanalyzeZstdBlock": "semantic_codec", "repSSrestartInterval": "semantic_records", "repSSWalkBlock": "semantic_records", "repSNEncodeRef": "semantic_codec", "repSNEncodeCPP": "semantic_codec", "repSNEncodeCPP15": "semantic_codec", "repPQAnalyzer.Chunk": "dynamic"}
	seen := map[string]int{}
	var edits []edit
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
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
		tag, ok := tags[name]
		if !ok {
			continue
		}
		seen[name]++
		expr := strconv.Quote(tag)
		if tag == "dynamic" {
			expr = "auditPQStage(plan==nil)"
		}
		edits = append(edits, edit{fs.Position(fn.Body.Lbrace).Offset + 1, "\ndefer auditEnter(" + expr + ")()\n"})
	}
	for name := range tags {
		if seen[name] != 1 {
			panic("missing/duplicate instrumented declaration: " + name)
		}
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].at > edits[j].at })
	for _, x := range edits {
		raw = append(raw[:x.at], append([]byte(x.text), raw[x.at:]...)...)
	}
	if e = os.WriteFile(os.Args[2], raw, 0644); e != nil {
		panic(e)
	}
	json.NewEncoder(os.Stdout).Encode(seen)
}
