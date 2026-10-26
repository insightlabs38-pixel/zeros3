package prodshadow

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"zeros3-z3/repz3"
	"zeros3-z3/repz3/kinds"
)

// Phys is one store's physical state after a stage, as the production stats report it.
type Phys struct {
	Stats                 Stats
	LooseAllocated        int64 // st_blocks of the loose chunk files (filesystem allocation)
	MetaBytes             int64 // shadow roots/descriptors (lab metadata, outside the store)
	PackedAuto, PackedRaw Stats // after production compaction with / without record compression
	PackedAutoAlloc       int64
}

// Total is the physical bytes of everything the representation needs: store chunk
// files plus lab metadata.
func (p Phys) Total(s Stats) int64 { return s.ChunkStoreFileBytes + p.MetaBytes }

// StageRow is the result of one chain stage over the three stores: C (production
// CDC only), Q (CDC + shadow) and S (shadow only).
type StageRow struct {
	Label                            string
	Bytes                            int64
	Shadowed                         bool // the candidate proved exact and was built (else CDC fallback: no shadow)
	Err                              string
	Cost                             repz3.Cost
	Q, S                             StageStats
	C, QP, SP                        Phys
	Encode, ShadowQ, ShadowS, Verify time.Duration
}

// ChainResult is a whole chain.
type ChainResult struct {
	Name string
	Rows []StageRow
}

// RunChain runs every stage of ch: authoritative CDC PUT into C and Q, shadow into
// Q and S, then for each stage snapshots all three stores and compacts the copies
// (with and without record compression; Q and S use the shadow-aware mark).
func RunChain(b *Binaries, ch *kinds.Chain, opt Options, work string, packMiB int) (*ChainResult, error) {
	os.RemoveAll(work)
	labs := map[string]*Lab{}
	for _, n := range []string{"C", "Q", "S"} {
		l, err := NewLab(b, filepath.Join(work, n), opt)
		if err != nil {
			return nil, err
		}
		if err := l.Start(); err != nil {
			return nil, err
		}
		defer l.Stop()
		labs[n] = l
	}
	mem := repz3.NewMemStore()
	cat := kinds.NewCatalog(mem)
	res := &ChainResult{Name: ch.Name}
	const key = "obj"
	for i, s := range ch.Stages {
		row := StageRow{Label: s.Label, Bytes: int64(len(s.Data))}
		for _, n := range []string{"C", "Q"} {
			if err := labs[n].Br.S3Put(key, s.Data); err != nil {
				return nil, err
			}
		}
		t0 := time.Now()
		cand, err := ch.Encode(mem, cat, s.Data)
		row.Encode = time.Since(t0)
		if err == nil {
			err = kinds.Prove(mem, cand, s.Data)
		}
		if err != nil {
			row.Err = err.Error()
		} else {
			_, cost, err := kinds.Commit(mem, cat, cand)
			if err != nil {
				return nil, err
			}
			row.Cost = cost
			t0 = time.Now()
			if row.Q, err = shadowOne(labs["Q"], key, s.Data, cand); err != nil {
				return nil, fmt.Errorf("%s stage %d Q: %w", ch.Name, i, err)
			}
			row.ShadowQ = time.Since(t0)
			t0 = time.Now()
			if row.S, err = shadowOne(labs["S"], key, s.Data, cand); err != nil {
				return nil, fmt.Errorf("%s stage %d S: %w", ch.Name, i, err)
			}
			row.ShadowS = time.Since(t0)
			row.Shadowed = true
		}
		for n, dst := range map[string]*Phys{"C": &row.C, "Q": &row.QP, "S": &row.SP} {
			p, err := snapshot(labs[n], filepath.Join(work, fmt.Sprintf("snap-%s-%d", n, i)), n != "C", packMiB)
			if err != nil {
				return nil, fmt.Errorf("%s stage %d snapshot %s: %w", ch.Name, i, n, err)
			}
			*dst = p
		}
		res.Rows = append(res.Rows, row)
	}
	return res, nil
}

func shadowOne(l *Lab, key string, data []byte, b *kinds.Bundle) (StageStats, error) {
	st, _, err := l.ShadowRevision(key, data, b, Hooks{})
	if err != nil {
		return StageStats{}, err
	}
	return st.Stats, nil
}

// snapshot records the live stats of l, then copies the store and compacts the
// copies: once with adaptive record compression and once raw.
func snapshot(l *Lab, dir string, shadow bool, packMiB int) (Phys, error) {
	var p Phys
	var err error
	if p.Stats, err = l.Store.Stats(); err != nil {
		return p, err
	}
	_, p.LooseAllocated = looseBytes(l.Store.Dir)
	p.MetaBytes, _ = DirBytes(l.Meta.Dir)
	live := ""
	if shadow {
		live = filepath.Join(dir + "-live.txt")
		os.MkdirAll(filepath.Dir(live), 0o755)
		if _, err := l.LiveFile(live); err != nil {
			return p, err
		}
	}
	for _, c := range []struct {
		compress bool
		out      *Stats
	}{{true, &p.PackedAuto}, {false, &p.PackedRaw}} {
		cp := &Store{B: l.B, Dir: dir + fmt.Sprintf("-%v", c.compress)}
		if err := CopyDir(l.Store.Dir, cp.Dir); err != nil {
			return p, err
		}
		if _, err := cp.Compact(packMiB, c.compress, live); err != nil {
			return p, err
		}
		if *c.out, err = cp.Stats(); err != nil {
			return p, err
		}
		if c.compress {
			_, p.PackedAutoAlloc = DirBytes(filepath.Join(cp.Dir, "chunks"))
			a, _ := DirBytes(filepath.Join(cp.Dir, "packs"))
			p.PackedAutoAlloc += a
		}
		os.RemoveAll(cp.Dir)
	}
	return p, nil
}

func looseBytes(storeDir string) (apparent, allocated int64) {
	return DirBytes(filepath.Join(storeDir, "chunks"))
}
