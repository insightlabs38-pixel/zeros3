# GF2 · byte-exact queries over compressed output

A compressed-output patch with bit-shifted source spans eliminated the
DEFLATE predictor from reads for one TypeScript release pair. Another
TypeScript pair and independent urllib3 revisions exposed the limit of
compressed-domain reuse. The candidate is specialized at this checkpoint.

## Frame compilation

Index verified source bytes at eight bit phases. Extend only verified
matches and split output at 32 KiB. An instruction copies bytes from a
source bit offset or retains literal compressed bytes. Relative offsets
encode changes in the source-to-output mapping. Independent output
digests prove exact bytes across frame boundaries, before and after store
reopen. The exact source includes gzip headers, DEFLATE blocks and trailers.
No semantic re-encoding occurs on this path.

| TypeScript gzip v0–v1, paired 32 KiB native packs | Allocated bytes | Warm target whole p95 | Warm 4 KiB p95 |
| --- | ---: | ---: | ---: |
| Direct packed frames | 8,409,088 | about 10 ms | 0.10 ms |
| Byte alignment, sparse index | 8,175,616 | about 26 ms | 0.20 ms |
| Eight alignments, sparse index | 6,258,688 | about 23 ms | 0.28 ms |
| Eight alignments, stride 4, relative offsets | 5,406,720 | 29–43 ms | 0.46–0.47 ms |

Relative stride 4 copies 3,223,980 target bytes, including 2,854,192
from non-byte-aligned sources. The stride-2 variant costs 5,365,760 bytes
and roughly twice as much analysis CPU. Stride 4 is frozen for the follow-up.
The GF0 archive allocated 4,829,184 bytes and had 1,182–1,196 ms warm whole
p95. That is a separate in-process native lane. The 12.0% space delta and
27–41× latency delta identify a candidate for a matched authority trial.
The patch lab publishes native packs and a verified index. Its retention
and timing omit the full signed namespace and authority integration.

The frozen relative patch retained 8,065,024 bytes on TypeScript v1–v2
and 602,112 on urllib3. It copied 504,955 and 14,994 target bytes. Neither
approached the archive storage frontier. Byte-aligned matching covered
1.93 MB of the Parquet target, and shifted matching added negligible reuse.
Equal decoded bytes with different coding settings proved little compressed
reuse. Reordered tar entries with changed block boundaries produced
bit-shift matches without saving an allocated block in that small control.

## Correction witness and hot overlays

A separate fast witness stores exact Huffman metadata, literal run
lengths, match length fields and distance corrections. Whole and 4 KiB
queries replay bounded block geometry, including padding and trailer.
A constant-time distance predictor indexed by nearby plaintext and
length symbol avoids searching the LZ match chain. Native archive-like
plaintext bodies plus the direct or predicted witness used 13,848,576
or 14,581,760 bytes. Whole p50 was 193 or 190 ms. Reused in-memory witness
metadata gave 2.9 or 2.7 ms range p95. The witness alone occupied too much
space. The tested cheap predictor increased exact correction cost.
This result leaves open smaller or selectively compiled witnesses.

Four retained 32 KiB compressed overlays added 131,072 bytes (2.7%) to
the actual compacted archive. At 117/120 hits, the 4 KiB p95 fell from
24.83 to 0.12 ms and p99 from 39.72 to 19.10 ms. At 108/120 hits p95
was 17.06 ms versus 26.53 ms. After a full shift to unseen frames,
p95 was 31.68 ms versus 32.32 ms. All 720 probes verified exact bytes.
The durability cost includes native data and metadata. The dispatcher is
experimental, and the two phases share an ambient page cache.

Next test the relative frame mode through the ordinary native authority
reader in two disposable stores with independent archive, CDC, and
prune/reopen/failure controls. Its admission must reject histories with
poor exact compressed reuse. Test whether native CDC source geometry
and reclamation preserve the measured space/read tradeoff.

[Reproduction](REPRODUCE.md) · [Results](results/) · [Design](PREREGISTRATION.md)
