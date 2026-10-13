// Package corpus generates deterministic revision families for Z3 research.
//
// Every family is a chain of byte artifacts r0..rN produced from a seed.
// Nothing here is a format implementation for production use: writers emit
// the smallest valid subset needed by later parsers and carry ground truth
// about which logical substructures are unchanged between revisions.
package corpus

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	// GeneratorVersion changes whenever any family's bytes change.
	GeneratorVersion = 1
	SchemaVersion    = "z3-corpus/1"

	MinSizeKiB   = 32
	MaxSizeKiB   = 256 * 1024
	MaxRevisions = 16
)

// Config selects a corpus. The zero value is invalid; use DefaultConfig.
type Config struct {
	Seed        uint64   `json:"seed"`
	SizeKiB     int      `json:"size_kib"`  // approximate base artifact size
	Revisions   int      `json:"revisions"` // revisions after r0
	Families    []string `json:"families,omitempty"`
	MaxTotalMiB int      `json:"max_total_mib"`
}

func DefaultConfig() Config {
	return Config{Seed: 1, SizeKiB: 512, Revisions: 3, MaxTotalMiB: 2048}
}

// Validate rejects configurations whose output is unbounded or meaningless.
func (c Config) Validate() error {
	if c.SizeKiB < MinSizeKiB || c.SizeKiB > MaxSizeKiB {
		return fmt.Errorf("corpus: size_kib %d outside [%d,%d]", c.SizeKiB, MinSizeKiB, MaxSizeKiB)
	}
	if c.Revisions < 1 || c.Revisions > MaxRevisions {
		return fmt.Errorf("corpus: revisions %d outside [1,%d]", c.Revisions, MaxRevisions)
	}
	if c.MaxTotalMiB < 1 {
		return errors.New("corpus: max_total_mib must be positive")
	}
	fams, err := c.selected()
	if err != nil {
		return err
	}
	est := int64(len(fams)) * int64(c.Revisions+1) * int64(c.SizeKiB) / 1024
	if est > int64(c.MaxTotalMiB) {
		return fmt.Errorf("corpus: estimated %d MiB exceeds max_total_mib %d (reduce families, revisions or size)", est, c.MaxTotalMiB)
	}
	return nil
}

func (c Config) selected() ([]*Family, error) {
	all := Families()
	if len(c.Families) == 0 {
		return all, nil
	}
	var out []*Family
	for _, pat := range c.Families {
		if _, err := path.Match(pat, ""); err != nil {
			return nil, fmt.Errorf("corpus: bad family pattern %q", pat)
		}
		n := 0
		for _, f := range all {
			if ok, _ := path.Match(pat, f.Name); ok {
				out = append(out, f)
				n++
			}
		}
		if n == 0 {
			return nil, fmt.Errorf("corpus: family pattern %q matches nothing", pat)
		}
	}
	return dedupFamilies(out), nil
}

func dedupFamilies(in []*Family) []*Family {
	seen := map[string]bool{}
	var out []*Family
	for _, f := range in {
		if !seen[f.Name] {
			seen[f.Name] = true
			out = append(out, f)
		}
	}
	return out
}

// Artifact is one generated revision.
type Artifact struct {
	Revision string
	Mutation string
	Parent   string
	Data     []byte
	Truth    map[string][]string
}

// Family is a named revision chain generator.
type Family struct {
	Name     string
	Format   string // file format class: raw, gzip, zip, safetensors, ...
	Workload string // research workload class
	Ext      string
	// ToolDependent marks output that depends on the Go standard library's
	// DEFLATE implementation; its bytes may differ across Go releases.
	ToolDependent bool
	gen           func(c *Ctx, emit func(Artifact) error) error
}

// Ctx carries generation parameters to a family.
type Ctx struct {
	Cfg  Config
	Fam  *Family
	size int // base artifact bytes
}

func (c *Ctx) RNG(label string) *RNG { return NewRNG(c.Cfg.Seed, c.Fam.Name+"/"+label) }

var registry []*Family

func register(f Family) { registry = append(registry, &f) }

// Families returns all registered families sorted by name.
func Families() []*Family {
	out := append([]*Family(nil), registry...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// chain emits r0..rN. build(i) produces revision i, keeping any state it
// needs in its closure.
func (c *Ctx) chain(emit func(Artifact) error, build func(i int) ([]byte, string, map[string][]string)) error {
	prev := ""
	for i := 0; i <= c.Cfg.Revisions; i++ {
		data, mut, truth := build(i)
		a := Artifact{Revision: fmt.Sprintf("r%d", i), Mutation: mut, Parent: prev, Data: data, Truth: truth}
		if err := emit(a); err != nil {
			return err
		}
		prev = a.Revision
	}
	return nil
}

// Member is one manifest row.
type Member struct {
	Family        string              `json:"family"`
	Format        string              `json:"format"`
	Workload      string              `json:"workload"`
	Revision      string              `json:"revision"`
	Index         int                 `json:"index"`
	Mutation      string              `json:"mutation"`
	Path          string              `json:"path"`
	Size          int64               `json:"size"`
	SHA256        string              `json:"sha256"`
	Parent        string              `json:"parent,omitempty"`
	Base          string              `json:"base,omitempty"`
	ToolDependent bool                `json:"toolchain_dependent,omitempty"`
	Truth         map[string][]string `json:"truth,omitempty"`
}

type Manifest struct {
	Schema           string   `json:"schema"`
	GeneratorVersion int      `json:"generator_version"`
	GoVersion        string   `json:"go_version"`
	Config           Config   `json:"config"`
	Members          []Member `json:"members"`
}

// Sink receives generated artifacts.
type Sink interface {
	Put(m Member, data []byte) error
}

type hashSink struct{}

func (hashSink) Put(Member, []byte) error { return nil }

// DiskSink writes artifacts below Dir.
type DiskSink struct{ Dir string }

func (d DiskSink) Put(m Member, data []byte) error {
	p, err := safeJoin(d.Dir, m.Path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func safeJoin(dir, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || !filepath.IsLocal(filepath.FromSlash(rel)) {
		return "", fmt.Errorf("corpus: unsafe member path %q", rel)
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), nil
}

// MemberPath resolves a member's file inside a corpus directory.
func MemberPath(dir string, m Member) (string, error) { return safeJoin(dir, m.Path) }

// Generate runs the configured families into sink and returns the manifest.
func Generate(cfg Config, sink Sink) (*Manifest, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	fams, _ := cfg.selected()
	m := &Manifest{Schema: SchemaVersion, GeneratorVersion: GeneratorVersion, GoVersion: runtime.Version(), Config: cfg}
	var total int64
	limit := int64(cfg.MaxTotalMiB) << 20
	perArtifact := int64(cfg.SizeKiB)*1024*3 + 4<<20
	for _, f := range fams {
		ctx := &Ctx{Cfg: cfg, Fam: f, size: cfg.SizeKiB * 1024}
		idx := 0
		err := f.gen(ctx, func(a Artifact) error {
			if int64(len(a.Data)) > perArtifact {
				return fmt.Errorf("%s is %d bytes, over the per-artifact bound %d", a.Revision, len(a.Data), perArtifact)
			}
			if total += int64(len(a.Data)); total > limit {
				return fmt.Errorf("output exceeds max_total_mib %d", cfg.MaxTotalMiB)
			}
			sum := sha256.Sum256(a.Data)
			mem := Member{
				Family: f.Name, Format: f.Format, Workload: f.Workload,
				Revision: a.Revision, Index: idx, Mutation: a.Mutation,
				Path: f.Name + "/" + a.Revision + f.Ext, Size: int64(len(a.Data)),
				SHA256: hex.EncodeToString(sum[:]), Parent: a.Parent, Base: "r0",
				ToolDependent: f.ToolDependent, Truth: a.Truth,
			}
			if idx == 0 {
				mem.Base = ""
			}
			idx++
			m.Members = append(m.Members, mem)
			return sink.Put(mem, a.Data)
		})
		if err != nil {
			return nil, fmt.Errorf("corpus: family %s: %w", f.Name, err)
		}
	}
	return m, nil
}

const manifestName = "manifest.json"

// WriteDir generates the corpus into dir and writes manifest.json.
func WriteDir(cfg Config, dir string) (*Manifest, error) {
	m, err := Generate(cfg, DiskSink{dir})
	if err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(m, "", " ")
	return m, os.WriteFile(filepath.Join(dir, manifestName), append(b, '\n'), 0o644)
}

// ReadManifest loads and sanity-checks dir/manifest.json.
func ReadManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("corpus: manifest: %w", err)
	}
	if m.Schema != SchemaVersion {
		return nil, fmt.Errorf("corpus: unsupported manifest schema %q", m.Schema)
	}
	for _, mem := range m.Members {
		if _, err := safeJoin(dir, mem.Path); err != nil {
			return nil, err
		}
	}
	return &m, nil
}

// VerifyFiles checks every member's size and SHA-256 against the files in dir.
func VerifyFiles(dir string, m *Manifest) error {
	for _, mem := range m.Members {
		p, err := safeJoin(dir, mem.Path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		if int64(len(b)) != mem.Size || hex.EncodeToString(sum[:]) != mem.SHA256 {
			return fmt.Errorf("corpus: %s: size/sha256 mismatch with manifest", mem.Path)
		}
	}
	return nil
}

// VerifyRegen regenerates the manifest's configuration in memory and compares
// every member. Toolchain-dependent members whose manifest was produced by a
// different Go release are reported in warnings, not as errors.
func VerifyRegen(m *Manifest) (warnings []string, err error) {
	if m.GeneratorVersion != GeneratorVersion {
		return nil, fmt.Errorf("corpus: manifest generator version %d, this build is %d", m.GeneratorVersion, GeneratorVersion)
	}
	fresh, err := Generate(m.Config, hashSink{})
	if err != nil {
		return nil, err
	}
	if len(fresh.Members) != len(m.Members) {
		return nil, fmt.Errorf("corpus: regenerated %d members, manifest has %d", len(fresh.Members), len(m.Members))
	}
	for i, f := range fresh.Members {
		o := m.Members[i]
		if f.Path != o.Path || f.Size != o.Size || f.SHA256 != o.SHA256 {
			if o.ToolDependent && m.GoVersion != runtime.Version() {
				warnings = append(warnings, fmt.Sprintf("%s differs (manifest %s, this %s)", o.Path, m.GoVersion, runtime.Version()))
				continue
			}
			return warnings, fmt.Errorf("corpus: %s does not regenerate to its manifest entry", o.Path)
		}
	}
	return warnings, nil
}

// Group returns the manifest members of each family in revision order.
func (m *Manifest) Group() (names []string, byFamily map[string][]Member) {
	byFamily = map[string][]Member{}
	for _, mem := range m.Members {
		if _, ok := byFamily[mem.Family]; !ok {
			names = append(names, mem.Family)
		}
		byFamily[mem.Family] = append(byFamily[mem.Family], mem)
	}
	for _, ms := range byFamily {
		sort.Slice(ms, func(i, j int) bool { return ms[i].Index < ms[j].Index })
	}
	return names, byFamily
}

// FamilyNames lists registered family names (for CLI help).
func FamilyNames() string {
	var n []string
	for _, f := range Families() {
		n = append(n, f.Name)
	}
	return strings.Join(n, ", ")
}
