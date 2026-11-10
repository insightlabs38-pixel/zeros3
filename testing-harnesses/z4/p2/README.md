# Z4-P2 evidence: read path (manifest cache, range index, chunk cache, pipelined streaming)

Report: [`../../results/Z4_P2_READ_PERFORMANCE.md`](../../results/Z4_P2_READ_PERFORMANCE.md). Design and integrity contract: [`CACHE_MODEL.md`](CACHE_MODEL.md).
Method = the Z4-P0 driver (`../p0/cmd/p0bench`, extended additively: phases, HEAD, offset policies, sampled verification, CPU pinning) against a real server over HTTP, one fresh store and process per repetition. Every GET is byte-compared with the deterministic generator inside the measured window (`verified:true`); the exceptions are the explicitly sampled runs (`P0_VERIFY=sample:N`, recorded as `verify_mode`). All base-vs-candidate numbers are **interleaved A/B on the same instance** (variant order alternates each round); no historical absolute figure is reused.

| Path | Purpose |
|---|---|
| `CACHE_MODEL.md` | identity, integrity contract, invalidation matrix, admission, memory bounds, pipelined streaming |
| `scripts/ab_p2.sh OUT ROUNDS SCENARIOS name=BINDIR ...` | interleaved A/B runner (BINDIR holds `zeros3`, `zeros3-prof`, `p0bench`) |
| `scripts/scenarios_p2.txt` | the read matrix: object sizes 1 MiB..1 GiB, HEAD / whole / 4 KiB..8 MiB / EOF / hot / random / first-read / cold, c1..c32, layouts (loose, direct pack, medium pack, segment, deflate, raw) |
| `scripts/scale_p2.sh` | store scale (objects) x cache budget sweep with hit counters and RSS |
| `scripts/report_p2.py FILE A B [prefix..]` | markdown tables (median [min-max] over rounds, p50/p99, CPU per request, read amplification, RSS) |
| `results/*.jsonl` | raw machine-readable results, one line per repetition and phase |
| `results/prof/` | pprof text summaries (CPU, allocation, mutex, block) |

Test code (repository root): `zeros3_p2_test.go` (parser differential and fuzz, index, cache), `zeros3_p2_read_test.go` (exact-byte range matrix over loose / segment / sealed / compressed layouts in serial, pipelined and cache-off modes, cache integrity and revalidation, pipelined-stream faults and leaks, P1C seal interplay, concurrent read/write/seal).

Reproduce (Go 1.27, Linux, root for `drop_caches`):
```sh
sh testing-harnesses/z4/p1b/scripts/build.sh $PWD /tmp/p2/cand           # candidate: zeros3, zeros3-prof, p0bench
git worktree add /tmp/p2/basewt 4059cd9 && sh testing-harnesses/z4/p1b/scripts/build.sh /tmp/p2/basewt /tmp/p2/base
cp /tmp/p2/cand/p0bench /tmp/p2/base/p0bench                             # one driver for both variants
P2_WORK=/tmp/p2/work testing-harnesses/z4/p2/scripts/ab_p2.sh out.jsonl 3 testing-harnesses/z4/p2/scripts/scenarios_p2.txt base=/tmp/p2/base cand=/tmp/p2/cand
python3 testing-harnesses/z4/p2/scripts/report_p2.py out.jsonl base cand sz_
# server-core scaling with the load generator on its own cores
P0_SERVER_CPUS=0,1 taskset -c 2,3 p0bench get -bin ... -objs 4 -size 64mib -phases 'c16|range|4kib|random|16|8000|warm'
# server-limited whole-object throughput (the client byte-verifies one request in 16)
P0_VERIFY=sample:16 p0bench get ... -phases 'whole|whole|4kib|random|1|12|warm'
```
Build `4059cd9` with the same script so the P0 overlay is identical; the base binary needs no source change.
