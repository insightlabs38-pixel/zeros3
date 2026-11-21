# Pythia migration policy comparison

Each arm used a fresh copy of the same five pinned 28 MB checkpoints.
Both migration arms ran the same Go 1.27.4 binary with GOMAXPROCS=2,
one worker, a 128 MiB reserve, no structural representations, a
5% minimum saving and at most two bases and two dependency levels.
The delta arm disabled ZN7. The CDC arm performed maintenance only.

| Policy | Allocated bytes after maintenance | Migration CPU | Peak RSS | Admitted |
| --- | ---: | ---: | ---: | ---: |
| cdc | 141,193,216 | — | — | — |
| delta | 141,193,216 | 2.71 s | 180,640 KiB | 0 |
| zn7 | 124,395,520 | 17.92 s | 92,132 KiB | 4 |

ZN7 reduced allocated bytes by 11.90%. Generic delta admitted no
object, so that arm measures the cost of evaluating the delta policy.
All three arms passed GC, repack, compaction and fresh deep verification.
Independent signed S3 readbacks matched every pinned source SHA-256.
Migration ran alongside the scale qualification. This fixture uses
small checkpoints and leaves the multi-GiB policy comparison open.
