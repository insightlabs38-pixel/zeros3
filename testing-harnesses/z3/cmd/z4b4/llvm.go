package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"

	"zeros3-z3/graphnorm"
	"zeros3-z3/semcas"
	"zeros3-z3/simdelta"
)

type chunkP struct{ min, tgt, max int }

var (
	fineP = chunkP{2 << 10, 8 << 10, 32 << 10}
	prodP = chunkP{16 << 10, 64 << 10, 256 << 10}
)

// graphCost charges an LLVMEncoding into a CAS: functions as whole objects, skeleton and id
// tables as CDC chunks, plus one reference per recipe step.
type breakdown struct{ fn, skel, idtab, refs int64 }

func graphCost(c *graphnorm.CAS, e *graphnorm.LLVMEncoding, sk chunkP) (int64, breakdown) {
	var inc int64
	var bd breakdown
	steps := 0
	for _, p := range e.Pieces {
		if p.Kind == 'F' {
			bd.fn += c.Put(p.Data)
			steps++
		} else {
			bd.skel += c.Chunked(p.Data, sk.min, sk.tgt, sk.max)
		}
	}
	bd.idtab = c.Chunked(e.IDTab, sk.min, sk.tgt, sk.max)
	bd.refs = c.Ref(steps) + c.Ref(1)
	inc = bd.fn + bd.skel + bd.idtab + bd.refs
	return inc, bd
}

// archiveCost charges an encoding with delta-capable components.
func archiveCost(a *graphnorm.Archive, e *graphnorm.LLVMEncoding, deltaRecipe bool) (inc int64, bd breakdown) {
	fi, si := 0, 0
	var recipe []byte
	steps := 0
	for _, p := range e.Pieces {
		sum := sha256.Sum256(p.Data)
		recipe = append(recipe, sum[:]...)
		steps++
		if p.Kind == 'F' {
			bd.fn += a.Put("f:"+e.FuncNames[fi], p.Data)
			fi++
		} else {
			bd.skel += a.Put(fmt.Sprintf("s:%d", si), p.Data)
			si++
		}
	}
	bd.idtab = a.Put("idtab", e.IDTab)
	rsum := sha256.Sum256(recipe)
	switch {
	case a.SeenRecipe(rsum):
		bd.refs = a.Ref(1) // identical artifact: only a root reference
	case deltaRecipe:
		bd.refs = a.Put("recipe", recipe) + a.Ref(1)
	default:
		bd.refs = a.Ref(steps+1) + a.Ref(1)
	}
	inc = bd.fn + bd.skel + bd.idtab + bd.refs
	return
}

func cmdLLVM(files []string) {
	var data [][]byte
	for _, f := range files {
		b, err := os.ReadFile(f)
		must(err)
		data = append(data, b)
	}
	prod, fine := semcas.ProdCDC(), semcas.FineCDC()
	none, all := graphnorm.NewCAS(), graphnorm.NewCAS()
	hyb, hybR, hybN := graphnorm.NewArchive(2), graphnorm.NewArchive(2), graphnorm.NewArchive(2)
	var b1Total int64
	fmt.Printf("%-28s %10s %10s %10s %10s %10s %10s %10s %10s\n", "revision", "prodCDC", "fineCDC", "B1best", "noNorm", "strong", "+delta", "+deltaRcp", "noN+delta")
	var cumP, cumF int64
	for i, d := range data {
		ip, ifn := prod.Add(d), fine.Add(d)
		cumP, cumF = prod.Total, fine.Total
		// B1: best of fine CDC increment and similarity deltas against the previous two revisions
		b1 := ifn
		if i > 0 {
			var bases [][]byte
			for j := max(0, i-2); j < i; j++ {
				bases = append(bases, data[j])
			}
			for _, alg := range []string{"adaptive", "git", "rolling"} {
				if w, err := simdelta.Encode(alg, bases, d); err == nil {
					if c := int64(simdelta.Physical(w)) + 36; c < b1 {
						b1 = c
					}
				}
			}
		}
		b1Total += b1
		en, err := graphnorm.EncodeLLVM(d, graphnorm.PolicyNone)
		must(err)
		ea, err := graphnorm.EncodeLLVM(d, graphnorm.PolicyAll)
		must(err)
		ino, _ := graphCost(none, en, fineP)
		ia, bd := graphCost(all, ea, fineP)
		_ = ip
		ih, bh := archiveCost(hyb, ea, false)
		ihr, _ := archiveCost(hybR, ea, true)
		ihn, _ := archiveCost(hybN, en, false)
		fmt.Printf("%-28s %10d %10d %10d %10d %10d %10d %10d %10d  strong: fn=%d skel=%d idtab=%d refs=%d | +delta: fn=%d skel=%d idtab=%d refs=%d\n", filepath.Base(files[i]), ip, ifn, b1, ino, ia, ih, ihr, ihn, bd.fn, bd.skel, bd.idtab, bd.refs, bh.fn, bh.skel, bh.idtab, bh.refs)
	}
	fmt.Printf("%-28s %10d %10d %10d %10d %10d %10d %10d %10d\n", "CUMULATIVE", cumP, cumF, b1Total, none.Total, all.Total, hyb.Total, hybR.Total, hybN.Total)
	fmt.Printf("objects: noNorm=%d strong=%d | hybrid objects=%d of which B1 deltas=%d (saved %d B vs direct) | noNorm hybrid objects=%d deltas=%d\n", none.Objects, all.Objects, hyb.Objects, hyb.Deltas, hyb.DeltaSav, hybN.Objects, hybN.Deltas)
}
