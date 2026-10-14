package main

import (
	"flag"
	"fmt"

	"zeros3-z3/eraez3"
	"zeros3-z3/lab"
)

var eraCols = []string{"cdc", "era-entry", "era-entry-i64", "era-plain"}

func printEra(name, note string, revs []lab.NamedBytes) {
	rows := lab.RunBytesFamilyPacked(name, "erae", revs, lab.EraReps(), true)
	by := map[string][]lab.FRow{}
	for _, r := range rows {
		if r.Status == "invalid" || r.Status == "error" || (r.Status == "ok" && !r.Exact) {
			fatal(fmt.Errorf("%s %s %s: %s %s", name, r.Rep, r.Label, r.Status, r.Err))
		}
		by[r.Rep] = append(by[r.Rep], r)
	}
	fmt.Printf("\n#### %s - %s\n\nCost per revision, KiB (%% of the file); first value = raw components, second = with production pack compression (DEFLATE per component).\n\n| revision | size KiB |", name, note)
	for _, c := range eraCols {
		fmt.Printf(" %s |", c)
	}
	fmt.Printf("\n|---|--:|")
	for range eraCols {
		fmt.Printf("--:|")
	}
	fmt.Println()
	var sizeSum int64
	for i, rv := range revs {
		sizeSum += int64(len(rv.Data))
		fmt.Printf("| %s | %s |", rv.Label, kib(int64(len(rv.Data))))
		for _, c := range eraCols {
			r := by[c][i]
			if r.Status != "ok" {
				fmt.Printf(" %s |", r.Status)
				continue
			}
			fmt.Printf(" %s (%.1f%%) / %s (%.1f%%) |", kib(r.Cost), 100*float64(r.Cost)/float64(r.Size), kib(r.CostPacked), 100*float64(r.CostPacked)/float64(r.Size))
		}
		fmt.Println()
	}
	fmt.Printf("| **cumulative** | %s |", kib(sizeSum))
	for _, c := range eraCols {
		var a, b int64
		for _, r := range by[c] {
			a += r.Cost
			b += r.CostPacked
		}
		fmt.Printf(" %s (%.1f%%) / %s (%.1f%%) |", kib(a), 100*float64(a)/float64(sizeSum), kib(b), 100*float64(b)/float64(sizeSum))
	}
	fmt.Println()
}

func cmdFamilies(args []string) error {
	fs := flag.NewFlagSet("families", flag.ExitOnError)
	only := fs.String("only", "", "run one family: sepolia|mainnet|reslice|layout")
	fs.Parse(args)
	want := func(n string) bool { return *only == "" || *only == n }
	rev := func(name string) lab.NamedBytes { return lab.NamedBytes{Label: name, Data: load(name)} }

	if want("sepolia") {
		printEra("sepolia-adjacent", "four adjacent epochs of 8192 blocks (disjoint ranges)", []lab.NamedBytes{rev("sepolia-00000-8e3e7dc9.erae"), rev("sepolia-00001-c78ee2bd.erae"), rev("sepolia-00002-d3abbc5d.erae"), rev("sepolia-00003-47e8d303.erae")})
	}
	if want("mainnet") {
		printEra("mainnet-adjacent", "mainnet epochs 0 and 1 (pre-merge: total difficulty, accumulator)", []lab.NamedBytes{rev("mainnet-00000-a6860fef.erae"), rev("mainnet-00001-05c64fc4.erae")})
	}
	if want("reslice") {
		b0, acc0, s0, err := eraez3.ExtractBlocks(load("sepolia-00000-8e3e7dc9.erae"))
		if err != nil {
			return err
		}
		b1, _, _, err := eraez3.ExtractBlocks(load("sepolia-00001-c78ee2bd.erae"))
		if err != nil {
			return err
		}
		all := append(append([]eraez3.Block(nil), b0...), b1...)
		slice := func(lo, hi int, name string) lab.NamedBytes {
			return lab.NamedBytes{Label: name, Data: eraez3.Write(all[lo:hi], eraez3.WriteOpts{IndexStart: s0 + uint64(lo)})}
		}
		_ = acc0
		printEra("sepolia-reslice", "the same blocks re-exported over shifted ranges (grouped layout, accumulator omitted)", []lab.NamedBytes{
			rev("sepolia-00000-8e3e7dc9.erae"),
			slice(4096, 12288, "[4096,12288) re-export"),
			rev("sepolia-00001-c78ee2bd.erae"),
			slice(6144, 14336, "[6144,14336) re-export"),
			slice(0, 8192, "[0,8192) re-export (no accumulator)"),
		})
	}
	if want("layout") {
		f0 := load("sepolia-00000-8e3e7dc9.erae")
		b0, acc, s0, err := eraez3.ExtractBlocks(f0)
		if err != nil {
			return err
		}
		mk := func(name string, o eraez3.WriteOpts) lab.NamedBytes {
			o.IndexStart = s0
			if o.Accumulator == nil && !o.NoIndex {
				o.Accumulator = acc
			}
			return lab.NamedBytes{Label: name, Data: eraez3.Write(b0, o)}
		}
		printEra("sepolia-layout", "the same 8192 blocks written other ways (layout / total difficulty / framing / index)", []lab.NamedBytes{
			{Label: "grouped (as published)", Data: f0},
			mk("interleaved per block (Era1 style)", eraez3.WriteOpts{Interleaved: true}),
			mk("grouped, no total difficulty", eraez3.WriteOpts{NoTD: true}),
			mk("grouped, uncompressed Snappy chunks", eraez3.WriteOpts{Frame: eraez3.FrameEncodeRaw}),
			mk("grouped, index offsets +3 (other convention)", eraez3.WriteOpts{ShiftIndex: 3}),
			mk("grouped, no index", eraez3.WriteOpts{NoIndex: true}),
		})
	}
	return nil
}
