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

Second checkpoint: cumulative Model/SST/Parquet restart proof and ten ordinary-PUT
crash gates pass. Real native revisions pass wire/maintenance matrices; an unchanged
RocksDB carry-forward file is retained as a control alongside a distinct new emission.
Four older local generations make 63 genuine floor-eight refusals; one pre-I0 CLI
flag is unavailable, not counted as safe refusal. Portable full/delta bytes remain exact.

Review found a malformed Parquet recipe whose sixteen 2^60 stream counts wrapped
to zero. A preserved failing regression now rejects through checked uint64 addition.
All structured tests pass after repair (4.177 s). Pre-hardening full suite passed
(260.207 s); final hardened-source validation and latency decision remain pending.
All 162 original native regression inputs were recovered by SHA before rerun;
no old accepted/fallback case is dropped. Original draft #81 is not promoted wholesale.

Latency decision in progress: the first isolated cohort shows 16.487 s optional
encode for native 1 GiB Parquet, while DuckDB Zstd spends ~6.086 s on 21.85 MiB.
Metadata parsing is negligible; semantic planning and component/revalidation passes
account for the work. Admission now caps decoded Zstd work at 4 MiB /512 frames SST and 128 MiB /1024 pages
Parquet, retaining the 1 GiB overall cap. Over-budget native controls already pass
successful streaming PUT, exact full/range reads, zero roots and no floor-eight bump.
Additional ~32/256 MiB native SST controls are admitted and exact.

Final review adds cumulative retained metadata guards before growing SST command/
block recipes and Parquet headers/layouts. Existing-root decoding is unchanged.
Boundary tests pass; final-source regression, full suite, compatibility and isolated
measurements are running. Verdict remains pending; no merge/READY claim yet.

A contended uncapped diagnostic on the native 32 MiB-valued SST spent 84.6 s in
encoding with high transient heap, motivating the tighter frame/byte admission.
It and the oversized Parquet remain exact existing roots under the new decoder.
Short Parquet files exposed a harness-generated malformed negative-start range;
the existing contract ignores malformed ranges and serves the full object. The
input and malformed request are retained, the intended positive range is clamped,
and the oracle now checks the frozen full-object contract. Corrected matrix passes.
Metadata layout prefixes are detached from whole decoded-page backing arrays
before retention. This source is frozen for final full/native/race/latency gates.
