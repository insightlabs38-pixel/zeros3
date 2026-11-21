# Parquet selection under ordinary pack maintenance

Six pinned PyArrow and DuckDB Snappy derivatives held the same public
NYC taxi append histories. All lanes started from clones of one store
and used the same verified similarity index. The active archive lanes
enabled both structural and two-base delta candidates.

| Pricing | Forms admitted | Allocated bytes after ordinary maintenance |
| --- | --- | ---: |
| CDC control | None | 7,372,800 |
| Default recompressed projection | Six structural | 17,752,064 |
| `-price-current-storage` | Three deltas | 6,631,424 |

Ordinary maintenance used GC, repack, compact and GC. Each store passed
deep verification. All six signed whole-file and 4 KiB range digests
matched their input files in every lane. The current-storage policy
used 10.1% less allocated storage than CDC on this fixture. The earlier
matched recompression trial gives the separate compressed end state.

[Trial provenance and plans](trials.json)
