package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash"
	"os"
	"sort"

	"zeros3-z3/parquetz3"
)

type dumpCol struct {
	Name     string         `json:"name"`
	Phys     int            `json:"phys"`
	MaxDef   int            `json:"maxdef"`
	MaxRep   int            `json:"maxrep"`
	Opaque   int            `json:"opaque_chunks"`
	Values   int64          `json:"values"`
	ValueSHA string         `json:"value_sha"`
	LevelSHA string         `json:"level_sha"`
	Codecs   []string       `json:"codecs"`
	Encs     []string       `json:"encodings"`
	Pages    map[string]int `json:"pages"`
}

type dumpFile struct {
	Rows       int64     `json:"rows"`
	RowGroups  int       `json:"row_groups"`
	RGRows     []int64   `json:"rg_rows"`
	Cols       []dumpCol `json:"columns"`
	CreatedBy  string    `json:"created_by"`
	Version    int32     `json:"version"`
	TotalPages int       `json:"pages"`
}

// cmdDump prints what the Z3 parser and logical decoder see in a file (schema, row groups,
// codecs, encodings, page counts, and one SHA-256 per column over its canonical value stream
// and its null-run stream); oracle/parquet/validate.py compares it with PyArrow and DuckDB.
func cmdDump(args []string) error {
	fs := flag.NewFlagSet("dump", flag.ExitOnError)
	path := fs.String("file", "", "parquet file")
	fs.Parse(args)
	data, err := os.ReadFile(*path)
	if err != nil {
		return err
	}
	r := bytes.NewReader(data)
	f, err := parquetz3.Open(r, int64(len(data)), parquetz3.Limits{})
	if err != nil {
		return err
	}
	an := parquetz3.NewAnalyzer(r, f, parquetz3.Limits{})
	d := dumpFile{Rows: f.NumRows, RowGroups: len(f.RowGroups), CreatedBy: f.CreatedBy, Version: f.Version}
	for _, rg := range f.RowGroups {
		d.RGRows = append(d.RGRows, rg.NumRows)
	}
	vh := make([]hash.Hash, len(f.Columns))
	lh := make([]hash.Hash, len(f.Columns))
	cols := make([]dumpCol, len(f.Columns))
	for i, c := range f.Columns {
		cols[i] = dumpCol{Name: c.Name, Phys: c.Phys, MaxDef: c.MaxDef, MaxRep: c.MaxRep, Pages: map[string]int{}}
		vh[i], lh[i] = sha256.New(), sha256.New()
	}
	codecs := make([]map[string]bool, len(f.Columns))
	encs := make([]map[string]bool, len(f.Columns))
	for i := range codecs {
		codecs[i], encs[i] = map[string]bool{}, map[string]bool{}
	}
	for gi := range f.RowGroups {
		for ci := range f.RowGroups[gi].Chunks {
			ch := &f.RowGroups[gi].Chunks[ci]
			codecs[ci][parquetz3.CodecName(ch.Codec)] = true
			for _, e := range ch.Encodings {
				encs[ci][parquetz3.EncodingName(e)] = true
			}
			f.Pages(r, ch, func(p *parquetz3.Page, _ []byte) error {
				d.TotalPages++
				cols[ci].Pages[pageLabel(p)]++
				return nil
			})
			opaque, err := an.Content(ch, func(rec []byte) error { vh[ci].Write(rec); cols[ci].Values++; return nil },
				func(rec []byte, _ int) error { lh[ci].Write(rec); return nil })
			if err != nil {
				return err
			}
			if opaque {
				cols[ci].Opaque++
			}
		}
	}
	for ci := range cols {
		an.FinishColumn(ci, func(rec []byte, _ int) error { lh[ci].Write(rec); return nil })
		cols[ci].ValueSHA = hex.EncodeToString(vh[ci].Sum(nil))
		cols[ci].LevelSHA = hex.EncodeToString(lh[ci].Sum(nil))
		for k := range codecs[ci] {
			cols[ci].Codecs = append(cols[ci].Codecs, k)
		}
		for k := range encs[ci] {
			cols[ci].Encs = append(cols[ci].Encs, k)
		}
		sort.Strings(cols[ci].Codecs)
		sort.Strings(cols[ci].Encs)
	}
	d.Cols = cols
	b, _ := json.MarshalIndent(d, "", " ")
	fmt.Println(string(b))
	return nil
}
