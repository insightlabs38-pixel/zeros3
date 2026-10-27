package main

// Controlled APK-shaped containers (SYNTHETIC_FIXTURE framing around PRODUCER_WORKLOAD DEX): an
// unsigned ZIP with a manifest-like blob, a stored resources.arsc-like blob, assets, icons and one
// or two classes*.dex, built with Info-ZIP. These are layout controls, not natural revisions; no
// Android resource or signing semantics are involved.

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func init() { cmds["apk"] = apkCmd }

func randBytes(seed int64, n int) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	r.Read(b)
	return b
}

// textBytes: compressible, text-like.
func textBytes(seed int64, n int) []byte {
	r := rand.New(rand.NewSource(seed))
	words := []string{"alpha", "beta", "gamma", "delta", "layout", "string", "color", "style", "drawable", "value"}
	var sb strings.Builder
	for sb.Len() < n {
		sb.WriteString(words[r.Intn(len(words))])
		sb.WriteString(fmt.Sprintf("%d ", r.Intn(1000)))
	}
	return []byte(sb.String()[:n])
}

type apkSpec struct {
	name       string
	dex        []string // dex paths in member order (classes.dex, classes2.dex...)
	storedDex  bool
	reverse    bool // member creation order reversed
	extraAsset bool
	resChange  bool
	stamp      time.Time
}

func buildAPK(dir string, sp apkSpec) (string, error) {
	work := filepath.Join(dir, sp.name+".d")
	os.RemoveAll(work)
	if err := os.MkdirAll(filepath.Join(work, "res"), 0o755); err != nil {
		return "", err
	}
	os.MkdirAll(filepath.Join(work, "assets"), 0o755)
	os.MkdirAll(filepath.Join(work, "META-INF"), 0o755)
	w := func(name string, b []byte) { os.WriteFile(filepath.Join(work, name), b, 0o644) }
	w("AndroidManifest.xml", randBytes(1, 6000))
	seed := int64(2)
	if sp.resChange {
		seed = 22
	}
	w("resources.arsc", append(randBytes(seed, 300000), textBytes(seed, 300000)...))
	for i := 0; i < 8; i++ {
		s := int64(10 + i)
		if sp.resChange && i == 3 {
			s = 99
		}
		w(fmt.Sprintf("assets/data%d.txt", i), textBytes(s, 120000))
	}
	for i := 0; i < 40; i++ {
		w(fmt.Sprintf("res/icon%02d.png", i), randBytes(int64(100+i), 4096))
	}
	w("META-INF/MANIFEST.MF", textBytes(7, 20000))
	if sp.extraAsset {
		w("assets/extra.bin", randBytes(555, 500000))
	}
	names := []string{"AndroidManifest.xml", "resources.arsc"}
	for i := 0; i < 8; i++ {
		names = append(names, fmt.Sprintf("assets/data%d.txt", i))
	}
	for i := 0; i < 40; i++ {
		names = append(names, fmt.Sprintf("res/icon%02d.png", i))
	}
	names = append(names, "META-INF/MANIFEST.MF")
	if sp.extraAsset {
		names = append(names, "assets/extra.bin")
	}
	var dexNames []string
	for i, p := range sp.dex {
		n := "classes.dex"
		if i > 0 {
			n = fmt.Sprintf("classes%d.dex", i+1)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		w(n, b)
		dexNames = append(dexNames, n)
	}
	all := append(append([]string(nil), names...), dexNames...)
	if sp.reverse {
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}
	}
	for _, n := range all {
		os.Chtimes(filepath.Join(work, n), sp.stamp, sp.stamp)
	}
	out := filepath.Join(dir, sp.name+".apk")
	os.Remove(out)
	// resources.arsc and (optionally) classes.dex are stored; everything else DEFLATE level 6
	var deflated, stored []string
	for _, n := range all {
		if n == "resources.arsc" || (sp.storedDex && strings.HasSuffix(n, ".dex")) {
			stored = append(stored, n)
		} else {
			deflated = append(deflated, n)
		}
	}
	run := func(args ...string) error {
		c := exec.Command("zip", args...)
		c.Dir = work
		if o, err := c.CombinedOutput(); err != nil {
			return fmt.Errorf("zip: %v\n%s", err, o)
		}
		return nil
	}
	// keep the creation order: add in order, switching method per member
	for _, n := range all {
		m := "-6"
		if contains(stored, n) {
			m = "-0"
		}
		if err := run("-q", m, out, n); err != nil {
			return "", err
		}
	}
	return out, nil
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// apk OUTDIR WLDIR: builds the controlled APK set from the workload revisions.
func apkCmd(args []string) error {
	out, wl := args[0], args[1]
	os.MkdirAll(out, 0o755)
	dex := func(rev string) string { return filepath.Join(wl, rev, "dex", "classes.dex") }
	t0 := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
	// multidex control: the same classes split over two DEX files
	var half [2][]string
	var cf []string
	filepath.Walk(filepath.Join(wl, "base", "classes"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && strings.HasSuffix(p, ".class") {
			cf = append(cf, p)
		}
		return nil
	})
	sort.Strings(cf)
	half[0], half[1] = cf[:len(cf)/2], cf[len(cf)/2:]
	r8 := os.Getenv("Z3_R8")
	var md []string
	for i := 0; i < 2; i++ {
		d := filepath.Join(out, fmt.Sprintf("multidex%d", i+1))
		os.MkdirAll(d, 0o755)
		a := append([]string{"-cp", r8, "com.android.tools.r8.D8", "--release", "--min-api", "24", "--output", d}, half[i]...)
		if o, err := sh("", nil, "java", a...); err != nil {
			return fmt.Errorf("d8 multidex: %v\n%s", err, o)
		}
		md = append(md, filepath.Join(d, "classes.dex"))
	}
	specs := []apkSpec{
		{name: "apk-multidex", dex: md, stamp: t0},
		{name: "apk-base", dex: []string{dex("base")}, stamp: t0},
		{name: "apk-dexbody", dex: []string{dex("x-body")}, stamp: t0},
		{name: "apk-dexaddmethod", dex: []string{dex("x-addmethod")}, stamp: t0},
		{name: "apk-dexaddclass", dex: []string{dex("x-addclass")}, stamp: t0},
		{name: "apk-dexearlyname", dex: []string{dex("x-earlyname")}, stamp: t0},
		{name: "apk-dexdep", dex: []string{dex("x-dep")}, stamp: t0},
		{name: "apk-dexunrelated", dex: []string{dex("x-unrelated")}, stamp: t0},
		{name: "apk-metadata", dex: []string{dex("base")}, stamp: t0.Add(48 * time.Hour)},
		{name: "apk-reorder", dex: []string{dex("base")}, reverse: true, stamp: t0},
		{name: "apk-storeddex", dex: []string{dex("base")}, storedDex: true, stamp: t0},
		{name: "apk-extraasset", dex: []string{dex("base")}, extraAsset: true, stamp: t0},
		{name: "apk-resonly", dex: []string{dex("base")}, resChange: true, stamp: t0},
	}
	for _, sp := range specs {
		p, err := buildAPK(out, sp)
		if err != nil {
			return err
		}
		fi, _ := os.Stat(p)
		fmt.Printf("%-18s %9d\n", sp.name, fi.Size())
	}
	return nil
}
