package main

// Controlled managed-bytecode producer workload (PRODUCER_WORKLOAD): a deterministic generated
// Java library compiled with javac and converted with D8. Mutations isolate one source change
// each; nothing here is needed at decode time.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func init() { cmds["workload"] = workloadCmd }

const (
	wlClasses = 800
	wlMethods = 10
	wlPkgs    = 20
)

type wlMethod struct {
	seed  int64
	tweak int  // alters one integer and one string constant
	early bool // adds a string constant that sorts before everything
}

type wlClass struct {
	pkg, name string
	methods   []wlMethod // m0..; statics s0..s2 are fixed
	extraF    int
	rev       bool // declare members in reverse order
}

type wlModel struct {
	classes map[string]*wlClass
	order   []string
	deps    int
}

func (m *wlModel) key(c *wlClass) string { return c.pkg + "." + c.name }

func baseModel() *wlModel {
	m := &wlModel{classes: map[string]*wlClass{}}
	for i := 0; i < wlClasses; i++ {
		c := &wlClass{pkg: fmt.Sprintf("app.p%02d", i%wlPkgs), name: fmt.Sprintf("C%04d", i)}
		for j := 0; j < wlMethods; j++ {
			c.methods = append(c.methods, wlMethod{seed: int64(i*131 + j*17 + 1)})
		}
		m.classes[m.key(c)] = c
		m.order = append(m.order, m.key(c))
	}
	return m
}

func (m *wlModel) clone() *wlModel {
	n := &wlModel{classes: map[string]*wlClass{}, order: append([]string(nil), m.order...), deps: m.deps}
	for k, c := range m.classes {
		cc := *c
		cc.methods = append([]wlMethod(nil), c.methods...)
		n.classes[k] = &cc
	}
	return n
}

// mutations by name
func (m *wlModel) apply(name string) {
	c := func(i int) *wlClass { return m.classes[fmt.Sprintf("app.p%02d.C%04d", i%wlPkgs, i)] }
	switch name {
	case "body":
		c(400).methods[2].seed += 100003
	case "const":
		c(200).methods[3].tweak++
	case "addmethod":
		c(300).methods = append(c(300).methods, wlMethod{seed: 777001})
	case "rmmethod":
		cc := c(300)
		cc.methods = append(cc.methods[:1], cc.methods[2:]...)
	case "addfield":
		c(100).extraF++
	case "addclass":
		n := &wlClass{pkg: "app.p03", name: "C0801"}
		for j := 0; j < wlMethods; j++ {
			n.methods = append(n.methods, wlMethod{seed: int64(900000 + j)})
		}
		m.classes[m.key(n)] = n
		m.order = append(m.order, m.key(n))
	case "earlystr":
		c(50).methods[1].early = true
	case "earlyname":
		n := &wlClass{pkg: "a", name: "A0"}
		for j := 0; j < 3; j++ {
			n.methods = append(n.methods, wlMethod{seed: int64(910000 + j)})
		}
		m.classes[m.key(n)] = n
		m.order = append(m.order, m.key(n))
	case "reorder":
		c(60).rev = true
	case "dep":
		m.deps = 60
	case "unrelated":
		for i := range m.order { // wholesale: every body changes
			cc := m.classes[m.order[i]]
			for j := range cc.methods {
				cc.methods[j].seed += 55555
			}
		}
	}
}

func (c *wlClass) src(m *wlModel) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "package %s;\n\nimport java.util.*;\n\npublic class %s {\n", c.pkg, c.name)
	fld := []string{"    private int f0;\n", fmt.Sprintf("    private String f1 = \"%s.%s.f1\";\n", c.pkg, c.name), "    private final List<String> f2 = new ArrayList<>();\n"}
	for i := 0; i < c.extraF; i++ {
		fld = append(fld, fmt.Sprintf("    private long x%d = %d;\n", i, 1000+i))
	}
	mem := []string{fmt.Sprintf("    public %s(int x) { f0 = x; }\n", c.name)}
	for j := 0; j < 3; j++ { // static helpers called from other classes
		mem = append(mem, fmt.Sprintf("    public static int s%d(int a) { return a * %d + %d; }\n", j, 3+j, 7*j+1))
	}
	for j, me := range c.methods {
		mem = append(mem, me.src(m, c, j))
	}
	if c.rev {
		for i, j := 0, len(mem)-1; i < j; i, j = i+1, j-1 {
			mem[i], mem[j] = mem[j], mem[i]
		}
	}
	for _, f := range fld {
		sb.WriteString(f)
	}
	for _, s := range mem {
		sb.WriteString(s)
	}
	sb.WriteString("}\n")
	return sb.String()
}

func (me wlMethod) src(m *wlModel, c *wlClass, j int) string {
	r := rand.New(rand.NewSource(me.seed))
	var sb strings.Builder
	name := fmt.Sprintf("m%d", j)
	if me.seed == 777001 {
		name = "mExtra"
	}
	fmt.Fprintf(&sb, "    public int %s(int a) {\n        int r = a + %d;\n", name, 3+me.tweak)
	n := 3 + r.Intn(5)
	for k := 0; k < n; k++ {
		switch r.Intn(7) {
		case 0:
			fmt.Fprintf(&sb, "        for (int i = 0; i < a %% %d + 2; i++) { r += i * %d ^ f0; }\n", 3+r.Intn(5), 11+r.Intn(50))
		case 1:
			t := m.classes[m.order[r.Intn(wlClasses)]]
			fmt.Fprintf(&sb, "        r += %s.%s.s%d(r);\n", t.pkg, t.name, r.Intn(3))
		case 2:
			fmt.Fprintf(&sb, "        f1 = \"%s.%s.%s.%d\" + r;\n", c.pkg, c.name, name, k)
		case 3:
			fmt.Fprintf(&sb, "        f2.add(Integer.toString(r + %d));\n", r.Intn(1000))
		case 4:
			fmt.Fprintf(&sb, "        r = Math.max(r, %d) - f2.size();\n", r.Intn(100))
		case 5:
			fmt.Fprintf(&sb, "        if (r %% %d == 0) { f0 += %d; } else { f0 -= 1; }\n", 2+r.Intn(7), 1+r.Intn(9))
		case 6:
			fmt.Fprintf(&sb, "        { Map<String,Integer> mp = new HashMap<>(); mp.put(f1, r); r += mp.size(); }\n")
		}
	}
	if me.tweak != 0 {
		fmt.Fprintf(&sb, "        f1 = \"tweak%d\";\n", me.tweak)
	}
	if me.early {
		sb.WriteString("        f1 = \"!!early\";\n")
	}
	sb.WriteString("        return r;\n    }\n")
	return sb.String()
}

func depSrc(i int) string {
	return fmt.Sprintf("package lib.dep;\n\nimport java.util.*;\n\npublic class D%03d {\n    public static String d%d(int a) { StringBuilder sb = new StringBuilder(\"dep%d\"); for (int i = 0; i < a %% 7; i++) { sb.append(i * %d); } return sb.toString(); }\n    public static List<Integer> l(int n) { List<Integer> r = new ArrayList<>(); for (int i = 0; i < n; i++) r.add(i + %d); return r; }\n}\n", i, i, i, i+3, i)
}

type tool struct{ Name, Version string }

type wlProv struct {
	Schema    string            `json:"schema"`
	Label     string            `json:"evidence_class"`
	Tools     map[string]string `json:"tools"`
	Options   map[string]string `json:"options"`
	Revisions []wlRev           `json:"revisions"`
}

type wlRev struct {
	Name, Mutation, Parent string
	DexSHA, JarSHA         string
	DexSize, JarSize       int64
	Classes                int
}

func sh(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func clean(s string) string {
	var o []string
	for _, l := range strings.Split(s, "\n") {
		if !strings.HasPrefix(l, "Picked up") && strings.TrimSpace(l) != "" {
			o = append(o, l)
		}
	}
	return strings.Join(o, "\n")
}

func workloadCmd(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: workload OUTDIR [R8JAR]")
	}
	out, _ := filepath.Abs(args[0])
	r8 := os.Getenv("Z3_R8")
	if len(args) > 1 {
		r8 = args[1]
	}
	if r8 == "" {
		return fmt.Errorf("set Z3_R8 or pass the r8 jar")
	}
	prov := wlProv{Schema: "z3mb-workload/1", Label: "PRODUCER_WORKLOAD", Tools: map[string]string{}, Options: map[string]string{}}
	jv, _ := sh("", nil, "javac", "-version")
	jr, _ := sh("", nil, "java", "-version")
	d8v, _ := sh("", nil, "java", "-cp", r8, "com.android.tools.r8.D8", "--version")
	prov.Tools["javac"] = clean(jv)
	prov.Tools["java"] = strings.ReplaceAll(clean(jr), "\n", " | ")
	prov.Tools["d8"] = clean(d8v)
	rh, _ := os.ReadFile(r8)
	hs := sha256.Sum256(rh)
	prov.Tools["r8_jar_sha256"] = hex.EncodeToString(hs[:])
	prov.Options["javac"] = "--release 8 (default -g: source,lines) -Xlint:-options"
	prov.Options["d8"] = "--release --min-api 24 (no --lib, no desugaring needed)"
	prov.Options["jar"] = "jar --create --date=2021-01-01T00:00:00Z (JDK zip DEFLATE)"
	base := baseModel()
	type rev struct {
		name, mut, parent string
		muts              []string
	}
	chain := []string{"body", "const", "addmethod", "addfield", "addclass", "dep", "earlyname"}
	revs := []rev{{"base", "", "", nil}}
	for _, m := range []string{"body", "const", "addmethod", "rmmethod", "addfield", "addclass", "earlystr", "earlyname", "reorder", "dep", "unrelated"} {
		revs = append(revs, rev{"x-" + m, m, "base", []string{m}})
	}
	for i := range chain {
		par := "base"
		if i > 0 {
			par = fmt.Sprintf("c%d", i)
		}
		revs = append(revs, rev{fmt.Sprintf("c%d", i+1), chain[i], par, chain[:i+1]})
	}
	for _, rv := range revs {
		m := base.clone()
		for _, mu := range rv.muts {
			m.apply(mu)
		}
		dir := filepath.Join(out, rv.name)
		os.RemoveAll(dir)
		src, cls := filepath.Join(dir, "src"), filepath.Join(dir, "classes")
		os.MkdirAll(src, 0o755)
		os.MkdirAll(cls, 0o755)
		var files []string
		for _, k := range m.order {
			c := m.classes[k]
			p := filepath.Join(src, strings.ReplaceAll(c.pkg, ".", "/"), c.name+".java")
			os.MkdirAll(filepath.Dir(p), 0o755)
			if err := os.WriteFile(p, []byte(c.src(m)), 0o644); err != nil {
				return err
			}
			files = append(files, p)
		}
		for i := 0; i < m.deps; i++ {
			p := filepath.Join(src, "lib/dep", fmt.Sprintf("D%03d.java", i))
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(depSrc(i)), 0o644)
			files = append(files, p)
		}
		sort.Strings(files)
		jargs := append([]string{"--release", "8", "-Xlint:-options", "-d", cls}, files...)
		if o, err := sh("", nil, "javac", jargs...); err != nil {
			return fmt.Errorf("javac %s: %v\n%s", rv.name, err, o)
		}
		dexDir := filepath.Join(dir, "dex")
		os.MkdirAll(dexDir, 0o755)
		var cf []string
		filepath.Walk(cls, func(p string, fi os.FileInfo, err error) error {
			if err == nil && strings.HasSuffix(p, ".class") {
				cf = append(cf, p)
			}
			return nil
		})
		sort.Strings(cf)
		d8a := append([]string{"-cp", r8, "com.android.tools.r8.D8", "--release", "--min-api", "24", "--output", dexDir}, cf...)
		if o, err := sh("", nil, "java", d8a...); err != nil {
			return fmt.Errorf("d8 %s: %v\n%s", rv.name, err, o)
		}
		jar := filepath.Join(dir, "app.jar")
		if o, err := sh(cls, nil, "jar", "--create", "--file", jar, "--date=2021-01-01T00:00:00Z", "-C", cls, "."); err != nil {
			return fmt.Errorf("jar %s: %v\n%s", rv.name, err, o)
		}
		db, _ := os.ReadFile(filepath.Join(dexDir, "classes.dex"))
		jb, _ := os.ReadFile(jar)
		ds, js := sha256.Sum256(db), sha256.Sum256(jb)
		prov.Revisions = append(prov.Revisions, wlRev{rv.name, rv.mut, rv.parent, hex.EncodeToString(ds[:]), hex.EncodeToString(js[:]), int64(len(db)), int64(len(jb)), len(cf)})
		fmt.Printf("%-12s classes %4d dex %8d jar %8d\n", rv.name, len(cf), len(db), len(jb))
	}
	b, _ := json.MarshalIndent(prov, "", " ")
	return os.WriteFile(filepath.Join(out, "provenance.json"), b, 0o644)
}
