package prodshadow

import (
	"fmt"
	"path/filepath"

	"zeros3-z3/repz3/kinds"
)

// RunLifecycle is the one end-to-end chain: PUT A, PUT B, shadows, compact, tier
// spread, verify, retire A's shadow, prune history, GC, repack, restart, verify.
// Compaction, tiering, pruning, GC, repack and verify are the production commands;
// the shadow-aware ones differ only by the Z3_SHADOW_LIVE mark.
func RunLifecycle(b *Binaries, work string, pages int) ([]Row, error) {
	ch := kinds.SQLiteChain(pages)
	f, err := NewFixture(b, filepath.Join(work, "life"), ch, Options{})
	if err != nil {
		return nil, err
	}
	l := f.L
	defer l.Stop()
	var rows []Row
	liveFile := filepath.Join(work, "life-live.txt")
	wantA, wantB := ch.Stages[0].Data, ch.Stages[1].Data

	// both exact: authoritative GET/range + shadow reconstruction (+ a point read via the shadow)
	checkAll := func(step string, shadowA bool, note string) error {
		auth, ok := authState(l, f.Key, wantB)
		l.Sh.Adopt()
		eb := l.CheckShadow(f.Reps[1], wantB)
		ea := error(nil)
		if shadowA {
			ea = l.CheckShadow(f.Reps[0], wantA)
		}
		c, err := l.Sh.Env().Open(f.Reps[1])
		var pageOK = err == nil
		if pageOK {
			var pg []byte
			pg, err = kinds.ReadSQLitePage(c, 7)
			pageOK = err == nil && string(pg) == string(wantB[7*4096:8*4096])
		}
		sh := "B " + classify(eb)
		if shadowA {
			sh = "A " + classify(ea) + ", " + sh
		}
		rows = append(rows, Row{step, auth, sh + fmt.Sprintf(", page read ok=%v", pageOK), note, ok && eb == nil && ea == nil && pageOK})
		return nil
	}
	stats := func() string {
		s, err := l.Store.Stats()
		if err != nil {
			return err.Error()
		}
		return fmt.Sprintf("loose %d (%.1f MiB), packs %d (%d raw/%d deflate recs, %.1f MiB)", s.LooseChunkCount, float64(s.LooseChunkFileBytes)/(1<<20), s.PackCount, s.PackedRawRecords, s.PackedCompressedRecs, float64(s.PackFileBytes)/(1<<20))
	}

	for i := 0; i < 2; i++ {
		if _, err := f.Put(i, true, Hooks{}); err != nil {
			return nil, err
		}
	}
	if err := checkAll("1 PUT A, PUT B, shadows A+B", true, stats()); err != nil {
		return nil, err
	}

	// compact with the shadow-aware mark
	if _, err := l.LiveFile(liveFile); err != nil {
		return nil, err
	}
	if err := l.Stop(); err != nil {
		return nil, err
	}
	if _, err := l.Store.Compact(2, true, liveFile); err != nil {
		return nil, err
	}
	if err := l.Start(); err != nil {
		return nil, err
	}
	if err := checkAll("2 compact (shadow-aware)", true, stats()); err != nil {
		return nil, err
	}

	// spread packs over hot/warm/cold
	if err := l.Stop(); err != nil {
		return nil, err
	}
	spread, err := l.Store.SpreadPacks()
	if err != nil {
		return nil, err
	}
	if err := l.Start(); err != nil {
		return nil, err
	}
	if err := checkAll("3 tier move (hot/warm/cold)", true, fmt.Sprintf("packs per tier %v", spread)); err != nil {
		return nil, err
	}
	if err := l.Stop(); err != nil {
		return nil, err
	}
	if m, ok, err := l.Store.Verify(true, ""); err != nil || !ok {
		return nil, fmt.Errorf("verify -deep after tiering: %v %v", m, err)
	}

	// retire A's shadow, prune the authoritative history, GC, repack
	before, _ := l.Store.Stats()
	if err := l.Meta.Retire(f.Roots[0]); err != nil {
		return nil, err
	}
	if _, _, err := l.Store.Run("", "versions", "prune", "-bucket", "shadow", "-keep-last", "0", "-apply", "-json"); err != nil {
		return nil, err
	}
	if err := l.Start(); err != nil {
		return nil, err
	}
	live, err := l.LiveFile(liveFile)
	if err != nil {
		return nil, err
	}
	_ = live
	if err := l.Stop(); err != nil {
		return nil, err
	}
	gc, err := l.Store.GC(true, liveFile)
	if err != nil {
		return nil, err
	}
	if err := l.Start(); err != nil {
		return nil, err
	}
	if err := checkAll("4 retire shadow A, prune CDC history, gc", false, fmt.Sprintf("gc deleted %d chunks, %d packs; %s", num(gc, "chunks_deleted"), num(gc, "packs_deleted"), stats())); err != nil {
		return nil, err
	}
	if err := l.Stop(); err != nil {
		return nil, err
	}
	rp, err := l.Store.Repack(true, 2, liveFile)
	if err != nil {
		return nil, err
	}
	if err := l.Start(); err != nil {
		return nil, err
	}
	if err := checkAll("5 repack (shadow-aware)", false, fmt.Sprintf("packs rewritten %d, deleted %d; %s", num(rp, "packs_rewritten"), num(rp, "packs_deleted"), stats())); err != nil {
		return nil, err
	}

	// restart and final verification
	if err := restart(l, false); err != nil {
		return nil, err
	}
	open := l.Br.OpenMS
	if err := checkAll("6 restart (locator reload)", false, fmt.Sprintf("OpenStore %.1f ms; %s", open, stats())); err != nil {
		return nil, err
	}
	if err := l.Stop(); err != nil {
		return nil, err
	}
	m, ok, err := l.Store.Verify(true, "")
	after, _ := l.Store.Stats()
	rows = append(rows, Row{"7 production verify -deep", "ok", "n/a", fmt.Sprintf("ok=%v unreachable=%v; store %.1f -> %.1f MiB", ok, m["unreachable_chunks"], float64(before.ActualStoreFileBytes)/(1<<20), float64(after.ActualStoreFileBytes)/(1<<20)), ok && err == nil})
	return rows, nil
}
