package main

import (
	"bytes"
	"flag"
	"fmt"
	"strings"
	"time"

	"zeros3-z3/ir"
	"zeros3-z3/lab"
)

// producersCmd: Z3-08B. One row per (producer, level) family of a corpus built
// by `z3corpus extreal|ext`: the profile the chooser picked, correction cost of
// the whole stream and of 16 KiB units, CDC / Preflate / predictive incremental
// cost over update revisions, entry cost and throughput.
func producersCmd(args []string) {
	fs := flag.NewFlagSet("producers", flag.ExitOnError)
	dir, fams := commonFlags(fs)
	refB := fs.Int64("ref-bytes", 35, "recipe bytes per component reference")
	jsonOut := fs.String("json", "", "save the lab report here")
	from := fs.String("from", "", "reuse a lab report written with -json")
	fs.Parse(args)
	if *dir == "" {
		fatal(fmt.Errorf("-corpus is required"))
	}
	var rep *lab.Report
	var err error
	if *from != "" {
		if rep, err = lab.ReadJSON(*from); err != nil {
			fatal(err)
		}
	} else {
		cfg := lab.DefaultRunConfig(*dir)
		cfg.Reps, cfg.RefBytes, cfg.RangeReads = []string{"cdc", "preflate", "oracle-logical", "irp-tok16k"}, *refB, 0
		cfg.Families = split(*fams)
		if rep, err = lab.Run(cfg); err != nil {
			fatal(err)
		}
		if *jsonOut != "" {
			if err := lab.WriteJSON(*jsonOut, rep); err != nil {
				fatal(err)
			}
		}
	}
	agg, order := aggregate(rep, *refB, rep.RefBytes)
	ins, _, cl, err := lab.FamilyInputs(*dir, split(*fams), lab.DefaultOptions())
	if err != nil {
		fatal(err)
	}
	defer cl.Close()
	fmt.Printf("Producer matrix, %d B/ref. Stream columns: r0, auto profile, %% of the compressed stream. Cost columns: update revisions r1..rN, packed, %% of their compressed bytes.\n\n", *refB)
	fmt.Println("| family | comp MiB | profile | whole-stream % | tok16K % | enc MiB/s | dec MiB/s | CDC incr % | Preflate incr % | Z3 predictive incr % | plaintext bound % | Z3 entry (r0) % | Preflate entry % |")
	fmt.Println("|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, fam := range order {
		row := map[string]*famRow{}
		for _, a := range agg[fam] {
			row[a.rep] = a
		}
		in := ins[fam]
		profile, mono, tok16, enc, dec, comp := "n/a", "-", "-", "-", "-", int64(0)
		if len(in) > 0 {
			if regs, err := in[0].StreamRegions(); err == nil {
				for _, rg := range regs {
					if !rg.Stream {
						continue
					}
					s, err := in[0].ParseStream(rg.Off, rg.Len)
					if err != nil {
						profile = "parse error"
						break
					}
					st := s.Stats()
					comp = int64(st.CompressedBytes)
					t0 := time.Now()
					pk, err := ir.Encode(s, ir.Config{Codec: ir.Pred, Policy: ir.Policy{Kind: ir.PStream}, Profile: ir.AutoProfile})
					if err != nil {
						profile = "encode error"
						break
					}
					profile = ir.Profiles[pk.Skel.Profile].Name
					mono = pct(int64(pk.TotalUnitBytes()+len(pk.Skeleton)), comp)
					t0 = time.Now()
					pk2, err := ir.Encode(s, ir.Config{Codec: ir.Pred, Policy: ir.Policy{Kind: ir.PTokRun, Target: 16 << 10}, Profile: pk.Skel.Profile})
					d := time.Since(t0)
					if err == nil {
						tok16 = pct(int64(pk2.TotalUnitBytes()+len(pk2.Skeleton)), comp)
						enc = fmt.Sprintf("%.1f", float64(st.PlainBytes)/(1<<20)/d.Seconds())
						var out bytes.Buffer
						t0 = time.Now()
						if err := ir.Reconstruct(&out, pk2.Skeleton, func(i int) ([]byte, error) { return pk2.Units[i], nil }, s.Plain, ir.DecodeOptions{}); err != nil {
							fatal(err)
						}
						dec = fmt.Sprintf("%.1f", float64(st.PlainBytes)/(1<<20)/time.Since(t0).Seconds())
					}
					break
				}
			}
			in[0].DropParsed()
		}
		get := func(rep string) (inc, entry string) {
			a := row[rep]
			if a == nil || a.revs == 0 {
				return "-", "-"
			}
			return pct(a.packed, a.logical), pct(a.base, a.baseLogical)
		}
		cdc, _ := get("cdc")
		pf, pfE := get("preflate")
		z3, z3E := get("irp-tok16k")
		lb, _ := get("oracle-logical")
		fmt.Printf("| %s | %.1f | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", strings.TrimPrefix(fam, "extreal-"), float64(comp)/(1<<20), profile, mono, tok16, enc, dec, cdc, pf, z3, lb, z3E, pfE)
	}
}
