# ZeroS3 C13 · Small-object compression contexts

**C13_SPECIALIZED.** Optional pack-local preset dictionaries lowered
finalized allocation by 3.75% on 4,926 real source fragments. The native
repack spent 1.54 s CPU versus 0.71 s, with similar signed 4 KiB read
latency. Cohort blocks remain research-only for coalesced consumers.

## Physical comparison

Both native lanes passed the same full finalization and deep verification
for 896 signed main objects. CDC used 1,777,664 allocated bytes.
The optimizer selected one grouped-authority change and retained
1,798,144 bytes. CDC packs accounted for 1,376,256 allocated bytes
and the journal for 335,872. The 128-object HTTPX held-out native store
used 237,568 bytes in either lane.

The archive lab uses the same objects, exact aliases, immutable extents
and a minimal digest-checked index. It omits production namespace and
journal services. Within that matched layout, two trials per lane gave:

| Main lane | Allocated bytes | Dictionary bytes | Index allocated | Build CPU | 8-client first-pass p95 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Independent deflate | 1,003,520 | 0 | 40,960 | 52 ms | 769 µs |
| Deflate with cohort prefix | 954,368 | 57,344 | 45,056 | 101 ms | 914 µs |
| Independent zstd | 991,232 | 0 | 40,960 | 32 ms | 989 µs |
| Trained cohort zstd | 958,464 | 52,705 | 45,056 | 148 ms | 1,264 µs |
| 32 KiB cohort blocks | 909,312 | 0 | 36,864 | 34 ms | 997 µs |
| 8 KiB cohort blocks | 929,792 | 0 | 36,864 | 31 ms | 980 µs |

The 32 KiB layout compressed across objects within a block. C6's
placement and restartable-dictionary runs preserved separate pack record
boundaries. The one-client p95 was 151 µs for independent deflate,
179 µs for 32 KiB blocks and 143 µs for 8 KiB. Eight-client first-pass
throughput was about 23.0k, 22.3k and 20.5k operations/s, respectively.
Four KiB-capped reads cover most complete objects in this small panel.

## Activation and retention

Independent entropy required 393,216 payload bytes. Ungated trained
dictionaries used 386,398 payload bytes plus an 8,192-byte context.
Charging each new dictionary and 1 KiB of index allowance left the
entropy cohort raw. Across the main archive, this reduced trained zstd
from seven to three contexts and 958,464 → 946,176 allocated bytes.
Prefix activation fell from seven to four contexts at identical allocation.
Training remains part of build CPU, including the rejected contexts.

HTTPX deflate used 65,536 allocated bytes. The 32 KiB and 8 KiB block
variants used 53,248 and 61,440 bytes. Their eight-client p95 was
1,518 and 880 µs versus 779 µs. The trained dictionary used 61,440 bytes
at about 24 ms build CPU versus 6 ms for independent zstd.

In main churn, one quarter of evaluation objects were replaced. The
32 KiB layout staged 171,283 new pack bytes versus 185,186 for deflate.
Rebuild and reopen left 974,848 versus 1,069,056 allocated bytes.
Both lanes still verified the original data through held-open readers.
Cohort blocks remain research-only. Ungated activation spent more bytes
on rejected entropy groups than it saved in compressed payload.

## Verification

Fresh reopen checked every live object's exact bytes. The fault checks
covered foreign scope, corrupt and missing dictionary references, missing
packs, a cut before index publication, stale generation, reader survival,
and releasing the last dictionary parent. The lab publishes a sealed
index after fsync and rename of the new pack. An open reader keeps file
descriptors for its original generation. This is a controlled stage cut.

Reads reopen the archive and request file-specific DONTNEED. Raw records
include actual input blocks. Page-cache eviction is advisory. The
benchmarks use two CPUs and exact content digests throughout.

[Reproduction](REPRODUCE.md) · [Fixtures](fixtures.jsonl) · [Raw evidence](results/)

## Native preset follow-up

Ordinary repack retained 8,519,680 bytes on the wide panel. Charging two
verified context copies, explicit per-record references retained 8,339,456
bytes and implicit pack-local references retained 8,200,192. The latter
also saved 20,480 bytes on the main panel. Held-out HTTPX stayed at 233,472
bytes and did not activate a dictionary. The operator supplies context to
`repack -preset-dictionary FILE -recompress -apply`. Existing stores and
new repacks without this flag use their existing layout. [Native follow-up](FOLLOWUP.md)
