//go:build b10bench && b6bench && b5locality && b1rangeexperiment

package main

// B10 deep-stream campaign (not a regression test): go test -tags b10bench,b6bench,b5locality,b1rangeexperiment.
// Environment as B6-B9 (ZEROS3_B6_FX, ZEROS3_B6_CORPUS, ZEROS3_B6_OUT, ZEROS3_B6_FAMILIES, ZEROS3_B6_LANES).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// b10Families are the pre-registered B10 families (testing-harnesses/next/b10/PREREGISTRATION.md).
func b10Families(c string) []b6Family {
	fam := func(name string, puts ...b6Put) b6Family {
		f := b6Family{name: name}
		for _, p := range puts {
			f.puts = append(f.puts, b6Put{p.key, filepath.Join(c, p.file)})
		}
		return f
	}
	hist := func(name, key, pat string, vs ...string) b6Family {
		f := b6Family{name: name}
		for _, v := range vs {
			f.puts = append(f.puts, b6Put{key, filepath.Join(c, fmt.Sprintf(pat, v))})
		}
		return f
	}
	mv := b6Family{name: "maven"}
	for _, v := range []string{"3.9.6", "3.9.7", "3.9.8", "3.9.9"} {
		mv.puts = append(mv.puts, b6Put{"maven.tar.gz", filepath.Join(c, "maven/apache-maven-"+v+"-bin.tar.gz")}, b6Put{"maven.zip", filepath.Join(c, "maven/apache-maven-"+v+"-bin.zip")})
	}
	v4 := []string{"0", "1", "2", "3"}
	return []b6Family{
		hist("gems-rubocop", "rubocop.gem", "gems/rubocop-%s.gem", "1.66.0", "1.66.1", "1.67.0", "1.68.0", "1.69.0"),
		hist("gems-ar", "activerecord.gem", "gems/activerecord-%s.gem", "7.2.0", "7.2.1", "7.2.2", "8.0.0"),
		mv,
		hist("oci-python", "python-layer.tar.gz", "oci/python-%s-slim.layer2.tar.gz", "3.12.6", "3.12.7", "3.12.8"),
		hist("pq-snappy", "co2.parquet", "b10/pq-snappy/v%s.parquet", v4...),
		hist("pq-zstd", "co2.parquet", "b10/pq-zstd/v%s.parquet", v4...),
		hist("pq-lz4", "co2.parquet", "b10/pq-lz4/v%s.parquet", v4...),
		hist("sst-snappy", "co2.sst", "b10/sst-snappy/v%s.sst", v4...),
		hist("sst-lz4", "co2.sst", "b10/sst-lz4/v%s.sst", v4...),
		hist("ts", "typescript.tgz", "ts/typescript-%s.tgz", "5.6.2", "5.6.3", "5.7.2"),
		hist("ctl-rnd", "r.tar", "b10/ctl/rnd%s.tar", "0", "1", "2"),
		fam("ctl-unrelated", b6Put{"rake.gem", "ctlgems/rake-13.2.1.gem"}, b6Put{"rack.gem", "ctlgems/rack-3.1.8.gem"}, b6Put{"thor.gem", "ctlgems/thor-1.3.2.gem"},
			b6Put{"puma.gem", "ctlgems/puma-6.4.3.gem"}, b6Put{"nokogiri.gem", "ctlgems/nokogiri-1.16.7.gem"}),
		fam("ctl-trunc", b6Put{"co2.parquet", "b10/ctl/trunc.parquet"}, b6Put{"co2.parquet", "b10/pq-snappy/v3.parquet"}),
	}
}

// b10Lanes: B9 (canonical, grouped records), B9 with every existing replay and structured kind (b9x), B10 and
// its ablations; read-priced profiles for b9x and b10.
func b10Lanes() []b6Lane {
	np := AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10}
	op := AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10}
	run := func(p OptimizePolicy, cfg string) func(context.Context, *Store) (any, error) {
		return func(ctx context.Context, s *Store) (any, error) {
			old := agWanted
			agWanted = true
			defer func() { agWanted = old }()
			p.Apply, p.AllowDelta, p.AllowRep, p.Canonical = true, true, true, true
			if cfg != "b9" {
				p.CodecReplay, p.HardCodecs = true, true
			}
			if strings.HasPrefix(cfg, "b10") {
				p.Splice = true
				p.spliceNoRep, p.spliceNoPage, p.spliceNoDelta, p.spliceAll = cfg == "b10-nonest", cfg == "b10-nopage", cfg == "b10-nodelta", cfg == "b10-all"
			}
			return s.AuthorityOptimize(ctx, p)
		}
	}
	lanes := []b6Lane{{"cdc", nil, ""}}
	for _, cfg := range []string{"b9", "b9x", "b10", "b10-nonest", "b10-nopage", "b10-nodelta", "b10-all"} {
		lanes = append(lanes, b6Lane{cfg + "-archive", run(OptimizePolicy{AuthPolicy: op, Profile: "archive"}, cfg), ""})
	}
	for _, cfg := range []string{"b9x", "b10"} {
		lanes = append(lanes, b6Lane{cfg + "-mixed", run(OptimizePolicy{AuthPolicy: np, Profile: "mixed"}, cfg), ""},
			b6Lane{cfg + "-hot", run(OptimizePolicy{AuthPolicy: np, Profile: "hot"}, cfg), ""})
	}
	return lanes
}

func b10Campaign(t *testing.T, test func(*testing.T)) {
	b6FamilySet, b6LaneSet = b10Families, b10Lanes
	defer func() { b6FamilySet, b6LaneSet = b6Families, b6Lanes }()
	test(t)
}

func TestB10Build(t *testing.T)      { b10Campaign(t, TestB6Build) }
func TestB10Lanes(t *testing.T)      { b10Campaign(t, TestB6Lanes) }
func TestB10Reads(t *testing.T)      { b10Campaign(t, TestB6Reads) }
func TestB10Concurrent(t *testing.T) { b10Campaign(t, TestB6Concurrent) }

// TestB10Probe: per-file deep-stream analysis of B10_FILES (units, refusals, deep-stream and recipe bytes).
func TestB10Probe(t *testing.T) {
	l := newAuthLab(t)
	for _, p := range strings.Fields(os.Getenv("B10_FILES")) {
		data, err := os.ReadFile(p)
		mustOK(t, err)
		key := fmt.Sprintf("probe/%x", len(l.want))
		l.put(key, data)
		mem := map[[32]byte][]byte{}
		st := &SpliceStats{}
		t0 := time.Now()
		root, canon, err := l.s.spliceConstruct(l.man(key), bytes.NewReader(data), 0, mem, splOpts{hard: true}, st)
		sec := time.Since(t0).Seconds()
		row := map[string]any{"file": p, "size": len(data), "seconds": sec, "stats": st}
		if err != nil {
			row["err"] = err.Error()
		} else {
			cl, _ := l.s.repClosureMem(root, mem)
			var rb int64
			for _, c := range cl.chunks {
				rb += c.Length
			}
			row["recipe_bytes"], row["deep"] = rb, canon.Size
			canon.close()
		}
		b, _ := json.Marshal(row)
		fmt.Println(string(b))
	}
}

// TestB10Cache: repeated and nearby 4 KiB reads of a lane store's largest objects, decoded-window cache off
// and at 64 MiB (both orders); every read is compared with the reference bytes.
func TestB10Cache(t *testing.T) {
	fx := os.Getenv("ZEROS3_B6_FX")
	lane := os.Getenv("ZEROS3_B10_LANE")
	if lane == "" {
		lane = "b10-archive"
	}
	for _, f := range b10Families(os.Getenv("ZEROS3_B6_CORPUS")) {
		if !b6Want("ZEROS3_B6_FAMILIES", f.name) {
			continue
		}
		dir := filepath.Join(fx, f.name, lane)
		if _, err := os.Stat(filepath.Join(dir, ".b6-ready")); err != nil {
			continue
		}
		s, done := b6Open(t, dir)
		objs := b6Objects(t, s, 4)
		for i := range objs {
			d, err := b6Read(s, objs[i].man, 0, objs[i].man.TotalLength)
			mustOK(t, err)
			objs[i].data = d
		}
		for _, order := range [][2]int64{{0, 64 << 20}, {64 << 20, 0}} {
			for _, budget := range order {
				for _, wl := range []string{"repeated", "nearby"} {
					s.SetWindowCache(budget)
					b5Purge(s)
					t0, n := time.Now(), 0
					for rep := range 6 {
						for _, o := range objs {
							size := o.man.TotalLength
							for k := range 16 {
								off := (int64(k)*7919*4096 + size/3) % max(1, size-4096)
								if wl == "nearby" {
									off = (size/3 + int64(rep*16+k)*4096) % max(1, size-4096)
								}
								got, err := b6Read(s, o.man, off, min(4096, size-off))
								if err != nil || !bytes.Equal(got, o.data[off:off+min(4096, size-off)]) {
									t.Fatalf("%s %s at %d: %v", f.name, wl, off, err)
								}
								n++
							}
						}
					}
					sec := time.Since(t0).Seconds()
					b6Emit(map[string]any{"kind": "cache", "family": f.name, "lane": lane, "workload": wl, "budget": budget, "reads": n, "seconds": sec, "reads_per_s": float64(n) / sec})
				}
			}
		}
		s.SetWindowCache(0)
		done()
	}
}
