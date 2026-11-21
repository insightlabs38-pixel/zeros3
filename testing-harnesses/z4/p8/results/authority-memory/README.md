# Windowed authority migration memory

Screening run on the same Linux amd64 workspace with Go 1.27.0 and two migration
workers. The G5 fixture generates three versions per size. The archived P8 merge
is af9b39db08db135709dd8c7a4f687ab2543c9ad1. The candidate source is
50490fd5b714c36829bc6dde0b7567fd33d41463. Both source trees ran
`testing-harnesses/z4/c1/run_large.sh WORKDIR SIZE_BYTES`.

| Bytes per version | P8 peak RSS KiB | Candidate peak RSS KiB | P8 wall s | Candidate wall s | Bytes after maintenance |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 268435456 | 200348 | 121080 | 13.89 | 18.41 | 270225507 / 270225507 |
| 1073741824 | 210708 | 154676 | 120.46 | 79.94 | 1080907913 / 1080907912 |
| 2147483648 | 219660 | 165104 | 181.03 | 142.51 | 2161774297 / 2161774294 |

All six runs recovered the three exact whole-object SHA-256 digests and passed
range checks and deep verification in fresh processes. Reported wall times were
taken under varying background load and are screening data. Each cell is one
run. The C1/T1 merge separately measured 61708 KiB on this host at 256 MiB.

The P8 in-use heap profile at the 256 MiB peak charged about 104 MiB directly
to decoded window allocations and 58 MiB to packed reads. The candidate
reuses the sequential root proof output buffer and releases encoding readers
before proof. The per-proof dependency-window and verified-piece caps are
16 MiB and 8 MiB. Planning caches and the on-disk format retain their previous
behavior.

The parallel G5 script creates one fixture and runs two simultaneous migrations
per binary with the same two-worker setting. It samples aggregate RSS, records
per-process peak RSS and CPU, then checks actual retained bytes and exact
fresh-process replay after GC, repack and compaction.

```sh
python3 concurrent-g5.py WORKDIR TESTBIN P8_BINARY CANDIDATE_BINARY 2147483648
```

The repository's 36-step T1 gate is separate from these results. Qualify the
candidate with pinned real-model and Parquet corpora before merging.

The first T1 attempt used Go 1.27.4 and GOMAXPROCS=2. All 12 preflight
steps passed. The normal root step failed because the mandatory pipeline
reader close test skipped. On both the unchanged P8 binary and the candidate
it skips at GOMAXPROCS=2 and passes at GOMAXPROCS=4. The retired attempt and
its interruption remain recorded. The rerun uses four logical Go processors
on the same two-CPU-quota workspace and retains the original test requirements.
