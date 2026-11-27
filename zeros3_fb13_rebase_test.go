package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func fb13DependencyBytes(t *testing.T, s *Store) AuthorityDependencyStorage {
	t.Helper()
	st, err := s.computeStats(statsScope{})
	mustOK(t, err)
	if st.Authority == nil || st.Authority.DependencyOnly == nil {
		return AuthorityDependencyStorage{}
	}
	return *st.Authority.DependencyOnly
}

func fb13CheckVersions(t *testing.T, l *authLab, history map[string][]byte) {
	t.Helper()
	for version, raw := range history {
		_, m, err := l.s.headReplicaVersion("b", "series", version)
		mustOK(t, err)
		got := repReadAll(t, l.s, m, byteRange{0, int64(len(raw)) - 1})
		if !bytes.Equal(raw, got) {
			t.Fatalf("retained version %s differs", version)
		}
	}
	l.verifyOK("retention")
}

func fb13RetentionFixture(t *testing.T, versions [][]byte, snapshot bool) (*authLab, map[string][]byte) {
	t.Helper()
	l := newAuthLab(t)
	byManifest := map[string][]byte{}
	for i, version := range versions[:len(versions)-1] {
		l.put("series", version)
		byManifest[l.man("series").ManifestUUID] = version
		if i == 0 && snapshot {
			entries, err := l.s.captureSnapshotEntries("b", "")
			mustOK(t, err)
			mustOK(t, l.s.publishSnapshot(snapshotDescriptorV1{SnapshotFormatVersion: snapshotFormatVersion,
				SnapshotID: newUUIDv7(), CreatedAt: l.man("series").CreatedAt, SourceBucket: "b", Entries: entries}))
		}
	}
	r := l.migrate(AuthPolicy{Order: "oldest", AllowDelta: true, MaxDepth: 1, MaxBases: 1, MinSaving: 0.01})
	t.Logf("seed_migration admitted=%d added=%d", r.Admitted, r.AddedBytes)
	last := versions[len(versions)-1]
	l.put("series", last)
	keep := 4
	pruned, err := pruneHistoryStoreOpen(l.s, historyPruneOptions{Bucket: "b", Key: "series", KeepLast: &keep})
	mustOK(t, err)
	history := map[string][]byte{"": last}
	for _, row := range historyFor(t, l.s, "b", "series") {
		raw := byManifest[row.manifestUUID]
		if raw == nil {
			t.Fatalf("retained root %s was never written", row.manifestUUID)
		}
		history[row.versionID] = raw
	}
	if len(history) != keep+1 || pruned.VersionsPruned != len(versions)-keep-1 {
		t.Fatalf("keep-last retention: %d live, prune %+v", len(history), pruned)
	}
	l.reclaim()
	fb13CheckVersions(t, l, history)
	return l, history
}

func TestFB13LocalizedRebaseRecovery(t *testing.T) {
	versions := [][]byte{genRandomBytes(1180, 512<<10)}
	for i := uint64(1); i <= 7; i++ {
		versions = append(versions, authVersion(versions[len(versions)-1], 1180+i))
	}
	ctx := context.Background()
	t.Run("snapshot-keeps-base-rooted", func(t *testing.T) {
		l, _ := fb13RetentionFixture(t, versions, true)
		r, err := l.s.authorityRebase(ctx, "b", "series", false, 0, 12)
		mustOK(t, err)
		before := fb13DependencyBytes(t, l.s)
		if r.Targets != 0 || before.Bodies != 0 {
			t.Fatalf("snapshot root counted as expired base: %+v %+v", r, before)
		}
	})
	t.Run("failed-proof-and-retry", func(t *testing.T) {
		l, history := fb13RetentionFixture(t, versions, false)
		before := fb13DependencyBytes(t, l.s)
		total, _, _ := fb13Allocation(t, l.dir)
		plan, err := l.s.authorityRebase(ctx, "b", "series", false, 0, 12)
		mustOK(t, err)
		t.Logf("before=%+v dry=%+v", before, plan)
		afterDry, _, _ := fb13Allocation(t, l.dir)
		if plan.Targets == 0 || plan.Base == plan.Anchor || total != afterDry || before.Bodies != 1 {
			t.Fatalf("no valid write-free plan: %+v / before %+v / allocation %d to %d", plan, before, total, afterDry)
		}
		_, err = l.s.authorityRebase(ctx, "b", "series", true, ^uint64(0)/2, 12)
		if !errors.Is(err, syscall.ENOSPC) {
			t.Fatalf("impossible reserve must fail: %v", err)
		}
		oldFault := authFault
		authFault = func(site string) error {
			if site == "rebase-proof" {
				return syscall.EIO
			}
			return nil
		}
		defer func() { authFault = oldFault }()
		failed, err := l.s.authorityRebase(ctx, "b", "series", true, 0, 12)
		authFault = oldFault
		if !errors.Is(err, syscall.EIO) || failed.Rebased != 0 {
			t.Fatalf("fault published candidate records: %+v %v", failed, err)
		}
		l.close()
		l.open()
		fb13CheckVersions(t, l, history)
		if d := fb13DependencyBytes(t, l.s); d != before {
			t.Fatalf("before/after failed proof: %+v %+v", before, d)
		}
		r, err := l.s.authorityRebase(ctx, "b", "series", true, 0, 12)
		mustOK(t, err)
		if r.Rebased != plan.Targets || r.Base != plan.Base || r.Anchor != plan.Anchor {
			t.Fatalf("retry diverged from dry plan: %+v %+v", plan, r)
		}
		l.reclaim()
		fb13CheckVersions(t, l, history)
		after := fb13DependencyBytes(t, l.s)
		t.Logf("after=%+v applied=%+v", after, r)
		if after.Bodies != 0 || after.ChunkBytes != 0 {
			t.Fatalf("dependency survived: %+v", after)
		}
		pruned, err := l.s.authLoad(sha256.Sum256(versions[0]))
		mustOK(t, err)
		if pruned != nil {
			t.Fatal("expired pin survived exact GC")
		}
	})
	t.Run("partial-publication", func(t *testing.T) {
		l, history := fb13RetentionFixture(t, versions, false)
		oldFault := authFault
		writes := 0
		authFault = func(site string) error {
			if site == "record-write" {
				writes++
				// The independent pin comes first, followed by each leaf.
				if writes == 3 {
					return syscall.EIO
				}
			}
			return nil
		}
		defer func() { authFault = oldFault }()
		partial, err := l.s.authorityRebase(ctx, "b", "series", true, 0, 12)
		authFault = oldFault
		if !errors.Is(err, syscall.EIO) || partial.Rebased != 1 {
			t.Fatalf("expected one verified replacement: %+v %v", partial, err)
		}
		l.reclaim()
		fb13CheckVersions(t, l, history)
		if before := fb13DependencyBytes(t, l.s); before.Bodies != 1 {
			t.Fatalf("old base was released before every leaf moved: %+v", before)
		}
		rest, err := l.s.authorityRebase(ctx, "b", "series", true, 0, 12)
		mustOK(t, err)
		if rest.Rebased != partial.Targets-1 {
			t.Fatalf("retry did not finish the remaining leaves: %+v", rest)
		}
		l.reclaim()
		fb13CheckVersions(t, l, history)
		if after := fb13DependencyBytes(t, l.s); after.Bodies != 0 || after.ChunkBytes != 0 {
			t.Fatalf("retry retained the expired base: %+v", after)
		}
		t.Logf("cut after %d verifiable replacement; resumed %d", partial.Rebased, rest.Rebased)
	})
}

func TestFB13X9RetentionReplay(t *testing.T) {
	root := os.Getenv("ZEROS3_FB13_X9")
	if root == "" {
		t.Skip("set ZEROS3_FB13_X9 to the eight archived tree revisions")
	}
	files, err := filepath.Glob(filepath.Join(root, "*.tar"))
	mustOK(t, err)
	if len(files) != 8 {
		t.Fatalf("expected all eight tree revisions, got %d", len(files))
	}
	var versions [][]byte
	for i, p := range files {
		raw, err := os.ReadFile(p)
		mustOK(t, err)
		versions = append(versions, raw)
		t.Logf("version=%d bytes=%d sha256=%x", i, len(raw), sha256.Sum256(raw))
	}
	l, history := fb13RetentionFixture(t, versions, false)
	before := fb13DependencyBytes(t, l.s)
	beforeAlloc, _, _ := fb13Allocation(t, l.dir)
	start := fb13CPU()
	r, err := l.s.authorityRebase(context.Background(), "b", "series", true, 0, 12)
	cpu := fb13CPU() - start
	mustOK(t, err)
	start = fb13CPU()
	l.reclaim()
	reclaimCPU := fb13CPU() - start
	fb13CheckVersions(t, l, history)
	after := fb13DependencyBytes(t, l.s)
	afterAlloc, _, _ := fb13Allocation(t, l.dir)
	t.Logf("before=%+v after=%+v allocated=%d to %d migration_cpu=%s reclaim_cpu=%s report=%+v",
		before, after, beforeAlloc, afterAlloc, cpu, reclaimCPU, r)
	if r.Rebased == 0 || before.ChunkBytes+before.BodyBytes == 0 ||
		2*(after.ChunkBytes+after.BodyBytes) > before.ChunkBytes+before.BodyBytes || afterAlloc >= beforeAlloc {
		t.Fatalf("real retained dependency gate missed")
	}
}
