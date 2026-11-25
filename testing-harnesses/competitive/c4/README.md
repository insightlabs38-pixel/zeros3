# ZeroS3 C4 · Single-node operations and ARM64

**C4_OPERATIONAL_EFFECTIVE.** Frozen B10 and C2 did not reproduce a consistent
default-S3 slowdown. The current engine passed the bounded operational contract
on Linux amd64 and under Linux ARM64 user-mode emulation.

## Default S3 triage

The workload completed 49,152 signed operations with independent expected bytes.
Six alternating four-client pairs used 768 objects per phase. Four one-client
pairs used 384. The table shows median paired C2 throughput change and the
percentile bootstrap interval for the four-client sample.

| Four clients | Paired change | 95% interval | B10 → C2 median p99 |
| --- | ---: | ---: | ---: |
| PUT | +11.0% | −29.5% to +33.8% | 70.34 → 73.77 ms |
| GET after process restart | −1.0% | −5.5% to +1.5% | 13.05 → 13.65 ms |
| Warm random GET | +0.9% | −2.5% to +3.4% | 13.25 → 13.82 ms |
| Warm 512-byte range | +0.5% | −8.7% to +1.9% | 13.17 → 14.12 ms |

The Linux kernel page cache was uncontrolled. Four-client PUT changed sign
twice across six pairs. One-client PUT was +19.9% with a small four-pair
sample. Neither configuration establishes a persistent throughput loss.
The p99 shifts warrant tracking in larger campaigns.

Frozen in-process HTTP profiles included PUT, full reads, range reads, signed
HEAD and a signing control. PUT CPU was dominated by MD5 and SHA-256 in both
builds. Allocations per operation were identical for PUT, GET and range.
C2 allocated 23,386 versus 21,145 bytes per profiled PUT. The allocated bytes
and locking samples supplied no demonstrated cause for the historical slowdown.
The bounded investigation left default request handling as it was.

## Operational contract

A single process owns the writer store. The smoke checks initialize and serve,
graceful stop, restart, basic and deep verify, archive finalization, online
maintenance with twelve exact concurrent reads, namespace checkpoint,
authority revert, snapshot, bundle export and inspect, fresh store import,
restore and peer repair. It flips one loose chunk in the disposable repair
store and verifies the exact recovery. An additional actual SIGKILL after
PUT acknowledgement preserves the object on fresh-process restart.

Boto3 covered conditional requests, ranges, pagination, metadata, copy,
delete, unsigned failure and one injected HTTP 503 followed by a verified
retry. The AWS SDK for Go v2 verified exact ordinary and multipart reads,
copy and preconditions on the migrated object. Restic performed two backups
over S3 and restored both revisions after restart to independent digests.

Two fresh T1 partitions passed all fourteen selected tests and reused all
fourteen on rerun. They covered old-binary refusal, capability lowering,
pack parity and repair, grouped corruption and stale reads, C2 and C3 crash
cuts, online GC and journal/checkpoint boundaries. The catalog has 1,517
top-level tests. The other root tests remained outside this C4 run. Three
focused concurrency and authority tests also passed with the race detector.

## ARM64 and operator limits

Go 1.27.0 cross-built a static Linux/arm64 ELF with CGO disabled. QEMU user
10.0.13 ran the operational smoke, including replay, exact 4 KiB reads,
snapshot restore, peer repair and an acknowledged PUT followed by SIGKILL.
The host was Linux amd64 and actual ARM64 hardware remains unmeasured.
The engine uses explicit byte order and Linux flock. The executable input
normalization formats have separate architecture requirements.

Small-store amd64 runs verified a repair, an archive and a restart in well
under one second per stage. Recovery time at larger scales, multi-node
failover, per-tenant access control and physical power-loss safety remain
deployment limits. Off-host backup and periodic restore remain part of
operating one writable store.

## Replay

Use `operations.py --binary BIN --scratch WORK --results OUTPUT` with the C1
Python environment. Optionally supply `--sdk` and `--restic`. Build the SDK from
`testing-harnesses/harness/z4_native_sdk`. Run `arm64.sh WORK QEMU` for the same
core operations on an emulated target. Use `recovery.sh WORK` to run or resume
the targeted partitions and `profile.sh WORK` to rebuild the frozen profiles.
[Raw evidence](results/) includes requests, CPU profiles and operational phases.
