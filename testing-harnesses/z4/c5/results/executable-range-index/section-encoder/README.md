# Production ELF section partition

The opt-in `ZEROS3_EXPERIMENTAL_ELF=1` encoder reads little-endian ELF64
section boundaries and tiles the entire file with the existing concat
recipe. Its range path uses the existing reader and its wire format
remains concat. The test checks overlaps, gaps, out-of-bounds sections
and ranges that cross section boundaries.

The pinned shipped Go 1.27.1–1.27.4 `asm` and `link` files were storage
inputs. Each lane was verified after the same GC, repack, compact and
full recompression. The section lane allowed expansion (`-min-saving -5`)
to measure the actual structure even when it would ordinarily be refused.

| History | CDC allocated bytes | Delta allocated bytes | ELF section allocated bytes |
| --- | ---: | ---: | ---: |
| asm | 6,598,656 | 5,246,976 | 6,520,832 |
| link | 9,621,504 | 7,712,768 | 9,564,160 |

Every whole-file read and sampled signed 4 KiB range matched its source.
Section partitions saved 1.2% and 0.6% against CDC and lost to delta on
both cohorts. The result preserves the opt-in encoder and leaves
normalization as the next promising executable research direction.

[Plans, hashes and production verification](trials.json)
