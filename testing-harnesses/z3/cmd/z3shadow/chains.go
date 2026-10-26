package main

import (
	"fmt"
	"path/filepath"

	"zeros3-z3/carz3"
	"zeros3-z3/prodshadow"
	"zeros3-z3/repz3/kinds"
)

type config struct {
	b       *prodshadow.Binaries
	work    string
	packMiB int
	full    bool
	only    []string
}

func (c config) want(name string) bool {
	if len(c.only) == 0 || c.only[0] == "" {
		return true
	}
	for _, o := range c.only {
		if o == name {
			return true
		}
	}
	return false
}

// workloads: SQLite, gzip+tar+DERIVED, Git repack, CAR.
func (c config) workloads() ([]*kinds.Chain, error) {
	sqlitePages, gzPages, gitScale := 3000, 600, 3
	car := carz3.TinyBlocks
	if c.full {
		sqlitePages, gzPages, gitScale, car = 12000, 2500, 6, carz3.BigBlocks
	}
	out := []*kinds.Chain{kinds.SQLiteChain(sqlitePages), kinds.GzipCompoundChain("gzip+tar+derived", gzPages), kinds.CompoundChain(gzPages / 2)}
	if g, ok, err := kinds.GitRepackChain(filepath.Join(c.work, "git"), gitScale); err != nil {
		return nil, err
	} else if ok {
		out = append(out, g)
	}
	out = append(out, kinds.CARChains(car)[0])
	var keep []*kinds.Chain
	for _, ch := range out {
		if c.want(ch.Name) {
			keep = append(keep, ch)
		}
	}
	return keep, nil
}

func mib(n int64) string { return fmt.Sprintf("%.2f", float64(n)/(1<<20)) }

func (c config) chains() {
	chs, err := c.workloads()
	must(err)
	for _, ch := range chs {
		res, err := prodshadow.RunChain(c.b, ch, prodshadow.Options{}, filepath.Join(c.work, "chain-"+filepath.Base(ch.Name)), c.packMiB)
		must(err)
		fmt.Printf("== %s (MiB; packed = production compaction, %d MiB packs)\n", ch.Name, c.packMiB)
		fmt.Printf("%-18s %6s | CDC %6s %6s | shadow-only %6s %6s %6s | CDC+shadow %6s %6s | incr. packed CDC / shadow | meta B | new/cdc/reuse | recs raw+defl | encode/stage\n",
			"stage", "size", "loose", "packed", "loose", "auto", "raw", "loose", "packed")
		var pc, ps, hind float64
		for _, r := range res.Rows {
			cp := float64(r.C.PackedAuto.ChunkStoreFileBytes)
			sp := float64(r.SP.Total(r.SP.PackedAuto))
			dc, ds := cp-pc, sp-ps
			pc, ps = cp, sp
			h := dc
			if r.Shadowed && ds < dc {
				h = ds
			}
			hind += h
			fmt.Printf("%-18s %6s | CDC %6s %6s | shadow-only %6s %6s %6s | CDC+shadow %6s %6s | %6.2f / %6.2f | %6d | %d/%d/%d | %d+%d | %v/%v\n", r.Label, mib(r.Bytes),
				mib(r.C.Stats.ChunkStoreFileBytes), mib(r.C.PackedAuto.ChunkStoreFileBytes),
				mib(r.SP.Total(r.SP.Stats)), mib(r.SP.Total(r.SP.PackedAuto)), mib(r.SP.Total(r.SP.PackedRaw)),
				mib(r.QP.Total(r.QP.Stats)), mib(r.QP.Total(r.QP.PackedAuto)),
				dc/(1<<20), ds/(1<<20), r.QP.MetaBytes, r.Q.ChunksNew, r.Q.ChunksExistedCDC, r.Q.ChunksReuse,
				r.SP.PackedAuto.PackedRawRecords, r.SP.PackedAuto.PackedCompressedRecs, r.Encode.Round(1e6), r.ShadowQ.Round(1e6))
			if r.Err != "" {
				fmt.Println("   shadow not built:", r.Err)
			}
		}
		fmt.Printf("   chain totals: CDC-only %.2f, shadow-only %.2f, CDC+shadow %.2f, hindsight cheapest per revision %.2f MiB\n",
			pc/(1<<20), ps/(1<<20), float64(res.Rows[len(res.Rows)-1].QP.Total(res.Rows[len(res.Rows)-1].QP.PackedAuto))/(1<<20), hind/(1<<20))
	}
}
