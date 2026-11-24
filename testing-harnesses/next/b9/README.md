# B9: grouped authority records, reversible executable normalization, decoded-window cache

Linux amd64, Go 1.27.0, 4 vCPU VM, virtio SSD; base `1e261c2` (B8). Families, lanes and criteria were fixed first
([PREREGISTRATION.md](PREREGISTRATION.md)); inputs [corpus.sha256](corpus.sha256) + [make_inputs.py](make_inputs.py);
reproduction [run.sh](run.sh); raw rows in [results/](results/) (`tables.py results/*.jsonl*` →
[tables.md](results/tables.md)). Stores are measured after optimize + gc + repack + compact by allocated blocks;
every store deep-verifies and every read is checked byte for byte.

**Verdict: `B9_EFFECTIVE`** for grouped records, the major change: the modeled B8-G gain survives implementation
in full. Executable normalization and the window cache work but are **specialized** (archive tier; repeated sparse
reads). Format: store features `recgroup`, `exenorm`; the B8 build refuses such stores and leaves them untouched.

## Architecture
* **Grouped records** (`recgroup`, `zeros3.go` 16e4): `bodies/groups/<seq>.zrg` of CRC-32C record/tomb frames,
  appended under a directory flock and fsynced before indexing (visible ⇔ durable). The latest frame of a content
  decides it; a content the log never names falls back to its record file (mixed stores). Groups seal at 1 MiB with
  an index frame, so open scans only unsealed groups. Retirement appends tombs (one fsync per batch); gc, repack and
  B4 online maintenance (pins, a T0 log mark, the reader lease) rewrite groups below 75% live by re-appending live
  frames before unlinking; tombs are carried while an older frame or file could resurface; torn tails are cut only by
  an appender when no valid frame follows; damaged groups are never rewritten. Priced as record + 51 B + group slack.
* **Executables** (kind 15, `exenorm`, 16e5): ELF64/PE32+ x86-64 with a Go 1.18+ function table. CALL/JMP/Jcc rel32,
  RIP-relative slots and aligned pointer words naming a unit (function or symbol; FNV-1a of a unique name) hold its
  hash; the candidate test reads only bytes before the slot, so encoder and decoder agree; collisions are exceptions.
  The canonical stream (same offsets, then the unit table) is a B8 body; restart points per 64 KiB bound range decodes.
* **Window cache** (16e6, opt-in `ZEROS3_WINDOW_CACHE_MIB`): 16 KiB output windows of replayed reps (gzip, zstd, ZIP,
  kind 15) keyed by the rep digest; a window is decoded whole at its second touch; CRC on hit, single-flight, sharded
  two-segment LRU; requests > 1 MiB and whole reads bypass; nothing persisted.

## A. Small-file storage (model units = B8-G pricing, MB)
| family / profile | b8 files | B8-G model | b9 | modeled → realized | allocated b8 → b9 | deltas b8/b9 |
|---|---:|---:|---:|---|---|---|
| Coreutils files, archive | 4.04 | 2.46 | 2.45 | −39.0% → **−39.4%** | 5.26 → 3.64 (**−30.8%**) | 89 / 525 |
| Coreutils files, mixed | 4.24 | 3.62 | 3.60 | −14.7% → −15.2% | 5.46 → 4.82 (−11.6%) | 32 / 76 |
| Go src files, archive | 3.50 | 2.27 | 2.26 | −35.1% → **−35.4%** | 5.39 → 4.14 (**−23.2%**) | 87 / 326 |
| Go src files, mixed | 3.77 | 3.27 | 3.25 | −13.3% → −13.7% | 5.66 → 5.14 (−9.1%) | 16 / 61 |
| synthetic 12k files, archive | 29.06 | 14.61 | 14.35 | −49.7% → −50.6% | 40.90 → 26.16 (−36.0%) | 0 / 8,000 |
| random files (control) | 26.91 | 26.91 | 26.91 | 0 → 0 | 28.12 → 28.12 | 0 / 0 |

Plans equal the materialized model on every lane; a record costs 203-268 B instead of 4,096. Layout alone (the b8
graph regrouped) is −10.5%/−9.1% with equal reads (cold 4 KiB 0.27 vs 0.28 ms; 8 readers 52-74k vs 46-71k/s). Record
lookup cold 147-180 µs (files 414-549), after an index rebuild 0.41-0.94 ms (12k records: 4.3 ms), warm 3 µs (files
10-15); 1,000 publishes 4.2-5.0k/s vs 1.6-1.9k/s, 238 KB vs 4.1 MB; retiring them 5 vs 125 ms. **Regression, priced:**
the archive graph b9 enables has 6× the deltas, so its cold 4 KiB reads take 0.55-0.59 ms (b8 0.28-0.31) and 8 readers
get 16k vs 62-71k/s; `mixed` keeps 0.26 ms. Optimize: 9.2 vs 7.8 s (Coreutils), 35 vs 10 s (synthetic, 124 MB written).

## B. Executables (Go 1.22.0/1.22.6/1.23.0/1.23.4, 6 tools × 4 releases, archive)
| format | CDC MB | b8 MB | **b9n MB** | cold 4 KiB ms b8 / b9n | 64 KiB | 1 MiB | whole |
|---|---:|---:|---:|---|---|---|---|
| ELF | 84.82 | 50.57 | **47.17 (−6.7%)** | 3.0 / 5.5 (1.84×) | 4.8 / 7.8 | 31 / 32 | 123 / 126 |
| PE | 87.35 | 52.63 | **46.67 (−11.3%)** | 2.6 / 5.4 (2.08×) | 4.7 / 7.8 | 32 / 31 | 127 / 131 |

Kind 15 is chosen for 23 of 24 ELF objects. Pairwise, a normalized point-release delta is 60-78% of the raw delta
(82-85% across minor releases, [exeprobe.txt](results/exeprobe.txt)); the store gains less because B8 already uses
cross-tool bases and the CDC bases dominate. What remains is mostly `.gopclntab` (36% of literal bytes, compile
1.22.0→1.22.6) and `.rodata` (28%); delta-coding the function table's offset columns (probe only) would cut
4-15 more points. Costs: optimize 206 vs 134 s, RSS 3.5 vs 1.8 GiB, 503 vs 41 MB written; each cold read loads
the ~120 KB unit table through the stream's delta. Criterion (≥5% bytes, cold 4 KiB ≤2×): met on ELF, PE reads 2.08×.

## C. Decoded-window cache (B8 canonical stores; off vs 64 MiB budget, mean of two orders)
| workload | TypeScript req/s | Django req/s | p50 ms (TS) |
|---|---|---|---|
| repeated ranges | 59 → 143 (2.41×) | 101 → 198 (1.96×) | 16.7 → 0.07 |
| Zipf, 8 readers | 192 → 304 (1.58×) | 231 → 362 (1.57×) | 34.6 → 0.10 |
| nearby / cold / unrelated | 1.21× / 1.01× / 0.94× | 1.05× / 0.87× / 0.98× | |

Hit rate 0.79-0.86; 1.4 MB holds the working set (16-256 MiB: flat). 64 KiB windows reuse nearby ranges better
(1.40-1.47×) but repeat less (1.70-1.83×); admitting at first touch halved cold throughput (0.43-0.47×,
[cache-v1.jsonl](results/cache-v1.jsonl)) since a miss decodes the whole window. Containers sharing one stream share
no windows. Criterion (≥2× repeated *and* nearby, cold ≤10%) not met: the cache stays opt-in.

## Correctness evidence (`zeros3_b9_test.go`)
Frame/seal/torn/damage scans (2,000 random mutations); lifecycle (seals, reopen, tombs, compaction, revert, a reused
identity, ungroup/regroup); mixed legacy + grouped; injected torn append; a damaged frame fails closed and is reported;
B4 online passes with live readers; concurrent publish + compaction + readers; another process's appends; **33 real
SIGKILL cuts** across append, fsync, seal, create, publish, compaction and regroup, each followed by exact reads,
deep verify, reclamation and convergence; the B8 binary refuses a grouped store. Kind 15: exact whole and 400 ranges
of real ELF/PE builds, window edges, 600 damaged images, a store round trip. Cache: reuse, single-flight, CRC damage,
eviction, bypass, representation change. Run: B9 tests; `-race` on the concurrent, online, lifecycle and cache tests
(pass); the authority/rep/B3-B8/pack suite (pass); the same authority/B4/B6/B8 suites with `ZEROS3_RECORD_GROUPS=1`
(pass but for 11 tests that edit `bodies/<hh>/*.body` files directly or expect another feature named first);
the root suite once (1,132 s: two wire tests still asserted kind 15 unassigned; updated, rerun alone: pass).
Not run: T1 qualification, exhaustive fuzzing, external harnesses; PDB not attempted.

## Limits and B10
* A process with a stale group index may serve an older (exact) generation until its next tail refresh.
* Executables: x86-64 Go images only (no C/C++ units, ARM64 or PDB); archive tier because of the unit-table read.
* B10 should inherit grouped records (a default-on candidate) and price per-record reads, keep normalization and the
  cache behind flags (unit-aligned, stream-level windows would remove the fill penalty), and pursue deeper
  container and compressed-structure representations.
