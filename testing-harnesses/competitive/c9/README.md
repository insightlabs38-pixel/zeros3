# ZeroS3 C9 · Integrated consumer comparison

**C9_SPECIALIZED_VALUE.** The strongest end-to-end fit is a single-owner
artifact and snapshot archive with scheduled maintenance and optional
native replication. Ordinary S3 clients retain the Core Client Profile.

## Artifact mirror and database restore

The TypeScript mirror uploaded three producer releases, repeated the
latest object, paginated all keys, enforced a conflicting PUT precondition,
deleted the oldest live key and restarted after an actual SIGKILL. It
restored all three revisions from a verified portable bundle. The
consumer checked full SHA-256, sparse ranges and the package version
inside each exact archive.

The database lane applied the same lifecycle to six producer Parquet and
SST snapshots. PyArrow opened restored Parquet row groups and RocksDB
ingested the restored SST files. Both verified version-specific records.
Restic also backed up those producer files through S3 and recovered
three snapshots after restart, checking both hashes and database contents.

| Materialized workflow | Before archive | Archived | Portable objects restored |
| --- | ---: | ---: | ---: |
| TypeScript mirror | 12,713,984 B | 5,300,224 B | 3 |
| Parquet and SST history | 13,459,456 B | 4,493,312 B | 6 |

Archive times were 24.95 s and 15.20 s. These store figures compare the
ingested state after restart with the archived output of full finalization.
The separate Restic lane occupied 5,701,632 bytes before shutdown, with
encrypted snapshot semantics. Full reads of archived TypeScript still
took around 1.2 s in the source store. Its restored CDC copies took 7–10 ms.

## Comparisons

| Same TypeScript revision at 768,000 B/s per relay leg | Median seconds | HTTP stream bytes |
| --- | ---: | ---: |
| Scoped native closure | 1.51 | 350,034 |
| Peer CDC relay | 11.04 | 8,381,025 |
| Ordinary S3 PUT from local file | 5.52 | 4,175,570 |
| Native CDC sync from local file | 5.56 | 4,192,302 |

These are two alternating measurements with verified prior authority.
The model charges source preparation separately. Loopback HTTP stream
bytes exclude transport overhead. Local OpenXet reverified exact, shifted
and entropy cases. It recorded 7,136 and 97,811 loopback TX bytes for exact
and edited inputs. Xet's interface counters include transport overhead.

Ordinary S3 repeated 2 trials per engine. Four-client median random GET
rates were 904/767/814 per second for ZeroS3/MinIO/RustFS. PUT throughput
was 500/487/435 per second. PUT p99 was 33.6/17.0/28.7 ms and range p99
was 21.2/16.5/12.3 ms. Keep the throughput and tail results together.
One RustFS trial raced housekeeping during allocation sampling. Its
fresh rerun replaced the incomplete trial in the reduction.

## Acceptance and disposition

The final engine passed focused transfer, recovery, pack, index and
range tests, plus both fresh recovery partitions. Linux/arm64 with
CGO disabled passed the operational smoke under QEMU user emulation.
Validation used bounded process cuts and targeted tests. Physical
ARM64 performance and power-loss qualification remain open.

Continue with exact compressed full-read replay and foreground tail
control in a separate research effort. Stop this completed queue.
The stable production reference and published maturity remain in place.

Use `replay.sh WORK C1_WORK C5_CORPUS C2_DOWNLOADS` with the C1 Python
environment and the binaries pinned in the
[reproducibility manifest](RESULTS_MANIFEST.json). Run
`acceptance.sh WORK` for focused checks. [Raw results](results/)
