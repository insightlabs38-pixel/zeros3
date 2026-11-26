# FB-03: CAS presence, maintenance isolation, locator hygiene

Code on `research/next`: `f9ca12e` (M-A2), `81faf04` (M-A7), `cf10d43`,
`ba5d109`, `6618aa6`, `b5d490f`, `1c7bfbb`, `ebfc5a1` (M-A8), `a808140`
(scale results). Base is the FB-02 head `7ea1686`; `main` unchanged at
`2ac9cab`. No on-disk format change; every locator structure is in memory and
rebuilt at open. Go 1.27.0, standard library, one file. Evidence is in
`results/`; see `REPRODUCE.md`.

## Verdict

| Milestone | Result | Status |
|---|---|---|
| M-A2 | presence only from published state, locator before the loose path; a failed publisher is never a dedup hit | COMPLETE |
| M-A7 | one damaged leaf or record no longer blocks or aborts maintenance; idle stores run no mark | COMPLETE (narrowed: reference damage still refuses deletion everywhere) |
| M-A8 | at most 4 levels under sustained ingest, a filter on every level, retired records dropped by the next merge, open at 1.0-1.1x steady, bounded descriptor cache | COMPLETE |

## M-A2: presence and stat order

`casStat` asks the locator first and counts a loose file only while holding
its publication claim; the segment and pack sinks use it, and the batch
leader re-checks against published packs only. Chunks already stored loose
are no longer rewritten into segments: growth on a re-upload falls from
1.005x to 0.004x of the logical size (F2-CAS-01). A packed hit costs 37 ns
against 1,004 ns in F2 (F2-CAS-02). On `7ea1686` both tests fail
(`results/ma2_tests_base_7ea1686.txt`). A per-key linearizability checker
over concurrent PUT, GC and repack (280 operations, 25 packs retired) found
no hit on unpublished or retired bytes. Ingest wall time moved -0.5% to
+3.2% over 12 alternated rounds (`ma2_ingest_run3.txt`). Repack and compact
already re-verified their output (F3-CARRY-07, confirmed by reading).

## M-A7: isolation and the dirty gate

Damage to a live chunk (a leaf) no longer stops a pass: every pack holding
any copy of a damaged digest is held, and the rest is reclaimed (186 KB of
the control's 1.68 MB in the isolation test, the remainder held). Damage to
a manifest or other reference, and any chunk damage in a store with
authority bodies, still refuses deletion (`errGCUnsafe`), as the narrowing
clause allows: a grouped authority frame can read as a torn tail, so a
missing chunk may be the only trace of a live body. A carry that cannot read
a record drops and quarantines its holder packs (in memory) instead of
aborting. An idle store skips the mark (journal sequence, events and the
sealed-pack hash), rechecked every 24 h. A crash at each of the 11 points an
isolating pass reaches reopens with every undamaged object intact, every
copy of the damaged digest kept, and a new pass completing. Five older tests
were updated to the new behaviour (`results/ma7_*`).

## M-A8: locator

Levels follow a size ratio that grows with the store, so the count stays at
most `ZEROS3_LOCATOR_LEVELS` (default 4). Publishes keep the bound
themselves; merges over 64k records run in the background, outside
`packMu`, and publish by pointer swap. Merges are linear and drop retired
records. Each level carries a 10-bit split-block filter (1.3 B/rec), reused
or OR-ed across a merge that drops nothing. Open parses each pack index into
one array and sorts it in place. Lookups for segment ingest run eight at a
time. GET, the pipelined reader and replication keep pack files in a
256-entry LRU that is evicted before and after every unlink or rename.

| Gate | Result (`results/ma8_*`) |
|---|---|
| Level bound | growth to 2M, 8M, 17M and 25M records (2- and 64-record commits): never more than 4 levels; 2-record commits p50 3.6-4.5 us, p99 43-97 us, max 4.6-11.5 ms (13 ms with 64-record commits); 2.9-4.6 us per new record |
| Negative lookup | X2 policy state 0.33x, 0.37x, 0.39x a single unfiltered level at 1M, 4M, 16M; after growth 0.30-0.99x, timed in the same process |
| Open memory | lean open peaks at 1.08x, 1.03x, 1.02x steady at 1M, 4M, 16M (1.00x with 8,192-record packs, 1.65x with 16-record packs); the reference loader 4.9x |
| Retirement | 10 cycles retiring 10% at 4M and 16M: records/live stays at 1.10, the previous cycle's retired records gone at the next merge |
| Failure criterion | 0 wrong answers in every X2, open, growth and retire run; forged digests (one bit, or the last 16 bytes) never accepted |
| Ingest | wall -4.3% to +0.9% against `7ea1686`, 12 alternated rounds (`ma8_ingest.txt`) |
| GET, 8 KB objects | p50 36 us with descriptors cached against 60 us without; cold 44 against 59 us |
| Tests | full suite at `a808140` passes in 1,691 s; locator, merge-model, forced-collision, open-equivalence, descriptor and race tests (`ma8_tests*.txt`) |

The 1.3x criterion is read against one level without a filter, the 266 ns
baseline in the milestone's motivation. Open now also filters its single
level, and against that the multi-level state costs 1.8-2.3x on absent
digests (about 30 ns per extra level) and 1.2-1.4x on a mixed workload.
Present digests cost up to 1.5x one level. Filters pay: without them four
levels cost 3.1-4.1x. A scale run found the open peak at 3.5x steady (two
allocations per record); fixed in `1c7bfbb` and `ebfc5a1`. A race run found
a quiet store keeping retired records in its newest level; fixed in
`b5d490f`.

## Gates

- G-COLLISION: digests sharing their bucket and first 16 bytes (the filter's
  and the open check's key), forgeries of present digests, clashes and
  duplicates, through open, merges, retirement and every lookup path
  (`TestFB03_LocForcedCollision`, `TestFB03_LocOpenEquivalence`).
- G-SCALE: generated metadata populations of 1M-25M records (above).
- G-FAULT: M-A7 crash matrix; descriptor eviction across seal, move,
  retirement and unlink (`TestFB03_PackFD*`).
- G-LINEAR: M-A2 per-key checker; merges published during concurrent reads.
- G-MODEL: merge model with publishes, retirements and settles; M-A7 model.

## Open

- A persistent locator generation and tag accelerator stay out (M-B2).
- Multi-level absent lookups carry a fixed cost per level that a combined
  filter could remove; that belongs with M-B2.
- Quarantine of unreadable records lives in memory until the next restart.
