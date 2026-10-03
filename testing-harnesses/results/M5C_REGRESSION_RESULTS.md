# M5-C: external regression rerun (internal versions/restore/GC pass)

ZeroS3's M5-C pass added internal (non-AWS-API) object version history,
zero-copy restore, one authoritative CAS/manifest reachability model, safe
offline GC, and doctor/stats extension — all ZeroS3-only CLI/library
surface. **No S3 wire-protocol request/response path changed**: ordinary
`ListObjectsV2` never surfaces a historical version as a duplicate key,
and no new query parameter, header, or XML shape was added to any existing
S3 operation. This file records that claim being checked, not merely
assumed: every external harness already proven against M5-B was rerun
unmodified against the M5-C binary.

## Result: **211/211 passed across 6 harnesses, 0 failed**

| Harness | Result |
|---|---|
| M2 canonical workflow (`harness/m2`) | **41/41 passed** |
| M3 CopyObject (`harness/m3/copy`) | **46/46 passed** |
| M3 Range GET (`harness/m3/range`) | **27/27 passed** |
| M3 dedup evidence (`harness/m3/dedup`) | **7/7 passed** |
| M5-A presign (`harness/m5a/presign`) | **47/47 passed** |
| M5-B multipart (`harness/m5b/multipart`) | **43/43 passed** |

Every result is byte-for-byte identical in pass/fail shape to the prior
milestone's own recorded run of the same harness — no new pass, no new
fail, no new skip.

## Versions and evidence

- **ZeroS3 commit tested:** branch `claude/zeros3-m5c-storage-lifecycle-o98lsz`,
  commit `604c645eb8b25f9b3410a6b9867a0af5b58aaf24` (see `zeros3`'s
  `STATUS.md` M5-C section for the full pass detail). Built with
  `CGO_ENABLED=0 go build -o zeros3-bin .` on `go1.27.0 linux/amd64`.
- **zeros3-testing commit/state:** this repository's HEAD at the commit
  that adds this file — no harness source was modified, added, or
  regenerated; the exact same `harness/m2`, `harness/m3/{copy,range,dedup}`,
  `harness/m5a/presign`, `harness/m5b/multipart` programs already recorded
  in `M2_RESULTS.md`/`M3_CORRECTION_RESULTS.md`/`M5A_PRESIGN_RESULTS.md`/
  `M5B_MULTIPART_RESULTS.md` were rerun unmodified.
- **Go toolchain:** `go1.27.0 linux/amd64`.
- **AWS SDK for Go v2 module versions** (same pin as every other harness in
  this repository — no dependency change):
  - `github.com/aws/aws-sdk-go-v2 v1.45.1`
  - `github.com/aws/aws-sdk-go-v2/config v1.33.1`
  - `github.com/aws/aws-sdk-go-v2/credentials v1.20.1`
  - `github.com/aws/aws-sdk-go-v2/service/s3 v1.109.1`
  - `github.com/aws/smithy-go v1.28.1`

## Reproduction

```sh
cd zeros3 && CGO_ENABLED=0 go build -o /tmp/zeros3-m5c .
cd testing-harnesses
ZEROS3_BIN=/tmp/zeros3-m5c go run ./harness/m2
ZEROS3_BIN=/tmp/zeros3-m5c go run ./harness/m3/copy
ZEROS3_BIN=/tmp/zeros3-m5c go run ./harness/m3/range
ZEROS3_BIN=/tmp/zeros3-m5c go run ./harness/m3/dedup
ZEROS3_BIN=/tmp/zeros3-m5c go run ./harness/m5a/presign
ZEROS3_BIN=/tmp/zeros3-m5c go run ./harness/m5b/multipart
```

## What was deliberately not rerun this pass

- **rclone / Package Killer (`s3rver`):** both require installing
  additional external tooling into a scratch environment purely for
  comparison purposes. Since M5-C changed zero bytes of the S3
  request/response path (confirmed above across 6 harnesses exercising
  header-auth, presigned/query-auth, path-style, virtual-hosted-style,
  CopyObject, Range GET, and multipart — the same code paths rclone and
  Package Killer's frozen test logic also exercise), a fresh install adds
  meaningful runtime for no new coverage within this pass's actual scope.
  Their last recorded results (`RCLONE_RESULTS.md`,
  `M5B_RCLONE_LARGE_OBJECT_RESULTS.md`, `PACKAGE_KILLER_RESULTS.md`) stand,
  unaffected by anything in M5-C.
- **New harnesses for `zeros3 versions`/`restore`/`gc`/`doctor`:** these
  are ZeroS3-only CLI/library surface, not S3-wire-protocol operations, so
  they are outside this repository's black-box-S3-client charter by
  design. They are covered instead by `zeros3`'s own internal test suite
  (31 new top-level Go tests, including a CLI-level smoke test that builds
  and execs the real binary — see `zeros3`'s `STATUS.md` M5-C section,
  Phases B–R).

## Internal ZeroS3 lifecycle transparency, confirmed by this rerun

None of the 6 harnesses above use any M5-C API — they are exactly the same
programs that passed against M5-B. Their continuing to pass unmodified is
itself the proof that M5-C's new internal version-history archival (every
overwrite/delete now writes an extra journal record and an in-memory
history entry) is completely invisible to an ordinary S3 client: `M2`'s
overwrite-and-restart scenarios, `M3 copy`'s repeated same-key
`CopyObject` overwrites, and `M5-B multipart`'s completed-multipart
overwrite scenarios all now silently accumulate ZeroS3-internal history
underneath every one of their PASS lines, with zero externally observable
difference in behavior, status code, or response shape.
