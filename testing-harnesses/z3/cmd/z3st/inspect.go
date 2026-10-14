package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"zeros3-z3/corpus"
	"zeros3-z3/st"
)

// inspectCmd lists every file's structure and measures the I/O of a bounded,
// range-friendly parse (Z3-09A/B): 8-byte prefix, header, then one tensor range.
func inspectCmd(args []string) {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	dir := fs.String("corpus", "", "corpus directory")
	fams := fs.String("families", "", "family globs")
	fs.Parse(args)
	m, err := corpus.ReadManifest(*dir)
	if err != nil {
		fatal(err)
	}
	names, by := m.Group()
	fmt.Println("| family | file | size MiB | header bytes | header % | pad | tensors | dtypes (tensors) | metadata | parse reads | parse bytes | one-tensor range read |")
	fmt.Println("|---|---|---:|---:|---:|---:|---:|---|---|---:|---:|---|")
	for _, fam := range names {
		if !matchAny(split(*fams), fam) || by[fam][0].Format != "safetensors" {
			continue
		}
		for _, mem := range by[fam] {
			p, _ := corpus.MemberPath(*dir, mem)
			fh, err := os.Open(p)
			if err != nil {
				fatal(err)
			}
			stt, _ := fh.Stat()
			cr := &st.CountingReaderAt{R: fh}
			f, err := st.Parse(cr, stt.Size(), st.Options{})
			if err != nil {
				fatal(fmt.Errorf("%s: %w", p, err))
			}
			parseReads, parseBytes := cr.Reads, cr.Bytes
			// largest tensor, first 4 KiB of it
			var big *st.Tensor
			for i := range f.Tensors {
				if big == nil || f.Tensors[i].Size() > big.Size() {
					big = &f.Tensors[i]
				}
			}
			cr.Reads, cr.Bytes = 0, 0
			rr := "-"
			if big != nil {
				n := min(big.Size(), 4096)
				if _, err := f.ReadTensor(cr, big, big.Size()/2, min(n, big.Size()-big.Size()/2)); err != nil {
					fatal(err)
				}
				rr = fmt.Sprintf("%d read(s), %d B", cr.Reads, cr.Bytes)
			}
			dt := map[string]int{}
			for i := range f.Tensors {
				dt[f.Tensors[i].DType]++
			}
			var dts []string
			for k, v := range dt {
				dts = append(dts, fmt.Sprintf("%s %d", k, v))
			}
			sort.Strings(dts)
			var meta []string
			for _, k := range f.MetaKeys {
				meta = append(meta, k+"="+f.Metadata[k])
			}
			fmt.Printf("| %s | %s | %.1f | %d | %.3f | %d | %d | %s | %s | %d | %d | %s |\n", strings.TrimPrefix(fam, "rw-"), mem.Mutation, float64(stt.Size())/(1<<20),
				f.HeaderLen, 100*float64(8+f.HeaderLen)/float64(stt.Size()), f.PadLen, len(f.Tensors), strings.Join(dts, ", "), strings.Join(meta, "; "), parseReads, parseBytes, rr)
			fh.Close()
		}
	}
}
