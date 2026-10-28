package main

import (
	"compress/gzip"
	"encoding/json"
	"flag"
	"os"
	"zeros3-z3/convergence"
	"zeros3-z3/frontiercapture"
)

func main() {
	module := flag.String("module", ".", "module directory")
	public := flag.String("public", "", "public cache")
	producer := flag.String("producer", "", "producer cache")
	git := flag.String("git", "", "Git cache")
	frontier := flag.String("frontier", "", "optional captured cost frontier")
	flag.Parse()
	s := convergence.New()
	c, e := frontiercapture.Build(*module, *public, *producer, *git, s.Observe)
	if e != nil {
		panic(e)
	}
	if *frontier != "" {
		f, e := os.Create(*frontier)
		if e != nil {
			panic(e)
		}
		gz := gzip.NewWriter(f)
		if e := json.NewEncoder(gz).Encode(c); e != nil {
			panic(e)
		}
		if e := gz.Close(); e != nil {
			panic(e)
		}
		if e := f.Close(); e != nil {
			panic(e)
		}
	}
	enc := json.NewEncoder(os.Stdout)
	for _, r := range s.Rows {
		if e := enc.Encode(r); e != nil {
			panic(e)
		}
	}
}
