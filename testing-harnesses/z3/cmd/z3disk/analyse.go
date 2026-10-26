package main

import (
	"fmt"
	"os"
	"sort"

	"zeros3-z3/diskimg"
	"zeros3-z3/frontier"
)

// LaneAcct is the cumulative accounting state of one lane across a family.
type LaneAcct struct {
	L      Lane
	CDC    *frontier.Chain
	Native *PieceStore
	Mid    *PieceStore // 64 KiB sub-pieces (VHDX payload blocks split; equals native for QCOW2)
	Fine   *PieceStore // 4 KiB oracle
}

func nativeGrid(l Lane, cs int64) func(string) int64 {
	if l.Fmt == "qcow2" {
		return func(string) int64 { return cs }
	}
	return func(class string) int64 {
		switch class {
		case diskimg.ClassPayload:
			return l.Block
		case diskimg.ClassBitmap:
			return 0
		}
		return 64 * KiB
	}
}

func NewLaneAcct(l Lane) *LaneAcct {
	return &LaneAcct{L: l, CDC: frontier.NewChain(),
		Native: NewPieceStore(nativeGrid(l, 64*KiB), false),
		Mid:    NewPieceStore(func(string) int64 { return 64 * KiB }, false),
		Fine:   NewPieceStore(func(string) int64 { return 4 * KiB }, true)}
}

// Row is everything measured about one exact image artifact.
type Row struct {
	Lane, Rev  string
	Size       int64            `json:"size"`
	Alloc      int64            `json:"alloc"`
	SHA        string           `json:"sha256"`
	Verified   bool             `json:"verified"`
	CDC        frontier.Cost    `json:"cdc"`
	CDCByClass map[string]int64 `json:"cdc_new_by_class"`
	Native     PieceCost        `json:"native"`
	Mid        PieceCost        `json:"mid64k"`
	Fine       PieceCost        `json:"fine4k"`
	Classes    map[string]int64 `json:"classes"`
	Guest      GuestCost        `json:"guest,omitempty"`
	Backing    string           `json:"backing,omitempty"`
	Comps      map[string]int   `json:"states,omitempty"`
}

// scanAny scans an image file as QCOW2 or VHDX.
func scanAny(path string) ([]diskimg.Region, map[string]int, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, "", err
	}
	defer f.Close()
	st, _ := f.Stat()
	if q, err := diskimg.ScanQcow2(f, st.Size()); err == nil {
		c := map[string]int{}
		names := []string{"unalloc", "zero", "data", "compressed"}
		for _, e := range q.Map {
			c[names[e.State]]++
		}
		return q.Regions, c, q.Backing, nil
	}
	v, err := diskimg.ScanVhdx(f, st.Size())
	if err != nil {
		return nil, nil, "", err
	}
	c := map[string]int{}
	names := map[uint8]string{0: "notpresent", 1: "undefined", 2: "zero", 3: "unmapped", 6: "full", 7: "partial"}
	for _, b := range v.Blocks {
		c[names[b.State]]++
	}
	return v.Regions, c, v.Parent["relative_path"], nil
}

// attribute splits [off,off+n) over region classes.
func attribute(regs []diskimg.Region, off, n int64, into map[string]int64) {
	i := sort.Search(len(regs), func(i int) bool { return regs[i].Off+regs[i].Len > off })
	for ; i < len(regs) && n > 0; i++ {
		r := regs[i]
		o := max(off, r.Off)
		e := min(off+n, r.Off+r.Len)
		if e > o {
			into[r.Class] += e - o
		}
	}
}

// Analyse measures one image against the lane's cumulative stores.
func (a *LaneAcct) Analyse(rev, path string) (Row, error) {
	row := Row{Lane: a.L.Name, Rev: rev}
	var err error
	var sha [32]byte
	sha, row.Size, row.Alloc, err = fileSHA(path)
	if err != nil {
		return row, err
	}
	row.SHA = fmt.Sprintf("%x", sha[:8])
	regs, states, backing, err := scanAny(path)
	if err != nil {
		return row, fmt.Errorf("scan %s: %w", path, err)
	}
	row.Backing, row.Comps = backing, states
	row.Classes = map[string]int64{}
	for _, r := range regs {
		row.Classes[r.Class] += r.Len
	}
	row.CDCByClass = map[string]int64{}
	a.CDC.OnNew = func(off, n, packed int64) { attribute(regs, off, n, row.CDCByClass) }
	row.CDC, err = a.CDC.AddFile(path)
	if err != nil {
		return row, err
	}
	if row.Native, err = a.Native.Add(path, regs); err != nil {
		return row, err
	}
	if row.Mid, err = a.Mid.Add(path, regs); err != nil {
		return row, err
	}
	row.Fine, err = a.Fine.Add(path, regs)
	return row, err
}
