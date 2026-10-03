# M8D — copy-on-write namespace fork external validation

External, real-server, real-AWS-SDK-v2 black-box proof of `zeros3 fork`
(M8D). Every object fixture is written through the AWS SDK (never
zeros3's internal CAS directly); the one exception the milestone spec
itself permits (Phase 10) directly corrupts an already-validly-published
CAS chunk file on disk afterward, since corruption is the condition under
test. `zeros3 fork`/`zeros3 repair`/`zeros3 verify`/`zeros3 gc` themselves
run as real subprocesses, driving one real `zeros3 serve` subprocess per
phase (fork is same-store only, unlike M8A/M8B/M8C's two-server pattern
-- Phase 10 additionally starts one independent third server purely as a
repair peer, since a same-store fork's own corrupted chunk cannot be
healed from itself).

## Build under test

```
zeros3 commit:     a920dbf (branch claude/zeros3-m8d-validation-cow-7dnjql,
                   on top of the exact merged M8C baseline
                   0f9fc3269a9a986a5b3af5aeebbcc4f742a3cc16 -- see
                   results/M8D_PREFLIGHT_RESULTS.md)
Go toolchain:      go1.27.0 linux/amd64
Build command:     CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
Binary SHA-256:    a99cf57d50fb36f6565681840098e59c61a954918ef276cb6188007631762825
```

Reproducible build confirmed: two independent builds from the same
working tree produced byte-identical output.

## Harness

`harness/m8d/fork/main.go`. See its own header comment for the full
black-box rationale. All 10 required phases:

1. **Basic fork** -- a small tree (`a.bin`, `sub/b.bin`, `sub/c.txt`)
   forked from `prod/` to `experiment/` (whole bucket). Exact destination
   keys and byte-for-byte AWS SDK `GetObject` equality.
2. **Zero CAS payload proof** -- CAS state measured two independent ways
   (raw chunk *file count* via `filepath.WalkDir`, and total chunk *file
   bytes* via `os.Stat` summed over every chunk file) before and after a
   multi-object fork with shared and unrelated content. Both measurements
   read `0` delta, matching the CLI's own self-reported `0 B`.
3. **Large logical clone** -- five 24 MiB objects (120 MB logical total)
   forked; `Logical bytes cloned` matches the true total (within the
   CLI's own 2-decimal human-readable rounding), `CAS payload bytes
   added` is `0 B`, and every forked object round-trips exact bytes.
4. **Copy-on-write mutation** -- after a fork of a 20 MB object, a
   localized edit (keep the first 90%, replace the last 10%) is written
   to the fork through the ordinary AWS SDK. New CAS chunk files appear
   (not zero), but well under half the destination object's total chunk
   count -- real, measured high reuse, not an asserted percentage. Source
   remains byte-for-byte unchanged; the fork reflects the edit exactly.
5. **Source divergence** -- source overwritten after fork: fork
   unaffected. Source object deleted after fork: fork remains readable.
6. **Destination conflict** -- one destination key prepopulated with
   unrelated content before a 3-object fork. `zeros3 fork` exits nonzero,
   names the conflicting key in its failure report, leaves the
   pre-existing destination object untouched, and both unrelated objects
   still fork successfully.
7. **>1000 objects** -- 1500 objects forked; complete `ListObjectsV2`
   pagination, no duplicates or missing keys, `0 B` new CAS payload.
8. **Interruption/resume** -- a real `zeros3 fork` **OS process, killed
   mid-run** (`SIGKILL`) partway through three 9 MB objects, then
   correctly resumed by a second real invocation with exact final
   content and zero new CAS payload on the resumed run.
9. **Restart / verify / GC** -- destination restart with AWS SDK
   list/GET-exact, a clean `verify -deep`, and a `zeros3 gc` dry run
   confirming a healthy live set (both namespaces' chunks reachable),
   followed by another restart and independent GET confirmation for both
   namespaces.
10. **M8B composition** -- a chunk shared by `prod/` and its fork
    `experiment/` (same physical file, since fork is same-store) is
    corrupted on disk. `verify -deep` detects it before repair. Because
    the store's own two namespaces cannot repair themselves from each
    other, an independent third `zeros3` peer is seeded with
    byte-identical content and used as the repair source. One
    `zeros3 repair` fetch reports exactly 1 bad chunk repaired, 2
    affected objects (one per namespace) — proving a single physical
    repair restores both namespaces referencing the digest.

## Result

```
===== SUMMARY: 146 passed, 0 failed, 3 informational =====
```

The 3 informational lines report:
- Phase 2's exact CAS-before/after file-count and byte totals (both
  measurements independently confirming zero delta).
- Phase 3's logical-cloned/new-CAS-payload figures for the large clone.
- Phase 4's exact new-vs-total chunk-file count and computed reuse
  percentage for the post-fork copy-on-write mutation.

## Reproduction

```sh
# 1. build zeros3
cd /path/to/zeros3 && git checkout a920dbf
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o /tmp/zeros3-bin .

# 2. run the harness
cd /path/to/zeros3/testing-harnesses
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m8d/fork
```

## Full pre-existing external harness regression

See `results/M8D_RELEASE_REGRESSION_RESULTS.md` for the complete rerun of
every historical harness (M2 through M8C, rclone, Package Killer) against
this exact build, alongside this new M8D harness.
