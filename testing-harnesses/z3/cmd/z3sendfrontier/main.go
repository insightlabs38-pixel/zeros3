// Send-stream Stage A compares CDC with native-oracle record/payload boundaries.
package main

import (
	"bytes"
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
	"zeros3-z3/sendz3"
)

type input struct {
	Family, Path, File, SHA256, Stage, Class_ string
	Resources                                 [][2]int64 `json:"resource_spans"`
	Chunks                                    [][2]int64 `json:"chunk_spans"`
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
	Ranges                                             []rangeSample
	Additive                                           *int64
}
type rangeSample struct {
	Offset, Length, ReadBytes int64
	Reads, Objects            int
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
		var baseline []int
		variants := []string{"source-cdc-prod", "source-cdc-4k"}
		if len(in.Resources) != 0 {
			variants = append(variants, "native-record-oracle", "native-payload-oracle", "strict-legacy-payload")
		}
		for _, v := range variants {
			key := in.Family + "/" + v
			st := states[key]
			if st == nil {
				st = &policy{seen: map[int]bool{}}
				states[key] = st
			}
			out := row{Family: in.Family, File: in.Stage, Class: in.Class_, Variant: v, SHA: in.SHA256, SourceBytes: int64(len(source)), OuterExact: !strings.HasPrefix(v, "logical-")}
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
				case "source-cdc-4k", "logical-cdc-4k":
					b, e = scaled(data, 4<<10)
				case "native-record-oracle":
					b, e = native(data, in.Resources)
				case "strict-legacy-payload":
					b, e = sendz3.Encode(data)
				case "native-payload-oracle":
					b, e = native(data, in.Chunks)
				default:
					panic("unknown variant")
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
				if v == "source-cdc-prod" {
					baseline = append([]int(nil), cand.Objects...)
				}
				if out.OuterExact {
					unionKey := in.Family + "/additive/" + v
					union := states[unionKey]
					if union == nil {
						union = &policy{seen: map[int]bool{}}
						states[unionKey] = union
					}
					for _, ids := range [][]int{baseline, cand.Objects} {
						for _, id := range ids {
							if !union.seen[id] {
								union.seen[id] = true
								union.cost += c.Corpus.Objects[id].Packed + 64
							}
						}
					}
					value := union.cost
					out.Additive = &value
				}

				if out.OuterExact {
					proofEnv := kinds.NewEnvLimits(c.Store, repz3.Limits{MaxDepth: 4, MaxWhole: scientificz3.MaxRegion})
					reach, err := proofEnv.Reachable(b.ID)
					must(err)
					n := min(int64(4096), int64(len(data)))
					for _, off := range []int64{0, (int64(len(data)) - n) / 2, int64(len(data)) - n} {
						c.Store.ResetReads()
						env := kinds.NewEnvLimits(c.Store, repz3.Limits{MaxDepth: 4, MaxWhole: scientificz3.MaxRegion})
						call, err := env.Open(b.ID)
						must(err)
						got, err := scientificz3.ReadRange(call, off, n)
						must(err)
						if !bytes.Equal(got, data[off:off+n]) {
							panic("cold serialized range mismatch")
						}
						reads, touched := c.Store.Touched()
						sample := rangeSample{Offset: off, Length: n, Reads: reads, Objects: len(touched)}
						for id, n := range touched {
							sample.ReadBytes += n
							if _, ok := reach.Role[id]; !ok {
								panic("range read unprotected object")
							}
						}
						out.Ranges = append(out.Ranges, sample)
					}
				}

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
