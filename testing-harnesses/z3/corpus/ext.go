package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Optional external-producer corpus. Locally available compressors recompress
// the same deterministic plaintext chains as the stdlib DEFLATE families. Nothing
// here is required: absent tools are skipped and reported. Output is scratch data.

// ExtProducer is one locally available compressor configuration.
type ExtProducer struct {
	Name    string // e.g. "zlib-py-L6"
	Format  string // deflate | gzip | zip
	Ext     string
	Version string // provenance of the tool actually used
	run     func(plain []byte) ([]byte, error)
}

const pyZlib = `import sys,zlib
l,w,m,s=map(int,sys.argv[1:5])
c=zlib.compressobj(l,8,w,m,s)
sys.stdout.buffer.write(c.compress(sys.stdin.buffer.read())+c.flush())`

// Compress runs the producer on plain.
func (p ExtProducer) Compress(plain []byte) ([]byte, error) { return p.run(plain) }

func runPipe(in []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = bytes.NewReader(in)
	var out, er bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &er
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(er.String()))
	}
	return out.Bytes(), nil
}

func firstLine(b []byte, err error) string {
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
}

// ExtProducers lists the producers found on this machine and the names skipped.
func ExtProducers() (found []ExtProducer, skipped []string) {
	if _, err := exec.LookPath("python3"); err == nil {
		ver := firstLine(runPipe(nil, "python3", "-I", "-c", "import zlib;print('zlib',zlib.ZLIB_RUNTIME_VERSION)"))
		if ver != "" {
			// level, wbits, memLevel, strategy (0 default, 1 filtered, 2 huffman-only, 3 rle, 4 fixed)
			for _, c := range []struct {
				name      string
				l, m, s   int
				wbits     int
				format    string
				extension string
			}{
				{"zlib-py-L1", 1, 8, 0, -15, "deflate", ".deflate"},
				{"zlib-py-L6", 6, 8, 0, -15, "deflate", ".deflate"},
				{"zlib-py-L9", 9, 8, 0, -15, "deflate", ".deflate"},
				{"zlib-py-L6-mem1", 6, 1, 0, -15, "deflate", ".deflate"},
				{"zlib-py-L6-mem9", 6, 9, 0, -15, "deflate", ".deflate"},
				{"zlib-py-L6-filtered", 6, 8, 1, -15, "deflate", ".deflate"},
				{"zlib-py-L6-rle", 6, 8, 3, -15, "deflate", ".deflate"},
				{"zlib-py-L6-fixed", 6, 8, 4, -15, "deflate", ".deflate"},
				{"zlib-py-gz-L6", 6, 8, 0, 31, "gzip", ".gz"},
			} {
				c := c
				args := []string{"-I", "-c", pyZlib, fmt.Sprint(c.l), fmt.Sprint(c.wbits), fmt.Sprint(c.m), fmt.Sprint(c.s)}
				found = append(found, ExtProducer{c.name, c.format, c.extension, "python3 " + ver, func(p []byte) ([]byte, error) {
					return runPipe(p, "python3", args...)
				}})
			}
		}
	} else {
		skipped = append(skipped, "zlib-py-* (python3 not found)")
	}
	if _, err := exec.LookPath("gzip"); err == nil {
		ver := "gzip " + firstLine(runPipe(nil, "gzip", "--version"))
		for _, l := range []string{"1", "6", "9"} {
			l := l
			found = append(found, ExtProducer{"gnu-gzip-L" + l, "gzip", ".gz", ver, func(p []byte) ([]byte, error) {
				return runPipe(p, "gzip", "-n", "-"+l, "-c")
			}})
		}
	} else {
		skipped = append(skipped, "gnu-gzip-* (gzip not found)")
	}
	if _, err := exec.LookPath("zip"); err == nil {
		ver := "zip " + firstLine(runPipe(nil, "sh", "-c", "zip -v | sed -n 2p"))
		found = append(found, ExtProducer{"info-zip-L6", "zip", ".zip", ver, func(p []byte) ([]byte, error) {
			dir, err := os.MkdirTemp("", "z3ext-zip-")
			if err != nil {
				return nil, err
			}
			defer os.RemoveAll(dir)
			f := filepath.Join(dir, "data.bin")
			if err := os.WriteFile(f, p, 0o644); err != nil {
				return nil, err
			}
			fixed := time.Date(2024, 1, 2, 3, 4, 6, 0, time.UTC)
			os.Chtimes(f, fixed, fixed)
			cmd := exec.Command("zip", "-X", "-q", "-6", "out.zip", "data.bin")
			cmd.Dir = dir
			if b, err := cmd.CombinedOutput(); err != nil {
				return nil, fmt.Errorf("zip: %v: %s", err, b)
			}
			return os.ReadFile(filepath.Join(dir, "out.zip"))
		}})
	} else {
		skipped = append(skipped, "info-zip-* (zip not found)")
	}
	found, skipped = appendExtraProducers(found, skipped)
	return found, skipped
}

// GenerateExt writes the external-producer corpus into dir. It returns the
// manifest and the names of producers that were unavailable.
func GenerateExt(cfg Config, dir string) (*Manifest, []string, error) {
	if err := cfg.Validate(); err != nil {
		return nil, nil, err
	}
	prods, skipped := ExtProducers()
	m := &Manifest{Schema: SchemaVersion, GeneratorVersion: GeneratorVersion, GoVersion: runtime.Version(), Config: cfg}
	workloads := []struct{ name, kind, mut string }{
		{"text-insert-mid", "text", "insert-mid"},
		{"text-insert-begin", "text", "insert-begin"},
		{"repeat-replace", "repeat", "replace"},
	}
	sink := DiskSink{dir}
	for _, p := range prods {
		for _, w := range workloads {
			name := "ext-" + p.Name + "-" + w.name
			fam := &Family{Name: name}
			ctx := &Ctx{Cfg: cfg, Fam: fam, size: cfg.SizeKiB * 1024}
			prev := ""
			err := plainChain(ctx, w.kind, w.mut, func(i int, plain []byte, mutation string) error {
				enc, err := p.run(plain)
				if err != nil {
					return err
				}
				rev := fmt.Sprintf("r%d", i)
				sum := sha256.Sum256(enc)
				mem := Member{Family: name, Format: p.Format, Workload: "deflate-ext", Revision: rev, Index: i, Mutation: mutation,
					Path: name + "/" + rev + p.Ext, Size: int64(len(enc)), SHA256: hex.EncodeToString(sum[:]), Parent: prev, Base: "r0",
					ToolDependent: true,
					Truth:         plainTruth(plain, map[string]string{"producer": p.Name, "producer_version": p.Version, "corpus": "optional-external"})}
				if i == 0 {
					mem.Base = ""
				}
				prev = rev
				m.Members = append(m.Members, mem)
				return sink.Put(mem, enc)
			})
			if err != nil {
				return nil, skipped, fmt.Errorf("corpus: ext family %s: %w", name, err)
			}
		}
	}
	b, _ := json.MarshalIndent(m, "", " ")
	return m, skipped, os.WriteFile(filepath.Join(dir, manifestName), append(b, '\n'), 0o644)
}
