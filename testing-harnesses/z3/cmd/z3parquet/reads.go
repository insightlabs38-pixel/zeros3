package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"

	"zeros3-z3/lab"
	"zeros3-z3/parquetz3"
)

// cdcChunks returns the production CDC chunk boundaries of data and the packed size of each.
func cdcChunks(data []byte) (ends []int64, packed []int64, err error) {
	sizes, err := lab.CDCSizes(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	var pos int64
	for _, z := range sizes {
		packed = append(packed, lab.PackedSize(data[pos:pos+int64(z)]))
		pos += int64(z)
		ends = append(ends, pos)
	}
	return
}

func cdcFetch(ends, packed []int64, off, n int64) (bytesFetched int64, chunks int) {
	var start int64
	for i, e := range ends {
		if e > off && start < off+n {
			bytesFetched += packed[i]
			chunks++
		}
		start = e
	}
	return
}

type readReq struct {
	name   string
	off, n int64
}

// pageSpan finds page number k of the column chunk (rg, col): file offset and total size.
func pageSpan(pf *pqFile, rg, col, k int) (off, size int64, err error) {
	ch := &pf.f.RowGroups[rg].Chunks[col]
	i := 0
	err = pf.f.Pages(pf.r, ch, func(p *parquetz3.Page, _ []byte) error {
		if i == k {
			off, size = p.Off, p.End()-p.Off
		}
		i++
		return nil
	})
	if k >= i {
		err = fmt.Errorf("chunk %d/%d has %d pages", rg, col, i)
	}
	return
}

func colIndex(pf *pqFile, name string) int {
	for i, c := range pf.f.Columns {
		if c.Name == name {
			return i
		}
	}
	return 0
}

// cmdReads characterizes ranged reads of the strongest candidate against production CDC.
func cmdReads(args []string) error {
	fs := flag.NewFlagSet("reads", flag.ExitOnError)
	target := fs.Int("target", 8<<10, "group target")
	fs.Parse(args)
	fmt.Println("| file | request | requested | CDC fetch (chunks) | rep fetch, packed | components | amplification (CDC / rep) |")
	fmt.Println("|---|---|---:|---:|---:|---:|---|")
	for _, tc := range []struct{ fam, rev string }{{"pyarrow-variants", "r0"}, {"pyarrow-variants", "m16-page-64k"}, {"duckdb-variants", "d01-snappy"}} {
		sts, _, err := loadFamily(tc.fam)
		if err != nil {
			return err
		}
		var s state
		for _, x := range sts {
			if x.Rev == tc.rev {
				s = x
			}
		}
		data, err := readFile(s.Path)
		if err != nil {
			return err
		}
		pf, err := parseFile(lab.EconFile{Name: s.Rev, Data: data})
		if err != nil {
			return err
		}
		st := memStore{}
		rec, sz, _, err := parquetz3.Encode(bytes.NewReader(data), int64(len(data)), parquetz3.Options{Target: *target}, st.emit)
		if err != nil {
			return err
		}
		ends, packed, err := cdcChunks(data)
		if err != nil {
			return err
		}
		val, cat, pay := colIndex(pf, "value"), colIndex(pf, "cat"), colIndex(pf, "payload")
		var reqs []readReq
		reqs = append(reqs, readReq{"whole file", 0, int64(len(data))})
		pOff, pSize, err := pageSpan(pf, 3, val, 0)
		if err != nil {
			return err
		}
		reqs = append(reqs, readReq{"4 KiB inside a PLAIN page (value, rg3)", pOff + pSize/2, 4096})
		if cOff, cSize, err := pageSpan(pf, 3, cat, 1); err == nil {
			reqs = append(reqs, readReq{"4 KiB inside a dictionary-encoded page (cat, rg3)", cOff + cSize/2, 4096})
		} else if cOff, cSize, err := pageSpan(pf, 3, cat, 0); err == nil {
			reqs = append(reqs, readReq{"4 KiB at the start of cat's dictionary page (rg3)", cOff + 20, min(4096, cSize-20)})
		}
		if o0, s0, err := pageSpan(pf, 3, pay, 2); err == nil {
			reqs = append(reqs, readReq{"4 KiB across two pages (payload, rg3)", o0 + s0 - 2048, 4096})
		}
		for _, cn := range []string{"value", "cat"} {
			ci := colIndex(pf, cn)
			ch := &pf.f.RowGroups[3].Chunks[ci]
			reqs = append(reqs, readReq{fmt.Sprintf("one column chunk (%s, rg3)", cn), ch.Start, ch.End - ch.Start})
		}
		recPacked := lab.PackedSize(rec)
		for _, rq := range reqs {
			seen := map[parquetz3.ID]bool{}
			var packedFetch int64
			get := func(id parquetz3.ID) ([]byte, error) {
				b, err := st.get(id)
				if err == nil && !seen[id] {
					seen[id] = true
					packedFetch += lab.PackedSize(b)
				}
				return b, err
			}
			var out bytes.Buffer
			stats, err := parquetz3.DecodeRange(&out, rec, get, rq.off, rq.n, parquetz3.Limits{})
			if err != nil || !bytes.Equal(out.Bytes(), data[rq.off:rq.off+rq.n]) {
				return fmt.Errorf("%s %s: %v", s.Rev, rq.name, err)
			}
			cf, nc := cdcFetch(ends, packed, rq.off, rq.n)
			fmt.Printf("| %s | %s | %s | %s (%d) | %s | %d (%d nodes) | %.1f× / %.1f× |\n", s.Rev, rq.name, human(rq.n), human(cf), nc, human(packedFetch), len(seen), stats.Nodes,
				float64(cf)/float64(rq.n), float64(packedFetch)/float64(rq.n))
		}
		fmt.Printf("| %s | recipe/index, loaded once | | | %s (%s unpacked, %d members) | 1 | |\n", s.Rev, human(recPacked), human(int64(len(rec))), len(pr(rec)))
		_ = sz
		_ = os.Stderr
		_ = strings.Join
	}
	return nil
}

func pr(rec []byte) []parquetz3.ID {
	r, err := parquetz3.ParseRecipe(rec, parquetz3.Limits{})
	if err != nil {
		return nil
	}
	return r.Digests
}

func human(n int64) string {
	switch {
	case n >= 10<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 10<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
