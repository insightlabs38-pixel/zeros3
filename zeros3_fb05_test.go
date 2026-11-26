package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestFB05StageCheckpointCancelsLargePack(t *testing.T) {
	s := b4OpenMaint(t, t.TempDir())
	defer s.Close()
	bodies := make(map[[32]byte][]byte)
	var batch []compactCandidate
	for i := range 32 {
		data := bytes.Repeat([]byte{byte(i)}, 32<<10)
		sum := sha256.Sum256(data)
		bodies[sum] = data
		batch = append(batch, compactCandidate{sum: sum, size: int64(len(data))})
	}
	reads := 0
	read := func(sum [32]byte) ([]byte, error) {
		reads++
		return bodies[sum], nil
	}
	path, _, err := s.stagePackWithin(tierHot, batch, read, nil, newBlockCompressor(true, 0), 1<<20,
		func() error { return context.Canceled }, nil)
	if !errors.Is(err, context.Canceled) || path != "" || reads != 8 {
		t.Fatalf("checkpoint should cancel at 256 KiB: %q, %d reads, %v", path, reads, err)
	}
	files, err := os.ReadDir(tierTmpDir(s.root, tierHot))
	mustOK(t, err)
	if len(files) != 0 {
		t.Fatalf("staging cancellation left %d temporary files", len(files))
	}
}

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

func TestFB05MultipartExpiryScopeAndRetry(t *testing.T) {
	for _, mode := range []string{"v1", "j2"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "j2" {
				t.Setenv("ZEROS3_JOURNAL_FORMAT", "j2")
			} else {
				t.Setenv("ZEROS3_JOURNAL_FORMAT", "")
			}
			dir := t.TempDir()
			s := b4OpenMaint(t, dir)
			defer s.Close()
			mustOK(t, s.CreateBucket("b"))
			mustOK(t, s.CreateBucket("other"))
			create := func(bucket, key string, at time.Time) string {
				id, err := s.createMultipartUpload(bucket, key, "application/octet-stream", nil, at)
				mustOK(t, err)
				return id
			}
			now := time.Now().UTC().Truncate(24 * time.Hour)
			at := now.AddDate(0, 0, -2)
			old := create("b", "temp/old", at.Add(-time.Nanosecond))
			atBoundary := create("b", "temp/boundary", at)
			outsidePrefix := create("b", "keep/old", at.AddDate(0, 0, -1))
			outsideBucket := create("other", "temp/old", at.AddDate(0, 0, -1))
			payload := b4Body(18, 70<<10)
			_, err := s.UploadPart("b", "temp/old", old, 1, payload)
			mustOK(t, err)
			rule := &multipartExpiryRule{Bucket: "b", Prefix: "temp/", Days: 2}
			if len(s.eligibleMultipartExpiry(rule, now.Add(-time.Nanosecond), 1)) != 0 {
				t.Fatal("expiry before UTC midnight")
			}
			opt := b4Opts()
			off, err := s.maintainOnline(context.Background(), opt)
			mustOK(t, err)
			if off.ExpiredUploads != 0 {
				t.Fatalf("expiry with no rule: %+v", off)
			}
			opt.MultipartExpiry = rule
			testJournalFault = func(point string, _ int) (int, error) {
				if point == faultJournalSync {
					return 0, syscall.ENOSPC
				}
				return 0, nil
			}
			defer func() { testJournalFault = nil }()
			failed, err := s.maintainOnline(context.Background(), opt)
			testJournalFault = nil
			if !errors.Is(err, syscall.ENOSPC) || failed.ExpiredUploads != 0 {
				t.Fatalf("failed abort appeared durable: %+v, %v", failed, err)
			}
			parts, err := s.ListParts("b", "temp/old", old)
			mustOK(t, err)
			if len(parts) != 1 {
				t.Fatalf("failed abort removed recent part: %v", parts)
			}
			mustOK(t, s.Close())
			s = b4OpenMaint(t, dir)
			defer s.Close()
			rep, err := s.maintainOnline(context.Background(), opt)
			mustOK(t, err)
			if rep.ExpiredUploads != 1 {
				t.Fatalf("expiry did not match one upload: %+v", rep)
			}
			mustOK(t, s.Close())
			s = b4OpenMaint(t, dir)
			defer s.Close()
			if _, err := s.ListParts("b", "temp/old", old); !errors.Is(err, errNoSuchUpload) {
				t.Fatalf("expired upload revived: %v", err)
			}
			for _, up := range [][3]string{{"b", "temp/boundary", atBoundary}, {"b", "keep/old", outsidePrefix}, {"other", "temp/old", outsideBucket}} {
				_, err := s.ListParts(up[0], up[1], up[2])
				mustOK(t, err)
			}
			t.Logf("%s: default expired=0, failed sync expired=0, retry expired=%d, guards survived reopen=3", mode, rep.ExpiredUploads)
		})
	}
}

func TestFB05RawCompressionAdmission(t *testing.T) {
	for _, mode := range []string{"compressible", "entropy"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			original := smallObjectSegments
			smallObjectSegments = false
			defer func() { smallObjectSegments = original }()
			s, err := OpenStore(dir)
			mustOK(t, err)
			mustOK(t, s.CreateBucket("b"))
			want := map[string][]byte{}
			for i := range 12 {
				key := fmt.Sprintf("part-%02d", i)
				payload := b4Body(i, 8<<10)
				if mode == "compressible" {
					payload = bytes.Repeat([]byte(fmt.Sprintf("source line %02d\n", i)), 512)
				}
				_, err := s.PutObject("b", key, payload, "application/octet-stream", nil)
				mustOK(t, err)
				want[key] = payload
			}
			mustOK(t, s.Close())
			options := defaultCompactOptions()
			options.Compress, options.MinBytes = false, 1
			_, err = compactStore(dir, options)
			mustOK(t, err)
			s = b4OpenMaint(t, dir)
			defer s.Close()
			opt := b4Opts()
			opt.MaxRewriteBytes = 1 << 20
			opt.RecompressRaw = true
			if mode == "compressible" {
				// Each replacement can fit on its own. The complete group
				// cannot fit and must leave the source intact on restart.
				opt.TargetBytes = 24 << 10
				opt.MaxStageBytes = 900
				limited, err := s.maintainOnline(context.Background(), opt)
				mustOK(t, err)
				if limited.Deferred != errMaintStageBudget.Error() || limited.RawProbeBytes == 0 ||
					limited.PacksRetired != 0 || limited.PacksWritten != 0 || limited.PendingPacks != 0 {
					t.Fatalf("staging limit published or retired packs: %+v", limited)
				}
				tmp, err := os.ReadDir(tierTmpDir(dir, tierHot))
				mustOK(t, err)
				if len(tmp) != 0 {
					t.Fatalf("failed staging left %d temporary entries", len(tmp))
				}
				t.Logf("stage_limit=%d deferred=%q published=%d retired=%d", opt.MaxStageBytes,
					limited.Deferred, limited.PacksWritten, limited.PacksRetired)
				mustOK(t, s.Close())
				b4Verify(t, dir, want)
				s = b4OpenMaint(t, dir)
				defer s.Close()
				opt.MaxStageBytes = 4 << 10
			}
			first, err := s.maintainOnline(context.Background(), opt)
			mustOK(t, err)
			if first.RawCandidateBytes == 0 || first.RawProbeBytes == 0 {
				t.Fatalf("no raw sample: %+v", first)
			}
			switch mode {
			case "compressible":
				if first.PacksRetired == 0 || first.PacksWritten < 2 || first.BytesWritten > opt.MaxStageBytes ||
					first.BytesWritten >= first.RawCandidateBytes/2 {
					t.Fatalf("compression did not pay: %+v", first)
				}
			case "entropy":
				if first.RawDeclined == 0 || first.PacksRetired != 0 || first.BytesWritten != 0 {
					t.Fatalf("raw negative control rewrote: %+v", first)
				}
			}
			second, err := s.maintainOnline(context.Background(), opt)
			mustOK(t, err)
			if second.PacksRetired != 0 || second.RawProbeBytes != 0 {
				t.Fatalf("repeated unchanged work: %+v", second)
			}
			t.Logf("%s: source=%d probe=%d admitted=%d written=%d retired=%d declined=%d next_source=%d next_probe=%d",
				mode, first.RawCandidateBytes, first.RawProbeBytes, first.RewriteSourceBytes, first.BytesWritten,
				first.PacksRetired, first.RawDeclined, second.RawCandidateBytes, second.RawProbeBytes)
			mustOK(t, s.Close())
			b4Verify(t, dir, want)
		})
	}
}
