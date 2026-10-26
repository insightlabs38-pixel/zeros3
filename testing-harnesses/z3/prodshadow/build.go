// Package prodshadow maps repz3 representations onto the real ZeroS3 CAS /
// pack / tier / GC machinery as optional, lab-only shadow state (Z3-22).
//
// Nothing here re-implements the store. The real zeros3.go is compiled as-is
// (once unmodified, once into a temporary same-package bridge that renames a
// single method and adds a lab file); every physical operation - CAS reads and
// writes, locator, packs, compaction, tiers, GC, repack, verify - is the
// production code. The shadow layer owns only descriptors and shadow roots.
package prodshadow

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

//go:embed bridge/zz3bridge.go.txt
var bridgeSrc []byte

// Binaries are the two builds of the current production source.
type Binaries struct {
	Prod   string // unmodified zeros3: the "current-production truth"
	Shadow string // same source + lab bridge; with Z3_SHADOW_LIVE its reachability includes shadow roots
	Dir    string
}

// RepoRoot is the repository checkout that holds zeros3.go.
func RepoRoot() (string, error) {
	_, f, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(f), "..", "..", "..")
	if _, err := os.Stat(filepath.Join(root, "zeros3.go")); err != nil {
		return "", fmt.Errorf("prodshadow: zeros3.go not found under %s", root)
	}
	return filepath.Abs(root)
}

const renameFrom = "func (s *Store) computeReachabilityObserved("
const renameTo = "func (s *Store) computeReachabilityObservedOrig("

// Build compiles both binaries into a content-keyed cache directory under
// os.TempDir (reused while zeros3.go and the bridge are unchanged).
func Build() (*Binaries, error) {
	root, err := RepoRoot()
	if err != nil {
		return nil, err
	}
	src, err := os.ReadFile(filepath.Join(root, "zeros3.go"))
	if err != nil {
		return nil, err
	}
	gomod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(src)
	h.Write(bridgeSrc)
	key := hex.EncodeToString(h.Sum(nil))[:12]
	dir := filepath.Join(os.TempDir(), "z3shadow-bin-"+key)
	b := &Binaries{Dir: dir, Prod: filepath.Join(dir, "zeros3-prod"), Shadow: filepath.Join(dir, "zeros3-shadow")}
	if exists(b.Prod) && exists(b.Shadow) {
		return b, nil
	}
	tmp, err := os.MkdirTemp("", "z3shadow-build-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	// 1. unmodified production source
	pdir := filepath.Join(tmp, "prod")
	if err := writeSrc(pdir, src, gomod); err != nil {
		return nil, err
	}
	// 2. bridge copy: one method renamed, one lab file added
	if n := strings.Count(string(src), renameFrom); n != 1 {
		return nil, fmt.Errorf("prodshadow: expected exactly one %q in zeros3.go, found %d", renameFrom, n)
	}
	patched := strings.Replace(string(src), renameFrom, renameTo, 1)
	sdir := filepath.Join(tmp, "shadow")
	if err := writeSrc(sdir, []byte(patched), gomod); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(sdir, "zz3bridge.go"), bridgeSrc, 0o644); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for _, j := range []struct{ src, out string }{{pdir, b.Prod}, {sdir, b.Shadow}} {
		out := j.out + ".tmp"
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", out, ".")
		cmd.Dir = j.src
		if msg, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("prodshadow: build %s: %v\n%s", j.out, err, msg)
		}
		if err := os.Rename(out, j.out); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func writeSrc(dir string, src, gomod []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "zeros3.go"), src, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "go.mod"), gomod, 0o644)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
