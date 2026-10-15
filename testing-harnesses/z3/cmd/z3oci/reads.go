package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"zeros3-z3/fstate"
	"zeros3-z3/lab"
	"zeros3-z3/ociz3"
	"zeros3-z3/tarz3"
)

// span is the stored-component footprint of a byte range of an object.
type span struct{ bytes, comps int64 }

func (s span) String() string { return fmt.Sprintf("%s in %d comps", fstate.Human(s.bytes), s.comps) }

// cdcTouch: bytes (and chunks) of the object's production CDC chunks that overlap
// [lo, hi).
func cdcTouch(data []byte, lo, hi int64) (span, error) {
	pl, err := fstate.FallbackCDC(fstate.File{Name: "o", Data: data})
	if err != nil {
		return span{}, err
	}
	var off int64
	var s span
	for _, p := range pl.Pieces {
		if p.Comp == nil {
			continue
		}
		end := off + p.Comp.Size
		if end > lo && off < hi {
			s.bytes += p.Comp.Size
			s.comps++
		}
		off = end
	}
	return s, nil
}

func vmHWM() int64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmHWM:") {
			var kb int64
			fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(l, "VmHWM:")), "%d", &kb)
			return kb << 10
		}
	}
	return 0
}

// transformTouch runs the layer's recipe: it returns the components fetched by a
// full rebuild (and the rebuild time) and verifies the output.
func transformFull(f fstate.File, cdc bool) (pl *lab.Plan, ac *lab.Acct, got span, tAnalyse, tRebuild time.Duration, err error) {
	t0 := time.Now()
	pl, err = ociz3.TransformPlan(f, cdc)
	tAnalyse = time.Since(t0)
	if err != nil {
		return
	}
	ac = lab.NewAcct(96, true)
	if _, err = ac.Add(pl); err != nil {
		return
	}
	seen := map[lab.ID]bool{}
	get := func(id lab.ID) ([]byte, error) {
		b, e := ac.Get(id)
		if e == nil && !seen[id] {
			seen[id] = true
			got.bytes += int64(len(b))
			got.comps++
		}
		return b, e
	}
	var out bytes.Buffer
	t1 := time.Now()
	err = pl.Rebuild(&out, get)
	tRebuild = time.Since(t1)
	if err == nil && (out.Len() != len(f.Data) || sha256.Sum256(out.Bytes()) != sha256.Sum256(f.Data)) {
		err = fmt.Errorf("rebuild mismatch")
	}
	return
}

// transformMember: stored plaintext components overlapping a member's payload
// (tarhdr/tarpay pieces are in tar order; their lengths are tar offsets).
func transformMember(pl *lab.Plan, lo, hi int64) (span, bool) {
	var off int64
	var s span
	for _, p := range pl.Pieces {
		if p.Comp == nil || (p.Comp.Class != "tarhdr" && p.Comp.Class != "tarpay") {
			continue
		}
		end := off + p.Len
		if end > lo && off < hi {
			s.bytes += p.Comp.Size
			s.comps++
		}
		off = end
	}
	return s, true
}

func cmdReads(args []string) error {
	fs := flag.NewFlagSet("reads", flag.ExitOnError)
	pins := fs.String("pins", "realworld/oci.json", "pinned images")
	group := fs.String("group", "nodeexp", "image group")
	name := fs.String("name", "v1.7.0", "image name")
	plat := fs.String("platform", "linux/amd64", "platform")
	scale := fs.Int("scale", 0, "synthetic layer size in MiB of tar (scale mode; replaces the real-image reads)")
	fs.Parse(args)
	if *scale > 0 {
		return scaleMode(*scale)
	}
	ps, err := pinsByGroup(*pins, *group)
	if err != nil {
		return err
	}
	var pin *Pin
	for i := range ps {
		if ps[i].Name == *name {
			pin = &ps[i]
		}
	}
	if pin == nil {
		return fmt.Errorf("no image %s/%s", *group, *name)
	}
	img, err := loadPin(*pin, *plat)
	if err != nil {
		return err
	}
	files := ociz3.Files(img)
	arc := ociz3.Archive(files)
	a, err := tarz3.Parse(arc, tarz3.Options{})
	if err != nil {
		return err
	}
	member := func(name string) (tarz3.Member, bool) {
		for _, m := range a.Members {
			if m.Path == name {
				return m, true
			}
		}
		return tarz3.Member{}, false
	}
	var man, big *ociz3.Node
	for _, n := range img.Nodes {
		switch n.Kind {
		case ociz3.KindManifest:
			if man == nil {
				man = n
			}
		case ociz3.KindLayerGz, ociz3.KindLayerTar:
			if big == nil || n.Size > big.Size {
				big = n
			}
		}
	}
	fmt.Printf("image %s %s %s: export archive %s (%d files), manifest %d B, largest layer %s %s (%s)\n\n", *group, *name, *plat, fstate.Human(int64(len(arc))), len(files), man.Size, big.Digest[7:19], big.Kind, fstate.Human(big.Size))
	fmt.Println("| operation | raw OCI archive, production CDC | blob-level components | transformed tar+gzip (members whole) | transformed tar+gzip (members CDC-chunked) |")
	fmt.Println("|---|---|---|---|---|")

	mm, _ := member("blobs/sha256/" + man.Digest[7:])
	arcMan, _ := cdcTouch(arc, mm.PayOff, mm.PayOff+mm.PayLen)
	blobMan, _ := cdcTouch(man.Data, 0, man.Size)
	fmt.Printf("| read one manifest (%d B) | %s | %s | same (not a layer) | same (not a layer) |\n", man.Size, arcMan, blobMan)

	lm, _ := member("blobs/sha256/" + big.Digest[7:])
	arcL, _ := cdcTouch(arc, lm.PayOff, lm.PayOff+lm.PayLen)
	blobL, _ := cdcTouch(big.Data, 0, big.Size)
	lf := fstate.File{Name: big.Digest, Kind: string(big.Kind), Data: big.Data}
	var pls [2]*lab.Plan
	var trL [2]string
	for i, cdc := range []bool{false, true} {
		var got span
		var tA, tR time.Duration
		var terr error
		pls[i], _, got, tA, tR, terr = transformFull(lf, cdc)
		if terr != nil {
			trL[i] = "n/a: " + terr.Error()
		} else {
			trL[i] = fmt.Sprintf("%s (rebuild %.0f ms, analysis %.0f ms)", got, ms(tR), ms(tA))
		}
	}
	fmt.Printf("| fetch one layer by digest (%s) | %s | %s | %s | %s |\n", fstate.Human(big.Size), arcL, blobL, trL[0], trL[1])

	// one member of that layer
	plain := big.Data
	if big.Kind == ociz3.KindLayerGz {
		zr, e := gzip.NewReader(bytes.NewReader(big.Data))
		if e != nil {
			return e
		}
		plain, e = io.ReadAll(zr)
		if e != nil {
			return e
		}
	}
	ta, err := tarz3.Parse(plain, tarz3.Options{})
	if err != nil {
		return err
	}
	var withData []int
	for i, m := range ta.Members {
		if m.PayLen > 0 {
			withData = append(withData, i)
		}
	}
	if len(withData) == 0 {
		return fmt.Errorf("layer has no regular file payloads")
	}
	for _, which := range []string{"first file", "middle file", "last file"} {
		var idx int
		switch which {
		case "first file":
			idx = withData[0]
		case "middle file":
			idx = withData[len(withData)/2]
		default:
			idx = withData[len(withData)-1]
		}
		m := ta.Members[idx]
		end := m.PayOff + m.PayLen
		// compressed prefix a sequential reader must consume
		var consumed int64
		if big.Kind == ociz3.KindLayerGz {
			br := bytes.NewReader(big.Data)
			zr, _ := gzip.NewReader(br)
			io.CopyN(io.Discard, zr, end)
			consumed = int64(len(big.Data)) - int64(br.Len())
		} else {
			consumed = end
		}
		blobM, _ := cdcTouch(big.Data, 0, consumed)
		arcM, _ := cdcTouch(arc, lm.PayOff, lm.PayOff+consumed)
		var trM [2]string
		for i := range pls {
			trM[i] = "n/a"
			if pls[i] != nil {
				if s, ok := transformMember(pls[i], m.PayOff, end); ok {
					trM[i] = s.String()
				}
			}
		}
		fmt.Printf("| read one member (%s %d of %d: `%s`, %s at tar offset %s; compressed prefix to inflate %s) | %s | %s | %s | %s |\n", which, idx, len(ta.Members), trunc(m.Path, 28), fstate.Human(m.PayLen), fstate.Human(m.PayOff), fstate.Human(consumed), arcM, blobM, trM[0], trM[1])
	}
	fmt.Println("\nA gzip layer blob has no random access: reading a member means inflating from the start of the stream (the compressed prefix is given in each row). The transformed representation stores the member payloads as separate plaintext components and can serve a member without touching the rest, but it cannot serve the registry-format blob without re-deflating the whole layer.")
	runtime.GC()
	fmt.Printf("\npeak RSS of this process: %s\n", fstate.Human(vmHWM()))
	return nil
}

func trunc(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n+1:]
	}
	return s
}

// scaleMode builds one synthetic gzip layer (about mib MiB of tar) and reports
// analysis / rebuild / baseline throughput and the process peak RSS.
func scaleMode(mib int) error {
	files := mib * 128 // mean 8 KiB
	ms0 := ociz3.Tree(777, "scale", files, 8192)
	tr, err := ociz3.WriteTar(ms0, 0)
	if err != nil {
		return err
	}
	ms0 = nil
	gz, err := ociz3.Compress(tr, ociz3.GzipOpts{Level: 6})
	if err != nil {
		return err
	}
	runtime.GC()
	base := vmHWM()
	f := fstate.File{Name: "scale", Kind: string(ociz3.KindLayerGz), Data: gz}
	t0 := time.Now()
	zr, _ := gzip.NewReader(bytes.NewReader(gz))
	io.Copy(io.Discard, zr)
	tGunzip := time.Since(t0)
	t0 = time.Now()
	cdc, err := fstate.FallbackCDC(f)
	if err != nil {
		return err
	}
	tCDC := time.Since(t0)
	_ = cdc
	pl, _, got, tA, tR, terr := transformFull(f, false)
	if terr != nil {
		return terr
	}
	var notes []string
	for k, v := range pl.Detail {
		switch x := v.(type) {
		case string:
			notes = append(notes, k+"="+x)
		case bool, int, int64:
			notes = append(notes, fmt.Sprintf("%s=%v", k, x))
		}
	}
	sort.Strings(notes)
	fmt.Fprintln(os.Stderr, mib, "MiB plan detail:", notes)
	fmt.Printf("| %d MiB tar / %s gz | %s | %.0f ms | %.0f ms | %.0f ms (%.1f MB/s of tar) | %.0f ms (%.1f MB/s of tar) | %s | %s |\n", mib, fstate.Human(int64(len(gz))), fstate.Human(int64(len(tr))), ms(tGunzip), ms(tCDC), ms(tA), float64(len(tr))/1e6/tA.Seconds(), ms(tR), float64(len(tr))/1e6/tR.Seconds(), fstate.Human(vmHWM()), fstate.Human(got.bytes))
	_ = base
	return nil
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
