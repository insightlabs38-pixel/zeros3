// Stage A characterizes a plaintext oracle. It never claims an exact XZ transform.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"time"
	"zeros3-z3/admission"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

type input struct {
	Family, Path, File, SHA256 string
	Oracles                    []struct {
		Path, SHA256 string
		Bytes        int64
	}
}
type row struct {
	Family, File, Variant, SHA                         string
	SourceBytes, LogicalBytes, Incremental, Cumulative int64
	OuterExact                                         bool
	InteriorProofs                                     []string
	Objects                                            int
}
type policy struct {
	seen map[int]bool
	cost int64
}

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func read(p, want string) []byte {
	f, e := os.Open(p)
	must(e)
	defer f.Close()
	st, e := f.Stat()
	must(e)
	if st.Size() > 128<<20 {
		panic("source limit")
	}
	b, e := os.ReadFile(p)
	must(e)
	if fmt.Sprintf("%x", sha256.Sum256(b)) != want {
		panic("source hash")
	}
	return b
}
func scaled(data []byte, k int) (*kinds.Bundle, error) {
	r := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(data))}
	var objs [][]byte
	for _, b := range lab.ChunkScaled(data, k/4, k, k*4) {
		objs = append(objs, b)
		r.Members = append(r.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(b), Size: int64(len(b))})
	}
	return kinds.NewBundle(r, objs)
}
func main() {
	raw, e := os.ReadFile(os.Args[1])
	must(e)
	var ins []input
	must(json.Unmarshal(raw, &ins))
	c := admission.NewCapture()
	states := map[string]*policy{}
	enc := json.NewEncoder(os.Stdout)
	for _, in := range ins {
		source := read(in.Path, in.SHA256)
		for _, v := range []string{"compressed-cdc-prod", "plaintext-cdc-prod", "plaintext-cdc-4k", "tar-members"} {
			key := in.Family + "/" + v
			st := states[key]
			if st == nil {
				st = &policy{seen: map[int]bool{}}
				states[key] = st
			}
			out := row{Family: in.Family, File: in.File, Variant: v, SHA: in.SHA256, SourceBytes: int64(len(source)), OuterExact: v == "compressed-cdc-prod"}
			before := st.cost
			var datas [][]byte
			if out.OuterExact {
				datas = [][]byte{source}
			} else {
				for _, o := range in.Oracles {
					datas = append(datas, read(o.Path, o.SHA256))
				}
			}
			for i, data := range datas {
				c.ResetStore()
				var b *kinds.Bundle
				switch v {
				case "compressed-cdc-prod", "plaintext-cdc-prod":
					b, e = admission.CDC(data)
				case "plaintext-cdc-4k":
					b, e = scaled(data, 4<<10)
				case "tar-members":
					b, _, e = kinds.EncodeTarLayer(data, 1<<62)
				}
				must(e)
				c.Case = in.Family
				c.Stage = fmt.Sprintf("%s/%d", in.File, i)
				c.Class = "REAL_RELEASE"
				cand, e := c.Variant(v, data, b, time.Now())
				must(e)
				out.LogicalBytes += int64(len(data))
				out.InteriorProofs = append(out.InteriorProofs, cand.ProofSHA)
				out.Objects += len(cand.Objects)
				for _, id := range cand.Objects {
					if !st.seen[id] {
						st.seen[id] = true
						st.cost += c.Corpus.Objects[id].Packed + 64
					}
				}
			}
			out.Incremental = st.cost - before
			out.Cumulative = st.cost
			must(enc.Encode(out))
		}
	}
}
