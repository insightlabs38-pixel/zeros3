# P8 corpus layout (`ZEROS3_B2_CORPUS`)

Built with `testing-harnesses/z4/p0/scripts/fetch_corpus.sh DIR tars|q6|py` (pinned, hash-checked) and four links/unpacks:

| path | content |
|---|---|
| `src/coreutils-9.{5,6,7}.tar` | gnu.org release tarballs, xz-decompressed |
| `clr/*.dll` | Newtonsoft.Json 12.0.3, 13.0.1-13.0.4 (`q6/clr`) |
| `dex-b8/<2000050+i>/classes.dex` | F-Droid 2.0.0-2.0.2 (`q6/fdroid-apk`) |
| `dex/{1019000,1020050,1021000}/classes{,2}.dex` | `unzip` of `https://f-droid.org/archive/org.fdroid.fdroid_<v>.apk` |
| `parquet/{snappy,zstd}/c*.parquet`, `sqlite/v*.db` | `make_structured.py` (pyarrow 26.0.1, zstandard 0.25.0) |
| `gz/coreutils-9.7.l{6,9}.tar.gz` | `make_structured.py` (zlib levels 6 and 9) |

`ECON_MANIFEST.sha256` (in `results/`) lists the SHA-256 of every input.
