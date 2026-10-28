package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() { cmds["gen"] = genCmd }

// wcfg describes one variant of the deterministic C (clang + wasm-ld) producer family
// (PRODUCER_WORKLOAD); the Rust family below mirrors the structural mutations.
type wcfg struct {
	N         int  // functions
	Seed      int  // body seed
	Names     bool // keep the name section
	BodyEdit  bool // one function body changed
	ConstEdit bool // one constant and one data byte changed
	AddFunc   bool // a function appended
	EarlyFunc bool // a function inserted first
	EarlyImp  bool // an imported function used first (declared before everything)
	AddType   bool // a function with a new signature appended
	AddExport bool // one more export
	AddData   bool // a new static data array
	DataEdit  bool // all data bytes rewritten
	Swap      bool // function definitions 10..29 pairwise swapped
	Custom    string
	Wholesale int
	NS        string
}

func lcg(a, b int) int { return (a*1103515245 + b*12345 + 7) & 0x7fffffff }

func (c wcfg) src() string {
	n := c.N
	seed := c.Seed
	if c.Wholesale != 0 {
		seed = c.Wholesale
	}
	var b strings.Builder
	imp := func(i int) {
		fmt.Fprintf(&b, "__attribute__((import_module(\"env\"), import_name(\"host%d\"))) extern int host%d(int);\n", i, i)
	}
	if c.EarlyImp {
		imp(99)
	}
	for i := 0; i < 24; i++ {
		imp(i)
	}
	fmt.Fprintf(&b, "typedef int (*fn)(int);\n")
	dseed := 1
	if c.DataEdit {
		dseed = 9
	}
	for t := 0; t < 8; t++ {
		fmt.Fprintf(&b, "static const unsigned char tab%d[256] = {", t)
		for i := 0; i < 256; i++ {
			v := lcg(i+t*256, dseed) & 255
			if c.ConstEdit && t == 3 && i == 17 {
				v ^= 0x55
			}
			fmt.Fprintf(&b, "%d,", v)
		}
		fmt.Fprintf(&b, "};\n")
	}
	if c.AddData {
		fmt.Fprintf(&b, "static const unsigned char tabAdded[512] = {")
		for i := 0; i < 512; i++ {
			fmt.Fprintf(&b, "%d,", lcg(i, 4242)&255)
		}
		fmt.Fprintf(&b, "};\n")
	}
	fmt.Fprintf(&b, "int g_state[64];\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "int f%d(int);\n", i)
	}
	body := func(i int) string {
		k1, k2, k3 := 3+lcg(i, seed)%19, 11+lcg(i, seed+1)%97, 1+lcg(i, seed+2)%7
		if c.BodyEdit && i == n/4 {
			k1 += 100
		}
		if c.ConstEdit && i == n/2 {
			k2 += 1000
		}
		next, other := (i+1)%n, (i*7+3)%n
		var s strings.Builder
		fmt.Fprintf(&s, "static int g%d(int x) { int a = x * %d + %d; for (int i = 0; i < (x & 7); i++) a += tab%d[(a + i) & 255]; return a ^ %d; }\n", i, k1, k2, i%8, k3)
		if i%7 == 0 {
			fmt.Fprintf(&s, "__attribute__((export_name(\"e%d\")))\n", i)
		}
		if c.AddExport && i == 5 {
			fmt.Fprintf(&s, "__attribute__((export_name(\"extra_export\")))\n")
		}
		fmt.Fprintf(&s, "int f%d(int x) {\n  int r = g%d(x) + host%d(x + %d);\n", i, i, i%24, k3)
		if i == 0 && c.EarlyImp {
			fmt.Fprintf(&s, "  r += host99(x);\n")
		}
		fmt.Fprintf(&s, "  if (x > %d) r += f%d(x - %d);\n  else r -= f%d(x + %d);\n", k2, next, k3, other, k1)
		fmt.Fprintf(&s, "  g_state[%d] += r;\n  return r;\n}\n", i%64)
		return s.String()
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	if c.Swap {
		for i := 10; i < 30 && i+1 < n; i += 2 {
			order[i], order[i+1] = order[i+1], order[i]
		}
	}
	if c.EarlyFunc {
		fmt.Fprintf(&b, "__attribute__((export_name(\"early_func\"))) int early_func(int x) { return x * 31 + %d; }\n", seed)
	}
	for _, i := range order {
		b.WriteString(body(i))
	}
	fmt.Fprintf(&b, "static const fn table[] = {")
	for i := 0; i < n; i += 13 {
		fmt.Fprintf(&b, "f%d,", i)
	}
	fmt.Fprintf(&b, "};\n__attribute__((export_name(\"dispatch\"))) int dispatch(int i, int x) { return table[i %% %d](x); }\n", (n+12)/13)
	if c.AddFunc {
		fmt.Fprintf(&b, "__attribute__((export_name(\"added\"))) int added(int x) { return x * 13 - %d; }\n", seed)
	}
	if c.AddType {
		fmt.Fprintf(&b, "__attribute__((export_name(\"newtype\"))) float newtype(long long a, double d, int k) { return (float)(a * k) + (float)d; }\n")
	}
	if c.AddData {
		fmt.Fprintf(&b, "__attribute__((export_name(\"usedata\"))) int usedata(int i) { return tabAdded[i & 511]; }\n")
	}
	return b.String()
}

func (c wcfg) build(dir string) ([]byte, error) {
	src := filepath.Join(dir, "m.c")
	if err := os.WriteFile(src, []byte(c.src()), 0o644); err != nil {
		return nil, err
	}
	out := filepath.Join(dir, "m.wasm")
	args := []string{"--target=wasm32", "-O2", "-ffreestanding", "-nostdlib", "-fno-builtin", "-Wl,--no-entry", "-Wl,--export-dynamic", "-Wl,--allow-undefined"}
	if !c.Names {
		args = append(args, "-Wl,--strip-all")
	}
	args = append(args, src, "-o", out)
	if o, err := exec.Command("clang", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("clang: %v\n%s", err, o)
	}
	d, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	if c.Custom != "" {
		d = appendCustom(d, "z3meta", []byte(c.Custom))
	}
	return d, nil
}

// appendCustom appends a custom section (id 0, name, payload): a post-link tooling step.
func appendCustom(d []byte, name string, payload []byte) []byte {
	body := binary.AppendUvarint(nil, uint64(len(name)))
	body = append(body, name...)
	body = append(body, payload...)
	d = append(d, 0)
	d = binary.AppendUvarint(d, uint64(len(body)))
	return append(d, body...)
}

type wvariant struct {
	Name string
	Cfg  wcfg
}

func wfamily(n int) []wvariant {
	b := wcfg{N: n, Seed: 1}
	with := func(f func(*wcfg)) wcfg { c := b; f(&c); return c }
	v := []wvariant{
		{"r0", b},
		{"r0-names", with(func(c *wcfg) { c.Names = true })},
		{"body", with(func(c *wcfg) { c.BodyEdit = true })},
		{"const", with(func(c *wcfg) { c.ConstEdit = true })},
		{"addfunc", with(func(c *wcfg) { c.AddFunc = true })},
		{"earlyfunc", with(func(c *wcfg) { c.EarlyFunc = true })},
		{"earlyimport", with(func(c *wcfg) { c.EarlyImp = true })},
		{"addtype", with(func(c *wcfg) { c.AddType = true })},
		{"addexport", with(func(c *wcfg) { c.AddExport = true })},
		{"adddata", with(func(c *wcfg) { c.AddData = true })},
		{"dataedit", with(func(c *wcfg) { c.DataEdit = true })},
		{"custom", with(func(c *wcfg) { c.Custom = "release 2 build metadata" })},
		{"reorder", with(func(c *wcfg) { c.Swap = true })},
		{"wholesale", with(func(c *wcfg) { c.Wholesale = 7 })},
		{"unrelated", wcfg{N: n, Seed: 5, Wholesale: 5}},
		{"earlyimport-names", with(func(c *wcfg) { c.EarlyImp = true; c.Names = true })},
	}
	ch := b
	v = append(v, wvariant{"c0", ch})
	for i, s := range []func(*wcfg){
		func(c *wcfg) { c.BodyEdit = true },
		func(c *wcfg) { c.ConstEdit = true },
		func(c *wcfg) { c.AddFunc = true },
		func(c *wcfg) { c.AddExport = true },
		func(c *wcfg) { c.AddData = true },
		func(c *wcfg) { c.EarlyImp = true },
		func(c *wcfg) { c.EarlyFunc = true },
	} {
		s(&ch)
		v = append(v, wvariant{fmt.Sprintf("c%d", i+1), ch})
	}
	return v
}

// rust family: the same mutations on a second, independent toolchain (rustc cdylib).
type rcfg struct {
	Feat      bool // bulk-memory, simd128, reference-types, multivalue, sign-ext, atomics-free feature set at opt-level 3
	N         int
	Seed      int
	BodyEdit  bool
	AddFunc   bool
	EarlyFunc bool
	EarlyImp  bool
}

func (c rcfg) src() string {
	var b strings.Builder
	fmt.Fprintf(&b, "#![no_std]\nuse core::panic::PanicInfo;\n#[panic_handler] fn panic(_: &PanicInfo) -> ! { loop {} }\n")
	fmt.Fprintf(&b, "extern \"C\" {\n")
	if c.EarlyImp {
		fmt.Fprintf(&b, "  fn host99(x: i32) -> i32;\n")
	}
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&b, "  fn host%d(x: i32) -> i32;\n", i)
	}
	fmt.Fprintf(&b, "}\nstatic mut STATE: [i32; 64] = [0; 64];\n")
	for t := 0; t < 4; t++ {
		fmt.Fprintf(&b, "static TAB%d: [u8; 256] = [", t)
		for i := 0; i < 256; i++ {
			fmt.Fprintf(&b, "%d,", lcg(i+t*256, 1)&255)
		}
		fmt.Fprintf(&b, "];\n")
	}
	if c.EarlyFunc {
		fmt.Fprintf(&b, "#[no_mangle] pub extern \"C\" fn early_func(x: i32) -> i32 { x.wrapping_mul(31).wrapping_add(%d) }\n", c.Seed)
	}
	n := c.N
	for i := 0; i < n; i++ {
		k1, k2, k3 := 3+lcg(i, c.Seed)%19, 11+lcg(i, c.Seed+1)%97, 1+lcg(i, c.Seed+2)%7
		if c.BodyEdit && i == n/4 {
			k1 += 100
		}
		next, other := (i+1)%n, (i*7+3)%n
		fmt.Fprintf(&b, "#[no_mangle] pub extern \"C\" fn f%d(x: i32) -> i32 {\n  let mut a = x.wrapping_mul(%d).wrapping_add(%d);\n  for i in 0..(x & 7) { a = a.wrapping_add(TAB%d[((a.wrapping_add(i)) & 255) as usize] as i32); }\n", i, k1, k2, i%4)
		fmt.Fprintf(&b, "  let mut r = unsafe { host%d(x + %d) } + (a ^ %d);\n", i%24, k3, k3)
		if i == 0 && c.EarlyImp {
			fmt.Fprintf(&b, "  r += unsafe { host99(x) };\n")
		}
		fmt.Fprintf(&b, "  if x > %d { r += f%d(x - %d); } else { r -= f%d(x + %d); }\n  unsafe { STATE[%d] = STATE[%d].wrapping_add(r); }\n  r\n}\n", k2, next, k3, other, k1, i%64, i%64)
	}
	if c.AddFunc {
		fmt.Fprintf(&b, "#[no_mangle] pub extern \"C\" fn added(x: i32) -> i32 { x.wrapping_mul(13).wrapping_sub(%d) }\n", c.Seed)
	}
	if c.Feat {
		fmt.Fprintf(&b, "#[no_mangle] pub unsafe extern \"C\" fn blit(d: *mut u8, s: *const u8, n: usize) { core::ptr::copy(s, d, n); core::ptr::write_bytes(d, 7, n / 2); }\n")
		fmt.Fprintf(&b, "#[no_mangle] pub unsafe extern \"C\" fn sum8(p: *const u8, n: usize) -> u32 { let mut t = 0u32; let mut i = 0; while i < n { t = t.wrapping_add(*p.add(i) as u32 * 3); i += 1; } t }\n")
		fmt.Fprintf(&b, "#[no_mangle] pub extern \"C\" fn pair(x: i64) -> (i64, i64) { (x.wrapping_mul(3), x ^ 5) }\n")
		fmt.Fprintf(&b, "#[no_mangle] pub extern \"C\" fn ext(x: i32) -> i32 { (x as i8) as i32 + (x as i16) as i32 }\n")
	}
	return b.String()
}

func (c rcfg) build(dir string) ([]byte, error) {
	src := filepath.Join(dir, "m.rs")
	if err := os.WriteFile(src, []byte(c.src()), 0o644); err != nil {
		return nil, err
	}
	out := filepath.Join(dir, "r.wasm")
	opt := "opt-level=2"
	var feat []string
	if c.Feat {
		opt = "opt-level=3"
		feat = []string{"-C", "target-feature=+bulk-memory,+simd128,+reference-types,+multivalue,+sign-ext,+nontrapping-fptoint,+mutable-globals"}
	}
	args := []string{"--target", "wasm32-unknown-unknown", "--crate-type", "cdylib", "-C", opt, "-C", "panic=abort", "-C", "strip=symbols",
		"-C", "codegen-units=1", "-C", "link-arg=--allow-undefined", "--edition", "2021", "-A", "warnings"}
	args = append(append(args, feat...), "-o", out, src)
	if o, err := exec.Command("rustc", args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("rustc: %v\n%s", err, o)
	}
	return os.ReadFile(out)
}

func rfamily(n int) []struct {
	Name string
	Cfg  rcfg
} {
	b := rcfg{N: n, Seed: 1}
	with := func(f func(*rcfg)) rcfg { c := b; f(&c); return c }
	return []struct {
		Name string
		Cfg  rcfg
	}{
		{"rs-r0", b},
		{"rs-body", with(func(c *rcfg) { c.BodyEdit = true })},
		{"rs-addfunc", with(func(c *rcfg) { c.AddFunc = true })},
		{"rs-earlyfunc", with(func(c *rcfg) { c.EarlyFunc = true })},
		{"rs-earlyimport", with(func(c *rcfg) { c.EarlyImp = true })},
		{"rs-feat", with(func(c *rcfg) { c.Feat = true })},
	}
}

// gen OUTDIR [NAME...] : build the controlled producer families with clang / wasm-ld and rustc.
func genCmd(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: gen OUTDIR [NAME...]")
	}
	out := args[0]
	want := map[string]bool{}
	for _, a := range args[1:] {
		want[a] = true
	}
	dir, err := os.MkdirTemp("", "z3wasmgen")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	os.MkdirAll(out, 0o755)
	// unsupported-feature modules (SYNTHETIC_FIXTURE): a component-model binary and a GC struct type
	if len(want) == 0 || want["neg-component"] {
		os.WriteFile(filepath.Join(out, "neg-component.wasm"), append([]byte("\x00asm\x0d\x00\x01\x00"), make([]byte, 64)...), 0o644)
	}
	if len(want) == 0 || want["neg-gc"] {
		os.WriteFile(filepath.Join(out, "neg-gc.wasm"), append([]byte("\x00asm\x01\x00\x00\x00"), 0x01, 0x05, 0x01, 0x5f, 0x01, 0x7f, 0x00), 0o644)
	}
	for _, x := range wfamily(300) {
		if len(want) > 0 && !want[x.Name] {
			continue
		}
		d, err := x.Cfg.build(dir)
		if err != nil {
			return fmt.Errorf("%s: %w", x.Name, err)
		}
		os.WriteFile(filepath.Join(out, x.Name+".wasm"), d, 0o644)
		fmt.Printf("%-18s %8d bytes\n", x.Name, len(d))
	}
	for _, x := range rfamily(300) {
		if len(want) > 0 && !want[x.Name] {
			continue
		}
		d, err := x.Cfg.build(dir)
		if err != nil {
			return fmt.Errorf("%s: %w", x.Name, err)
		}
		os.WriteFile(filepath.Join(out, x.Name+".wasm"), d, 0o644)
		fmt.Printf("%-18s %8d bytes\n", x.Name, len(d))
	}
	return nil
}
