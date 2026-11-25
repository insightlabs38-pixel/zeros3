# ZeroS3 C7 · Bounded optimization with disk-backed candidates

**C7_INDEX_EFFECTIVE.** The existing verified posting index now supplies
same-bucket bases to bounded global optimization. The one-target pass
recovered most of the full search's retained-byte benefit.

## New-version pass

`authority optimize -max-targets 1 -indexed-discovery` probes the prior
generation and resolves digests against the current graph. Posting runs,
catalog and original journal prefix are verified before use. The hint
must resolve to a live content with the same length and bucket.
The ordinary encoder and publication proof verify the selected base
and exact target bytes. Older generations can remain useful after writes.

| Workload | Full CPU / retained | One target CPU / retained | Indexed one target CPU / retained |
| --- | ---: | ---: | ---: |
| Binary revisions, 64 unrelated objects | 1.323 s / 1,486,848 B | 0.022 s / 1,654,784 B | 0.319 s / 1,482,752 B |
| Coreutils revisions, 64 unrelated objects | 0.258 s / 622,592 B | 0.025 s / 753,664 B | 0.171 s / 622,592 B |
| Binary revisions, 1,024 unrelated objects | 1.488 s / 6,230,016 B | 0.089 s / 6,397,952 B | 0.314 s / 6,225,920 B |
| Held-out HTTPX compressed history | 0.063 s / 684,032 B | 0.019 s / 684,032 B | 0.045 s / 684,032 B |

Retained data excludes the disposable index, which every paired clone
contained. The 1,031 post-build contents occupied 495,616 index bytes
including filesystem allocation and directory costs. The retained
data savings and the index footprint are separate expenses. The dense
append refresh took 0.039 CPU seconds. One deletion and prune produced
one tombstone in 0.048 CPU seconds. Reconcile and append preserve existing
generations until explicit pruning. HTTPX illustrates candidate cost
without savings.

Full dense analysis took 1.44 s wall versus 0.33 s indexed, at comparable
final allocation. C2's completed-pass memo took about 0.054 s on an
unchanged store. That memo experiment had no newly added revision.
The first pilot selected the most recent unrelated small object under the
one-target limit. The matched trials uploaded the revision last.

## Namespace scale

The separate B3 fixture writes journal metadata records without object
bodies. Fresh-process figures compare checkpoint open and journal replay
of the same namespace. They do not qualify million-object S3 serving.

| Synthetic entries | Checkpoint open | Journal replay open |
| --- | ---: | ---: |
| 1,000 | 1.25 ms | 2.09 ms |
| 10,000 | 4.29 ms | 46.05 ms |
| 100,000 | 30.92 ms | 195.62 ms |
| 1,000,000 | 273.48 ms | 2,526.94 ms |

The million-entry journal was 501.4 MB and the checkpoint was 94.3 MB.
After open, the process's peak RSS was 27.5 MiB with the checkpoint
and 705.6 MiB with replay. Index verification still reads its metadata
and the relevant journal prefix. Full candidate construction remains
available for unrelated or canonical histories with weak sketch recall.

## Verification and replay

The focused suite covered strict/current and historical-prefix queries,
scope isolation, digest corruption, tombstones, multipart/copy state,
publication restart and reconstruction after index loss. A corrupted
catalog also exercised the CLI's local fallback and exact ordinary reads.
All optimizer lanes finalized, reopened and checked exact bytes.

Run `incremental.py --help` for frozen C2/C7 binaries and the C5 corpus.
Build the root test binary with `go test -c` and pass it to `namespace.py`
for the metadata curve. [Raw evidence](results/) includes the dense lane,
the fallback and the separate namespace measurements. C8 investigates
repeated proofs and bounded I/O on native reconstruction.
