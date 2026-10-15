package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/carz3"
	"zeros3-z3/composez3"
	"zeros3-z3/fstate"
	"zeros3-z3/lab"
	"zeros3-z3/ociz3"
)

// layerWorkload runs the four-way comparison (CDC, outer, inner alone, composed) for a
// sequence of layer-like files; arts[label] is the inner object of each state, used for
// the standalone inner-only row.
type layerWorkload struct {
	Title   string
	States  []*fstate.State
	Arts    map[string][]byte
	ArtKind string // fstate kind of the standalone artifact
	Inner   composez3.Kind
	Json    string
}

func (w *layerWorkload) run() error {
	r := &fstate.Runner{RefBytes: 96, Pack: true}
	reps := []fstate.RepDef{cdcRep(), outerRep("outer", false), outerRep("outer-cdc", true), composedRep("composed", w.Inner)}
	reps = append(reps, cheapest("greedy-oracle", cdcRep(), outerRep("outer", false), composedRep("composed", w.Inner)),
		admit("admit-slack10", 0.10, cdcRep(), composedRep("composed", w.Inner)))
	m, err := runMatrix(r, w.States, reps)
	if err != nil {
		return err
	}
	printCosts(w.Title, w.States, reps, m)
	printSplit("composed", w.States, "composed", m)
	printSplit("outer", w.States, "outer", m)

	// the inner transform alone: the artifacts stored as loose objects
	var arts []*fstate.State
	for _, st := range w.States {
		arts = append(arts, &fstate.State{Label: st.Label, Files: []fstate.File{{Name: "artifact", Kind: w.ArtKind, Data: w.Arts[st.Label]}}})
	}
	// each state replaces the artifact: name the file by content so every revision is stored
	for _, st := range arts {
		st.Files[0].Name = "artifact-" + st.Label
	}
	ireps := []fstate.RepDef{cdcRep(), innerRep("inner-alone", w.Inner)}
	im, err := runMatrix(r, arts, ireps)
	if err != nil {
		return err
	}
	printCosts(w.Title+": the inner object alone (loose artifact)", arts, ireps, im)
	save := map[string]int64{}
	for _, st := range arts {
		save[st.Label] = im["cdc"][st.Label].Total - im["inner-alone"][st.Label].Total
	}
	fmt.Println()
	for _, l := range classify(w.States, m, "outer", "composed", save) {
		fmt.Println(l)
	}
	for _, st := range w.States {
		if p := m["admit-slack10"][st.Label].Picks; len(p) > 0 {
			fmt.Printf("admit-slack10 %-14s %v\n", st.Label, p)
		}
	}
	last := w.States[len(w.States)-1]
	rs, err := measureReads(last.Label, last.Files[0], []composez3.Kind{w.Inner})
	if err != nil {
		return err
	}
	printReads([]*ReadStats{rs})
	return writeJSON(w.Json, map[string]any{"title": w.Title, "matrix": m, "inner_alone": im, "reads": rs})
}

// ---- A: real OCI layers ----

func ociCache() string {
	if p := os.Getenv("Z3_OCI_CACHE"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "z3-realworld", "oci")
}

type pin struct {
	Group, Name, Registry, Repository, Index string
	Platforms                                []string
}

func loadPin(p pin, plat string) (*ociz3.Image, error) {
	c := ociz3.NewClient(p.Registry, filepath.Join(ociCache(), "blobs"), 1) // cache only
	root, st, _, err := c.FetchImage(p.Repository, p.Index, p.Platforms)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w (run `z3oci fetch -group %s` first)", p.Group, p.Name, err, p.Group)
	}
	ix, err := ociz3.ParseIndex(root, ociz3.Limits{})
	if err != nil {
		return nil, err
	}
	var sel []ociz3.Descriptor
	for _, d := range ix.Manifests {
		if d.Platform != nil && d.Platform.OS+"/"+d.Platform.Architecture == plat {
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
	return ociz3.Walk(st, root, ociz3.Limits{})
}

func cmdOCI(args []string) error {
	fs := flag.NewFlagSet("oci", flag.ExitOnError)
	pins := fs.String("pins", "realworld/oci.json", "pinned images")
	group := fs.String("group", "nodeexp", "image group")
	jsonOut := fs.String("json", "", "write results as JSON")
	fs.Parse(args)
	b, err := os.ReadFile(*pins)
	if err != nil {
		return err
	}
	var f struct{ Images []pin }
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	w := &layerWorkload{Title: "A. OCI layer -> tar+gzip -> Go ELF (" + *group + ", linux/amd64, real)", Arts: map[string][]byte{}, ArtKind: "elf", Inner: composez3.KindExec, Json: *jsonOut}
	for _, p := range f.Images {
		if p.Group != *group {
			continue
		}
		img, err := loadPin(p, "linux/amd64")
		if err != nil {
			return err
		}
		var best *fstate.File
		var exe []byte
		for _, lf := range ociz3.Files(img) {
			lf := lf
			if ociz3.Kind(lf.Kind) != ociz3.KindLayerGz {
				continue
			}
			plain, a, err := layerMembers(lf)
			if err != nil {
				continue
			}
			for _, m := range a.Members {
				if m.PayLen >= 1<<20 && bytes.HasPrefix(plain[m.PayOff:], []byte("\x7fELF")) {
					best, exe = &lf, plain[m.PayOff:m.PayOff+m.PayLen]
				}
			}
		}
		if best == nil {
			return fmt.Errorf("%s: no layer carries an ELF >= 1 MiB", p.Name)
		}
		lf := *best
		lf.Name = "layer-" + p.Name
		w.States = append(w.States, &fstate.State{Label: p.Name, Files: []fstate.File{lf}})
		w.Arts[p.Name] = append([]byte(nil), exe...)
		fmt.Printf("%s: layer %.19s (%d B gzip), executable %d B\n", p.Name, best.Name, len(lf.Data), len(exe))
	}
	if len(w.States) == 0 {
		return fmt.Errorf("no images in group %q", *group)
	}
	return w.run()
}

// ---- B: OCI-like layers carrying SQLite revisions ----

func structCache() string {
	if p := os.Getenv("Z3_STRUCT_CACHE"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "z3-realworld", "structured")
}

func cmdSQLite(args []string) error {
	fs := flag.NewFlagSet("sqlite", flag.ExitOnError)
	jsonOut := fs.String("json", "", "write results as JSON")
	fs.Parse(args)
	lib := make([]byte, 1<<20) // a constant shared library next to the database
	x := uint64(88172645463325252)
	for i := range lib {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		lib[i] = byte(x>>56) & 0x3f // low-entropy bytes: compressible like code
	}
	w := &layerWorkload{Title: "B. OCI-like layer -> tar+gzip -> SQLite (locally produced revisions)", Arts: map[string][]byte{}, ArtKind: "sqlite", Inner: composez3.KindSQLite, Json: *jsonOut}
	for _, rev := range []string{"r0", "m02-update-100-sparse", "m09-vacuum", "m13-update-1000"} {
		db, err := os.ReadFile(filepath.Join(structCache(), "sqlite", "kv24", rev, "db.sqlite"))
		if err != nil {
			return fmt.Errorf("%w (generate with oracle/structured/gen_sqlite.py)", err)
		}
		lf, err := gzipLayer([]ociz3.Member{
			{Path: "etc/app.conf", Mode: 0o644, MTime: 1700000000, Data: bytes.Repeat([]byte("setting=value\n"), 150)},
			{Path: "usr/lib/libshared.so.1", Mode: 0o755, MTime: 1700000000, Data: lib},
			{Path: "var/lib/app/app.db", Mode: 0o644, MTime: 1700000000, Data: db},
		})
		if err != nil {
			return err
		}
		lf.Name = "layer-" + rev
		w.States = append(w.States, &fstate.State{Label: rev, Files: []fstate.File{lf}})
		w.Arts[rev] = db
	}
	return w.run()
}

// ---- D: tar.gz archives of CAR exports ----

func cmdCAR(args []string) error {
	fs := flag.NewFlagSet("car", flag.ExitOnError)
	jsonOut := fs.String("json", "", "write results as JSON")
	fs.Parse(args)
	for _, fam := range carz3.Families(carz3.BigBlocks) {
		if fam.Name != "v1-reorder" && fam.Name != "v1-add" {
			continue
		}
		w := &layerWorkload{Title: "D. tar.gz archive -> CAR (" + fam.Name + ", synthetic blocks)", Arts: map[string][]byte{}, ArtKind: "car", Inner: composez3.KindCAR}
		if *jsonOut != "" {
			w.Json = strings.TrimSuffix(*jsonOut, ".json") + "-" + fam.Name + ".json"
		}
		for i, rv := range fam.Revs {
			lf, err := gzipLayer([]ociz3.Member{
				{Path: "export/MANIFEST", Mode: 0o644, MTime: 1700000000, Data: []byte("car export\n")},
				{Path: "export/data.car", Mode: 0o644, MTime: 1700000000, Data: rv.Data},
			})
			if err != nil {
				return err
			}
			label := fmt.Sprintf("r%d", i)
			lf.Name = "archive-" + label
			w.States = append(w.States, &fstate.State{Label: label, Files: []fstate.File{lf}})
			w.Arts[label] = rv.Data
		}
		if err := w.run(); err != nil {
			return err
		}
	}
	return nil
}

var _ = lab.ID{}
