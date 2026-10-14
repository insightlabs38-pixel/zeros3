package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"zeros3-z3/merklez3"
)

// cmdPartial measures what a reader needs to touch for one component, one
// subtree and the whole snapshot, against a disk-backed store.
func cmdPartial(args []string) error {
	fs := flag.NewFlagSet("partial", flag.ExitOnError)
	n := fs.Int("n", 400000, "keys")
	dir := fs.String("dir", os.TempDir(), "scratch directory")
	fs.Parse(args)
	cfg := merklez3.DefaultConfig()
	root := fmt.Sprintf("%s/z3merkle-partial", *dir)
	defer os.RemoveAll(root)
	st := merklez3.NewState(1, *n)
	ds, rid, bst, err := buildDisk(root, st, cfg)
	if err != nil {
		return err
	}
	defer ds.Close()
	fmt.Printf("%d keys, %d nodes, %.1f MiB of node bytes\n\n| read | nodes touched | bytes read | store reads | time |\n|---|--:|--:|--:|--:|\n", *n, bst.Nodes, float64(bst.Bytes)/(1<<20))
	rn, _ := ds.Get(rid)
	run := func(name string, f func() (merklez3.Stats, error)) {
		r0 := ds.Reads
		t0 := time.Now()
		s, err := f()
		if err != nil {
			fatal(err)
		}
		fmt.Printf("| %s | %d | %d | %d | %v |\n", name, s.NodesRead, s.BytesRead, ds.Reads-r0, time.Since(t0).Round(time.Microsecond))
	}
	run("one component by native id (the root)", func() (merklez3.Stats, error) {
		nd, err := ds.Get(rid)
		return merklez3.Stats{NodesRead: 1, BytesRead: int64(len(nd.Payload))}, err
	})
	// descend to the leaf holding one key
	key := st.Keys[len(st.Keys)/3]
	run("lookup one key (root -> leaf)", func() (merklez3.Stats, error) {
		var s merklez3.Stats
		id := rid
		for depth := 0; ; depth++ {
			nd, err := ds.Get(id)
			if err != nil {
				return s, err
			}
			s.NodesRead++
			s.BytesRead += int64(len(nd.Payload))
			if len(nd.Kids) == 0 {
				return s, nil
			}
			id = nd.Kids[(key>>(60-4*uint(depth)))&15]
		}
	})
	var sub merklez3.ID
	for _, k := range rn.Kids {
		if k != "" {
			sub = k
			break
		}
	}
	run("traverse one depth-1 subtree (~1/16)", func() (merklez3.Stats, error) { return merklez3.Walk(ds, sub, nil) })
	sn, _ := ds.Get(sub)
	for _, k := range sn.Kids {
		if k != "" {
			sub = k
			break
		}
	}
	run("traverse one depth-2 subtree (~1/256)", func() (merklez3.Stats, error) { return merklez3.Walk(ds, sub, nil) })
	run("materialise the whole snapshot (DFS walk)", func() (merklez3.Stats, error) { return merklez3.Walk(ds, rid, nil) })
	return nil
}
