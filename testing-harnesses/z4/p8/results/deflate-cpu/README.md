# Exact DEFLATE CPU screening

Go 1.27.0, amd64, GOMAXPROCS=1. Both binaries measured the pinned P8 coreutils
9.7 tar.gz inputs using two repetitions each. The corpus SHA-256 values are in
the P8 input manifest. The candidate commit is 29a1d96 on the CPU experiment
branch. It halves the populated head table and clears it at each unit boundary,
then compares two words at a time while finding the first differing byte.
The same changes apply to the research IR.

| Zlib level | P8 CPU median, s | Candidate CPU median, s | Added rep bytes |
| --- | ---: | ---: | ---: |
| 6 | 20.01 | 16.36 | 62663755 |
| 9 | 18.78 | 15.74 | 62910583 |

The representations replayed byte-for-byte and the stored representation sizes
matched. Wall times reflect other workloads on the same host. The candidate
child printed PASS and logged all four exact rows, which were matched against
its staged JSONL. Its controller was interrupted after the child finished, so
the rows are retained here as screening evidence outside the driver's published
results. The earlier predictive suite attempt timed out under host load. The
focused work-budget, malformed-stream and worker-parity controls passed.

Reproduce with the same pinned gzip files and a clean output tag:

```sh
GOMAXPROCS=1 testing-harnesses/z4/p8/run.sh TESTBIN OUTDIR TAG rep LEVEL6,LEVEL9 2
```

The original CPU profile accounted for the largest application share in
predictor search and head accesses. The next comparison should alternate the
binaries on a quiet host and confirm producer parity on the combined source.

The consolidated source at a8993ab passed the targeted Go 1.27.4 producer
suite. It compared streaming skeletons and units against one-shot encoding,
reconstructed the original streams through both paths, and exercised the
pinned zlib L1–L3 fixtures, warm-up variants, worker parity and work limits.
The run completed in 74.682 seconds. See combined-producer-oracles.log.
