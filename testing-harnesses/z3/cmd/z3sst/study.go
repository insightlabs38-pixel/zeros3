package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/fstate"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/sstz3"
)

func repByName(target int, name string) repDef {
	for _, r := range allReps(target) {
		if r.name == name {
			return r
		}
	}
	panic("no representation " + name)
}

// cmdCodecs: the base load under other block compressions. Raw blocks are expressed; LZ4 and
// Zstd blocks make the table unsupported and the candidate falls back to CDC (never worse).
func cmdCodecs(args []string) error {
	fs := flag.NewFlagSet("codecs", flag.ExitOnError)
	root := fs.String("corpus", filepath.Join(os.Getenv("HOME"), ".cache", "z3-sst", "corpus"), "corpus root")
	fs.Parse(args)
	h := fstate.Human
	fmt.Println("| producer | codec | stored table bytes | cdc packed | ukv packed | fallback files | exact |")
	fmt.Println("|---|---|---:|---:|---:|---:|---|")
	for _, prod := range []string{"leveldb", "rocksdb", "pebble"} {
		dir := filepath.Join(*root, prod, "codecs")
		for _, rev := range revNames(dir) {
			tables, _, err := loadRev(filepath.Join(dir, rev))
			if err != nil {
				return err
			}
			var raw int64
			for _, f := range tables {
				raw += int64(len(f.data))
			}
			c := runFiles(newStore(), repByName(8<<10, "cdc"), tables)
			u := runFiles(newStore(), repByName(8<<10, "ukv"), tables)
			fmt.Printf("| %s | %s | %s | %s | %s | %d/%d | %v |\n", prod, strings.TrimPrefix(rev, "k-"), h(raw), h(c.Pack), h(u.Pack), u.Fallback, u.Files, u.Exact)
		}
	}
	return nil
}

// cmdLockIn compares admission policies over the chain for one representation: everything
// under CDC, everything under the representation from first sight, greedy per-file admission
// (each table takes whichever of CDC and the representation is cheaper against the store as it
// is at that moment) and the hindsight family optimum min(all-CDC, all-representation).
func cmdLockIn(args []string) error {
	fs := flag.NewFlagSet("lockin", flag.ExitOnError)
	root := fs.String("corpus", filepath.Join(os.Getenv("HOME"), ".cache", "z3-sst", "corpus"), "corpus root")
	repName := fs.String("rep", "ukv", "representation")
	prods := fs.String("prod", "leveldb,rocksdb,pebble", "producers")
	fam := fs.String("family", "chain", "family")
	fs.Parse(args)
	h := fstate.Human
	cdc, rep := repByName(8<<10, "cdc"), repByName(8<<10, *repName)
	fmt.Printf("| producer | all CDC | all %s (first sight) | greedy per-file | hindsight min | greedy picked %s for |\n|---|---:|---:|---:|---:|---|\n", *repName, *repName)
	for _, prod := range strings.Split(*prods, ",") {
		dir := filepath.Join(*root, prod, *fam)
		sts, err := loadFamily(dir, revNames(dir))
		if err != nil {
			return err
		}
		a, b, g := newStore(), newStore(), newStore()
		var ta, tb, tg int64
		picked, files := 0, 0
		for _, s := range sts {
			for _, f := range s.tables {
				ta += runFiles(a, cdc, []file{f}).Pack
				tb += runFiles(b, rep, []file{f}).Pack
				ca := runFiles(g.Clone(), cdc, []file{f})
				cb := runFiles(g.Clone(), rep, []file{f})
				files++
				if cb.Pack < ca.Pack {
					tg += runFiles(g, rep, []file{f}).Pack
					picked++
				} else {
					tg += runFiles(g, cdc, []file{f}).Pack
				}
			}
		}
		fmt.Printf("| %s | %s | %s | %s | %s | %d of %d tables |\n", prod, h(ta), h(tb), h(tg), h(min(ta, tb)), picked, files)
	}
	return nil
}

// cmdRepCost accounts the chain through the real repz3 contract (serialized representation
// objects, 35-byte members, one object per distinct component) instead of the lab's 96 B/ref
// convention, for one form. Packed = production pack rule over every newly stored object.
func cmdRepCost(args []string) error {
	fs := flag.NewFlagSet("repcost", flag.ExitOnError)
	root := fs.String("corpus", filepath.Join(os.Getenv("HOME"), ".cache", "z3-sst", "corpus"), "corpus root")
	form := fs.Int("form", int(sstz3.FormUKV), "form")
	target := fs.Int("target", 8<<10, "group target")
	prods := fs.String("prod", "leveldb,rocksdb,pebble", "producers")
	perObj := fs.Int64("perobject", 96, "overhead charged per new object")
	fs.Parse(args)
	h := fstate.Human
	fmt.Printf("| producer | state | new objects | component bytes | packed components | representation objects | members/recipe | packed total + %d B/object | CDC (repz3 concat) packed + %d B/object |\n|---|---|---:|---:|---:|---:|---:|---:|---:|\n", *perObj, *perObj)
	for _, prod := range strings.Split(*prods, ",") {
		dir := filepath.Join(*root, prod, "chain")
		sts, err := loadFamily(dir, revNames(dir))
		if err != nil {
			return err
		}
		st, stc := repz3.NewMemStore(), repz3.NewMemStore()
		var tot, totc int64
		for _, s := range sts {
			var objs int
			var compB, packed, recB, memB, cdcPacked int64
			var cdcObjs int
			for _, f := range s.tables {
				var cut sstz3.Cutter
				if sstz3.Form(*form) != sstz3.FormBlocks {
					cut = &sstz3.HashCutter{Target: *target, Max: 4 * *target}
				}
				b, _, _, err := kinds.EncodeSST(bytes.NewReader(f.data), int64(len(f.data)), sstz3.Options{Form: sstz3.Form(*form), Cutter: cut})
				if err != nil {
					return err
				}
				id, added, _ := b.Persist(st)
				env := kinds.NewEnv(st)
				if err := proveOnce(env, id, f.data); err != nil {
					return err
				}
				for _, a := range added {
					objs++
					ob := st.Bytes(a)
					if a == id {
						recB += int64(len(ob))
						memB += int64(len(b.Rep.Recipe)) + int64(len(ob)-len(b.Rep.Recipe))
						packed += lab.PackedLen(ob)
						continue
					}
					compB += int64(len(ob))
					packed += lab.PackedLen(ob)
				}
				cb, _ := kinds.CDC(f.data)
				_, cadded, _ := cb.Persist(stc)
				for _, a := range cadded {
					cdcObjs++
					cdcPacked += lab.PackedLen(stc.Bytes(a))
				}
			}
			t := packed + *perObj*int64(objs)
			tc := cdcPacked + *perObj*int64(cdcObjs)
			tot += t
			totc += tc
			fmt.Printf("| %s | %s | %d | %s | %s | %s | %s | %s | %s |\n", prod, s.rev, objs, h(compB), h(packed-lab.PackedLen(nil)), h(recB), h(memB), h(t), h(tc))
		}
		fmt.Printf("| %s | **cumulative** | | | | | | **%s** | **%s** |\n", prod, h(tot), h(totc))
	}
	return nil
}

func proveOnce(env *repz3.Env, id repz3.SHA, want []byte) error {
	var out bytes.Buffer
	n, _, err := env.Reconstruct(id, &out)
	if err != nil {
		return err
	}
	if n != int64(len(want)) || !bytes.Equal(out.Bytes(), want) {
		return fmt.Errorf("reconstruction differs")
	}
	return nil
}
