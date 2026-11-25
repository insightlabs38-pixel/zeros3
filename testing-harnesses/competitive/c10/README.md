# ZeroS3 C10 · Exact compressed reads

**C10_SPECIALIZED.** `ZEROS3_GZIP_SEQUENTIAL=1` opts canonical gzip whole
reads into one traversal of the verified plaintext body. The existing codec
decodes bounded units ahead of the output writer. It checks DEFLATE bit
alignment and gzip CRC/ISIZE, and the whole reader verifies original SHA-256.
The policy changes no retained representation, graph edge, or dependency.

## Matched end-to-end results

Read medians below are medians of the two per-pair p50 values. All requests
were signed S3 reads with exact slice or object digests. Finalized allocation
includes namespace, records, live pack slack and transitive dependencies.

| TypeScript, allocated bytes | C9 | C10 | First-pass whole p50, C9 → C10 |
| --- | ---: | ---: | ---: |
| CDC, 1 client | 12,689,408 | 12,689,408 | 8.7 → 8.4 ms |
| Canonical, 1 client | 5,296,128 | 5,296,128 | 1,136 → 914 ms |
| Canonical, 4 clients | 5,296,128 | 5,296,128 | 4,347 → 3,124 ms |
| Deep, 1 client | 5,296,128 | 5,296,128 | 1,173 → 923 ms |
| Deep, 4 clients | 5,296,128 | 5,296,128 | 4,474 → 2,674 ms |

Canonical and deep selection converged on three gzip wrappers and two
plaintext deltas. Both retained 58.3% less than CDC. In the canonical
one-client lane, server CPU fell 2,127 → 1,682 ms per first-pass request.
The four-client warm p95 remained close to 4.65 s. The canonical lane had
24 samples per phase and pair and the deep lane 6 whole samples per phase
and pair, so the canonical tail carries more evidence.

Fresh server processes reset decoded state. The Linux page cache was
ambient. Process read telemetry includes non-storage reads. Each TypeScript
read matrix includes 4 KiB, 64 KiB and 1 MiB ranges and same-offset repeats.
A requested range covering a smaller object's complete byte span follows
the whole path. The held-out HTTPX archives retained 270,336 bytes versus
364,544 CDC. Their canonical first-pass whole p50 fell 48.2 → 27.3 ms at
one client, with roughly the same CPU. Four-client HTTPX results varied
by lane and phase. RuboCop deep replay used 925,696 allocated bytes versus
1,372,160 CDC, with little whole-read change from this gzip-root policy.
Parquet and SST retained 712,704 and 3,878,912 bytes. ZIP and the entropy
control picked the CDC incumbent at 1,921,024 and 12,689,408 bytes.

## Replay work and selection

The TypeScript Store profile measured 1.161 → 0.976 s/op and
115.09 → 91.39 MB of Go heap allocations per whole read. Match-chain
search accounted for 65% of C9 CPU samples and 68% in C10. The two groups
use the same retained bytes. The decoder still expands about 22.6 MB of
plaintext per package before reproducing about 4.2 MB of original gzip.
Socket-inclusive process read counters cannot substitute for that codec
work. Four-iteration sparse profiles are diagnostic.

**SPECIALIZED** for sequential whole replay. Keeping the existing default
preserves the baseline sparse and whole behavior for unprofiled stores.
Whole server CPU divided by the compressed output for TypeScript is about
0.51 → 0.40 µs/byte. Single-client HTTPX used about 50 ms per object in
either mode. These are measured calibration points for workload-specific
optimizer pricing. A shared coefficient would fold application-family
and replay-schedule differences into the default model, which is unchanged.

**KILL** for the early-stop predictor experiment. Halting candidate
search once the output cap made subsequent candidates irrelevant yielded
effectively the same single-client times and CPU in both TypeScript pairs.
The applied production path contains no predictor change. Pricing a full
materialization would add 12.58 MB of logical duplicate TypeScript bytes
before its own physical framing, beyond the existing retained savings.
The match-chain work and four-client tails are the remaining priorities.

## Verification and limits

The four-client sparse tail probe ran 120 operations per phase and binary.
First-pass 4 KiB p99 was 125 → 144 ms and 64 KiB p99 was 261 → 219 ms.
The same-offset p99 values were 83 → 101 ms and 203 → 202 ms. This single
pair shows timing variance on the unchanged sparse route. Whole-read
samples support p50 and indicative p95, with insufficient data for p99.

Focused tests passed for canonical reopen, cancellation, missing source key
with preserved dependency, range reconstruction, corrupted seek indexes,
and structured delta and splice failure. The broad race run timed out in
archive candidate re-analysis. A reader-only race test on the finalized
HTTPX store then passed concurrent exact whole and overlapping range reads.

[Reproduction](REPRODUCE.md) · [Fixtures](fixtures.json) · [Raw results](results/)
