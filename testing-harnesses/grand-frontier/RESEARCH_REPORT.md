# Exact compressed histories under physical storage and read constraints

## Abstract

Byte-exact object storage must preserve the compressor's output and
serve ranges of those output bytes. Sharing the underlying plaintext
can save storage while making requests depend on an expensive
reconstruction. We measure physical closure costs and compare two
compiled representations. Bit-span programs copy compressed bits from
retained source records. Token translation decodes selected source
DEFLATE runs and emits their exact target codes. Distance corrections
recover reuse when Huffman tables and LZ distances both change. On the
adjacent held-out TypeScript pair, signed translation occupied 5.68 MB,
versus 5.18 MB for the archive. Sparse and whole p95 were 3.65 and 241 ms
versus 31.84 and 1,269 ms. On the development pair it also used less
space than the archive. The experiment supports history-specific
selection with a distinct fast CDC point and a separate large-object gap.

## Related work and matched controls

Preflate observes compressor choices and stores enough information to
reproduce the original bitstream from inflated data. The token program
here keeps original compressed source records and reads indexed slices
to generate one target compressed block. This is distinguished by the
physical closure and compressed output range contract. Canonical
reconstruction and B10 splice already share reversible intermediates
in ZeroS3. GF7 directly compares the current archive, including those
incumbent candidates, and the strongest local exact bit-patch variant.
GF8 also runs zstd 1.5.7 with a full base and patch-from at two levels
on the same original gzip bytes. GF5 includes independently encoded
zstd, XOR and integer word-delta.
GF3 compares current multi-source factoring with native XD programs.
Physical costs and restore contracts differ from those in backup,
structured entropy coding, and model-hub studies. We report their
published figures with their scope and avoid direct numerical rankings.
[Prior art and links](PRIOR_ART.md)

## Method

Let each live root choose exactly one feasible representation edge.
An edge has a direct cost and an immutable, digest-addressed dependency
closure. The objective charges the union of transitive records once,
with allocation boundaries, metadata, retained copies and read caps.
An exact small oracle compares bounded beam and cohort selection. The
native revision measures actual materialization and separate signed
latency. Its restricted TypeScript cost estimate was within one 4 KiB
block of measured savings. The overlap example shows why independent
root decisions can lose globally. [GF1](GF1/README.md)

GF2 indexes shifted compressed bits and emits independently verifiable
32 KiB output programs. GF7 uses each target DEFLATE block as a replay
unit. One variant copies equal decoded tokens and emits them via the
target's original tables. The other permits matches with equal literals
or length representation and specifies each changed distance as an
XOR correction. Match cost includes source offsets, side information
and pack alignment. The upper bound on gross savings for a match is the
number of target code bits its run replaces. One compressed index
specifies source locations, target block syntax and exact sizes. Each
program competes with retaining its masked raw compressed bytes.
[GF2](GF2/README.md) · [GF7](GF7/README.md)

## Correctness and recovery

For a well-formed frame, source code decoding yields the recorded LZ
tokens and XOR corrections recover their exact target distance fields.
Re-emitting the target tables and tokens in sequence recovers the
original code bits, including the end-of-block code. Every target frame
has an authenticated digest over its masked bytes. Overlapping
boundary bytes reconstruct by combining disjoint bit masks. The gzip
envelope and original padding remain in the representation. Whole
reads and authority proof require the original object identity.
Publication makes the closure durable before the authority record.
Pruning follows digest reachability from live roots and snapshots.
The reader enforces format features and bounds input extent, expansion
and replay before serving contributing bytes.
[Recovery cases](GF8/README.md)

## Evaluation

Two pinned TypeScript releases are the development sequence. A third
forms the independent adjacent target, with the preceding release as an
explicit shared anchor. urllib3 supplies a different held-out release
family. A changed compressor and independent entropy probe the benefits
of finding shared syntax. The corrected translator reduced TypeScript
held-out allocation by 33.2% versus direct storage in a matched native
build. Signed authority was measured separately with namespace metadata
and real request verification. Held-out translation cost
1.42 CPU-seconds to plan and prove, versus 26.40 for the archive on the
same signed harness. The small compressor-settings case reached the
same 24,576-byte physical floor for both programs and direct storage.
On development TypeScript GF2 retained a faster whole-read operating
point at a higher physical cost. With two signed workers doing small
ranges, whole reads, HEADs and PUTs, CDC whole p99 was 11–14 ms,
bit patch 39–118 ms, translation 228–341 ms and archive 2,277–2,324 ms.
Process I/O accounted for preparation writes that a final-pack
counter omits. [Mixed evidence](GF8/results/mixed.jsonl)

The level-19 zstd files use 8,269,824 B
for the adjacent pair and reconstruct both files in 16.1 ms in a standalone
process. They provide no compressed byte-range contract or native
namespace, repair or retention metadata. [GF7 receipts](GF7/results) ·
[Standalone zstd rows](GF8/results/zstd.jsonl)

The native phrase bank won on deliberately shared synthetic passages.
It lost to existing representations on SQLite, Parquet and HTTPX after
charging program and page bytes. GF4's exact hindsight identifies when
hot-residency changes can pay, while competing online policies both
missed the signed latency target under churn. GF5's adaptive tensor
residuals improved on independent XOR and integer delta, while native
ZN7 remained smaller and faster for whole checkpoint reads. Receiver-aware
transfer in GF6 amortizes repeated basis bytes for a prepared destination.
At high emulated delays, an ordinary relay overtakes the staged protocol.
[GF3](GF3/README.md) · [GF4](GF4/README.md) · [GF5](GF5/README.md) · [GF6](GF6/README.md)

## Limitations and reproduction

Repeated reversed-order and layered cold, warm and mixed probes expose
sensitivity to cache and queueing. With limited version pairs and tail
samples we report descriptive percentiles and round ranges. Benchmarks
share one local filesystem and a two-worker Go quota. This does not
measure fleet contention, NVMe durability behavior, actual battery-backed
caches or production WAN routing. The experimental Go reader is bounded
to small artifacts, and the encoder remains a research entry point.
Dependency loss can be repaired using the object-scoped signed closure.
Generic digest repair has a discovery gap on retired paths. The online
selection and background maintenance gate needs further evidence.
Fixture hashes, raw per-lane metrics, and the bounded selected suite
are in the repository. [Reproduction](GF8/REPRODUCE.md)
