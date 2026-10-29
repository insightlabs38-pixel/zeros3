# Z4 Frontier Campaign — runtime interruption checkpoint

Status: **ACTIVE, unfinished**. Runtime connection recovered; execution resumed.
Fetch, Go toolchain, race smoke and repository checks succeeded. Earlier transport failure
had no recorded OOM kill; its exact cause is unconfirmed. Do not restart completed work.
Production main remains e4e5b1b (A3 merged at a19ace9; #94 preserves interruption evidence).
Active branch z4/a4-managed-streaming. First checkpoint aa5d7d4 fixes exact index lookup;
second checkpoint 9551dff stages reference bytes; the next checkpoint adds bounded CLR
source windows, external sorting and four real-size exactness/memory evidence.
Original draft PRs #80/#81 remain open; neither was merged wholesale.

| Item | Durable outcome | PR / state |
|---|---|---|
| A0 | PRODUCTION_TRANCHE_NEEDS_REWORK; isolated audit and old-retirement evidence | #90 merged |
| A1 | cumulative store-format capability design; old/new/wire/crash matrix | #91 merged |
| A2 | MODEL8_PRODUCTION_READY; bounded Model-only extraction | #92 merged |
| A3 | STRUCTURED9_10_PARTIAL; final gates passed for bounded subset | #93 merged |
| A4 | first unfinished; exact compact indexes and bounded source-window prototypes; production gates pending | #95 draft; #80 stays draft |
| B1 | similarity/delta CAS decision pending | queued |
| B2 | representation-authority decision pending; **DRAFT_ONLY** | queued |
| B3 | cross-format semantic CAS decision pending | queued |
| B4 | graph normalization / restricted reversible VM decision pending | queued |
| B5 | Model delta 2.0 decision pending | queued |
| B6 | hard-codec consumer ranking/deepening pending | queued |
| B7 | planner decision under new economics pending | queued |
| B8 | convergence / production decision pending | queued |
| Expansion | useful pool remains authorized after B8 | not started |

Current production kinds are 1–10. Model kind 8 publication requires store floor 7;
kinds 9/10 require floor 8. Creation defaults off. All earlier decoders remain.
No managed kind 11/12 is merged.
A1 tests show old local binaries refuse newer floors; compatible servers preserve
older wire clients and portable full/delta bundles omit optional graphs/floors.

Strong evidence: A2 exact independent real MiniLM full/range reads, 153.5 MiB/s
optional encoding, and fresh 1 GiB proof at 94.74 MiB maximum process RSS.
A3 preserves all 162 native cases: 66 SST /39 Parquet exact roots with unchanged
identities, 57 explicit fallbacks. PR #93 merged the bounded subset after final gates.
A3 full suite: 196.031 seconds; structured race: 13.112 seconds; vet passed.
Fresh 1 GiB Parquet: 62.51 MiB/s optional encoding, 84.91 MiB maximum RSS.
Final source SHA-256: 34262c8eda1025bfc6110582cdc6dd193a1455578ed7a3fd89693e552b3164ce.
A3 checkpoints: cf00639, 5f74649, 44b616f, 3436876.
Evidence and preserved failures: Z4_A3_STRUCTURED_PRODUCTION.md and its artifacts.

Largest remaining blockers: expensive exact Zstd paths; optional physical growth
while CDC remains authoritative; managed whole-file copies, reference-state memory
and quadratic duplicate lookup; unproven generic similarity and authority/GC design.
A3 limits new creation and retained metadata, without invalidating existing roots.
No test case is removed to obtain a pass; failing diagnostics and harness contract
corrections are retained. LOC alone has not rejected any tranche.

Resume A4 first: replace quadratic managed indexes while preserving exact wire
identities, then eliminate whole-artifact copies and unbounded retained references
using streaming/staged state. Index speed alone does not satisfy the architecture
gate. Require four-size memory/scaling evidence and malformed-input integrity.
Then continue B1–B8 and useful expansion; B2 remains DRAFT_ONLY.
A0–A3 reports and pushed commits are durable; do not restart completed work.
The existing hourly wake is recovery only; no second worker was started.
Runtime is available. A4 production integration is not authorized by these partial results.
If disk becomes critical, remove fully reproducible scratch first. If stopping is unavoidable,
commit/push all changes and preserve valuable artifacts on an unmerged backup branch.
The campaign is not complete; A4, every B item, and expansion remain unfinished.
