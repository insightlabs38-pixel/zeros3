package main

import (
	"flag"
	"fmt"

	"zeros3-z3/gitz3"
	"zeros3-z3/lab"
)

// cmdMapping measures what the native object id costs: the SHA-1 -> ZeroS3
// SHA-256 mapping, and when a SHA-256 repository's object id already is the
// component identity.
func cmdMapping(args []string) error {
	var c common
	fs := flag.NewFlagSet("mapping", flag.ExitOnError)
	c.flags(fs)
	fs.Parse(args)
	w, err := c.setup()
	if err != nil {
		return err
	}
	fmt.Printf("%s\n\n", gitz3.GitVersion())
	fmt.Println("| object format | pack | objects | pack B | .idx B (B/obj) | native-id mapping if ids are not SHA-256 of the component (B) | id == component SHA-256 | not equal: inline / chunked / delta entries | gp-set table ids column (B) |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---|---:|")
	var fam gitz3.Family
	for _, f := range gitz3.Families() {
		if f.Name == "add-small" {
			fam = f
		}
	}
	h := gitz3.FamilyHistory(fam, c.seed, c.scale, c.prefix)
	for _, a := range []gitz3.Algo{gitz3.SHA1, gitz3.SHA256} {
		for _, sp := range []gitz3.RepackSpec{gitz3.SpecDefault, gitz3.SpecNoDelta} {
			r, err := gitz3.Build(w, "mp", a, h, sp)
			if err != nil {
				return err
			}
			pp, _ := r.PackPairs()
			p, err := gitz3.ParsePack(pp[0].Pack, a, gitz3.Limits{})
			if err != nil {
				return err
			}
			if _, err := p.Resolve(); err != nil {
				return err
			}
			an, err := gitz3.AnalyzePack(p)
			if err != nil {
				return err
			}
			ne := an.NativeEquality(gitz3.RepOpts{Mode: "plain"})
			n := int64(len(p.Entries))
			mapB := int64(0)
			if a == gitz3.SHA1 {
				mapB = n * (20 + 32)
			}
			fmt.Printf("| %v | %s | %d | %d | %d (%.0f) | %d | %d (%.0f%%) | %d / %d / %d | %d |\n", a, sp.Label, n, len(pp[0].Pack), len(pp[0].Idx), float64(len(pp[0].Idx))/float64(n), mapB, ne.Equal, 100*float64(ne.Equal)/float64(n), ne.Inline, ne.Chunked, ne.DeltaEntries, n*int64(a.Size()))
		}
	}
	// recipe overhead: what each layout charges per entry, in bytes, for one first-ingest pack
	fmt.Println("\nExact recipe overhead per entry (first ingest of one pack; auto mode; table layout = 32 B digest + varint length per distinct component, varint index per reference):")
	fmt.Println("\n| object format | pack | entries | seq: recipe B | seq: meta B | seq: B/entry | set: section refs B | set: meta incl. permutation B | set: perm B | set: table B (stored as components) | set: B/entry (refs+meta) | set: table B/entry | sections |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, a := range []gitz3.Algo{gitz3.SHA1, gitz3.SHA256} {
		for _, sp := range []gitz3.RepackSpec{gitz3.SpecDefault} {
			r, err := gitz3.Build(w, "mp", a, h, sp)
			if err != nil {
				return err
			}
			pp, _ := r.PackPairs()
			p, err := gitz3.ParsePack(pp[0].Pack, a, gitz3.Limits{})
			if err != nil {
				return err
			}
			p.Resolve()
			an, err := gitz3.AnalyzePack(p)
			if err != nil {
				return err
			}
			n := float64(len(p.Entries))
			acS := lab.NewAcct(96, false)
			plS, _ := an.SeqPlan(gitz3.RepOpts{Mode: "auto"})
			accS, _ := acS.Add(plS)
			acT := lab.NewAcct(96, false)
			plT, st := an.SetPlan(gitz3.RepOpts{Mode: "auto"})
			accT, _ := acT.Add(plT)
			fmt.Printf("| %v | %s | %d | %d | %d | %.1f | %d | %d | %d | %d | %.1f | %.1f | %d |\n", a, sp.Label, len(p.Entries), accS.Layouts["table"], plS.MetaBytes, float64(accS.Layouts["table"]+plS.MetaBytes)/n, accT.Layouts["table"], plT.MetaBytes, st.PermBytes, st.TableBytes, float64(accT.Layouts["table"]+plT.MetaBytes)/n, float64(st.TableBytes)/n, st.TableChunks)
		}
	}
	return nil
}
