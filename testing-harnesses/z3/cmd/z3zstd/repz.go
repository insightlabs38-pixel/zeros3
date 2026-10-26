package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zeros3-z3/frontier"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/tarz3"
	"zeros3-z3/zstdz3"
)

// tarChunkChild represents an uncompressed tar as a KindConcat representation whose
// members are STORED components: every header region and padding is one component and
// every member body is cut by production CDC, so unchanged parts of a file dedupe. It is
// the experimental layer's "structural tar" child; the zstd kind never sees it.
func tarChunkChild(tarB []byte) (*kinds.Bundle, error) {
	a, err := tarz3.Parse(tarB, tarz3.Options{})
	if err != nil {
		return nil, err
	}
	rep := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(tarB))}
	var objs [][]byte
	stored := func(b []byte) {
		cp := append([]byte(nil), b...)
		rep.Members = append(rep.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(cp), Size: int64(len(cp))})
		objs = append(objs, cp)
	}
	pos := int64(0)
	for i := range a.Members {
		m := &a.Members[i]
		if m.PayOff > pos {
			stored(tarB[pos:m.PayOff])
		}
		if m.PayLen > 0 {
			if err := lab.ChunkCDC(bytes.NewReader(tarB[m.PayOff:m.PayOff+m.PayLen]), func(_ int64, c []byte) error { stored(c); return nil }); err != nil {
				return nil, err
			}
		}
		pos = m.PayOff + m.PayLen
		if e := m.End(); e > pos {
			stored(tarB[pos:e])
			pos = e
		}
	}
	if pos < int64(len(tarB)) {
		stored(tarB[pos:])
	}
	raw, err := rep.Marshal()
	if err != nil {
		return nil, err
	}
	return &kinds.Bundle{Rep: rep, Raw: raw, ID: sha256.Sum256(raw), Objects: objs}, nil
}

// cmdRepz runs the chain through the real repz3 contract: serialized representation
// objects in a MemStore, reconstruction through the kind table only, and the bytes
// persisted per class (raw, and under the production pack rule) with object counts.
func cmdRepz(args []string) {
	fs := flag.NewFlagSet("repz", flag.ExitOnError)
	dir := fs.String("dir", "/root/z3scratch/c25", "gen output directory")
	tag := fs.String("tag", "z155l3", "producer tag")
	revs := fs.String("revs", strings.Join(frontier.Revisions, ","), "revisions")
	child := fs.String("child", "cdc", "semantic child: cdc (plain tar through CDC), tar (existing tar layer), tarcdc (tar with CDC bodies)")
	lane := fs.String("lane", "zstd", "zstd (exact replay over the child) or cdc (baseline: the compressed stream through production CDC, same contract)")
	prove := fs.String("prove", "R0,R3,R10,R11", "revisions whose reconstruction is checked byte for byte")
	out := fs.String("out", "", "results JSONL")
	fs.Parse(args)
	var of *os.File
	var enc *json.Encoder
	if *out != "" {
		var err error
		of, err = os.Create(*out)
		must(err)
		defer of.Close()
		enc = json.NewEncoder(of)
	}
	st := repz3.NewMemStore()
	provSet := map[string]bool{}
	for _, r := range strings.Split(*prove, ",") {
		provSet[r] = true
	}
	childFn := func(plain []byte) (*kinds.Bundle, error) {
		switch *child {
		case "cdc":
			return kinds.CDC(plain)
		case "tar":
			b, _, err := kinds.EncodeTarLayer(plain, 1<<62)
			return b, err
		}
		return tarChunkChild(plain)
	}
	fmt.Printf("%-4s %10s | %9s %9s %9s %9s | %7s %9s | %s\n", "rev", "zst", "recipe", "skel+tab", "units", "plaintext", "objects", "total", "(packed bytes; +96 B/object in total)")
	var cumTotal int64
	for _, rev := range strings.Split(*revs, ",") {
		t0 := time.Now()
		zst, err := os.ReadFile(filepath.Join(*dir, rev+"."+*tag+".zst"))
		must(err)
		var b *kinds.Bundle
		var zs *kinds.ZstdStats
		if *lane == "cdc" {
			b, err = kinds.CDC(zst)
			zs = &kinds.ZstdStats{}
		} else {
			b, zs, err = kinds.EncodeZstd(zst, childFn)
		}
		must(err)
		// classify the objects this bundle adds by where they came from
		class := map[repz3.SHA]string{}
		for _, o := range b.Objects {
			class[sha256.Sum256(o)] = "units"
			if *lane == "cdc" {
				class[sha256.Sum256(o)] = "plaintext" // the compressed stream's own chunks
			}
		}
		var mark func(x *kinds.Bundle)
		mark = func(x *kinds.Bundle) {
			class[x.ID] = "recipe"
			for _, o := range x.Objects {
				if _, ok := class[sha256.Sum256(o)]; !ok {
					class[sha256.Sum256(o)] = "plaintext"
				}
			}
			for _, c := range x.Children {
				mark(c)
			}
		}
		// table chunks and units are the zstd bundle's own objects: separate them
		tableChunks := map[repz3.SHA]bool{}
		for i := 0; i < len(b.Rep.Members)-1 && *lane != "cdc"; i++ {
			tableChunks[b.Rep.Members[i].SHA] = true
		}
		for id := range tableChunks {
			class[id] = "skel+tab"
		}
		class[b.ID] = "recipe"
		for _, c := range b.Children {
			mark(c)
		}
		id, added, err := b.Persist(st)
		must(err)
		var raw, packed [4]int64
		var n int
		idx := map[string]int{"recipe": 0, "skel+tab": 1, "units": 2, "plaintext": 3}
		for _, a := range added {
			bs := st.Bytes(a)
			c, ok := class[a]
			if !ok {
				c = "plaintext"
			}
			raw[idx[c]] += int64(len(bs))
			packed[idx[c]] += lab.PackedLen(bs)
			n++
		}
		var tot int64
		for _, p := range packed {
			tot += p
		}
		tot += 96 * int64(n)
		cumTotal += tot
		ok := "-"
		if provSet[rev] {
			var got bytes.Buffer
			nn, sum, err := kinds.NewEnv(st).Reconstruct(id, &got)
			if err != nil || nn != int64(len(zst)) || sum != sha256.Sum256(zst) || !bytes.Equal(got.Bytes(), zst) {
				must(fmt.Errorf("%s: reconstruction through the representation failed: %v", rev, err))
			}
			reach, err := kinds.NewEnv(st).Reachable(id)
			must(err)
			_ = reach
			ok = "proved"
		}
		fmt.Printf("%-4s %10d | %9s %9s %9s %9s | %7d %9s | %s %v (blocks %v, fallback %d B)\n", rev, len(zst), hum(float64(packed[0])), hum(float64(packed[1])), hum(float64(packed[2])), hum(float64(packed[3])), n, hum(float64(tot)), ok, since(t0), zs.Stats.Blocks, zs.Stats.FallbackBytes)
		if enc != nil {
			enc.Encode(map[string]any{"rev": rev, "child": *child, "zst": len(zst), "packed": packed, "raw": raw, "objects": n, "total": tot, "table_bytes": zs.TableBytes, "skeleton": zs.SkeletonBytes, "units": zs.Units, "unit_bytes": zs.UnitBytes})
		}
	}
	fmt.Printf("cumulative total %s\n", hum(float64(cumTotal)))
	_ = zstdz3.BlockRaw
}
