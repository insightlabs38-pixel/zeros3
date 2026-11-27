# Reproduce FB-12

The X6 source was selected from
`F6-zeros3-audit/zeros3-f6-evidence.tar.gz`, under
`exp/x6-entropy/src/lzl`. The original file hashes in
`results/x6-original-sha256.txt` establish the starting point.

Build the pinned input manifest as a JSON array of
`{"corpus":"name","label":"source","path":"/local/input"}` rows.
The input byte sizes and hashes are in `results/native-records.txt`.
Use the X9 tree tar already identified in FB-05, C12 TypeScript and
HTTPX sources, and C14 SQLite, events and entropy fixtures.

```sh
GOMAXPROCS=2 ZEROS3_FB12_INPUTS=/path/to/input.json ZEROS3_FB12_RECORDS=/path/to/records \
  go test -run '^TestFB12ExportRecordCorpora$' -v -count=1 .
cd testing-harnesses
GOMAXPROCS=1 go build -o /tmp/fb12-range ./foundational/FB-12/bench
GOMAXPROCS=1 /tmp/fb12-range -input /path/to/records -decode-rounds 3
python foundational/FB-12/xz_compare.py /path/to/records --rounds 3
go test ./foundational/FB-12/lzl
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzRangePayload$' \
  -fuzztime=24000x -fuzzminimizetime=0 -parallel=2 ./foundational/FB-12/lzl
```

Go 1.27 and xz/liblzma 5.8.1 ran under the same two-core quota.
Pack byte accounting uses actual files on the workspace overlay.
The Go and Python decode timings both validate SHA-256 and exact
input bytes. The initial comparison runs are whole-corpus CPU
samples with ambient cache.

Additional matched lanes and native delta tests

```sh
# From testing-harnesses with the same bounded Go build as above.
GOMAXPROCS=1 /tmp/fb12-range -input /path/to/records -decode-rounds 3 \
  -lanes fast-deep:best,optimal-deep:best,fast-deep:dict,optimal-deep:dict
go test -race ./foundational/FB-12/lzl
GOMAXPROCS=2 go test -run '^$' -fuzz '^FuzzRangeContextPayload$' \
  -fuzztime=24000x -fuzzminimizetime=0 -parallel=2 ./foundational/FB-12/lzl
# From the root Go module. Base/target paths follow the C12 and C14
# fixture sets. Names and SHA-256 are in results/delta-native.txt.
ZEROS3_FB12_DELTA_INPUTS=/path/to/pairs.json ZEROS3_FB12_XD_PROGRAMS=/path/to/xd \
  go test -v -count=1 -run '^TestFB12NativeDeltaMatcherMatrix$' .
ZEROS3_FB12_STRUCTURED=/path/to/c14/final-corpus \
  go test -v -count=1 -run '^TestFB12StructuralAllocation$' .
go test -v -count=1 -run '^(TestFB12Windowed|TestFB12SharedWindow|TestAuthorityWindowedDelta|TestAuthorityWindowedMigrationReportsStagingENOSPC|TestC12FastReencode)' .
```

The pair manifest is an array of `{"name":"family","base":"/local/base","target":"/local/target"}`
objects. Use `windowed` for the C12 retained 33 MiB windowed control.
The exporter emits its independent XD programs into the same `.zrc`
format. Run the Go and xz harnesses on that directory. For the B9/B10
control, export the three C14 `parquet` and three `sst` versions as two
corpora, then compare the native allocated store to the distinct-record
entropy pack measurement. The native store includes metadata and retained
dependencies, which the record-only pack measurement omits.
