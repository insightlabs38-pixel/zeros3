// Stage A compares native-region and logical oracles with source CDC.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"zeros3-z3/admission"
	"zeros3-z3/lab"
	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
	"zeros3-z3/scientificz3"
)

type input struct {
	Family, Path, File, SHA256, Stage, Class_ string
	Spans                                     [][2]int64
	Oracles                                   []struct {
		Path, SHA256 string
		Bytes        int64
	}
}
type row struct {
	Family, File, Variant, SHA, Class                  string
	SourceBytes, LogicalBytes, Incremental, Cumulative int64
	OuterExact                                         bool
	InteriorProofs                                     []string
	Objects                                            int
	Fallback                                           string
	Detail                                             scientificz3.Stats
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

// Region positions come from the isolated native oracle in Stage A, not a
// candidate parser. Unknown framing is retained exactly and split by 4K CDC.
func native(data []byte, spans [][2]int64) (*kinds.Bundle, error) {
	r := &repz3.Rep{Kind: repz3.KindConcat, Size: int64(len(data))}
	var objects [][]byte
	add := func(b []byte) {
		if len(b) == 0 {
			return
		}
		objects = append(objects, b)
		r.Members = append(r.Members, repz3.Member{Role: repz3.Stored, SHA: sha256.Sum256(b), Size: int64(len(b))})
	}
	gap := func(b []byte) {
		for _, p := range lab.ChunkScaled(b, 1<<10, 4<<10, 16<<10) {
			add(p)
		}
	}
	off := int64(0)
	for _, s := range spans {
		if s[0] < off || s[1] <= 0 || s[0] > int64(len(data)) || s[1] > int64(len(data))-s[0] {
			return nil, fmt.Errorf("native span bounds")
		}
		gap(data[off:s[0]])
		add(data[s[0] : s[0]+s[1]])
		off = s[0] + s[1]
	}
	gap(data[off:])
	return kinds.NewBundle(r, objects)
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
		for _, v := range []string{"source-cdc-prod", "native-span-oracle", "logical-cdc-prod", "logical-cdc-4k", "native-fine-exact", "planes-codec-exact"} {
			key := in.Family + "/" + v
			st := states[key]
			if st == nil {
				st = &policy{seen: map[int]bool{}}
				states[key] = st
			}
			out := row{Family: in.Family, File: in.Stage, Class: in.Class_, Variant: v, SHA: in.SHA256, SourceBytes: int64(len(source)), OuterExact: v == "source-cdc-prod" || v == "native-span-oracle" || v == "native-fine-exact" || v == "planes-codec-exact"}
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
				case "source-cdc-prod", "logical-cdc-prod":
					b, e = admission.CDC(data)
				case "logical-cdc-4k":
					b, e = scaled(data, 4<<10)
				case "native-span-oracle":
					b, e = native(data, in.Spans)
				case "native-fine-exact", "planes-codec-exact":
					if strings.HasPrefix(in.Family, "hdf5") {
						b, out.Detail, e = scientificz3.EncodeHDF5(data, v == "planes-codec-exact")
					} else if strings.HasPrefix(in.Family, "arrow") {
						b, out.Detail, e = scientificz3.EncodeArrow(data, v == "planes-codec-exact")
					} else {
						e = scientificz3.ErrUnsupported
					}
					if e != nil {
						out.Fallback = e.Error()
						b, e = admission.CDC(data)
					}

				}
				must(e)
				c.Case = in.Family
				c.Stage = fmt.Sprintf("%s/%d", in.Stage, i)
				c.Class = in.Class_
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
