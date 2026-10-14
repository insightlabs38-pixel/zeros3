package main

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"hash"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"zeros3-z3/carz3"
	"zeros3-z3/lab"
)

// cmdRead measures laboratory read paths over CARv2 archives: one block by CID,
// an archive byte range, and a sequential rebuild.
func cmdRead(args []string) error {
	fs := flag.NewFlagSet("read", flag.ExitOnError)
	scale := fs.String("scale", "big", "big|small")
	fs.Parse(args)
	sc, err := scaleByName(*scale)
	if err != nil {
		return err
	}
	bs, root := carz3.Gen(carz3.GenSpec{Seed: 14, N: sc.N, Min: sc.Min, Max: sc.Max, Fan: sc.Fan})
	v1 := carz3.V1([][]byte{root}, bs)
	targets := map[string][]byte{
		"generated v2 " + sc.Name: carz3.V2From(v1, carz3.V2Opts{IndexCodec: carz3.MultihashIndexSortedCodec}),
	}
	names := []string{"generated v2 " + sc.Name}
	if b, err := os.ReadFile(filepath.Join(cacheDir(), "sample-wrapped-v2.car")); err == nil {
		targets["real sample-wrapped-v2 (blake2b CIDs)"] = b
		names = append(names, "real sample-wrapped-v2 (blake2b CIDs)")
	}
	for _, n := range names {
		if err := readOne(n, targets[n]); err != nil {
			return err
		}
	}
	return nil
}

func readOne(name string, car []byte) error {
	rep, st, err := carz3.Build(bytes.NewReader(car), int64(len(car)), carz3.DefaultOptions())
	if err != nil {
		return err
	}
	store := map[[32]byte][]byte{}
	for _, r := range rep.Refs {
		store[r.Sum] = car[r.SrcOff : r.SrcOff+r.Len]
	}
	get := func(s [32]byte) ([]byte, error) { return store[s], nil }
	recipe, sz := rep.Marshal()
	n := len(rep.Refs)
	fmt.Printf("\n#### %s\n\n%d bytes, %d sections, recipe %d B (index mode %d, derived CIDs %d/%d), original index %d B\n", name, len(car), n, len(recipe), rep.IndexMode, st.Derived, st.Sections, rep.IndexRaw)

	// Strategy tables built once (the cost of building them is part of the report).
	t0 := time.Now()
	idx, _, _ := carz3.ParseIndex(rep.IndexBytesOrRebuilt())
	sort.Slice(idx.Recs, func(i, j int) bool { return bytes.Compare(idx.Recs[i].Digest, idx.Recs[j].Digest) < 0 })
	parseIdx := time.Since(t0)

	type side struct {
		dig [32]byte
		ref int
	}
	t0 = time.Now()
	var sidx []side
	cids := make([]carz3.CID, n)
	for i := range rep.Refs {
		c, _, _ := carz3.ParseCID(rep.CIDBytes(i))
		cids[i] = c
		var d [32]byte
		copy(d[:], c.Digest)
		sidx = append(sidx, side{d, i})
	}
	sort.Slice(sidx, func(i, j int) bool { return bytes.Compare(sidx[i].dig[:], sidx[j].dig[:]) < 0 })
	buildSide := time.Since(t0)
	recSize := 8 + 32
	entryB := float64(sz.Order+sz.CIDExact) / float64(n)
	sideSize := n * (32 + 4)

	const picks = 200
	var aTouched, bTouched, cTouched, payload int64
	var tA, tB, tC time.Duration
	for k := 0; k < picks; k++ {
		i := (k*7919 + 13) % n
		for cids[i].HashCode == carz3.MhIdentity { // writers do not index identity CIDs
			i = (i + 1) % n
		}
		want := cids[i]
		// A: binary search the original sorted index, then walk the recipe to the entry at that offset.
		t := time.Now()
		j := sort.Search(len(idx.Recs), func(x int) bool { return bytes.Compare(idx.Recs[x].Digest, want.Digest) >= 0 })
		off := int64(idx.Recs[j].Offset)
		pos := 0
		for ; pos < n; pos++ {
			if rep.Refs[pos].Off == off {
				break
			}
		}
		tA += time.Since(t)
		aTouched += int64(math.Ceil(math.Log2(float64(len(idx.Recs))+1))+1)*int64(recSize) + int64(float64(pos+1)*entryB)
		// B: rebuild digest -> ref by scanning the whole recipe.
		t = time.Now()
		m := map[string]int{}
		for q := range rep.Refs {
			m[string(cids[q].Digest)] = q
		}
		_ = m[string(want.Digest)]
		tB += time.Since(t)
		bTouched += int64(len(recipe))
		// C: side index of (digest -> component ref) next to the recipe.
		t = time.Now()
		var wd [32]byte
		copy(wd[:], want.Digest)
		x := sort.Search(len(sidx), func(q int) bool { return bytes.Compare(sidx[q].dig[:], wd[:]) >= 0 })
		_ = sidx[x].ref
		tC += time.Since(t)
		cTouched += int64(math.Ceil(math.Log2(float64(n)+1))+1) * 36
		payload += rep.Refs[i].Len
		if got, _ := get(rep.Refs[sidx[x].ref].Sum); int64(len(got)) != rep.Refs[i].Len {
			return fmt.Errorf("side index returned a wrong block")
		}
	}
	avgBlock := payload / picks
	fmt.Printf("\nRead one block by CID (%d lookups; bytes per lookup; avg block %d B):\n\n| strategy | metadata bytes touched | payload bytes | extra stored bytes | build | per lookup |\n|---|--:|--:|--:|--:|--:|\n", picks, avgBlock)
	fmt.Printf("| raw CDC / no structure (scan archive) | %d | - | 0 | - | O(archive) |\n", len(car))
	fmt.Printf("| exact original index + recipe walk | %d | %d | %d (the index itself) | %v | %v |\n", aTouched/picks, avgBlock, rep.IndexRaw, parseIdx.Round(time.Microsecond), (tA / picks).Round(time.Nanosecond))
	fmt.Printf("| rebuild index from recipe each time | %d | %d | 0 | - | %v |\n", bTouched/picks, avgBlock, (tB / picks).Round(time.Microsecond))
	fmt.Printf("| ZeroS3 side index (digest, component ref) | %d | %d | %d | %v | %v |\n", cTouched/picks, avgBlock, sideSize, buildSide.Round(time.Microsecond), (tC / picks).Round(time.Nanosecond))

	// archive byte ranges
	fmt.Printf("\nArchive byte range reads (offset in the middle; fetched = component payload bytes requested):\n\n| range bytes | raw CDC fetched | car-section fetched | car-block recipe fetched | recipe entries walked |\n|--:|--:|--:|--:|--:|\n")
	rawPlan, cdcPlan, secPlan, err := basePlans(car)
	if err != nil {
		return err
	}
	_ = rawPlan
	for _, rn := range []int64{100, 4096, 65536, 1 << 20} {
		if rn > int64(len(car))/2 {
			continue
		}
		off := int64(len(car)) / 3
		got, f1, _, err := rep.ReadRange(off, rn, get)
		if err != nil || !bytes.Equal(got, car[off:off+rn]) {
			return fmt.Errorf("range read differs: %v", err)
		}
		_, fc, err := lab.ReadRange(cdcPlan.p, cdcPlan.get, off, rn)
		if err != nil {
			return err
		}
		_, fs, err := lab.ReadRange(secPlan.p, secPlan.get, off, rn)
		if err != nil {
			return err
		}
		_, _, sc, _ := rep.ReadRange(off, rn, get)
		fmt.Printf("| %d | %d | %d | %d | %d |\n", rn, fc, fs, f1, sc)
	}

	// sequential reconstruct
	var h hash.Hash = sha256.New()
	t0 = time.Now()
	const reps = 5
	for i := 0; i < reps; i++ {
		h.Reset()
		if err := rep.Reconstruct(h, get); err != nil {
			return err
		}
	}
	dt := time.Since(t0) / reps
	fmt.Printf("\nSequential reconstruct of the whole archive: %v (%.0f MiB/s, hashing included); raw copy baseline is memory bandwidth.\n", dt.Round(time.Microsecond), float64(len(car))/(1<<20)/dt.Seconds())
	return nil
}

type planGet struct {
	p   *lab.Plan
	get func(lab.ID) ([]byte, error)
}

// basePlans builds the CDC and car-section plans of one archive so that the
// range-read amplification of the baselines can be measured with the shared
// lab.ReadRange.
func basePlans(car []byte) (raw, cdc, sec planGet, err error) {
	mk := func(name string) (planGet, error) {
		for _, r := range lab.CARReps() {
			if r.Name != name {
				continue
			}
			in := &lab.Input{Family: "rr", Format: "car", Revision: "r", Src: lab.BytesSource("x", car), Opts: lab.DefaultOptions()}
			p, err := r.Analyze(in)
			if err != nil {
				return planGet{}, err
			}
			ac := lab.NewAcct(35, false)
			if _, err := ac.Add(p); err != nil {
				return planGet{}, err
			}
			return planGet{p, ac.Get}, nil
		}
		return planGet{}, fmt.Errorf("no rep %s", name)
	}
	if cdc, err = mk("cdc"); err != nil {
		return
	}
	sec, err = mk("car-section")
	return
}
