# M8D — full release regression

Every historical harness in this repository, re-run **unmodified**,
against the exact same M8D build, plus the two preflight validations
(rclone, Package Killer) re-confirmed at their release-candidate level.

## Build under test

```
zeros3 commit:     5052a50 (branch claude/zeros3-m8d-validation-cow-7dnjql)
Go toolchain:      go1.27.0 linux/amd64
Build command:     CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
Binary SHA-256:    a99cf57d50fb36f6565681840098e59c61a954918ef276cb6188007631762825
```

Byte-identical to the build tested in `results/M8D_FORK_RESULTS.md`
(commit `a920dbf`) and `results/M8D_PREFLIGHT_RESULTS.md` (the exact
merged M8C baseline `0f9fc327...`) -- confirming `zeros3.go`/
`zeros3_test.go` did not change between `a920dbf` (fork implementation)
and `5052a50` (hostile-review test addition only), and that the
reproducible build property held throughout.

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
| **`m8d/fork` (new)** | **146 passed, 0 failed, 3 informational** | new this pass |
| `rclone` | 20 passed, 0 failed, 1 documented known limitation | yes, identical |
| `package-killer` | ZeroS3 14/14, s3rver 14/14 -- **GO** | yes, identical |

**Totals: 828 passed, 0 failed, 14 informational, 1 documented known
limitation, across 15 harnesses.** Every pre-existing harness's count is
byte-for-byte identical to its own last-recorded baseline
(`results/M8C_NAMESPACE_REPLICATION_RESULTS.md` for the 12 harnesses
recorded there, `results/M8D_PREFLIGHT_RESULTS.md` for rclone/
package-killer's own M8D-session rerun) -- zero regressions anywhere.

## Internal (zeros3 repo) regression

```
gofmt -l .                -> clean
go vet ./...               -> clean
go test ./...               -> ok, 479 tests (up from 442 at the M8C
                               baseline -- 37 new M8D tests), ~63s
go test -race ./...         -> ok
```

## Reproducibility / dependency proof

- Two independent clean clones of the `zeros3` repository at commit
  `5052a50`, each built independently
  (`CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go`):
  byte-identical SHA-256
  (`a99cf57d50fb36f6565681840098e59c61a954918ef276cb6188007631762825`).
- `go.mod`: `module zeros3` / `go 1.27.0`, zero `require` directives --
  unchanged from the M8C baseline.
- No `golang.org/x/...` import anywhere in `zeros3.go` or
  `zeros3_test.go`.
- No `vendor/` directory.
- `zeros3.go` remains the sole implementation source file;
  `zeros3_test.go` remains the sole first-party test file (confirmed:
  `find . -name "*.go"` lists exactly these two).
- No new stdlib import was added by M8D (confirmed via `git diff` against
  the `m8c-gold` checkpoint: only existing already-imported packages are
  used by the new fork code).

## Verdict

No regression anywhere in the full historical matrix. M8D's own new
harness (internal: 37 tests; external: 146 assertions) is green. The
release regression gate is satisfied.
