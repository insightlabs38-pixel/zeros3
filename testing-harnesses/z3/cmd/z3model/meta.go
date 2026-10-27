package main

import (
	"fmt"
	"strings"

	"zeros3-z3/ggufz3"
	"zeros3-z3/st"
	"zeros3-z3/torchz3"
)

// runMeta prints producer provenance read from the artifacts themselves.
func runMeta(s *store) {
	type agg struct {
		n  int
		ex string
	}
	gg := map[string]*agg{}
	for _, e := range s.c.Entries {
		f := s.get(e.Group + "/" + e.Name)
		switch e.Format {
		case "gguf":
			g, err := ggufz3.Parse(f.f, f.Size, ggufz3.Limits{})
			check(err)
			get := func(k string) string {
				if kv, ok := g.Lookup(k); ok {
					if kv.Type == ggufz3.TString {
						return kv.Str
					}
					return fmt.Sprint(kv.Uint)
				}
				return "-"
			}
			key := fmt.Sprintf("GGUF v%d quantization_version=%s quantized_by=%s", g.Version, get("general.quantization_version"), get("general.quantized_by"))
			if gg[key] == nil {
				gg[key] = &agg{ex: e.Group}
			}
			gg[key].n++
		case "safetensors":
			sf, err := st.Parse(f.f, f.Size, st.Options{AllowUnknownDType: true})
			check(err)
			key := fmt.Sprintf("Safetensors header metadata=%v", sf.Metadata)
			if gg[key] == nil {
				gg[key] = &agg{ex: e.Group}
			}
			gg[key].n++
		case "pytorch-zip":
			z, err := torchz3.Parse(f.f, f.Size, torchz3.Limits{})
			check(err)
			var parts []string
			for _, m := range z.Members {
				n := m.Name[strings.Index(m.Name, "/")+1:]
				switch {
				case n == "version", n == "byteorder", n == ".data/serialization_id", n == ".format_version", n == ".storage_alignment":
					parts = append(parts, n)
				}
			}
			key := fmt.Sprintf("PyTorch ZIP prefix=%s members=%d zip64=%v extras=%v", z.Members[0].Name[:strings.Index(z.Members[0].Name, "/")], len(z.Members), z.Zip64, parts)
			if gg[key] == nil {
				gg[key] = &agg{ex: e.Group}
			}
			gg[key].n++
		}
	}
	for k, v := range gg {
		fmt.Printf("%3d x %s (e.g. %s)\n", v.n, k, v.ex)
	}
}
