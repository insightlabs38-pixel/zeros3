// Command z3disk is the Z3-26 laboratory driver for QCOW2 / VHDX sparse and differencing images.
//
//	z3disk inspect IMAGE            structure summary of one image
//	z3disk famA | famB              Family A (rebuilt standalone) / Family B (parent chain) matrices -> $Z3_WORK/res/*.json
//	z3disk reads | det | parentpath | zerochunks | norm
//
// Environment: Z3_WORK scratch dir, Z3_LANES comma list of lanes (qcow2, qcow2-xl2, qcow2-zlib, vhdx-2M, vhdx-1M),
// Z3_LIMIT revisions for famA, Z3_KEEP images to retain. Needs qemu-img and the oracle/ helpers (oracle/disk_setup.sh).
package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"zeros3-z3/diskimg"
)

func summarize(regs []diskimg.Region) string {
	by := map[string][2]int64{}
	for _, r := range regs {
		v := by[r.Class]
		v[0]++
		v[1] += r.Len
		by[r.Class] = v
	}
	var ks []string
	for k := range by {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	s := ""
	for _, k := range ks {
		s += fmt.Sprintf(" %s=%d/%dKiB", k, by[k][0], by[k][1]>>10)
	}
	return s
}

func inspect(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, _ := f.Stat()
	if q, err := diskimg.ScanQcow2(f, st.Size()); err == nil {
		var c [4]int
		for _, e := range q.Map {
			c[e.State]++
		}
		fmt.Printf("qcow2 v%d cs=%d virt=%d extL2=%v comp=%d backing=%q map(unalloc,zero,data,comp)=%v\n  regions:%s\n", q.Version, q.ClusterSize, q.VirtSize, q.ExtendedL2, q.Compression, q.Backing, c, summarize(q.Regions))
		return nil
	}
	v, err := diskimg.ScanVhdx(f, st.Size())
	if err != nil {
		return err
	}
	var c [8]int
	part := 0
	for _, b := range v.Blocks {
		c[b.State]++
		if b.State == diskimg.VPartial {
			part += len(b.Present)
		}
	}
	fmt.Printf("vhdx block=%d lss=%d virt=%d parent=%v states=%v partialRuns=%d active=%d seq=%v\n  parent=%v\n  regions:%s\n", v.BlockSize, v.LSS, v.VirtSize, v.HasParent, c, part, v.ActiveHeader, v.Seq, v.Parent, summarize(v.Regions))
	return nil
}

func main() {
	switch os.Args[1] {
	case "famA":
		e := &Env{Work: os.Getenv("Z3_WORK"), Keep: map[string]bool{}}
		for _, k := range strings.Split(os.Getenv("Z3_KEEP"), ",") {
			e.Keep[k] = true
		}
		limit, _ := strconv.Atoi(os.Getenv("Z3_LIMIT"))
		var lanes []Lane
		for _, n := range strings.Split(os.Getenv("Z3_LANES"), ",") {
			for _, l := range []Lane{laneQcow, laneXL2, laneZlib, laneVhdx, laneV1M} {
				if l.Name == n {
					lanes = append(lanes, l)
				}
			}
		}
		if err := runFamilyA(e, lanes, limit); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "famB":
		e := &Env{Work: os.Getenv("Z3_WORK"), Keep: map[string]bool{}}
		var lanes []Lane
		for _, n := range strings.Split(os.Getenv("Z3_LANES"), ",") {
			for _, l := range []Lane{laneQcow, laneXL2, laneZlib, laneVhdx, laneV1M} {
				if l.Name == n {
					lanes = append(lanes, l)
				}
			}
		}
		if err := runFamilyB(e, lanes); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "reads":
		e := &Env{Work: os.Getenv("Z3_WORK")}
		if err := runReads(e, strings.Split(os.Getenv("Z3_LANES"), ",")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "det", "parentpath":
		e := &Env{Work: os.Getenv("Z3_WORK")}
		var lanes []Lane
		for _, n := range strings.Split(os.Getenv("Z3_LANES"), ",") {
			for _, l := range []Lane{laneQcow, laneXL2, laneZlib, laneVhdx, laneV1M} {
				if l.Name == n {
					lanes = append(lanes, l)
				}
			}
		}
		f := runDeterminism
		if os.Args[1] == "parentpath" {
			f = runParentPath
		}
		if err := f(e, lanes); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "zerochunks":
		for _, p := range os.Args[2:] {
			if err := zeroChunks(p); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
		}
	case "norm":
		if err := norm(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "inspect":
		if err := inspect(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func norm(args []string) error {
	parent := ""
	if len(args) > 2 {
		parent = args[2]
	}
	return diskimg.NormalizeVhdx(args[0], args[1], parent)
}
