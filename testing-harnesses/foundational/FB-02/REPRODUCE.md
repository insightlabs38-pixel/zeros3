# Reproducing FB-02

Go 1.27.0, standard library only, from the repository root on
`research/next`. Times are from a 4 vCPU Xeon 2.30 GHz container with 16 GiB.

## Tests

```sh
go test -count=1 -run 'FB02' .                       # every FB-02 test, ~1 min
go test -count=1 -run 'TestFB02_LargePartsHTTP' -v . # M-A4 over HTTP, ~25 s, 9 GiB ingested
go test -race -count=1 -run 'TestFB02_(Snapshot|Concurrent|Fault)' .
go test -count=1 -timeout 110m .                     # full suite, ~23 min
```

## M-A3 ingest CPU and checksum throughput

`zeros3_fb02_bench_test.go` uses only pre-FB-02 APIs, so it also runs on the
FB-01R head (`git worktree add ../base 5a10a9a`, copy the file in). Interleave
8 rounds of base and new:

```sh
go test -run '^$' -bench '^BenchmarkFB02Put' -benchtime 100x .
go test -run '^$' -bench '^BenchmarkFB02(Algo|PutAlgo)' -benchtime 100x -count 3 .
```

## M-A5 equivalence

```sh
go test -count=1 -v -run 'TestFB02_(ReplicateEquivalenceCases|MultipartOriginEquivalence|CreateOnlyRefuses|NamespaceRerunWritesNothing|CommitIfUnchanged|ConcurrentIfUnchanged|IfUnchangedCrashCuts|SnapshotDescribeAndRestore|AuthorityImportRetry)' .
```

The two negative controls in `results/ma5_equivalence.txt` are manual edits:
remove the pre-check in `publishSyncManifest`, or make the planner's
equivalence `exists && etag == desc.ETag`; each must fail the named tests.

## M-A6 restore sessions

```sh
sh testing-harnesses/foundational/FB-02/ma6_profile.sh /tmp/fb02-ma6   # ~15 min
```

It builds the test binary once, runs each configuration of
`TestFB02SnapshotProfile` in its own process (peak RSS is `VmHWM`, reset after
the fixture is built), then the describe and open benchmarks. Snapshots are
synthetic: N entries naming one real 4 KiB object, so the snapshot read is
what differs between fresh and session runs. One profile by hand:

```sh
FB02_PROFILE='mode=session n=10000 case=ok' go test -v -count=1 -run '^TestFB02SnapshotProfile$' .
```

`case` is `ok`, `deleted` (snapshot deleted after half the objects),
`corrupt` (one byte flipped after half), `describe` (20 describes) or `heap`
(index bytes retained after a GC).

## Fuzz (G-FUZZ)

```sh
for f in FuzzFB02AWSChunkTrailer FuzzFB02PayloadCheck FuzzFB02SnapshotIndex FuzzFB02SyncIdentity; do
  go test -run '^$' -fuzz "^$f\$" -fuzztime 60s .
done
```

## Checksums

`SHA256SUMS` covers every file under this directory except itself:

```sh
cd testing-harnesses/foundational/FB-02 && sha256sum -c SHA256SUMS
```
