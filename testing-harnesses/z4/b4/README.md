# Z4-B4 reproduction (research only; no production change)
Code: `z3/graphnorm` (library + tests), `z3/cmd/z4b4` (driver). Go 1.27 toolchain of the z3 module.
1. `z4/b4/build_corpus.sh WORK` — downloads the six pinned SQLite amalgamations (SHA-256 checked) and the five
   Newtonsoft.Json symbol packages, compiles O2/O0 bitcode with the local clang (**18.1.3 here**, `-g0 -fno-ident`,
   `x86_64-unknown-linux-gnu`; a different clang changes bytes, not the method), builds the controlled C histories
   (`gen_controlled.py`) and their wasm32 counterparts.
2. `python3 scripts/q6_fetch_frontier_inputs.py WORK/q6 z3` (from `testing-harnesses`) fetches the pinned F-Droid DEX and
   Newtonsoft DLL inputs used by B1.
3. `z4/b4/run_evidence.sh WORK` — runs every table in the report (`evidence.txt.gz` is a captured run).
4. Exactness: `z4b4 llvm-store DIR files…` / `prog-store pdb|dex DIR files…`, then in a **fresh process**
   `z4b4 llvm-replay DIR OUT` / `prog-replay DIR OUT`, then `python3 -I verify_replay.py DIR OUT ORIGINALS`.
5. Tests: `cd z3 && go test ./graphnorm` (fixtures in `graphnorm/testdata`); with `B4_CORPUS=WORK` the real-graph
   matching, line-oracle and DEX-column tests also run; `B4_A/B4_B=<file names>` enables the stability diagnostics.
   Fuzz: `go test ./graphnorm -run '^$' -fuzz '^FuzzVM$' -fuzztime 10s` (also FuzzBitstream, FuzzRecipe, FuzzPDBandDEX).
Inputs: SQLite 3.45.1 (3450100), 3.45.2, 3.45.3, 3.46.0, 3.46.1, 3.49.1 (sqlite.org); Newtonsoft.Json 12.0.3, 13.0.1–13.0.4
(nuget.org symbol packages, member `lib/netstandard2.0/Newtonsoft.Json.pdb`); F-Droid 2.0.0–2.0.2 DEX and the same
Newtonsoft DLLs as B1. Class: SQLite bitcode = real release *source* compiled locally; PDB/DEX/CLR = real published
artifacts; controlled C/wasm = synthetic producer workload.
