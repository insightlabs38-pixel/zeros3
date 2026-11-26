# Native feasibility follow-up

The original residual frames paid for full-frame hashes in the lab.
The next experiment tests an independent question. Can a bounded
contiguous-edit matcher regenerate the existing native XD program during
optimizer apply, with the exact incumbent program and native sparse reader?

Compare `authority optimize -apply` with and without the flag on copied
seeds, especially shift histories, SQLite, the volume, events, decoded
TypeScript and held-out HTTPX. The fast path must produce the original
priced envelope SHA-256. Otherwise the normal matcher handles the apply.
Measure hits, elapsed, CPU, RSS, applied options, finalized allocation
and exact data. Include an independently generated 24-revision mixed-edit
history. Keep the 32 KiB edit and single-base bounds pre-registered for
this trial. Planning remains matched, so distinguish apply-phase gain from
whole-optimizer gain.

The original XOR and packed-stream evidence remains intact. The new route
uses the current XD envelope and authority checks, so it isolates the
encoder CPU question from the experimental residual read penalty.

The native variant also tries retaining priced raw and windowed envelopes
for apply. The memory cap is 4 MiB of envelope payload, with a 256 KiB
per-envelope limit and 4,096 entries. The retained envelope must match
the chosen SHA and piece boundaries. Normal proof and publication still
run. Include windowed sparse mutations and a 20-revision pressure case.

## Result

The flagged small-edit encoder reproduced both selected native deltas in
the shifted history. Median apply was 120 → 53 ms in the initial trials.
It reproduced nine of the 17 selected deltas in the 24-revision mixed
history, reducing apply from 1.16 → 0.62 s. SQLite, volume, event, tar and
held-out HTTPX selected no qualifying contiguous edit. The result depends
on the edit shape. Exact output verification permits deployment without
the prototype's framed range-read cost.

Envelope retention reused every selected delta on the three-revision
SQLite, sparse volume, event and decoded TypeScript histories and the
held-out HTTPX pair. Median full optimizer CPU fell from 3.10 → 2.76 s
on decoded TypeScript and 1.02 → 0.83 s on the volume. An untouched
entropy history selected no delta. A windowed insertion that CDC
already handled also selected no delta, despite eligible cache entries.

When candidate programs hit the memory cap, retain one smallest envelope
per target and resume filling from later targets. The solver still uses
every priced option. The original first-come policy reused five of 19
selected plans. The revised policy reused 18, holding 3,934,440 bytes.
These are two matched pairs per workload:

| Follow-up workload | Apply seconds control → retained | Full optimizer CPU | Final allocation |
| --- | ---: | ---: | ---: |
| 20-revision budget pressure | 1.526 → 0.253 | 9.816 → 8.538 s | 6,000,640 B |
| 33 MiB windowed sparse edit | 0.673 → 0.097 | 3.308 → 2.435 s | 34,848,768 B |
| 24-revision mixed edits | 1.169 → 0.096 | 9.282 → 8.384 s | 2,588,672 B |

Focused tests covered cap and replacement, matched XD bytes, exact
reopen/reclamation, native closure transfer and the graph incumbent.
Every measured store preserved plans and digests through finalization.
The policies write the existing native case and still invoke its authority
proof and publication. Planning remains unchanged. The raw follow-up
records include actual process RSS, native output writes and CPU.

Run `fast_reencode.py --help` with the final binary and the frozen C12
corpus. Use `--families budget-stress windowed-churn mixed-edits
--lanes control retained` for the final pressure comparison. The raw
admission-first data is preserved alongside the pressure redesign.
