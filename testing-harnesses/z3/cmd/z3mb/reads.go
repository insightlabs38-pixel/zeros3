package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"zeros3-z3/dexz3"
	"zeros3-z3/lab"
)

func init() {
	cmds["reads"] = readsCmd
	cmds["perf"] = perfCmd
}

func vmhwm() string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "?"
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM:") {
			return strings.TrimSpace(strings.TrimPrefix(l, "VmHWM:"))
		}
	}
	return "?"
}

// reads DEX: bytes fetched (packed) to serve whole-file, one code item and a 4 KiB range inside it
// under production CDC and under the indexed-reference representation (unit-4k grouping).
func readsCmd(args []string) error {
	f, b, err := loadDex(args[0])
	if err != nil {
		return err
	}
	w, err := dexz3.Walk(f, b, dexz3.WalkOpts{Offsets: true, Debug: true})
	if err != nil {
		return err
	}
	cut := unitCut(4)
	rep, err := dexz3.Encode(f, b, dexz3.Config{Cut: cut.Cut})
	if err != nil {
		return err
	}
	// code item in the middle of the code section
	var codes []dexz3.Item
	for _, it := range w.Items {
		if it.Type == dexz3.TypeCode {
			codes = append(codes, it)
		}
	}
	mid := codes[len(codes)/2]
	// largest code item near the middle for the "inside a method" range
	best := mid
	for _, it := range codes[len(codes)/2-200 : len(codes)/2+200] {
		if it.Len > best.Len {
			best = it
		}
	}
	// production CDC chunks
	type span struct {
		off, n int64
		packed int64
	}
	var chunks []span
	lab.ChunkCDC(bytesReader(b), func(off int64, c []byte) error {
		chunks = append(chunks, span{off, int64(len(c)), lab.PackedLen(c)})
		return nil
	})
	cdcCost := func(off, n int64) (int64, int) {
		var tot int64
		k := 0
		for _, c := range chunks {
			if c.off < off+n && off < c.off+c.n {
				tot += c.packed
				k++
			}
		}
		return tot, k
	}
	// representation components
	mOff := int64(f.Hdr.MapOff)
	mLen := int64(4 + 12*len(f.Map))
	toPayload := func(o int64) int {
		o -= dexz3.HeaderSize
		if o+dexz3.HeaderSize >= mOff+mLen {
			o -= mLen
		}
		return int(o)
	}
	type comp struct {
		start, end int
		packed     int64
	}
	var comps []comp
	st := 0
	for i, c := range rep.Comps {
		comps = append(comps, comp{st, rep.Cuts[i], lab.PackedLen(c)})
		st = rep.Cuts[i]
	}
	repCost := func(off, n int64, withContext bool) (int64, int) {
		a, z := toPayload(off), toPayload(off+n-1)+1
		need := map[int]bool{}
		for i, c := range comps {
			if c.start < z && a < c.end {
				need[i] = true
			}
		}
		if withContext {
			// resolving references needs the identity tables: ID sections, type lists and all strings
			for _, s := range rep.Secs {
				switch s.Type {
				case dexz3.TypeStringID, dexz3.TypeTypeID, dexz3.TypeProtoID, dexz3.TypeFieldID, dexz3.TypeMethodID, dexz3.TypeClassDef, dexz3.TypeTypeList, dexz3.TypeStringData:
					for i, c := range comps {
						if c.start < s.End && s.Off < c.end {
							need[i] = true
						}
					}
				}
			}
		}
		var tot int64
		for i := range need {
			tot += comps[i].packed
		}
		return tot, len(need)
	}
	fmt.Printf("%s: %d B, %d code items; %d CDC chunks, %d rep components\n", args[0], len(b), len(codes), len(chunks), len(comps))
	fmt.Printf("%-34s %22s %34s %34s\n", "request", "CDC (fetch, chunks)", "rep: data comps only", "rep: + identity context")
	rows := []struct {
		name   string
		off, n int64
	}{{"whole file", 0, int64(len(b))}, {fmt.Sprintf("one code item (%d B)", mid.Len), mid.Off, mid.Len}, {"4 KiB inside the largest nearby", best.Off + best.Len/2 - 2048, 4096}}
	for _, r := range rows {
		cf, ck := cdcCost(r.off, r.n)
		d1, n1 := repCost(r.off, r.n, false)
		d2, n2 := repCost(r.off, r.n, true)
		fmt.Printf("%-34s %14s (%3d) %20s (%4d) %20s (%4d)   amp %.0fx / %.0fx\n", r.name, kib(cf), ck, kib(d1), n1, kib(d2), n2, float64(cf)/float64(max(r.n, 1)), float64(d2)/float64(max(r.n, 1)))
	}
	return nil
}

// perf DEX...: parse / analyse / encode / decode timings and peak RSS of this process.
func perfCmd(args []string) error {
	for _, p := range args {
		t0 := time.Now()
		f, b, err := loadDex(p)
		if err != nil {
			return err
		}
		tParse := time.Since(t0)
		t1 := time.Now()
		w, err := dexz3.Walk(f, b, dexz3.WalkOpts{Offsets: true, Debug: true})
		if err != nil {
			return err
		}
		tWalk := time.Since(t1)
		t2 := time.Now()
		rep, err := dexz3.Encode(f, b, dexz3.Config{Cut: unitCut(4).Cut})
		if err != nil {
			return err
		}
		tEnc := time.Since(t2) // includes the verifying decode
		t3 := time.Now()
		if _, err := dexz3.Decode(rep.Meta, rep.Comps); err != nil {
			return err
		}
		tDec := time.Since(t3)
		mb := float64(len(b)) / (1 << 20)
		fmt.Printf("%s: %.1f MiB slots %d | parse %v walk %v (%.0f MiB/s) encode+verify %v | decode %v (%.0f MiB/s) | peak RSS %s\n", p, mb, len(w.Slots), tParse, tWalk, mb/tWalk.Seconds(), tEnc, tDec, mb/tDec.Seconds(), vmhwm())
	}
	return nil
}

func init() { cmds["readsjar"] = readsJar }

// readsjar ARCHIVE MEMBER: bytes fetched to read one member out of a ZIP: production CDC chunks
// that overlap the member's stored span, against the exact-DEFLATE plan of a container holding
// just that member (whole-class comps, cp+body comps, or a DEX under unit-4k).
func readsJar(args []string) error {
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	var zf *zip.File
	for _, f := range zr.File {
		if f.Name == args[1] {
			zf = f
		}
	}
	if zf == nil {
		return fmt.Errorf("no member %s", args[1])
	}
	off, _ := zf.DataOffset()
	type span struct{ off, n, packed int64 }
	var chunks []span
	lab.ChunkCDC(bytesReader(data), func(o int64, c []byte) error {
		chunks = append(chunks, span{o, int64(len(c)), lab.PackedLen(c)})
		return nil
	})
	var cdcFetch int64
	k := 0
	for _, c := range chunks {
		if c.off < off+int64(zf.CompressedSize64) && off < c.off+c.n {
			cdcFetch += c.packed
			k++
		}
	}
	rc, _ := zf.Open()
	plain := make([]byte, zf.UncompressedSize64)
	if _, err := io.ReadFull(rc, plain); err != nil {
		return err
	}
	rc.Close()
	var mini bytes.Buffer
	w := zip.NewWriter(&mini)
	h, _ := w.CreateHeader(&zip.FileHeader{Name: args[1], Method: zip.Deflate})
	h.Write(plain)
	w.Close()
	fmt.Printf("%s %s: member %d B stored, %d B plain | CDC fetch %s in %d chunks\n", args[0], args[1], zf.CompressedSize64, len(plain), kib(cdcFetch), k)
	for _, name := range args[2:] {
		st := &innerStats{}
		rp := containerReps([]string{name}, st)[0]
		pl, _, err := rp.Plan(lab.EconFile{Name: "mini", Data: mini.Bytes()})
		if err != nil {
			return err
		}
		ac := lab.NewAcct(refBytes, true)
		a, err := ac.Add(pl)
		if err != nil {
			return err
		}
		fmt.Printf("   %-22s fetch %s packed in %d components (+%d B inline recipe)\n", name, kib(a.PackedNew), a.Components, a.InlineBytes)
	}
	return nil
}
