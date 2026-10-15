// Command z3oci runs the Z3-17 OCI registry / image-layer experiments.
//
//	z3oci resolve   tag -> index digest (to write a pin)
//	z3oci fetch     fetch pinned images by digest into a cache outside Git
//	z3oci walk      verify an image graph (descriptors, DiffIDs) and print it
//	z3oci synth     deterministic synthetic revisions (Z3-17D)
//	z3oci real      real image families (Z3-17B/C/E/F)
//	z3oci reads     partial reads, throughput, memory (Z3-17G)
//
// Nothing is committed or fetched by tests; caches live under ~/.cache/z3-realworld/oci.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"zeros3-z3/ociz3"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3oci <resolve|fetch|walk|synth|real|reads> [flags]")
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "resolve":
		err = cmdResolve(args)
	case "fetch":
		err = cmdFetch(args)
	case "walk":
		err = cmdWalk(args)
	case "synth":
		err = cmdSynth(args)
	case "real":
		err = cmdReal(args)
	case "reads":
		err = cmdReads(args)
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3oci:", err)
		os.Exit(1)
	}
}

func cacheRoot() string {
	if p := os.Getenv("Z3_OCI_CACHE"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".cache", "z3-realworld", "oci")
}

// Pin is one image in realworld/oci.json.
type Pin struct {
	Group      string   `json:"group"`
	Name       string   `json:"name"`
	Registry   string   `json:"registry"`
	Repository string   `json:"repository"`
	Tag        string   `json:"tag"` // informational: the pin is the digest
	Index      string   `json:"index"`
	Platforms  []string `json:"platforms"`
	License    string   `json:"license"`
	Note       string   `json:"note,omitempty"`
}

func loadPins(path string) ([]Pin, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f struct{ Images []Pin }
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Images, nil
}

func cmdResolve(args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ExitOnError)
	reg := fs.String("registry", "quay.io", "registry host")
	repo := fs.String("repo", "", "repository")
	maxMiB := fs.Int64("max-mib", 1, "byte budget (manifests only)")
	fs.Parse(args)
	c := ociz3.NewClient(*reg, "", *maxMiB<<20)
	for _, tag := range fs.Args() {
		d, body, err := c.Manifest(*repo, tag)
		if err != nil {
			return err
		}
		ix, _ := ociz3.ParseIndex(body, ociz3.Limits{})
		var plats []string
		if ix != nil {
			for _, m := range ix.Manifests {
				plats = append(plats, m.Platform.String()+"="+m.Digest[7:19])
			}
		}
		fmt.Printf("%s/%s:%s -> %s (%d B) %s\n", *reg, *repo, tag, d, len(body), strings.Join(plats, " "))
	}
	return nil
}

func cmdFetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	pins := fs.String("pins", "realworld/oci.json", "pinned images")
	group := fs.String("group", "", "comma-separated groups (empty = list)")
	maxMiB := fs.Int64("max-mib", 400, "hard cap on bytes downloaded")
	out := fs.String("out", "", "also write each image as an OCI layout directory under this directory")
	fs.Parse(args)
	ps, err := loadPins(*pins)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, g := range strings.Split(*group, ",") {
		want[g] = true
	}
	var total int64
	for _, p := range ps {
		if *group == "" {
			fmt.Printf("%-14s %-10s %s/%s@%s %v\n", p.Group, p.Name, p.Registry, p.Repository, p.Index[:19], p.Platforms)
			continue
		}
		if !want[p.Group] {
			continue
		}
		c := ociz3.NewClient(p.Registry, filepath.Join(cacheRoot(), "blobs"), (*maxMiB<<20)-total)
		root, st, notes, err := c.FetchImage(p.Repository, p.Index, p.Platforms)
		if err != nil {
			return fmt.Errorf("%s %s: %w", p.Group, p.Name, err)
		}
		total += c.Fetched
		fmt.Printf("ok %-14s %-10s %s/%s@%s platforms=%v blobs=%d fetched=%d B (requests %d) %s\n", p.Group, p.Name, p.Registry, p.Repository, p.Index[:19], p.Platforms, len(st), c.Fetched, c.Requests, strings.Join(notes, "; "))
		if *out != "" {
			if err := ociz3.WriteLayout(filepath.Join(*out, p.Group+"-"+p.Name), root, st); err != nil {
				return err
			}
		}
	}
	return nil
}

func goVersion() string { return runtime.Version() }
