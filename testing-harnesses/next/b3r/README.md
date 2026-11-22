# B3-R: correctness recovery and completion of the compact namespace

Linux amd64, Go 1.27.0, 4 vCPU. Old = B3 head `a9a7147`. Raw rows and probe sources (`*.txt`)
are beside this file. The V4 architecture is unchanged; its failure behavior and integration are.

## Correctness
* Live roots stream through `nsEachRoot`, which returns the first page, row, snapshot or
  manifest error. Authority revert, revert-all, migration, repair, the virtual chunk index and
  the representation live set propagate it; the best-effort snapshot helpers are gone. Revert-all
  over an unreadable late object page, or the history page holding a body's only root, now fails
  before retiring anything (the old code retired every record).
* `histPrepare` resolves the history row of a put, multipart completion, delete or prune before
  its journal frame (batch-local for grouped commits on one key); only infallible application
  follows the fsync. With a damaged history page every path rejects without appending a frame,
  and concurrent grouped commits are durable exactly when acknowledged.
* V4 pages are validated on load (framing, restarts, row count, key order and page bounds);
  open also checks every page, payload and row/bucket count beside the digest, so a
  well-checksummed but malformed file is replaced by replay. Two focused fuzz targets ran clean.
* The serial commit and live prune held `mu` without `defer`, which is why B3 dropped the lock
  from `Close`. Both defer now; `Close` locks, starts no new consolidation and aborts a running
  write (the overlay is restored). Online publication was cut at six boundaries by process exit.

## Integration
* Ordinary and shared opens load a verified checkpoint and replay the suffix; owners publish (a
  server start folds a suffix of 16,384+ rows), `ZEROS3_NAMESPACE_CHECKPOINT=off` opts out.
* Listings share overlay blocks copy-on-write instead of copying the matching range, and seek
  past a delimiter group, resuming after the common prefix. `-compact-history` now warns.

| 150k-put packed store (181k rows, 74 MB journal), median of 5 | B3 | B3-R |
|---|---:|---:|
| ordinary `OpenStore`: ms / Go bytes allocated | 534 / 213 MB | 166 / 88 MB |
| live heap / peak RSS after open | 71.7 / 174 MB | 18.8 / 98 MB |
| 100-unit delimiter page / cold point lookup | 57 ms / 1.4 µs | 0.36 ms / 9 µs |
| 10-key page over a 100k-row mutable overlay | 4.9 MB, 2.5 ms | 1.3 KB, 4.5 µs |

## Similarity index
`authSketchAt` zeroed a fresh 1 MiB buffer per content (181 GB allocated for 171k contents).
Pooled: index-build 32.9 s / 180 MB RSS -> 5.7 s / 98 MB, publish 163 -> 90 MB, identical
catalog and runs. Build RSS grows ~0.44 KB per content (0.69 before). The C2 1.46 GiB peak (a
million-entry publish holding the B2 map namespace) was not re-measured: no million-body corpus
was rebuilt, and ~0.5 GB is only a linear extrapolation.

## Pricing and zstd reach
* `physChunk` now applies the pack writer's exact deflate rule, also to raw records under 512 B,
  which B3 priced raw (a 400 B chunk: 496 priced, 110 stored). Per-chunk equality is tested for
  text, structured, model-like, random and tiny data in every storage state. Thresholds are
  unchanged; the TypeScript cohort probe needs external tarballs and was not rerun.
* Reach was added to zstd recipes under the existing `cxseek` feature, so B2 builds open such
  stores and fail on reads. Authority bodies with reach now record `cxreach`, refused at open by
  older builds; stores written by B3 itself are not retro-marked.
* The separate reach replay costs ~25% of zstd construction (150 of ~600 ms); not folded into the
  proof decoder.
