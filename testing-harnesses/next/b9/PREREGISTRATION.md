# B9 pre-registration

Fixed before any comparative B9 measurement (base `1e261c2`, B8). Inputs: `make_inputs.py` over published files
(GNU Coreutils 9.5-9.7, Go 1.22.0/1.22.6/1.23.0/1.23.4 source and linux/windows amd64 distributions, npm
TypeScript 5.6.2/5.6.3/5.7.2, PyPI Django 5.1.1-5.1.3); digests in `corpus.sha256`.

## A. Small-file storage economics (grouped records)
Families: `cu-files` (B6's per-file Coreutils history), `go-files` (src/cmd/compile + src/go `.go` files of
4-256 KiB, four releases), `rnd-files` (negative control: unrelated random files), `synth` (scale: 4,000 text
files x 3 versions). Lanes, each from the same CDC store, archive and mixed profiles: `cdc`, `b8` (record
files), `b8m` (B8-G model price, planned only), `b9` (grouped records, materialized). Every store is reclaimed
(gc, repack, compact, gc) and measured by allocated blocks; deep verify must pass.
Criteria: the B8-G model's gain *survives* on a family if `b9` realizes at least 75% of the `b8m` reduction
from `b8`; `rnd-files` must not grow by more than 1%; plan bytes must equal the materialized model; cold record
lookup p50 must stay within 2x of the record-file lookup.

## B. Reversible executable normalization
Families: Go linux/amd64 ELF (`compile`, `link`, `go`, `vet`, `asm`, `cgo`) and the windows/amd64 PE `.exe` of
the same tools, four releases each, one key per tool. Lanes (archive): `cdc`, `b8` (current optimizer, raw
deltas), `b9n` (b8 plus normalized executable candidates). Measured: allocated bytes, components, cold 4 KiB /
64 KiB / 1 MiB / whole reads, CPU. Criteria: normalization is *effective* on a format if `b9n` stores at least
5% fewer bytes than `b8` on that format with cold 4 KiB reads at most 2x `b8`'s; otherwise specialized or rejected.

## C. Verified decoded-window cache
Stores: B8 canonical archive stores of TypeScript and Django; negative control: the Coreutils per-file `b9`
store (no canonical stream). Workloads: cold first touch, repeated identical ranges, nearby ranges, several
objects sharing a stream, 8 concurrent readers, unrelated non-repeating reads, cache-capacity sweep (0, 16,
64, 256 MiB). Cache off vs on at matched concurrency, two orders. Reported: hit rate, work avoided, memory,
CPU, p50/p95/p99, throughput. Criterion: integrate if repeated/nearby workloads gain at least 2x throughput
with no cold-read regression above 10% and no change in what is stored.
