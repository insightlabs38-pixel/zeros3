# Root collection rebuild comparison

The Go 1.27.4 binaries recorded in the build manifests replayed the
same production fixture from the expanded churn screen. Each ran
`authority index-build -store STORE` on the same machine with
`GOMAXPROCS=2` and the chunk cache disabled. Both staged a fresh
generation and left CURRENT unchanged. The fixture had 26,624 current
and 26,624 historical roots.

The comparison verified identical journal and snapshot digests, catalog
counts and every encoded posting and directory byte. New generation IDs
and their checksums differ by design.

| Build | CPU | Wall | Peak RSS |
| --- | ---: | ---: | ---: |
| Before compact root collection | 146.53 s | 808.05 s | 155,688 KiB |
| Compact root collection | 165.05 s | 969.94 s | 165,180 KiB |

This single pair used a heavily loaded qualification host. The whole
operation consumed more time and memory in the candidate trial.
It establishes exact output parity without a measured end-to-end win.

An isolated follow-up timed only root collection on the already-open
store. Each of three runs returned all 53,248 roots. Allocation fell
from 24,304,272 bytes and 72 allocations per call to 2,555,904 bytes
and one allocation per call. Wall timings varied under concurrent
qualification. This improvement is for namespace root collection.
The end-to-end rebuild measurements above remain the observed outcome.

To repeat the isolated measurement, copy collection-benchmark.go.txt
into each source worktree as zeros3_similarity_roots_bench_test.go,
point ZEROS3_C2_INDEX_STORE at the verified expanded fixture, and run
`go test -run '^$' -bench '^BenchmarkSimilarityRootCollection$' -benchmem -benchtime=1s -count=3 .`
with Go 1.27.4 and GOMAXPROCS=2.
