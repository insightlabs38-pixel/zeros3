# DF0 · Independent revalidation of compressed-output translation

DF0 profiles the existing GF7 reader and compares it with CDC, the
current canonical archive, and GF2 bit patch on exact upstream gzip
bytes. The formats, encoding decisions and production behaviors are
held fixed. The two frozen nginx histories confirm a compact and faster
operating point versus archive. Retained history and client concurrency
place clear limits on its use. The timed workloads use disposable
native stores with signed S3 requests. [Preregistration](PREREGISTRATION.md) ·
[Path audit](MEASUREMENT.md) · [Reproduction](REPRODUCE.md)

## Corpus and reproduction

Twelve upstream archives were pinned before evaluation, including hashes
of the original gzip and the full inflated tar. curl and Lua are fresh
validation. Python is the large-object scope control. nginx was reserved
for confirmation, outside harness development and selection. The frozen
code revision was `4f4b1ec9844d61441ed504075b2108dca954bdea`.
curl and Python show gzip XFL 2, while Lua and nginx show XFL 0.
Those are observed header values. The public artifacts do not identify
the packagers' complete settings. [Corpus and provenance](corpus.json)

GF7's old TypeScript and urllib3 data were rerun in clean stores and
reproduced physical allocation exactly. TypeScript v1/v2 was already
seen before DF0. Latency ordering was stable against archive, with
meaningful run-to-run variation in sparse and mixed tails.
[Original replay](ORIGINAL_REPLAY.md)

## Fresh native comparisons

| History | CDC B | Archive B | GF2 B | GF7 B | GF7 / archive signed mixed whole p95 |
| --- | ---: | ---: | ---: | ---: | --- |
| curl 8.10.0–8.10.1 | 8,306,688 | 5,267,456 | 6,217,728 | 4,616,192 | 208–217 / 2,345–2,381 ms |
| curl 8.10.1–8.11.0 | 8,343,552 | 5,566,464 | 8,126,464 | 5,758,976 | 229–232 / 2,296–2,314 ms |
| curl 8.11.0–8.11.1 | 8,384,512 | 5,382,144 | 7,524,352 | 4,866,048 | 219 / 2,373–2,392 ms |
| curl four releases and snapshot | 16,666,624 | 6,467,584 | 21,811,200 | 15,179,776 | 214–226 / 2,494–2,520 ms |
| Lua 5.4.6–5.4.7 | 765,952 | 479,232 | 679,936 | 512,000 | 27–29 / 173–178 ms |
| Lua 5.4.7–5.4.8 | 778,240 | 466,944 | 561,152 | 442,368 | 23–26 / 166–169 ms |
| nginx 1.27.2–1.27.3 | 2,564,096 | 1,576,960 | 2,338,816 | 1,449,984 | 69–88 / 1,089–1,132 ms |
| nginx 1.27.3–1.27.4 | 2,580,480 | 1,572,864 | 2,326,528 | 1,429,504 | 64–84 / 1,057–1,075 ms |
| Python 3.13.0–3.13.1 | 58,523,648 | 35 s budget | 8 MiB cap | 8 MiB cap | Unavailable |

Each row uses the full final allocation of the native store. Archive
has the smallest four-release footprint. GF7 uses 2.35 times as much
space there, while signed whole p95 is about one tenth of archive's.
In a short curl pair, GF7 uses less space and reads faster than archive,
while GF2 provides a faster whole-read point at greater retained cost.
The two Lua transitions show both orders of space ranking.
On nginx, the confirmed point uses 8.1% and 9.1% less space than
archive. The sparse p95 was 4.02–4.54 ms versus 60.43–71.20 ms.
GF2 and CDC remain faster than GF7 on these two histories in this mix.
[Frozen confirmation rows](results/nginx.jsonl)

The Python objects are about 28 MiB each. Both compiled readers are
limited to 8 MiB. The archive optimizer stopped cleanly after 35.15
seconds of wall time, so only the bounded CDC control is compared there.
[Detailed curl results](CURL.md) · [Raw validation](results)

## Read cost and workload sensitivity

Live signed CPU samples attribute 81.8% of whole-read CPU to frame
replay. Instruction varint parsing consumed 25.1% cumulative CPU.
Input token decoding and target code emission each consumed 23.2%
cumulative CPU in separate branches. The output bit writer was the
hottest single leaf. A Lua heap profile isolates 98% of estimated
allocation objects to the parser's canonical-varint width check.
On the same released curl pair, GF7 whole p95
was 203–220 ms at 2 clients and 2.54–2.81 s at 32 clients on two cores.
Cold pages raised the first small-read latency. The whole-read gap
between matched cold and warm first requests was much smaller.
[Load, cold and cost decomposition](LOAD.md)

With 384 whole reads per Lua order, GF7 whole p99 was 142.23 ms
with a descriptive block interval of 73.07–155.37 ms, and 32.87 ms
with an interval of 27.92–41.51 ms. GF2 recorded 4.35–4.84 ms p99.
Two isolated GF7-only repeats measured 60.04 and 44.13 ms p99.
The high-tail regime did not reproduce in isolation. This comparison
does not identify a specific cause or establish a stable Lua p99.
[Tail intervals](results/tail-summary.jsonl) ·
[Isolated repeats](results/tail-isolated.jsonl)

## Adverse controls and recovery

A source history can be unfavorable despite near-identical plaintext.
A single tar-member edit inside the generated in-place control retained
434,176 B in GF7 versus archive's 237,568 B. Reordering and changing
compressor level also favored archive. Unrelated entropy added 8,192 B
over CDC. The concatenated gzip fixture contains two valid members and
was refused by the current single-member GF7 grammar.

GF7 closed its exact source and target through native proof and
verification. A separate fault in a finalized store made an actual frame
unavailable. Generic repair found one item and returned without error.
The missing frame remained unavailable. Object-scoped signed recovery
restored it, and exact whole and range reads passed after reopen. This
receipt is an input to DF5. [Recovery and adverse evidence](results)

The focused race group passed actual old-reader refusal, native
publication cuts, cold reopen, relocation, retention and signed repair.
Three decoder fuzz targets completed 6,000 executions each.
The original and fresh confirmation runs completed with no skips.
[Verification receipts](results/verification.jsonl)

## Prior-art boundary

Preflate reproduces exact DEFLATE bytes from an inflated representation
and the recorded compression choices. GF7 retains digest-addressed
compressed source runs across releases and locally reconstructs the
target's exact compressed output ranges using their original tables.
GF2 reuses unchanged bit spans and avoids token execution for those
copies. The canonical ZeroS3 archive can retain shared semantic content
more compactly over longer histories. The DF0 contribution here is
independent measurement of those contracts. [Preflate source](https://github.com/deus-libri/preflate) ·
[Grand Frontier related work](../../grand-frontier/PRIOR_ART.md)

## Decision

GF7 has a reproducible specialized point on short compressed histories.
Future admission must charge retained original bases, serial replay
CPU and foreground concurrency. The strongest adverse cases are the
four-release snapshot footprint, overloaded two-core mixed reads, and
unsupported large or multi-member gzip. This study did not evaluate
device power loss, fleet contention or broad archive-size generality.

DF0 is complete as research. The measured handoff is the allocation and
CPU cost of canonical instruction-varint validation inside exact replay.
The report leaves the codec and its admission policy as measured.
