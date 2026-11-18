# Z4-C1 evidence and harnesses

Production code: `zeros3.go` sections 16e (authority) and 16f (extracted `dcore` windowed delta); tests in `zeros3_authority_test.go`, `zeros3_b2_economics_test.go`. Report: `../../results/Z4_C1_AUTHORITY_PRODUCTION.md`.

| Script | Gate |
|---|---|
| `scripts/validate.sh authority` | G1: every non-authority root test under the retire-everything hook (`ZEROS3_G1_RETIRE_ALL=1`), the authority suite natively; tests of the CDC layout itself call `g1CDCSpecific(t, class)` and run with CDC authoritative, their authority-mode equivalents are `TestAuthorityEquiv*` and the C1 equivalents. `g1_ledger.py OUT.json` regenerates `results/g1_ledger.json` from the sources; `results/g1_ledger_c1_hook_run.json` is the failing hook run this replaced |
| `go test -run 'TestAuthorityRetireEverythingSubprocess\|TestAuthorityRetiredEquivalents' .` | G1 physical retirement through the real binary, and the logical guarantees after it |
| `go test -run TestAuthorityRepairCrashQualification .` | G8 repair under SIGKILL, restarts, concurrent maintenance |
| `ZEROS3_C1_MODELS=DIR go test -run TestAuthorityZN7RealModels .` | ZN7 on Pythia-14M and SmolLM2 (f16, imatrix, Q4_0) in fresh processes |
| `ZEROS3_C1_LARGE_DIR=D ZEROS3_C1_PROF_OUT=O go test -run TestAuthorityLargeProfile .` | G5 heap/RSS sampling and pprof of a migration |
| `ZEROS3_B2_CORPUS=DIR [ZEROS3_ECON_RAW=1] go test -run 'TestB2RepBreakdown' .` | component-by-component physical account with fresh-process reconstruction (`ZEROS3_ECON_RAW=1`: raw packs on both sides) |
| `qualify.sh OUT [SCRATCH]` | the whole final qualification, one row per step |
| `ZEROS3_PL_CUTS=300 go test -run TestAuthorityPersistenceLossModel .` | G2 persistence-loss model over real `strace -y` traces (migrate, gc, repack, revert-all) |
| `go test -run 'TestAuthorityCrashMatrix\|TestAuthorityRepeatedCrashLoops' .` | G2 SIGKILL matrix, G8 crash loops |
| `run_scale.sh DIR [OBJECTS]` (`TINY=900000`) | G3, 1.1M objects / 1.01M chunks |
| `run_large.sh DIR [BYTES]` | G5, 1 GiB object history, fresh-process RSS (`rss.py`) |
| `ZEROS3_C1_MATRIX=1 go test -run TestAuthorityFiveGenerationMatrix .` | G4, five historical binaries |
| `ZEROS3_B2_CORPUS=DIR ZEROS3_B2_VARIANTS=... go test -run 'TestB2Economics/<family>$' .` | G9 physical economics (corpus: `p0/scripts/fetch_corpus.sh`, Z3-31 chain from `z3/oracle/parquet/gen_pyarrow.py`) |
| `srcmap.py ../../../zeros3.go` | regenerate the source map (check with `harness/toc_check`) |
| `p7/scripts/extract_all.sh OUT` | P7 numeric/hard-codec extraction onto the current tree (nothing is written to the repository) |

`results/`: `economics.json` (all families, physical bytes / CDC), `scale_1m_*`, `large_*`, `g1_retired_chunk_paths.txt` (distinct production call chains that touched a retired chunk), `p7_*`.
