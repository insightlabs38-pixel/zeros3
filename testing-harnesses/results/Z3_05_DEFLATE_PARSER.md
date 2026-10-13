# Z3-05: Native exact DEFLATE parser/replayer

Package `z3/deflate` (stdlib only, inside the Z3 research module; `zeros3.go`, the root `go.mod` and every persistent
format are untouched). Gate: `replay(parse(X)) == X` bit for bit, not merely equal inflation.

## What is modelled

`deflate.Parse(data, Options) -> *Stream`; `(*Stream).Replay(io.Writer)` / `Bytes()`.

| Item | Preserved exactly |
|---|---|
| block | BFINAL, BTYPE, plaintext span, bit span |
| stored | the padding bits (value and count) before LEN; LEN/NLEN (NLEN must equal ^LEN, so it carries no extra freedom); raw bytes |
| fixed | token stream; the fixed code is implicit |
| dynamic | HLIT, HDIST, HCLEN as transmitted (including a larger-than-necessary HCLEN / HLIT), the 19 code-length code lengths, the **actual code-length symbol sequence** with every 16/17/18 choice and extra-bit value, resulting literal/length and distance lengths |
| tokens | literal byte, or raw length symbol + length extra value + distance code + distance extra value (so length 258 coded as symbol 284+31 survives, and `Stats.NonCanonicalLengths` counts it) |
| stream tail | the padding bits after the final block |
| gzip | the original header bytes (never regenerated: MTIME, XFL, OS, FLG, extra, name, comment, FHCRC), the trailer bytes, multiple members; CRC32, ISIZE and header CRC are verified unless `ignoreChecksums` |
| zlib | header, Adler-32 (research input; preset dictionaries are rejected as unsupported) |
| ZIP | raw member payloads parse and replay on their own (`TestZipMemberPayloads`, lab rep `deflate-ir`); no container reconstruction |

The decoded plaintext is retained in `Stream.Plain` with a per-block span, ready for semantic CDC (Z3-06).
`Stream.Stats()` reports blocks by kind, tokens, literals, matches, dynamic-header bytes, token bits, code-length symbols
and a deliberately crude `NaiveIRBytes`.

## Validity and bounds

Rejected (all tested, each also rejected by `compress/flate` unless it is one of our added limits): reserved BTYPE 3,
truncation anywhere (every strict prefix of every test stream fails), NLEN != ^LEN, short stored data, HLIT > 286,
HDIST > 30, oversubscribed or incomplete literal/distance/code-length codes (the degenerate single length-1 code and an
empty distance code are allowed, as in `compress/flate`), repeat 16 with no previous length, repeats overrunning
HLIT+HDIST, missing end-of-block code, length symbols 286/287, distance symbols 30/31, a match reaching before the start
of output (including distance 32768 with only 32767 bytes), a match with no distance code, trailing data (unless
`AllowTrailing`; `Consumed` reports the boundary). Limits: `MaxOutput` (default 64 MiB), `MaxTokens` (32 Mi),
`MaxBlocks` (256 Ki) turn decompression bombs into `ErrLimit` instead of allocation.

Acceptance follows Go's `compress/flate`, the independent decoder oracle. Known differences from zlib: a code-length
code consisting of one length-1 symbol is accepted (zlib rejects incomplete code-length codes), which this parser and
`compress/flate` accept; streams start with an empty window (no preset dictionary). **Not supported:** preset
dictionaries (raw, zlib FDICT), Deflate64, a streaming/windowed parser (input is in memory and the plaintext of the whole
stream is retained, bounded by `Options`), and gzip header fields beyond RFC1952 flags (reserved flag bits are rejected).

## Evidence

Tests (`go test ./deflate`, ~2 s): handcrafted fixtures built with an independent bit assembler (stored incl. non-zero
padding and max length, empty stream, empty blocks of each kind, tail padding, every match length 3..258 with overlap,
every distance-code boundary up to 32768 across a block boundary, length 258 as 284+31, the same trees spelled with 3
code-length strategies x 2 HCLEN x 2 HLIT widths (12 spellings; at least 8 distinct bitstreams asserted), all replaying exactly, a repeat
crossing the literal/distance boundary, no-distance and single-distance-code dynamic blocks, EOB-only block, full
286/30 alphabet, mixed stored/fixed/dynamic blocks); real captures from zlib (default, Z_FIXED, Z_HUFFMAN_ONLY, Z_RLE,
level 0) and GNU gzip; Go's compressor at all 11 levels over 11 input shapes including the 32 KiB window edge; the
fixed-Huffman emitter; every `deflate-*`, `gzip-L6-insert-mid` and ZIP member from the corpus; wrapper tests; malformed
suite; limits; a 6000-case deterministic mutation test (4355 accepted, 1645 rejected) asserting accept/reject agreement
with `compress/flate`, identical plaintext, and exact replay. Mutating the replayer (dropping stored or tail padding,
canonicalising length 258, removing the window check) makes tests fail.

Fuzz targets: `FuzzParse` (bytes), `FuzzMutatedSeed` (structured edits of 14 valid seeds), `FuzzGzip`; invariants:
no panic, bounded work, acceptance parity and plaintext equality with `compress/flate`, exact replay of the consumed
bytes, strict/lenient trailing-data consistency, stats consistency. Seeds run in normal `go test`; smoke run 20 s per
target found nothing. A longer campaign (FuzzParse 20 min, FuzzMutatedSeed 20 min, FuzzGzip 8 min, 2 workers) was started separately; its outcome is recorded at the end of this section.

Lab integration: representation `deflate-ir` (`Plan.NoCost`: parse success, exact replay by SHA-256, plaintext and
structural counters, parse/replay time; physical cost and comparisons are **N/A**, marked with a dagger in tables).
`z3deflate -corpus DIR` prints the table below. Exactness over the whole stdlib corpus (27 families, 108 objects), the
optional external-producer corpus (zlib levels/memLevels/strategies, GNU gzip, Info-ZIP: 117 objects, including the
streams Preflate rejected) and 10 real PyPI wheels/sdists: all parsed and replayed bit-identically. The native plaintext
also agrees with Preflate's (`TestNativeParserAgreesWithPreflate`).

## Structure at 4 MiB scale (3 update revisions + base, plaintext 48 MiB/family)

| family | fmt | streams | comp MiB | plain MiB | blocks S/F/D | tokens | lit% | avg match | dyn hdr % of comp | naive IR % of comp | parse MiB/s | replay MiB/s | exact |
|---|---|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|---:|---|
| deflate-L1-insert-mid | deflate | 4 | 16.90 | 48.00 | 0/4/85 | 12163811 | 52.3 | 7.6 | 0.0 | 150.5 | 62 | 226 | 4/4 |
| deflate-L6-append | deflate | 4 | 15.66 | 48.01 | 0/4/12 | 8827415 | 23.9 | 7.2 | 0.0 | 177.3 | 82 | 207 | 4/4 |
| deflate-L6-insert-begin | deflate | 4 | 15.66 | 48.00 | 0/4/70 | 8825070 | 23.9 | 7.2 | 0.0 | 177.4 | 80 | 209 | 4/4 |
| deflate-L6-insert-mid | deflate | 4 | 15.65 | 48.00 | 0/4/36 | 8815876 | 23.8 | 7.2 | 0.0 | 177.4 | 87 | 233 | 4/4 |
| deflate-L6-replace | deflate | 4 | 15.65 | 48.00 | 0/4/64 | 8820655 | 23.9 | 7.2 | 0.0 | 177.3 | 83 | 254 | 4/4 |
| deflate-L9-insert-mid | deflate | 4 | 15.53 | 48.00 | 0/4/273 | 8916215 | 29.3 | 7.6 | 0.1 | 170.2 | 81 | 248 | 4/4 |
| deflate-fixed-insert-mid | deflate | 4 | 21.97 | 48.00 | 0/560/0 | 9160209 | 7.5 | 5.9 | 0.0 | 151.7 | 86 | 209 | 4/4 |
| gzip-L6-insert-begin | gzip | 4 | 15.66 | 48.00 | 0/4/40 | 8823077 | 23.9 | 7.2 | 0.0 | 177.4 | 67 | 237 | 4/4 |
| gzip-L6-insert-mid | gzip | 4 | 15.65 | 48.00 | 0/4/68 | 8822251 | 23.9 | 7.2 | 0.0 | 177.4 | 86 | 255 | 4/4 |
| zip-deflate-change | zip | 44 | 9.18 | 16.00 | 96/44/96 | 1939368 | 27.1 | 7.2 | 0.0 | 63.9 | 112 | 304 | 4/4 |
| zip-deflate-meta | zip | 44 | 9.18 | 16.00 | 96/44/92 | 1941464 | 27.2 | 7.2 | 0.0 | 64.0 | 115 | 262 | 4/4 |

Total 166.7 MiB compressed / 464 MiB plaintext in one run.

- Go L6 text: 24% literals, 7.2-byte average match, ~0.54 tokens per compressed byte, dynamic headers only ~0.03% of
  compressed bytes (blocks are large). Block count differs sharply by producer (per 12 MiB stream: Go L6 ~9 dynamic
  blocks, Go L9 ~68, the fixed emitter 140).
- The naive explicit IR is **150-177% of the compressed size** for single streams (4 B/match + 2 B/literal run), ~64% for
  the ZIP of many small members that are stored or highly compressible. An uncompressed token IR is therefore not
  a candidate; Z3-06 needs entropy coding or prediction. Preflate's measured blob (Z3-04) corresponds to ~1.9 bits per
  token for Go L6 (12.9% of compressed bytes over 2.2 M tokens per stream) and ~0.9 for Go L9; for zlib-family streams
  it is roughly 5x smaller than for Go L6.

## Performance (single thread, 4-core VM shared with a background fuzzer, so conservative)

- Parse: 62-115 MiB/s of plaintext (~25-30 MiB/s of compressed input) on the corpus; `compress/flate` inflates the same
  16 MiB text stream at 88 MiB/s in the same harness (`BenchmarkParse` 71-86, `BenchmarkStdlibInflate` 88). No
  Huffman micro-optimisation was done beyond a 9-bit fast table.
- Replay: 160-300 MiB/s of plaintext (~70-100 MiB/s of compressed output), >1 GiB/s for stored blocks.
- Memory: a parsed 16 MiB-plaintext / 5.2 MiB-compressed stream retains 31.8 MiB (plaintext + 4 bytes per token for
  2.9 M tokens); parsing allocates about 10x the plaintext transiently (append growth). Output is bounded by `Options`;
  the in-memory design is the stated limitation.

## Real-world samples (side track)

`realworld/sources.json` pins 15 hash-verified public samples (Pythia-14M safetensors checkpoints at five training
steps, ~28 MB each, Apache-2.0; `six` wheels and sdists 1.12-1.16 from PyPI, MIT); `z3fetch -group G -out DIR` caches
them outside Git, verifies SHA-256 and lays out a corpus. Only the wheels/sdists were fetched in this run (small): the
native parser replays all of their DEFLATE streams exactly, and Preflate handles them (sdist blobs 0.1% of compressed
bytes, wheels 3.9%), but versions share almost no content at 10 KB scale, so they are parser evidence, not a reuse
benchmark. Pythia checkpoints were not downloaded here. Unverified candidates for later batches are listed in
`realworld/README.md`.

## Recommendation for Z3-06

1. The correctness foundation holds; build the compact explicit IR on top of `deflate.Stream` rather than a second parser.
2. The measured Preflate blob is ~2 bits/token for non-zlib producers and far lower for zlib-family ones, while the
   naive IR is >1.5x the stream. An explicit IR is only competitive if it is a *predicted* encoding or models
   block-local structure; the first Z3-06 experiment should therefore measure the entropy of the exact token
   stream conditioned on simple predictors (previous-block parameters, longest-match rank, literal/match choice), using
   `Stats` and the per-block spans, and compare bits/token against Preflate's 0.4-2 bits.
3. Make recon blocks local to plaintext chunk boundaries (cut at DEFLATE block boundaries, which are explicit here) so that
   an edit invalidates only the blocks it touches; Z3-04 showed monolithic blobs defeat that.
4. Keep the per-object fallback (CDC when the transform loses, e.g. appends) and the "valid input must parse" rule:
   this parser accepts streams (Go Huffman-only) that Preflate rejects.
