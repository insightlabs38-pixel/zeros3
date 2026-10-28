// z3admission captures proved physical candidate frontiers and replays policies.
package main

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"zeros3-z3/admission"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func main() {
	mode := flag.String("mode", "replay", "capture|replay")
	in := flag.String("in", "", "frontier JSON[.gz]")
	out := flag.String("out", "", "output path")
	module := flag.String("module", ".", "Z3 module directory")
	public := flag.String("public", "", "pinned public input cache")
	producer := flag.String("producer", "", "native producer input cache")
	git := flag.String("git", "", "cached I1C Git reexports")
	overhead := flag.Int64("object-overhead", -1, "optional physical-object cost sensitivity, bytes")
	flag.Parse()
	if *mode == "capture" {
		c, e := capture(*module, *public, *producer, *git)
		must(e)
		f, e := os.Create(*out)
		must(e)
		defer f.Close()
		var w io.Writer = f
		var gz *gzip.Writer
		if strings.HasSuffix(*out, ".gz") {
			gz = gzip.NewWriter(f)
			w = gz
		}
		must(json.NewEncoder(w).Encode(c))
		if gz != nil {
			must(gz.Close())
		}
		fmt.Fprintf(os.Stderr, "captured cases=%d physical_objects=%d\n", len(c.Cases), len(c.Objects))
		return
	}
	f, e := os.Open(*in)
	must(e)
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(*in, ".gz") {
		gz, e := gzip.NewReader(f)
		must(e)
		defer gz.Close()
		r = gz
	}
	limited := &io.LimitedReader{R: r, N: (64 << 20) + 1}
	d := json.NewDecoder(limited)
	d.DisallowUnknownFields()
	var c admission.Corpus
	must(d.Decode(&c))
	if *overhead >= 0 {
		c.ObjectOverhead = *overhead
	}
	must(c.Validate())
	var trailing any
	if e := d.Decode(&trailing); e != io.EOF || limited.N <= 0 {
		panic("frontier trailing data or size bound")
	}
	var w io.Writer = os.Stdout
	if *out != "" {
		f, e := os.Create(*out)
		must(e)
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)
	for _, f := range c.Cases {
		for _, p := range admission.Policies {
			result, e := c.Run(f, p)
			must(e)
			must(enc.Encode(result))
		}
	}
}
