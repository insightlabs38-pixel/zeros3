// Command z4p5bench measures the Z4-P5 matcher, estimator, index and window
// codec on real corpora and synthetic scale inputs. Output is JSON lines.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"zeros3-z3/dcore"
	"zeros3-z3/simdelta"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func emit(v map[string]any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

type family struct {
	Name  string
	Files []string
}

func families(root string) []family {
	g := func(pats ...string) []string {
		var out []string
		for _, p := range pats {
			m, _ := filepath.Glob(filepath.Join(root, p))
			sort.Strings(m)
			out = append(out, m...)
		}
		return out
	}
	return []family{
		{"tar", g("econ/src/coreutils-*.tar")},
		{"clr", g("q6/clr/*.dll")},
		{"dex-classes", g("econ/dex/*/classes.dex")},
		{"dex-classes2", g("econ/dex/*/classes2.dex")},
		{"parquet-zstd", g("parquet/zstd/c*.parquet")},
		{"parquet-snappy", g("parquet/snappy/c*.parquet")},
		{"sqlite", g("sqlite/v*.db")},
		{"git-pack", g("git/h*.pack")},
		{"safetensors", g("q6/pythia-14m/*.safetensors")},
	}
}

func read(p string, limit int) []byte {
	b, err := os.ReadFile(p)
	must(err)
	if limit > 0 && len(b) > limit {
		b = b[:limit]
	}
	return b
}

func refToOps(ref []simdelta.RefOp) []dcore.Op {
	var out []dcore.Op
	var pos int64
	for _, o := range ref {
		op := dcore.Op{TPos: pos, N: int64(o.N), Base: o.Base, Off: int64(o.Off), Lit: o.Lit}
		if o.Lit != nil {
			op.Base, op.N = -1, int64(len(o.Lit))
		}
		pos += op.N
		out = append(out, op)
	}
	return out
}

func timeBest(n int, f func()) float64 {
	best := 1e18
	for i := 0; i < n; i++ {
		t := time.Now()
		f()
		if d := float64(time.Since(t).Nanoseconds()); d < best {
			best = d
		}
	}
	return best / 1e9
}

func summarize(ops []dcore.Op) (copies int, copyB, litB int64) {
	for _, o := range ops {
		if o.Base < 0 {
			litB += o.N
		} else {
			copies++
			copyB += o.N
		}
	}
	return
}

// cmdMatch compares the unmodified B1 matcher (16 MiB-bounded) with dcore.
func cmdMatch(root string, only string, reps int) {
	const lim = 16 << 20
	for _, f := range families(root) {
		if only != "" && !strings.Contains(","+only+",", ","+f.Name+",") {
			continue
		}
		for i := 0; i+1 < len(f.Files); i++ {
			bb, tt := read(f.Files[i], lim), read(f.Files[i+1], lim)
			row := func(name string, secs float64, ops []dcore.Op, extra map[string]any) {
				c, cb, lb := summarize(ops)
				r := map[string]any{"mode": "match", "family": f.Name, "pair": i, "base": len(bb), "target": len(tt), "matcher": name,
					"sec": secs, "mib_s": float64(len(tt)) / secs / (1 << 20), "copies": c, "copy_bytes": cb, "lit_bytes": lb,
					"envelope": dcore.EnvelopeSize(ops, 128<<10), "bounded_16mib": true}
				for k, v := range extra {
					r[k] = v
				}
				emit(r)
			}
			for _, alg := range []string{"adaptive", "git", "rolling"} {
				var ref []simdelta.RefOp
				secs := timeBest(reps, func() { ref = simdelta.MatchRef(alg, [][]byte{bb}, tt) })
				row("b1-"+alg, secs, refToOps(ref), nil)
			}
			for _, cfg := range [][2]int{{1 << 18, 0}, {1 << 20, 0}, {1 << 22, 0}, {1 << 20, 2}, {1 << 20, 3}, {1 << 20, 4}, {1 << 22, 3}} {
				ma := cfg[0]
				p := dcore.DefaultParams()
				p.MaxAnchors, p.MinSampleBits = ma, cfg[1]
				var ops []dcore.Op
				var ibuild float64
				secs := timeBest(reps, func() {
					t0 := time.Now()
					x, err := dcore.BuildIndex([][]byte{bb}, p)
					must(err)
					ibuild = time.Since(t0).Seconds()
					ops = x.Match(tt)
				})
				row(fmt.Sprintf("dcore-a%d-f%d", ma, cfg[1]), secs, ops, map[string]any{"index_build_sec": ibuild})
			}
		}
	}
}

// cmdFull runs dcore on whole files (no 16 MiB bound).
func cmdFull(root, only string, reps int) {
	for _, f := range families(root) {
		if only != "" && !strings.Contains(","+only+",", ","+f.Name+",") {
			continue
		}
		for i := 0; i+1 < len(f.Files); i++ {
			bb, tt := read(f.Files[i], 0), read(f.Files[i+1], 0)
			var ops []dcore.Op
			secs := timeBest(reps, func() {
				x, err := dcore.BuildIndex([][]byte{bb}, dcore.DefaultParams())
				must(err)
				ops = x.Match(tt)
			})
			c, cb, lb := summarize(ops)
			emit(map[string]any{"mode": "full", "family": f.Name, "pair": i, "base": len(bb), "target": len(tt), "sec": secs, "mib_s": float64(len(tt)+len(bb)) / secs / (1 << 20),
				"copies": c, "copy_bytes": cb, "lit_bytes": lb, "envelope": dcore.EnvelopeSize(ops, 128<<10)})
		}
	}
}

func rss() float64 {
	b, _ := os.ReadFile("/proc/self/status")
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM:") {
			var kb float64
			fmt.Sscan(strings.Fields(l)[1], &kb)
			return kb / 1024
		}
	}
	return 0
}

func main() {
	mode := flag.String("mode", "match", "match|full|select|index|window|runs")
	root := flag.String("corpus", "/home/user/p5corpus", "corpus root")
	only := flag.String("families", "", "comma list")
	reps := flag.Int("reps", 3, "timing repetitions")
	flag.Parse()
	runtime.GOMAXPROCS(runtime.NumCPU())
	switch *mode {
	case "match":
		cmdMatch(*root, *only, *reps)
	case "full":
		cmdFull(*root, *only, *reps)
	case "select":
		cmdSelect(*root, *only)
	default:
		if !runExtra(*mode, flag.Args(), *root) {
			fmt.Fprintln(os.Stderr, "unknown mode")
			os.Exit(2)
		}
	}
	fmt.Fprintf(os.Stderr, "peak_rss_mib=%.1f\n", rss())
}
