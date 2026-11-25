# ZeroS3 C8 · Streaming native reconstruction proof

**C8_STREAM_EFFECTIVE.** Native import now verifies a new root and its
manifest during one reconstruction pass. The relay keeps small known-size
signed closures in a bounded memory buffer.

## End-to-end effect

The receiver feeds existing exact SHA-256 proof windows to incremental CDC
chunk and ETag hashing. The check runs before root authority publication.
An already-held root, including a retry after a process cut, still verifies
the live record. Namespace acknowledgement uses the existing commit.
Definite signed requests up to 1 MiB use relay memory and larger transfers
retain the established temporary-file path.

| TypeScript scoped transfer, unshaped | C7 | C8 |
| --- | ---: | ---: |
| Median elapsed, three alternating pairs | 4.160 s | 1.063 s |
| Median receiver CPU | 4.98 s | 1.84 s |
| Median receiver logical reads, including sockets | 70.61 MB | 48.13 MB |
| Median receiver peak RSS | 47.53 MB | 48.93 MB |
| Client output blocks | 320 | 0 |
| HTTP bytes across both legs | 350,034 | 350,034 |

A matched warm source served both clients and receivers. Each replay was
read back through ordinary S3 and checked against the frozen target digest.
Producer SST and bundled source also used structural transfer. Parquet,
shifted media and entropy supplied ordinary S3 and CDC controls. Single
control trials establish compatibility and exactness under those inputs.

## Planning scratch and contention

A separate syscall trace of the TypeScript planner recorded 67.83 MB of
peak sampled temporary allocation, 779 fsync calls and 5,760 pread64 calls.
It includes materializing decoded streams and applying candidates. Timing
from that traced run is descriptive because tracing adds overhead.

The seekable scratch prototype independently compressed 256 KiB blocks.
On decoded TypeScript it used 5.48 MB versus 22.54 MB of raw allocation.
It verified the whole decoded SHA-256 and 200 random 4 KiB reads per trial.
Its first trial's 4 KiB p95 rose from 7.1 to 974 µs, and the opaque control
used the same allocation with extra encoding CPU. This prototype remains
a research harness. The exact optimizer still uses seekable spool files.

During two 512-request four-client foreground trials, the scoped transfer
took median 6.12 s on C7 and 2.27 s on C8. The foreground p99 was 42.8–51.8
ms on C7 and 54.5–56.4 ms on C8. This faster import can still compete for
CPU on a two-core node. A separate online maintenance experiment reclaimed
9.66 MB and had p99 29.7–33.1 ms versus 16.0–24.0 ms for idle controls.
Offline optimization respected the writer lock and refused the live store.

## Verification and replay

The focused suite verified older closure framing, bad target digest and
chunk layouts, scoped import and fallback, retained metadata, and
ordinary compatibility. Existing real SIGKILL cuts covered durable
components, manifest publication, and journal sync. The new code feeds
the same staged publication path with one verified stream.

Use `stream.py --help` for the matched source, receiver, pacing and
foreground workload. `scratch.py` reproduces the read/space probe.
`scheduling.py` replays the idle and online maintenance comparison.
[Raw measurements](results/) include all exact identities and the planner
syscall summary. C9 evaluates the resulting tradeoffs with complete
artifact and snapshot consumer workflows.
