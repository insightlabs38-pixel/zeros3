package gitz3

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Producer harness: runs the installed git with a controlled environment. Git
// is a research producer/oracle only; nothing in the parsers, representations
// or reconstruction calls it.

// GitBin is the git executable (override with Z3_GIT).
func GitBin() string {
	if p := os.Getenv("Z3_GIT"); p != "" {
		return p
	}
	return "git"
}

// GitVersion returns "git version X".
func GitVersion() string {
	out, err := exec.Command(GitBin(), "--version").Output()
	if err != nil {
		return "unavailable"
	}
	return strings.TrimSpace(string(out))
}

// Repo is a scratch repository driven by the git CLI.
type Repo struct {
	Dir  string // the repository directory (bare-style: Dir is the git dir)
	Algo Algo
	Log  []string // every command line run, for provenance
}

func (r *Repo) env() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + r.Dir, "LC_ALL=C", "LANG=C", "TZ=UTC",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Z3 Lab", "GIT_AUTHOR_EMAIL=z3@example.invalid", "GIT_AUTHOR_DATE=1700000000 +0000",
		"GIT_COMMITTER_NAME=Z3 Lab", "GIT_COMMITTER_EMAIL=z3@example.invalid", "GIT_COMMITTER_DATE=1700000000 +0000",
		"GIT_DIR=" + r.Dir,
	}
}

// Git runs git with args (after the controlled -c settings) and returns stdout.
func (r *Repo) Git(stdin io.Reader, args ...string) ([]byte, error) {
	full := append([]string{"-c", "pack.threads=1", "-c", "gc.auto=0", "-c", "pack.writeReverseIndex=false", "-c", "core.logAllRefUpdates=false"}, args...)
	cmd := exec.Command(GitBin(), full...)
	cmd.Env = r.env()
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	r.Log = append(r.Log, "git "+strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// NewRepo creates an empty bare repository in dir with the given object format.
func NewRepo(dir string, a Algo) (*Repo, error) {
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	r := &Repo{Dir: dir, Algo: a}
	cmd := exec.Command(GitBin(), "init", "-q", "--bare", "--object-format="+a.String(), dir)
	cmd.Env = append(r.env()[:len(r.env())-1], "GIT_DIR=")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git init: %v: %s", err, out)
	}
	return r, nil
}

// Import runs git fast-import over a stream.
func (r *Repo) Import(stream []byte) error {
	_, err := r.Git(bytes.NewReader(stream), "fast-import", "--quiet", "--done")
	return err
}

// ImportHistory imports a synthetic history onto refs/heads/main.
func (r *Repo) ImportHistory(h *History) error {
	var b bytes.Buffer
	if err := h.Stream(&b, "main"); err != nil {
		return err
	}
	return r.Import(b.Bytes())
}

// Unpack explodes every pack into loose objects (core.looseCompression applies)
// and removes the packs.
func (r *Repo) Unpack() error {
	packs, err := r.PackPairs()
	if err != nil {
		return err
	}
	// objects already present in the repository are skipped by unpack-objects, so
	// the packs must be gone before their objects are written loose.
	if err := r.RemovePacks(); err != nil {
		return err
	}
	for _, p := range packs {
		if _, err := r.Git(bytes.NewReader(p.Pack), "unpack-objects", "-q"); err != nil {
			return err
		}
	}
	return nil
}

// RemovePacks deletes every pack and index.
func (r *Repo) RemovePacks() error {
	m, _ := filepath.Glob(filepath.Join(r.Dir, "objects", "pack", "pack-*"))
	for _, f := range m {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	return nil
}

// RepackSpec selects producer parameters.
type RepackSpec struct {
	Window, Depth int  // 0 = git default (10 / 50)
	Compression   int  // pack.compression; 0 = leave default (zlib -1 -> level 6); use 1..9 explicit, -1 default
	NoDelta       bool // window=0 (no delta search) and no reuse of deltas
	Force         bool // -f: recompute deltas
	NoReuseObject bool // -F: recompress everything
	REFDelta      bool // repack.useDeltaBaseOffset=false (REF_DELTA entries)
	Incremental   bool // no -a: pack only loose objects into a new pack
	Reverse       bool // pack-objects over a reversed object list (changes write order)
	IndexV1       bool // --index-version=1 (characterisation only)
	Keep          bool // do not delete old packs (-d omitted)
	Label         string
}

// Repack runs git repack (or pack-objects for Reverse) with the spec.
func (r *Repo) Repack(s RepackSpec) error {
	cfg := []string{}
	if s.Compression != 0 {
		cfg = append(cfg, "-c", fmt.Sprintf("pack.compression=%d", s.Compression))
	}
	if s.REFDelta {
		cfg = append(cfg, "-c", "repack.useDeltaBaseOffset=false")
	}
	if s.IndexV1 {
		cfg = append(cfg, "-c", "pack.indexVersion=1")
	}
	if s.Reverse {
		return r.packObjectsReversed(s, cfg)
	}
	args := append(cfg, "repack", "-q")
	if !s.Incremental {
		args = append(args, "-a")
	}
	if !s.Keep {
		args = append(args, "-d")
	}
	if s.Force {
		args = append(args, "-f")
	}
	if s.NoReuseObject {
		args = append(args, "-F")
	}
	if s.NoDelta {
		args = append(args, "--window=0", "-f")
	} else {
		if s.Window > 0 {
			args = append(args, fmt.Sprintf("--window=%d", s.Window))
		}
		if s.Depth > 0 {
			args = append(args, fmt.Sprintf("--depth=%d", s.Depth))
		}
	}
	_, err := r.Git(nil, args...)
	if err == nil && !s.Incremental && !s.Keep {
		_, err = r.Git(nil, "prune-packed")
	}
	return err
}

func (r *Repo) packObjectsReversed(s RepackSpec, cfg []string) error {
	ls, err := r.Git(nil, "rev-list", "--objects", "--all")
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(ls), "\n"), "\n")
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	old, _ := r.PackPairs()
	args := append(cfg, "pack-objects", "-q", "--delta-base-offset", filepath.Join(r.Dir, "objects", "pack", "pack"))
	if s.REFDelta {
		args = append(cfg, "pack-objects", "-q", filepath.Join(r.Dir, "objects", "pack", "pack"))
	}
	if s.Window > 0 {
		args = append(args, fmt.Sprintf("--window=%d", s.Window))
	}
	if s.Depth > 0 {
		args = append(args, fmt.Sprintf("--depth=%d", s.Depth))
	}
	if s.NoDelta {
		args = append(args, "--window=0")
	}
	if s.Force || s.NoReuseObject {
		args = append(args, "--no-reuse-delta")
	}
	if s.NoReuseObject {
		args = append(args, "--no-reuse-object")
	}
	// the reversed order must be what pack-objects sees; --no-reuse-delta makes the order the only reuse-independent input
	if _, err := r.Git(strings.NewReader(strings.Join(lines, "\n")+"\n"), args...); err != nil {
		return err
	}
	if !s.Keep {
		// remove the previous packs (not the one just written: it is not in `old`)
		for _, p := range old {
			base := filepath.Join(r.Dir, "objects", "pack", p.Name)
			os.Remove(base + ".pack")
			os.Remove(base + ".idx")
		}
	}
	return nil
}

// PackPair is one pack with its index.
type PackPair struct {
	Name string // pack-<hash> without extension
	Pack []byte
	Idx  []byte
}

// PackPairs reads every pack+idx in the repository, sorted by name.
func (r *Repo) PackPairs() ([]PackPair, error) {
	m, _ := filepath.Glob(filepath.Join(r.Dir, "objects", "pack", "pack-*.pack"))
	sort.Strings(m)
	var out []PackPair
	for _, f := range m {
		pk, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		ix, err := os.ReadFile(strings.TrimSuffix(f, ".pack") + ".idx")
		if err != nil {
			return nil, err
		}
		out = append(out, PackPair{Name: strings.TrimSuffix(filepath.Base(f), ".pack"), Pack: pk, Idx: ix})
	}
	return out, nil
}

// LooseFile is one loose object file.
type LooseFile struct {
	Path string // "xx/yyyy..." (the file name encodes the object id)
	Data []byte
}

// Loose reads every loose object file, sorted by path.
func (r *Repo) Loose() ([]LooseFile, error) {
	var out []LooseFile
	root := filepath.Join(r.Dir, "objects")
	ents, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, d := range ents {
		if !d.IsDir() || len(d.Name()) != 2 {
			continue
		}
		fs, _ := os.ReadDir(filepath.Join(root, d.Name()))
		for _, f := range fs {
			b, err := os.ReadFile(filepath.Join(root, d.Name(), f.Name()))
			if err != nil {
				return nil, err
			}
			out = append(out, LooseFile{Path: d.Name() + "/" + f.Name(), Data: b})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// HexID returns the lower-case hex of an id.
func HexID(id []byte) string { return fmt.Sprintf("%x", id) }

// SHA256Hex is the SHA-256 of b in hex (provenance records).
func SHA256Hex(b []byte) string { h := sha256.Sum256(b); return fmt.Sprintf("%x", h) }
