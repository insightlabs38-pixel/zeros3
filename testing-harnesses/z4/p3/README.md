# Z4-P3 evidence: maintenance, reachability, verification, GC, repack, startup

Report: [`../../results/Z4_P3_MAINTENANCE_PERFORMANCE.md`](../../results/Z4_P3_MAINTENANCE_PERFORMANCE.md). Model and rules: [`REACHABILITY_MODEL.md`](REACHABILITY_MODEL.md). Deferred design: [`CHECKPOINT_DESIGN.md`](CHECKPOINT_DESIGN.md).

Method. Fixture stores are built once through the **real server** with the unmodified base binary (`da88b4d`, the P2 merge) using the Z4-P0 driver (`../p0/cmd/p0bench`, unchanged). Read-only commands run on the fixture itself, mutating commands on a `cp -a` clone (never on a store a previous run modified), each command as a fresh process (`p0bench cli`: wall, user+sys CPU, peak RSS, I/O). Base and candidate binaries run the same sequence; the 1 M and churn matrices alternate them. Absolute figures are from this 4-vCPU VM (ext4 on virtio, Go 1.27, fsync p50 ~0.26 ms) and are only compared with figures measured here, in the same run family.

| Path | Purpose |
|---|---|
| `scripts/mkfix.sh` | the fixtures: 1k / 10k / 100k / 1M small objects, 10k loose, 600 mixed (4 KiB-16 MiB, duplicates, revisions), 20 keys x 256 versions |
| `scripts/maint.sh BINDIR LABEL FIXTURE..` | stats / verify / verify -deep / doctor / gc / compact / repack (dry-run, read-only fixture) and gc / repack / compact / prune apply (clones) |
| `scripts/maint1m.sh` | the same on the 1,000,000-object store, base and candidate interleaved, plus prune -> gc -> repack on clones |
| `scripts/churn.sh FIXTURE OUT` | reclamation matrix: 0 / 10 / 30 / 50 / 75 / 90% dead x random / concentrated deletion x {gc, repack (50% rule), repack (100%), gc then repack} x {base, candidate} |
| `scripts/ab_p3.sh`, `scenarios_regress.txt`, `regress_table.py` | PUT / GET regression, interleaved A/B (the P2 runner, unchanged logic) |
| `scripts/tables.py RESDIR` | the markdown tables of the report from the raw JSONL |
| `results/*.jsonl` | raw machine-readable results, one line per run |
| `results/prof/` | pprof text summaries (CPU, allocation) |
| `results/*.txt` | worker sweep, test logs, gate records |

In-process benchmarks (`zeros3_p3_bench_test.go`) read a fixture without modifying it:
```sh
ZEROS3_P3_STORE=$P3_ROOT/fix/s100k go test -run xxx -bench 'P3(Open|Reachability|Stats|Verify)' -benchtime 3x -cpuprofile cpu.out .
ZEROS3_MAINT_WORKERS=1|2|4|8 ...        # worker sweep
```
Tests (repository root, `zeros3_p3_test.go`, `zeros3_p3_reference_test.go`): the serial scan of `da88b4d` kept verbatim as `referenceReachability` and compared with the parallel scan on healthy and damaged stores in both layouts, 1-8 workers and block sizes 1..32768; journal replay vs the original on valid, truncated, flipped and garbage-tailed journals; the strict fast journal decoders vs `encoding/json`; serial vs pipelined repack writing identical packs; repack crash points with many small records; real `kill -9` of `repack` and `gc`; corruption injected mid-scan; shared opener vs live journal.

Reproduce (Go 1.27, Linux, root for `drop_caches`):
```sh
export P3_ROOT=/tmp/p3; mkdir -p $P3_ROOT/{base,new,fix,res,work}
git worktree add $P3_ROOT/basewt da88b4d && sh testing-harnesses/z4/p0/build.sh $P3_ROOT/basewt $P3_ROOT/base
go -C testing-harnesses/z4 build -o $P3_ROOT/base/p0bench ./p0/cmd/p0bench
sh testing-harnesses/z4/p0/build.sh $PWD $P3_ROOT/new && cp $P3_ROOT/base/p0bench $P3_ROOT/new/
go test -c -o $P3_ROOT/zeros3.test .
testing-harnesses/z4/p3/scripts/mkfix.sh                      # base binary builds every fixture
testing-harnesses/z4/p3/scripts/maint.sh $P3_ROOT/base base s1k s10k loose10k hist mix s100k
testing-harnesses/z4/p3/scripts/maint.sh $P3_ROOT/new  new  s1k s10k loose10k hist mix s100k
testing-harnesses/z4/p3/scripts/maint1m.sh; testing-harnesses/z4/p3/scripts/churn.sh s100kc churn_s100kc.jsonl
python3 testing-harnesses/z4/p3/scripts/tables.py $P3_ROOT/res
```
