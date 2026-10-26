package prodshadow

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"zeros3-z3/repz3/kinds"
)

// ManifestChunks is every chunk digest named by any manifest file in the store
// (current and retained history): the authoritative CDC chunk set.
func ManifestChunks(storeDir string) (map[string]bool, error) {
	out := map[string]bool{}
	ents, err := os.ReadDir(filepath.Join(storeDir, "manifests"))
	if err != nil {
		return nil, err
	}
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(storeDir, "manifests", e.Name()))
		if err != nil {
			return nil, err
		}
		var m struct {
			Chunks []struct {
				SHA string `json:"sha256"`
			} `json:"chunks"`
		}
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
		for _, c := range m.Chunks {
			out[c.SHA] = true
		}
	}
	return out, nil
}

// LooseChunks lists the loose chunk file names.
func LooseChunks(storeDir string) map[string]bool {
	out := map[string]bool{}
	filepath.WalkDir(filepath.Join(storeDir, "chunks"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out[d.Name()] = true
		}
		return nil
	})
	return out
}

func num(m map[string]any, k string) int {
	f, _ := m[k].(float64)
	return int(f)
}

// clone copies a stopped lab (store and shadow metadata) to dst.
func (l *Lab) clone(dst string) (*Lab, error) {
	n, err := NewLab(l.B, dst, l.Opt)
	if err != nil {
		return nil, err
	}
	for _, p := range [][2]string{{l.Store.Dir, n.Store.Dir}, {l.Meta.Dir, n.Meta.Dir}} {
		if err := CopyDir(p[0], p[1]); err != nil {
			return nil, err
		}
	}
	return n, nil
}

func hexSet(chunks map[[32]byte]bool) map[string]bool {
	m := map[string]bool{}
	for c := range chunks {
		m[hex.EncodeToString(c[:])] = true
	}
	return m
}

func union(a, b map[string]bool) map[string]bool {
	m := map[string]bool{}
	for k := range a {
		m[k] = true
	}
	for k := range b {
		m[k] = true
	}
	return m
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// RunGC exercises production GC against shadow state: (A) the unmodified binary,
// which knows nothing of shadow roots, then (B) the same code with the
// shadow-aware mark (Z3_SHADOW_LIVE) over the lifecycle cases. No GC code is
// committed or reimplemented: the sweep, pack handling and fail-closed rules are
// the production ones.
func RunGC(b *Binaries, work string, pages int) ([]Row, error) {
	ch := kinds.SQLiteChain(pages)
	f, err := NewFixture(b, filepath.Join(work, "gc"), ch, Options{})
	if err != nil {
		return nil, err
	}
	for i := 0; i < 2; i++ { // A then B: A becomes retained history
		if _, err := f.Put(i, true, Hooks{}); err != nil {
			return nil, err
		}
	}
	orphan := []byte("a stray chunk nobody references: shadow GC debris")
	if _, err := f.L.Br.PutBatch([][]byte{orphan}); err != nil {
		return nil, err
	}
	orphanHex := hex.EncodeToString(func() []byte { s := shaOf(orphan); return s[:] }())
	rootA, rootB := f.Roots[0], f.Roots[1]
	if err := f.L.Stop(); err != nil {
		return nil, err
	}
	var rows []Row
	want := ch.Stages[1].Data
	base := f.L

	// live sets from the persisted roots
	if err := base.Start(); err != nil {
		return nil, err
	}
	roots, _ := base.Sh.Adopt()
	byID := map[[32]byte]*Root{}
	_, ids, _ := base.Meta.Load()
	for i, r := range roots {
		byID[ids[i]] = r
	}
	liveOf := func(rs ...*Root) (map[string]bool, error) {
		l, err := base.Sh.LiveSet(rs)
		if err != nil {
			return nil, err
		}
		return hexSet(l.Chunks), nil
	}
	liveA, err := liveOf(byID[rootA])
	if err != nil {
		return nil, err
	}
	liveB, err := liveOf(byID[rootB])
	if err != nil {
		return nil, err
	}
	if err := base.Stop(); err != nil {
		return nil, err
	}
	cdc, err := ManifestChunks(base.Store.Dir)
	if err != nil {
		return nil, err
	}
	liveAB := union(liveA, liveB)
	shadowOnly := 0
	for h := range liveAB {
		if !cdc[h] {
			shadowOnly++
		}
	}
	shared := 0
	for h := range liveA {
		if liveB[h] && !cdc[h] {
			shared++
		}
	}
	writeLive := func(l *Lab, name string, set map[string]bool) string {
		var hs []string
		for h := range set {
			hs = append(hs, h)
		}
		p := filepath.Join(work, name)
		os.WriteFile(p, []byte(strings.Join(hs, "\n")+"\n"), 0o644)
		return p
	}
	check := func(l *Lab, key string) (string, bool) {
		if err := l.Start(); err != nil {
			return err.Error(), false
		}
		defer l.Stop()
		return authState(l, key, want)
	}

	// A. current production: shadow-only chunks are unreachable (expected, not a bug).
	{
		c, err := base.clone(filepath.Join(work, "gcA"))
		if err != nil {
			return nil, err
		}
		dry, err := c.Store.GC(false, "")
		if err != nil {
			return nil, err
		}
		app, err := c.Store.GC(true, "")
		if err != nil {
			return nil, err
		}
		left := LooseChunks(c.Store.Dir)
		gone := 0
		for h := range liveAB {
			if !cdc[h] && !left[h] {
				gone++
			}
		}
		auth, ok := check(c, f.Key)
		_ = c.Start()
		c.Sh.Adopt()
		serr := c.CheckShadow(f.Reps[1], want)
		c.Stop()
		rows = append(rows, Row{"A prod gc: shadow roots unknown", auth,
			fmt.Sprintf("dry-run: %d/%d chunks unreachable; apply removed all %d/%d shadow-only chunks; shadow now %s", num(dry, "chunks_unreachable"), num(dry, "chunks_scanned"), gone, shadowOnly, classify(serr)),
			fmt.Sprintf("deleted %d; expected integration point", num(app, "chunks_deleted")),
			ok && gone == shadowOnly && serr != nil && num(dry, "chunks_unreachable") == shadowOnly+1})
	}

	// B1. shadow-aware mark, both shadows live: nothing needed is reclaimed, only the stray chunk.
	{
		c, err := base.clone(filepath.Join(work, "gcB1"))
		if err != nil {
			return nil, err
		}
		live := writeLive(c, "live-ab", liveAB)
		dry, err := c.Store.GC(false, live)
		if err != nil {
			return nil, err
		}
		app, err := c.Store.GC(true, live)
		if err != nil {
			return nil, err
		}
		left := LooseChunks(c.Store.Dir)
		exact := sameSet(left, union(cdc, liveAB)) && !left[orphanHex]
		auth, ok := check(c, f.Key)
		_ = c.Start()
		c.Sh.Adopt()
		ea, eb := c.CheckShadow(f.Reps[0], ch.Stages[0].Data), c.CheckShadow(f.Reps[1], want)
		c.Stop()
		rows = append(rows, Row{"B1 live current + historical shadow", auth,
			fmt.Sprintf("dry-run unreachable=%d (the stray); both shadows %s/%s", num(dry, "chunks_unreachable"), classify(ea), classify(eb)),
			fmt.Sprintf("remaining == CDC ∪ shadow-live: %v; deleted %d", exact, num(app, "chunks_deleted")),
			ok && exact && ea == nil && eb == nil && num(dry, "chunks_unreachable") == 1 && num(app, "chunks_deleted") == 1})
	}

	// B2. retire A's shadow root: A-only components reclaimable, components shared with B stay.
	{
		c, err := base.clone(filepath.Join(work, "gcB2"))
		if err != nil {
			return nil, err
		}
		if err := c.Meta.Retire(rootA); err != nil {
			return nil, err
		}
		live := writeLive(c, "live-b", liveB)
		app, err := c.Store.GC(true, live)
		if err != nil {
			return nil, err
		}
		left := LooseChunks(c.Store.Dir)
		exact := sameSet(left, union(cdc, liveB))
		auth, ok := check(c, f.Key)
		_ = c.Start()
		c.Sh.Adopt()
		eb := c.CheckShadow(f.Reps[1], want)
		c.Stop()
		aOnly := 0
		for h := range liveA {
			if !liveB[h] && !cdc[h] {
				aOnly++
			}
		}
		rows = append(rows, Row{"B2 shadow A retired, B live", auth,
			fmt.Sprintf("%d A-only chunks reclaimed, %d shared kept; B %s", aOnly, shared, classify(eb)),
			fmt.Sprintf("remaining == CDC ∪ live(B): %v; deleted %d (A-only + stray)", exact, num(app, "chunks_deleted")),
			ok && exact && eb == nil && num(app, "chunks_deleted") == aOnly+1})
	}

	// B3. authoritative history pruned (production semantics); shadow policy independent.
	{
		c, err := base.clone(filepath.Join(work, "gcB3"))
		if err != nil {
			return nil, err
		}
		if _, _, err := c.Store.Run("", "versions", "prune", "-bucket", "shadow", "-keep-last", "0", "-apply", "-json"); err != nil {
			return nil, err
		}
		live := writeLive(c, "live-ab3", liveAB)
		if _, err := c.Store.GC(true, live); err != nil {
			return nil, err
		}
		cdc2, _ := ManifestChunks(c.Store.Dir)
		left := LooseChunks(c.Store.Dir)
		exact := sameSet(left, union(cdc2, liveAB))
		auth, ok := check(c, f.Key)
		_ = c.Start()
		c.Sh.Adopt()
		ea, eb := c.CheckShadow(f.Reps[0], ch.Stages[0].Data), c.CheckShadow(f.Reps[1], want)
		c.Stop()
		rows = append(rows, Row{"B3 CDC history pruned, shadows kept", auth,
			fmt.Sprintf("CDC chunks %d -> %d; shadow A/B %s/%s (CDC chunks kept by shadow only)", len(cdc), len(cdc2), classify(ea), classify(eb)),
			fmt.Sprintf("remaining == CDC' ∪ shadow-live: %v", exact), ok && exact && ea == nil && eb == nil})
		// then retire A's root too
		if err := c.Meta.Retire(rootA); err != nil {
			return nil, err
		}
		live2 := writeLive(c, "live-b3", liveB)
		if _, err := c.Store.GC(true, live2); err != nil {
			return nil, err
		}
		left = LooseChunks(c.Store.Dir)
		exact2 := sameSet(left, union(cdc2, liveB))
		auth, ok = check(c, f.Key)
		rows = append(rows, Row{"B3b ...then shadow A retired", auth, "A-only shadow chunks gone", fmt.Sprintf("remaining == CDC' ∪ live(B): %v", exact2), ok && exact2})
	}

	// B4. broken shadow: marking fails closed; dropping the root unblocks cleanup.
	{
		c, err := base.clone(filepath.Join(work, "gcB4"))
		if err != nil {
			return nil, err
		}
		var victim [32]byte
		for h := range liveB {
			if !cdc[h] && !liveA[h] {
				bs, _ := hex.DecodeString(h)
				copy(victim[:], bs)
				break
			}
		}
		if err := os.Remove(ChunkPath(c.Store.Dir, victim)); err != nil {
			return nil, err
		}
		_ = c.Start()
		rs, _ := c.Sh.Adopt()
		_, merr := c.Sh.LiveSet(rs)
		c.Stop()
		// even a live file that lists the missing chunk anyway must make production GC refuse
		forced := writeLive(c, "live-forced", union(liveAB, map[string]bool{}))
		before := LooseChunks(c.Store.Dir)
		_, ferr := c.Store.GC(true, forced)
		refused := ferr != nil && sameSet(before, LooseChunks(c.Store.Dir))
		if err := c.Meta.Retire(rootB); err != nil {
			return nil, err
		}
		_ = c.Start()
		rs, _ = c.Sh.Adopt()
		l, lerr := c.Sh.LiveSet(rs)
		c.Stop()
		if lerr != nil {
			return nil, lerr
		}
		live := writeLive(c, "live-a4", hexSet(l.Chunks))
		_, gerr := c.Store.GC(true, live)
		auth, ok := check(c, f.Key)
		rows = append(rows, Row{"B4 broken shadow B, root dropped", auth,
			fmt.Sprintf("mark fails closed: %v; production gc refuses a live set naming the absent chunk (nothing deleted): %v; after dropping the root gc ok=%v", merr != nil, refused, gerr == nil),
			"authoritative object untouched", ok && merr != nil && refused && gerr == nil})
	}
	return rows, nil
}
