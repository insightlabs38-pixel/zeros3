package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// The corpus is built by the Go toolchain from this module's own cmd/z3lab
// (the real 5-6 MiB binary of the earlier Z3-07 evidence). Source mutations
// are applied with `go build -overlay`, so the working tree is never touched.
// One file in package main carries every mutation (late in the link order);
// the "early" revision also adds a file to package errors, which links before
// nearly everything, so code added there moves every later address.

// Mut describes one source revision.
type Mut struct {
	Name   string
	Marker string // string constant in .rodata
	K      int    // numeric constant in fnA/fnB
	AddFn  bool   // add fnNew (about 56 bytes of machine code)
	BodyB  bool   // different body for fnB
	Swap   bool   // fnNew emitted before fnA (layout movement)
	Table  int    // size of a global data table (0 = none)
	Deps   bool   // import extra std packages
	Early  bool   // also add a function in package lab
}

var muts = []Mut{
	{Name: "r0", Marker: "r0", K: 7},
	{Name: "r1", Marker: "r1-const", K: 9},
	{Name: "r2", Marker: "r1-const", K: 9, AddFn: true},
	{Name: "r3", Marker: "r1-const", K: 9, AddFn: true, BodyB: true},
	{Name: "early", Marker: "r1-const", K: 9, AddFn: true, Early: true},
	{Name: "swap", Marker: "r1-const", K: 9, AddFn: true, Swap: true},
	{Name: "data", Marker: "r1-const", K: 9, AddFn: true, Table: 96},
	{Name: "deps", Marker: "r1-const", K: 9, AddFn: true, Deps: true},
}

// Flags is a build-flag set (a metadata experiment axis).
type Flags struct {
	Name string
	Args []string
}

var flagSets = []Flags{
	{"std", []string{"-trimpath"}},
	{"strip", []string{"-trimpath", "-ldflags=-s -w"}},
	{"notrim", nil},
	{"nobid", []string{"-trimpath", "-ldflags=-buildid="}},
	{"pie", []string{"-trimpath", "-buildmode=pie"}},
	{"piestrip", []string{"-trimpath", "-buildmode=pie", "-ldflags=-s -w"}},
}

// matrix lists the (flag set, revision) cells that are built.
func matrix(flag string) []string {
	switch flag {
	case "std":
		return []string{"r0", "r1", "r2", "r3", "early", "swap", "data", "deps"}
	case "strip":
		return []string{"r0", "r1", "r2", "r3", "early", "swap", "data", "deps"}
	case "notrim", "nobid":
		return []string{"r0", "r2", "r3"}
	case "pie", "piestrip":
		return []string{"r0", "r1", "r2", "early"}
	}
	return nil
}

func mainSrc(m Mut) string {
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n\t\"os\"\n\t\"strconv\"\n\t\"strings\"\n")
	if m.Deps {
		b.WriteString("\t\"encoding/csv\"\n\t\"text/tabwriter\"\n")
	}
	b.WriteString(")\n\n")
	fmt.Fprintf(&b, "var zzMarker = %q\n\nconst zzK = %d\n\n", m.Marker, m.K)
	fnA := "//go:noinline\nfunc fnA(n int) string {\n\treturn strings.Repeat(zzMarker, n%3+1) + strconv.Itoa(n*zzK)\n}\n\n"
	fnNew := "//go:noinline\nfunc fnNew(n int) int {\n\treturn n*5 + len(zzMarker)\n}\n\n"
	if m.Swap && m.AddFn {
		b.WriteString(fnNew)
	}
	b.WriteString(fnA)
	if m.AddFn && !m.Swap {
		b.WriteString(fnNew)
	}
	if m.BodyB {
		b.WriteString("//go:noinline\nfunc fnB(n int) int {\n\ts := 0\n\tfor i := 0; i < n; i++ {\n\t\ts += i*zzK ^ len(zzMarker)\n\t\tif s > 1<<20 {\n\t\t\ts -= zzK\n\t\t}\n\t}\n\treturn s\n}\n\n")
	} else {
		b.WriteString("//go:noinline\nfunc fnB(n int) int {\n\ts := 0\n\tfor i := 0; i < n; i++ {\n\t\ts += i*zzK ^ len(zzMarker)\n\t}\n\treturn s\n}\n\n")
	}
	if m.Table > 0 {
		b.WriteString("var zzTable = [...]uint32{")
		for i := 0; i < m.Table; i++ {
			fmt.Fprintf(&b, "%d,", uint32(i)*2654435761>>3)
		}
		b.WriteString("}\n\n")
	}
	b.WriteString("func init() {\n\tif len(os.Args) > 1<<20 {\n\t\tprintln(fnA(len(os.Args)), fnB(len(os.Args)))\n")
	if m.AddFn {
		b.WriteString("\t\tprintln(fnNew(len(os.Args)))\n")
	}
	if m.Table > 0 {
		b.WriteString("\t\tprintln(zzTable[len(os.Args)%len(zzTable)])\n")
	}
	if m.Deps {
		b.WriteString("\t\tw := tabwriter.NewWriter(os.Stdout, 0, 8, 1, ' ', 0)\n\t\tw.Flush()\n\t\tcsv.NewWriter(os.Stdout).Flush()\n")
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

// earlySrc adds a function to package errors, which links before nearly all
// other code, so every later address moves.
func earlySrc() string {
	return "package errors\n\nvar zzEarlyMarker = \"early\"\n\n//go:noinline\nfunc zzEarlyFn(n int) int {\n\treturn n*11 + len(zzEarlyMarker)\n}\n\nfunc init() {\n\tif zzEarlyFn(3) == 0 {\n\t\tprintln(\"unreachable\")\n\t}\n}\n"
}

// gorootCopy is a writable copy of GOROOT (the "early" revision overlays a
// standard-library package, which the toolchain forbids inside the module
// cache). Empty means the default toolchain and no "early" revision.
var gorootCopy string

func goTool() string {
	if gorootCopy != "" {
		return filepath.Join(gorootCopy, "bin", "go")
	}
	return "go"
}

// Artifact is one built binary.
type Artifact struct {
	GOOS, GOARCH, Flags, Rev string
	File                     string
	Size                     int64
	SHA256                   string
}

func modDir() string {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func buildOne(root, work, goos, goarch string, fl Flags, m Mut, out string) error {
	mainF := filepath.Join(root, "cmd", "z3lab", "zz_mut_main.go")
	earlyF := filepath.Join(gorootCopy, "src", "errors", "zz_mut_early.go")
	tmp := filepath.Join(work, "ov-"+m.Name)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	a, b := filepath.Join(tmp, "main.go"), filepath.Join(tmp, "early.go")
	if err := os.WriteFile(a, []byte(mainSrc(m)), 0o644); err != nil {
		return err
	}
	rep := map[string]string{mainF: a}
	if m.Early {
		if err := os.WriteFile(b, []byte(earlySrc()), 0o644); err != nil {
			return err
		}
		rep[earlyF] = b
	}
	ov, _ := json.Marshal(map[string]map[string]string{"Replace": rep})
	ovf := filepath.Join(tmp, "overlay.json")
	if err := os.WriteFile(ovf, ov, 0o644); err != nil {
		return err
	}
	args := append([]string{"build", "-overlay=" + ovf}, fl.Args...)
	args = append(args, "-o", out, "./cmd/z3lab")
	cmd := exec.Command(goTool(), args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	if gorootCopy != "" {
		cmd.Env = append(cmd.Env, "GOROOT="+gorootCopy, "GOTOOLCHAIN=local")
	}
	if o, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s/%s %s %s: %v\n%s", goos, goarch, fl.Name, m.Name, err, o)
	}
	return nil
}

// buildOther builds an unrelated program (cmd/z3tar) with the same flags: the
// control for the admission rule.
func buildOther(root, goos, goarch string, fl Flags, out string) error {
	args := append([]string{"build"}, fl.Args...)
	args = append(args, "-o", out, "./cmd/z3tar")
	cmd := exec.Command(goTool(), args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	if gorootCopy != "" {
		cmd.Env = append(cmd.Env, "GOROOT="+gorootCopy, "GOTOOLCHAIN=local")
	}
	if o, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("other %s: %v\n%s", fl.Name, err, o)
	}
	return nil
}

func runCorpus(args []string) {
	fs := flag.NewFlagSet("corpus", flag.ExitOnError)
	out := fs.String("out", "", "output directory (outside the repository)")
	goos := fs.String("goos", "linux", "linux|windows|darwin")
	goarch := fs.String("goarch", "amd64", "amd64|arm64")
	only := fs.String("flags", "", "comma list of flag sets (default: all)")
	fs.StringVar(&gorootCopy, "goroot", "", "writable copy of GOROOT (required for the early revision)")
	fs.Parse(args)
	if *out == "" {
		fatal(fmt.Errorf("-out required"))
	}
	root := modDir()
	work, _ := os.MkdirTemp("", "z3bin-ov")
	defer os.RemoveAll(work)
	want := map[string]bool{}
	for _, f := range strings.Split(*only, ",") {
		if f != "" {
			want[f] = true
		}
	}
	var arts []Artifact
	for _, fl := range flagSets {
		if len(want) > 0 && !want[fl.Name] {
			continue
		}
		if (fl.Name == "pie" || fl.Name == "piestrip") && *goos != "linux" {
			continue
		}
		if *goarch != "amd64" && fl.Name != "std" && fl.Name != "strip" {
			continue
		}
		dir := filepath.Join(*out, *goos+"-"+*goarch, fl.Name)
		os.MkdirAll(dir, 0o755)
		for _, name := range matrix(fl.Name) {
			if name == "early" && gorootCopy == "" {
				continue
			}
			var m Mut
			for _, x := range muts {
				if x.Name == name {
					m = x
				}
			}
			ext := ""
			if *goos == "windows" {
				ext = ".exe"
			}
			file := filepath.Join(dir, name+ext)
			if err := buildOne(root, work, *goos, *goarch, fl, m, file); err != nil {
				fatal(err)
			}
			data, _ := os.ReadFile(file)
			sum := sha256.Sum256(data)
			arts = append(arts, Artifact{*goos, *goarch, fl.Name, name, file, int64(len(data)), hex.EncodeToString(sum[:])})
			fmt.Fprintf(os.Stderr, "built %s/%s %-6s %-6s %d\n", *goos, *goarch, fl.Name, name, len(data))
		}
		if fl.Name == "std" || fl.Name == "strip" {
			file := filepath.Join(dir, "other"+ext(*goos))
			if err := buildOther(root, *goos, *goarch, fl, file); err != nil {
				fatal(err)
			}
			data, _ := os.ReadFile(file)
			sum := sha256.Sum256(data)
			arts = append(arts, Artifact{*goos, *goarch, fl.Name, "other", file, int64(len(data)), hex.EncodeToString(sum[:])})
		}
	}
	ver, _ := exec.Command("go", "version").Output()
	prov := map[string]any{"toolchain": strings.TrimSpace(string(ver)), "host": runtime.GOOS + "/" + runtime.GOARCH, "artifacts": arts}
	j, _ := json.MarshalIndent(prov, "", " ")
	os.WriteFile(filepath.Join(*out, *goos+"-"+*goarch+"-provenance.json"), j, 0o644)
}

// ---- optional C family (gcc, statically linked glibc) ----

func cSrc(variant string) string {
	var b strings.Builder
	b.WriteString("#include <stdio.h>\n#include <stdlib.h>\n#include <string.h>\n")
	if variant == "c1" {
		// one small function inserted before everything else in the object
		b.WriteString("__attribute__((noinline)) int added(int x) { return x * 5 + 3; }\n")
	}
	k := 7
	if variant == "c2" {
		k = 9
	}
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "__attribute__((noinline)) int f%d(int x) { char buf[32]; snprintf(buf, sizeof buf, \"%%d-%d\", x * %d); return (int)strlen(buf) + x %% %d; }\n", i, i, k+i, 3+i)
	}
	b.WriteString("static int cmp(const void *a, const void *b) { return *(const int *)a - *(const int *)b; }\n")
	b.WriteString("int main(int argc, char **argv) {\n\tint v[40]; int s = 0;\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "\tv[%d] = f%d(argc + %d);\n", i, i, i)
	}
	if variant == "c1" {
		b.WriteString("\ts += added(argc);\n")
	}
	b.WriteString("\tqsort(v, 40, sizeof v[0], cmp);\n\tfor (int i = 0; i < 40; i++) s += v[i];\n\tprintf(\"%d\\n\", s);\n\treturn s == 12345;\n}\n")
	return b.String()
}

func runCCorpus(args []string) {
	fs := flag.NewFlagSet("ccorpus", flag.ExitOnError)
	out := fs.String("out", "", "output directory")
	cc := fs.String("cc", "gcc", "C compiler")
	fs.Parse(args)
	if *out == "" {
		fatal(fmt.Errorf("-out required"))
	}
	ver, _ := exec.Command(*cc, "--version").Output()
	fmt.Fprintf(os.Stderr, "%s\n", strings.SplitN(string(ver), "\n", 2)[0])
	for _, fl := range []struct {
		name string
		args []string
	}{{"std", nil}, {"strip", []string{"-s"}}} {
		dir := filepath.Join(*out, "c-"+*cc, fl.name)
		os.MkdirAll(dir, 0o755)
		for _, v := range []string{"c0", "c1", "c2"} {
			src := filepath.Join(dir, v+".c")
			os.WriteFile(src, []byte(cSrc(v)), 0o644)
			a := append([]string{"-O2", "-static", "-fno-pie", "-no-pie", "-fno-asynchronous-unwind-tables", "-Wl,--build-id=none", "-o", filepath.Join(dir, v)}, fl.args...)
			if o, err := exec.Command(*cc, append(a, src)...).CombinedOutput(); err != nil {
				fatal(fmt.Errorf("%s: %v\n%s", v, err, o))
			}
			os.Remove(src)
		}
	}
}
