# Catalog allocation for a small refresh

This isolated benchmark streams the verified 26,624-row production
catalog with one touched digest. The recorded Go 1.27.4 sources used
GOMAXPROCS=2 and three repetitions. It excludes the incremental manifest
prepass, run hardlinks and publication.

| Build | Identities retained | Median bytes allocated per call | Allocations per call |
| --- | ---: | ---: | ---: |
| Baseline | 26,624 | 3,250,489 | 152–153 |
| Bounded refresh | 1 | 66,008 | 7 |

The candidate still reads and copies every catalog row. The focused
race test checks that the copied bytes, identity and runs are preserved
through an alias update, recovery, compaction and maintenance.

Copy benchmark.go.txt to zeros3_similarity_catalog_bench_test.go in
the source worktree, set ZEROS3_C2_INDEX_STORE to the expanded verified
fixture and run `go test -run '^$' -bench '^BenchmarkSimilarityCatalogSmallRefresh$' -count=3 -benchtime=1s .`.
