# A4 runtime recovery ledger — 2026-10-29

Execution is blocked: exec transport disconnected and subsequent commands report
"No usable execution environment is available." The status endpoint's running
observation did not establish shell availability. No disk/OOM cause is established.

This unmerged branch preserves captured evidence and this ledger, NOT a snapshot
of the latest uncommitted source. Last pushed source checkpoint:
450bd56622417dd0793bc1e42c7c9ec861f9de32 on z4/a4-managed-streaming, PR #95 DRAFT.
Main inspected at e4e5b1bb21fa7abbb2e3512545ef3d5540ff264b.
PRs #80/#81 remain drafts. A0–A3 are completed; A4 is first unfinished.
B1–B8 and useful expansion remain queued; B2 stays DRAFT_ONLY.

## Work present locally after the checkpoint, not remotely backed up

- Mechanical managed production extraction into zeros3.go: kinds 11/12, cumulative
  floor 9, default-off creation, bounded source/staged references, independent proof.
- Frozen wire/root identity, lifecycle and fallback tests; future-version test
  boundary updates and STATUS.md capability documentation.
- Extraction, real CDC-manifest production benchmark, compatibility/refusal scripts
  and immutable managed fixtures/provenance.
- CLR separate metadata/body/layout caches and DEX parameter cache: fixed actual
  production inspection-budget exhaustion without raising the 1 GiB budget.
- DecodeFrom two-pass component getters remove retention of all input components.
  Root focused tests passed after getter extraction.
- Unique class/handle index fast path passed library differential tests; it has
  NOT yet been re-extracted into production or measured there.
- Cleanup inventory: 64 reproducible A3 refusal clones removed, 3,466,877,760 bytes.
  Source stores, inputs, scripts and logs retained; last free disk about 5.4 GiB.

## Captured evidence and limits

captured-production-summary.json records 21 exact native production samples from
seven inputs, three fresh processes/stores each. Source SHA-256:
08e72b3da77878d3856a1b7de97316a439b5a6a6e2d5306e28e9a925952ff7ac.
This precedes the getter/unique-index refinements. Roots were deterministic and
cold full-SHA proof/publication used floor 9. CDC remains authoritative; growth
is additive. Roslyn creation RSS was about 195 MiB; final memory gate is unresolved.

Earlier production full suite passed in 194.596 seconds, BEFORE those later
refinements. Initial stale future-kind/version assertions failed and were corrected
to the next unsupported values; no tests were dropped. Negative logs retained.
Latest library index/CLR/DEX suites passed (31.521 / 2.593 / 5.601 seconds).
These observations do not establish final production readiness.

## Resume in the existing workspace, without restarting completed work

1. Check for an active worker; fetch origin and inspect tree/PR95 before edits.
2. Inspect uncommitted source, tests, scripts and logs. Do not overwrite local work.
3. Finish exact pycache cleanup via bounded Python unlink/rmdir if still needed.
   Check owned handle copy is handles[:len(ids)] on the unique-index fast path.
4. Re-run a4_extract_managed.py to port the latest library declarations, then gofmt.
   Preserve hand-written builder/adapters and mechanically reused legacy framing.
5. Run frozen-root/lifecycle and library differential tests and relevant races.
6. Rebuild the production probe, measure Roslyn/DEX memory first, then freeze source
   and repeat all seven inputs x three fresh processes with full/cold proof.
7. Complete native revision compatibility/snapshot matrix, five old generations'
   floor-9 refusal evidence, ordinary PUT crash and real SIGKILL matrices.
8. Run final full suite, relevant race/vet/fuzz gates; preserve failures.
9. Update A4 report and required campaign summary, genuine checkpoint commit/push,
   and review every campaign merge gate before changing PR95 readiness.
10. Continue B1–B8 and useful expansion continuously; B2 is never auto-merged.

Raw work paths: /workspace/work/z4-a4 (diagnostics), z4-a4-final (first fallback),
z4-a4-io-fixed (21 successful samples). z4-a4-memory-final was not created:
the attempted command failed before process creation. Latest source changes cannot
be committed or pushed while the execution environment is inaccessible.
