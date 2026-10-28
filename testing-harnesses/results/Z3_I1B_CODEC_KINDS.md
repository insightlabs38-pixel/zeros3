# Z3-I1B — reserved Gzip/Zstd production codecs

Status: IN PROGRESS; base `4f97c7beeaad542db854a54aa874f87a6dc6d5ff`.
No merge eligibility is claimed before the complete integration gates pass.
Kinds 6/7 retain the I0 reservations; ZS3R/ZS3T and default off are unchanged.

## Port provenance
The streaming DEFLATE/IR and Zstd code is mechanically extracted from
`testing-harnesses/z3/{deflate,ir,zstdz3}`. The type-aware extraction tool is
`z3/cmd/prodcodecport`: select reachable declarations, retain receiver methods
for interface dispatch, prefix package globals, flatten package qualifiers.
This avoids algorithm rewrites and excludes unrelated lab entry points.
Decoder adapters are shell-extracted from `repz3/kinds/{gzip,zstd}.go` plus the
shared readTable helper, then rename only Call/source/error boundaries.
The standalone extraction and production decoder compile.

## Fidelity boundary
Lab representations already use Z3RP v2; production already uses ZS3R v1.
Full-envelope equality would require changing one of those contracts, forbidden
here. `z3/cmd/i1b-fixtures` instead keeps codec recipes, tables, units and stored
components byte-identical and translates only magic/version and derived-child
addresses. The unmodified lab builders first prove their original bundles.
Production parses/reserializes the translated envelopes and reconstructs exactly,
checking size, SHA-256, bytes, and that every read belongs to reachability.
Seven fixtures: three Go gzip producer modes (SYNTHETIC_FIXTURE) and four pinned
lab Zstd fixtures (REFERENCE_FIXTURE); no natural-history claim.
No source tests are removed. The reserved-kind rejection test still rejects Git 4,
since reserved 6/7 are now deliberately implemented.

## Remaining gates/work
Bounded streaming analyzers and temp-backed plaintext staging; cumulative budgets;
gzip/zstd→tar→SQLite/CAR; economic sanity; fallback/hostile inputs; nested liveness;
semantic ranges and amplification; crash/fuzz/full/race/compatibility; LOC audit.
First-sight storage remains additive while CDC is authoritative. No planner state.
