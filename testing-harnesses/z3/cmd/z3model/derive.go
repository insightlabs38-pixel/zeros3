package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/modelz3"
	"zeros3-z3/st"
)

// runDerive writes the layout-only re-exports (REAL_REEXPORT: real tensor bytes, changed framing).
func runDerive(s *store, out string) {
	write := func(rel string, b []byte) {
		p := filepath.Join(out, rel)
		check(os.MkdirAll(filepath.Dir(p), 0o755))
		check(os.WriteFile(p, b, 0o644))
		h := sha256.Sum256(b)
		fmt.Printf("%s\t%d\t%s\n", rel, len(b), hex.EncodeToString(h[:]))
	}
	g := s.get("gguf-smollm2/qf-inst-Q4_K_M")
	for name, e := range map[string]modelz3.GGUFEdit{
		"smollm2-inst-Q4_K_M.addkv.gguf":   {AddKV: [][2]string{{"z3.note", strings.Repeat("re-export control ", 170)}}},
		"smollm2-inst-Q4_K_M.reverse.gguf": {Reverse: true},
		"smollm2-inst-Q4_K_M.align64.gguf": {Align: 64},
	} {
		var b bytes.Buffer
		check(modelz3.RewriteGGUF(g.f, g.Size, e, &b))
		write("reexport/"+name, b.Bytes())
	}
	i := s.get("st-smollm2-star/instruct")
	for name, e := range map[string]modelz3.STEdit{
		"smollm2-inst.reverse": {Reverse: true},
		"smollm2-inst.shards3": {Shards: 3},
		"smollm2-inst.shards5": {Shards: 5},
	} {
		files, idx, err := modelz3.RewriteST(i.f, i.Size, e)
		check(err)
		if len(files) == 1 {
			write("reexport/"+name+".safetensors", files[0])
			continue
		}
		for k, f := range files {
			write(fmt.Sprintf("reexport/%s/model-%05d-of-%05d.safetensors", name, k+1, len(files)), f)
		}
		write("reexport/"+name+"/model.safetensors.index.json", idx)
	}
	// one real tensor changed: instruct with the embedding taken from the SFT-only sibling
	o := s.get("st-smollm2-star/sft-only")
	fi, _ := st.Parse(i.f, i.Size, st.Options{})
	fo, _ := st.Parse(o.f, o.Size, st.Options{})
	var ts []st.BuildTensor
	for k := range fi.Tensors {
		t := &fi.Tensors[k]
		src, sf := i, fi
		if t.Name == "model.embed_tokens.weight" {
			src, sf = o, fo
		}
		tt, ok := sf.Lookup(t.Name)
		if !ok {
			fatalf("no %s", t.Name)
		}
		b, err := sf.ReadTensor(src.f, tt, 0, tt.Size())
		check(err)
		ts = append(ts, st.BuildTensor{Name: t.Name, DType: t.DType, Shape: t.Shape, Data: b})
	}
	write("reexport/smollm2-inst.onetensor.safetensors", st.Build([]st.KV{{K: "format", V: "pt"}}, ts))
}
