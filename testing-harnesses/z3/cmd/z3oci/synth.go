package main

import (
	"archive/tar"
	"flag"
	"fmt"
	"os"
	"sort"

	"zeros3-z3/corpus"
	"zeros3-z3/fstate"
	"zeros3-z3/ociz3"
)

// cfg is a synthetic image: member lists per layer, tar layout, gzip producer and
// document-level options. Every Z3-17D revision is a one-field edit of a base cfg.
type cfg struct {
	layers   [][]ociz3.Member
	format   tar.Format
	gz       ociz3.GzipOpts
	plain    map[int]bool // layers stored as uncompressed tar
	env      []string
	manifest map[string]string
	indexAnn map[string]string
	arm      bool // index also lists an arm64 manifest over the same layers
	docker   bool
}

func (c cfg) clone() cfg {
	n := c
	n.layers = append([][]ociz3.Member(nil), c.layers...)
	return n
}

func (c cfg) image() (*ociz3.Image, error) {
	spec := func(arch string) (*ociz3.Built, error) {
		var ls []ociz3.LayerSpec
		for i, ms := range c.layers {
			tr, err := ociz3.WriteTar(ms, c.format)
			if err != nil {
				return nil, err
			}
			ls0 := ociz3.LayerSpec{Tar: tr}
			if !c.plain[i] {
				if ls0.Blob, err = ociz3.Compress(tr, c.gz); err != nil {
					return nil, err
				}
			}
			ls = append(ls, ls0)
		}
		s := ociz3.ImageSpec{OS: "linux", Arch: arch, Layers: ls, Env: c.env, ManifestAnnotations: c.manifest, Created: "2024-01-01T00:00:00Z"}
		if c.docker {
			s.MediaTypes = "docker"
		}
		return s.Build()
	}
	b, err := spec("amd64")
	if err != nil {
		return nil, err
	}
	st := ociz3.MemStore{}
	for d, v := range b.Blobs {
		st[d] = v
	}
	descs := []ociz3.Descriptor{b.Desc}
	if c.arm {
		b2, err := spec("arm64")
		if err != nil {
			return nil, err
		}
		for d, v := range b2.Blobs {
			st[d] = v
		}
		descs = append(descs, b2.Desc)
	}
	root, err := ociz3.RootIndex(descs, c.indexAnn, ociz3.MTOCIIndex)
	if err != nil {
		return nil, err
	}
	return ociz3.Walk(st, root, ociz3.Limits{})
}

func stdBase() cfg {
	return cfg{
		layers: [][]ociz3.Member{
			ociz3.Tree(11, "os", 150, 4000),
			ociz3.Tree(12, "lib", 40, 3000),
			ociz3.Tree(13, "etc", 10, 1000),
		},
		format: tar.FormatPAX,
		gz:     ociz3.GzipOpts{Level: 6},
		env:    []string{"PATH=/usr/bin", "LANG=C"},
	}
}

func bigBase() cfg {
	r := corpus.NewRNG(21, "oci-big")
	big := make([]byte, 0, 1<<20+1<<19)
	for len(big) < 1<<20+1<<19 {
		big = append(big, ociz3.Tree(r.U64(), "x", 1, 4000)[0].Data...)
	}
	c := stdBase()
	c.layers = [][]ociz3.Member{
		append(ociz3.Tree(22, "os", 20, 2000), ociz3.Member{Path: "opt/big.dat", Mode: 0o644, MTime: 1700000000, Data: big}),
		ociz3.Tree(23, "lib", 10, 2000),
	}
	return c
}

func tinyBase() cfg {
	c := stdBase()
	c.layers = [][]ociz3.Member{ociz3.Tree(31, "t", 3000, 100), ociz3.Tree(32, "lib", 10, 2000)}
	return c
}

func editMember(ms []ociz3.Member, i int, f func(*ociz3.Member)) []ociz3.Member {
	out := append([]ociz3.Member(nil), ms...)
	f(&out[i])
	return out
}

type variant struct {
	name, desc string
	base       func() cfg
	rev        func(cfg) cfg
	note       string
}

func variants() []variant {
	std := stdBase
	return []variant{
		{"D1 one file changed", "one 4 KB file in layer 0 gets 24 bytes appended; layer rebuilt with the same producer", std, func(c cfg) cfg {
			c.layers[0] = editMember(c.layers[0], 40, func(m *ociz3.Member) {
				m.Data = append(append([]byte(nil), m.Data...), []byte(" appended-in-revision!")...)
			})
			return c
		}, ""},
		{"D2 one file added", "one new 2 KB file appended to layer 0", std, func(c cfg) cfg {
			c.layers[0] = append(append([]ociz3.Member(nil), c.layers[0]...), ociz3.Tree(99, "new", 1, 2000)[0])
			return c
		}, ""},
		{"D3 whiteout layer", "a new tiny top layer holding a whiteout of one file (what a build `rm` produces)", std, func(c cfg) cfg {
			c.layers = append(append([][]ociz3.Member(nil), c.layers...), []ociz3.Member{{Path: "os/d03/.wh.f00003", Mode: 0o644, MTime: 1700000000}})
			return c
		}, ""},
		{"D3b removal in place", "one file removed from layer 0 (layer rebuilt)", std, func(c cfg) cfg {
			ms := c.layers[0]
			c.layers[0] = append(append([]ociz3.Member(nil), ms[:40]...), ms[41:]...)
			return c
		}, ""},
		{"D4 member reorder", "layer 0 members written in a different (rotated) order", std, func(c cfg) cfg {
			ms := c.layers[0]
			c.layers[0] = append(append([]ociz3.Member(nil), ms[75:]...), ms[:75]...)
			return c
		}, ""},
		{"D5 tar header metadata", "mtime, uid, gid and mode of every member of layer 0 changed (payloads identical)", std, func(c cfg) cfg {
			out := append([]ociz3.Member(nil), c.layers[0]...)
			for i := range out {
				out[i].MTime += 3600
				out[i].UID, out[i].GID = 1000, 1000
				out[i].Mode = 0o600
			}
			c.layers[0] = out
			return c
		}, ""},
		{"D6 gzip header only", "same tar, same DEFLATE stream, gzip header mtime/name/OS differ (DiffID identical, layer digest differs)", std, func(c cfg) cfg {
			c.gz = ociz3.GzipOpts{Level: 6, MTime: 1700000000, Name: "layer.tar", OS: 3}
			return c
		}, ""},
		{"D7a other producer: GNU gzip", "same tars recompressed by GNU gzip -6 (an independent implementation)", std, func(c cfg) cfg {
			c.gz = ociz3.GzipOpts{Producer: "gnugzip", Level: 6}
			return c
		}, ""},
		{"D7b other producer: python zlib", "same tars recompressed by CPython gzip/zlib level 6", std, func(c cfg) cfg {
			c.gz = ociz3.GzipOpts{Producer: "python", Level: 6}
			return c
		}, ""},
		{"D7c same producer, level 1", "same tars recompressed by the Go producer at level 1 instead of 6", std, func(c cfg) cfg {
			c.gz = ociz3.GzipOpts{Level: 1}
			return c
		}, ""},
		{"D7d same producer, level 9", "same tars recompressed by the Go producer at level 9", std, func(c cfg) cfg {
			c.gz = ociz3.GzipOpts{Level: 9}
			return c
		}, ""},
		{"D8 tar layout", "same member payloads written as a GNU-format tar instead of PAX", std, func(c cfg) cfg {
			c.format = tar.FormatGNU
			return c
		}, ""},
		{"D9 sparse edit of a large file", "one 1.5 MB file in layer 0, 16 bytes overwritten in the middle", bigBase, func(c cfg) cfg {
			c.layers[0] = editMember(c.layers[0], len(c.layers[0])-1, func(m *ociz3.Member) {
				d := append([]byte(nil), m.Data...)
				copy(d[len(d)/2:], []byte("SPARSE-EDIT-16B!"))
				m.Data = d
			})
			return c
		}, ""},
		{"D10 many tiny files", "3000 files of ~100 B in layer 0; one file changed", tinyBase, func(c cfg) cfg {
			c.layers[0] = editMember(c.layers[0], 1500, func(m *ociz3.Member) { m.Data = append(append([]byte(nil), m.Data...), 'x') })
			return c
		}, ""},
		{"D11 config only", "config Env changed; every layer, the layer order and the DiffIDs unchanged", std, func(c cfg) cfg {
			c.env = append(append([]string(nil), c.env...), "NEW=1")
			return c
		}, ""},
		{"D12 manifest annotation", "one manifest annotation added; config and layers unchanged", std, func(c cfg) cfg {
			c.manifest = map[string]string{"org.opencontainers.image.revision": "0123456789abcdef"}
			return c
		}, ""},
		{"D13 image index", "index gains an annotation and a second (arm64) manifest over the same layer blobs", std, func(c cfg) cfg {
			c.indexAnn = map[string]string{"org.opencontainers.image.ref.name": "rev"}
			c.arm = true
			return c
		}, ""},
		{"D14 layer order", "layers 1 and 2 swapped (a valid image; DiffIDs permuted)", std, func(c cfg) cfg {
			c.layers[1], c.layers[2] = c.layers[2], c.layers[1]
			return c
		}, ""},
		{"D15 layer partitioning", "layers 0 and 1 merged into one layer (same final filesystem)", std, func(c cfg) cfg {
			merged := append(append([]ociz3.Member(nil), c.layers[0]...), c.layers[1]...)
			c.layers = [][]ociz3.Member{merged, c.layers[2]}
			return c
		}, "oracle only: no representation exploits equal final filesystems"},
	}
}

func cmdSynth(args []string) error {
	fs := flag.NewFlagSet("synth", flag.ExitOnError)
	ref := fs.Int64("ref-bytes", 96, "JSON-layout reference cost")
	only := fs.String("only", "", "variant name prefix (e.g. D6)")
	fs.Parse(args)
	fmt.Printf("producers: %v; Go %s\n\n", ociz3.ProducerVersions(), goVersion())
	reps := []string{"cdc", "cdc-archive", "archive-tar", "reg-blob (model)", "layer-gzip-exact", "layer-tar+gzip", "layer-tar+gzip-cdc", "oci-auto", "oci-auto-s10", "layer-oracle-logical"}
	type line struct {
		v     variant
		rows  map[string]fstate.Row
		brows map[string]fstate.Row
		reg   int64
		diff  string
		nbase int64
	}
	var lines []line
	for _, v := range variants() {
		if *only != "" && len(v.name) >= len(*only) && v.name[:len(*only)] != *only {
			continue
		}
		bc := v.base()
		base, err := bc.image()
		if err != nil {
			return err
		}
		rev, err := v.rev(bc.clone()).image()
		if err != nil {
			return err
		}
		sc := &fstate.Scenario{Name: v.name, States: []*fstate.State{{Label: "base", Files: ociz3.Files(base)}, {Label: "rev", Files: ociz3.Files(rev)}}}
		res := ociz3.Runner(*ref, true).Run(sc, ociz3.Reps())
		l := line{v: v, rows: map[string]fstate.Row{}, brows: map[string]fstate.Row{}}
		for _, rp := range reps {
			if r := res.Row("base", rp); r != nil {
				l.brows[rp] = *r
			}
			if r := res.Row("rev", rp); r != nil {
				l.rows[rp] = *r
				if !r.Oracle && !r.Exact {
					fmt.Fprintf(os.Stderr, "NOT EXACT %s / %s: %s\n", v.name, rp, r.Err)
				}
			}
		}
		seen := map[string]bool{}
		regNew(seen, base)
		_, l.reg = regNew(seen, rev)
		l.diff = diffNote(base, rev)
		lines = append(lines, l)
	}
	fmt.Println("Incremental ZeroS3 cost of the revision on top of the stored base image (production pack rule; bytes; every non-oracle cell reconstructs exactly through its serialised recipe):")
	fmt.Print("\n| revision | blobs new to a digest registry | what changed (digests) |")
	for _, rp := range reps {
		fmt.Printf(" %s |", rp)
	}
	fmt.Print("\n|---|---:|---|")
	for range reps {
		fmt.Print("---:|")
	}
	fmt.Println()
	for _, l := range lines {
		fmt.Printf("| %s | %s | %s |", l.v.name, fstate.Human(l.reg), l.diff)
		for _, rp := range reps {
			r, ok := l.rows[rp]
			if !ok {
				fmt.Print(" - |")
				continue
			}
			flag := ""
			if !r.Oracle && !r.Exact {
				flag = " ✗"
			}
			fmt.Printf(" %s%s |", fstate.Human(r.CostPacked), flag)
		}
		fmt.Println()
	}
	fmt.Println("\nFirst-sight premium of transforming and break-even (the base image is stored first; premium = cost(layer-tar+gzip) - cost(cdc) for the base; saving = cost(cdc) - cost(layer-tar+gzip) for the revision when the base was stored transformed; break-even = premium / saving, in revisions of that kind):")
	fmt.Println("\n| revision | base: cdc | base: layer-tar+gzip | base: oci-auto-s10 | premium | saving per revision | break-even revisions | same, members CDC-chunked (layer-tar+gzip-cdc): premium / saving / break-even |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---|---|")
	for _, l := range lines {
		bc, bt, bs := l.brows["cdc"], l.brows["layer-tar+gzip"], l.brows["oci-auto-s10"]
		prem := bt.CostPacked - bc.CostPacked
		sav := l.rows["cdc"].CostPacked - l.rows["layer-tar+gzip"].CostPacked
		be := "never"
		if sav > 0 && prem <= 0 {
			be = "0 (no premium)"
		} else if sav > 0 {
			be = fmt.Sprintf("%.2f", float64(prem)/float64(sav))
		}
		bk := l.brows["layer-tar+gzip-cdc"]
		premK := bk.CostPacked - bc.CostPacked
		savK := l.rows["cdc"].CostPacked - l.rows["layer-tar+gzip-cdc"].CostPacked
		beK := "never"
		if savK > 0 && premK <= 0 {
			beK = "0 (no premium)"
		} else if savK > 0 {
			beK = fmt.Sprintf("%.2f", float64(premK)/float64(savK))
		}
		fmt.Printf("| %s | %s | %s | %s | %s | %s | %s | %s / %s / %s |\n", l.v.name, fstate.Human(bc.CostPacked), fstate.Human(bt.CostPacked), fstate.Human(bs.CostPacked), fmtSigned(prem), fmtSigned(sav), be, fmtSigned(premK), fmtSigned(savK), beK)
	}
	fmt.Println("\nComposition of the `layer-tar+gzip` cost (new components, recipe, plan meta) next to `cdc`:")
	fmt.Println("\n| revision | cdc new | cdc recipe | tar+gzip new | tar+gzip recipe | tar+gzip plan meta | classes of new bytes (tar+gzip) |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---|")
	for _, l := range lines {
		c, t := l.rows["cdc"], l.rows["layer-tar+gzip"]
		var cl []string
		for k, v := range t.Detail {
			if len(k) > 4 && k[:4] == "new_" {
				cl = append(cl, fmt.Sprintf("%s=%s", k[4:], fstate.Human(v)))
			} else {
				cl = append(cl, fmt.Sprintf("%s=%d", k, v))
			}
		}
		sort.Strings(cl)
		fmt.Printf("| %s | %s | %s | %s | %s | %s | %v |\n", l.v.name, fstate.Human(c.CostPacked-c.Recipe-c.Meta-c.Index), fstate.Human(c.Recipe), fstate.Human(t.CostPacked-t.Recipe-t.Meta-t.Index), fstate.Human(t.Recipe), fstate.Human(t.Meta), cl)
	}
	return nil
}

// diffNote summarises which node classes differ between two images, by digest and
// (for layers) by DiffID.
func diffNote(a, b *ociz3.Image) string {
	var lay, layNew, diffSame, cfgs int
	adiff := map[string]bool{}
	for _, n := range a.Nodes {
		if n.DiffID != "" {
			adiff[n.DiffID] = true
		}
	}
	for _, n := range b.Nodes {
		switch n.Kind {
		case ociz3.KindLayerGz, ociz3.KindLayerTar:
			lay++
			if _, ok := a.By[n.Digest]; !ok {
				layNew++
				if adiff[n.DiffID] {
					diffSame++
				}
			}
		case ociz3.KindConfig:
			if _, ok := a.By[n.Digest]; !ok {
				cfgs++
			}
		}
	}
	return fmt.Sprintf("%d/%d layers new; %d of them whose DiffID is already in the base; config new %d", layNew, lay, diffSame, cfgs)
}
