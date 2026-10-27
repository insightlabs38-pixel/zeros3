package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// cmdLogical is the differential check against the workload's independent model: the live
// logical state visible in the parsed tables of every revision (newest sequence number per
// user key, deletions removed) must have exactly the entry count and key+value bytes the
// generator computed for that revision. The tables come from the producers, the model does
// not, so agreement says the native parser sees what the engines wrote.
func cmdLogical(args []string) error {
	fs := flag.NewFlagSet("logical", flag.ExitOnError)
	root := fs.String("corpus", filepath.Join(os.Getenv("HOME"), ".cache", "z3-sst", "corpus"), "corpus root")
	fs.Parse(args)
	bad, total := 0, 0
	for _, prod := range []string{"leveldb", "rocksdb", "pebble"} {
		for _, fam := range []string{"chain", "variants"} {
			dir := filepath.Join(*root, prod, fam)
			sts, err := loadFamily(dir, revNames(dir))
			if err != nil {
				return err
			}
			for _, s := range sts {
				type ent struct {
					seq  uint64
					kind byte
					n    int
				}
				live := map[string]ent{}
				for _, f := range s.tables {
					err := recordsOf(f, func(uk []byte, tr uint64, v []byte) error {
						k := string(uk)
						e := ent{tr >> 8, byte(tr), len(uk) + len(v)}
						if o, ok := live[k]; !ok || e.seq > o.seq {
							live[k] = e
						}
						return nil
					})
					if err != nil {
						return fmt.Errorf("%s/%s/%s %s: %w", prod, fam, s.rev, f.name, err)
					}
				}
				var n int
				var b int64
				for _, e := range live {
					if e.kind == 1 {
						n++
						b += int64(e.n)
					}
				}
				if prod == "pebble" { // table format Pebblev3+ stores a one-byte value prefix in every value
					b -= int64(n)
				}
				total++
				ok := n == s.meta.Entries && b == s.meta.Logical
				if !ok {
					bad++
				}
				fmt.Printf("%-8s %-8s %-20s entries %7d (model %7d) logical %10d (model %10d) %v\n", prod, fam, s.rev, n, s.meta.Entries, b, s.meta.Logical, map[bool]string{true: "ok", false: "MISMATCH"}[ok])
			}
		}
	}
	fmt.Printf("%d revisions, %d mismatching\n", total, bad)
	if bad > 0 {
		return fmt.Errorf("%d revisions differ from the model", bad)
	}
	return nil
}

func recordsOf(f file, fn func(uk []byte, tr uint64, v []byte) error) error {
	return sstRecords(bytes.NewReader(f.data), int64(len(f.data)), fn)
}
