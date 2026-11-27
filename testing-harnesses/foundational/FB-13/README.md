# FB-13 · CDC and representation retention

## Smaller CDC records

The production CAS, binary manifests, pack compressor and split locator
stored each alternate boundary list in disposable test stores. Every
lane compacted and deep-verified. The baseline matches the frozen
streaming CDC chunker. The smaller lanes use the same Gear table with
proportionally scaled minimum, target, maximum and masks.

| Workload | CDC v1 retained | 32 KiB target | 16 KiB target | v1 then 16 KiB |
|---|---:|---:|---:|---:|
| X9 eight source revisions | 9,605,120 | 7,421,952 | 6,422,528 | 8,290,304 |
| X9 standalone tree | 2,473,984 | 2,568,192 | 2,740,224 | — |
| TypeScript tar | 9,211,904 | 9,113,600 | 8,888,320 | 13,086,720 |
| TypeScript gzip | 12,632,064 | 12,660,736 | 12,705,792 | 12,685,312 |
| Held-out HTTPX | 323,584 | 331,776 | 335,872 | 352,256 |
| SQLite revisions | 655,360 | 655,360 | 663,552 | 868,352 |
| Parquet revisions | 1,253,376 | 1,257,472 | 897,024 | 1,814,528 |
| Independent entropy | 12,636,160 | 12,660,736 | 12,709,888 | 12,689,408 |

X9's 16 KiB lane retained 33% fewer bytes and decoded about 34 KiB per
4 KiB sample versus 139 KiB at v1. Locator arrays and filters grew
from 15.2 to 42.5 kB and measured ingest CPU from 0.30 to 0.75 s.
Binary manifest payloads grew from 23 to 102 kB and are already charged
in allocated packs. The locator figures include backing capacities.
They exclude allocator and process overhead. Repack CPU measured 0.40 s
versus 0.50 s on this fixture. Results include warm exact lookup and
native range time. Range byte counts sum intersecting record lengths.
The CPU and lookup timings are single local passes.

Use v1 for isolated objects and for these gzip, entropy and short
held-out histories. Consider 16 KiB for a new dense source revision
series with known retention and a byte-oriented workload. Consider it
for closely related Parquet when structure-aware optimization also
fails to pay, after measuring the intended corpus. The modest tar
gain and doubled-to-tripled CPU and locator costs favor v1 for the
measured TypeScript case. SQLite's equal 32 KiB allocation and halved
read geometry only favor the smaller lane when sparse reads dominate.
Mixing boundaries cost 42% more than v1 on TypeScript and 32% more on
SQLite. Decide per cohort before ingest to preserve reuse. The table
supports a workload recommendation rather than an automatic classifier
or a new wire schema. CDC v1 remains active and CDC evolution is
owner-gated.

## Dependency-only retention

Stats classify bodies reached only by dependency edges and deduplicate
their component digests against current, historical, snapshot,
representation and multipart roots. The report includes stored extents
and hot pack copies. A damaged live graph cannot produce a dependency
accounting verdict.

The explicit offline `authority rebase -bucket B -key K` command prices
one expired CDC base at a time, with up to eight eligible direct leaf
deltas and four independent retained anchors. Source reads and staged
program bytes are bounded. It charges the union of new pieces and body
files, shared old extents, hot copies, expected pack rebuild work and
scratch. Existing read-amplification and free-space options apply. The
dry run does no publishing. Apply proves every new body through the
native decoder before replacing any target record. Each replacement
keeps its identity and commits independently. GC and repack retain the
old base while any verified leaf still needs it.

After pruning X9 to four historical versions plus current, dependency
extents fell from 3,836,205 bytes to zero across four rebased versions.
Total allocation after GC, repack and compact declined from 10,665,984
to 4,321,280 bytes. The planner charged 49,152 bytes of staging and
7,349,168 bytes of pack source. Initial native execution used 223 ms
migration CPU and 82 ms reclaim CPU. Native whole-version reads and
deep verification included every surviving historical version. The
controlled fault replay exercised an impossible reserve, failed proof,
snapshot-held base and a partial replacement followed by reopen,
exact reclamation and retry.

[A reproduction guide](REPRODUCE.md) and [raw results](results/) are available.
