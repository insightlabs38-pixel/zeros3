# Z4-P8 evidence: cross-engine performance convergence

Report: [`../../results/Z4_P8_CROSS_ENGINE_PERFORMANCE.md`](../../results/Z4_P8_CROSS_ENGINE_PERFORMANCE.md).
Production stays one file (`zeros3.go`), standard library only, no store-format change.

| Path | Purpose |
|---|---|
| `run.sh`, `run.py` | measurement driver (`migrate`, `matcher`, `rep`) over a root test binary; one fresh process per family |
| `test_driver.py` | exit status, output validation, deadline and interruption controls |
| `CORPUS.md` | layout and provenance of the pinned inputs (`ZEROS3_B2_CORPUS`) |
| `results/*.jsonl` | raw rows of every run kept, including the slow baselines |
| `results/*.md` | tables generated from the rows |
| `../../../zeros3_p8_test.go` | `TestP8Migration` (authority migration, read-back SHA-256, physical bytes after gc/repack), `TestP8Matcher` (matcher A/B on real revisions), `TestP8Rep` (structured gzip/zstd build + replay), unit and differential tests of the shared matcher and window cache |

Baseline = the P8 base commit (`5d63897`) with the same P8 measurement test harness; "P8" = the final revision.
Build both test binaries with `go test -c -o BIN .` on the same machine and toolchain.
The driver needs Python 3 and a fresh tag per invocation. It writes logs and profiles below `OUT/TAG/` and
publishes `OUT/TAG.jsonl` only after all requested children complete and their rows validate.
Any missing input, failed child, timeout, or incomplete output makes the command fail. Use
`ZEROS3_P8_TIMEOUT_SECONDS` to shorten the driver's per-child deadline (default 10800 seconds).
Run driver failure controls with `python3 testing-harnesses/z4/p8/test_driver.py`.

```sh
go test -c -o /tmp/p8base.test .      # at 5d63897 with zeros3_p8_test.go and the TestP8Migration part of zeros3_b2_economics_test.go copied in
testing-harnesses/z4/p8/run.sh /tmp/p8base.test OUT base migrate sqlite,clr-short,dex,parquet,parquet-zstd,src-tar-short delta 3
testing-harnesses/z4/p8/run.sh /tmp/p8.test    OUT p8-migrate migrate sqlite,clr-short,dex,parquet,parquet-zstd,src-tar-short delta 3
testing-harnesses/z4/p8/run.sh /tmp/p8.test    OUT p8-matcher matcher sqlite,parquet,src-tar-short,dex xd,dw
testing-harnesses/z4/p8/run.sh /tmp/p8.test    OUT p8-rep rep /path/coreutils-9.7.l6.tar.gz,/path/coreutils-9.7.l9.tar.gz 3
go -C testing-harnesses/z3 test ./dcore ./ir ./simdelta            # lab packages (matcher, exact DEFLATE)
```
