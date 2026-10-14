package main

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zeros3-z3/binref"
)

// runVerify reconstructs every binary of a corpus under several
// representations and compares length, SHA-256 and bytes (normRep does the
// comparison and fails the row otherwise).
func runVerify(args []string) {
	f := flag.NewFlagSet("verify", flag.ExitOnError)
	dir := f.String("corp", "", "corpus directory")
	f.Parse(args)
	cfgs := []cfg{
		{setFull, binref.IdentSymbol, "auto"},
		{setFull, binref.IdentContent, "call"},
		{setFull, binref.IdentOrdinal, "pcln"},
		{setX86, binref.IdentRegionRel, "auto"},
		{setReloc, binref.IdentSymbol, "sym"},
	}
	var files, rows, refs int
	var bytesTotal int64
	t0 := time.Now()
	filepath.WalkDir(*dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, ".json") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			fatal(err)
		}
		b, err := loadBin(p, data)
		if err != nil {
			fatal(fmt.Errorf("%s: %v", p, err))
		}
		files++
		bytesTotal += int64(len(data))
		for _, c := range cfgs {
			r, err := normRep(b, c, false)
			if err != nil {
				fatal(err)
			}
			rows++
			refs += r.Refs
		}
		return nil
	})
	fmt.Printf("verified %d binaries (%.0f MiB) x %d representations = %d exact reconstructions (length, SHA-256, bytes); %d references normalized in total; %.0fs\n",
		files, float64(bytesTotal)/(1<<20), len(cfgs), rows, refs, time.Since(t0).Seconds())
}
