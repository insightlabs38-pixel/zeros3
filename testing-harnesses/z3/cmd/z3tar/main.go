// z3tar characterises tar-aware segmentation of container layers and archives
// (Z3-11). Inputs are z3 corpora (`z3corpus gen` or `z3fetch -out`) whose
// members are plain tar or gzip-compressed tar.
//
//	parse   structure of every revision: members, formats, types, header/payload/trailer bytes, unsupported reasons
//	churn   per update pair: how much of the plaintext CDC floor is header churn, member payload reuse, header cost alternatives
//	range   what reconstructing one member / a tar byte range / a compressed range touches, with and without indexes
//
// Laboratory code: prints Markdown, writes nothing.
package main

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"zeros3-z3/corpus"
	"zeros3-z3/lab"
	"zeros3-z3/tarz3"
)

type rev struct {
	fam, label string
	index      int
	gz         bool
	raw        []byte // file bytes
	plain      []byte // tar bytes
	ar         *tarz3.Archive
	perr       error
	digests    map[[32]byte]int64 // payload digest -> bytes (distinct)
	chunks     map[[32]byte]int
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "z3tar:", err)
	os.Exit(1)
}

func match(pats []string, s string) bool {
	if len(pats) == 0 {
		return true
	}
	for _, p := range pats {
		if p == s || "rw-"+p == s {
			return true
		}
		if ok, _ := pathMatch(p, s); ok {
			return true
		}
	}
	return false
}

func load(dir string, fams []string) (map[string][]*rev, []string) {
	m, err := corpus.ReadManifest(dir)
	if err != nil {
		fatal(err)
	}
	names, by := m.Group()
	out := map[string][]*rev{}
	var order []string
	for _, fam := range names {
		if !match(fams, fam) {
			continue
		}
		f0 := by[fam][0].Format
		if f0 != "tar" && f0 != "gzip" {
			continue
		}
		var list []*rev
		for _, mem := range by[fam] {
			p, err := corpus.MemberPath(dir, mem)
			if err != nil {
				fatal(err)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				fatal(err)
			}
			label := mem.Mutation
			if label == "" || strings.Contains(label, " ") {
				label = mem.Revision
			}
			r := &rev{fam: fam, label: label, index: mem.Index, raw: b, plain: b}
			if mem.Format == "gzip" {
				g, err := gzip.NewReader(bytes.NewReader(b))
				if err != nil {
					continue
				}
				pl, err := io.ReadAll(io.LimitReader(g, 1<<31))
				if err != nil {
					continue
				}
				r.gz, r.plain = true, pl
			}
			r.ar, r.perr = tarz3.Parse(r.plain, tarz3.Options{})
			list = append(list, r)
		}
		if len(list) > 0 {
			out[fam] = list
			order = append(order, fam)
		}
	}
	return out, order
}

func (r *rev) payloadDigests() map[[32]byte]int64 {
	if r.digests == nil {
		r.digests = map[[32]byte]int64{}
		if r.ar != nil {
			for i := range r.ar.Members {
				m := &r.ar.Members[i]
				if m.PayLen > 0 {
					r.digests[sha256.Sum256(r.plain[m.PayOff:m.PayOff+m.PayLen])] = m.PayLen
				}
			}
		}
	}
	return r.digests
}

func (r *rev) cdcChunks() map[[32]byte]int {
	if r.chunks == nil {
		r.chunks = map[[32]byte]int{}
		lab.ChunkCDC(bytes.NewReader(r.plain), func(_ int64, c []byte) error {
			r.chunks[sha256.Sum256(c)] = len(c)
			return nil
		})
	}
	return r.chunks
}

func pct(a, b int64) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f", 100*float64(a)/float64(b))
}

func short(f string) string { return strings.TrimPrefix(f, "rw-") }

func flateLen(b []byte) int {
	var buf bytes.Buffer
	w, _ := flate.NewWriter(&buf, 6)
	w.Write(b)
	w.Close()
	return buf.Len()
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3tar parse|churn|range|admit -corpus DIR [-families a,b] | cost -from report.json")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fam := fs.String("families", "", "comma list of family globs")
	trials := fs.Int("trials", 30, "range: random ranges per size")
	if os.Args[1] == "cost" {
		runCost(os.Args[2:])
		return
	}
	rest := splitFlags(os.Args[2:])
	fs.Parse(rest.common)
	if *dir == "" {
		fatal(fmt.Errorf("-corpus is required"))
	}
	var fl []string
	if *fam != "" {
		fl = strings.Split(*fam, ",")
	}
	revs, order := load(*dir, fl)
	switch os.Args[1] {
	case "parse":
		runParse(revs, order)
	case "churn":
		runChurn(revs, order)
	case "range":
		runRange(revs, order, *trials)
	case "admit":
		runAdmit(revs, order, rest.extra)
	default:
		fatal(fmt.Errorf("unknown command %q", os.Args[1]))
	}
}

func sortedKeys[K ~string | ~byte, V any](m map[K]V) []K {
	var ks []K
	for k := range m {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i] < ks[j] })
	return ks
}

// splitFlags separates the flags shared by every command from admit's own.
type flagSplit struct{ common, extra []string }

func splitFlags(args []string) flagSplit {
	var fsplit flagSplit
	own := map[string]bool{"-from": true, "-ref-bytes": true, "--from": true, "--ref-bytes": true}
	for i := 0; i < len(args); i++ {
		if own[args[i]] {
			fsplit.extra = append(fsplit.extra, args[i])
			if i+1 < len(args) {
				fsplit.extra = append(fsplit.extra, args[i+1])
				i++
			}
			continue
		}
		fsplit.common = append(fsplit.common, args[i])
	}
	return fsplit
}
