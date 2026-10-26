package prodshadow

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Cost is what a read needed from the production store.
type Cost struct {
	Calls        int64 // Source ReadAt calls (shadow) or 1 (authoritative range)
	Chunks       int   // distinct production chunks
	Records      int   // of those, packed records
	Loose        int   // loose chunk files
	Packs        int   // distinct pack files
	Tiers        string
	StoredBytes  int64 // bytes read from disk: pack record payloads (as stored) + loose files
	LogicalBytes int64 // chunk bytes after decompression
	Fetches      int64 // production chunk reads actually issued (shadow chunk cache misses)
	Elapsed      time.Duration
}

func (c Cost) String() string {
	return fmt.Sprintf("%d chunks (%d rec/%d loose) %d packs %s %d B stored", c.Chunks, c.Records, c.Loose, c.Packs, c.Tiers, c.StoredBytes)
}

// Aggregate resolves where chunks live and totals what reading them costs.
func (l *Lab) Aggregate(chunks [][32]byte) (Cost, error) {
	var c Cost
	packs := map[string]bool{}
	tiers := map[string]int{}
	for _, ch := range chunks {
		w, err := l.Br.Stat(ch)
		if err != nil {
			return c, err
		}
		c.Chunks++
		c.LogicalBytes += w.Size
		if len(w.Packed) > 0 {
			p := w.Packed[0]
			c.Records++
			c.StoredBytes += int64(p.Stored)
			packs[p.Tier+"/"+p.Pack] = true
			tiers[p.Tier]++
			continue
		}
		c.Loose++
		c.StoredBytes += w.LooseLen
		tiers["hot"]++
	}
	c.Packs = len(packs)
	var ts []string
	for t, n := range tiers {
		ts = append(ts, fmt.Sprintf("%s:%d", t, n))
	}
	sort.Strings(ts)
	c.Tiers = strings.Join(ts, ",")
	return c, nil
}

// AuthCost is the cost of an authoritative range read: the manifest chunks that
// overlap the range (the ordinary read path reads exactly those, coalescing
// adjacent packed records).
func (l *Lab) AuthCost(key string, off, n int64) (Cost, error) {
	t0 := time.Now()
	var data []byte
	var err error
	if off == 0 && n < 0 {
		data, err = l.Br.S3Get(key)
		n = int64(len(data))
	} else {
		data, err = l.Br.S3Range(key, off, n)
	}
	if err != nil {
		return Cost{}, err
	}
	el := time.Since(t0)
	ws, err := l.Br.Touch(key, off, n)
	if err != nil {
		return Cost{}, err
	}
	var chunks [][32]byte
	for _, w := range ws {
		b, err := hex.DecodeString(w.SHA)
		if err != nil || len(b) != 32 {
			return Cost{}, fmt.Errorf("bad sha %q", w.SHA)
		}
		var s [32]byte
		copy(s[:], b)
		chunks = append(chunks, s)
	}
	c, err := l.Aggregate(chunks)
	c.Calls, c.Fetches, c.Elapsed = 1, int64(c.Chunks), el
	_ = data
	return c, err
}

// ShadowCost runs fn against the production-backed Source (cold chunk cache) and
// totals the production chunks it needed.
func (l *Lab) ShadowCost(fn func() error) (Cost, error) {
	src := l.Sh.Src
	src.Reset()
	src.AddDescriptors(l.descsOfRoots())
	src.ResetStats()
	t0 := time.Now()
	if err := fn(); err != nil {
		return Cost{}, err
	}
	el := time.Since(t0)
	c, err := l.Aggregate(src.Touched())
	c.Calls, c.Fetches, c.Elapsed = src.ReadAts.Load(), src.ChunkFetches.Load(), el
	return c, err
}

func (l *Lab) descsOfRoots() map[[32]byte]*Descriptor {
	roots, _, _ := l.Meta.Load()
	out := map[[32]byte]*Descriptor{}
	for _, r := range roots {
		for id, d := range r.Descs {
			out[id] = d
		}
	}
	return out
}
