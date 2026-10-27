package modelz3

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"zeros3-z3/torchz3"
)

// Oracle is the output of realworld/pt_oracle.py for one checkpoint: ORACLE data (the tensor
// name, dtype and shape behind each data/<id> storage member), never an input to a candidate
// production mechanism.
type Oracle struct {
	ArchivePrefix string `json:"archive_prefix"`
	Tensors       []struct {
		Name   string  `json:"name"`
		Member string  `json:"member"`
		DType  string  `json:"dtype"`
		Width  int     `json:"width"`
		Shape  []int64 `json:"shape"`
		Offset int64   `json:"offset"`
	} `json:"tensors"`
}

func LoadOracle(path string) (*Oracle, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	o := &Oracle{}
	return o, json.Unmarshal(b, o)
}

// SizeRank renames the tensor segments of a pickle-free layout to "<bytes>#<rank>", the rank counting
// the storages of that exact size in archive order: a match key that needs no data.pkl, only the
// member sizes and the order torch.save wrote them in.
func (l *Layout) SizeRank() *Layout {
	out := &Layout{Format: l.Format, Size: l.Size, Segs: append([]Seg(nil), l.Segs...)}
	rank := map[int64]int{}
	for i := range out.Segs {
		s := &out.Segs[i]
		if s.Kind != "tensor" {
			continue
		}
		s.Name = fmt.Sprintf("%d#%d", s.Size(), rank[s.Size()])
		rank[s.Size()]++
	}
	return out
}

// PTLayout builds the layout of a torch.save archive from the raw ZIP parser. Framing (local
// headers, data descriptors, the central directory and end records), data.pkl and the small
// non-storage members are "meta"; every data/<id> member is a tensor segment.
//
// Naming of the tensor segments is the point of the study: with o == nil a storage is known only by
// its member name with the archive prefix removed ("data/12": the "member" identity a safe
// pickle-free representation has); with an oracle it is the state_dict key, dtype and shape.
func PTLayout(r io.ReaderAt, size int64, o *Oracle) (*Layout, *torchz3.File, error) {
	f, err := torchz3.Parse(r, size, torchz3.Limits{})
	if err != nil {
		return nil, nil, err
	}
	by := map[string]int{}
	if o != nil {
		for i, t := range o.Tensors {
			if _, ok := by[t.Member]; !ok {
				by[t.Member] = i
			}
		}
	}
	l := &Layout{Format: "pytorch-zip", Size: size}
	// without an oracle the element width is unknown (0): consumers probe it
	for _, s := range f.Segments() {
		seg := Seg{Begin: s.Begin, End: s.End, Kind: "meta", Width: 1}
		switch s.Kind {
		case "storage":
			seg.Kind, seg.Width = "tensor", 0
			short := s.Name
			if i := strings.Index(short, "/data/"); i >= 0 {
				short = short[i+1:]
			}
			seg.Name, seg.Type = short, "storage"
			if i, ok := by[short]; ok {
				t := o.Tensors[i]
				seg.Name, seg.Type, seg.Shape, seg.Width = t.Name, t.DType, t.Shape, max(1, t.Width)
			}
		case "pickle":
			seg.Name = "data.pkl"
		}
		l.Segs = append(l.Segs, seg)
	}
	return l, f, l.Check()
}
