package main

import (
	"bytes"
	"crypto/sha256"
	"io"
	"os"
	"sort"

	"zeros3-z3/diskimg"
	"zeros3-z3/lab"
)

const (
	nativeRef  = 36 // digest + length
	copyRun    = 24 // delta-coded recipe: one copy run = previous offset + length
	guestMap   = 48 // guest offset + length + blob reference per changed run
	packRecord = 256 * 1024
)

type piece struct {
	off, n int64
	class  string
}

// cut splits regions into pieces: grid(class) returns the maximum piece length (0 = keep the region whole).
func cut(regs []diskimg.Region, grid func(class string) int64) []piece {
	var ps []piece
	for _, r := range regs {
		g := grid(r.Class)
		if g <= 0 || g >= r.Len {
			ps = append(ps, piece{r.Off, r.Len, r.Class})
			continue
		}
		for o := int64(0); o < r.Len; o += g {
			ps = append(ps, piece{r.Off + o, min(g, r.Len-o), r.Class})
		}
	}
	return ps
}

// PieceCost is one image's cost against a content-addressed piece store.
type PieceCost struct {
	Pieces        int              `json:"pieces"`
	NewPieces     int              `json:"new_pieces"`
	NewRaw        int64            `json:"new_raw"`
	NewPacked     int64            `json:"new_packed"`
	ByClassRaw    map[string]int64 `json:"new_by_class"`
	Flat          int64            `json:"flat"`  // packed + 36 B per piece (full ordered recipe)
	Delta         int64            `json:"delta"` // packed + recipe delta-coded against the previous image (copy runs + refs)
	CopyRuns      int              `json:"copy_runs"`
	Refs          int              `json:"refs"`
	Changed       int              `json:"changed_pieces"`
	SamePos       int              `json:"same_pos"`
	Relocated     int              `json:"relocated"` // digest known from previous image but at another offset
	RelocatedRaw  int64            `json:"relocated_raw"`
	NonZeroPieces int              `json:"nonzero_pieces"`
}

// PieceStore accounts one lane at one grid. Fine mode coalesces adjacent new pieces into <=256 KiB records before packing
// (an oracle: it assumes the store could address any 4 KiB run).
type PieceStore struct {
	seen    map[[32]byte]struct{}
	prevAt  map[int64][32]byte
	prevAny map[[32]byte]int64 // digest -> first offset in the previous image
	grid    func(string) int64
	coalesc bool
	Cum     PieceCost
}

func NewPieceStore(grid func(string) int64, coalesce bool) *PieceStore {
	return &PieceStore{seen: map[[32]byte]struct{}{}, grid: grid, coalesc: coalesce}
}

func (s *PieceStore) Add(path string, regs []diskimg.Region) (PieceCost, error) {
	f, err := os.Open(path)
	if err != nil {
		return PieceCost{}, err
	}
	defer f.Close()
	ps := cut(regs, s.grid)
	c := PieceCost{ByClassRaw: map[string]int64{}, Pieces: len(ps)}
	curAt := make(map[int64][32]byte, len(ps))
	curAny := map[[32]byte]int64{}
	var run []byte
	flush := func() {
		if len(run) > 0 {
			c.NewPacked += lab.PackedLen(run)
			run = run[:0]
		}
	}
	var buf []byte
	lastNewEnd := int64(-1)
	lastEnd := int64(-1) // end of the previous image range the current copy run continues from
	for _, p := range ps {
		if int64(cap(buf)) < p.n {
			buf = make([]byte, p.n)
		}
		b := buf[:p.n]
		if _, err := f.ReadAt(b, p.off); err != nil && err != io.EOF {
			return c, err
		}
		id := sha256.Sum256(b)
		curAt[p.off] = id
		if _, ok := curAny[id]; !ok {
			curAny[id] = p.off
		}
		same := false
		if s.prevAt != nil {
			if pid, ok := s.prevAt[p.off]; ok && pid == id {
				same = true
				c.SamePos++
			}
		}
		if !same {
			c.Changed++
		}
		// delta-coded recipe: continue a copy run, start one from the digest's place in the previous image, or reference a stored piece
		if s.prevAt != nil && lastEnd >= 0 && s.prevAt[lastEnd] == id {
			lastEnd += p.n
		} else if po, ok := s.prevAny[id]; ok && s.prevAny != nil {
			c.CopyRuns++
			lastEnd = po + p.n
		} else {
			c.Refs++
			lastEnd = -1
		}
		if _, ok := s.seen[id]; !ok {
			s.seen[id] = struct{}{}
			c.NewPieces++
			c.NewRaw += p.n
			c.ByClassRaw[p.class] += p.n
			if s.coalesc {
				if p.off != lastNewEnd || int64(len(run))+p.n > packRecord {
					flush()
				}
				run = append(run, b...)
				lastNewEnd = p.off + p.n
			} else {
				c.NewPacked += lab.PackedLen(b)
			}
		} else if _, ok := s.prevAny[id]; ok && !same {
			c.Relocated++
			c.RelocatedRaw += p.n
		}
		if !allZero(b) {
			c.NonZeroPieces++
		}
	}
	flush()
	c.Flat = c.NewPacked + nativeRef*int64(len(ps))
	c.Delta = c.NewPacked + nativeRef*int64(c.Refs) + copyRun*int64(c.CopyRuns)
	s.prevAt, s.prevAny = curAt, curAny
	return c, nil
}

// GuestFloor is the guest-semantic oracle: new unique 4 KiB guest sectors relative to everything stored, packed in
// <=256 KiB runs, plus a mapping entry per changed run (including runs that became zero).
type GuestFloor struct {
	seen map[[32]byte]struct{}
	Cum  GuestCost
}

type GuestCost struct {
	Changed int   `json:"changed_sectors"`
	Runs    int   `json:"changed_runs"`
	NewRaw  int64 `json:"new_raw"`
	NewPack int64 `json:"new_packed"`
	Map     int64 `json:"map"`
	Total   int64 `json:"total"`
	ToZero  int   `json:"to_zero_sectors"`
}

// State is a logical disk reduced to its non-zero 4 KiB sectors.
type State map[int64][32]byte

func NewGuestFloor() *GuestFloor { return &GuestFloor{seen: map[[32]byte]struct{}{}} }

// Add accounts disk d against prev (nil = empty) and returns the new State.
func (g *GuestFloor) Add(d *diskimg.Disk, prev State) (GuestCost, State, error) {
	cur := State{}
	var c GuestCost
	var changed []int64
	var run []byte
	var runEnd int64 = -1
	flush := func() {
		if len(run) > 0 {
			c.NewPack += lab.PackedLen(run)
			run = run[:0]
		}
	}
	sec := make([]byte, 4096)
	for _, p := range d.Flatten() {
		if p.Off%4096 != 0 || p.Len%4096 != 0 {
			return c, nil, io.ErrShortBuffer
		}
		for o := p.Off; o < p.Off+p.Len; o += 4096 {
			diskimg.FillExtent(sec, p, o)
			s := o / 4096
			id := sha256.Sum256(sec)
			if allZero(sec) {
				continue
			}
			cur[s] = id
			if pid, ok := prev[s]; ok && pid == id {
				continue
			}
			changed = append(changed, s)
			if _, ok := g.seen[id]; !ok {
				g.seen[id] = struct{}{}
				c.NewRaw += 4096
				if o != runEnd || len(run)+4096 > packRecord {
					flush()
				}
				run = append(run, sec...)
				runEnd = o + 4096
			}
		}
	}
	flush()
	for s := range prev {
		if _, ok := cur[s]; !ok {
			changed = append(changed, s)
			c.ToZero++
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i] < changed[j] })
	c.Changed = len(changed)
	for i, s := range changed {
		if i == 0 || s != changed[i-1]+1 {
			c.Runs++
		}
	}
	c.Map = guestMap * int64(c.Runs)
	c.Total = c.NewPack + c.Map
	g.Cum.Changed += c.Changed
	g.Cum.Runs += c.Runs
	g.Cum.NewRaw += c.NewRaw
	g.Cum.NewPack += c.NewPack
	g.Cum.Map += c.Map
	g.Cum.Total += c.Total
	return c, cur, nil
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
