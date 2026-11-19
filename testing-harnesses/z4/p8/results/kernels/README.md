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
