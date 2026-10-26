package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/frontier"
	"zeros3-z3/lab"
	"zeros3-z3/zstdz3"
)

// cmdUnits parses every revision once and keeps the description (skeleton, unit stream,
// unit sizes) so unitization variants can be compared without re-parsing.
func cmdUnits(args []string) {
	fs := flag.NewFlagSet("units", flag.ExitOnError)
	dir := fs.String("dir", "/root/z3scratch/c25", "gen output directory")
	tag := fs.String("tag", "z155l3", "producer tag")
	revs := fs.String("revs", strings.Join(frontier.Revisions, ","), "revisions")
	fs.Parse(args)
	for _, rev := range strings.Split(*revs, ",") {
		zst, err := os.ReadFile(filepath.Join(*dir, rev+"."+*tag+".zst"))
		must(err)
		var plain, units bytes.Buffer
		res, err := zstdz3.Parse(bytes.NewReader(zst), &plain, &units, zstdz3.Options{})
		must(err)
		base := filepath.Join(*dir, rev+"."+*tag)
		must(os.WriteFile(base+".skel", res.Raw, 0o644))
		must(os.WriteFile(base+".units", units.Bytes(), 0o644))
		var sz []byte
		for _, n := range res.UnitSizes {
			sz = binary.AppendUvarint(sz, uint64(n))
		}
		must(os.WriteFile(base+".usizes", sz, 0o644))
		fmt.Fprintf(os.Stderr, "units %s: %d bytes\n", rev, units.Len())
	}
}

// gearCDC cuts data with the production algorithm's shape at configurable sizes
// (min = target/4, max = target*4; masks scale with the target as in production).
func gearCDC(data []byte, target int, fn func(chunk []byte)) {
	k := 0
	for 1<<k < target {
		k++
	}
	minS, maxS := target/4, target*4
	maskS, maskL := uint64(1)<<k-1, uint64(1)<<(k-1)-1
	for len(data) > 0 {
		n := len(data)
		limit := min(n, maxS)
		var h uint64
		cut := n
		for i := 0; i < limit; i++ {
			h = h<<1 + lab.GearAt(data[i])
			size := i + 1
			if size >= maxS {
				cut = size
				break
			}
			if size < minS {
				continue
			}
			mask := maskL
			if size < target {
				mask = maskS
			}
			if h&mask == 0 {
				cut = size
				break
			}
		}
		fn(data[:cut])
		data = data[cut:]
	}
}

// cmdUnitSim costs the reconstruction units under several unitizations: every new
// unit is charged its pack-rule bytes, every reference 36 bytes (a repz3 member).
func cmdUnitSim(args []string) {
	fs := flag.NewFlagSet("unitsim", flag.ExitOnError)
	dir := fs.String("dir", "/root/z3scratch/c25", "gen output directory")
	tag := fs.String("tag", "z155l3", "producer tag")
	fs.Parse(args)
	sizes := []int{1 << 10, 2 << 10, 4 << 10, 8 << 10, 16 << 10, 64 << 10}
	seen := make([]map[[32]byte]bool, len(sizes))
	for i := range seen {
		seen[i] = map[[32]byte]bool{}
	}
	fmt.Printf("%-5s", "rev")
	for _, s := range sizes {
		fmt.Printf("%14s", fmt.Sprintf("cdc~%dK", s>>10))
	}
	fmt.Println()
	tot := make([]float64, len(sizes))
	mid := make([]float64, len(sizes))
	for _, rev := range frontier.Revisions {
		u, err := os.ReadFile(filepath.Join(*dir, rev+"."+*tag+".units"))
		must(err)
		fmt.Printf("%-5s", rev)
		for i, s := range sizes {
			var cost int64
			var n int
			gearCDC(u, s, func(c []byte) {
				n++
				id := sha256.Sum256(c)
				cost += 36
				if !seen[i][id] {
					seen[i][id] = true
					cost += lab.PackedLen(c)
				}
			})
			fmt.Printf("%9s/%4d", hum(float64(cost)), n)
			tot[i] += float64(cost)
			if rev != "R0" && rev != "R11" {
				mid[i] += float64(cost)
			}
		}
		fmt.Println()
	}
	fmt.Printf("%-5s", "R1-10")
	for i := range sizes {
		fmt.Printf("%14s", hum(mid[i]))
	}
	fmt.Printf("\n%-5s", "cum")
	for i := range sizes {
		fmt.Printf("%14s", hum(tot[i]))
	}
	fmt.Println()
}
