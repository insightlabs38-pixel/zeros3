Q8 research evidence; no production or persistent-format changes.
frontiers.json.gz: all162 input SHA/size, exact proof or explicit CDC fallback,
serialized reachable closure costs (+64B/object), grouped by retained history.
summary.json.gz: final cumulative costs and every fallback reason.
provenance.json.gz: native producer metadata, source operation hashes, pinned
Apache URLs/hashes and isolated newer-Pebble module lockfiles.
Audits retain before/after byte-weighted feature coverage; unknown compressed
indexes never imply zero unsupported-codec share. SST runtime output covers all
six forms; Parquet output covers every exact or fallback case. Costs retain CDC;
reference files and balanced codec controls are not natural history/usage shares.

Reproduce using pinned compiler and producer-only dependencies outside Git:
- q8_prepare_sst.py Q6_OPS OPS mechanically copies existing operation logs/plans.
- oracle/sst/gen_sst.py with rocksdict0.3.29 and native Pebble1.1.5 producer.
- q8_new_pebble.py GO WORK adapts the same producer to pinned Pebble2.1.7.
- q8_fetch_reference.py CACHE/ref fetches30 commit-pinned reference files, bounded.
- oracle/parquet/gen_pyarrow.py chain duckdb controls with PyArrow18.1.0/DuckDB2.0.0.
- cmd/z3sst audit/rt, cmd/z3parquet audit/rt and cmd/z3breadth prove the corpus.
- q8_summarize.py ROOT WORK OUT validates every file's hash and archives evidence.
No fixtures, tests or reference cases were removed to obtain a pass.
