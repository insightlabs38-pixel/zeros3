# ZeroS3 C19 · Integrated frontier

**C19_FRONTIER_EFFECTIVE.** Faster exact archive preparation reproduced
on two families, sparse volume discovery added a separate retention
gain, and receiver readiness explained the transfer break-even.
These results support multiple specialized modes on a single node.

## Ordinary S3 and maintenance

Two reverse-order trials used 70 signed objects, 31 overwrites, four
clients, exact bytes and metadata, paginated LIST, and a fresh restart.
Small, medium and large object cohorts are separate in the raw rows.

| Engine | First PUT wall | 4 KiB p99 | HEAD p95 | Immutable / post-churn MB |
| --- | ---: | ---: | ---: | ---: |
| C9 | 0.185 s | 11.8 ms | 10.9 ms | 14.03 / 15.33 |
| Integrated | 0.232 s | 13.2 ms | 8.5 ms | 14.04 / 15.34 |
| MinIO | 0.277 s | 15.9 ms | 9.5 ms | 22.54 / 25.17 |
| RustFS | 0.226 s | 14.8 ms | 7.7 ms | 22.63 / 24.56 |

ZeroS3's default compression and overwrite history differ from the
competitors' placement and retention. These are local single-node
runs with filesystem defaults. S3 timings report medians of two
observed trials. All engines restored 70 objects after restart.
In the mixed replay, adaptive foreground p99 was 77.2 versus 93.3 ms
with the C9 incumbent. Import took 3.26 versus 2.70 s.

## Exact revision histories

Finalized KiB after GC, recompress, compact and deep verify. Read
columns compare CDC and integrated medians of the two trial statistics.

| History | CDC / C9 / integrated KiB | 4 KiB p95 ms | Whole p50 ms |
| --- | ---: | ---: | ---: |
| TypeScript gzip | 8,256 / 4,784 / 4,792 | 1.8 / 39.9 | 8.8 / 945 |
| urllib3 gzip, held out | 668 / 456 / 464 | 1.3 / 25.2 | 1.2 / 53.6 |
| Chinook SQLite, held out | 668 / 564 / 572 | 3.3 / 3.0 | 1.7 / 2.7 |
| SQLite/WAL history | 700 / 404 / 412 | 2.3 / 2.5 | 1.9 / 3.3 |
| Parquet | 1,284 / 696 / 704 | 2.5 / 8.8 | 2.7 / 94.3 |
| RocksDB SST | 10,156 / 3,788 / 3,796 | 2.6 / 5.8 | 29.0 / 9.3 |
| Typed checkpoint | 248 / 144 / 152 | 2.0 / 11.2 | 2.4 / 7.6 |
| Independent MobileNet | 27,828 / 27,820 / 27,828 | 2.5 / 2.1 | 30.0 / 28.7 |
| Rebuilt ext4 and shift | 384 / 376 / 316 | 1.7 / 2.1 | 25.5 / 32.3 |
| Incremental ext4 and shift | 456 / 448 / 436 | 1.7 / 2.2 | 23.8 / 29.9 |
| Unrelated entropy | 12,400 / 12,392 / 12,400 | 1.4 / 1.5 | 8.9 / 8.2 |

TypeScript optimization took 8.06 s wall versus 17.35 s C9, and
urllib3 took 0.71 s versus 1.31 s. Integrated whole reads were 23%
and 44% faster than C9, with comparable short-range cost. All 11
histories restored byte-exact through portable bundles into C9, with
native consumer checks as applicable. The two published Chinook
assets are byte-identical. The third fixture contains a controlled
12-row edit. MobileNet and entropy selected zero graph rewrites.
The oldest ext4 base stayed pinned by a live dependent after pruning.
C9 fell to 304 KiB while the integrated rebuild remained at 316 KiB.
Restic, Kopia and Borg restored all 54 independent snapshots. Rebuilt
ext4 repositories retained 1,384 / 544 / 746 KiB, with encryption
and different snapshot, compression and durability policies.
Exact whole-patch baselines left urllib3 nearly full-size at 300 kB.
For the edited Chinook file, xdelta3, XD, and zstd used 437, 491, and
516 bytes against the same verified base, before allocating a store.

## Native and shaped transfer

| TypeScript destination | Path | Two-leg bytes | Median wall |
| --- | --- | ---: | ---: |
| Normal prior, 1 MiB/s and 80 ms/request | Derived closure / direct PUT / CDC replicate | 382,491 / 4,175,570 / 8,381,079 | 2.70 / 4.13 / 8.76 s |
| Authority prior, same shaping | Closure / CDC replicate | 350,088 / 8,381,079 | 2.25 / 8.80 s |
| Identical bytes at another key, loopback | CDC replicate / closure / direct PUT | 20,737 / 32,626 / 4,175,570 | 24 / 24 / 26 ms |
| Empty, same shaping | Closure / CDC replicate / direct PUT | 8,382,089 / 8,381,079 / 4,175,570 | 8.90 / 8.84 / 4.15 s |

Source optimization cost 7.98 s wall and 13.56 CPU seconds once.
Derived transfer with an ordinary prior cost 1.56 s on loopback
versus 33 ms PUT, and 2.54 receiver CPU seconds under shaping.
It used 9.03 MB retained versus 8.45 MB PUT. Roughly six similar
shaped transfers amortize preparation against direct PUT. The
unprepared source fell back to CDC. Shifted plain bytes favored CDC
without closure. Entropy sent full data. Local OpenXet uses a
different client and repository policy. Its official client uploaded the
second TypeScript file with 4.19 MB of transmit traffic and restored it
exactly. The fresh-cache download received 4.58 MB.

Root integration passed 119 focused tests and nine race tests, plus a
reader-only gzip race check. At a808140, 31 focused and seven race tests
passed. The historical B8 race run reached its cap. [Logs](results/)
Physical faults, actual WAN, multi-node and native ARM performance
remain open. The first production release remains undecided.

[Decision](../POST_C19_DECISION.md) · [Reproduction](REPRODUCE.md) · [Raw results](results/)
