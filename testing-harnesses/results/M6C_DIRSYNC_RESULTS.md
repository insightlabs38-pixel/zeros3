# M6C — recursive directory sync (`zeros3 sync LOCAL_DIRECTORY s3://bucket/prefix/`) harness results

External, black-box interoperability evidence for ZeroS3 M6C (recursive
directory sync). Like the M6 (single-file delta sync) harness, this one
drives `zeros3 sync` itself as a real external subprocess (`os/exec`
against the built `zeros3` binary) — never a Go package call into ZeroS3
internals — while the AWS SDK for Go v2 acts as a completely independent
S3 client. See `harness/m6c/dirsync/main.go` and the root `README.md`'s
"Running the M6C directory-sync harness" section for how to reproduce
this.

**Result: 69 passed, 0 failed, 2 informational.**

## What this proves

- **Phase 1 (initial sync):** a deterministic 5-file/2-level-nested
  fixture tree (including a Unicode filename) is synced with one
  `zeros3 sync ./tree s3://dirsync-bucket/dirsync/` invocation; a real AWS
  SDK `ListObjectsV2` returns exactly the 5 expected keys and `GetObject`
  on each round-trips exact bytes.
- **Phase 2 (modified tree):** a small (8KiB) localized edit to the
  largest fixture file plus one brand-new file are synced in a second
  run; the aggregate `Reuse:` line the CLI printed was independently
  parsed and asserted `>= 50%` (measured **96.2%** this run — only the
  edited chunk(s) and the new file's own bytes crossed the wire), the new
  key appears in a fresh `ListObjectsV2`, and an unrelated unchanged file
  (`bravo.bin`) is proven untouched by the edit.
- **Phase 3 (non-destructive local deletion):** `text.txt` is deleted
  locally and directory sync is re-run; the AWS SDK proves its remote
  object still exists with its original, byte-for-byte-unchanged content
  and is still listed — directory sync never deletes.
- **Phase 4 (restart):** the `zeros3` server process is killed and a
  fresh `zeros3 serve` is started on the same store directory; every one
  of the 6 objects from phases 1-3 is re-verified byte-for-byte via the
  AWS SDK, and a real `zeros3 verify -deep` (run directly against the
  store directory) reports `result OK` (17 journal frames, 16 manifests,
  66 chunks checked, 0 missing/corrupt/invalid).
- **Phase 5 (partial failure/conflict):** two unrelated files are changed
  at once — a small edit to `bravo.bin` and a brand-new 15MB
  `zzz-race.bin` (named to sort and process last, giving a real external
  race a window) — and a real AWS SDK `PutObject` races a direct write to
  `zzz-race.bin`'s destination key while the directory sync subprocess is
  still running. This run, the AWS SDK write won: `zzz-race.bin` is
  reported `FAILED` in the CLI summary with the exact safe-mode-conflict
  reason, the process exits non-zero, and `directory sync completed with
  errors` is printed — while `bravo.bin`'s own edit still committed
  correctly and is retrievable, proving partial-failure isolation through
  a real subprocess, not just the in-process test suite. The raced
  object itself holds exactly one writer's content, never a mix. (Being a
  genuine external timing race, the outcome can vary run to run — see the
  caveat in the M6 sync harness results for the same property at the
  single-file level; what's asserted here holds either way.)

## Regression: pre-existing harnesses re-run against the same M6C build

Every harness that existed before this pass was re-run, unmodified,
against the exact same `zeros3` build (from the `claude/zeros3-m6c-
recursive-sync-3m8t4f` branch) this M6C harness used:

| Harness | Result | Previously recorded |
|---|---|---|
| `m2` | 41/41 | 41/41 |
| `m3/copy` | 46/46 | 46/46 |
| `m3/range` | 27/27 | 27/27 |
| `m3/dedup` | 7/7 | 7/7 |
| `m5a/presign` | 47/47 | 47/47 |
| `m5b/multipart` | 43/43 | 43/43 |
| `m5d/pagination` | 43/43 | 43/43 |
| `m6/sync` | 33/33, 2 informational | 33/33, 2 informational |

Every count matches exactly — M6C introduced no regression to any tier
below it, including the original single-file M6A/M6B sync harness this
directory-sync harness builds directly on top of.

## Full transcript

```
2026/08/29 20:03:45 zeros3: listening on 127.0.0.1:39807 (store=/tmp/zeros3-m6c-dirsync-harness-store-3449908384)
PASS: CreateBucket
PASS: write alpha.bin
PASS: write text.txt
PASS: write nested/bravo.bin
PASS: write nested/unicode-✓.txt
PASS: write nested/deeper/charlie.bin
Files discovered:  5
Files synced:      5
Files skipped:     0
Files failed:      0

Logical scanned:     3.29 MiB
Chunks:              64
Chunks reused:       0
Uploaded payload:    3.29 MiB (64 unique chunks)
Transfer avoided:    0 B
Reuse:               0.0%
PASS: zeros3 sync (initial directory sync, brand-new tree)
PASS: initial directory sync exits zero
PASS: initial directory sync summary reports 5/5 files
PASS: AWS SDK ListObjectsV2 after initial sync
PASS: ListObjectsV2 returns exactly the expected 5 keys
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject dirsync/alpha.bin
PASS: exact byte equality for dirsync/alpha.bin
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject dirsync/nested/bravo.bin
PASS: exact byte equality for dirsync/nested/bravo.bin
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject dirsync/nested/deeper/charlie.bin
PASS: exact byte equality for dirsync/nested/deeper/charlie.bin
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject dirsync/nested/unicode-✓.txt
PASS: exact byte equality for dirsync/nested/unicode-✓.txt
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject dirsync/text.txt
PASS: exact byte equality for dirsync/text.txt
PASS: rewrite alpha.bin with a small localized edit
PASS: write new file nested/deeper/newfile.bin
Files discovered:  6
Files synced:      6
Files skipped:     0
Files failed:      0

Logical scanned:     3.32 MiB
Chunks:              65
Chunks reused:       63
Uploaded payload:    129.51 KiB (2 unique chunks)
Transfer avoided:    3.19 MiB
Reuse:               96.2%
PASS: zeros3 sync (modified tree: 1 edited file + 1 new file)
PASS: modified-tree directory sync exits zero
PASS: modified-tree summary reports 6/6 files
INFO: phase 2 aggregate stats: Uploaded payload:    129.51 KiB (2 unique chunks) | Reuse:               96.2%
PASS: aggregate reuse is meaningful (>=50%) when only a small edit touched one file among an otherwise-unchanged tree
PASS: AWS SDK ListObjectsV2 after modified-tree sync
PASS: ListObjectsV2 returns exactly the expected 6 keys
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject alpha.bin after edit
PASS: alpha.bin holds the modified content
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject newfile.bin
PASS: newfile.bin holds the expected content
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject bravo.bin (should be unaffected by alpha.bin's edit)
PASS: bravo.bin is unaffected by an unrelated file's edit
PASS: delete local text.txt
Files discovered:  5
Files synced:      5
Files skipped:     0
Files failed:      0

Logical scanned:     3.32 MiB
Chunks:              64
Chunks reused:       64
Uploaded payload:    0 B (0 unique chunks)
Transfer avoided:    3.32 MiB
Reuse:               100.0%
PASS: zeros3 sync (after local deletion of text.txt)
PASS: post-deletion directory sync exits zero
PASS: post-deletion summary reports 5 discovered (text.txt no longer local)
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject text.txt after local deletion
PASS: text.txt's remote object survives byte-for-byte after its local file was deleted (non-destructive semantics)
PASS: AWS SDK ListObjectsV2 after local deletion
PASS: text.txt's key is still listed even though it no longer exists locally
2026/08/29 20:03:45 zeros3: listening on 127.0.0.1:39807 (store=/tmp/zeros3-m6c-dirsync-harness-store-3449908384)
PASS: restart zeros3 on the same store directory
PASS: AWS SDK ListObjectsV2 after restart
PASS: listing is unchanged after restart
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject dirsync/alpha.bin after restart
PASS: exact byte equality for dirsync/alpha.bin after restart
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject dirsync/nested/bravo.bin after restart
PASS: exact byte equality for dirsync/nested/bravo.bin after restart
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject dirsync/nested/deeper/charlie.bin after restart
PASS: exact byte equality for dirsync/nested/deeper/charlie.bin after restart
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject dirsync/nested/deeper/newfile.bin after restart
PASS: exact byte equality for dirsync/nested/deeper/newfile.bin after restart
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject dirsync/nested/unicode-✓.txt after restart
PASS: exact byte equality for dirsync/nested/unicode-✓.txt after restart
SDK 2026/08/29 20:03:45 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject dirsync/text.txt after restart
PASS: exact byte equality for dirsync/text.txt after restart
ZeroS3 verify (deep)
journal          17 frames checked | ok=true
roots            6 current | 10 historical | 0 multipart
manifests        16 checked
chunks           66 checked
integrity        0 missing | 0 corrupt | 0 invalid
reclaimable      0 unreachable manifests | 0 unreachable chunks | 0 bytes
result           OK

PASS: zeros3 verify -deep after directory sync + restart
2026/08/29 20:03:46 zeros3: listening on 127.0.0.1:39807 (store=/tmp/zeros3-m6c-dirsync-harness-store-3449908384)
PASS: restart zeros3 after verify
PASS: rewrite nested/bravo.bin with an unrelated small edit
PASS: write large racing fixture zzz-race.bin (sorts last, gives the race a window)
PASS: AWS SDK racing PutObject against zzz-race.bin
Files discovered:  6
Files synced:      5
Files skipped:     0
Files failed:      1

Logical scanned:     3.32 MiB
Chunks:              64
Chunks reused:       63
Uploaded payload:    21.53 KiB (1 unique chunks)
Transfer avoided:    3.30 MiB
Reuse:               99.4%

FAILED:
  /tmp/zeros3-m6c-dirsync-harness-scratch-1633773976/tree/zzz-race.bin -> s3://dirsync-bucket/dirsync/zzz-race.bin
  reason: sync: sync: destination changed since sync began (safe-mode conflict); rerun sync to retry against the new state

directory sync completed with errors
SDK 2026/08/29 20:03:47 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject zzz-race.bin after the race
PASS: raced object holds exactly one writer's content, never a corrupted mix
PASS: when the AWS SDK PutObject won the race, the directory sync process reported a non-zero exit
PASS: the summary reports the raced file as failed, not silently as full success
INFO: phase 5 race outcome this run: AWS SDK PutObject won (zzz-race.bin correctly reported as a failed/conflicted file)
SDK 2026/08/29 20:03:47 WARN Response has no supported checksum. Not validating response payload.
PASS: AWS SDK GetObject bravo.bin after phase 5
PASS: bravo.bin's own edit committed correctly regardless of the unrelated zzz-race.bin race outcome

===== SUMMARY: 69 passed, 0 failed, 2 informational =====
```

## Caveats

- Phase 5's race outcome is, by nature, timing-dependent (see the M6
  single-file harness's own caveat for the analogous case). The
  invariants this harness actually asserts — exactly one writer's content
  on the raced key, unrelated files unaffected, and a non-zero process
  exit specifically when the interloper wins — hold regardless of which
  side wins on a given run.
- This harness pins the same `zeros3-testing` dependency set (AWS SDK for
  Go v2) as every other harness in this repository; see the root
  `README.md`/`go.mod` for exact versions. No new external dependency was
  introduced for M6C.
