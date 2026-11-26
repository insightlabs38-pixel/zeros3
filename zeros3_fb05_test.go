package main

import (
	"context"
	"maps"
	"slices"
	"testing"
)

func TestFB05RewriteBudgetRetiresDeadSeparately(t *testing.T) {
	original := b4FixturePackBytes
	b4FixturePackBytes = 48 << 10
	dir, want := func() (string, map[string][]byte) {
		defer func() { b4FixturePackBytes = original }()
		return b4Fixture(t, 24, 8<<10)
	}()
	s := b4OpenMaint(t, dir)
	defer s.Close()
	byPack := map[int32][]string{}
	for _, key := range slices.Sorted(maps.Keys(want)) {
		chunks := fb03Chunks(t, s, "b", key)
		if len(chunks) != 1 {
			t.Fatalf("budget fixture %q has %d chunks", key, len(chunks))
		}
		loc, ok := s.packLookup(chunks[0])
		if !ok {
			t.Fatalf("budget fixture %q is loose", key)
		}
		byPack[loc.pack] = append(byPack[loc.pack], key)
	}
	var full []int32
	for _, idx := range slices.Sorted(maps.Keys(byPack)) {
		if len(byPack[idx]) > 1 {
			full = append(full, idx)
		}
	}
	if len(full) < 3 {
		t.Fatalf("need three packs with multiple live objects, found %d", len(full))
	}
	for _, key := range append(slices.Clone(byPack[full[0]]), byPack[full[1]][0], byPack[full[2]][0]) {
		mustOK(t, s.DeleteObject("b", key))
		delete(want, key)
	}
	b4Prune(t, s)
	packs := s.packSnap().packs
	dead, a, b := packs[full[0]].size, packs[full[1]].size, packs[full[2]].size
	opt := b4Opts()
	opt.MaxRewriteBytes = min(a, b) - 1
	first, err := s.maintainOnline(context.Background(), opt)
	mustOK(t, err)
	if first.DeadSourceBytes != dead || first.RewriteSourceBytes != 0 || first.PacksDeferred != 2 ||
		first.DeferredSourceBytes != a+b || first.PacksRetired != 1 {
		t.Fatalf("dead-pack pass: %+v", first)
	}
	opt.MaxRewriteBytes = max(a, b)
	second, err := s.maintainOnline(context.Background(), opt)
	mustOK(t, err)
	if second.RewriteSourceBytes < min(a, b) || second.RewriteSourceBytes > opt.MaxRewriteBytes ||
		second.PacksDeferred != 1 || second.PacksRetired != 1 {
		t.Fatalf("rewrite pass: %+v", second)
	}
	t.Logf("source budget %d: dead=%d deferred=%d; budget %d: rewritten source=%d deferred=%d physical output=%d",
		min(a, b)-1, first.DeadSourceBytes, first.DeferredSourceBytes,
		opt.MaxRewriteBytes, second.RewriteSourceBytes, second.DeferredSourceBytes, second.BytesWritten)
	mustOK(t, s.Close())
	b4Verify(t, dir, want)
}
