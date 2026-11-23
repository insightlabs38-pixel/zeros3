//go:build b8bench && b6bench && b5locality && b1rangeexperiment

package main

// B8 cross-format canonical campaign (not a regression test): go test -tags b8bench,b6bench,b5locality,b1rangeexperiment.
// Environment as B6/B7 (ZEROS3_B6_FX, ZEROS3_B6_CORPUS, ZEROS3_B6_OUT, ZEROS3_B6_FAMILIES, ZEROS3_B6_LANES).

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// b8Families are the pre-registered B8 families (testing-harnesses/next/b8/PREREGISTRATION.md).
func b8Families(c string) []b6Family {
	hist := func(name, key string, files ...string) b6Family {
		f := b6Family{name: name}
		for _, p := range files {
			f.puts = append(f.puts, b6Put{key, filepath.Join(c, p)})
		}
		return f
	}
	dj := b6Family{name: "django"}
	for _, v := range []string{"5.1.1", "5.1.2", "5.1.3"} {
		dj.puts = append(dj.puts, b6Put{"django.tar.gz", filepath.Join(c, "django/Django-"+v+".tar.gz")},
			b6Put{"django.whl", filepath.Join(c, "django/Django-"+v+"-py3-none-any.whl")})
	}
	mix := func(name string, puts ...b6Put) b6Family {
		f := b6Family{name: name}
		for _, p := range puts {
			f.puts = append(f.puts, b6Put{p.key, filepath.Join(c, p.file)})
		}
		return f
	}
	return []b6Family{
		hist("ts", "typescript.tgz", "ts/typescript-5.6.2.tgz", "ts/typescript-5.6.3.tgz", "ts/typescript-5.7.2.tgz"),
		dj,
		hist("cu-gz", "coreutils.tar.gz", "src/coreutils-9.5.tar.gz", "src/coreutils-9.6.tar.gz", "src/coreutils-9.7.tar.gz"),
		mix("wrap", b6Put{"cu/9.7.tar.gz", "src/coreutils-9.7.tar.gz"}, b6Put{"cu/9.7.tar", "src/coreutils-9.7.tar"},
			b6Put{"cu/9.7.tar.zst", "b8/cu-9.7.l3.tar.zst"}, b6Put{"cu/9.6.tar.zst", "b8/cu-9.6.l3.tar.zst"}),
		mix("ts-mix", b6Put{"ts", "ts/typescript-5.6.2.tgz"}, b6Put{"ts", "b8/ts-5.6.3.tar"}, b6Put{"ts", "b8/ts-5.7.2.l3.tar.zst"}),
		hist("random", "r.bin", "random/r0.bin", "random/r1.bin", "random/r2.bin"),
		mix("lone", b6Put{"requests.tar.gz", "lone/requests-2.32.3.tar.gz"}, b6Put{"requests.whl", "lone/requests-2.32.3-py3-none-any.whl"},
			b6Put{"coreutils.tar.xz", "lone/coreutils-9.5.tar.xz"}),
		hist("zst19", "cu.tar.zst", "b8/cu-9.6.l19.tar.zst", "b8/cu-9.7.l19.tar.zst"),
	}
}

// b8Lanes: B7 as committed, B7 with existing codec replay eligible.
func b8Lanes() []b6Lane {
	np := AuthPolicy{Order: "newest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10}
	op := AuthPolicy{Order: "oldest", MaxDepth: 2, MaxBases: 2, MinSaving: 0.10}
	// cfg: b7 (as committed), b7r (+ codec replay), b7rg (+ B8 search and pricing), b8s (canonical, B7 search), b8
	run := func(p OptimizePolicy, cfg string) func(context.Context, *Store) (any, error) {
		return func(ctx context.Context, s *Store) (any, error) {
			p.Apply, p.AllowDelta, p.AllowRep = true, true, true
			switch cfg {
			case "b7", "b7r":
				p.B7Search, p.CodecReplay = true, cfg == "b7r"
			case "b7rg":
				p.CodecReplay = true
			case "b8s":
				p.Canonical, p.B7Search = true, true
			case "b8":
				p.Canonical = true
			case "b8-nodelta":
				p.Canonical, p.canonNoDelta = true, true
			case "b8-nozip":
				p.Canonical, p.noZip = true, true
			}
			return s.AuthorityOptimize(ctx, p)
		}
	}
	lanes := []b6Lane{{"cdc", nil, ""}}
	for _, cfg := range []string{"b7", "b7r", "b7rg", "b8s", "b8", "b8-nodelta", "b8-nozip"} {
		lanes = append(lanes, b6Lane{cfg + "-archive", run(OptimizePolicy{AuthPolicy: op, Profile: "archive"}, cfg), ""},
			b6Lane{cfg + "-mixed", run(OptimizePolicy{AuthPolicy: np, Profile: "mixed"}, cfg), ""},
			b6Lane{cfg + "-hot", run(OptimizePolicy{AuthPolicy: np, Profile: "hot"}, cfg), ""})
	}
	return lanes
}

func b8Campaign(t *testing.T, test func(*testing.T)) {
	b6FamilySet, b6LaneSet = b8Families, b8Lanes
	defer func() { b6FamilySet, b6LaneSet = b6Families, b6Lanes }()
	test(t)
}

func TestB8Build(t *testing.T) { b8Campaign(t, TestB6Build) }
func TestB8Lanes(t *testing.T) { b8Campaign(t, TestB6Lanes) }
func TestB8Reads(t *testing.T) { b8Campaign(t, TestB6Reads) }

// TestB8Diag solves one family's CDC store (no apply) and prints every node's selection and requirement.
func TestB8Diag(t *testing.T) {
	fx := os.Getenv("ZEROS3_B6_FX")
	for _, fam := range strings.Split(os.Getenv("ZEROS3_B6_FAMILIES"), ",") {
		s, done := b6Open(t, filepath.Join(fx, fam, "cdc"))
		prof := cmpOr(os.Getenv("B8_PROFILE"), "mixed")
		order := "newest"
		if prof == "archive" {
			order = "oldest"
		}
		pol := OptimizePolicy{AuthPolicy: AuthPolicy{Order: order, MaxDepth: 2, MaxBases: 2, MinSaving: 0.10, AllowDelta: true, AllowRep: true}, Profile: prof, Canonical: true}.norm()
		g, err := s.rgBuild(context.Background(), pol)
		if err != nil {
			t.Fatal(err)
		}
		if err := g.generate(context.Background()); err != nil {
			t.Fatal(err)
		}
		g.partition()
		e, _ := g.solve("global", pol.ReadWeight)
		for i, nd := range g.nodes {
			o := e.opt(int32(i))
			t.Logf("node %d %x %s size %d live %v req %d sel %s bases %v users %v", i, nd.sha[:4], nd.obj.key, nd.size, nd.live, e.req[i], o.label, o.bases, e.users[i])
			for _, op := range nd.opts {
				var own int64
				for _, c := range op.comps {
					own += g.cphys[c]
				}
				t.Logf("    opt %s bases %v own %d", op.label, op.bases, own)
			}
		}
		st := e.state("x", false)
		t.Logf("bytes %d required %d", st.Bytes, st.Required)
		g.close()
		done()
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// TestB8Canon builds the canonical representation of every live object of a family's CDC store and prints the
// outcome (diagnostic).
func TestB8Canon(t *testing.T) {
	fx := os.Getenv("ZEROS3_B6_FX")
	for _, fam := range strings.Split(os.Getenv("ZEROS3_B6_FAMILIES"), ",") {
		s, done := b6Open(t, filepath.Join(fx, fam, "cdc"))
		s.eachLiveManifest(nil, func(lm liveMan) error {
			t0 := time.Now()
			root, c, err := s.repConstructCanon(lm.man, nil, 0, map[[32]byte][]byte{})
			if err != nil {
				t.Logf("%s %d: %v", lm.key, lm.man.TotalLength, err)
				return nil
			}
			t.Logf("%s %d: plain %d %x in %v", lm.key, lm.man.TotalLength, c.Size, c.SHA[:6], time.Since(t0))
			_ = root
			c.close()
			return nil
		})
		done()
	}
}

// TestB8IRProbe (B8-A) prices the exact-DEFLATE residual of each file of B8_FILES: gzip streams whole, ZIP
// entries one by one (skeleton and unit bytes against the compressed stream).
type b8nullSink struct{ plain, units int64 }

func (s *b8nullSink) Plain(p []byte) error       { s.plain += int64(len(p)); return nil }
func (s *b8nullSink) Unit(i int, d []byte) error { s.units += int64(len(d)); return nil }

func b8irCost(t *testing.T, deflate []byte) (plain, skel, units int64, err error) {
	s := &b8nullSink{}
	st, _, err := repIREncodeStream(bytes.NewReader(deflate), repIRStreamConfig{Target: 16 << 10, Profile: repIRAutoProfile}, s)
	if err != nil {
		return 0, 0, 0, err
	}
	return s.plain, int64(len(st.Skeleton)), s.units, nil
}

func TestB8IRProbe(t *testing.T) {
	for _, p := range strings.Split(os.Getenv("B8_FILES"), ",") {
		data, _ := os.ReadFile(p)
		if strings.HasSuffix(p, ".whl") || strings.HasSuffix(p, ".zip") {
			z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			var pl, sk, un, comp, fail int64
			for _, f := range z.File {
				off, _ := f.DataOffset()
				raw := data[off : off+int64(f.CompressedSize64)]
				comp += int64(len(raw))
				a, b, c, err := b8irCost(t, raw)
				if err != nil {
					fail++
					continue
				}
				pl, sk, un = pl+a, sk+b, un+c
			}
			t.Logf("%s zip members %d comp %d plain %d skel %d units %d fail %d", p, len(z.File), comp, pl, sk, un, fail)
			continue
		}
		hl, err := repDFGzipHeader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		pl, sk, un, err := b8irCost(t, data[len(hl):len(data)-8])
		t.Logf("%s gzip size %d plain %d skel %d units %d err %v", p, len(data), pl, sk, un, err)
	}
}
