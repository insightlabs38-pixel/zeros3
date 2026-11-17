# Z4-C1 evidence and harnesses

Production code: `zeros3.go` sections 16e (authority) and 16f (extracted `dcore` windowed delta); tests in `zeros3_authority_test.go`, `zeros3_b2_economics_test.go`. Report: `../../results/Z4_C1_AUTHORITY_PRODUCTION.md`.

| Script | Gate |
|---|---|
| `ZEROS3_G1_RETIRE_ALL=1 [ZEROS3_G1_STACKS=out.txt] go test -skip TestAuthority .` | G1 retire-everything differential (hook + retired-chunk seam) |
| `ZEROS3_PL_CUTS=300 go test -run TestAuthorityPersistenceLossModel .` | G2 persistence-loss model over real `strace -y` traces (migrate, gc, repack, revert-all) |
| `go test -run 'TestAuthorityCrashMatrix\|TestAuthorityRepeatedCrashLoops' .` | G2 SIGKILL matrix, G8 crash loops |
| `run_scale.sh DIR [OBJECTS]` (`TINY=900000`) | G3, 1.1M objects / 1.01M chunks |
| `run_large.sh DIR [BYTES]` | G5, 1 GiB object history, fresh-process RSS (`rss.py`) |
| `ZEROS3_C1_MATRIX=1 go test -run TestAuthorityFiveGenerationMatrix .` | G4, five historical binaries |
| `ZEROS3_B2_CORPUS=DIR ZEROS3_B2_VARIANTS=... go test -run 'TestB2Economics/<family>$' .` | G9 physical economics (corpus: `p0/scripts/fetch_corpus.sh`, Z3-31 chain from `z3/oracle/parquet/gen_pyarrow.py`) |
| `srcmap.py ../../../zeros3.go` | regenerate the source map (check with `harness/toc_check`) |
| `p7/scripts/extract_all.sh OUT` | P7 numeric/hard-codec extraction onto the current tree (nothing is written to the repository) |

`results/`: `economics.json` (all families, physical bytes / CDC), `scale_1m_*`, `large_*`, `g1_retired_chunk_paths.txt` (distinct production call chains that touched a retired chunk), `p7_*`.
