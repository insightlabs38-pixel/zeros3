# Matcher kernel experiment

Two research alternatives kept the production sampling and exact byte extension. The sorted
variant uses exact 64-bit anchors within prefix ranges and the blocked variant probes two
cache-line-sized buckets. Both include index construction in the timing.

Go 1.27.0, amd64, two available CPUs, three repetitions per cell. Coreutils 9.5→9.6,
Newtonsoft.Json 12.0.3→13.0.1 and the first two pinned F-Droid releases use the P8
and Q6 corpus sources. The unrelated control is deterministic. The host was running
other qualification jobs. Compare the raw records and repeat under quiet conditions
before making a throughput claim.

Both prototypes preserved target SHA-256. Sorted lookup raised build costs across these
inputs. Blocked lookup reduced median tar build-plus-match time by about 5% and allocation
volume substantially, but raised build-plus-match time for CLR, DEX and unrelated data.
The smallest tar envelope gained 238 bytes on a 3.2 MB program. Serialized envelope
measurements do not establish savings after physical maintenance.

Apply `experiment.diff` from the repository root, prepare inputs below `CORPUS` using
the P0 tar fetch and Q6 fetch, then run:

```sh
ZEROS3_KERNEL_CORPUS=CORPUS ZEROS3_KERNEL_RESULTS=ABS_JSONL GOMAXPROCS=2 \
  go -C testing-harnesses/z3 test -run '^TestMatcherKernelsExperiment$' -count=1 -v ./dcore
```

## Go 1.27.4 confirmation

The same patch ran on `d893247` without other queue jobs running. All
36 reconstructions matched the target digest. Median build plus match
times in milliseconds, three runs per cell:

| Family | P8 | Sorted | Blocked |
| --- | ---: | ---: | ---: |
| Coreutils tar | 282.87 | 424.99 | 337.31 |
| Newtonsoft CLR | 30.08 | 191.63 | 75.71 |
| F-Droid DEX | 77.89 | 207.84 | 130.30 |
| Deterministic unrelated | 97.17 | 477.94 | 240.96 |

Blocked saved 44.6% of median allocated bytes on the tar cell, 17.9%
on CLR and 26.6% on DEX. Unrelated input allocated 21.1% more.
The best tar size changed by 238 bytes in a roughly 3.2 MB estimated
envelope. Other envelope lengths were identical. The size column in the
raw data measures the dcore envelope, without pack allocation or GC.
The original tar speed advantage did not reproduce. Retain P8 for these
workloads and keep the memory trade-off as evidence for future designs.
