# ZeroS3 benchmarks

This document records measurements that explain ZeroS3's storage tradeoffs.
They are workload- and machine-specific observations, not performance
guarantees.

When two builds are compared, both sides used the same fixture and environment.
Numbers from unrelated tables should not be compared as if they came from one
benchmark suite.

## What the benchmarks measure

ZeroS3 tracks more than throughput because storage changes often trade one cost
for another. Depending on the experiment, the important metrics include:

- upload/read throughput and latency;
- peak RSS;
- physical bytes and file count;
- request/syscall count;
- time to reach the read-optimized representation;
- write amplification;
- store-open/index cost;
- content reuse after edits.

## Direct pack ingest

The direct-pack experiment used a 256 MiB known-size pseudo-random PutObject.

The comparison uses baseline commit `3df362b` and direct-pack implementation
commit `1c25a4c`.

| Metric | Before direct pack ingest | Direct pack ingest |
|---|---:|---:|
| PUT throughput | 61-67 MiB/s | 72-89 MiB/s |
| server RSS growth | +6 MiB | +7 MiB |
| loose payload files | 4,019 | 0 |
| immediate packs | 0 | 4 |

The baseline required a later locality compaction to reach the same physical
shape:

| Step | Time |
|---|---:|
| baseline PUT | ~4.2 s |
| locality compact | ~2.9 s |
| baseline total to packed-ready state | ~7.1 s |
| direct ingest to packed-ready state | ~2.9 s |

That reduced time to the read-optimized representation by about 59% in the
recorded run.

Immediate direct-packed reads measured:

| Read | Result |
|---|---:|
| full 256 MiB GET | ~877 MiB/s |
| 16 MiB range | ~811 MiB/s |

A separate 1 GiB sanity run measured 81.6 MiB/s PUT throughput, about +14 MiB
RSS, 16 packs, no loose payload files, and a 917 MiB/s immediate full GET.

## Content-defined edit reuse

A 4 MiB object with an approximately 4 KiB insertion near the beginning reused
96.6% of the original bytes under CDC. The comparison fixture using fixed
64 KiB blocks reused 0% because the insertion shifted every later boundary.

A separate 8 MiB delta-sync edit reused 99.0% of bytes, so only the affected
logical chunks were transferred.

These percentages demonstrate CDC re-synchronization for those fixtures; edit
shape and content can change the exact reuse ratio.

## Pack locality and coalesced reads

The locality/coalescing experiment used a 256 MiB pseudo-random object,
64 MiB raw packs, warm page cache, 4 vCPU, loopback, and medians of three runs.

| Metric | Before | After |
|---|---:|---:|
| adjacent logical pairs physically contiguous | 0% | 99.9% |
| average contiguous run | n/a | ~1,005 chunks |
| pack opens per full GET | 4,019 | 4 |
| `pread64` calls per full GET | 4,021 | 68 |
| full GET | 609 MiB/s | 852 MiB/s |
| 16 MiB range | 538 MiB/s | 707 MiB/s |
| 64 MiB range | 566 MiB/s | 833 MiB/s |
| 1 MiB range | 4.7 ms | 4.8 ms |
| random 64 KiB read | 3.1 ms | 3.2 ms |

The small/random-read differences were within noise. The meaningful gain came
from sequential locality and fewer opens/reads.

A three-version 128 MiB checkpoint fixture improved from 662 to 839 MiB/s and
from 2,012 pack opens to 2. A 400-file site fixture reduced pack opens from
3,460 to 401.

Locality-oriented compaction was about 30-45% slower for large loose objects in
the recorded fixture because it read loose files in manifest order. Direct pack
ingest removes most of that cost for large known-size new uploads.

## Snapshot bundles

### Browser/site revision

| Artifact | Size |
|---|---:|
| full target bundle | 0.89 MiB |
| delta against previous snapshot | 0.10 MiB |

The delta was 88.7% smaller.

### 128 MiB checkpoint sequence

| Artifact | Size |
|---|---:|
| full S2 bundle | 128.26 MiB |
| S1 -> S2 delta | 3.67 MiB |
| S2 -> S3 delta | 2.52 MiB |

The first delta was 97.1% smaller than the full target bundle and reused 97.3%
of chunk bytes from its base; the second was 98.0% smaller.

A 1 KiB insertion in a separate 64 MiB object produced one payload chunk in the
delta fixture after CDC re-synchronization.

Checkpoint runs also recorded roughly 0.1 s export, 0.65 s delta import,
2.6 s full S1 import, and about 16 MiB peak RSS.

The sorted-inventory overlap merge for 1 million base descriptors and
1 million target descriptors took about 21 ms and allocated no additional
memory in the merge itself. Each inventory descriptor is 36 bytes.

## Packed locator scale

Synthetic tiny-chunk stores measured:

| Packed chunks | Open time | Locator heap | Peak RSS |
|---:|---:|---:|---:|
| 1M | 0.52 s | 51 MiB | ~125 MiB |
| 5M | 2.7 s | ~256 MiB | 539 MiB |

Historical Go-map comparison:

| Packed chunks | Map open | Map heap |
|---:|---:|---:|
| 1M | 0.71 s | 128 MiB |
| 5M | 3.6 s | 513 MiB |

Point lookups remained sub-microsecond in the synthetic runs. The locator is
reconstructible physical metadata; logical SHA-256 verification remains the
content-integrity authority.

## Packed compression

Adaptive per-record DEFLATE over 64 MiB data families measured:

| Data family | Space saved |
|---|---:|
| English text | 70.4% |
| JSON | 86.5% |
| HTML/CSS/JS-like | 88.2% |
| half text / half random | 34.7% |
| random / already compressed | effectively 0%; records stayed raw |

Compression reduced both compaction and read throughput for compressible data.
The policy therefore keeps raw records unless compression saves at least 1/16
of logical size.

The direct-ingest compression experiment made the tradeoff more explicit:

| Mode | PUT | Full read | Stored-byte effect |
|---|---:|---:|---|
| raw direct pack | ~57.8 MiB/s | ~870 MiB/s | baseline |
| adaptive DEFLATE | ~35.3 MiB/s | ~170 MiB/s | ~78% saved on the text fixture |

That result is why online direct packs are raw by default.

## Packed reclamation

A 1 GiB lifecycle fixture measured the cost of rewriting partly-dead packs.

| Approx. live fraction | Bytes rewritten | Bytes reclaimed | Write/reclaim ratio |
|---:|---:|---:|---:|
| 90% | default policy skipped rewrite | n/a | forced rewrite ~8.9x |
| 50% | ~259 MiB | ~285 MiB | ~0.9x |
| 10% | ~102 MiB | ~923 MiB | ~0.11x |

The result supports the default policy of avoiding rewrites of nearly-live
packs.

## Bulk delta transport

A 256 MiB missing-payload fixture compared per-chunk transfer with bulk v2.

| Added destination delay | Per-chunk | Bulk | Transfer requests |
|---:|---:|---:|---:|
| 0 ms | 21.11 MiB/s | 31.96 MiB/s | 7,868 -> 66 |
| 5 ms | 20.92 MiB/s | 31.12 MiB/s | 7,868 -> 66 |
| 10 ms | 18.44 MiB/s | 31.42 MiB/s | 7,868 -> 66 |

Bulk transport reduced transfer requests by 99.2% and stayed relatively flat as
the injected request delay increased. Later grouped CAS publication moved the
destination bottleneck and produced bulk runs in the mid-30 MiB/s range on the
same class of fixture.

## Streaming memory changes

Streaming ingest removed the earlier whole-body buffering model.

| Upload fixture | Earlier behavior | Streaming behavior |
|---|---|---|
| 288 MiB PUT | ~582 MiB RSS and rejected by old ceiling | ~13 MiB RSS and accepted |
| 1 GiB PUT | whole-body model impractical | ~19 MiB RSS |

The streaming-read change similarly replaced whole-object buffering with
bounded chunk reconstruction.

A 1 GiB full GET changed from roughly 2,043 MiB server RSS and 99 MiB/s to
roughly 16 MiB RSS and 581 MiB/s in that historical comparison.

Those read numbers predate later pack-locality work; they demonstrate the
memory-model change rather than current peak read throughput.

## Evidence and reproduction

Detailed historical transcripts remain under:

```text
testing-harnesses/results/
```

Their filenames preserve the original development milestone names for
provenance. Current harness organization is documented in
[../testing-harnesses/README.md](../testing-harnesses/README.md).

List benchmark and validation stages with:

```sh
scripts/validate.sh list
```

Examples include:

```sh
scripts/validate.sh direct-pack-bench
scripts/validate.sh locality-bench
scripts/validate.sh bulk-bench
scripts/validate.sh cas-bench
```

Some stages use Linux-specific facilities such as `/proc` or `strace`.

When reporting a benchmark externally, keep same-harness comparisons together
and include the fixture, relevant object size, source commit or release,
machine/environment context, and material storage/CPU/RSS tradeoffs.

## Related documentation

| Document | Purpose |
|---|---|
| [../README.md](../README.md) | headline results |
| [ARCHITECTURE.md](./ARCHITECTURE.md) | storage design behind the measurements |
| [../testing-harnesses/README.md](../testing-harnesses/README.md) | harness map and validation structure |