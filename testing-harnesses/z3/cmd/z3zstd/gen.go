package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zeros3-z3/frontier"
)

// cmdGen materialises each revision once, writes its deterministic tar, compresses it
// with every requested producer tag and records the FLOOR oracle chain. Existing
// outputs are kept, so the step is resumable and the expensive tree is never rebuilt
// for a revision whose tar and compressed files already exist.
func cmdGen(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	dir := fs.String("dir", "/root/z3scratch/c25", "output directory")
	tags := fs.String("tags", "z155l3", "producer tags")
	revs := fs.String("revs", strings.Join(frontier.Revisions, ","), "revisions")
	fs.Parse(args)
	must(os.MkdirAll(*dir, 0o755))
	treeDir := filepath.Join(*dir, "tree")
	floorPath := filepath.Join(*dir, "floor.jsonl")
	floor := frontier.NewFloor()
	var floorOut []map[string]any
	_, floorErr := os.Stat(floorPath)
	base := frontier.Base()
	for _, rev := range strings.Split(*revs, ",") {
		t0 := time.Now()
		tarPath := filepath.Join(*dir, rev+".tar")
		need := floorErr != nil
		if _, err := os.Stat(tarPath); err != nil {
			need = true
		}
		for _, tg := range strings.Split(*tags, ",") {
			if _, err := os.Stat(filepath.Join(*dir, rev+"."+tg+".zst")); err != nil {
				need = true
			}
		}
		if !need {
			continue
		}
		tree := frontier.Revision(base, rev)
		must(frontier.Materialize(tree, treeDir))
		if floorErr != nil {
			fc, err := floor.Add(tree, treeDir)
			must(err)
			floorOut = append(floorOut, map[string]any{"rev": rev, "total": fc.Total, "file_packed": fc.FilePacked, "manifest": fc.Manifest, "files": fc.Files})
		}
		if _, err := os.Stat(tarPath); err != nil {
			f, err := os.Create(tarPath)
			must(err)
			must(frontier.WriteTar(f, tree, treeDir))
			must(f.Close())
		}
		for _, tg := range strings.Split(*tags, ",") {
			p, ok := producers[tg]
			if !ok {
				must(fmt.Errorf("unknown producer tag %q", tg))
			}
			out := filepath.Join(*dir, rev+"."+tg+".zst")
			if _, err := os.Stat(out); err != nil {
				must(p.compress(tarPath, out))
			}
		}
		must(os.RemoveAll(treeDir))
		fmt.Fprintf(os.Stderr, "gen %s done in %v\n", rev, since(t0))
	}
	if floorErr != nil {
		f, err := os.Create(floorPath)
		must(err)
		enc := json.NewEncoder(f)
		for _, r := range floorOut {
			must(enc.Encode(r))
		}
		must(f.Close())
	}
}
