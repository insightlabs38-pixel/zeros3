# ZeroS3 C17 · Verified pipeline reuse and bounded ingest

**C17_STREAM_EFFECTIVE.** The optimizer can reuse verified canonical
recipe components between planning and apply. The C16 receiver now
streams derived plaintext through CDC into a staged pack. Both paths
retain the existing exact proof and durable authority format.

## Planner and retained storage

The original C8 profile reproduced 67.83 MB of sampled scratch, 779
`fsync` and 5,760 `pread64` calls on the C16 engine. The integrated
comparison builds both lanes above `20f13bc`. The lean TypeScript
profile with retained recipes made 2,021 positioned reads versus 3,371,
used 58.52 MB versus 67.78 MB sampled scratch, and took 17.16 versus
32.22 s under tracing. Both applied the same plan and made 782
`fsync` calls. Write call totals include GC logging in those traces.

| TypeScript, two rotated trials | Baseline wall | With recipe reuse | Baseline apply | With recipe reuse |
| --- | ---: | ---: | ---: | ---: |
| 2 Go workers, 64 MiB soft heap limit | 14.61 s | 8.62 s | 9.20 s | 3.26 s |
| 4 Go workers, 192 MiB soft heap limit | 11.46 s | 7.35 s | 6.02 s | 2.15 s |

The tight case spent 26.16 versus 14.98 CPU seconds. The two recipes
used 297,317 retained bytes in 1,274 entries. Their target identities,
selected transitions and priced bytes matched. Finalized allocation
was 4.90–4.91 MB in both lanes. SQLite and independent entropy served
as controls. The canonical cache had no eligible plans in either.

Enable `authority optimize -retain-canonical-plans` with canonical
analysis. Retention is limited to 4 MiB and 4,096 entries per invocation.
Selected components are checked against their planned digests and
published through the existing pack/segment path. Apply then proves
the complete target independently. Unretained recipes still rebuild
with the original seekable path. The initial canonical source remains
seekable for random COPY matching.

## Derived receiver

The fused path verifies the named compressed object while maintaining
the existing 16 MiB input limit. Its decoder feeds the original CDC
and whole-object SHA-256 pipeline. A verified pack waits for the entire
plaintext identity, including the expected logical length, before
publication. The receiver then proves the requested object and
publishes through the existing namespace transaction.

Both TypeScript lanes moved 382,491 bytes across the relay legs.
In the tight idle pair, receiver peak RSS fell from 72.15 to 56.98 MB,
physical writes from 26.74 to 25.12 MB, and elapsed from 1.71 to 1.63 s.
Receiver CPU was 2.55 versus 2.58 s. Final allocation was approximately
9.06 MB in both lanes. With foreground load, receiver RSS fell from
70.86 to 58.82 MB and four-client GET/PUT p99 from 26.03 to 22.62 ms.
These are medians of two trials per lane. Both CPU profiles ran on a
two-core quota. The four-worker lane exercises scheduling on that quota.

## Verification

Every trial deep-verified and read its retained objects whole and by
4 KiB ranges. Focused tests exercise retained-component corruption,
retention bounds, truncation, cancellation, verified large pack
staging, publication errors and SIGKILL during derived-base and
namespace publication. The resulting format uses the existing
authority and pack layouts. Looped resource limits remain soft Go
heap limits, and real physical power loss remains unqualified.

[Reproduction](REPRODUCE.md) · [Raw rows](results/)
