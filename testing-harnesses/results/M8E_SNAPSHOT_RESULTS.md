# M8E — durable namespace snapshots + zero-payload restore external validation

External, real-server, real-AWS-SDK-v2 black-box proof of `zeros3
snapshot create/list/show/delete/restore` (M8E). Every object fixture is
written through the AWS SDK (never zeros3's internal CAS directly); the
two exceptions the milestone spec itself permits directly corrupt an
already-validly-published file on disk afterward, since corruption is
the condition under test (Phase 7: a snapshot descriptor's trailing
CRC32C; Phase 10: a CAS chunk file, mirroring M8D fork's own Phase 10
exception). `zeros3 snapshot ...`/`zeros3 gc`/`zeros3 verify`/`zeros3
repair` themselves run as real subprocesses, driving one real `zeros3
serve` subprocess per phase (snapshot restore is same-store only, so
unlike M8A/M8B/M8C's two-server pattern, one server hosts both the
source and destination buckets per phase -- Phase 10 additionally starts
one independent third server purely as a repair peer).

## Build under test

```
zeros3 commit:     83415fa (branch claude/zeros3-m8e-snapshots-5a6inu,
                   on top of the exact merged M8D baseline
                   8943f11207c19dee0af16d7ff7fc4b28ee11e4c9)
Go toolchain:      go1.27.0 linux/amd64
Build command:     CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
Binary SHA-256:    9f87452c07231e3614d98e9a9d6ddcd53ad728e7f6ad1827f5ebf114c04707cd
```

Reproducible build confirmed: two independent builds from the same
working tree (`scripts/reproducible_build.sh` in the `zeros3` repo)
produced byte-identical output.

## Harness

`harness/m8e/snapshot/main.go`. See its own header comment for the full
black-box rationale. All 10 required phases:

1. **Snapshot create/list/show** -- a small tree (`a.bin`, `sub/b.bin`,
   `sub/c.txt`) uploaded via the AWS SDK, snapshotted via the real CLI.
   The returned ID is syntactically a valid UUID; `snapshot list`
   includes it; `snapshot show` reports the exact object count and
   source scope.
2. **Point-in-time mutation** -- object `a.bin` = v1, snapshotted,
   overwritten to v2 via the AWS SDK, then restored to a recovery
   prefix. Independent AWS SDK `GetObject` calls confirm the live source
   is v2 and the restored copy is v1.
3. **Source deletion + GC (the mandatory showcase proof)** -- unique
   data uploaded, snapshotted, the live object deleted via the AWS SDK
   (confirmed gone with a 404), then a real `zeros3 gc -apply` run
   against the stopped store. The snapshot remains showable and restores
   successfully afterward, with an exact AWS SDK `GetObject` on the
   restored copy.
4. **Zero-payload restore** -- CAS state measured two independent ways
   (raw chunk *file count* via `filepath.WalkDir`, and total chunk *file
   bytes* via `os.Stat` summed over every chunk file) before and after
   restoring a 5-object, 2 MB snapshot. Both measurements read `0` delta,
   matching the restore CLI's own self-reported `New CAS payload: 0 B`.
5. **>1000 objects** -- 1500 objects uploaded, snapshotted, then one
   mutated and one deleted at the live source *after* the snapshot.
   Restore to a new prefix reproduces the exact 1500-key set (via full
   AWS SDK `ListObjectsV2` pagination) with the pre-mutation,
   pre-deletion point-in-time content for both affected keys.
6. **Snapshot pin/release** -- content referenced only by one snapshot
   stays alive (and is reported as a snapshot GC root) while the
   snapshot exists; after `snapshot delete`, GC no longer counts any
   snapshot root. A separately-live object sharing the same content
   survives both GC passes. (See the harness's own doc comment on why
   "bytes physically vanish" is not independently provable through the
   public S3 API alone in this store's pre-existing permanent-version-
   history model -- the internal suite's
   `TestSnapshotGC_DeleteFinalSnapshotAllowsEventualCollection` covers
   the root-less-content collection case directly.)
7. **Corrupt snapshot metadata (mandatory)** -- a real filesystem
   bit-flip of a published snapshot descriptor's trailing CRC32C. Both
   `snapshot show` and `snapshot restore` fail with a non-zero exit;
   `gc` dry-run reports `live set ok=false`; `gc -apply` refuses to
   sweep (non-zero exit, zero deletions); an unrelated live object is
   completely unaffected throughout.
8. **Restore interruption/resume** -- a real `zeros3 snapshot restore`
   **OS process, killed mid-run** (`SIGKILL`) partway through a
   500-object restore, then correctly resumed by a second real
   invocation: every object present with its exact, non-partial content,
   and zero extra CAS payload measured across the whole interrupted-plus-
   resumed sequence.
9. **Restart** -- a full `zeros3` process restart with a snapshot
   present: `snapshot list`/`show` output is unchanged, `snapshot
   restore` still works with exact AWS SDK content, and a `gc` dry-run
   still recognizes the snapshot root afterward.
10. **M8B composition** -- a snapshot restored into a second namespace
    (structurally sharing CAS with the source, exactly like an M8D
    fork), then one shared chunk corrupted on disk. `zeros3 repair -from`
    an independent peer -- completely unmodified -- repairs it in a
    single fetch, and both the source and the restored namespace read
    back exact content afterward.

## Results

**151 passed, 0 failed, 3 informational.**

## Reproducibility / dependency proof

- Two independent clean builds of `zeros3` at commit `83415fa`
  (`scripts/reproducible_build.sh`): byte-identical SHA-256
  (`9f87452c07231e3614d98e9a9d6ddcd53ad728e7f6ad1827f5ebf114c04707cd`).
- `go.mod`: `module zeros3` / `go 1.27.0`, zero `require` directives --
  unchanged from the M8D baseline.
- No new stdlib import was added by M8E (confirmed via `deps-proof.txt`
  regeneration: `go list -deps .` package list byte-identical to the
  M8D baseline).

## Verdict

All 10 required phases pass. Snapshot create/list/show/delete/restore
behave correctly under real-process interruption, real filesystem
corruption, real garbage collection, a real process restart, and compose
cleanly with M8B peer repair -- see `results/M8E_RELEASE_REGRESSION_RESULTS.md`
for the full historical regression alongside this harness.
