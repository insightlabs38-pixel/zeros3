# Z3-04: Preflate oracle and DEFLATE characterization

Evidence only. Synthetic data, one seed, 3 update revisions (2 for the external corpus). Oracle provenance, pin and
build: `z3/oracle/preflate/README.md`. Everything below is reproducible with:

```sh
z3/oracle/preflate/setup.sh
go run ./cmd/z3corpus gen -out C -size-kib 256 -families 'deflate-*,gzip-*,zip-deflate-*'     # small
go run ./cmd/z3corpus gen -out B -size-kib 4096 -families '<11 families, see below>'           # scale
go run ./cmd/z3corpus ext -out E                                                               # optional producers
go run ./cmd/z3oracle -corpus C -ext E        # also: -corpus B
```

## Method

For every raw DEFLATE stream (raw `.deflate`, the payload of a gzip member, each method-8 ZIP member payload),
`preflate-rs` returns the plaintext and one reconstruction blob. The `preflate` representation stores the plaintext
CDC-chunked in the family store (new chunks cost bytes, repeated chunks are reuse, same cumulative-family accounting
as Z3-02), charges the blob as per-revision recipe metadata, keeps gzip header/trailer inline, and CDC-stores any region
the oracle rejects. `preflate-rcdc` additionally CDC-chunks the blob into the store (reuses identical blobs and
shared chunks). Every row is reconstructed through the store and compared by length and SHA-256 to the original object,
and `Rebuild` independently requires the oracle's output to equal the original stream bytes (a unit test with a
deliberately wrong oracle confirms this is detected). A semantically equivalent but different stream would be `invalid`.
Preflate's own internal verify is disabled so analysis time is not inflated. All reported rows below reconstructed
bit-exactly (`exact` = rows exact / rows built); including fallback rows.

Costs: update revisions r1..rN, production pack rule applied to new CAS components (recon blobs are not packable),
percent of the family's compressed bytes, 96 B per component reference. `preflate%` = total (new plaintext CAS bytes +
recon blob + other = recipe refs, wrapper bytes). `plain-oracle` = the Z3-02 estimate (CDC over plaintext, no
reconstruction information). Times are tool-internal ms per MiB of compressed input (single-threaded, no I/O).

## Stdlib guaranteed corpus, 256 KiB base (plaintext 0.75-1.5 MiB/revision)

| family | fmt | comp MiB | plain MiB | cdc% | plain-oracle% | preflate% (new plain + recon + other) | preflate-rcdc% | vs cdc | recon/comp% | stream ok | analyze ms/MiB | recreate ms/MiB | exact |
|---|---|---:|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| deflate-L0-insert-mid | deflate | 2.25 | 2.25 | 18.2 | 4.8 | 4.8 (4.7 + 0.0 + 0.1) | 4.8 | 26% | 0.0 | 3/3 | 27 | 9 | 3/3 |
| deflate-L1-insert-mid | deflate | 0.80 | 2.25 | 100.1 | 7.9 | 28.1 (7.5 + 20.2 + 0.4) | 28.1 | 28% | 20.2 | 3/3 | 157 | 112 | 3/3 |
| deflate-L6-append | deflate | 0.74 | 2.26 | 6.8 | 6.8 | 19.6 (6.4 + 12.8 + 0.4) | 19.6 | 287% | 12.8 | 3/3 | 134 | 81 | 3/3 |
| deflate-L6-delete | deflate | 0.74 | 2.25 | 79.3 | 18.0 | 30.8 (17.6 + 12.8 + 0.4) | 30.9 | 39% | 12.8 | 3/3 | 139 | 93 | 3/3 |
| deflate-L6-incompressible | deflate | 0.75 | 0.75 | 73.0 | 29.0 | 29.0 (28.9 + 0.0 + 0.1) | 29.1 | 40% | 0.0 | 3/3 | 27 | 7 | 3/3 |
| deflate-L6-insert-begin | deflate | 0.74 | 2.25 | 100.2 | 4.1 | 16.8 (3.6 + 12.8 + 0.5) | 16.9 | 17% | 12.8 | 3/3 | 136 | 80 | 3/3 |
| deflate-L6-insert-mid | deflate | 0.74 | 2.25 | 56.7 | 46.2 | 59.0 (45.9 + 12.7 + 0.3) | 59.0 | 104% | 12.7 | 3/3 | 150 | 88 | 3/3 |
| deflate-L6-multirev | deflate | 0.74 | 2.25 | 51.6 | 15.5 | 28.3 (15.2 + 12.8 + 0.3) | 28.3 | 55% | 12.8 | 3/3 | 210 | 104 | 3/3 |
| deflate-L6-repetitive | deflate | 0.43 | 4.50 | 79.1 | 17.3 | 26.5 (16.8 + 9.2 + 0.5) | 26.6 | 33% | 9.2 | 3/3 | 385 | 275 | 3/3 |
| deflate-L6-replace | deflate | 0.74 | 2.25 | 75.7 | 17.4 | 30.2 (17.1 + 12.8 + 0.3) | 30.2 | 40% | 12.8 | 3/3 | 142 | 96 | 3/3 |
| deflate-L9-insert-mid | deflate | 0.73 | 2.25 | 91.5 | 9.5 | 15.4 (9.2 + 5.9 + 0.3) | 15.4 | 17% | 5.9 | 3/3 | 124 | 70 | 3/3 |
| deflate-fixed-insert-mid | deflate | 1.04 | 2.25 | 76.4 | 24.0 | 24.0 (23.8 + 0.0 + 0.2) | 24.1 | 31% | 0.0 | 3/3 | 78 | 27 | 3/3 |
| deflate-huffonly-insert-mid | deflate | 1.33 | 0.00 | 31.7 | 9.1 | 31.7 (31.5 + 0.0 + 0.2) | 31.7 | 100% | 0.0 | 0/3 | 0 | 0 | 3/3 |
| gzip-L1-insert-mid | gzip | 0.80 | 2.25 | 51.6 | 15.7 | 35.9 (15.3 + 20.2 + 0.4) | 35.9 | 70% | 20.2 | 3/3 | 182 | 125 | 3/3 |
| gzip-L6-append | gzip | 0.74 | 2.26 | 18.8 | 4.8 | 17.6 (4.4 + 12.8 + 0.4) | 17.7 | 94% | 12.8 | 3/3 | 168 | 90 | 3/3 |
| gzip-L6-delete | gzip | 0.74 | 2.25 | 65.1 | 11.6 | 24.3 (11.1 + 12.7 + 0.5) | 24.3 | 37% | 12.7 | 3/3 | 144 | 78 | 3/3 |
| gzip-L6-incompressible | gzip | 0.75 | 0.75 | 72.5 | 45.6 | 45.6 (45.4 + 0.0 + 0.2) | 45.7 | 63% | 0.0 | 3/3 | 27 | 8 | 3/3 |
| gzip-L6-insert-begin | gzip | 0.74 | 2.25 | 100.2 | 8.8 | 21.6 (8.4 + 12.8 + 0.4) | 21.7 | 22% | 12.8 | 3/3 | 154 | 90 | 3/3 |
| gzip-L6-insert-mid | gzip | 0.74 | 2.25 | 100.1 | 18.4 | 31.2 (18.0 + 12.8 + 0.4) | 31.2 | 31% | 12.8 | 3/3 | 167 | 97 | 3/3 |
| gzip-L6-repetitive | gzip | 0.43 | 4.50 | 59.7 | 17.2 | 26.4 (16.7 + 9.1 + 0.5) | 26.4 | 44% | 9.1 | 3/3 | 449 | 294 | 3/3 |
| gzip-L6-replace | gzip | 0.74 | 2.25 | 85.6 | 14.5 | 27.3 (14.2 + 12.8 + 0.3) | 27.3 | 32% | 12.8 | 3/3 | 149 | 91 | 3/3 |
| gzip-L9-insert-mid | gzip | 0.73 | 2.25 | 65.3 | 11.8 | 17.6 (11.3 + 5.8 + 0.5) | 17.6 | 27% | 5.8 | 3/3 | 144 | 71 | 3/3 |
| zip-deflate-add | zip | 0.53 | 0.89 | 28.5 | 9.1 | 11.9 (7.6 + 3.2 + 1.1) | 9.6 | 42% | 3.2 | 39/39 | 134 | 44 | 3/3 |
| zip-deflate-change | zip | 0.45 | 0.75 | 50.4 | 13.5 | 16.2 (12.0 + 3.1 + 1.1) | 13.9 | 32% | 3.1 | 33/33 | 144 | 43 | 3/3 |
| zip-deflate-meta | zip | 0.45 | 0.75 | 100.1 | 1.9 | 4.4 (0.2 + 3.1 + 1.1) | 1.9 | 4% | 3.1 | 33/33 | 142 | 43 | 3/3 |
| zip-deflate-remove | zip | 0.39 | 0.61 | 100.1 | 1.5 | 4.0 (0.1 + 2.8 + 1.2) | 1.9 | 4% | 2.8 | 27/27 | 134 | 39 | 3/3 |
| zip-deflate-reorder | zip | 0.46 | 0.75 | 94.8 | 1.6 | 4.3 (0.1 + 3.1 + 1.1) | 1.9 | 5% | 3.1 | 33/33 | 132 | 45 | 3/3 |

Unsupported streams (fell back to CDC of the compressed bytes):
- deflate-huffonly-insert-mid map[read-deflate:3]

CDC granularity dominates at this scale: a 200-byte edit still re-stores one 16-256 KiB plaintext chunk, which is why
`plain-oracle` is 5-46% here. See the 4 MiB run for the regime the question is really about.

## Scale run, 4 MiB base (12 MiB plaintext/revision)

`deflate-{L6-insert-mid,L6-insert-begin,L6-append,L6-replace,L9-insert-mid,L1-insert-mid,fixed-insert-mid}`,
`gzip-L6-{insert-mid,insert-begin}`, `zip-deflate-{change,meta}`.

| family | fmt | comp MiB | plain MiB | cdc% | plain-oracle% | preflate% (new plain + recon + other) | preflate-rcdc% | vs cdc | recon/comp% | stream ok | analyze ms/MiB | recreate ms/MiB | exact |
|---|---|---:|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| deflate-L1-insert-mid | deflate | 12.67 | 36.00 | 50.2 | 0.6 | 21.0 (0.3 + 20.4 + 0.4) | 19.4 | 42% | 20.4 | 3/3 | 170 | 103 | 3/3 |
| deflate-L6-append | deflate | 11.74 | 36.01 | 2.4 | 0.5 | 13.5 (0.2 + 12.9 + 0.4) | 10.2 | 558% | 12.9 | 3/3 | 136 | 83 | 3/3 |
| deflate-L6-insert-begin | deflate | 11.75 | 36.00 | 100.1 | 0.6 | 13.6 (0.2 + 12.9 + 0.4) | 13.6 | 14% | 12.9 | 3/3 | 137 | 79 | 3/3 |
| deflate-L6-insert-mid | deflate | 11.74 | 36.00 | 52.3 | 0.9 | 13.9 (0.6 + 12.9 + 0.4) | 12.4 | 27% | 12.9 | 3/3 | 143 | 80 | 3/3 |
| deflate-L6-replace | deflate | 11.74 | 36.00 | 34.4 | 1.0 | 13.9 (0.6 + 12.9 + 0.4) | 14.0 | 40% | 12.9 | 3/3 | 138 | 86 | 3/3 |
| deflate-L9-insert-mid | deflate | 11.65 | 36.00 | 51.6 | 0.9 | 6.7 (0.5 + 5.9 + 0.4) | 6.8 | 13% | 5.9 | 3/3 | 126 | 77 | 3/3 |
| deflate-fixed-insert-mid | deflate | 16.48 | 36.00 | 50.5 | 0.6 | 0.6 (0.4 + 0.0 + 0.3) | 0.6 | 1% | 0.0 | 3/3 | 79 | 25 | 3/3 |
| gzip-L6-insert-begin | gzip | 11.74 | 36.00 | 100.1 | 0.9 | 13.9 (0.5 + 13.0 + 0.4) | 13.9 | 14% | 13.0 | 3/3 | 161 | 103 | 3/3 |
| gzip-L6-insert-mid | gzip | 11.74 | 36.00 | 51.0 | 1.1 | 14.1 (0.8 + 13.0 + 0.4) | 10.0 | 28% | 13.0 | 3/3 | 134 | 81 | 3/3 |
| zip-deflate-change | zip | 6.89 | 12.00 | 9.6 | 8.5 | 13.1 (8.2 + 4.6 + 0.3) | 8.5 | 137% | 4.6 | 33/33 | 74 | 36 | 3/3 |
| zip-deflate-meta | zip | 6.89 | 12.00 | 42.4 | 0.3 | 4.9 (0.0 + 4.6 + 0.3) | 0.3 | 12% | 4.6 | 33/33 | 70 | 41 | 3/3 |

## Optional external-producer corpus (python3 zlib 1.3, GNU gzip 1.12, Info-ZIP 3.0), 256 KiB base, `text-insert-mid` rows

Not part of the guaranteed corpus; tools are probed at generation time and skipped if absent. Full run: 39 families.

| family | fmt | comp MiB | plain MiB | cdc% | plain-oracle% | preflate% (new plain + recon + other) | preflate-rcdc% | vs cdc | recon/comp% | stream ok | analyze ms/MiB | recreate ms/MiB | exact |
|---|---|---:|---:|---:|---:|---|---:|---:|---:|---:|---:|---:|---|
| ext-gnu-gzip-L1-text-insert-mid | gzip | 0.54 | 1.50 | 67.4 | 15.0 | 15.0 (14.7 + 0.0 + 0.3) | 15.1 | 22% | 0.0 | 2/2 | 82 | 38 | 2/2 |
| ext-gnu-gzip-L6-text-insert-mid | gzip | 0.50 | 1.50 | 71.8 | 7.6 | 10.3 (7.1 + 2.7 + 0.5) | 10.3 | 14% | 2.7 | 2/2 | 188 | 80 | 2/2 |
| ext-gnu-gzip-L9-text-insert-mid | gzip | 0.49 | 1.50 | 86.0 | 15.4 | 18.2 (15.1 + 2.8 + 0.3) | 18.3 | 21% | 2.8 | 2/2 | 145 | 79 | 2/2 |
| ext-info-zip-L6-text-insert-mid | zip | 0.49 | 1.50 | 67.3 | 15.3 | 18.0 (14.8 + 2.7 + 0.5) | 18.0 | 27% | 2.7 | 2/2 | 135 | 78 | 2/2 |
| ext-zlib-py-L1-text-insert-mid | deflate | 0.54 | 1.50 | 56.3 | 10.4 | 10.4 (10.0 + 0.0 + 0.4) | 10.4 | 18% | 0.0 | 2/2 | 77 | 32 | 2/2 |
| ext-zlib-py-L6-filtered-text-insert-mid | deflate | 0.50 | 1.50 | 61.5 | 21.2 | 27.8 (21.0 + 6.6 + 0.2) | 27.8 | 45% | 6.6 | 2/2 | 155 | 84 | 2/2 |
| ext-zlib-py-L6-fixed-text-insert-mid | deflate | 0.62 | 1.50 | 68.2 | 8.4 | 10.6 (8.1 + 2.2 + 0.2) | 10.6 | 16% | 2.2 | 2/2 | 115 | 57 | 2/2 |
| ext-zlib-py-L6-mem1-text-insert-mid | deflate | 0.55 | 1.50 | 54.0 | 24.3 | 29.1 (24.0 + 4.8 + 0.3) | 29.2 | 54% | 4.8 | 2/2 | 191 | 112 | 2/2 |
| ext-zlib-py-L6-mem9-text-insert-mid | deflate | 0.49 | 1.50 | 57.7 | 16.3 | 19.0 (15.9 + 2.7 + 0.4) | 19.0 | 33% | 2.7 | 2/2 | 146 | 74 | 2/2 |
| ext-zlib-py-L6-rle-text-insert-mid | deflate | 0.89 | 1.50 | 60.6 | 4.0 | 4.0 (3.8 + 0.0 + 0.2) | 4.0 | 7% | 0.0 | 2/2 | 38 | 21 | 2/2 |
| ext-zlib-py-L6-text-insert-mid | deflate | 0.49 | 1.50 | 56.9 | 10.2 | 13.0 (9.9 + 2.8 + 0.3) | 13.0 | 23% | 2.8 | 2/2 | 138 | 72 | 2/2 |
| ext-zlib-py-L9-text-insert-mid | deflate | 0.50 | 1.50 | 63.2 | 16.0 | 18.8 (15.7 + 2.7 + 0.4) | 18.8 | 30% | 2.7 | 2/2 | 153 | 77 | 2/2 |
| ext-zlib-py-gz-L6-text-insert-mid | gzip | 0.49 | 1.50 | 78.4 | 11.5 | 14.3 (11.1 + 2.8 + 0.5) | 14.3 | 18% | 2.8 | 2/2 | 152 | 93 | 2/2 |
Unsupported streams (fell back to CDC of the compressed bytes):
- ext-gnu-gzip-L1-repeat-replace map[PredictionFailure:2]

Preflate's estimated parameters (first revision): zlib/GNU gzip/Info-ZIP streams are detected as `zlib_compatible=true`
with the classic zlib hash; Go `compress/flate` streams are *not* zlib-compatible and are fitted to the nearest model
(`ZlibNG`/`Crc32c`/`Libdeflate4Fast` hash, `Mixed` blocks, `zlib_compatible=false`, `max_chain` 34-640), which shows up as
a much larger correction stream.

## Findings

- **Severity.** At 4 MiB, non-append edits cost current CDC 34-100% of the compressed bytes (insert-begin 100%,
  insert-mid ~52%) while plaintext-level reuse needs 0.5-1.1%; the gap is 50-150x. Append is the exception (CDC 2.4%).
- **Exactness holds.** Every supported stream (all stdlib families except Go Huffman-only; every external stream except
  GNU gzip -1 on repetitive data) reconstructed bit-identically; no equivalent-but-different output was observed.
- **The reconstruction blob, not the plaintext delta, is the cost.** New plaintext is 0.2-0.8% of compressed bytes at
  4 MiB but the blob is 12.9% (Go L6), 20.4% (Go L1), 5.9% (Go L9), 4.6% (ZIP of Go L6 members), and 2.2-3.7% for
  typical zlib/GNU gzip/Info-ZIP level-6 text (up to 6.5% for zlib L9 repetitive / `Z_FILTERED`). Totals: Go L6
  insert-mid 52.3% -> 13.9% (oracle 0.9%): ~75% of the closable gap closed; insert-begin 100.1% -> 13.6%.
- **The blob is proportional to stream size, not edit size.** A 200-byte edit regenerates a blob of the same size;
  consecutive-revision blobs share little (insert-begin: none; insert-mid: ~9% of windows; append: the whole blob
  shares windows). CDC-chunking the blob (`preflate-rcdc`) helped modestly (gzip insert-mid 14.1% -> 10.0%, append
  13.5% -> 10.2%) and strongly for ZIP where unchanged members have identical blobs (zip-deflate-meta 4.9% -> 0.3%).
- **Producer dependence is large.** Zero/near-zero blob: stored blocks (40 B/stream), custom fixed-Huffman (44 B),
  zlib `Z_RLE`/`Z_HUFFMAN_ONLY`. Low: zlib-compatible tools. High: Go `compress/flate` (the corpus's default producer,
  so the synthetic gzip result is a harder-than-typical case; real zlib-family data should look like the ~3% rows).
- **Pathological / unsupported cases.** Go `flate.HuffmanOnly` streams are rejected (`InvalidDeflate: Invalid Huffman
  code lengths`, huffman_encoding.rs:113) though they are valid RFC1951: upstream gap that the native parser must not
  share. GNU gzip -1 on repetitive records fails with `PredictionFailure` (2/2). Both fell back to CDC with exact
  roundtrip (an optimisation loss only). Append is a regression: Preflate 13.5% vs CDC 2.4% (558% of CDC) because the
  untouched prefix is already shared by CDC and the blob is paid again, so a transform must be chosen per object
  (or the blob made prefix-stable), never applied unconditionally.
- **Speed.** Analysis ~130-190 ms/MiB compressed (up to ~450 for 10x-expanding repetitive data); recreation 70-130
  ms/MiB (~8-14 MiB/s), 8-40 for stored/fixed/RLE streams. A reversible-GET path would be roughly 10x slower than a
  plain copy.
- **Is predictive reconstruction worth it?** For zlib-family producers, yes in principle: ~3% blobs would turn
  50-100% churn into ~4-10%. For non-zlib producers the 13-20% blob keeps most of the win but with a much
  higher floor, and pure-append edits lose. The residual (blob 13% vs plaintext 0.9%) is the room left for a
  compressor-independent IR whose cost scales with changed blocks; whether an explicit IR beats these blobs is not
  yet measured (Z3-05 provides the structural statistics; Z3-06 decides).

## Caveats

Synthetic text/records (about 3x compressible), single seed, few revisions; one-block-ish edits; CDC v1 sizes
(16/64/256 KiB) are large relative to the 256 KiB corpus; Go DEFLATE output is toolchain dependent; ext corpus has two
update revisions; the packed estimate applies DEFLATE to plaintext components. No real-world data was used here.
