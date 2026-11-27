# Reproduce FB-14

Use Go 1.27 and the FB-13 JSON fixture schema. The seven families are
the X9 eight-tree history, C12 TypeScript tar and gzip histories,
held-out C12 HTTPX, C14 SQLite and Parquet histories, and the C12
native retained windowed pair. Fixture SHA-256 and lengths appear in
the logs. Retain the last four historical versions plus current when
a corpus has at least six versions.

```sh
GOMAXPROCS=2 ZEROS3_FB14_SERIES=/path/to/series.json \
  go test -v -count=1 -timeout=4m15s \
    -run '^(TestFB14NativeOptimizedBaselines|TestFB14ChunkDeltaPanel|TestFB14PatchBounds)$' .
```

The native control starts with recompressed packs, measures the C13
preset proposal separately, and compares archive and mixed authority
policies with current representation and delta adapters. It reopens,
deep-verifies, and checks every retained version. In the scratch
pack lanes, bound the index to 2,048 four-posting buckets. Compare
otherwise identical no-delta and delta ingest for targets from 8 to
64 KiB. The fixture byte buffers are resident in the harness and its
latency numbers come from local in-process reads.

The scratch format uses a test-only codec tag. It stores the full base
digest, the program encoding mode and a checked native XD frame.
Throughput samples exclude format negotiation or publication.
