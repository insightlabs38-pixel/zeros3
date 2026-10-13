package corpus

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func smallCfg() Config {
	c := DefaultConfig()
	c.SizeKiB = 64
	return c
}

type collect struct {
	m    []Member
	data map[string][]byte
}

func (c *collect) Put(m Member, d []byte) error {
	c.m = append(c.m, m)
	if c.data == nil {
		c.data = map[string][]byte{}
	}
	c.data[m.Path] = d
	return nil
}

func TestDeterministicRegeneration(t *testing.T) {
	var a, b collect
	ma, err := Generate(smallCfg(), &a)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := Generate(smallCfg(), &b)
	if err != nil {
		t.Fatal(err)
	}
	if len(ma.Members) != len(mb.Members) {
		t.Fatal("member count differs")
	}
	for i := range ma.Members {
		x, y := ma.Members[i], mb.Members[i]
		x.Truth, y.Truth = nil, nil
		if x.SHA256 != y.SHA256 || x.Path != y.Path || x.Size != y.Size {
			t.Fatalf("%s not reproducible", x.Path)
		}
	}
	c2 := smallCfg()
	c2.Seed = 2
	mc, _ := Generate(c2, hashSink{})
	same := 0
	for i := range ma.Members {
		if ma.Members[i].SHA256 == mc.Members[i].SHA256 {
			same++
		}
	}
	if same > len(ma.Members)/10 {
		t.Fatalf("seed barely affects output: %d/%d members identical", same, len(ma.Members))
	}
}

func TestManifestCountsAndCoverage(t *testing.T) {
	cfg := smallCfg()
	m, err := Generate(cfg, hashSink{})
	if err != nil {
		t.Fatal(err)
	}
	names, by := m.Group()
	if len(names) != len(Families()) {
		t.Fatalf("families %d, registered %d", len(names), len(Families()))
	}
	formats := map[string]bool{}
	for _, n := range names {
		ms := by[n]
		if len(ms) != cfg.Revisions+1 {
			t.Fatalf("%s: %d revisions", n, len(ms))
		}
		for i, mem := range ms {
			formats[mem.Format] = true
			if mem.Index != i || len(mem.SHA256) != 64 || mem.Size <= 0 || mem.Mutation == "" {
				t.Fatalf("%s/%s malformed row %+v", n, mem.Revision, mem)
			}
			if i == 0 && (mem.Parent != "" || mem.Base != "") {
				t.Fatalf("%s r0 has parent/base", n)
			}
			if i > 0 && (mem.Parent != ms[i-1].Revision || mem.Base != "r0") {
				t.Fatalf("%s/%s wrong lineage", n, mem.Revision)
			}
		}
	}
	for _, f := range []string{"raw", "gzip", "zip", "safetensors", "gguf", "pytorch-zip", "zbin", "car", "pagedb", "colstore", "ndarray", "protorec"} {
		if !formats[f] {
			t.Errorf("missing workload class %s", f)
		}
	}
	if len(Families()) < 50 {
		t.Errorf("only %d families", len(Families()))
	}
}

func TestDiskRoundTripAndVerify(t *testing.T) {
	dir := t.TempDir()
	cfg := smallCfg()
	cfg.Families = []string{"raw-*", "zip-deflate-*", "car-add"}
	m, err := WriteDir(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(m2.Members) != len(m.Members) {
		t.Fatal("manifest round trip lost members")
	}
	if err := VerifyFiles(dir, m2); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyRegen(m2); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, filepath.FromSlash(m2.Members[1].Path))
	b, _ := os.ReadFile(p)
	b[len(b)/2] ^= 1
	os.WriteFile(p, b, 0o644)
	if err := VerifyFiles(dir, m2); err == nil {
		t.Fatal("corrupted member not detected")
	}
}

func TestVerifyRegenDetectsDrift(t *testing.T) {
	cfg := smallCfg()
	cfg.Families = []string{"raw-insert"}
	m, _ := Generate(cfg, hashSink{})
	m.Members[2].SHA256 = strings.Repeat("0", 64)
	if _, err := VerifyRegen(m); err == nil {
		t.Fatal("drift not detected")
	}
	m, _ = Generate(cfg, hashSink{})
	m.GeneratorVersion++
	if _, err := VerifyRegen(m); err == nil {
		t.Fatal("generator version mismatch not detected")
	}
}

func TestToolDependentDriftIsWarning(t *testing.T) {
	cfg := smallCfg()
	cfg.Families = []string{"gzip-L6-append"}
	m, _ := Generate(cfg, hashSink{})
	m.Members[1].SHA256 = strings.Repeat("0", 64)
	m.GoVersion = "go0.0.0"
	w, err := VerifyRegen(m)
	if err != nil || len(w) != 1 {
		t.Fatalf("want one warning, got %v err=%v", w, err)
	}
}

func TestInvalidConfigRejected(t *testing.T) {
	bad := map[string]func(c *Config){
		"tiny size":      func(c *Config) { c.SizeKiB = 1 },
		"huge size":      func(c *Config) { c.SizeKiB = MaxSizeKiB + 1 },
		"zero revisions": func(c *Config) { c.Revisions = 0 },
		"many revisions": func(c *Config) { c.Revisions = MaxRevisions + 1 },
		"zero budget":    func(c *Config) { c.MaxTotalMiB = 0 },
		"unknown family": func(c *Config) { c.Families = []string{"nope-*"} },
		"bad pattern":    func(c *Config) { c.Families = []string{"["} },
		"over budget":    func(c *Config) { c.SizeKiB = 4096; c.MaxTotalMiB = 8 },
	}
	for name, mod := range bad {
		c := smallCfg()
		mod(&c)
		if _, err := Generate(c, hashSink{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOutputBounded(t *testing.T) {
	m, err := Generate(smallCfg(), hashSink{})
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, mem := range m.Members {
		total += mem.Size
		if mem.Size > 3*64*1024+4<<20 {
			t.Errorf("%s: %d bytes", mem.Path, mem.Size)
		}
	}
	if total > 80<<20 {
		t.Errorf("small corpus is %d bytes", total)
	}
}

func TestUnsafeManifestPathRejected(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"../x", "/etc/passwd", "a/../../x", ""} {
		if _, err := safeJoin(dir, p); err == nil {
			t.Errorf("path %q accepted", p)
		}
	}
	if (DiskSink{dir}).Put(Member{Path: "../escape"}, nil) == nil {
		t.Error("DiskSink wrote outside dir")
	}
}

func chainDigests(t *testing.T) map[string]string {
	cfg := smallCfg()
	cfg.Families = []string{"raw-insert", "st-modify-sparse", "bin-rebase", "car-add", "pagedb-update", "gguf-requant"}
	m, err := Generate(cfg, hashSink{})
	if err != nil {
		t.Fatal(err)
	}
	h := map[string]string{}
	for _, mem := range m.Members {
		h[mem.Family] += mem.SHA256
	}
	out := map[string]string{}
	for k, v := range h {
		sum := sha256.Sum256([]byte(v))
		out[k] = hex.EncodeToString(sum[:8])
	}
	return out
}

// TestPinnedGolden pins every revision of toolchain-independent families so
// generator drift is noticed. Bump GeneratorVersion and update on purpose.
func TestPinnedGolden(t *testing.T) {
	for k, v := range chainDigests(t) {
		if pinned[k] != v {
			t.Errorf("%s chain digest %s, pinned %q", k, v, pinned[k])
		}
	}
}
