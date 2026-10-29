# Z4 Frontier Campaign — active progress ledger

Status: **ACTIVE, unfinished**. This is a recovery cursor, not a terminal verdict.
Continue the first unfinished item; hourly wakes do not authorize a pause.
Latest merged main: 90eed88 (A2, PR #92). Active branch: z4/a3-structured-production.
Original draft PRs #80/#81 remain open; neither was merged wholesale.

| Item | Durable outcome | PR / state |
|---|---|---|
| A0 | PRODUCTION_TRANCHE_NEEDS_REWORK; isolated audit and old-retirement evidence | #90 merged |
| A1 | cumulative store-format capability design; old/new/wire/crash matrix | #91 merged |
| A2 | MODEL8_PRODUCTION_READY; bounded Model-only extraction | #92 merged |
| A3 | STRUCTURED9_10_PARTIAL; final gates passed for bounded subset | #93; merge pending |
| A4 | queued; real managed memory/lookup defects remain | #80 stays draft |
| B1 | similarity/delta CAS decision pending | queued |
| B2 | representation-authority decision pending; **DRAFT_ONLY** | queued |
| B3 | cross-format semantic CAS decision pending | queued |
| B4 | graph normalization / restricted reversible VM decision pending | queued |
| B5 | Model delta 2.0 decision pending | queued |
| B6 | hard-codec consumer ranking/deepening pending | queued |
| B7 | planner decision under new economics pending | queued |
| B8 | convergence / production decision pending | queued |
| Expansion | useful pool remains authorized after B8 | not started |

Current production kinds are 1–8. Model kind 8 publication requires store floor 7;
creation defaults off. A3's unmerged candidate adds kinds 9/10 and floor 8 while
retaining Model and all earlier decoders. No managed kind 11/12 is merged.
A1 tests show old local binaries refuse newer floors; compatible servers preserve
older wire clients and portable full/delta bundles omit optional graphs/floors.

Strong evidence: A2 exact independent real MiniLM full/range reads, 153.5 MiB/s
optional encoding, and fresh 1 GiB proof at 94.74 MiB maximum process RSS.
A3 preserves all 162 native cases: 66 SST /39 Parquet exact roots with unchanged
identities, 57 explicit fallbacks. Final frozen-source full/latency gates pass; PR #93 contains the bounded subset.
Its actual PR/main state decides whether promotion has landed.

Largest remaining blockers: expensive exact Zstd paths; optional physical growth
while CDC remains authoritative; managed whole-file copies, reference-state memory
and quadratic duplicate lookup; unproven generic similarity and authority/GC design.
A3 limits new creation and retained metadata, without invalidating existing roots.
No test case is removed to obtain a pass; failing diagnostics and harness contract
corrections are retained. LOC alone has not rejected any tranche.

Next: close A3's actual remaining gates, then A4, B1–B8 and useful expansion.
At an actual runtime/usage stop or campaign end, update this file with the exact
stop reason and final decisions; do not claim queued/draft work complete.
