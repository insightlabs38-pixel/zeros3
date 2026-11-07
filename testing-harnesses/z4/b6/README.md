# Z4-B6 hard-codec frontier: reproduction and pins

Laboratory only. Code: `z3/lz4z3` (exact LZ4 substrate), `z3/sqfsz3` (SquashFS-lz4), LZ4 block kinds added to
`z3/sstz3` and `z3/parquetz3`, drivers `z3/cmd/z4b6`, `z3/cmd/z4b6rank`, cost model `z3/b6cost`. Production
(`zeros3.go`, root `go.mod`) is untouched.

## Producers and corpora (all outside Git; nothing here is a runtime dependency)
| lane | producer / source | class |
|---|---|---|
| RocksDB LZ4 / LZ4HC SSTs | rocksdict 0.3.29 (RocksDB 9.8.4), Z3-30 operation logs (`z3sst gen-ops`), `oracle/sst/gen_sst.py` with `SST_CODEC=lz4|lz4hc|snappy` | PRODUCER_WORKLOAD |
| Parquet LZ4_RAW / Snappy / Brotli | pyarrow 26.0.1 (parquet-cpp-arrow), Z3-31 8-revision chain via `gen_parquet_lz4.py OUT lz4|snappy|brotli` | PRODUCER_WORKLOAD |
| SquashFS | squashfs-tools 4.6 (`-comp lz4|lz4 -Xhc|xz|zstd|gzip -b 131072`) over npm releases typescript 5.3.3/5.4.5/5.5.4/5.6.3/5.7.3 and three 0.160-0.164 | real release histories, producer-generated images |
| raw LZ4 blocks | python-lz4 4.4.5 (`gen_lz4_blocks.py`), lz4 CLI 1.9.4 frames, pierrec/lz4 v4.1.22 (`/root/pierrec` driver in this report), 48 library fixtures | REFERENCE / controls |
| WOFF2 | npm tarballs fetched by `fetch_npm.py` (fontawesome-free 6.0.0-7.0.0 x10, bootstrap-icons 1.8-1.13 x8, katex 0.15-0.19 x10, material-icons x5, @fontsource/inter x6); split by `woff2split.py` | real release histories |
| WIM | archive.org `Win7PE_B7062/7070/7077/7100_x86/7229.iso` -> SOURCES/BOOT.WIM (LZX, real Windows 7 PE pre-release builds); `wimtable.py` reads the native lookup table, `wimlib-imagex extract` for decoded trees | real build history |

Not obtainable: Microsoft ESD/WIM delivery hosts are refused by the session egress policy, so no real LZMS ESD or XPRESS history exists here.

## Reproduce (each step fails the script on error; there is no `|| true`)
```
fetch_npm.py CACHE <pkg> <versions...>; woff2split.py CACHE OUT; z4b6rank OUT json            # fonts, WIM trees, squashfs families
z3sst econ -corpus CORPUS -prod rocksdb -reps cdc,blocks,ukv -creps cdc,blocks,ukv            # SST economics (lz4, lz4hc, snappy)
z3sst rt DIR; z3parquet rt -fam pyarrow-chain; z3parquet chain -fam pyarrow-chain              # in-process round trips
z4b6 sqfs ROOT FAMILY...; z4b6 lz4frames FILES; z4b6 lz4blocks DIR...                          # SquashFS economics, frames, producer matrix
verify_fresh.sh z4b6 sst|parquet|sqfs SCRATCH FILES...                                         # fresh-process pack/unpack + sha256sum
go test ./lz4z3 ./sqfsz3 ./sstz3 ./parquetz3 (B6_LZ4FX=DIR adds the 48 python-lz4 library fixtures)
```
`results/` holds the compact raw outputs the report quotes.
