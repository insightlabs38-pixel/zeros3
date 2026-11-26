# Foundational ledger

Status: COMPLETE, PARTIAL, BLOCKED, RESEARCH_ONLY or ALREADY_FIXED. Branch
`research/next`; `main` stays at `2ac9cab`.

| Milestone | SHA | Source paths | Evidence | Status |
|---|---|---|---|---|
| M-A0 qualification (FB-01 part) | be82fc0, d2727f4 | `zeros3.go` (`testJournalFault`, `testJournalPolicy`), `zeros3_fb01_test.go`, `zeros3_fb01_ckpt_test.go`, `zeros3_fb01_fuzz_test.go`, `testing-harnesses/foundational/FB-01/compat.sh` | `FB-01/results/repro_*.jsonl`, `compat.jsonl`, `fuzz_*.txt` | PARTIAL: repro, journal and checkpoint fault seams, two fuzz targets, compat runner done. Open: fuzz in CI (no CI), seams at other fsync sites, other decoders' fuzz targets, corpus manifest, benchmark runner, ListObjectsV2 regression, docs-drift sweep |
| M-A1 journal recovery hardening | d2727f4 | `zeros3.go` (`scanJournalRange`, `openJournalOpts`, `checkpointWitness`, `runJournal`, `salvageJournal`, `Journal.rollback`), `zeros3_fb01_recovery_test.go`, `zeros3_fb01_x14_test.go` | `FB-01/README.md`, `FB-01/results/fb01_d2727f4.jsonl`, `x14_engine.jsonl` | PARTIAL: no silent loss in any tested fixture except the v1-undetectable S6 cut above every checkpoint; S4 loss 0%; refusals name offset, frame and cause; old journals open unchanged. Open: S6 residual and crash-refusal cost (markers and seal, M-C2) |
