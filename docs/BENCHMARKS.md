# ZeroS3 benchmarks

This document records measurements that explain ZeroS3's architectural
tradeoffs. They are **not universal performance guarantees**.

The measurements below were collected during development leading to the first
public release. Relevant comparison commits are recorded where they help
reproduce a result.

Where a result compares two builds, the comparison is meaningful primarily
because both sides used the same fixture/machine/harness. Do not compare raw
numbers from unrelated tables as if they were one benchmark suite.

## Reading the numbers

ZeroS3 benchmarks emphasize several different costs:

- upload throughput;
- peak server RSS;
- physical file count;
- time until data reaches its read-optimized representation;
- full/range read throughput;
- storage bytes;
- delta-transfer request count;
- content reuse after edits;
- store-open/index cost.

Those metrics often trade against one another.

For example, DEFLATE compression can save substantial physical storage while
reducing both compaction speed and read throughput. ZeroS3 therefore does not
present one "best" number independent of workload.

## Current headline: direct pack ingest

### Fixture

Large known-size pseudo-random PutObject, 256 MiB.

Baseline commit: `3df362b` (before direct pack ingest).

Direct-pack implementation commit: `1c25a4c`.

Recorded on the same development machine.

| Metric | Before direct pack ingest | Direct pack ingest |
|---|---:|---:|
| PUT throughput | 61–67 MiB/s | 72–89 MiB/s |
| server RSS growth | +6 MiB | +7 MiB |
| loose payload files | 4,019 | 0 |
| immediate packs | 0 | 4 |

The direct path streams new chunks into ordinary hot pack-v1 files instead of
writing thousands of loose chunk files and compacting them later.

### Time to locality-packed state

The meaningful end-to-end comparison is not only PUT time.

Baseline:

```text
PUT            ~4.2 s
locality compact ~2.9 s
total           ~7.1 s
```

Direct ingest:

```text
PUT to locality-packed state ~2.9 s
```

That is about **59% less time** to the read-optimized packed representation in
the recorded run.

Immediate reads after direct ingest:

| Read | Recorded result |
|---|---:|
| full 256 MiB GET | ~877 MiB/s |
| 16 MiB range | ~811 MiB/s |

No post-PUT compact was required.

### 1 GiB sanity run

Direct-pack build:

| Metric | Result |
|---|---:|
| PUT throughput | 81.6 MiB/s |
| RSS growth | +14 MiB |
| pack count | 16 |
| loose payload files | 0 |
| immediate full GET | 917 MiB/s |

This is a single sanity run, not a distribution.

## Direct-pack compression decision

A compressible-text experiment compared online raw versus adaptive DEFLATE
direct packs.

Recorded:

```text
raw direct pack:       ~57.8 MiB/s PUT
adaptive compression:  ~35.3 MiB/s PUT
```

Adaptive compression saved about 78% of stored bytes in that fixture, but full
read throughput fell from roughly:

```text
~870 MiB/s -> ~170 MiB/s
```

That tradeoff is why direct pack ingest is raw by default.

Compression remains available through offline pack creation/rewrites where the
operator intentionally chooses the storage/CPU tradeoff.

## Content-defined edit reuse

### 4 MiB localized insertion

A 4 MiB object received an approximately 4 KiB insertion near the beginning.

Recorded CDC reuse:

```text
96.6% of original bytes reused
```

The comparison fixture using fixed 64 KiB chunk boundaries reused:

```text
0%
```

because the insertion shifted later fixed boundaries.

### Delta sync edit

An 8 MiB file was synced, modified with a small 4 KiB insertion, and synced
again.

Recorded:

```text
99.0% bytes reused
```

Only affected logical chunks were transferred.

These are fixture-specific demonstrations of CDC re-synchronization, not a
promise that every edit shape produces those exact percentages.

## Pack locality and read coalescing

The locality/coalescing change moved new compacted pack record order from digest-oriented layout to
first-reference locality order and added bounded contiguous run reads.

Fixture:

```text
256 MiB pseudo-random object
64 MiB raw packs
warm page cache
4 vCPU
loopback
medians of three
```

| Metric | Before locality/coalescing | After |
|---|---:|---:|
| adjacent logical chunk pairs physically contiguous | 0% | 99.9% |
| average contiguous run | — | ~1,005 chunks |
| pack opens / full GET | 4,019 | 4 |
| `pread64` / full GET | 4,021 | 68 |
| full GET | 609 MiB/s | 852 MiB/s |
| 16 MiB range | 538 MiB/s | 707 MiB/s |
| 64 MiB range | 566 MiB/s | 833 MiB/s |
| 1 MiB range | 4.7 ms | 4.8 ms |
| random 64 KiB read | 3.1 ms | 3.2 ms |

The small/random-read differences were noise-level. The gain is primarily
sequential/range locality and syscall reduction.

A three-version 128 MiB checkpoint fixture improved:

```text
662 -> 839 MiB/s
2,012 -> 2 pack opens
```

A 400-file site fixture reduced pack opens from:

```text
3,460 -> 401
```

### Cost

Locality-oriented compaction was about 30–45% slower for large loose objects in
the recorded fixture because it read loose files in manifest order rather than
digest/directory order.

Direct pack ingest later removes much of that cost for large known-size new uploads by
writing object-order packs directly.

## Portable full and delta bundles

### Browser/site revision

Recorded target full bundle:

```text
0.89 MiB
```

Delta against prior snapshot:

```text
0.10 MiB
```

Reduction:

```text
88.7%
```

### 128 MiB checkpoint sequence

S1 -> S2:

| Artifact | Size |
|---|---:|
| full S2 bundle | 128.26 MiB |
| S1->S2 delta | 3.67 MiB |

Recorded reduction:

```text
97.1%
```

Chunk bytes reused from base:

```text
97.3%
```

S2 -> S3 delta:

```text
2.52 MiB
98.0% smaller than the full target bundle
```

A 1 KiB insertion in a separate 64 MiB object produced only one payload chunk
in the delta fixture after CDC re-synchronization.

### Import/export resource use

Checkpoint delta runs recorded:

- export around 0.1 s;
- delta import around 0.65 s;
- full S1 import around 2.6 s;
- approximately 16 MiB peak RSS.

The delta planner's sorted-inventory merge for:

```text
1M base descriptors
1M target descriptors
```

recorded about:

```text
21 ms
0 bytes allocated by the merge itself
36 bytes / inventory descriptor
```

The inventories themselves naturally consume memory; the point of the
measurement is that the overlap classification does not add a large per-chunk
map.

## Packed locator scale

The packed-chunk locator uses sorted compact records plus prefix narrowing
rather than a Go map entry per digest.

Synthetic tiny-chunk stores:

| Packed chunks | Open time | Locator heap | Peak RSS |
|---:|---:|---:|---:|
| 1M | 0.52 s | 51 MiB | ~125 MiB in the recorded comparison environment |
| 5M | 2.7 s | ~256 MiB | 539 MiB |

Historical Go-map comparison:

| Packed chunks | Map open | Map heap |
|---:|---:|---:|
| 1M | 0.71 s | 128 MiB |
| 5M | 3.6 s | 513 MiB |

Point lookup stayed in the sub-microsecond range in the synthetic runs.

The locator is reconstructible physical metadata; content correctness is still
checked by logical SHA-256.

## Packed compression

64 MiB per data family, adaptive per-record DEFLATE:

| Data family | Space saved |
|---|---:|
| English text | 70.4% |
| JSON | 86.5% |
| HTML/CSS/JS-like | 88.2% |
| half text / half random | 34.7% |
| random / already deflated | effectively 0%; records stayed raw |

Compaction throughput in the recorded 4-vCPU runs was lower for compressible
data because of DEFLATE CPU cost.

Full-GET throughput likewise decreased for compressed text-like data.

The policy avoids expanding incompressible chunks by keeping raw records unless
compression saves at least 1/16.

## Packed reclamation

A 1 GiB packed lifecycle fixture measured how much partially-dead immutable
packs cost to reclaim.

At roughly 90% live:

- default policy did not rewrite the packs;
- forcing rewrite cost about 8.9 bytes written per byte reclaimed.

At roughly 50% live:

- ~259 MiB rewritten;
- ~285 MiB reclaimed;
- about 0.9 bytes written per byte reclaimed.

At roughly 10% live:

- ~102 MiB rewritten;
- ~923 MiB reclaimed;
- about 0.11 bytes written per byte reclaimed.

This is the reason the default repack policy avoids rewriting nearly-live
packs.

## Bulk delta transport

The bulk-transport experiment compared per-chunk transport against the optional bulk v2 transport on a
256 MiB missing-payload transfer.

Same harness, 8-worker per-chunk path versus bulk:

| Added destination request delay | Per-chunk | Bulk | Transfer requests |
|---:|---:|---:|---:|
| 0 ms | 21.11 MiB/s | 31.96 MiB/s | 7,868 -> 66 |
| 5 ms | 20.92 MiB/s | 31.12 MiB/s | 7,868 -> 66 |
| 10 ms | 18.44 MiB/s | 31.42 MiB/s | 7,868 -> 66 |

Request reduction:

```text
99.2%
```

The bulk path stayed roughly flat across the injected delay because storage
publication became the dominant limit.

Grouped CAS publication later improved the destination publication path and recorded bulk runs in
the mid-30 MiB/s range on the same class of fixture.

Do not compare those absolute numbers to unrelated sequential-transfer
benchmarks that used different worker/proxy setups.

## Grouped loose-CAS publication

Before grouped CAS publication, a 64 MiB PutObject issued roughly two fsync operations plus one
rename per new chunk in the investigated path.

The chosen grouped publication mechanism stages/fsyncs chunks with bounded
concurrency and uses a short publication barrier for rename/directory durability.

Recorded 256 MiB PutObject:

```text
20.1 -> 35.6 MiB/s
1.77x
```

Recorded 256 MiB multipart:

```text
18.5 -> 26.7 MiB/s
```

Server peak RSS remained around 15 MiB in those runs.

Direct pack ingest later bypasses most loose-file fanout for large known-size
uploads altogether.

## Streaming memory evolution

### Upload

Earlier whole-body buffering was removed by the streaming-ingest change.

Recorded:

```text
288 MiB PUT:
old RSS ~582 MiB and rejected by old ceiling
streaming RSS ~13 MiB and accepted

1 GiB streaming:
~19 MiB RSS
```

### Download

The streaming-read change switched full/range reads to bounded chunk-at-a-time reconstruction.

Recorded 1 GiB full GET:

```text
old: ~2043 MiB server RSS, ~99 MiB/s
new: ~16 MiB server RSS, ~581 MiB/s
```

Those numbers predate later pack-locality improvements; they demonstrate the
memory-model change, not current maximum read throughput.

## Benchmark provenance

Many detailed historical benchmark transcripts live under:

```text
testing-harnesses/results/
```

Those files are intentionally historical evidence. Their milestone names,
commands, and commit references should not be read as current user
documentation.

Current focused harnesses include:

- `z2_direct_pack`;
- `z2_locality`;
- `z2_pack_compression`;
- `z2_packed_cas`;
- `z2_repack`;
- `z2_bulk_transfer`;
- `z2_cas_batch`;
- `z2_consumer`.

See [../testing-harnesses/README.md](../testing-harnesses/README.md).

## Reproducing measurements

The validation runner lists focused stages:

```sh
scripts/validate.sh list
```

Examples:

```sh
scripts/validate.sh direct-pack-bench
scripts/validate.sh locality-bench
scripts/validate.sh bulk-bench
scripts/validate.sh cas-bench
```

Some benchmark stages depend on Linux-specific facilities such as `/proc` or
`strace`.

Run them on an otherwise quiet machine when you care about absolute numbers.

## Reporting rules

When quoting ZeroS3 benchmark results externally:

1. state the fixture and relevant object size;
2. state whether the number is a current-build measurement or historical
   comparison;
3. keep same-harness comparisons together;
4. do not imply a single development-machine number is a guaranteed production
   throughput;
5. report storage/CPU/RSS tradeoffs when they materially affect the result.

## Related documentation

- [../README.md](../README.md) — headline results only
- [ARCHITECTURE.md](./ARCHITECTURE.md) — why the measured behavior exists
- [../testing-harnesses/README.md](../testing-harnesses/README.md) — harness map