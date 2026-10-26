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

	"zeros3-z3/frontier"
	"zeros3-z3/lab"
	"zeros3-z3/tarz3"
	"zeros3-z3/zstdz3"
)

type rec map[string]any

// payloadSet accounts tar payload chunks by native identity: a new chunk costs its
// pack-rule bytes, every reference 36 bytes (the repz3 member convention).
type payloadSet struct {
	seen map[[32]byte]struct{}
	bs   *frontier.BlockSet
}

func newPayloadSet() *payloadSet {
	return &payloadSet{seen: map[[32]byte]struct{}{}, bs: frontier.NewBlockSet()}
}

func (p *payloadSet) add(payloads [][]byte) frontier.BlockCost {
	var ids [][32]byte
	var sizes []int64
	for _, pl := range payloads {
		must(lab.ChunkCDC(bytes.NewReader(pl), func(_ int64, ch []byte) error {
			id := sha256.Sum256(ch)
			sz := int64(0)
			if _, ok := p.seen[id]; !ok {
				p.seen[id] = struct{}{}
				sz = lab.PackedLen(ch)
			}
			ids = append(ids, id)
			sizes = append(sizes, sz)
			return nil
		}))
	}
	// BlockSet charges size only for ids it has not seen; sizes of seen ids are ignored.
	return p.bs.Add(ids, sizes)
}

// ecoLanes holds the stateful accounting of every representation across the chain.
type ecoLanes struct {
	cdc       *frontier.Chain // A: production CDC of the compressed stream
	blocks    *frontier.BlockSet
	tarCDC    *frontier.Chain // C: plaintext as plain tar through CDC
	tarStruct *frontier.Chain // D: tar structure stream
	tarPay    *payloadSet     // D: tar payload chunks
	skel      *frontier.Chain // IR skeleton blob
	useq      *frontier.Chain // unit stream through CDC
	unative   *frontier.BlockSet
	zcdc      *frontier.Chain // reference: zstd-compressed IR? (unused)
}

func newEco() *ecoLanes {
	return &ecoLanes{cdc: frontier.NewChain(), blocks: frontier.NewBlockSet(), tarCDC: frontier.NewChain(), tarStruct: frontier.NewChain(),
		tarPay: newPayloadSet(), skel: frontier.NewChain(), useq: frontier.NewChain(), unative: frontier.NewBlockSet()}
}

func cmdEco(args []string) {
	fs := flag.NewFlagSet("eco", flag.ExitOnError)
	dir := fs.String("dir", "/root/z3scratch/c25", "gen output directory")
	tag := fs.String("tag", "z155l3", "producer tag")
	revs := fs.String("revs", strings.Join(frontier.Revisions, ","), "revisions")
	out := fs.String("out", "", "results JSONL (default DIR/eco.TAG.jsonl)")
	nomodel := fs.Bool("nomodel", false, "explicit varint sequences (no match model)")
	tars := fs.String("tars", "", "explicit series: comma-separated tar files (with -zsts and -names instead of -dir/-tag/-revs)")
	zsts := fs.String("zsts", "", "explicit series: matching compressed files")
	names := fs.String("names", "", "explicit series: revision names")
	fs.Parse(args)
	if *out == "" {
		*out = filepath.Join(*dir, "eco."+*tag+".jsonl")
		if *nomodel {
			*out = filepath.Join(*dir, "eco."+*tag+".explicit.jsonl")
		}
	}
	of, err := os.Create(*out)
	must(err)
	defer of.Close()
	enc := json.NewEncoder(of)
	L := newEco()
	type item struct{ rev, tar, zst string }
	var items []item
	if *tars != "" {
		ts, zs, ns := strings.Split(*tars, ","), strings.Split(*zsts, ","), strings.Split(*names, ",")
		for i := range ts {
			items = append(items, item{ns[i], ts[i], zs[i]})
		}
	} else {
		for _, rev := range strings.Split(*revs, ",") {
			items = append(items, item{rev, filepath.Join(*dir, rev+".tar"), filepath.Join(*dir, rev+"."+*tag+".zst")})
		}
	}
	for _, it := range items {
		rev := it.rev
		tarB, err := os.ReadFile(it.tar)
		must(err)
		zst, err := os.ReadFile(it.zst)
		must(err)
		r := rec{"rev": rev, "tag": *tag, "tar": len(tarB), "zst": len(zst)}

		// A and B: the Z3-23 baselines, re-measured.
		c, err := L.cdc.AddStream(bytes.NewReader(zst))
		must(err)
		r["A_cdc"] = c
		spans, err := zstdz3.Scan(bytes.NewReader(zst), int64(len(zst)), zstdz3.Limits{})
		must(err)
		var ids [][32]byte
		var sizes []int64
		for _, f := range spans {
			for _, b := range f.Blocks {
				ids = append(ids, sha256.Sum256(zst[b.Off:b.Off+b.Len]))
				sizes = append(sizes, b.Len)
			}
		}
		r["B_native"] = L.blocks.Add(ids, sizes)

		// Parse, then prove: plaintext equals the tar and replay equals the stream.
		var plain, units bytes.Buffer
		plain.Grow(len(tarB))
		res, err := zstdz3.Parse(bytes.NewReader(zst), &plain, &units, zstdz3.Options{NoSeqModel: *nomodel})
		must(err)
		if !bytes.Equal(plain.Bytes(), tarB) {
			must(fmt.Errorf("%s: decoded plaintext differs from the tar", rev))
		}
		var re bytes.Buffer
		re.Grow(len(zst))
		_, err = zstdz3.Replay(&re, res.Raw, bytes.NewReader(units.Bytes()), bytes.NewReader(plain.Bytes()), zstdz3.Limits{})
		must(err)
		if !bytes.Equal(re.Bytes(), zst) {
			must(fmt.Errorf("%s: replay differs from the original stream", rev))
		}
		re = bytes.Buffer{}
		s := res.Stats
		r["stats"] = s

		// Plaintext representations.
		r["C_plain"], err = L.tarCDC.AddStream(bytes.NewReader(tarB))
		must(err)
		a, err := tarz3.Parse(tarB, tarz3.Options{})
		must(err)
		st, payloads := tarz3.Split(tarB, a)
		r["D_struct"], err = L.tarStruct.AddStream(bytes.NewReader(st))
		must(err)
		r["D_pay"] = L.tarPay.add(payloads)

		// Reconstruction description.
		r["IR_skel"], err = L.skel.AddStream(bytes.NewReader(res.Raw))
		must(err)
		r["IR_useq"], err = L.useq.AddStream(bytes.NewReader(units.Bytes()))
		must(err)
		// native units: one SHA-addressed object per block unit
		var uids [][32]byte
		var usz []int64
		ub := units.Bytes()
		off := 0
		for _, n := range res.UnitSizes {
			u := ub[off : off+int(n)]
			off += int(n)
			uids = append(uids, sha256.Sum256(u))
			usz = append(usz, lab.PackedLen(u))
		}
		r["IR_unative"] = L.unative.Add(uids, usz)
		r["unit_bytes"] = units.Len()
		r["unit_packed"] = lab.PackedLen(units.Bytes())
		r["skel_bytes"] = len(res.Raw)
		must(enc.Encode(r))
		fmt.Fprintf(os.Stderr, "eco %s: zst %d IR %d (units %d skel %d)\n", rev, len(zst), s.IRBytes(), units.Len(), len(res.Raw))
	}
}
