# Request-local reuse in normalized executable replay

One normalized executable range can request multiple slices of the same
authenticated delta windows. The harness now shares those windows and
base pages within a request. The frame cache retains up to 8 MiB.
Each new request begins with empty caches, and every materialized frame
must pass its stored SHA-256 before any of its bytes enter the cache.

The pinned Go 1.27.1 and 1.27.4 binaries were input data. Each pair used
the same tool build, with the independent-stream mode as a control. The
raw and normalized records, compressed bases and indexes matched byte
for byte across modes. The decoded object digest and all 36 sampled
4 KiB comparisons matched the inputs.

| History | Base bytes requested before | With reuse | Median replay before | With reuse |
| --- | ---: | ---: | ---: | ---: |
| asm | 30,180,828 | 14,992,110 | 7.06 ms | 4.03 ms |
| compile | 51,382,322 | 31,491,097 | 10.24 ms | 8.65 ms |
| link | 39,313,874 | 19,230,953 | 14.28 ms | 7.12 ms |

Six samples per cell. These are logical base reads within the harness.
Timing excludes opening the compressed base and the benchmark shared
the host with ordinary tests. The production ELF section encoder
remains opt-in. The normalized recipe and this range path remain in the
laboratory, and the full normalized basis is still loaded at open.

[Paired measurements](measurements.json)
