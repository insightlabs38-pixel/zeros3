package gitz3

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// RealRepo is a cached bare clone of a public (or local) repository. Pinned
// object sets are produced with local, controlled `git pack-objects` runs, so
// every pack in an experiment comes from one git version and explicit settings
// rather than from a server's pack.
type RealRepo struct{ *Repo }

func OpenRealRepo(dir string) *RealRepo { return &RealRepo{&Repo{Dir: dir, Algo: SHA1}} }

// CacheRoot is where real-world git caches live (outside the repository).
func CacheRoot() string {
	if p := os.Getenv("Z3_GIT_CACHE"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "z3-realworld", "git")
}

// Fetch makes sure the commit `tip` (and `depth` generations of history) is in
// the cache, fetching it by explicit SHA when it is missing. The pack size in
// the cache is checked against maxMiB and the fetch is undone if it is larger.
func (c *RealRepo) Fetch(url, tip string, depth int, maxMiB int) error {
	if _, err := os.Stat(filepath.Join(c.Dir, "HEAD")); err != nil {
		if err := os.MkdirAll(c.Dir, 0o755); err != nil {
			return err
		}
		cmd := exec.Command(GitBin(), "init", "-q", "--bare", c.Dir)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_DIR=")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %v: %s", err, out)
		}
	}
	if _, err := c.Git(nil, "cat-file", "-e", tip+"^{commit}"); err == nil {
		return nil
	}
	args := []string{"fetch", "-q", "--no-tags", "--no-write-fetch-head"}
	if depth > 0 {
		args = append(args, "--depth="+strconv.Itoa(depth))
	}
	args = append(args, url, tip)
	if _, err := c.Git(nil, args...); err != nil {
		return err
	}
	if _, err := c.Git(nil, "update-ref", "refs/pins/tip", tip); err != nil {
		return err
	}
	out, err := c.Git(nil, "count-objects", "-v")
	if err != nil {
		return err
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "size-pack:") {
			kib, _ := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(l, "size-pack:")))
			if kib > maxMiB<<10 {
				os.RemoveAll(c.Dir)
				return fmt.Errorf("fetched pack %d KiB exceeds -max-mib %d; cache removed", kib, maxMiB)
			}
		}
	}
	return nil
}

// RevList returns `git rev-list --first-parent -n count rev` (newest first).
func (c *RealRepo) RevList(rev string, count int) ([]string, error) {
	out, err := c.Git(nil, "rev-list", "--first-parent", "-n", strconv.Itoa(count), rev)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(out)), nil
}

// PackState writes the pack of everything reachable from revs, with the given
// producer settings, into dir and returns it as a State.
func (c *RealRepo) PackState(label, dir string, revs []string, spec RepackSpec) (*State, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	cfg := []string{}
	if spec.Compression != 0 {
		cfg = append(cfg, "-c", fmt.Sprintf("pack.compression=%d", spec.Compression))
	}
	args := append(cfg, "pack-objects", "-q", "--threads=1")
	if !spec.REFDelta {
		args = append(args, "--delta-base-offset")
	}
	if spec.Force || spec.NoReuseObject || spec.NoDelta {
		args = append(args, "--no-reuse-delta")
	}
	if spec.NoReuseObject {
		args = append(args, "--no-reuse-object")
	}
	if spec.NoDelta {
		args = append(args, "--window=0")
	} else {
		if spec.Window > 0 {
			args = append(args, fmt.Sprintf("--window=%d", spec.Window))
		}
		if spec.Depth > 0 {
			args = append(args, fmt.Sprintf("--depth=%d", spec.Depth))
		}
	}
	var in string
	if spec.Reverse {
		ls, err := c.Git(nil, append([]string{"rev-list", "--objects"}, revs...)...)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimRight(string(ls), "\n"), "\n")
		for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
			lines[i], lines[j] = lines[j], lines[i]
		}
		in = strings.Join(lines, "\n") + "\n"
	} else {
		args = append(args, "--revs")
		in = strings.Join(revs, "\n") + "\n"
	}
	args = append(args, filepath.Join(dir, "pack"))
	if _, err := c.Git(strings.NewReader(in), args...); err != nil {
		return nil, err
	}
	pp := &Repo{Dir: dir}
	_ = pp
	m, _ := filepath.Glob(filepath.Join(dir, "pack-*.pack"))
	if len(m) != 1 {
		return nil, fmt.Errorf("pack-objects wrote %d packs", len(m))
	}
	pk, err := os.ReadFile(m[0])
	if err != nil {
		return nil, err
	}
	ix, err := os.ReadFile(strings.TrimSuffix(m[0], ".pack") + ".idx")
	if err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(filepath.Base(m[0]), ".pack")
	return &State{Label: label, Tag: int(SHA1), Files: []File{{Name: "pack/" + name + ".pack", Kind: "pack", Data: pk}, {Name: "pack/" + name + ".idx", Kind: "idx", Data: ix}}}, nil
}

// LooseState explodes the pack of revs into loose object files (git unpack-objects).
func (c *RealRepo) LooseState(label, dir string, revs []string) (*State, error) {
	pk, err := c.Git(strings.NewReader(strings.Join(revs, "\n")+"\n"), "pack-objects", "-q", "--revs", "--stdout", "--threads=1", "--delta-base-offset")
	if err != nil {
		return nil, err
	}
	r, err := NewRepo(dir, SHA1)
	if err != nil {
		return nil, err
	}
	if _, err := r.Git(bytes.NewReader(pk), "unpack-objects", "-q"); err != nil {
		return nil, err
	}
	return StateOf(r, label)
}
