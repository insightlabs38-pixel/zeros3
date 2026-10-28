# ZeroS3 surplus campaign checkpoint

Campaign base and current main: `b050175029f8a484db1e97c6678c863b4d4984dd`.
Merged campaign PRs: none. Draft: #72, Z3-I1A runtime hardening.
Branch: `z3/i1a-representation-runtime`.
Checkpoints: `20519bb` characterization; `6adfc67` bounded root loading and
closure allocation; final healthy-set-sharing/evidence checkpoint in this branch.
The campaign is unfinished; do not infer completion from this checkpoint.

## Delivered and measured
- Root reads reject oversized on-disk files before allocation; sparse corrupt
  root regression preserves fail-closed maintenance and manifest availability.
- Remove duplicate length-map storage, share healthy read-only liveness sets,
  and size closure allocations from the validated graph.
- Full-scan benchmark allocation down 26.3%; no proven deep-verification RSS win.
- 1 GiB exact whole/range/deep verification passes (266,240 components).
- Compatibility, bundle/replication, lifecycle and crash black-box checks pass.
- Final vet, formatting, targeted race and read matrix pass. Final full suite
  fails two replication interruption/reuse tests; interruption reproduces 3/3
  on unchanged main, reuse passes 3/3 in isolation. No tests changed/skipped.
- Wire bytes, production kind numbers, store level and default mode unchanged.
- No research promotions/kills, new integration kinds or persistent decisions.

## Remaining work and resume
Q1 is active; PR #72 stays draft. Main has not advanced. Resume the existing
branch and inspect its latest report and validation state before doing new work.
Next priorities: peak verification memory (211 MiB baseline / 215 MiB final),
whole-read overhead (full matrix in Z3_I1A_REP_RUNTIME.md), descriptor streaming,
and root creation on sync/native replication/bundle import.
Sync/native replication share handleSyncCommit. Full/delta bundle publication
shares bundleImporter.publish; the CLI currently lacks destination representation
mode configuration. Preserve CDC success and snapshot publication semantics.
Do not start Q2 until Q1 is complete or its remaining work is explicitly resolved.
New format kinds and ZS3R/ZS3T changes remain unauthorized for unattended merge.

## Continuation task
An enabled hourly task was created in this conversation at the user's request.
Treat a wake while already active as a reminder, never a second worker.
The uploaded campaign file remains the queue/gate authority; this checkpoint is
only a progress record. No branches were deleted and no history was rewritten.
