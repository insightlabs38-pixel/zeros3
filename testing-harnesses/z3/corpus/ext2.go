package corpus

import (
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// Z3-08B additional producers. Everything is optional and detected, never
// assumed:
//
//	Z3_PYTHON  python3 that can import zlib_ng, isal, deflate (libdeflate) and/or zopfli
//	           (oracle/producers/setup.sh builds a venv outside Git)
//	Z3_MINIZ   z3miniz binary (miniz_oxide) built by oracle/producers/setup.sh
//
// Go's compress/flate is always available in process.

const pyExtra = `import sys
mod, lvl = sys.argv[1], int(sys.argv[2])
data = sys.stdin.buffer.read()
if mod == "zlib_ng":
    from zlib_ng import zlib_ng as z
    c = z.compressobj(lvl, 8, -15, 8, 0); out = c.compress(data) + c.flush()
elif mod == "isal":
    from isal import isal_zlib as z
    c = z.compressobj(lvl, 8, -15); out = c.compress(data) + c.flush()
elif mod == "libdeflate":
    import deflate
    out = deflate.deflate_compress(data, lvl)
elif mod == "zopfli":
    import zopfli.zlib as z
    out = z.compress(data, numiterations=lvl)[2:-4]  # strip the zlib wrapper
sys.stdout.buffer.write(out)`

func appendExtraProducers(found []ExtProducer, skipped []string) ([]ExtProducer, []string) {
	for _, lv := range []int{1, 5, 9} {
		lv := lv
		found = append(found, ExtProducer{fmt.Sprintf("go-flate-L%d", lv), "deflate", ".deflate", "Go compress/flate (in process)", func(p []byte) ([]byte, error) {
			var b bytes.Buffer
			w, err := flate.NewWriter(&b, lv)
			if err != nil {
				return nil, err
			}
			w.Write(p)
			w.Close()
			return b.Bytes(), nil
		}})
	}
	py := os.Getenv("Z3_PYTHON")
	if py == "" {
		skipped = append(skipped, "zlib-ng/libdeflate/isa-l/zopfli (Z3_PYTHON not set)")
	} else {
		type spec struct {
			prefix, mod string
			levels      []int
		}
		for _, sp := range []spec{
			{"zlib-ng", "zlib_ng", []int{1, 3, 6, 9}},
			{"libdeflate", "libdeflate", []int{1, 6, 9, 12}},
			{"isal", "isal", []int{0, 1, 2, 3}},
			{"zopfli", "zopfli", []int{5}},
		} {
			ver := firstLine(runPipe(nil, py, "-I", "-c", fmt.Sprintf("import importlib.metadata as m;print(m.version(%q))", map[string]string{"zlib_ng": "zlib-ng", "isal": "isal", "libdeflate": "deflate", "zopfli": "zopfli"}[sp.mod])))
			if ver == "" {
				skipped = append(skipped, sp.prefix+" (python module missing)")
				continue
			}
			for _, lv := range sp.levels {
				sp, lv := sp, lv
				name := fmt.Sprintf("%s-L%d", sp.prefix, lv)
				found = append(found, ExtProducer{name, "deflate", ".deflate", sp.prefix + " " + strings.TrimSpace(ver) + " (python)", func(p []byte) ([]byte, error) {
					return runPipe(p, py, "-I", "-c", pyExtra, sp.mod, fmt.Sprint(lv))
				}})
			}
		}
	}
	if mz := os.Getenv("Z3_MINIZ"); mz != "" {
		if _, err := exec.LookPath(mz); err == nil {
			for _, lv := range []int{1, 6, 9} {
				lv := lv
				found = append(found, ExtProducer{fmt.Sprintf("miniz-oxide-L%d", lv), "deflate", ".deflate", "miniz_oxide 0.8.0", func(p []byte) ([]byte, error) {
					return runPipe(p, mz, fmt.Sprint(lv))
				}})
			}
		} else {
			skipped = append(skipped, "miniz-oxide (Z3_MINIZ not executable)")
		}
	} else {
		skipped = append(skipped, "miniz-oxide (Z3_MINIZ not set)")
	}
	return found, skipped
}

// GenerateExtReal recompresses a chain of *real* plaintext revisions (for
// example consecutive release tarballs, decompressed) with every available
// producer whose name matches one of the globs. Each plaintext is truncated to
// maxBytes, so revision i is the first maxBytes of plains[i]. Families are named
// "extreal-<producer>-<label>"; the manifest is written to dir.
func GenerateExtReal(dir, label string, plains []string, maxBytes int, globs []string) (*Manifest, []string, error) {
	prods, skipped := ExtProducers()
	m := &Manifest{Schema: SchemaVersion, GeneratorVersion: GeneratorVersion, GoVersion: "extreal", Config: DefaultConfig()}
	if old, err := ReadManifest(dir); err == nil { // several labels may share one corpus directory
		m = old
	}
	sink := DiskSink{dir}
	for _, p := range prods {
		if p.Format != "deflate" && p.Format != "gzip" {
			continue
		}
		if len(globs) > 0 && !matchGlobs(globs, p.Name) {
			continue
		}
		name := "extreal-" + p.Name + "-" + label
		prev := ""
		for i, pf := range plains {
			f, err := os.Open(pf)
			if err != nil {
				return nil, skipped, err
			}
			plain := make([]byte, maxBytes)
			n, _ := io.ReadFull(f, plain)
			f.Close()
			plain = plain[:n]
			enc, err := p.run(plain)
			if err != nil {
				return nil, skipped, fmt.Errorf("%s: %w", name, err)
			}
			rev := fmt.Sprintf("r%d", i)
			sum := sha256.Sum256(enc)
			mem := Member{Family: name, Format: p.Format, Workload: "deflate-extreal", Revision: rev, Index: i, Mutation: filepath.Base(pf),
				Path: name + "/" + rev + p.Ext, Size: int64(len(enc)), SHA256: hex.EncodeToString(sum[:]), Parent: prev, ToolDependent: true,
				Truth: map[string][]string{"producer": {p.Name}, "producer_version": {p.Version}, "plaintext": {pf}, "corpus": {"optional-external-real"}}}
			if i > 0 {
				mem.Base = "r0"
			}
			prev = rev
			m.Members = append(m.Members, mem)
			if err := sink.Put(mem, enc); err != nil {
				return nil, skipped, err
			}
		}
	}
	b, _ := json.MarshalIndent(m, "", " ")
	return m, skipped, os.WriteFile(filepath.Join(dir, manifestName), append(b, '\n'), 0o644)
}

func matchGlobs(globs []string, name string) bool {
	for _, g := range globs {
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	return false
}
