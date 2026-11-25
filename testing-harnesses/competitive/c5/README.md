# ZeroS3 C5 · Independent artifact validation

**C5_SPECIALIZED_VALUE.** Cold software artifacts and some database snapshots
show a material storage benefit. Opaque checkpoints and unrelated files
showed little room beyond ordinary finalized CDC in this sample.

## Matched recapture

C4 `ce98b10` and C2 `4bc66f3` were rebuilt or reused from pinned binaries.
MinIO and RustFS used the same S3 keys, payloads and retained revisions.
The history lane reopened every store and rehashed each object. ZeroS3
completed GC, full-live recompressing repack, 1 MiB compaction, and final GC
before recording allocated bytes. The peer engines retained all written
keys across restart. MinIO and RustFS used local single-node configurations.

| Family | ZeroS3 CDC | Archive | Mixed | MinIO | RustFS |
| --- | ---: | ---: | ---: | ---: | ---: |
| Coreutils source | 565,248 | 409,600 | 397,312 | 1,900,544 | 1,986,560 |
| Parquet snapshots | 1,306,624 | 712,704 | 1,097,728 | 5,963,776 | 6,049,792 |
| SST snapshots | 10,391,552 | 3,878,912 | 3,899,392 | 11,366,400 | 11,452,416 |
| High entropy | 12,689,408 | 12,689,408 | 12,689,408 | 12,746,752 | 12,832,768 |
| TypeScript releases | 12,689,408 | 5,296,128 | 5,312,512 | 12,742,656 | 12,828,672 |
| HTTPX releases | 364,544 | 270,336 | 364,544 | 417,792 | 471,040 |
| MobileNetV2 checkpoints | 28,487,680 | 28,487,680 | 28,487,680 | 28,610,560 | 28,696,576 |

The units are filesystem-allocated bytes. HTTPX and MobileNetV2 were unseen
during C3/C4 tuning; their exact inputs and URLs were fixed before C5 trials.
C2 matched the retained CDC and archive allocations on these histories.
Controller overhead dominates parts of the smallest per-family stores.

## Latency, CPU and transfer

With four clients the median S3 rates for ZeroS3, MinIO, and RustFS were
460/459/371 PUT/s, 903/798/819 random GET/s, 981/806/912 small ranges/s and
769/691/640 mixed operations/s. Median PUT p99 was 24.5/18.4/24.7 ms.
All nine ordinary S3 smoke runs passed. The per-engine lanes used three
alternating trials and small batches; the ranking is local and noisy.

The TypeScript archive used 58.3% less storage than CDC. It added about
42 CPU seconds during maintenance, with a 1,137 ms warm full-read median
versus 20 ms for CDC. For Parquet the archive retained 45.5% less than CDC,
but the mixed policy kept warm full reads around 5 ms versus 111 ms for
archive. SST retained 62.7% less in archive with a faster measured full
pass; the sample covers three snapshot objects.

The paced TypeScript repeat used two alternating trials per lane.
The scoped closure took 4.62 s and 350,034 HTTP bytes. Peer CDC took
11.06 s and 8,381,025 bytes. Direct S3 took 5.50 s and 4,175,570 bytes,
and native sync took 5.56 s and 4,192,302 bytes. The prior authority was
prepared before the timed transfer. Xet's C1 local controls use interface
TX counters. Restic verified nine restored revisions and retained
6,746,112 bytes for the SST backup with its snapshot and encryption semantics. [C1 OpenXet evidence](../c1/results/xet-local-repeat.jsonl)

## Economics and next gates

At illustrative $0.025/GiB-month and $0.05 per core-hour, TypeScript's
archive maintenance CPU pays back after roughly 3.4 months on stored bytes
alone. The model also recomputes disk and compute sensitivity. Frequent
reads, extra safety copies, provisioning, requests and change frequency
can dominate this calculation. The test provides no market-fit result.

- C6 should measure recompression and physical write cost after a full
  finalize. Opaque models and entropy isolate redundant writes.
- C7 should bound new-version analysis on related source and archive
  updates while checking index update cost and candidate quality.
- C8 should lower exact reconstruction CPU and read amplification on the
  type of compressed packages that won storage and transfer.
- C9 should exercise an actual backup consumer and a versioned artifact
  replica with cold and warm read paths.

Run `replay.sh WORK C2_BIN C1_CORPUS C2_DOWNLOADS` with the pinned binaries
and the C1 Python dependencies. [Input manifest](inputs.json) · [raw data](results/)
