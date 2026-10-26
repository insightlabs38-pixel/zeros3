# Z3-22 — Production-shaped shadow integration
**Decision: PROMOTE.** `repz3` representations map onto the real ZeroS3 CAS/pack/tier/read/GC machinery as optional
shadow state with exact reconstruction in every storage state, while the ordinary CDC object stays authoritative and
sufficient. This authorizes a later, separately approved integration *design* batch, nothing more. Production source,
manifests, journal, pack/locator/tier formats and GC are untouched (`git diff -- zeros3.go go.mod go.sum` is empty).

## Method (no second store)
The harness compiles the current `zeros3.go` twice into the OS temp dir: unmodified, and a bridge copy with one method
renamed plus one lab file (`prodshadow/bridge/zz3bridge.go.txt`, never built into the product). The bridge serves
the real `Store` (casRead/casStat/CAS batch, locator, an S3 signing proxy in front of the real handler). `compact`,
`tier move`, `versions prune`, `gc`, `repack`, `verify -deep`, `stats` are the production CLI. The bridge binary differs only
by `Z3_SHADOW_LIVE`: extra live chunks added to `computeReachabilityObserved` (a missing one fails the live set closed).

## Component → production storage mapping (settled by evidence)
Production invariants: pack records are 1..256 KiB (`maxPackedChunkBytes`); `compact` *skips* larger loose chunks (tested: a
1 MiB direct chunk stays loose forever), so Model A is **invalid above 256 KiB**. Chosen hybrid:
- **≤ 256 KiB: direct** — one CAS chunk whose digest is the logical component SHA (no descriptor, production verifies it).
- **> 256 KiB: virtual** — a flat descriptor of ordered production-CDC chunks (`Descriptor`, ≤ 65 536 refs, checked sums,
  canonical, CRC32C root file, 36 B/ref); logical id stays SHA256(bytes). Empty components have no physical object.
- Descriptors live only in lab shadow **roots** (`shadow/roots/<sha>.root`, tmp+fsync+rename). Not in manifests/journal.

| size (random) | n | mapping | records | descriptor B | loose MiB (alloc) | packed MiB | 4 KiB point read (stored B) |
|---|---|---|---|---|---|---|---|
| 1 KiB | 2048 | direct / virtual | 2049 / 2050 | 0 / 75 849 | 2.07 (8.07) | 2.26 | 1 024 |
| 64 KiB | 128 | direct / virtual | 129 / 215 | 0 / 8 017 | 8.00 | 8.02 | 65 536 / 65 536 |
| 256 KiB | 32 | direct / virtual | 33 / 155 | 0 / 5 547 | 8.00 | 8.00 / 8.02 | 262 144 / 106 837 |
| 1 MiB | 8 | virtual only | 125 | 4 408 | 8.00 (8.24) | 8.01 | 70 857 |

Virtual everywhere buys nothing for small parts (+3.6 % descriptor at 1 KiB, +0.9 % at 4 KiB, more records); above 256 KiB it bounds a point
read to ~1–2 production chunks. Loose 1 KiB files cost 4× in allocated blocks; compaction repairs it (2.26 MiB).

## Real physical cost (production `stats`, MiB, 4 MiB compaction packs; shadow = shadow-only store, adaptive DEFLATE)
| workload | CDC 1st | shadow 1st loose → packed | CDC incr. rev2 / rev3 | shadow incr. rev2 / rev3 | totals CDC / shadow / CDC+shadow |
|---|---|---|---|---|---|
| SQLite 47 MiB | 46.95 | 47.25 → 48.37 (+3 %) | 3.35 / 41.64 | 0.24 / 0.36 | 138.9 / 97.4 / 236.2 |
| gzip→tar→SQLite | 4.04 | **10.59 → 5.02** (raw packs 10.87) | 3.51 / 3.57 | 0.27 / 0.46 | 11.12 / 5.76 / 16.87 |
| tar+SQLite (512 KiB virtual member) | 5.48 | 5.51 → 5.63 | 1.83 / – | 0.15 / – | 7.31 / 5.78 / 12.67 |
| Git repack chain | 4.47 | 4.50 → 4.55 | 0.14 / 0.42 | 0.02 / 0.06 | 5.35 / 4.68 / 5.89 |
| CAR reorder | 10.50 | 10.48 → 10.50 | 7.71 / – | 0.00 / – | 18.21 / 10.50 / 28.70 |
SQLite/CAR rows include the negative "wholesale" stage for the totals (hindsight cheapest per revision: 94.5, 4.78 gzip, 10.5 CAR). Pack cost is 96 B/record (44 header + 52 index): SQLite's 12 189 page records carry 1.1 MiB of it (net +1.4 MiB, +3 %).

**gzip first sight** (Z3-21: ≈ 2.6× CDC): loose 10.59 / 4.04 = **2.62×** reproduced; after production packing 5.02 / 4.04 = **1.24×**
(2 516 of 3 024 records DEFLATE, 508 raw). Packing repairs most of the gap but not all: first sight still loses to CDC by 24 %;
revisions 2–3 win ≥ 7× (0.27/0.46 vs 3.51/3.57). The representation is not changed. **Double counting:** shadow content
equals CDC chunks only where a component is a verbatim byte range cut by the same CDC — Git streams (65 chunks, 4 MiB shared,
CDC+shadow only +0.40 MiB); elsewhere 0, so CDC + shadow ≈ the sum. Storing both costs both until CDC can be dropped.

## Reads, locality, restart, tiers, concurrency (final stores; every row exact)
| read (packed) | CDC range/GET: records / packs / stored B | shadow: records / packs / stored B / ReadAt |
|---|---|---|
| SQLite page 6000 | 1 / 1 / 111 304 | 3 / 3 / 12 823 / 3 |
| Git object (159 B) | 1 / 1 / 31 203 | 3 / 1 / 24 370 / 11 |
| CAR block (128 KiB) | 4 / 1 / 253 832 | 3 / 2 / 135 682 / 3 |
| whole SQLite 46.9 MiB | 738 / 12 / 0.52 s | 12 189 / 25 / 10.3 s (4.6 MiB/s) |
| whole gzip layer 3.6 MiB | 57 / 1 / 0.05 s | 2 728 / 4 / 5.0 s (0.7 MiB/s, exact DEFLATE replay) |
- **Restart**: reopening runs the production locator load (33 ms / 78 packs, ≤ 4 ms otherwise); only persisted roots are needed
  (adopt < 0.2 ms); Git catalog rebuild 5 ms / 464 keys; identical results loose and packed-compressed. No hidden state.
- **Tiers** (packs round-robin hot/warm/cold with production `tier move`): `Size`, bounded `ReadAt`, whole, nested-gzip, concurrent
  all exact. Locality worsens: a SQLite page read touches three packs in three tiers (CDC: one); bounded, not optimized.
- **Concurrency**: 8 whole reconstructions (each gzip parent + DERIVED child) + 8 random readers over one packed, tiered store;
  `go test -race` on those tests passes; 1 reader ≈ 1.1–2.9 k reads/s, 8 readers ≈ 3.4–16 k/s. Bridge peak RSS 16–23 MiB.

## Failure matrix (authoritative GET + range GET exact and `verify -deep` ok in every row)
| scenario | shadow outcome |
|---|---|
| 1 commit, no shadow / 3 complete components, root unpublished | absent / not visible although bytes exist |
| 2 partial components, SIGKILL | no root; 40 orphan chunks reported unreachable (shadow GC debris) |
| 4 root ok, dependency deleted / corrupted / substituted | clean failure (missing / mismatch / mismatch) |
| 5 root bit flip / truncated / trailing byte / empty | all rejected (CRC + digest-named file) |
| 6 catalog lost | rebuilt from roots + index sections, identical |
| 7 crash in metadata publish before / after rename | no root, tmp ignored / root valid and exact |
Journal, manifests and snapshots are byte-identical before/after building, retiring and failing shadows. Parser: deterministic malformed tests, every truncation and bit flip rejected, 25 s fuzz (349 k execs) clean.

## GC / repack lifecycle (all exact; "remaining == CDC ∪ shadow-live" checked set-wise)
- **Current production GC** (shadow roots unknown): all 1 584 shadow-only chunks unreachable; `gc -apply` removes them and the shadow
  then fails cleanly while CDC GET stays exact — the expected integration point, not a bug.
- **Shadow-aware mark**: live current+historical shadows → only the stray chunk reclaimed; shadow A retired → 59 A-only reclaimed,
  1 466 shared kept; CDC history pruned (132→102 chunks) with shadows retained → shadow-only liveness independent and exact; broken
  shadow → mark fails closed, production GC refuses a live set naming an absent chunk, dropping the root unblocks cleanup.
- **Chain** (PUT A, B, shadows, shadow-aware compact, tier spread, retire A, prune, gc, repack, restart, verify): 14.1 → 12.3 MiB, all exact.

## Assumption audit
| assumption | result |
|---|---|
| component = one chunk | false: parts reach MiB, records cap at 256 KiB → hybrid mapping |
| adaptive compression changes rankings | yes for gzip (2.62× → 1.24×); not for random SQLite pages |
| thousands of small components acceptable | yes after packing (96 B/rec, ~+3 %); loose 4 KiB files cost 4× allocation |
| concurrent readers / mixed tiers / restart | pass; locality worse across tiers; no hidden state |
| shadow reachability joins GC | one reachability source feeds compact, gc, repack, verify, tier unchanged |
| shadow needs the journal | no |

## Limitations and carry-forwards
- Whole reconstruction is 3–100× slower than production GET: every record is read alone (no `packRunReader` coalescing; lab HTTP hop ≈ 0.7 ms) — design needs a coalescing Source.
- Shadow plus CDC is additive (sum of both) until an alternate-read/drop-CDC design exists; first-sight gzip still +24 %.
- Lab scale (≤ 47 MiB objects, 4 MiB packs not 64), synthetic SQLite/gzip pages, one Git chain; allocation figures are filesystem-specific. Carry-forwards: coalesced Source; shadow orphan-GC policy; DEFLATE replay speed (out of scope); mixed-tier placement.
## Reproduce
`cd testing-harnesses/z3 && gofmt -l . && go vet ./... && go test ./prodshadow` (builds both binaries once, ~1.5 min);
`go test -race ./prodshadow -run 'TestConcurrentReaders|TestRangeReads|TestSourceRejects|TestMapping'`;
`go run ./cmd/z3shadow -work $TMPDIR/z3w -scale full all` (~30 min; subcommands: mapping chains reads restart scenarios).
