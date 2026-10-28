# Z4 A3 SST/Parquet production — active checkpoint

A3 active, not READY. Base main 90eed88 (A2 #92); original #81 stays a draft.
Mechanically extracted the immutable #81 appended codec/parser/analyzer tranche
onto cumulative Model-capable main. Existing Zstd is reused, not copied again.
Ceiling/floor eight is allocated only to supported SST-9/Parquet-10 publication;
Model remains readable at seven, older kinds at six. Default creation stays off.

Copied all nine frozen structured vectors, encoder identity checks, fallback/
lifecycle and preallocation/prefix-expansion/Zstd-window guards unchanged except
fixture paths. They pass on the cumulative candidate (1.243 s). No LOC veto.

Preselected latency cohort: cached native RocksDB none/Snappy/Zstd, DuckDB Snappy/
Zstd, exact 32/256 MiB PyArrow random-INT32 controls, and cached exact 1 GiB control.
The 32/256 controls mechanically reuse the Q10 native producer; the existing GiB
file is not regenerated. These controls test memory/latency, not dedup economics.
Parse, semantic planning, component construction/CAS, proof and publication need
separate timings before admission policy is decided. Private instrumentation will
retain uninstrumented production-source identity and its own patch provenance.

Pending: cumulative actual-binary old/new/Model matrix; unsupported cases retained;
latency/admission decision; malformed recipes/fuzz/race; exact ranges; full suite;
READY or clean bounded PARTIAL verdict. No production merge claimed yet.
