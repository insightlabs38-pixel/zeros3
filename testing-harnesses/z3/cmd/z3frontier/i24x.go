package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zeros3-z3/frontier"
)

// cmdI24Cross answers the Z3-24 compression-setting question: the same R0 tree packed with two zstd
// levels; how many native units and CDC bytes of the second image does the store holding the first reuse?
func cmdI24Cross(args []string) {
	fs := flag.NewFlagSet("i24x", flag.ExitOnError)
	work := fs.String("work", "/root/z3scratch/i24x", "scratch directory")
	tl := fs.String("tools", "/root/z3cache", "tools root")
	fs.Parse(args)
	must(os.MkdirAll(*work, 0o755))
	tree := frontier.Base()
	dir := filepath.Join(*work, "tree")
	must(frontier.Materialize(tree, dir))
	c := &ictx{rev: "R0", tree: tree, dir: dir, work: *work, T: findTools(*tl), root: *tl, objs: filepath.Join(*work, "objs")}
	byName := map[string]*ilane{}
	for _, l := range imageLanes() {
		byName[l.name] = l
	}
	for _, pair := range [][2]string{{"sq", "sq9"}, {"er", "er9"}} {
		var units [2]map[[32]byte]bool
		var sizes [2][]unit
		ch := frontier.NewChain()
		for i, n := range pair {
			l := byName[n]
			img := filepath.Join(*work, n+".img")
			t0 := time.Now()
			must(l.build(c, img))
			b, err := os.ReadFile(img)
			must(err)
			sc, err := c.scanImage(l, img, b)
			must(err)
			units[i] = map[[32]byte]bool{}
			for _, u := range sc.units {
				units[i][u.ID] = true
			}
			sizes[i] = sc.units
			cost, err := ch.AddStream(bytes.NewReader(b))
			must(err)
			fmt.Printf("%-5s size=%d units=%d build=%v cdc_new_chunks=%d/%d cdc_incr=%d (loose %d)\n", n, len(b), len(sc.units), time.Since(t0).Round(time.Millisecond), cost.NewChunks, cost.Chunks, cost.Incr, cost.IncrLoose)
			os.Remove(img)
		}
		var shared, sharedB, tot, totB int64
		for _, u := range sizes[1] {
			tot++
			totB += u.Len
			if units[0][u.ID] {
				shared++
				sharedB += u.Len
			}
		}
		fmt.Printf("%s vs %s: %d/%d native units (%d of %d bytes = %.1f%%) of the second image already exist in the first\n\n", pair[1], pair[0], shared, tot, sharedB, totB, 100*float64(sharedB)/float64(totB))
	}
	os.RemoveAll(dir)
}

// cmdDupes builds a tree of only the duplicate sets and compares first-sight dedupe settings.
func cmdDupes(args []string) {
	fs := flag.NewFlagSet("dupes", flag.ExitOnError)
	work := fs.String("work", "/root/z3scratch/dupes", "scratch directory")
	tl := fs.String("tools", "/root/z3cache", "tools root")
	fs.Parse(args)
	must(os.MkdirAll(*work, 0o755))
	full := frontier.Base()
	sub := &frontier.Tree{Rev: "dup"}
	for _, e := range full.Entries {
		if strings.HasPrefix(e.Path, "dup/") || e.Path == "dup" {
			sub.Entries = append(sub.Entries, e)
		}
	}
	dir := filepath.Join(*work, "tree")
	must(frontier.Materialize(sub, dir))
	c := &ictx{rev: "R0", tree: sub, dir: dir, work: *work, T: findTools(*tl), root: *tl, objs: filepath.Join(*work, "objs")}
	st := sub.Stats()
	fmt.Printf("dup subtree: %s (100 identical 128 KiB + 3 unique 1 MiB x2)\n", st)
	type v struct {
		name  string
		build func(c *ictx, out string) error
	}
	for _, x := range []v{
		{"squashfs default (dedupe on)", sqBuild("3")},
		{"squashfs -no-duplicates", sqBuild("3", "-no-duplicates")},
		{"squashfs -no-duplicates -no-hardlinks", sqBuild("3", "-no-duplicates", "-no-hardlinks")},
		{"erofs zstd -Efragments (no dedupe)", erBuild("-zzstd,level=3", "-C131072", "-Efragments")},
		{"erofs zstd -Efragments,dedupe", erBuild("-zzstd,level=3", "-C131072", "-Efragments,dedupe")},
		{"erofs plain", erBuild()},
		{"erofs plain -Ededupe", erBuild("-Ededupe")},
	} {
		img := filepath.Join(*work, "d.img")
		t0 := time.Now()
		done := make(chan error, 1)
		go func() { done <- x.build(c, img) }()
		select {
		case err := <-done:
			st, _ := os.Stat(img)
			if err != nil {
				fmt.Printf("%-40s error: %v\n", x.name, err)
			} else {
				fmt.Printf("%-40s %10d B  (%v)\n", x.name, st.Size(), time.Since(t0).Round(time.Millisecond))
			}
		case <-time.After(90 * time.Second):
			fmt.Printf("%-40s TIMEOUT >90s (abandoned)\n", x.name)
		}
		os.Remove(img)
	}
	os.RemoveAll(dir)
}
