# Z3-28 — Representation DAGs, deep composition, global planning, integration readiness (lab)

**Question.** Can the compact `repz3` v2 contract express, reconstruct, account for, garbage-collect and economically choose bounded representation DAGs (depth 4, fan-out,
shared descendants, competing plans) without another generic wire primitive — and is the promoted code portable enough for Z3-I0 to wire rather than reimplement?
**Decision: `READY_FOR_I0`.** **`repz3` v2 survives unchanged** (golden wire test and `Version=2` untouched; no BASE/ZERO/ALIAS/GRAPH/PLAN/ROOTS). Production untouched (`zeros3.go`, `go.mod/sum` diff empty).

## Contract outcome
- A shared child is just an identical `DERIVED.Child` digest: one rep object, one component subgraph (fan-out: duplicate member cost 617 B against a 196 608 B database). Graph-shaped formats
  (Merkle, CAR, Git) use **0** DERIVED edges between revisions; sharing is content-addressed components plus kind-local record sections. Roots and planner state live only in the lab store layer.
- Five **non-wire** faults found by the DAG tests and fixed (each has a test): (1) `Reachable` memoised shared reps without their height, so `ReachableAll([shallow,deep])` accepted a root `Reconstruct` rejects
  (liveness depended on root-set order) → `Reach.Height`; (2) `Catalog.Ingest` had the same defect, accepted a cycle (in-progress node treated as done) and could not take `Limits` (depth-4 roots refused) →
  `SetLimits/RebuildLimits`; (3) gzip/zstd decoders reported the codec's symptom ("truncated unit stream", "plaintext ends early") instead of the child/unit failure, hiding `ErrMissing/ErrDepth` through nested
  codecs → `replayCause`; (4) format-package errors (`sqlitez3: corrupt …`) carried no contract class → `classify` wraps with `ErrCorrupt` (both stay `errors.Is`-visible; destination-writer errors are not reclassified);
  (5) `kinds.Prove/NewEnv` could not run depth 4 → `*Limits` variants.

## DAG correctness (every row: reload from serialized objects only, `Reconstruct` equals length + SHA-256 + bytes; `read ⊈ reach` = objects read but not reachable)
| workload | depth | roots | rep nodes | shared reps | exact | reach union / sum objs | read∖reach |
|---|---|---|---|---|---|---|---|
| depth-4 A `zstd>tar>gzip>sqlite` | 4 | 1 | 4 | 0 | yes | 209 / 209 | 0 |
| depth-4 B `gzip>tar>zstd>car` | 4 | 1 | 4 | 0 | yes | 74 / 74 | 0 |
| fan-out (8 members: 2× SQLite = 1 child, CAR, gzip, ELF-ish, broken DB, raw blob, note) | 3 | 1 | 8 | 1 (2 parent edges) | yes | 137 / 137 | 0 |
| shared roots (tar A, tar B, loose DB) | 2 | 3 | 3 | 1 | yes | 107 / 307 | 0 |
| natural graph: Merkle r0–r2 + wrapper / CAR reorder ×2 / Git ofs+ref | 2 / 1 / 1 | 4 / 2 / 2 | 4 / 2 / 2 | 0 | yes | 1059/4142 · 65/128 · 17/30 | 0 |
| cross-format child (tar.gz, tar.zst, loose) | 3 | 3 | 4 | 1 | yes | 135 / 336 | 0 |
- Gates (all tests): depth 5 built from real nodes → `ErrDepth` from reconstruct, reach and catalog (`MaxDepth 99` clamps; limit not raised). Deleting **each** reachable object of A, B and a Merkle wrapper (6 object
  classes: rep, table, stored, via, owned, index; 42/74/193 objects) breaks reconstruction with a bounded error every time (0 unclassified, 0 wrong outputs) — reachability has no superfluous or missing object.
- Liveness (real `ReachableAll` over a lab root set, no refcount): remove A → exactly `Reach(A)∖Reach(B)` is freed, shared child lives; remove B with a third root holding the child → still live; last root → store empty.
  The owned targets of a shared Merkle child survive removal of one parent. Catalog rebuild equals the incremental catalog in any root order; `ErrAmbiguous` preserved; self/2/3-cycles terminate; 12 malformed DAG classes and
  900 garbage recipes (6 kinds) fail bounded. Framing differing per parent keeps one child. `-race` clean; no goroutine leak on success, mid-stream failure, or 8 concurrent readers.

## Depth economics (cumulative persisted bytes, stored raw; per-object overhead 0; versions after 1 / last)
| workload | CDC | d1 | d2 | d3 | d4 | winner |
|---|---|---|---|---|---|---|
| A `zstd>tar>gzip>sqlite`, 6 revs, 6 of 160 pages change | 258 704 / 1 552 352 | 263 145 / 1 578 951 | 263 125 / 1 524 528 | 687 217 / 2 479 518 | 692 077 / **918 613** | CDC first sight; d4 from ≈4 versions (real libzstd 1.5.7 row agrees: 1 040 518 vs **826 795** at 4) |
| B `gzip>tar>zstd>car`, 4 revs | 89 773 / 381 654 | 90 380 / 383 537 | 90 236 / 376 866 | 92 956 / 383 548 | 92 848 / **115 699** | d4 from 2 versions |
| complementary `tar.gz>sqlite`, 5 | 258 540 / 1 264 799 | 684 101 / 2 081 529 | 684 253 / 2 080 241 | 689 113 / **866 532** | = d3 | d3 from ≈4 |
| complementary `tar.gz>car`, 4 | 89 633 / 383 298 | 91 543 / 387 920 | 91 404 / 380 103 | 91 296 / **112 254** | = d3 | d3 from 2 |
| overlap: tar of duplicate DBs, 3 revs | 464 743 / 992 631 | **395 821** / 783 817 | 398 728 / **436 444** | = d2 | = d2 | first sight d1 (d2 +0.7 %); d2 once pages change |
| overlap: gzip of random + appended, 4 | **616 734** / 836 040 | +122 B first sight | = d1 | = d1 | = d1 | CDC (exact layer exposes nothing new) |
| negative control: unrelated DBs in tar.gz, 3 | **155 265** / 466 144 | 410 886 | 411 038 | 414 088 | = d3 | CDC |
| cross-format gz→zst→loose, 3 | 258 540 / 1 316 728 | 684 101 / 1 897 716 | 684 253 / 1 896 996 | 689 113 / **958 620** | = d3 | d3 from 3 |
- Each level only pays when **the payload below the codecs is structured**: d3 with a CDC leaf loses to CDC in A (1.6×). Exact replay is a first-sight premium (A: 2.7×, components counted uncompressed).
  Same plaintext as gzip-1, gzip-9, zstd: the 2nd outer adds 11.6 KB structured vs 229 KB CDC (child shared). Store already holding the DB: arriving archive adds 28 413 B (cap 4) vs 258 540 B (CDC).
  Per-object overhead 0–512 B never changes a winner for 24–160-page DBs; 1 KiB/object flips ≤40-page DBs to CDC. Hostile inputs and budgets are in `cmd/z3dag budget` (below).

## Planner (storage = SHA-set union of new objects + overhead; exact B&B vs brute force on 40 random instances)
| scenario | independent | marginal | marginal+swap | optimum | gap |
|---|---|---|---|---|---|
| shared child pays (2 parents, X=100) | 210 | 210 | 210 | **120** | 75 % |
| expensive child unlocks 4 parents (X=300) | 480 | 480 | 480 | **320** | 50 % |
| fake sharing (children differ) / independent-optimal / store already holds X | 210 / 330 / 20 | = | = | = | 0 |
| synthetic n=8 / 16 (exact: 514 / 5 650 nodes, ≤1 ms) | 1015 / 2116 | 1015 / 1456 | 1015 / 1456 | 487 / 828 | 2.08× / 2.56× (ind), 1.76× (marg) |
| synthetic n=32 / 64 / 128 (exact capped at 30 M nodes, 3.5 s; best found) | 4195 / 7964 / 15960 | 4195 / 7589 / 15022 | 4195 / 7494 / 14581 | ≤1971 / 6016 / 13396 | ind 2.1×/1.32×/1.19×, swap 1.09× at 128 |
| **real chain A, 6 revs (cumulative)** | 1 552 352 | 1 552 352 | — | **918 613** (d4 ×6) | 1.69× |
| real chain B, 4 revs / tar of duplicate DBs, 3 | 381 654 / 783 817 | = | — | 115 699 / 436 444 | 3.3× / 1.8× |
- Independent and marginal admission lock into the cheap first-sight CDC and never pay the investment (marginal = independent on every revision chain); single swaps cannot escape. Planning is path-dependent: v3 of A costs
  258 749 B against an empty store or earlier revisions stored as CDC, 45 312 B against earlier revisions at depth 4, 31 395 B against an unrelated top-level holding the DB. Mixed winner (one tar, store holds the previous revision):
  deep for the sparse-updated db.gz and the loose db, shallow for random.gz = 319 862 B vs all-deep 319 989 (+127 B) vs all-shallow 820 281; plan == store growth to the byte (also asserted for 4 planners × 2 overheads).
  Read-aware (λ = storage byte per byte fetched): first sight stays CDC (point read 258 860 B) until λ>1.72, then d4 (6 901 B); with lineage d4 wins at every λ. Invalid candidates (unknown child, output mismatch, duplicate id,
  cycle, no candidate, overflow, depth) are rejected, never repaired. Production needs lineage-aware lookahead and per-candidate child-SHA sets, not a perfect optimizer; no planner field enters a wire object.

## Reads and scale
- One page of a database, depth 1→4 (artifact differs): 6 353 / 6 505 / 6 642 / 6 760 B — +407 B for three more levels (one 100–150 B rep object each; codec warm-up 0 because the semantic plaintext child is addressed directly).
  Same depth-4 artifact by candidate cap: CDC 258 681 B (whole), d1 260 761, d2 260 754, d3 66 085 (CDC leaf chunk), d4 **6 760**. CAR block: 2 810 B (d1), 3 286 B (d4), 93 480 B when the CAR is not exposed.
- Wide root, 100/1k/10k tiny children: reach 0.18/2.1/21 ms (N+1 reads), reconstruct 2.3/23/160 ms, root 6.8/68/680 KB, reach alloc 0.14/1.7/16.6 MB — linear in nodes, not payload. Fan-in 10/100/1000 parents: union walk N+1 reads vs 2N
  separate (1.2 vs 2.9 ms at 1000). Planner: 15 000 candidates 100 ms / 43 MB. Serial reconstruction suffices (8 goroutines: 110→92 ms over 2000 roots); no scheduler proposed.
- Recursive discovery budgets are cumulative: 5000 magic-looking members → 65 attempts, 80–140 ms; 64 MiB gzip bomb refused after one pre-flight (decoded ≤ budget+1); 8×400 KiB gzip under a 1 MiB budget → 2 accepted, 6 refused.

## Portability audit (import audit: `lab/fstate/composez3` appear only in `kinds/{car,git,kinds}.go` (3 `ChunkCDC` calls and the `lab.Piece/Comp/ID` types in Git decode), 9/22 `gitz3`, `ociz3`, `composez3`, `frontier/acct`; 0 in repz3, carz3, sqlitez3, merklez3, zstdz3, ir, deflate, binref)
| mechanism | status | reusable core | I0/I1 debt |
|---|---|---|---|
| repz3 core | CORE_PORTABLE | `rep.go` (Rep, Marshal/Unmarshal/Validate, Limits), `section.go` (ScanIndex); stdlib | none |
| repz3 decode/reach/catalog/account | CORE_PORTABLE | `decode.go` (Env, Call, Reconstruct, Open/Child, Get*), `reach.go`, `catalog.go`, `source.go` Source/ReadObject; lab appendices removable: MemStore, Cost/Account, ExpandOnly, noVerify | `Source` over CAS ReaderAt+Size; catalog persistence |
| Concat/tar | PORT_SMALL_REFACTOR | `KindConcat` decode (core); splitter `kinds.EncodeTarLayer`, `dagz3.splitTar` | `[]byte`→ReaderAt walk; `lab.ChunkCDC`→production chunker |
| SQLite | CORE_PORTABLE | `sqlitez3` (423 l) + `kinds/sqlite.go` (74): ReaderAt in, Writer out | none |
| CAR | PORT_SMALL_REFACTOR | `carz3` (1.9 kl, no lab) + `kinds/car.go` | one `lab.ChunkCDC`; block table in memory (cap 256 MiB) |
| Merkle | PORT_SMALL_REFACTOR | `merklez3` (840 l) + `kinds/merkle.go` decoder, owning sections | `EncodeMerkle(snapshot []byte, MemStore, Catalog)` → CAS `Has`, streaming ingest |
| ELF/PE | NOT_READY | `binref/elfz3/pez3` parsers (1.7 kl, stdlib) | no repz3 kind or recipe exists; Z3-19 path is `lab.Comp`/closures (`composez3`); needs a designed kind |
| Git | PORT_SMALL_REFACTOR | `gitz3` pack/delta/idx + gp-zlib kind, no closure on decode | `lab.Piece/ID` types in `RebuildSeq`; no Git→inner DERIVED (Z3-19 Git→SQLite not expressible: kind-local, no wire change) |
| Gzip | PORT_SMALL_REFACTOR | `kinds/gzip.go` + `ir` + `deflate` (5.9 kl, no lab) | plaintext buffered (`child([]byte)`); ≈0.35 s per 0.65 MB database incl. proofs |
| Zstd | PORT_SMALL_REFACTOR | `kinds/zstd.go` + `zstdz3` (3.4 kl, no lab) | as gzip; `zstdz3/synth.go`, `dagz3`, `lab/*Rebuild` closures, `composez3` are ANALYSIS_ONLY |
- Closure audit: 0 reconstruction closures reachable from `Env.Reconstruct`; every decoder reads only the serialized Rep/recipe/table + `Source`. Whole-object memory: decode is Writer/Source-bounded everywhere (tables held up to 256 MiB caps);
  analysis takes `[]byte` for tar, Merkle, Git, gzip, zstd (small adapters; real blocker only for gzip/zstd beyond 256 MiB/1 GiB); SQLite and CAR already take ReaderAt.

## Recommendations
- **Z3-I0:** repz3 foundation (core + decode/reach/catalog), KindConcat/tar framing, SQLite, CAR, Merkle. ELF/PE excluded (NOT_READY). **Z3-I1:** Gzip, Zstd, Git, OCI/container composition; shared blockers: streaming analyzer → component builder,
  CAS-backed ReaderAt with concurrent child/source reads, native-index/catalog wiring, lineage-aware admission (marginal admission locks in CDC), analysis scheduling, Git→inner kind work, ELF/PE kind design.
- **Depth:** default 3 / hard 4 unchanged. Depth 4 is a real shape (`.tar.zst` of `.db.gz`), pays after ≈4 versions (A) or 2 (B), costs +407 B per read; admit it only over a structured leaf. Synthetic depth 5 is not evidence; no raise.
- **Work limits for I0** (none added to `zeros3.go`): reuse `Limits` (MaxDepth, MaxMembers → 1<<20, MaxRepBytes → 64 MiB, MaxWhole, MaxObjects ≤ 1<<22, 16 indexes); add per-object analysis caps as in `dagz3.Budget` defaults: 256 attempts,
  64 analyzed members per container, 65 536 tar entries, 1 GiB inspected, 256 MiB cumulative decoded, 1 GiB proof bytes.

## Limitations, hermetic fix, reproduce
- Stored bytes are counted raw (no pack compression); `zstdz3.Synth` streams are not model-coded (a real libzstd 1.5.7 stream, produced by python-zstandard 0.25 outside Git, gives the same ranking); synthetic databases/CARs;
  single-machine timings; exact planner not run past n≈16; no ELF/PE or Git composition (Z3-19 regression: OCI/`tar.gz→SQLite` and `tar.gz→CAR` keep their qualitative result through serialized composition, rows above; Git→SQLite/ELF is not expressible in the gp-zlib `KindGit`);
  heterogeneous plans gain little where the shallow member is cheap to wrap.
- **Hermetic tests:** `zstdz3.TestCodingModeCoverage` failed on pristine main without `oracle/zstd/setup.sh` (LL/OF/ML repeat modes). Fixed with committed `testdata/fx_repeat_tables.zst` (389 B, built by the test builder, decoded by libzstd 1.5.7),
  `zstdz3.Synth` (lab producer) and a Synth-backed `TestZstdOverTar`; reference-zstd differential tests still skip without the tools. `go test ./...` passes in a fresh clone with no setup.
- Reproduce: `cd testing-harnesses/z3 && go test ./dagz3 ./repz3/... ./zstdz3 && go run ./cmd/z3dag all` (≈3 min; `Z3_PYZSTD=<dir with zstandard>` adds the libzstd row).
