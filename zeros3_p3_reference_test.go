package main

// referenceReachability is the serial, map-based scan Z4-P0..P2 shipped, kept
// verbatim as the oracle the parallel scan is compared against.

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

type verifiedManifestCacheEntry struct {
	man            manifestV1
	sha            [32]byte
	structurallyOK bool
}

func (s *Store) referenceReachability(deep bool, obs rootObserver) (reachabilityResult, error) {
	res := reachabilityResult{
		ReferencedManifests: map[string]bool{},
		ManifestSHA:         map[string][32]byte{},
		ReferencedChunks:    map[string]bool{},
		ChunkLength:         map[string]int64{},
	}

	jf, err := os.Open(filepath.Join(s.root, "journal", "visibility.log"))
	if err != nil {
		return res, fmt.Errorf("reachability: opening journal: %w", err)
	}
	_, _, nFrames, _, jerr := replayJournalFn(jf, false, nil) // validation only: nothing is retained
	jf.Close()
	res.JournalFramesChecked = nFrames
	if jerr != nil {
		res.addIssue("corrupt", "journal/visibility.log", jerr.Error())
	} else {
		res.JournalOK = true
	}

	manifestCache := map[string]verifiedManifestCacheEntry{}

	// checkRoot validates one root's claimed (manifestUUID, manifestSHA256)
	// pair, reading/parsing/structurally-checking the manifest at most once
	// per UUID via manifestCache, but independently re-checking THIS root's
	// own journal-recorded hash claim against it every time -- two roots
	// (current and historical alike) can legally share one manifest UUID,
	// and each must independently prove journal-recorded SHA256 == actual
	// manifest-file SHA256, exactly like Verify always did for current-only
	// roots. On success, marks the manifest referenced (protected from GC)
	// and returns its parsed content; on failure, records the issue and
	// returns ok=false without marking it referenced -- a manifest that
	// cannot be trusted enough to extract a chunk list from cannot protect
	// any chunk either, which is safe only because that failure also flips
	// OK() to false, refusing all destructive action store-wide (section
	// 16b), not just around this one broken root.
	checkRoot := func(subject, manifestUUID string, manifestSHA [32]byte) (manifestV1, bool) {
		cached, ok := manifestCache[manifestUUID]
		if !ok {
			data, rerr := s.loadManifestBytes(manifestUUID, manifestSHA)
			if rerr != nil {
				if os.IsNotExist(rerr) {
					res.addIssue("missing", subject, "manifest file "+manifestUUID+".json does not exist")
				} else if errors.Is(rerr, errChunkCorrupt) {
					res.addIssue("corrupt", subject, "manifest record is corrupt: "+rerr.Error())
				} else {
					res.addIssue("invalid", subject, rerr.Error())
				}
				manifestCache[manifestUUID] = verifiedManifestCacheEntry{}
				return manifestV1{}, false
			}
			cached.sha = sha256.Sum256(data)
			cached.structurallyOK = true
			if uerr := json.Unmarshal(data, &cached.man); uerr != nil {
				res.addIssue("invalid", subject, "manifest json does not parse: "+uerr.Error())
				cached.structurallyOK = false
			} else if cached.man.ManifestFormatVersion != manifestFormatVersion || cached.man.CDCFormatVersion != cdcFormatVersion || cached.man.HashAlgorithm != "sha256" {
				res.addIssue("invalid", subject, "manifest declares an unsupported format/CDC/hash version")
				cached.structurallyOK = false
			} else {
				var sum int64
				validRefs := true
				for _, c := range cached.man.Chunks {
					if _, herr := decodeHexSHA256(c.SHA256); herr != nil {
						res.addIssue("invalid", subject, "manifest chunk reference has malformed sha256: "+c.SHA256)
						validRefs = false
						continue
					}
					if c.Length < 0 {
						res.addIssue("invalid", subject, "manifest chunk reference has a negative length")
						validRefs = false
						continue
					}
					sum += c.Length
				}
				if !validRefs {
					cached.structurallyOK = false
				} else if sum != cached.man.TotalLength {
					res.addIssue("invalid", subject, fmt.Sprintf("manifest chunk lengths sum to %d, want total_length %d", sum, cached.man.TotalLength))
					cached.structurallyOK = false
				}
			}
			manifestCache[manifestUUID] = cached
			res.ManifestsChecked++
		}
		if cached.sha != manifestSHA {
			res.addIssue("corrupt", subject, "manifest file sha256 does not match this root's recorded reference")
			return manifestV1{}, false
		}
		if !cached.structurallyOK {
			return manifestV1{}, false
		}
		res.ReferencedManifests[manifestUUID] = true
		res.ManifestSHA[manifestUUID] = manifestSHA
		return cached.man, true
	}

	// Build the final length map directly; retaining a second copy costs one
	// hash-table entry per representation component during maintenance.
	wantChunks := res.ChunkLength
	want := func(scope rootScope, bucket, key string, chunks []chunkRef) {
		for _, c := range chunks {
			wantChunks[c.SHA256] = c.Length
		}
		if obs != nil {
			obs(scope, bucket, key, chunks)
		}
	}
	// The optional representation of a live manifest is part of the same root:
	// its chunks are wanted (and observed) beside the manifest's own.
	rctx := s.newRepReachCtx(&res)
	wantRep := func(scope rootScope, bucket, key, uuid string, man manifestV1) {
		rctx.observe(uuid, man, func(chunks []chunkRef) { want(scope, bucket, key, chunks) })
	}

	// Observation order is locality priority (compaction ranks chunks by first
	// reference): current objects, active multipart, snapshots, then retained
	// history. Within a category the order is deterministic when an observer
	// is attached. GC and verify only take the union, so order is otherwise moot.
	// Root category 1: current visible objects.
	curObjs := s.snapshotNamespace()
	if obs != nil {
		sort.Slice(curObjs, func(i, j int) bool {
			return cmp.Or(cmp.Compare(curObjs[i].bucket, curObjs[j].bucket), cmp.Compare(curObjs[i].key, curObjs[j].key)) < 0
		})
	}
	for _, o := range curObjs {
		res.CurrentRootCount++
		if man, ok := checkRoot(o.bucket+"/"+o.key, o.entry.manifestUUID, o.entry.manifestSHA256); ok {
			want(scopeCurrent, o.bucket, o.key, man.Chunks)
			wantRep(scopeCurrent, o.bucket, o.key, o.entry.manifestUUID, man)
		}
	}
	// Root category 3: active multipart uploads. These do not go through
	// the manifest mechanism before completion -- each already-published
	// part's own chunk list is a live root directly.
	uploads := s.snapshotUploads()
	if obs != nil {
		sort.Slice(uploads, func(i, j int) bool {
			a, b := uploads[i], uploads[j]
			return cmp.Or(cmp.Compare(a.bucket, b.bucket), cmp.Compare(a.key, b.key), cmp.Compare(a.uploadID, b.uploadID)) < 0
		})
	}
	for _, up := range uploads {
		for _, pn := range slices.Sorted(maps.Keys(up.parts)) {
			p := up.parts[pn]
			res.MultipartRootCount++
			subject := fmt.Sprintf("multipart:%s/part%d", up.uploadID, p.partNumber)
			valid := p.chunks[:0:0]
			for i, c := range p.chunks {
				if _, herr := decodeHexSHA256(c.SHA256); herr != nil {
					res.addIssue("invalid", subject, fmt.Sprintf("part chunk %d has a malformed sha256: %s", i, c.SHA256))
					continue
				}
				valid = append(valid, c)
			}
			want(scopeMultipart, up.bucket, up.key, valid)
		}
	}
	// Root category 4 (section 15h): durable namespace snapshots.
	// scanSnapshots reads store/snapshots/ fresh off disk (never cached --
	// see Store.snapshotMu's doc comment) and reports every parse/
	// integrity issue it finds via the exact same issueTracker every
	// other root category already feeds; a corrupt/malformed snapshot
	// descriptor therefore flips res.OK() to false exactly like a corrupt
	// manifest would, which is what makes destructive GC's existing
	// fail-closed gate (section 13b's errGCUnsafe) refuse to sweep
	// without any new gating logic -- see this section's own "Root
	// category 4" doc comment in section 15h for the full rationale. A
	// snapshot's entries reference manifests by (UUID, SHA256) exactly
	// like a current or historical root does, so they run through the
	// same checkRoot closure, unmodified.
	validSnaps, snapIssues := s.scanSnapshots()
	for _, iss := range snapIssues {
		res.addIssue(iss.Kind, iss.Subject, iss.Detail)
	}
	for _, snap := range validSnaps {
		for _, se := range snap.Entries {
			res.SnapshotRootCount++
			subject := "snapshot:" + snap.SnapshotID + ":" + se.Key
			sum, herr := decodeHexSHA256(se.ManifestSHA256)
			if herr != nil {
				res.addIssue("invalid", subject, "snapshot entry has a malformed manifest sha256: "+herr.Error())
				continue
			}
			if man, ok := checkRoot(subject, se.ManifestUUID, sum); ok {
				want(scopeSnapshot, snap.SourceBucket, se.Key, man.Chunks)
				wantRep(scopeSnapshot, snap.SourceBucket, se.Key, se.ManifestUUID, man)
			}
		}
	}

	// Root category 2: retained historical versions.
	histObjs := s.snapshotHistory()
	if obs != nil { // newest retained version first
		sort.Slice(histObjs, func(i, j int) bool {
			a, b := histObjs[i], histObjs[j]
			return cmp.Or(cmp.Compare(a.bucket, b.bucket), cmp.Compare(a.key, b.key), cmp.Compare(b.entry.seq, a.entry.seq)) < 0
		})
	}
	for _, o := range histObjs {
		res.HistoricalRootCount++
		subject := fmt.Sprintf("history:%s/%s@%s", o.bucket, o.key, o.entry.versionID)
		if man, ok := checkRoot(subject, o.entry.manifestUUID, o.entry.manifestSHA256); ok {
			want(scopeHistory, o.bucket, o.key, man.Chunks)
			wantRep(scopeHistory, o.bucket, o.key, o.entry.manifestUUID, man)
		}
	}
	rctx.finish()
	// One unified chunk existence/integrity pass over every referenced
	// digest, regardless of which root category claimed it. Healthy scans share
	// the two read-only sets; only a faulty scan needs a separate valid subset.
	var invalidChunks []string
	for sha, length := range wantChunks {
		res.ReferencedChunks[sha] = true
		invalidChunks = append(invalidChunks, sha)
		sum, herr := decodeHexSHA256(sha) // already validated above when added to wantChunks
		if herr != nil {
			continue
		}
		res.ChunksChecked++
		if loc, ok := s.packLookup(sum); !ok || int64(loc.logical) != length {
			info, serr := os.Stat(s.chunkPath(sum))
			if serr != nil {
				if os.IsNotExist(serr) {
					res.addIssue("missing", "chunk "+sha, "chunk file does not exist")
				} else {
					res.addIssue("invalid", "chunk "+sha, serr.Error())
				}
				continue
			}
			if info.Size() != length {
				res.addIssue("corrupt", "chunk "+sha, fmt.Sprintf("file length %d does not match reference length %d", info.Size(), length))
				continue
			}
		}
		if deep {
			if _, rerr := s.casRead(sum); rerr != nil {
				switch {
				case os.IsNotExist(rerr):
					res.addIssue("missing", "chunk "+sha, rerr.Error())
				case errors.Is(rerr, errChunkCorrupt):
					res.addIssue("corrupt", "chunk "+sha, "content hash does not match its content-addressed name")
				default:
					res.addIssue("corrupt", "chunk "+sha, rerr.Error())
				}
				continue
			}
		}
		invalidChunks = invalidChunks[:len(invalidChunks)-1]
	}
	res.ValidChunks = res.ReferencedChunks
	if len(invalidChunks) != 0 {
		res.ValidChunks = maps.Clone(res.ReferencedChunks)
		for _, sha := range invalidChunks {
			delete(res.ValidChunks, sha)
		}
	}

	return res, nil
}
