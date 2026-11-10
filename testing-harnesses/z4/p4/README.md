# Z4-P4 representation performance: harness and evidence

Report: [`../../results/Z4_P4_REPRESENTATION_PERFORMANCE.md`](../../results/Z4_P4_REPRESENTATION_PERFORMANCE.md).
Design of the deferred construction policy: [`DEFERRED_CONSTRUCTION.md`](DEFERRED_CONSTRUCTION.md).

| Path | Purpose |
|---|---|
| `../../../zeros3_p4_bench_test.go` | `TestP4Probe`: one process = one (object set, mode, policy). PUT wall/CPU/alloc/RSS, per-phase build times, component and file counts, byte-exact whole/range reads, `verify`, `verify -deep`, JSONL row. Env: `ZEROS3_P4_FILES` (paths or `gen:merkle:N`, `gen:car:N`, `gen:tar:N`), `_MODE`, `_BUILD=deferred`, `_WORKERS`, `_CONC`, `_DIR`, `_OUT`, `_CPUPROF` (PUT phase), `_READPROF` (read phase). |
| `../../../zeros3_p4_test.go` | qualification tests (deferred queue, crash images at every build phase, real SIGKILL rounds, read cache, parallel deep verify, prefix index) |
| `scripts/kinds.sh` | kind -> object list over the P0 corpus (`../p0/scripts/fetch_corpus.sh` + `make_structured.py`) |
| `scripts/run_kinds.sh` | runs every kind x mode with a given test binary |
| `results/*.jsonl` | raw rows; `results/*.md` tables derived from them |

```sh
go test -c -o /tmp/p4.test .                                   # at the commit to measure
C=/path/to/p0-corpus W=/tmp/p4work testing-harnesses/z4/p4/scripts/run_kinds.sh /tmp/p4.test LABEL out.jsonl off shadow prefer
ZEROS3_P4_BUILD=deferred ... run_kinds.sh /tmp/p4.test deferred out.jsonl shadow
```
Every object is read back (whole and ranges) and compared to the input bytes, and `verify -deep` must pass, or the row fails the run.
