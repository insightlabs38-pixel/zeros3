# M7 — release-candidate external validation freeze

Every harness in this repository, run unmodified (except for
`harness/rclone`'s M5-B-supersession fix, see below) against one exact
`zeros3` release-candidate build, in one sitting. This is the M7I
"external validation freeze" record: the consolidated result set that
must stay green for the release candidate to be considered submission-
ready.

## Build under test

```
zeros3 commit:    381db3c0cd32108437d3951917f78d23c0d625ad
zeros3 branch:    claude/zeros3-m7-release-o6wnzj
zeros3-testing commit (this run): HEAD at the commit that adds this file
Go toolchain:     go1.27.0 linux/amd64
Build command:    go build -o zeros3-bin zeros3.go   (from the zeros3 checkout)
Binary SHA-256:   b0902f95fca2bb7e0a84db13061c29cbdeaa8491d912c6bc633ab159af885e0c
```

## Consolidated result

**404 passed, 0 failed, 4 informational, 1 documented known limitation**
across every harness in this repository.

| Harness | Result | Reference |
|---|---|---|
| `harness/m2` (canonical AWS SDK workflow) | **41/41 passed** | `results/M2_RESULTS.md` |
| `harness/m3/copy` (CopyObject) | **46/46 passed** | `results/M3_CORRECTION_RESULTS.md` |
| `harness/m3/range` (Range GET) | **27/27 passed** | `results/M3_CORRECTION_RESULTS.md` |
| `harness/m3/dedup` (dedup evidence) | **7/7 passed** | `results/M3_CORRECTION_RESULTS.md` |
| `harness/m5a/presign` (presign/vhost/CLI) | **47/47 passed** | `results/M5A_PRESIGN_RESULTS.md` |
| `harness/m5b/multipart` (persistent multipart) | **43/43 passed** | `results/M5B_MULTIPART_RESULTS.md` |
| `harness/m5d/pagination` (ListParts/ListMultipartUploads pagination) | **43/43 passed** | `results/M5D_PAGINATION_RESULTS.md` |
| `harness/m6/sync` (delta sync) | **33/33 passed, 2 informational** | `results/M6_SYNC_RESULTS.md` |
| `harness/m6c/dirsync` (recursive directory sync) | **69/69 passed, 2 informational** | `results/M6C_DIRSYNC_RESULTS.md` |
| `harness/package-killer` (ZeroS3 vs s3rver 3.7.1) | **14/14 passed on ZeroS3, 14/14 on s3rver — GO** | `results/PACKAGE_KILLER_RESULTS.md` |
| `harness/rclone` (T1 secondary client) | **20/20 passed, 1 documented known limitation** | this file, "rclone harness fix" below |

Sum of pass counts: 41+46+27+7+47+43+43+33+69+14+14+20 = **404**. Zero
failures anywhere. The 4 "informational" lines (2 in `m6/sync`, 2 in
`m6c/dirsync`) are expected, non-deterministic race-outcome reports
(which side of a deliberate AWS-SDK-vs-`zeros3-sync` race won this
run) — both outcomes are asserted safe either way, so neither is a
pass/fail signal; see those harnesses' own results files.

## rclone harness fix (M7, this run)

`harness/rclone/main.go`'s `checkRcloneUploadLimitation` still expected
rclone's *default* (`UNSIGNED-PAYLOAD`) upload path to be rejected by
ZeroS3 — accurate when the harness was first written, but M5-B added
header-auth `UNSIGNED-PAYLOAD` support specifically so this path would
work (see `results/M5B_RCLONE_LARGE_OBJECT_RESULTS.md`, already landed
before M7). The probe was never updated to match: run unmodified against
the M7 release candidate, it produced 4 false failures (the now-
succeeding probe left an orphan object that broke the harness's later
"bucket is empty" and "DeleteBucket" assertions on a target that was not
actually broken).

This is a stale-harness bug, not a `zeros3` regression: `results/
RCLONE_RESULTS.md` itself already carries a "Superseded in part by M5-B"
notice pointing at exactly this gap. Fixed in this pass (see
`harness/rclone/main.go`'s history) to expect and clean up after the now-
successful default upload; the second probe (rclone's own non-seekable-
body limitation when forced into literal-payload signing) is a genuine,
unrelated rclone-side limitation and is unaffected by M5-B, so it still
correctly reports as a known limitation. New result: **20 passed, 0
failed, 1 documented known limitation** (up from the stale pre-fix
baseline of 19/0/2 — one former "known limitation" is now a genuine
pass, matching the M5-B capability).

## Reproduction

```sh
# 1. build the exact release candidate
cd /path/to/zeros3 && git checkout 381db3c0cd32108437d3951917f78d23c0d625ad
go build -o /tmp/zeros3-bin zeros3.go

# 2. from this repository
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m2
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m3/copy
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m3/range
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m3/dedup
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m5a/presign
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m5b/multipart
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m5d/pagination
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m6/sync
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m6c/dirsync
S3RVER_BIN=/path/to/node_modules/.bin/s3rver ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/package-killer   # s3rver 3.7.1, npm-installed into a scratch dir outside both repos
ZEROS3_BIN=/tmp/zeros3-bin RCLONE_BIN=/path/to/rclone go run ./harness/rclone   # rclone v1.75.0
```

No harness was rewritten to make a failure disappear; the one harness
change in this pass (`harness/rclone`) corrects an assumption that a
previous, already-shipped milestone (M5-B) had already made stale, and
is documented as such above rather than silently absorbed.
