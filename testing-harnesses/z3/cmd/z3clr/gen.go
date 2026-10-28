package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() { cmds["gen"] = genCmd }

// genCfg describes one variant of the deterministic C# producer family (PRODUCER_WORKLOAD).
type genCfg struct {
	N         int    // classes
	Seed      int    // expression / constant seed
	NS        string // namespace
	PadStr    int    // padding fields with long unique names (#Strings volume)
	PadBlob   int    // padding members with long unique attribute strings (#Blob volume)
	EarlyType bool   // a new type that sorts first
	EarlyMeth bool   // a new method at the start of the first class
	BodyEdit  bool   // one method body changed
	ConstEdit bool   // one constant and one string changed
	AddMeth   bool   // a method added to a class in the middle
	AddField  bool   // a field added to a class in the middle
	AddType   bool   // a type appended
	Dep       bool   // new references to framework types
	Version   string // assembly version
	Swap      bool   // classes 10..19 compiled in a different order
	Attrs     int    // members carrying a custom attribute
	Resource  string // embedded resource payload
	Wholesale int    // body seed override (all bodies rewritten)
	Generic   bool   // nothing: reserved
}

func (c genCfg) ns() string {
	if c.NS == "" {
		return "Z3.Lib"
	}
	return c.NS
}

func (c genCfg) ver() string {
	if c.Version == "" {
		return "1.0.0.0"
	}
	return c.Version
}

func lcg(a, b int) int { return (a*1103515245 + b*12345 + 7) & 0x7fffffff }

func (c genCfg) class(i int) string {
	s := c.Seed
	if c.Wholesale != 0 {
		s = c.Wholesale
	}
	k1, k2, k3 := 3+lcg(i, s)%17, 11+lcg(i, s+1)%97, 1+lcg(i, s+2)%9
	n := c.N
	next := (i + 1) % n
	var b strings.Builder
	fmt.Fprintf(&b, "using System;\nusing System.Collections.Generic;\nnamespace %s {\n", c.ns())
	fmt.Fprintf(&b, "public class C%04d {\n", i)
	fmt.Fprintf(&b, "  private int f0; private string f1; private List<int> f2;\n")
	if c.AddField && i == n/2 {
		fmt.Fprintf(&b, "  private long fAdded;\n")
	}
	fmt.Fprintf(&b, "  public int P0 { get; set; }\n  public string P1 { get { return f1; } }\n")
	fmt.Fprintf(&b, "  public C%04d(int a) { f0 = a; f1 = \"s%d_%d\"; f2 = new List<int>(); }\n", i, i, s)
	if c.EarlyMeth && i == 0 {
		fmt.Fprintf(&b, "  public int Early(int x) { return x ^ 0x5a5a; }\n")
	}
	mul := k1
	if c.BodyEdit && i == n/4 {
		mul = k1 + 100
	}
	cst := k2
	str := fmt.Sprintf("k%d_1", i)
	if c.ConstEdit && i == n*2/5 {
		cst = k2 + 1000
		str = fmt.Sprintf("k%d_changed", i)
	}
	fmt.Fprintf(&b, "  public int M0(int x) { return x * %d + f0 + %d; }\n", mul, cst)
	fmt.Fprintf(&b, "  public string M1(string s) { return string.Concat(s, f1, \"%s\"); }\n", str)
	fmt.Fprintf(&b, "  public void M2(int n) { for (int i = 0; i < n; i++) f2.Add(i + f0 * %d); }\n", k3)
	if c.N > 1 {
		fmt.Fprintf(&b, "  public int M3() { var o = new C%04d(f0); return o.M0(f0) + o.P0; }\n", next)
	}
	fmt.Fprintf(&b, "  public static int S0(int a, int b) { return a > b ? a - b : b - a + %d; }\n", k3)
	fmt.Fprintf(&b, "  public int M4(Dictionary<string, int> d) { int r = 0; foreach (var kv in d) r += kv.Value + kv.Key.Length; return r; }\n")
	if c.AddMeth && i == n/3 {
		fmt.Fprintf(&b, "  public int Added(int q) { return q * %d - f0; }\n", k2)
	}
	if c.Dep && (i == 0 || i == n/2) {
		fmt.Fprintf(&b, "  public string Dep%d() { var sb = new System.Text.StringBuilder(); sb.Append(f0); return sb.ToString() + System.IO.Path.GetFileName(f1) + System.Linq.Enumerable.Count(f2); }\n", i)
	}
	if i < c.Attrs {
		fmt.Fprintf(&b, "  [Obsolete(\"obsolete member %d of %s\")] public int Old%d() { return f0; }\n", i, c.ns(), i)
	}
	fmt.Fprintf(&b, "}\n}\n")
	return b.String()
}

func (c genCfg) pad() string {
	var b strings.Builder
	fmt.Fprintf(&b, "using System;\nnamespace %s {\n", c.ns())
	fmt.Fprintf(&b, "[AttributeUsage(AttributeTargets.All)] public class Z3TagAttribute : Attribute { public string V; public Z3TagAttribute(string v) { V = v; } }\n")
	fmt.Fprintf(&b, "public class Pad {\n")
	for i := 0; i < c.PadStr; i++ {
		fmt.Fprintf(&b, "  public int Padding_Field_With_A_Deliberately_Long_Unique_Identifier_Number_%06d_XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX;\n", lcg(i, 77)%1000000)
	}
	for i := 0; i < c.PadBlob; i++ {
		fmt.Fprintf(&b, "  [Z3Tag(\"%s%05d\")] public int B%d;\n", strings.Repeat("blob-payload-", 14), lcg(i, 91)%100000, i)
	}
	fmt.Fprintf(&b, "}\n}\n")
	return b.String()
}

const csproj = `<Project Sdk="Microsoft.NET.Sdk">
  <PropertyGroup>
    <TargetFramework>net8.0</TargetFramework>
    <AssemblyName>Z3.Lib</AssemblyName>
    <RootNamespace>Z3.Lib</RootNamespace>
    <Deterministic>true</Deterministic>
    <ContinuousIntegrationBuild>true</ContinuousIntegrationBuild>
    <DebugType>none</DebugType>
    <DebugSymbols>false</DebugSymbols>
    <Optimize>true</Optimize>
    <Configuration>Release</Configuration>
    <PlatformTarget>AnyCPU</PlatformTarget>
    <GenerateAssemblyInfo>false</GenerateAssemblyInfo>
    <GenerateTargetFrameworkAttribute>true</GenerateTargetFrameworkAttribute>
    <ImplicitUsings>disable</ImplicitUsings>
    <Nullable>disable</Nullable>
    <NoWarn>CS0169;CS0414;CS0649;CS0618</NoWarn>
    <EnableDefaultCompileItems>false</EnableDefaultCompileItems>
  </PropertyGroup>
  <ItemGroup>
    <Compile Include="src/*.cs" />
    <EmbeddedResource Include="res/data.txt" LogicalName="Z3.data.txt" />
  </ItemGroup>
</Project>
`

// build writes the sources of c into proj and builds them; the DLL is returned.
func (c genCfg) build(proj string) ([]byte, error) {
	src := filepath.Join(proj, "src")
	os.RemoveAll(src)
	if err := os.MkdirAll(src, 0o755); err != nil {
		return nil, err
	}
	os.MkdirAll(filepath.Join(proj, "res"), 0o755)
	wr := func(name, body string) {
		os.WriteFile(filepath.Join(src, name), []byte(body), 0o644)
	}
	order := make([]int, c.N)
	for i := range order {
		order[i] = i
	}
	if c.Swap {
		for i := 10; i < 20 && i < c.N; i += 2 {
			if i+1 < c.N {
				order[i], order[i+1] = order[i+1], order[i]
			}
		}
	}
	for pos, i := range order {
		wr(fmt.Sprintf("C%04d.cs", pos), c.class(i))
	}
	if c.AddType {
		wr("C9999.cs", fmt.Sprintf("using System;\nnamespace %s {\npublic class Appended { public int V(int a) { return a * 31 + %d; } public string S() { return \"appended\"; } }\n}\n", c.ns(), c.Seed))
	}
	if c.EarlyType {
		wr("A0000.cs", fmt.Sprintf("using System;\nnamespace %s {\npublic class EarlyBird { public int V(int a) { return a * 17 + %d; } public string S() { return \"early\"; } }\n}\n", c.ns(), c.Seed))
	}
	if c.PadStr > 0 || c.PadBlob > 0 {
		wr("P0000.cs", c.pad())
	}
	wr("Z_Assembly.cs", fmt.Sprintf("using System.Reflection;\n[assembly: AssemblyVersion(\"%s\")]\n[assembly: AssemblyFileVersion(\"%s\")]\n[assembly: AssemblyTitle(\"Z3 producer family\")]\n", c.ver(), c.ver()))
	res := c.Resource
	if res == "" {
		res = "resource payload v1"
	}
	os.WriteFile(filepath.Join(proj, "res", "data.txt"), []byte(res), 0o644)
	os.WriteFile(filepath.Join(proj, "Z3.Lib.csproj"), []byte(csproj), 0o644)
	cmd := exec.Command("dotnet", "build", "-c", "Release", "--nologo", "-v", "q", "-p:UseSharedCompilation=false")
	cmd.Dir = proj
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("dotnet build: %v\n%s", err, out)
	}
	return os.ReadFile(filepath.Join(proj, "bin", "Release", "net8.0", "Z3.Lib.dll"))
}

type variant struct {
	Name string
	Cfg  genCfg
}

// family is the controlled set; "r*" is a cumulative chain, the rest are one-mutation variants of r0.
func family(n int) []variant {
	b := genCfg{N: n, Seed: 1}
	with := func(f func(*genCfg)) genCfg { c := b; f(&c); return c }
	v := []variant{
		{"r0", b},
		{"body", with(func(c *genCfg) { c.BodyEdit = true })},
		{"const", with(func(c *genCfg) { c.ConstEdit = true })},
		{"addmeth", with(func(c *genCfg) { c.AddMeth = true })},
		{"addfield", with(func(c *genCfg) { c.AddField = true })},
		{"addtype", with(func(c *genCfg) { c.AddType = true })},
		{"earlytype", with(func(c *genCfg) { c.EarlyType = true })},
		{"earlymeth", with(func(c *genCfg) { c.EarlyMeth = true })},
		{"dep", with(func(c *genCfg) { c.Dep = true })},
		{"version", with(func(c *genCfg) { c.Version = "1.0.1.0" })},
		{"reorder", with(func(c *genCfg) { c.Swap = true })},
		{"attrs", with(func(c *genCfg) { c.Attrs = n / 2 })},
		{"resource", with(func(c *genCfg) { c.Resource = "resource payload v2 with more text to make it larger than before" })},
		{"wholesale", with(func(c *genCfg) { c.Wholesale = 7 })},
		{"unrelated", genCfg{N: n, Seed: 5, NS: "Other.Lib", Wholesale: 5}},
	}
	// chain
	ch := b
	steps := []func(*genCfg){
		func(c *genCfg) { c.BodyEdit = true },
		func(c *genCfg) { c.ConstEdit = true },
		func(c *genCfg) { c.AddMeth = true },
		func(c *genCfg) { c.AddType = true },
		func(c *genCfg) { c.Attrs = n / 2 },
		func(c *genCfg) { c.Dep = true },
		func(c *genCfg) { c.EarlyType = true },
	}
	v = append(v, variant{"c0", ch})
	for i, s := range steps {
		s(&ch)
		v = append(v, variant{fmt.Sprintf("c%d", i+1), ch})
	}
	return v
}

// thresholds are the width-transition controls (Z3-33 headline stress): one added member either
// stays on the same side of a 16-bit index threshold or crosses it.
func thresholds() []variant {
	var v []variant
	// #Strings: padding fields of ~103 bytes each; "below" sits ~1 KB under 64 KiB, "small" adds one
	// field on the same side, "above" crosses
	str := func(pad int) genCfg { return genCfg{N: 150, Seed: 1, PadStr: pad} }
	v = append(v, variant{"str-below", str(612)}, variant{"str-small", str(613)}, variant{"str-above", str(640)})
	// #Blob: members with a unique 200-byte attribute argument (~195 bytes of blob each)
	blob := func(pad int) genCfg { return genCfg{N: 150, Seed: 1, PadBlob: pad} }
	v = append(v, variant{"blob-below", blob(325)}, variant{"blob-small", blob(326)}, variant{"blob-above", blob(345)})
	// HasCustomAttribute is 4 bytes from 2048 rows in any of its tables; each class has 10 methods
	v = append(v, variant{"coded-below", genCfg{N: 204, Seed: 1}},
		variant{"coded-small", genCfg{N: 204, Seed: 1, AddMeth: true}},
		variant{"coded-above", genCfg{N: 210, Seed: 1}})
	return v
}

// gen OUTDIR [family|thresholds|all] [NAME...] [-n N] : build variants with the dotnet SDK.
func genCmd(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: gen OUTDIR family|thresholds [NAME...]")
	}
	out, which := args[0], args[1]
	want := map[string]bool{}
	n := 150
	for _, a := range args[2:] {
		if v, ok := strings.CutPrefix(a, "n="); ok {
			fmt.Sscan(v, &n)
			continue
		}
		want[a] = true
	}
	var vs []variant
	if which == "family" || which == "all" {
		vs = append(vs, family(n)...)
	}
	if which == "thresholds" || which == "all" {
		vs = append(vs, thresholds()...)
	}
	proj, err := os.MkdirTemp("", "z3clrproj")
	if err != nil {
		return err
	}
	defer os.RemoveAll(proj)
	os.MkdirAll(out, 0o755)
	for _, x := range vs {
		if len(want) > 0 && !want[x.Name] {
			continue
		}
		dll, err := x.Cfg.build(proj)
		if err != nil {
			return fmt.Errorf("%s: %w", x.Name, err)
		}
		if err := os.WriteFile(filepath.Join(out, x.Name+".dll"), dll, 0o644); err != nil {
			return err
		}
		fmt.Printf("%-12s %8d bytes\n", x.Name, len(dll))
	}
	return nil
}
