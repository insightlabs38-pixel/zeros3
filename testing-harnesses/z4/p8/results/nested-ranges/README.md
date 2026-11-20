# Nested ZW1 window screening

The replay compared 1 MiB and 128 KiB windows against three pinned GNU
coreutils source tar versions. The experiment used Go 1.27.0 and the
9c1b952 source with four progress log lines added to its test harness.
Both modes selected two deltas and produced depth 0, 1 and 2. Fresh whole
reads matched all independent source hashes after reclamation. The depth-2
object matched the source tar at all 16 seeded 4 KiB offsets in both modes.

| Window | Retained bytes | Migration CPU | 4 KiB median, index cache off | 4 KiB median, cache 64 MiB |
| --- | ---: | ---: | ---: | ---: |
| 1 MiB | 21,901,451 | 230.53 s | 3.80 s | 4.22 s |
| 128 KiB | 22,397,038 | 180.33 s | 59.53 ms | 57.67 ms |

The 128 KiB layout used 495,587 more bytes after reclamation (2.26%).
These are one-cohort screening measurements collected under changing
concurrent host load. Cache-off disables the authority cache. OS page
caches were not purged. The 64 MiB numbers use the second pass over the
same offsets. The recorded RSS is the cumulative test-process peak and
cannot compare independent window settings. Read amplification, fanout
and old/new writer qualification are pending. The production default is
still 1 MiB.

Reproduce with the referenced corpus and an unused output path:

```sh
ZEROS3_B2_CORPUS=/path/to/corpus ZEROS3_RANGE_OUT=/tmp/p8-ranges.jsonl \
  go test -tags p8rangeexperiment -run '^TestP8NestedRangeExperiment$' \
  -count=1 -timeout 60m -v .
```
