// z3model is the Z3-29 model-artifact laboratory: real GGUF, Safetensors and PyTorch ZIP
// corpora, structural and delta representations on the repz3 contract.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

	"zeros3-z3/ggufz3"
	"zeros3-z3/modelz3"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: z3model gguf-info|... [-manifest f -cache d -groups a,b]")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	manifest := fs.String("manifest", "realworld/models.json", "pinned corpus")
	cache := fs.String("cache", "/home/user/z3cache/rw", "download cache")
	filter := fs.String("only", "", "substring filter")
	noProve := fs.Bool("no-prove", false, "exactness check only on the last revision")
	noPXR := fs.Bool("no-pxr", false, "skip the PXR container pass")
	fs.Parse(os.Args[2:])
	c, err := modelz3.LoadCorpus(*manifest, *cache)
	check(err)
	switch os.Args[1] {
	case "gguf-info":
		cover := map[string]int64{}
		var total int64
		for _, e := range c.Entries {
			if e.Format != "gguf" {
				continue
			}
			f, err := c.Open(e)
			check(err)
			g, err := ggufz3.Parse(f, e.Size, ggufz3.Limits{})
			if err != nil {
				fmt.Printf("%-28s %-22s PARSE ERROR %v\n", e.Group, e.Name, err)
				continue
			}
			var hdr, tens int64
			for _, s := range g.Segments() {
				if s.Kind == "tensor" {
					tens += s.End - s.Begin
					cover[ggufz3.Geom[g.Tensors[s.Tensor].Type].Name] += s.End - s.Begin
				}
			}
			hdr = e.Size - tens
			total += e.Size
			arch, _ := g.Lookup("general.architecture")
			fmt.Printf("%-24s %-18s v%d align %d kv %d tensors %d header+gaps %d (%.3f%%) tail %d arch %s\n", e.Group, e.Name, g.Version, g.Alignment, len(g.KVs), len(g.Tensors), hdr, 100*float64(hdr)/float64(e.Size), e.Size-g.DataEnd, arch.Str)
			f.Close()
		}
		var ks []string
		for k := range cover {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			fmt.Printf("type %-8s %12d bytes %.2f%%\n", k, cover[k], 100*float64(cover[k])/float64(total))
		}
	case "st":
		runST(newStore(c, "/home/user/z3cache/derived"), *filter, !*noPXR)
	case "deep":
		runDeep(newStore(c, "/home/user/z3cache/derived"), *filter, !*noProve)
	case "entropy":
		runEntropy(newStore(c, "/home/user/z3cache/derived"), fs.Arg(0), fs.Arg(1))
	case "derive":
		runDerive(newStore(c, ""), fs.Arg(0))
	case "reexport":
		runReexport(newStore(c, "/home/user/z3cache/derived"))
	case "meta":
		runMeta(newStore(c, "/home/user/z3cache/derived"))
	case "reads":
		runReads(newStore(c, "/home/user/z3cache/derived"))
	case "shards":
		runShards(newStore(c, "/home/user/z3cache/derived"), "/home/user/z3cache/shardwork")
	case "fields":
		runFields(newStore(c, "/home/user/z3cache/derived"), fs.Arg(0), fs.Arg(1))
	case "pt":
		runPT(newStore(c, "/home/user/z3cache/derived"), *filter)
	case "gguf":
		runGGUF(newStore(c, "/home/user/z3cache/derived"), *filter)
	default:
		fmt.Fprintln(os.Stderr, "unknown command", os.Args[1])
		os.Exit(2)
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "z3model:", err)
		os.Exit(1)
	}
}
