# M8E — full release regression

Every historical harness in this repository, re-run **unmodified**,
against the exact same M8E build, plus rclone and Package Killer
re-confirmed at their release-candidate level.

## Build under test

```
zeros3 commit:     83415fa (branch claude/zeros3-m8e-snapshots-5a6inu)
Go toolchain:      go1.27.0 linux/amd64
Build command:     CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
Binary SHA-256:    9f87452c07231e3614d98e9a9d6ddcd53ad728e7f6ad1827f5ebf114c04707cd
```

Byte-identical to the build tested in `results/M8E_SNAPSHOT_RESULTS.md`.

## Results

| Harness | Result | Matches recorded baseline? |
|---|---|---|
| `m2` | 41 passed, 0 failed | yes, identical |
| `m3/copy` | 46 passed, 0 failed | yes, identical |
| `m3/range` | 27 passed, 0 failed | yes, identical |
| `m3/dedup` | 7 passed, 0 failed | yes, identical |
| `m5a/presign` | 47 passed, 0 failed | yes, identical |
| `m5b/multipart` | 43 passed, 0 failed | yes, identical |
| `m5d/pagination` | 43 passed, 0 failed | yes, identical |
| `m6/sync` | 33 passed, 0 failed, 2 informational | yes, identical |
| `m6c/dirsync` | 69 passed, 0 failed, 2 informational | yes, identical |
| `m8a/remote_delta` | 34 passed, 0 failed, 4 informational | yes, identical |
| `m8b/repair` | 133 passed, 0 failed, 1 informational | yes, identical |
| `m8c/namespace_replication` | 111 passed, 0 failed, 2 informational | yes, identical |
| `m8d/fork` | 146 passed, 0 failed, 3 informational | yes, identical |
| **`m8e/snapshot` (new)** | **151 passed, 0 failed, 3 informational** | new this pass |
| `rclone` | 20 passed, 0 failed, 1 documented known limitation | yes, identical |
| `package-killer` | ZeroS3 14/14, s3rver 14/14 -- **GO** | yes, identical |

**Totals: 965 passed, 0 failed, 17 informational, 1 documented known
limitation, across 16 harnesses.** Every pre-existing harness's count is
byte-for-byte identical to its own last-recorded baseline
(`results/M8D_RELEASE_REGRESSION_RESULTS.md`) -- zero regressions
anywhere.

## Internal (zeros3 repo) regression

```
gofmt -l .                -> clean
go vet ./...               -> clean
go test ./...               -> ok, 552 tests (up from 479 at the M8D
                               baseline -- 73 new M8E tests), ~86s
go test -race ./...         -> ok (~196s)
```

## Reproducibility / dependency proof

- Two independent clean builds of `zeros3` at commit `83415fa`, each
  built independently
  (`CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go`):
  byte-identical SHA-256
  (`9f87452c07231e3614d98e9a9d6ddcd53ad728e7f6ad1827f5ebf114c04707cd`).
- `go.mod`: `module zeros3` / `go 1.27.0`, zero `require` directives --
  unchanged from the M8D baseline.
- No `golang.org/x/...` import anywhere in `zeros3.go` or
  `zeros3_test.go`.
- No `vendor/` directory.
- `zeros3.go` remains the sole implementation source file;
  `zeros3_test.go` remains the sole first-party test file.
- No new stdlib import was added by M8E (confirmed via `deps-proof.txt`
  regeneration: `go list -deps .` package list byte-identical to the
  M8D baseline -- M8E's snapshot format/create/GC-integration/restore
  code uses only already-imported packages: `encoding/binary`,
  `hash/crc32`, `sort`, `strings`, `sync`, `net/url`, `os`,
  `path/filepath`, `time`, `fmt`, `errors`, `encoding/json`,
  `net/http`, `flag`).

## Verdict

No regression anywhere in the full historical matrix. M8E's own new
harness (internal: 73 tests; external: 151 assertions across all 10
required phases) is green. The release regression gate is satisfied.
