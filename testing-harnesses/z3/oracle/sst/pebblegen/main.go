// Command pebblegen is the Z3-30 Pebble producer driver (a nested module: Pebble is a
// producer here, never a dependency of the Z3 module). It replays the operation logs and
// plan written by `z3sst gen-ops` against Pebble and snapshots the live tables.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/bloom"
)

const (
	file  = 4 << 20
	block = 4096
)

type step map[string]any
type fam struct {
	Name  string           `json:"name"`
	Steps []step           `json:"steps"`
	Revs  []map[string]any `json:"revs"`
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

var codec = pebble.SnappyCompression

func options(scale float64) *pebble.Options {
	o := &pebble.Options{FormatMajorVersion: pebble.FormatNewest, DisableAutomaticCompactions: true, MemTableSize: 32 << 20, Levels: make([]pebble.LevelOptions, 7)}
	for i := range o.Levels {
		o.Levels[i].BlockSize = block
		o.Levels[i].TargetFileSize = int64(float64(file) * scale)
		o.Levels[i].FilterPolicy = bloom.FilterPolicy(10)
		o.Levels[i].Compression = codec
	}
	return o
}

func readOps(path string, fn func(op byte, k, v []byte)) {
	b, err := os.ReadFile(path)
	must(err)
	i := 0
	uv := func() int { v, n := binary.Uvarint(b[i:]); i += n; return int(v) }
	for i < len(b) {
		op := b[i]
		i++
		kl := uv()
		k := b[i : i+kl]
		i += kl
		var v []byte
		if op == 1 {
			vl := uv()
			v = b[i : i+vl]
			i += vl
		}
		fn(op, k, v)
	}
}

func copyFile(src, dst string) {
	in, err := os.Open(src)
	must(err)
	defer in.Close()
	out, err := os.Create(dst)
	must(err)
	_, err = io.Copy(out, in)
	must(err)
	must(out.Close())
}

func sha(p string) string {
	f, err := os.Open(p)
	must(err)
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	return fmt.Sprintf("%x", h.Sum(nil))
}

func main() {
	opsdir, outroot, workroot := os.Args[1], os.Args[2], os.Args[3]
	only := ""
	if len(os.Args) > 4 {
		only = os.Args[4] // run one family
	}
	var plan struct {
		Families []fam `json:"families"`
	}
	b, err := os.ReadFile(filepath.Join(opsdir, "plan.json"))
	must(err)
	must(json.Unmarshal(b, &plan))
	for _, f := range plan.Families {
		if only != "" && f.Name != only {
			continue
		}
		work := filepath.Join(workroot, "pebble", f.Name)
		os.RemoveAll(work)
		must(os.MkdirAll(work, 0o755))
		path, basepath := filepath.Join(work, "db"), filepath.Join(work, "base")
		out := filepath.Join(outroot, "pebble", f.Name)
		os.RemoveAll(out)
		must(os.MkdirAll(out, 0o755))
		var db *pebble.DB
		open := func(s float64) {
			db, err = pebble.Open(path, options(s))
			must(err)
		}
		closeDB := func() {
			if db != nil {
				must(db.Close())
				db = nil
			}
		}
		files := map[string][]map[string]any{}
		skip := false
		for _, st := range f.Steps {
			if skip && st["c"].(string) != "newdb" {
				continue
			}
			switch st["c"].(string) {
			case "newdb":
				closeDB()
				os.RemoveAll(path)
				codec = pebble.SnappyCompression
				if c, ok := st["codec"].(string); ok {
					switch c {
					case "none":
						codec = pebble.NoCompression
					case "zstd":
						codec = pebble.ZstdCompression
					default:
						skip = true
						continue
					}
				}
				skip = false
				s := 1.0
				if v, ok := st["scale"]; ok {
					s = v.(float64)
				}
				open(s)
			case "apply":
				batch := int(st["batch"].(float64))
				bt := db.NewBatch()
				n := 0
				readOps(filepath.Join(opsdir, st["ops"].(string)), func(op byte, k, v []byte) {
					if op == 1 {
						must(bt.Set(k, v, nil))
					} else {
						must(bt.Delete(k, nil))
					}
					n++
					if n%batch == 0 {
						must(bt.Commit(pebble.NoSync))
						bt = db.NewBatch()
					}
				})
				must(bt.Commit(pebble.NoSync))
			case "flush":
				must(db.Flush())
			case "compact", "autocompact":
				must(db.Flush())
				must(db.Compact([]byte{0}, []byte{0xff, 0xff, 0xff, 0xff}, true))
			case "reopen":
				closeDB()
				open(st["scale"].(float64))
			case "savebase":
				closeDB()
				os.RemoveAll(basepath)
				must(exec("cp", "-a", path, basepath))
				open(1)
			case "frombase":
				closeDB()
				os.RemoveAll(path)
				must(exec("cp", "-a", basepath, path))
				open(1)
			case "snapshot":
				rev := st["rev"].(string)
				dst := filepath.Join(out, rev)
				must(os.MkdirAll(dst, 0o755))
				ck := filepath.Join(work, "ck")
				os.RemoveAll(ck)
				must(db.Checkpoint(ck, pebble.WithFlushedWAL()))
				ents, _ := os.ReadDir(ck)
				var tabs []map[string]any
				for _, e := range ents {
					if strings.HasSuffix(e.Name(), ".sst") {
						copyFile(filepath.Join(ck, e.Name()), filepath.Join(dst, e.Name()))
					} else if d, _ := st["dir"].(bool); d {
						must(os.MkdirAll(filepath.Join(dst, "_other"), 0o755))
						copyFile(filepath.Join(ck, e.Name()), filepath.Join(dst, "_other", e.Name()))
					}
				}
				names, _ := filepath.Glob(filepath.Join(dst, "*.sst"))
				sort.Strings(names)
				for _, n := range names {
					fi, _ := os.Stat(n)
					tabs = append(tabs, map[string]any{"name": filepath.Base(n), "size": fi.Size(), "sha256": sha(n)})
				}
				files[rev] = tabs
				os.RemoveAll(ck)
			default:
				panic("unknown step")
			}
		}
		closeDB()
		revs := make([]map[string]any, 0, len(f.Revs))
		for _, r := range f.Revs {
			r["files"] = files[r["Rev"].(string)]
			revs = append(revs, r)
		}
		meta := map[string]any{"lane": "sst", "family": f.Name, "revisions": revs, "producer": map[string]any{
			"producer": "Pebble", "version": "v1.1.5", "binding": "native Go", "options": map[string]any{
				"format_major_version": "FormatNewest (table format Pebblev4)", "target_file_size": file, "block_size": block, "compression": "snappy", "bloom_filter_bits": 10, "auto_compactions": false}}}
		mb, _ := json.MarshalIndent(meta, "", " ")
		must(os.WriteFile(filepath.Join(out, "meta.json"), mb, 0o644))
		fmt.Println("pebble", f.Name, "done")
	}
}
