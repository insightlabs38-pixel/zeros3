package zstdz3

import (
	goparser "go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStdlibOnly keeps the substrate movable: no import outside the standard library
// (no laboratory, frontier, command or repz3 package, no third-party zstd).
func TestStdlibOnly(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := goparser.ParseFile(token.NewFileSet(), f, src, goparser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range af.Imports {
			p := strings.Trim(im.Path.Value, `"`)
			if strings.Contains(strings.Split(p, "/")[0], ".") || strings.HasPrefix(p, "zeros3-z3") {
				t.Errorf("%s imports %s", f, p)
			}
		}
	}
}
