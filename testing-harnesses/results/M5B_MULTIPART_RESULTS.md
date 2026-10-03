# M5-B: persistent S3 multipart upload (AWS SDK for Go v2)

External, black-box proof that ZeroS3's persistent multipart upload
implementation (`CreateMultipartUpload`/`UploadPart`/`ListParts`/
`CompleteMultipartUpload`/`AbortMultipartUpload`/`ListMultipartUploads`)
interoperates with a real, unmodified AWS SDK for Go v2 client, using only
the SDK's low-level S3 client calls — never any internal ZeroS3 API — and
that multipart state and completed objects both survive a real process
restart, not just an in-process code path.

## Result: **43/43 passed**

## Versions and evidence

- **ZeroS3 commit tested:** branch `claude/zeros3-m5b-large-object-jfnvkb`,
  branched from and identical-tree to `main` (`ae5574957f62396a9aa8ed772501bb8e6df2f454`)
  at the start of this pass. See `zeros3`'s `STATUS.md` M5-B section for the
  exact resulting commit.
- **zeros3-testing commit/state:** this repository's HEAD at the commit
  that adds this file and `harness/m5b/multipart/main.go`.
- **Go toolchain:** `go1.27.0 linux/amd64`.
- **AWS SDK for Go v2 module versions** (same pin as every other harness in
  this repository — no new dependency was added):
  - `github.com/aws/aws-sdk-go-v2 v1.45.1`
  - `github.com/aws/aws-sdk-go-v2/config v1.33.1`
  - `github.com/aws/aws-sdk-go-v2/credentials v1.20.1`
  - `github.com/aws/aws-sdk-go-v2/service/s3 v1.109.1`
  - `github.com/aws/smithy-go v1.28.1`

## What the harness proves

### Core lifecycle, restart mid-upload, resume, completion

`CreateMultipartUpload` → `UploadPart` ×2 (6MiB each) → `ListParts`
(confirms 2 parts) → **kill and restart the zeros3 process against the
same store directory, on a fresh port** → `ListParts` again (confirms both
parts survived the restart) → `UploadPart` for a 3rd part (2MiB) →
`CompleteMultipartUpload` with all three parts in order → `HeadObject`
(content-length matches) → `GetObject` (exact byte round trip, SHA-256
equality against the independently-computed concatenation of all three
part buffers) → `Range` `GetObject` straddling the part-1/part-2 boundary
(exact byte match) → `CopyObject` of the completed multipart object (exact
byte round trip) → **restart the process again** → `GetObject` (SHA-256
still matches) → `ListParts` on the now-retired upload ID (rejected,
`NoSuchUpload`, confirming the session is not resurrected after the object
is durably visible).

### Abort scenario

`CreateMultipartUpload` → `UploadPart` (1 part) → `AbortMultipartUpload` →
`ListParts` on the aborted ID (rejected) → `HeadObject` on the target key
(rejected — the aborted upload's key never became an ordinary visible
object).

### Negative scenarios

- `ListParts` with a syntactically-valid but nonexistent upload ID:
  rejected (`NoSuchUpload`).
- `CompleteMultipartUpload` with a deliberately wrong ETag for an uploaded
  part: rejected (`InvalidPart`).
- `CompleteMultipartUpload` with an empty part list: rejected
  (`MalformedXML`).

### `ListMultipartUploads`

Two concurrent open uploads on the same bucket both appear in
`ListMultipartUploads`.

## Reproduction

```sh
cd zeros3            # the ZeroS3 repository
go build -o /tmp/zeros3-bin zeros3.go

cd testing-harnesses/harness/m5b/multipart
ZEROS3_BIN=/tmp/zeros3-bin go run .
```

## Regression harnesses rerun against the same ZeroS3 binary

Confirming multipart's addition (new journal record types, new HTTP
routing, `classifySigV4Payload`) changed nothing about existing
interoperability:

| Harness | Result |
|---|---|
| M2 canonical workflow (`harness/m2`) | **41/41 passed** |
| M3 CopyObject (`harness/m3/copy`) | **46/46 passed** |
| M3 Range GET (`harness/m3/range`) | **27/27 passed** |
| M3 dedup evidence (`harness/m3/dedup`) | **7/7 passed** |
| M5-A presign (`harness/m5a/presign`) | **47/47 passed** |

## Payload modes actually observed

The pinned AWS SDK for Go v2's `UploadPart`/`CreateMultipartUpload`/
`CompleteMultipartUpload` calls in this harness all used ordinary fixed
`x-amz-content-sha256` (a literal SHA-256 digest) — never
`STREAMING-AWS4-HMAC-SHA256-PAYLOAD`/`-TRAILER`. No server-side
`NotImplemented` rejection was logged at any point in this run
(`grep -i error` over the full server log is empty). This confirms neither
eligible HMAC streaming mode was required to complete this workflow — see
`M5B_RCLONE_LARGE_OBJECT_RESULTS.md` for the same finding against a
second, independent real client.
