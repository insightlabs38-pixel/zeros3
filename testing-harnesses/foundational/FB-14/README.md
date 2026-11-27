# FB-14 · Chunk deltas

**Research only.** TypeScript and Parquet cleared the measured storage
and sparse-read comparisons. The three-corpus gate for an ingest format
remains unmet after charging physical placement and read costs.
M-G3 is not applicable on this evidence.

## Measurements

The Go research harness uses the native Gear table, anchor matcher, XD
instruction decoder, DEFLATE compressor and pack writer. It deduplicates
first. A bounded similarity table and nearby positions in the prior
revision nominate up to four candidates. The selected record carries
one full base digest. Four CDC sizes and depths one, two and four were
measured. Reopened packs verify their body, index and whole-object
digests. The research tag is confined to disposable files.

The native comparator takes the best verified allocation across
recompressed CDC, C13 presets and current archive and mixed authority
policies. The windowed control also ran direct native migration.
Chunk totals include the allocated research pack, binary manifests,
and common namespace and journal bytes from the matched native CDC
lane. The table shows selected low-space lanes, including borderline
rows so their read and placement costs remain visible.

| Corpus and retention | Best native bytes | Research bytes | Target / max depth |
|---|---:|---:|---:|
| X9 eight revisions | 2,539,520 | 2,600,960 | 32 KiB / 4 |
| X9 keep-last-4 plus current | 2,519,040 | 2,580,480 | 32 KiB / 4 |
| TypeScript tar | 4,771,840 | 4,255,744 | 16 KiB / 2 |
| TypeScript gzip | 5,222,400 | 12,566,528 | 32 KiB / 1 |
| Held-out HTTPX | 188,416 | 184,320 | 64 KiB / 1 |
| SQLite | 319,488 | 450,560 | 16 KiB / 2 |
| Parquet | 634,880 | 266,240 | 8 KiB / 2 |
| 33 MiB windowed C12 pair | 34,766,848 | 34,729,984 | 64 KiB / 1 |
| Event log | 118,784 | 147,456 | 8 KiB / 1 |
| C12 mixed edits, keep-last-4 plus current | 2,191,360 | 2,215,936 | 64 KiB / 1 |

TypeScript's 16 KiB depth-two lane decoded 180 kB at the 4 KiB p95
versus 280 kB of native CAS reads. Parquet's 8 KiB lane used 98 kB
versus 1.94 MB for the native minimum-space lane. The harness counts
both the patch target and bases as decode work, and records local
timings separately. The fixed similarity table occupied 114,688
bytes and the largest active anchor table 1 MiB. Input fixture bytes
were memory-resident, so the measured CPU excludes fetching candidate
bases from cold storage.

## Placement and retention

HTTPX saved one allocation block in the aggregate research pack.
Its best candidate decoded twice the bytes of the native archive lane,
and the corresponding local p95 was approximately 1.1 ms versus
0.61 ms. Per-revision packs would need 139,264 bytes of independent
base copies. The windowed pair's 36,864-byte apparent total advantage
was exactly offset by its verified pack-local copy. Its native
representation pack already used fewer bytes than the chunk prototype.

On X9, depth four slightly reduced storage compared with depth two,
while increasing p95 decode work from 254 to 392 kB at 32 KiB. It
still used more space than the native optimizer. The aggressive
expired-base rewrite dropped dependency extents from 684,293 to
58,067 bytes, while materializing 744,542 bytes of independent records
and increasing allocated packs from 2,551,808 to 2,650,112 bytes.
These are different outcomes for pin retention and storage.

A pack-local TypeScript placement would add 2,801,664 verified
copy bytes to its 16 KiB depth-one lane. The 8 KiB depth-one Parquet
lane needed only 32,768 bytes of copies and still paid. A design with
cross-pack references would need verified dependency coverage during
publication, reclamation and recovery. The measured outcomes support
further work on such layouts for specific workloads. They do not
establish a broadly useful ingest format.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
