# Z3-I0 — Production representation foundation + first structural format wave

**Decision: `I0_COMPLETE`.** Base `0aa62b2e`. One file (`zeros3.go`), stdlib only, `go.mod/go.sum` unchanged. Representations are an optional, immutable
sidecar: CDC stays authoritative, the journal stays the only visibility boundary, mode defaults to `off`. Creation never fails a PUT.
**Honest debts:** representation whole-reads are 1.5–4.2× slower than CDC, storage is additive, and reachability memory is O(components) (see Reads, Storage).

## Formats and store level
| artifact | version | where | notes |
|---|---|---|---|
| Representation object `ZS3R` | 1 | CAS object (content-addressed) | STORED/DERIVED members, prefix/zero framing, opaque recipe, owning index sections; depth 3 default, 4 hard limit |
| Representation root `ZS3T` | 1 | `representations/<manifest-uuid>.root`, CRC32C | names the object + flat descriptors for components >256 KiB (CDC-chunk lists) |
| Store format | 6 | `FORMAT.json` | raised durably just before the first root is published; a store below 5 first gets its (empty) warm/cold tier roots |
Manifest, `ZS3BNDL1`/`ZS3DLTA1` bundles and the native protocol are unchanged. Kind numbers: 1 Concat/tar, 2 CAR, 3 SQLite, 4 Git (reserved), 5 Merkle, 6 Gzip (reserved), 7 Zstd (reserved).

## Lifecycle (`-representations off|shadow|prefer`; `doctor -retire-bad-representations`)
| operation | behaviour | evidence |
|---|---|---|
| PUT / CopyObject / multipart | after the CDC commit is durable, best-effort build; proof (decode+Source → SHA/size) before publish | `TestRepOptionalFailureNeverFailsPut`, `TestRepHostileObjectsNeverFailPut`, `TestRepCopyObjectAndMultipart` |
| GC / compact / repack / tier move | one reachability scan covers roots; component chunks stay live while the root is live | `TestRepKindLifecycleMatrix`, harness kinds phase (shadow+prefer, 8 objects, 6129 components) |
| versions prune → gc | root of a pruned manifest is an orphan: reported by dry-run, removed by `gc -apply` | harness kinds |
| `verify -deep` / `doctor` / `stats` | re-decodes every root against its object; new fields only (pre-existing stats fields unchanged) | harness compat (4 levels) |
| corrupt/unreadable live root | reachability not OK → GC/compact/repack fail closed (`errGCUnsafe`); reads fall back to CDC; retire flag is the supported recovery | `TestRepLiveRootFaultsFailClosed`, `TestRepLateCreationAndRetirementLeaveNamespaceUntouched` |
| off mode | existing roots still protected, served only in prefer, never downgraded | harness compat, `TestRepOffModeMaintenanceKeepsRoots` |
Snapshots retain representations through history prune (`TestRepSnapshotRetainsRepresentation`). Sync/replicate/bundle import do **not** create roots.

## Kinds (production decoders; all prove-before-publish)
| kind | decomposition | tested with |
|---|---|---|
| Concat (tar) | members + nested children (tar→SQLite/CAR/Merkle, depth ≤2 built, 4 read) | `TestRepKindMatrixRoundTrip`, `TestRepSharedChildAcrossFormatsAndRootLifecycle` |
| SQLite | page-tree nodes (fanout 64, head split for 0x00 pages), sparse updates reuse unchanged pages | 24 MiB × 5 revs |
| CAR v1/v2 | sorted digest table cut by CDC + head/tail recipe; >256 KiB blocks become descriptors | `car-large-blocks` |
| Merkle | native hash tags (sha256/sha3-256), owning index sections, chain reuse ≤8 sections + compaction | `TestRepMerkleRevisionChainSections` |

## Compatibility (harness `z3_i0 compat|bundles`, black-box, base binary built from `0aa62b2e`)
| check | result |
|---|---|
| pre-existing store levels 1,2,3,5 opened by I0 in off/shadow/prefer | GET + ranges exact, level not moved, stats fields identical, `verify -deep` OK |
| S3 transcript + chunk set, base vs I0 off/shadow/prefer | identical |
| first root raises level to 6 | pre-I0 `verify/stats/gc/compact/serve` all refuse with `unsupported store format version 6`; on-disk digest unchanged by the refusal |
| bundle export of a prefer store vs base store | same counts; pre-I0 `bundle inspect -verify` passes; I0 and pre-I0 import: no `representations/`, level <6, restore exact |
| native replicate prefer-I0 → pre-I0 | exact; destination stays <6; pre-I0 `verify -deep` OK |
54 compat checks + 24 bundle/replicate checks, all pass.

## Crash matrix (`TestRepCrashMatrix`, test-hook panics + harness SIGKILL rounds)
| point | after restart |
|---|---|
| C1 before components / C2 some components | object visible via CDC only; stray components are plain garbage |
| C3 rep object published, root absent; C3b root temp written | no root, `.tmp` ignored and never liveness; GC reclaims |
| C3c renamed, not synced / C4 root published, no namespace commit | root is an orphan or valid, never half-visible; verify clean |
| C5 committed | object + root consistent. C6 build fails | PUT still succeeds. `TestRepCrashAfterFormatRaiseBeforeFirstRoot`: level 6 with no root is valid |

## Reads (compacted store, median of 5; `ZEROS3_Z3_I0_MEASURE=1 go test -run TestRepMeasure`)
| workload | CDC whole ms | rep whole ms | ratio | 4 KiB CDC/rep ms | pack opens whole CDC/rep |
|---|---|---|---|---|---|
| SQLite 24 MiB | 20.1 | 63.3 | 3.2× | 0.06/0.11 | 237/19 |
| CAR 6 MiB | 5.7 | 23.8 | 4.2× | 0.06/0.54 | 3/9 |
| tar→SQLite 8 MiB | 6.3 | 24.9 | 4.0× | falls back to CDC | 12/4 |
Benchmarks: SQLite 381 MB/s rep vs 1277 MB/s CDC; Merkle 38 MB/s (≈3.6× CDC, pooled inflater + prefetch). The harness (4 KiB fixtures, loose) shows 1.5× for SQLite whole reads.
Ranges stay sub-millisecond. Representation reads use request-local run coalescing and a per-request locator snapshot; every failure before the first byte falls back to CDC.
1 GiB large phase (SQLite-shaped, 266,240 components): PUT 20.8 s off / 34.2 s prefer, GET 1.3 s / 3.5 s, server RSS 25 → 155 MiB, `verify -deep` 11.5 s at 235 MiB peak.

## Storage and root discovery
| workload (5 revs) | CDC only | CDC + rep | rep marginal | per-revision marginal |
|---|---|---|---|---|
| SQLite 24 MiB sparse | 40.5 MB | 66.3 MB | +25.8 MB | 24.8 MB, then ≈252 KB/rev |
| CAR 6 MiB | 32.0 MB | 39.0 MB | +7.0 MB | 6.1 MB, then ≈230 KB/rev |
| Merkle 20k keys | 6.5 MB | 8.5 MB | +2.0 MB | 1.7 MB, then ≈65 KB/rev |
Representation storage is **additive** (components are separate chunks from CDC chunks); I0 does not claim a space saving, only that later revisions cost ≈1% of the first. Root files are 0.4–2.3 KB.
Root discovery: open 11 ms / 37 ms at 100 / 1000 roots vs 5/11 ms off; `verify` 27 / 279 ms; derived-state rebuild < 3 ms per object, ≤ 3.3 MiB heap per workload.
Memory is ≈0.5–0.8 KiB per 4 KiB-page component (hex-keyed reachability maps) — the 266k-component object adds 129 MiB to `verify -deep`.

## Validation
`go test -count=1 ./...` (328 s) pass; 161 `TestRep|FuzzRep` PASS lines; `-race` on the whole `TestRep|FuzzRep` suite clean (a late run caught a real race: `ensureRepFormat`/`initTierRoots` read the format outside `formatMu`;
fixed in production with a publisher mutex, test unchanged; 14 repeats clean). Fuzz smokes 25 s each (`-fuzzminimizetime 0`): `FuzzRepUnmarshal` 582k execs, `FuzzRepRootUnmarshal` 461k, `FuzzRepDecoders` 412k; no findings.
Z3 module `gofmt/vet/test` pass, toc_check 35/35. Checkpoint commits 1 and 2 are carved states of the final tree (no representation tests: their features are absent): each builds, vets and passed the pre-existing suite; correctness claims rest only on the final tree.

## Port fidelity and LOC audit
Ported from `repz3` (rep, decode, reach, catalog, section), `repz3/kinds` (tar, sqlite, car, merkle), `sqlitez3`, `carz3`, `merklez3` and `prodshadow` descriptors. Kept: wire, framing, height-memoized reachability (Z3-28 fix, regression test),
error classes (`errRepCorrupt/Missing/Depth/Cycle/Unsupported`), decoder logic, proof-before-publish. Adapted: stdlib-only package merge, production `Source` over CAS/packs, depth 4 limit, the root/descriptor layer, hex-keyed reachability integration.
Not ported: planner, BASE/ZERO/graph primitives, Gzip/Zstd/Git/OCI/ELF. Pinned by `TestRepWireGoldenAndRoundTrip` (golden bytes) — there is no byte-for-byte cross-check against the lab module in CI.
| file | LOC |
|---|---|
| `zeros3.go` | 21,676 → ≈27.5k (+5.8k; section 16d ≈5.7k) |
| `zeros3_rep_test.go` | ≈3.9k (new) |
| `testing-harnesses/harness/z3_i0/main.go` | ≈1.2k (new) |

## I1 carry-forward
1. Whole-read speed (3–4×): batch/readahead across components, avoid per-page hashing on trusted direct reads; or serve whole GETs from CDC (already the `shadow` behaviour).
2. Memory: replace hex-keyed reachability maps with digest-keyed structures; stream descriptor-heavy roots.
3. Storage: component/CDC chunk sharing so a representation is not additive; Merkle direct-hash liveness without a graph walk.
4. Root creation on sync/replicate/bundle import; a background rebuild is explicitly out of scope until then.
5. Tar-in-tar analysis (read supports depth 4, analysis builds ≤2 levels); Gzip/Zstd/Git remain reserved kinds 6/7/4.
