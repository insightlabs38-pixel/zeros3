# B7 pre-registered families and criteria

Fixed before any comparative B7 measurement (this file's first commit precedes every result row).

## Families (all deterministic from the B6 corpus plus `make_inputs.py`)
| family | contents | role |
|---|---|---|
| cu-files | Coreutils 9.5-9.7 src/ lib/ members >= 4 KiB (B6) | many small related files |
| go-files | go/src/cmd/compile members >= 4 KiB, 1.22.0-1.23.4 | many small related files, generated code |
| vendor | 6 products x 2 releases (vendored lib/ + own src/ sixth) | partial overlap, vendored, complementary bases |
| bundle | B6 vendored bundles a/b/c | partial overlap (B6) |
| coreutils, go, sqlite | B6 large histories (60 / 15 / 32 MB objects) | large objects, index density |
| tensors | float32 checkpoint history, retrained/perturbed/shifted tensors | numerical arrays, dense edits, shifts |
| git, ts, random, unrelated | compressed, random, unrelated mix | negative controls / realistic no-help |

## Configurations
1. `b6`: B6 sketch top-4 (top-2 windowed) candidates, B6 refinement, B6 optimizer.
2. `b7-single`: B7 retrieval, single-base candidates only.
3. `b7-multi`: B7 retrieval with complementary multi-base candidates.
4. `b7`: the combined production default.
5. ablations: oracle candidates (exhaustive pairwise; upper bound), dense large-object index.

## Criteria
* Discovery: recall@K (K = 1, 2, 4, 8) of the oracle's best single base; benefit recall = the share of the
  oracle's estimated single-base saving (over standalone compression) captured by the top-K shortlist;
  multi-base marginal saving over the best single base; index bytes, build and query CPU.
* Materialized: allocated bytes after optimize + gc + repack + compact, archive and mixed profiles, vs `b6`
  on the same store; paired cold 4 KiB / 64 KiB / whole reads where bytes change.
* "Meaningful": >= 2% fewer allocated bytes on a family (bytes are deterministic), with sparse reads not
  worse beyond the B6 +/-15% noise band under mixed; a family whose bytes change < 0.5% is "no help".
* EFFECTIVE needs meaningful wins on >= 3 independent families (not counting two variants of one source);
  SPECIALIZED: 1-2 families; NO_GENERAL_WIN: none. Negative controls must not change by > 0.5% or gain
  candidates that cost more than 5% extra planning CPU.
