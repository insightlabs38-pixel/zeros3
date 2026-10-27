package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"zeros3-z3/parquetz3"
)

// cov is the byte-weighted audit of one file or of a group of files.
type cov struct {
	Files, Failed int
	Bytes         int64
	Framing       int64 // magic + gaps + footer
	Footer, Gaps  int64
	IndexMeta     int64 // column/offset index + bloom bytes declared by the footer
	Hdr           int64 // page headers
	Pages         int
	// payload classification (compressed payload bytes)
	Predicted int64 // logical pages: compression predictor (or none) and standard layout
	Explicit  int64 // logical pages needing an explicit compression recipe, layout or dictionary
	Opaque    int64 // pages of opaque chunks
	Unsup     int64 // whole files that failed to open
	// detail
	EncBytes   map[string]int64
	EncPages   map[string]int
	CompPages  map[string]int
	CompBytes  map[string]int64
	CompRec    int64
	LayoutStd  int
	LayoutLvl  int
	LayoutVal  int
	DictChunks map[string]int
	DictBytes  map[string]int64
	OpaqueWhy  map[string]int64
	V1, V2     int
	CRC, PStat int
	ChunkFlags map[string]int
	Codecs     map[string]int64
	Versions   map[string]int
	Creators   map[string]int
	Chunks     int
}

func newCov() *cov {
	return &cov{EncBytes: map[string]int64{}, EncPages: map[string]int{}, CompPages: map[string]int{}, CompBytes: map[string]int64{},
		DictChunks: map[string]int{}, DictBytes: map[string]int64{}, OpaqueWhy: map[string]int64{}, ChunkFlags: map[string]int{},
		Codecs: map[string]int64{}, Versions: map[string]int{}, Creators: map[string]int{}}
}

func (c *cov) add(o *cov) {
	c.Files += o.Files
	c.Failed += o.Failed
	c.Bytes += o.Bytes
	c.Framing += o.Framing
	c.Footer += o.Footer
	c.Gaps += o.Gaps
	c.IndexMeta += o.IndexMeta
	c.Hdr += o.Hdr
	c.Pages += o.Pages
	c.Predicted += o.Predicted
	c.Explicit += o.Explicit
	c.Opaque += o.Opaque
	c.Unsup += o.Unsup
	c.CompRec += o.CompRec
	c.LayoutStd += o.LayoutStd
	c.LayoutLvl += o.LayoutLvl
	c.LayoutVal += o.LayoutVal
	c.V1 += o.V1
	c.V2 += o.V2
	c.CRC += o.CRC
	c.PStat += o.PStat
	c.Chunks += o.Chunks
	for _, m := range []struct{ d, s map[string]int64 }{{c.EncBytes, o.EncBytes}, {c.CompBytes, o.CompBytes}, {c.DictBytes, o.DictBytes}, {c.OpaqueWhy, o.OpaqueWhy}, {c.Codecs, o.Codecs}} {
		for k, v := range m.s {
			m.d[k] += v
		}
	}
	for _, m := range []struct{ d, s map[string]int }{{c.EncPages, o.EncPages}, {c.CompPages, o.CompPages}, {c.DictChunks, o.DictChunks}, {c.ChunkFlags, o.ChunkFlags}, {c.Versions, o.Versions}, {c.Creators, o.Creators}} {
		for k, v := range m.s {
			m.d[k] += v
		}
	}
}

type schemaRow struct {
	Name    string `json:"name"`
	Phys    int    `json:"phys"`
	Logical string `json:"logical"`
	Def     int    `json:"def"`
	Rep     int    `json:"rep"`
}

type fileInfo struct {
	Name, Fam, Class string
	Size             int64
	Err              string
	CreatedBy        string
	Version          int32
	Rows             int64
	RGs, Cols        int
	Schema           []schemaRow
	Cov              *cov
}

var physNames = [...]string{"BOOLEAN", "INT32", "INT64", "INT96", "FLOAT", "DOUBLE", "BYTE_ARRAY", "FIXED_LEN_BYTE_ARRAY"}

func pageLabel(p *parquetz3.Page) string {
	switch p.Type {
	case parquetz3.PageDict:
		return "DICT"
	case parquetz3.PageDataV2:
		return "V2 " + parquetz3.EncodingName(p.Enc)
	case parquetz3.PageData:
		return "V1 " + parquetz3.EncodingName(p.Enc)
	}
	return fmt.Sprint("TYPE", p.Type)
}

// auditFile characterizes one file. It never panics on a malformed file: a failure to open
// is itself the result (UNSUPPORTED_FILE).
func auditFile(data []byte, noPredict bool) *fileInfo {
	fi := &fileInfo{Size: int64(len(data)), Cov: newCov()}
	c := fi.Cov
	c.Files, c.Bytes = 1, int64(len(data))
	r := bytes.NewReader(data)
	f, err := parquetz3.Open(r, int64(len(data)), parquetz3.Limits{})
	if err != nil {
		fi.Err = err.Error()
		c.Failed, c.Unsup = 1, int64(len(data))
		return fi
	}
	fi.CreatedBy, fi.Version, fi.Rows, fi.RGs, fi.Cols = f.CreatedBy, f.Version, f.NumRows, len(f.RowGroups), len(f.Columns)
	c.Versions[fmt.Sprint(f.Version)]++
	c.Creators[strings.Fields(f.CreatedBy + " ?")[0]]++
	for _, col := range f.Columns {
		fi.Schema = append(fi.Schema, schemaRow{col.Name, col.Phys, col.Logical, col.MaxDef, col.MaxRep})
	}
	spans, err := f.Spans()
	if err != nil {
		fi.Err = err.Error()
		c.Failed, c.Unsup = 1, int64(len(data))
		return fi
	}
	an := parquetz3.NewAnalyzer(r, f, parquetz3.Limits{})
	an.NoPredict = noPredict
	for _, s := range spans {
		switch s.Kind {
		case parquetz3.SpanMagic:
			c.Framing += s.Len
		case parquetz3.SpanGap:
			c.Framing += s.Len
			c.Gaps += s.Len
		case parquetz3.SpanFooter:
			c.Framing += s.Len
			c.Footer += s.Len
		}
	}
	for gi := range f.RowGroups {
		for ci := range f.RowGroups[gi].Chunks {
			ch := &f.RowGroups[gi].Chunks[ci]
			c.Chunks++
			c.Codecs[parquetz3.CodecName(ch.Codec)] += ch.TotalComp
			for name, ok := range map[string]bool{"stats": ch.HasStats, "encoding_stats": ch.HasEncStats, "size_stats": ch.HasSizeStats,
				"column_index": ch.ColIdx[0] >= 0, "offset_index": ch.OffIdx[0] >= 0, "bloom_filter": ch.Bloom[0] >= 0, "dictionary_page": ch.DictOff > 0} {
				if ok {
					c.ChunkFlags[name]++
				}
			}
			for _, x := range [][2]int64{ch.ColIdx, ch.OffIdx, ch.Bloom} {
				if x[0] >= 0 && x[1] > 0 {
					c.IndexMeta += x[1]
				}
			}
			plan, err := an.Chunk(ch, nil, nil)
			if err != nil {
				fi.Err = err.Error()
				c.Failed, c.Unsup = 1, int64(len(data))
				return fi
			}
			if plan.Opaque && os.Getenv("Z3PQ_WHY") != "" {
				fmt.Fprintf(os.Stderr, "  opaque %d/%d %s: %s\n", gi, ci, f.Columns[ci].Name, plan.Reason)
			}
			if plan.Opaque {
				why := plan.Reason
				if i := strings.Index(why, ":"); i > 0 && strings.HasPrefix(why, "page") {
					why = "page: " + strings.TrimSpace(why[i+1:])
				}
				c.OpaqueWhy[why] += ch.TotalComp
			}
			if !plan.Opaque && plan.DictMode != parquetz3.DictNone {
				c.DictChunks[[...]string{"none", "predicted", "explicit"}[plan.DictMode]]++
				c.DictBytes[[...]string{"none", "predicted", "explicit"}[plan.DictMode]] += ch.TotalComp
			}
			err = f.Pages(r, ch, func(p *parquetz3.Page, hdr []byte) error {
				c.Pages++
				c.Hdr += int64(p.HdrLen)
				if p.HasCRC {
					c.CRC++
				}
				if p.HasStats {
					c.PStat++
				}
				switch p.Type {
				case parquetz3.PageDataV2:
					c.V2++
				case parquetz3.PageData:
					c.V1++
				}
				key := parquetz3.CodecName(ch.Codec) + " " + pageLabel(p)
				c.EncBytes[key] += int64(p.Comp)
				c.EncPages[key]++
				return nil
			})
			if err != nil {
				fi.Err = err.Error()
				c.Failed, c.Unsup = 1, int64(len(data))
				return fi
			}
			if plan.Opaque {
				c.Opaque += ch.TotalComp - hdrBytes(plan, f, r, ch)
				continue
			}
			for i := range plan.Pages {
				pi := &plan.Pages[i]
				pay := int64(pi.Page.Comp)
				c.CompPages[parquetz3.CompName(int(pi.Comp.ID))]++
				c.CompBytes[parquetz3.CompName(int(pi.Comp.ID))] += pay
				c.CompRec += int64(pi.CompRecBytes)
				explicit := pi.Comp.ID == parquetz3.CompSnapCmds || pi.Comp.ID == parquetz3.CompZstd
				if pi.Mode == parquetz3.ModeData {
					l, v := pi.Lay.Explicit()
					switch {
					case l || v:
						explicit = true
						if l {
							c.LayoutLvl++
						}
						if v {
							c.LayoutVal++
						}
					default:
						c.LayoutStd++
					}
				}
				if pi.Mode == parquetz3.ModeDict && plan.DictMode == parquetz3.DictExplicit {
					explicit = true
				}
				if explicit {
					c.Explicit += pay
				} else {
					c.Predicted += pay
				}
			}
		}
	}
	return fi
}

func hdrBytes(plan *parquetz3.ChunkPlan, f *parquetz3.File, r *bytes.Reader, ch *parquetz3.Chunk) int64 {
	var n int64
	f.Pages(r, ch, func(p *parquetz3.Page, _ []byte) error { n += int64(p.HdrLen); return nil })
	return n
}

func pct(a, b int64) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(a)/float64(b))
}

func mib(n int64) string { return fmt.Sprintf("%.2f", float64(n)/(1<<20)) }

func sortedKeys[V any](m map[string]V) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// cmdAudit characterizes the corpus: per file group, formats/producers/encodings/codecs and
// the byte-weighted exact-coverage classification.
func cmdAudit(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	fams := fs.String("fam", "pyarrow-variants,duckdb-variants,pyarrow-chain,controls", "families")
	jsonOut := fs.String("json", "", "write the per-file audit as JSON")
	detail := fs.Bool("detail", false, "print schema and per-column detail of the first file of each group")
	noPred := fs.Bool("nopredict", false, "force explicit recipes (measurement)")
	extra := fs.String("file", "", "audit these files too (comma separated)")
	fs.Parse(args)
	type group struct {
		name   string
		states []state
	}
	var groups []group
	for _, fam := range strings.Split(*fams, ",") {
		sts, _, err := loadFamily(fam)
		if err != nil {
			return err
		}
		groups = append(groups, group{fam, sts})
	}
	if rs := refStates(); len(rs) > 0 {
		groups = append(groups, group{"ref", rs})
	}
	if *extra != "" {
		var sts []state
		for _, p := range strings.Split(*extra, ",") {
			sts = append(sts, state{"extra", p, ClsWorkload, "", "", p})
		}
		groups = append(groups, group{"extra", sts})
	}
	var all []*fileInfo
	tot := map[string]*cov{}
	for _, g := range groups {
		gc := newCov()
		for _, s := range g.states {
			b, err := readFile(s.Path)
			if err != nil {
				return err
			}
			fi := auditFile(b, *noPred)
			fi.Name, fi.Fam, fi.Class = s.Rev, s.Fam, s.Class
			all = append(all, fi)
			gc.add(fi.Cov)
			if *detail && fi.Name == g.states[0].Rev {
				printDetail(fi)
			}
			if fi.Err != "" {
				fmt.Fprintf(os.Stderr, "%s/%s: %s\n", s.Fam, s.Rev, fi.Err)
			}
		}
		tot[g.name] = gc
	}
	fmt.Println("| group | class | files (unsupported) | MiB | framing | page hdrs | predicted payload | explicit payload | opaque payload | unsupported file |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|")
	for _, g := range groups {
		c := tot[g.name]
		cls := g.states[0].Class
		if g.name == "controls" {
			cls = "PRODUCER_WORKLOAD (controls)"
		}
		fmt.Printf("| %s | %s | %d (%d) | %s | %s | %s | %s | %s | %s | %s |\n", g.name, cls, c.Files, c.Failed, mib(c.Bytes),
			pct(c.Framing, c.Bytes), pct(c.Hdr, c.Bytes), pct(c.Predicted, c.Bytes), pct(c.Explicit, c.Bytes), pct(c.Opaque, c.Bytes), pct(c.Unsup, c.Bytes))
	}
	for _, g := range groups {
		c := tot[g.name]
		fmt.Printf("\n#### %s (%d files, %d pages, %d chunks)\n", g.name, c.Files, c.Pages, c.Chunks)
		fmt.Printf("creators %v, format versions %v, v1/v2 data pages %d/%d, page CRC %d, page stats %d\n", c.Creators, c.Versions, c.V1, c.V2, c.CRC, c.PStat)
		fmt.Printf("chunk features %v; index/bloom bytes declared %s of %s (%s); gaps %s; footer %s\n", c.ChunkFlags, mib(c.IndexMeta), mib(c.Bytes), pct(c.IndexMeta, c.Bytes), pct(c.Gaps, c.Bytes), pct(c.Footer, c.Bytes))
		fmt.Printf("codecs (compressed chunk bytes): ")
		for _, k := range sortedKeys(c.Codecs) {
			fmt.Printf("%s %s  ", k, pct(c.Codecs[k], c.Bytes))
		}
		fmt.Println()
		fmt.Println("| codec / page / encoding | pages | payload MiB | % file |")
		fmt.Println("|---|---:|---:|---:|")
		for _, k := range sortedKeys(c.EncBytes) {
			fmt.Printf("| %s | %d | %s | %s |\n", k, c.EncPages[k], mib(c.EncBytes[k]), pct(c.EncBytes[k], c.Bytes))
		}
		fmt.Print("compression replay: ")
		var cb int64
		for _, v := range c.CompBytes {
			cb += v
		}
		for _, k := range sortedKeys(c.CompPages) {
			fmt.Printf("%s %d pages %s of replayed payload bytes; ", k, c.CompPages[k], pct(c.CompBytes[k], cb))
		}
		fmt.Printf("explicit recipe bytes %s (%s of file)\n", mib(c.CompRec), pct(c.CompRec, c.Bytes))
		fmt.Printf("layout: %d pages standard, %d explicit level streams, %d explicit value/index streams; dictionary chunks %v (bytes %v)\n", c.LayoutStd, c.LayoutLvl, c.LayoutVal, c.DictChunks, c.DictBytes)
		if len(c.OpaqueWhy) > 0 {
			fmt.Printf("opaque chunk reasons (payload MiB): ")
			for _, k := range sortedKeys(c.OpaqueWhy) {
				fmt.Printf("%q %s; ", k, mib(c.OpaqueWhy[k]))
			}
			fmt.Println()
		}
	}
	if *jsonOut != "" {
		b, _ := json.MarshalIndent(all, "", " ")
		return os.WriteFile(*jsonOut, b, 0o644)
	}
	return nil
}

func printDetail(fi *fileInfo) {
	fmt.Printf("\n##### %s/%s: %s, format version %d, %d rows, %d row groups, %d columns\n", fi.Fam, fi.Name, fi.CreatedBy, fi.Version, fi.Rows, fi.RGs, fi.Cols)
	for _, s := range fi.Schema {
		fmt.Printf("  %-8s %-22s logical=%-10s def=%d rep=%d\n", s.Name, physNames[s.Phys], s.Logical, s.Def, s.Rep)
	}
}
