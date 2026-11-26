# ZeroS3 C15 · Compact verified posting runs

**C15_INDEX_EFFECTIVE.** Adaptive posting directories reduced index
allocation while preserving the C7 candidate sequence and selected
content plans. Read-only status and inspection can run beside a live
server. Mutating index operations retain exclusive store ownership.

## Allocation and incremental change

Both binaries used the C7 indexed one-target policy and identical data,
scope and finalization. The control was C14's fixed-run implementation.
The first three histories used four indexed candidates apiece and picked
identical changes and component bytes. The cross-format lane nominated
zero indexed candidates and made no new authority change in either build.

| Workload | All-generation index before latest apply | Final data excluding index | Latest optimizer CPU |
| --- | ---: | ---: | ---: |
| Binary, 64 unrelated | 294,912 → 36,864 B | 1,695,744 B | 0.225 → 0.210 s |
| Binary, 1,024 unrelated | 495,616 → 249,856 B | 6,434,816 B | 0.377 → 0.295 s |
| 32 binary revisions | 6,696,960 → 409,600 B | 2,895,872 B | 0.098 → 0.084 s |
| TypeScript gzip and tar | 286,720 → 24,576 B | 9,080,832 B | 3.852 → 3.911 s |

Both 32-version lanes started with eight revisions and refreshed 23
individual additions before the latest indexed optimization. Median
refresh CPU fell from 21.2 to 13.9 ms. Following the final append and
tombstone, pruning inactive generations left 1,093,632 versus 45,056
index bytes. Refresh still copies the full catalog. Each fresh query
verifies its applicable journal prefix. Opening the dense store's
hint fell from 2.05 to 1.50 ms in the paired optimizer run. Cross-format
source reads and analysis still dominate.

## Lookup and freshness tradeoffs

ZSR2 uses 0, 8 or 12 radix bits in compact runs and retains ZSR1's
16-bit layout for large runs. It preserves all feature bits, newest-ID
order, digests, zero padding checks and a bounded search. A 16-posting
run uses 228 B instead of 262,332 B. At 16,384 postings, the adaptive
run used 181,280 B versus 426,012 B. Verified open fell from about 806 to
324 µs. Lookup p95 rose from 2.64 to 4.12 µs, and 768 lookups read
0.46 MB versus 0.09 MB. Equality against an independent expected posting
list held across all tested sizes, skew and lookup misses.

Sparse 16-bit directories with explicit pairs or a bitmap had more
metadata in the small and medium modeled cases. A flat sorted layout
reduces directory space for tiny runs, with more bytes per posting and
more positioned operations as a run grows. The selected widths bound
average bucket occupancy and keep the existing hot-key probe limit.

During 12 concurrent status probes and four-client signed GETs, GET
p99 rose from 20.24 to 36.68 ms in one loopback pair. Median status was
14.17 ms. The previous CLI refused concurrent status. Shared status
reported an active index and then refused the same index after a signed
namespace update. This is short-run interference data.

## Namespace and verification

The metadata-only checkpoint opened 262,144 entries in 65.95 ms
versus 508.66 ms for replay. Checkpoint and journal sizes were 24.68
and 131.16 MB. The checkpoint format stayed the same. The probe
bounds metadata overhead independently of any object-serving claim.

The current reader refreshed a prior generation into mixed ZSR1/ZSR2
runs, with the old generation ID preserved. A previous reader fell
back for optimizer hints and retrieved every surviving object exactly.
The focused suite covered independent posting equality, malformed
features, scope aliases, multipart/copy, pruning, snapshot pins,
rebuild after corruption, stale hints, compaction, and real SIGKILL at
generation and pointer publication. Both builds deep-verified and
returned the exact full content set. Derived indexes remain rebuildable
and can be pruned independently of data.

[Reproduction](REPRODUCE.md) · [Fixture digests](fixtures.json) · [Raw rows](results/)
