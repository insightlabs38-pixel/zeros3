package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"zeros3-z3/fstate"
	"zeros3-z3/ociz3"
)

// loadPin reads a pinned image from the cache only (budget 1 byte: a cache miss is
// an error, run `z3oci fetch` first). plat "" keeps the whole index; otherwise the
// state is the single-platform image whose index.json lists that manifest.
func loadPin(p Pin, plat string) (*ociz3.Image, error) {
	c := ociz3.NewClient(p.Registry, filepath.Join(cacheRoot(), "blobs"), 1)
	root, st, _, err := c.FetchImage(p.Repository, p.Index, p.Platforms)
	if err != nil {
		return nil, fmt.Errorf("%s/%s@%s: %w (run `z3oci fetch -group %s` first)", p.Group, p.Name, p.Index[:19], err, p.Group)
	}
	if plat != "" {
		ix, err := ociz3.ParseIndex(root, ociz3.Limits{})
		if err != nil {
			return nil, err
		}
		var sel []ociz3.Descriptor
		for _, d := range ix.Manifests {
			if d.Platform != nil && (d.Platform.String() == plat || d.Platform.OS+"/"+d.Platform.Architecture == plat) {
				sel = append(sel, d)
			}
		}
		if len(sel) != 1 {
			return nil, fmt.Errorf("%s: platform %q matches %d manifests", p.Name, plat, len(sel))
		}
		root, err = ociz3.RootIndex(sel, nil, ociz3.MTOCIIndex)
		if err != nil {
			return nil, err
		}
	} else {
		// the fetched store holds only the selected platforms; the index document lists
		// all of them, so the walked graph of an incomplete store reports the rest missing
		var keep []ociz3.Descriptor
		ix, _ := ociz3.ParseIndex(root, ociz3.Limits{})
		for _, d := range ix.Manifests {
			if _, e := st.Blob(d.Digest); e == nil {
				keep = append(keep, d)
			}
		}
		root, err = ociz3.RootIndex(keep, nil, ociz3.MTOCIIndex)
		if err != nil {
			return nil, err
		}
	}
	return ociz3.Walk(st, root, ociz3.Limits{})
}

func pinsByGroup(path, group string) ([]Pin, error) {
	ps, err := loadPins(path)
	if err != nil {
		return nil, err
	}
	var out []Pin
	for _, p := range ps {
		if p.Group == group {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no pins in group %q", group)
	}
	return out, nil
}

func cmdWalk(args []string) error {
	fs := flag.NewFlagSet("walk", flag.ExitOnError)
	pins := fs.String("pins", "realworld/oci.json", "pinned images")
	group := fs.String("group", "", "group")
	fs.Parse(args)
	ps, err := pinsByGroup(*pins, *group)
	if err != nil {
		return err
	}
	fmt.Printf("fetch date: %s; Go %s; all blobs size- and sha256-verified before use\n\n", time.Now().UTC().Format("2006-01-02"), goVersion())
	defer nativeTable(*pins, *group)
	fmt.Println("| image | platform | role | digest | size | media type | status | DiffID check |")
	fmt.Println("|---|---|---|---|---:|---|---|---|")
	for _, p := range ps {
		img, err := loadPin(p, "")
		if err != nil {
			return err
		}
		for _, n := range img.Nodes {
			plat := ""
			if n.Platform != nil {
				plat = n.Platform.String()
			}
			fmt.Printf("| %s %s | %s | %s | `%s` | %d | `%s` | %s | %s |\n", p.Group, p.Name, plat, n.Kind, n.Digest[7:19], n.Size, strings.TrimPrefix(n.MediaType, "application/vnd."), n.Status, n.DiffOK)
		}
	}
	return nil
}

// kinds of sharing between images
func sharing(imgs []*ociz3.Image, names []string) {
	owners := map[string][]string{}
	size := map[string]int64{}
	kind := map[string]ociz3.Kind{}
	for i, img := range imgs {
		for _, n := range img.Nodes {
			owners[n.Digest] = append(owners[n.Digest], names[i])
			size[n.Digest] = n.Size
			kind[n.Digest] = n.Kind
		}
	}
	var ds []string
	for d, o := range owners {
		if len(o) > 1 {
			ds = append(ds, d)
		}
	}
	sort.Strings(ds)
	fmt.Println("\nBlobs referenced by more than one image:")
	fmt.Println("\n| blob | kind | size | referenced by |")
	fmt.Println("|---|---|---:|---|")
	for _, d := range ds {
		fmt.Printf("| `%s` | %s | %d | %s |\n", d[7:19], kind[d], size[d], strings.Join(owners[d], ", "))
	}
}

type step struct {
	label string
	img   *ociz3.Image
}

// regBlobBytes: bytes of the image's blobs not yet seen by digest (what a
// digest-addressed blob store adds).
func regNew(seen map[string]bool, img *ociz3.Image) (total, added int64) {
	for _, n := range img.Nodes {
		if n.Data == nil {
			continue
		}
		total += int64(len(n.Data))
		if !seen[n.Digest] {
			seen[n.Digest] = true
			added += int64(len(n.Data))
		}
	}
	return
}

// runSteps measures a sequence of images as one cumulative family under every
// representation and prints the table, the three-number split and the exactness.
func runSteps(name, desc string, steps []step, refBytes int64) *fstate.Result {
	sc := &fstate.Scenario{Name: name, Desc: desc}
	for _, s := range steps {
		sc.States = append(sc.States, &fstate.State{Label: s.label, Files: ociz3.Files(s.img)})
	}
	r := ociz3.Runner(refBytes, true)
	res := r.Run(sc, ociz3.Reps())
	res.WriteMD(os.Stdout, true)
	threeNumbers(res, steps)
	fmt.Println("\nCumulative cost over the sequence (production pack rule; bytes, and relative to cdc):")
	fmt.Println("\n| representation | total | vs cdc | first image | later images |")
	fmt.Println("|---|---:|---:|---:|---:|")
	var cdcTotal int64
	for _, row := range res.Rows {
		if row.Rep == "cdc" {
			cdcTotal += row.CostPacked
		}
	}
	for _, rp := range []string{"cdc", "cdc-archive", "archive-tar", "reg-blob (model)", "layer-gzip-exact", "layer-tar+gzip", "layer-tar+gzip-cdc", "oci-auto", "oci-auto-s10", "layer-oracle-logical", "layer-oracle-tar"} {
		var tot, first int64
		for i, s := range steps {
			if row := res.Row(s.label, rp); row != nil {
				tot += row.CostPacked
				if i == 0 {
					first = row.CostPacked
				}
			}
		}
		fmt.Printf("| %s | %s | %+.1f%% | %s | %s |\n", rp, fstate.Human(tot), 100*(float64(tot)/float64(max(cdcTotal, 1))-1), fstate.Human(first), fstate.Human(tot-first))
	}
	nonExact := 0
	for _, row := range res.Rows {
		if !row.Oracle && !row.Exact {
			nonExact++
			fmt.Printf("\nNOT EXACT: %s / %s: %s\n", row.State, row.Rep, row.Err)
		}
	}
	fmt.Printf("\nexactness: every non-oracle (state, representation) cell reconstructed through its serialised recipe: %d failures of %d cells\n", nonExact, len(res.Rows))
	return res
}

// threeNumbers separates, per step and for the production-shaped chooser
// (`oci-auto`) and plain `layer-tar+gzip`: (1) the blob-level reuse a digest
// registry already gets, (2) the additional saving ZeroS3 gets inside the blobs the
// registry would store as new, (3) the metadata/reconstruction cost paid for (2).
func threeNumbers(res *fstate.Result, steps []step) {
	seen := map[string]bool{}
	fmt.Println("\nThree-number split per step (bytes; ZeroS3 columns are production-pack-rule costs):")
	fmt.Println("\n| step | image bytes | (1) registry blob-level reuse | blobs stored as new | representation | new component bytes | (2) additional saving inside new blobs | (3) recipe + plan meta + index | ZeroS3 total | vs registry-new |")
	fmt.Println("|---|---:|---:|---:|---|---:|---:|---:|---:|---:|")
	for _, s := range steps {
		total, added := regNew(seen, s.img)
		for _, rep := range []string{"cdc", "layer-tar+gzip", "layer-tar+gzip-cdc", "oci-auto-s10"} {
			row := res.Row(s.label, rep)
			if row == nil {
				continue
			}
			meta := row.Recipe + row.Meta + row.Index
			newb := row.CostPacked - meta
			fmt.Printf("| %s | %s | %s | %s | %s | %s | %s | %s | %s | %+.1f%% |\n", s.label, fstate.Human(total), fstate.Human(total-added), fstate.Human(added), rep, fstate.Human(newb), fmtSigned(added-newb), fstate.Human(meta), fstate.Human(row.CostPacked), 100*(float64(row.CostPacked)/float64(max(added, 1))-1))
		}
	}
}

func fmtSigned(n int64) string {
	if n < 0 {
		return "-" + fstate.Human(-n)
	}
	return fstate.Human(n)
}

func cmdReal(args []string) error {
	fs := flag.NewFlagSet("real", flag.ExitOnError)
	pins := fs.String("pins", "realworld/oci.json", "pinned images")
	ref := fs.Int64("ref-bytes", 96, "JSON-layout reference cost")
	only := fs.String("only", "", "comma separated scenarios (default all)")
	detail := fs.Bool("detail", true, "print cost composition of key representations")
	fs.Parse(args)
	want := map[string]bool{}
	for _, s := range strings.Split(*only, ",") {
		want[s] = true
	}
	on := func(n string) bool { return *only == "" || want[n] }
	for _, group := range []string{"nodeexp", "rtdeps"} {
		ps, err := pinsByGroup(*pins, group)
		if err != nil {
			return err
		}
		for _, plat := range []string{"linux/amd64", "linux/arm64", ""} {
			scen := group + "-" + strings.ReplaceAll(plat, "/", "-")
			if plat == "" {
				scen = group + "-index"
			}
			if !on(scen) {
				continue
			}
			var steps []step
			var imgs []*ociz3.Image
			var names []string
			for _, p := range ps {
				img, err := loadPin(p, plat)
				if err != nil {
					return err
				}
				steps = append(steps, step{label: p.Name, img: img})
				imgs = append(imgs, img)
				names = append(names, p.Name)
			}
			fmt.Printf("\n### %s\n", scen)
			res := runSteps(scen, fmt.Sprintf("%s %s, successive releases through one cumulative store", group, plat), steps, *ref)
			sharing(imgs, names)
			if *detail {
				res.WriteDetail(os.Stdout, "cdc", "reg-blob (model)", "layer-tar+gzip", "oci-auto")
			}
		}
	}
	if on("cross-platform") {
		ps, _ := pinsByGroup(*pins, "nodeexp")
		var steps []step
		for _, plat := range []string{"linux/amd64", "linux/arm64"} {
			img, err := loadPin(ps[0], plat)
			if err != nil {
				return err
			}
			steps = append(steps, step{label: ps[0].Name + " " + plat, img: img})
		}
		fmt.Printf("\n### cross-platform\n")
		runSteps("cross-platform", "the same release for two platforms, one after the other", steps, *ref)
	}
	return nil
}

// nativeTable: how registry digests map onto ZeroS3 components under production CDC.
func nativeTable(pins, group string) {
	ps, err := pinsByGroup(pins, group)
	if err != nil {
		return
	}
	fmt.Println("\nRegistry digest vs ZeroS3 component identity (production CDC; a blob digest is a component SHA only when the blob is a single chunk):")
	fmt.Println("\n| image | verified blobs | single-chunk blobs (digest = component SHA, no mapping) | their bytes | multi-chunk blobs (need digest -> component list) | their bytes | chunks | mapping recipe bytes |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|")
	for _, p := range ps {
		img, err := loadPin(p, "")
		if err != nil {
			continue
		}
		n, err := ociz3.Natives(img)
		if err != nil {
			continue
		}
		fmt.Printf("| %s %s | %d | %d | %s | %d | %s | %d | %s |\n", p.Group, p.Name, n.Blobs, n.SingleChunk, fstate.Human(n.SingleBytes), n.Multi, fstate.Human(n.MultiBytes), n.MultiChunks, fstate.Human(n.MappingBytes))
	}
}
