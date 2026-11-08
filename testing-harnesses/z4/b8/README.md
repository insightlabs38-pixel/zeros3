# Z4-B8 audit evidence (research only; no production file touched)
Report: `../../results/Z4_B8_CONVERGENCE.md`. All reruns used pinned inputs (sha256-checked) and the producers named in the B3-B6 READMEs
(clang 18.1.3, pyarrow 26.0.1, rocksdict 0.3.29 / RocksDB 9.8.4, lz4 4.4.5, python-brotli). Scratch trees were outside the repo.

| File | What it is |
|---|---|
| `b7_protocol_audit.py` / `.txt` | Applies B7's registered promotion rule to `../b7/rows.json.gz` for `combined`, `auto` and others; fold-drop and cluster-bootstrap sensitivity. Run from repo root: `python3 -I testing-harnesses/z4/b8/b7_protocol_audit.py` |
| `b7_perturb_series.py`, `b7_perturb_analyze.py`, `b7_perturbation.txt` | Loader-valid perturbations of `../b7/series.json` (noise, heavy tail, bias, first-sight inversion), rerun through `z3/cmd/z4b7 -mode eval -seeds 1 -scenarios S1,S2`, and ranking summaries |
| `economics.py` / `.txt` | Common cost model: per-class ratios with provenance tags, hypothetical mixes, break-even share, encode-CPU break-even |
| `zeros3_b8_physical_test.go.txt` | Scratch Go test (copy next to the B2 draft tree) measuring real packed bytes under production CDC, production additive reps and B2 authority; `physical_smollm2.jsonl`, `physical_*.txt` are its outputs |
| `b2_authority_tests.txt`, `b2_econ_rerun_*.jsonl` | `TestXDelta|TestAuthority` on a trial merge of #97 into main; physical economics reruns (CLR x5, DEX x3 `classes.dex`, Parquet x8) |
| `b3_smol_repro.txt`, `b4_repro.txt`, `b5_repro.txt`, `b6_repro.txt` | Independent reruns of B3 (SmolLM2 5 files), B4 (SQLite O2 x6, PDB x5), B5 (Pythia-14M subset, F16 check), B6 (Parquet/RocksDB chains, Brotli parity) |
| `b5_narrowing_check.py`, `parquet_brotli_parity.py` | Small independent probes (outputs inside `b5_repro.txt`, `b6_repro.txt`) |
| `z3_tests.txt` | `go test` of the Z3 research modules on main |

Trial merge: `git worktree add W origin/main && cd W && git merge --no-commit origin/z4/representation-authority` (clean), then
`go build`, `go vet`, `go test -run 'TestXDelta|TestAuthority' .`. B2 economics: `ZEROS3_B2_CORPUS=DIR ZEROS3_B2_OUT=out.jsonl go test -run 'TestB2Economics/<family>' .`
(DEX family = `dex/<ver>/classes.dex` only; Parquet = `parquet/c*.parquet` copied from the Z3-31 pyarrow chain).
