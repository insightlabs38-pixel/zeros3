# Z4-P5 evidence: advanced similarity deltas, scalable discovery, representation-authority planning

Report: [`../../results/Z4_P5_DELTA_PLANNER_PERFORMANCE.md`](../../results/Z4_P5_DELTA_PLANNER_PERFORMANCE.md).
Nothing here changes `zeros3.go`, a store format, a default or an S3 behaviour. The reusable code is
`testing-harnesses/z3/dcore` (standard library only); `testing-harnesses/z3/simdelta` calls it through a thin adapter.

## Layout
| Path | Purpose |
|---|---|
| `../../z3/dcore/` | matcher (`match.go`), cost model + staged estimator (`cost.go`), candidate selection (`select.go`), windowed codec ZW1 (`window.go`), postings/runs (`postings.go`, `runset.go`), exact group accounting + planners (`acct.go`, `plan.go`), extraction vectors (`vectors.go.txt`) |
| `../../z3/cmd/z4p5bench/` | measurement driver: `-mode match|full|select|index|runs|window|wencode|wdecode` |
| `../../z3/cmd/dcoreport/` | mechanical extraction of `dcore` into a TEMPORARY single-file integration of the production source, compile, differential vectors, fidelity check |
| `cmd/casaudit/` | go/ast inventory of every production function that touches CDC chunk bytes/presence |
| `scripts/formatmatrix.sh` | real-binary store-format refusal matrix |
| `b2_p5_planner.diff.txt` | the scratch B2 planner change (never applied to the repository) |
| `results/` | raw JSONL, summaries, logs |

## Reproduce (Go 1.27, Linux; corpus is fetched, never committed)
```sh
testing-harnesses/z4/p0/scripts/fetch_corpus.sh /home/user/p5corpus tars; ... q6; ... py; ... git   # pinned sources as P0/B1/B2
# DEX: F-Droid org.fdroid.fdroid_{1019000,1020050,1021000}.apk, unzip classes*.dex into econ/dex/<ver>/
go -C testing-harnesses/z3 build -o /tmp/z4p5bench ./cmd/z4p5bench
/tmp/z4p5bench -mode match -reps 3          # reference B1 matcher vs dcore (16 MiB-bounded pairs, B1's limit)
/tmp/z4p5bench -mode full                   # dcore on whole files
/tmp/z4p5bench -mode select                 # exhaustive oracle vs staged estimator on real families
/tmp/z4p5bench -mode index 1000000          # also 100000, 10000000
/tmp/z4p5bench -mode runs 1000000 262144    # RunSet append/query/delete/compact/startup
P5TMP=/big/disk /tmp/z4p5bench -mode window 1024 4 4096     # synthetic 1 GiB, 4 workers, 4 MiB windows
/tmp/z4p5bench -mode window 0 4 4096 BASE TARGET            # real concatenated multi-revision object
/tmp/z4p5bench -mode wencode BASE TARGET REC; /tmp/z4p5bench -mode wdecode REC BASE OUT; sha256sum OUT TARGET
go -C testing-harnesses/z3 run ./cmd/dcoreport -dcore dcore -prod ../../zeros3.go -work /tmp/dcw -z3 .
go -C testing-harnesses/z4 run ./p5/cmd/casaudit -draft B2_DRAFT_zeros3.go ../../zeros3.go
# B2 scratch (draft #97 checked out detached; the P5 planner diff is applied to a COPY of that worktree)
git worktree add --detach /tmp/b2wt origin/z4/representation-authority
ZEROS3_B2_CORPUS=/home/user/p5corpus/econ ZEROS3_B2_OUT=out.jsonl ZEROS3_B2_VARIANTS=auth-archive-oldest,auth-archive-p5,... \
  go test -run 'TestB2Economics/<family>$' -count=1 .
```
Test commands: `go -C testing-harnesses/z3 test ./dcore ./simdelta`, `-race`, `go test -fuzz FuzzMatchExact|FuzzWindowRecord`.
