package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"zeros3-z3/deflate"
	"zeros3-z3/ir"
	"zeros3-z3/lab"
)

type entAgg struct {
	streams                       int
	comp, plain                   int64
	tokens, matches               int64
	naive, e1, e1f, e2, skel      int64
	tokBitsHuff                   int64
	cost                          [ir.NumCats]float64
	ent                           ir.Bits
	rep                           map[int]float64
	hist                          ir.Entropy
	e2Dur, e1Dur                  time.Duration
	e2Dec                         time.Duration
	e2Tok16k, e2Tok16kUnits       int64
	e2Tok4k, e2Tok4kUnits         int64
	e2Tok2k, e2Tok2kUnits         int64
	e2Block                       int64
	dynBlocks, dynHdrBits, blocks int64
}

func entropyCmd(args []string) {
	fs := flag.NewFlagSet("entropy", flag.ExitOnError)
	dir, fams := commonFlags(fs)
	fs.Parse(args)
	if *dir == "" {
		fatal(fmt.Errorf("-corpus is required"))
	}
	ins, order, cl, err := lab.FamilyInputs(*dir, split(*fams), lab.DefaultOptions())
	if err != nil {
		fatal(err)
	}
	defer cl.Close()
	by := map[string]*entAgg{}
	for _, fam := range order {
		a := &entAgg{rep: map[int]float64{}}
		by[fam] = a
		for _, in := range ins[fam] {
			regs, err := in.StreamRegions()
			if err != nil {
				break
			}
			for _, rg := range regs {
				if !rg.Stream {
					continue
				}
				s, err := in.ParseStream(rg.Off, rg.Len)
				if err != nil {
					continue
				}
				st := s.Stats()
				if st.Tokens == 0 {
					continue
				}
				a.streams++
				a.comp += int64(st.CompressedBytes)
				a.plain += int64(st.PlainBytes)
				a.tokens += int64(st.Tokens)
				a.matches += int64(st.Matches)
				a.naive += int64(st.NaiveIRBytes)
				a.tokBitsHuff += st.TokenBits
				a.dynBlocks += int64(st.DynamicBlocks)
				a.dynHdrBits += st.DynHeaderBits + 3*int64(st.DynamicBlocks)
				a.blocks += int64(st.Blocks)
				t0 := time.Now()
				p1, _ := ir.Encode(s, ir.Config{Codec: ir.E1, Policy: ir.Policy{Kind: ir.PStream}})
				a.e1Dur += time.Since(t0)
				a.e1 += int64(p1.TotalUnitBytes() + len(p1.Skeleton))
				p1f, _ := ir.Encode(s, ir.Config{Codec: ir.E1Flate, Policy: ir.Policy{Kind: ir.PStream}})
				a.e1f += int64(p1f.TotalUnitBytes() + len(p1f.Skeleton))
				t0 = time.Now()
				p2, _ := ir.Encode(s, ir.Config{Codec: ir.E2, Policy: ir.Policy{Kind: ir.PStream}, Cost: &a.cost})
				a.e2Dur += time.Since(t0)
				a.e2 += int64(p2.TotalUnitBytes() + len(p2.Skeleton))
				a.skel += int64(len(p2.Skeleton))
				t0 = time.Now()
				var sink nullW
				_ = ir.Reconstruct(&sink, p2.Skeleton, func(i int) ([]byte, error) { return p2.Units[i], nil }, s.Plain, ir.DecodeOptions{})
				a.e2Dec += time.Since(t0)
				for _, c := range []struct {
					pol     ir.Policy
					dst, du *int64
				}{
					{ir.Policy{Kind: ir.PTokRun, Target: 16 << 10}, &a.e2Tok16k, &a.e2Tok16kUnits},
					{ir.Policy{Kind: ir.PTokRun, Target: 4 << 10}, &a.e2Tok4k, &a.e2Tok4kUnits},
					{ir.Policy{Kind: ir.PTokRun, Target: 2 << 10}, &a.e2Tok2k, &a.e2Tok2kUnits},
				} {
					pk, _ := ir.Encode(s, ir.Config{Codec: ir.E2, Policy: c.pol})
					*c.dst += int64(pk.TotalUnitBytes() + len(pk.Skeleton))
					*c.du += int64(len(pk.Units))
				}
				pb, _ := ir.Encode(s, ir.Config{Codec: ir.E2, Policy: ir.Policy{Kind: ir.PBlock}})
				a.e2Block += int64(pb.TotalUnitBytes() + len(pb.Skeleton))
				e := ir.Analyze(s)
				b := e.StaticBits()
				a.ent.LitRun += b.LitRun
				a.ent.LenSym += b.LenSym
				a.ent.LenExtra += b.LenExtra
				a.ent.DistSym += b.DistSym
				a.ent.DistExtra += b.DistExtra
				a.ent.Order0Total += b.Order0Total
				a.ent.Cond += b.Cond
				for d, v := range b.RepModel {
					a.rep[d] += v
				}
				a.hist.Matches += e.Matches
				for k := range e.RepHits {
					a.hist.RepHits[k] += e.RepHits[k]
				}
				a.hist.NearbyDelta += e.NearbyDelta
			}
			in.DropParsed()
		}
	}
	fmt.Println("### Size of the exact IR (monolithic; skeleton included), % of compressed bytes")
	fmt.Println("| family | comp MiB | tokens | matches/token | Huffman bits/token | naive | E1 | E1+flate | E2 | E2 bits/token | E2 bits/match | skeleton B |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, fam := range order {
		a := by[fam]
		if a.tokens == 0 {
			continue
		}
		fmt.Printf("| %s | %.2f | %d | %.2f | %.2f | %s | %s | %s | %s | %.2f | %.2f | %d |\n", fam, float64(a.comp)/(1<<20), a.tokens,
			float64(a.matches)/float64(a.tokens), float64(a.tokBitsHuff)/float64(a.tokens), pct(a.naive, a.comp), pct(a.e1, a.comp), pct(a.e1f, a.comp), pct(a.e2, a.comp),
			float64(a.e2)*8/float64(a.tokens), float64(a.e2)*8/float64(max(a.matches, 1)), a.skel/int64(a.streams))
	}
	fmt.Println("\n### E2 cost per category (bits per match) and static entropies (bits per match)")
	fmt.Println("| family | E2 litrun | E2 lensym | E2 lenextra | E2 rep flags | E2 distsym | E2 distextra | order-0 static total | + context | + MTF4 cache | + MTF16 cache | dist hit MTF1/4/16 % | dist within ±16 of previous % |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---:|")
	for _, fam := range order {
		a := by[fam]
		if a.matches == 0 {
			continue
		}
		m := float64(a.matches)
		h1, h4, h16 := 0, 0, 0
		for i, v := range a.hist.RepHits {
			if i < 1 {
				h1 += v
			}
			if i < 4 {
				h4 += v
			}
			h16 += v
		}
		fmt.Printf("| %s | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.2f | %.0f/%.0f/%.0f | %.1f |\n", fam,
			a.cost[ir.CatLitRun]/m, a.cost[ir.CatLenSym]/m, a.cost[ir.CatLenExtra]/m, a.cost[ir.CatRep]/m, a.cost[ir.CatDistSym]/m, a.cost[ir.CatDistExtra]/m,
			a.ent.Order0Total/m, a.ent.Cond/m, a.rep[4]/m, a.rep[16]/m,
			100*float64(h1)/m, 100*float64(h4)/m, 100*float64(h16)/m, 100*float64(a.hist.NearbyDelta)/m)
	}
	fmt.Println("\n### Block-local variants of E2 (% of compressed bytes, skeleton + all units; no cross-unit sharing)")
	fmt.Println("| family | mono | block | tok16K | tok4K | tok2K | units tok2K |")
	fmt.Println("|---|---:|---:|---:|---:|---:|---:|")
	for _, fam := range order {
		a := by[fam]
		if a.tokens == 0 {
			continue
		}
		fmt.Printf("| %s | %s | %s | %s | %s | %s | %d |\n", fam, pct(a.e2, a.comp), pct(a.e2Block, a.comp), pct(a.e2Tok16k, a.comp), pct(a.e2Tok4k, a.comp), pct(a.e2Tok2k, a.comp), a.e2Tok2kUnits/int64(a.streams))
	}
	fmt.Println("\n### Block table (skeleton) and dynamic Huffman headers, exact")
	fmt.Println("| family | blocks/stream | dynamic header bytes/block (DEFLATE bits) | skeleton bytes/block | skeleton % of compressed |")
	fmt.Println("|---|---:|---:|---:|---:|")
	for _, fam := range order {
		a := by[fam]
		if a.tokens == 0 || a.blocks == 0 {
			continue
		}
		hb := "-"
		if a.dynBlocks > 0 {
			hb = fmt.Sprintf("%.0f", float64(a.dynHdrBits)/8/float64(a.dynBlocks))
		}
		fmt.Printf("| %s | %.1f | %s | %.0f | %s |\n", fam, float64(a.blocks)/float64(a.streams), hb, float64(a.skel)/float64(a.blocks), pct(a.skel, a.comp))
	}
	fmt.Println("\n### Speed (single thread, MiB/s of plaintext)")
	fmt.Println("| family | E1 encode | E2 encode | E2 decode+replay+CRC |")
	fmt.Println("|---|---:|---:|---:|")
	for _, fam := range order {
		a := by[fam]
		if a.tokens == 0 {
			continue
		}
		r := func(d time.Duration) string { return fmt.Sprintf("%.0f", float64(a.plain)/(1<<20)/d.Seconds()) }
		fmt.Printf("| %s | %s | %s | %s |\n", fam, r(a.e1Dur), r(a.e2Dur), r(a.e2Dec))
	}
	_ = deflate.Stored
	_ = os.Stdout
}

type nullW struct{}

func (nullW) Write(b []byte) (int, error) { return len(b), nil }
