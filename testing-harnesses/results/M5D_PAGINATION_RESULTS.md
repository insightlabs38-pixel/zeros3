# M5-D: ListParts / ListMultipartUploads pagination (AWS SDK for Go v2)

External, black-box proof that ZeroS3's newly-added `ListParts` and
`ListMultipartUploads` pagination (`part-number-marker`/`max-parts`,
`key-marker`/`upload-id-marker`/`max-uploads`) interoperates with a real,
unmodified AWS SDK for Go v2 client, using only the SDK's low-level S3
client calls — never any internal ZeroS3 API — with deliberately small
page sizes (`max-parts=2`, `max-uploads=2`) so a small, cheap fixture
still forces multiple real pagination round trips.

## Result: **43/43 passed**

## Versions and evidence

- **ZeroS3 commit tested:** branch `claude/zeros3-m5d-multipart-pagination-gzlhq8`,
  branched from and identical-tree to `main` at the start of this pass
  (`6ef28fd042d57a56e1eeb5c914b4b274d5b36894`). See `zeros3`'s `STATUS.md`
  M5-D section for the exact resulting commit.
- **zeros3-testing commit/state:** this repository's HEAD at the commit
  that adds this file and `harness/m5d/pagination/main.go`.
- **Go toolchain:** `go1.27.0 linux/amd64`.
- **AWS SDK for Go v2 module versions** (same pin as every other harness in
  this repository — no new dependency was added):
  - `github.com/aws/aws-sdk-go-v2 v1.45.1`
  - `github.com/aws/aws-sdk-go-v2/config v1.33.1`
  - `github.com/aws/aws-sdk-go-v2/credentials v1.20.1`
  - `github.com/aws/aws-sdk-go-v2/service/s3 v1.109.1`
  - `github.com/aws/smithy-go v1.28.1`

## What the harness proves

### `ListParts` pagination

`CreateMultipartUpload` → `UploadPart` × 7 (tiny bodies — `ListParts`
pagination doesn't depend on part size, so no 5MiB minimum-part-size
fixture is needed) → repeated `ListParts` calls with `MaxParts=2`,
following `NextPartNumberMarker` into `PartNumberMarker` on each
subsequent call, until `IsTruncated=false`. Asserts: more than one page
was actually visited; every page has at most 2 parts; the union across
pages is exactly parts 1..7, each exactly once, in ascending order. A
final unpaginated call (default `max-parts`) confirms the single-page
path still returns all 7 parts with `IsTruncated=false`.

### `ListMultipartUploads` pagination

Six active uploads are created across five keys — `alpha`, `bravo` (two
concurrent uploads on the same key), `charlie`, `delta`, `echo` — plus a
seventh upload that is immediately completed and an eighth that is
aborted. Repeated `ListMultipartUploads` calls with `MaxUploads=2`,
following `NextKeyMarker`/`NextUploadIdMarker` into
`KeyMarker`/`UploadIdMarker` on each subsequent call, until
`IsTruncated=false`. Asserts: more than one page was actually visited;
every page has at most 2 uploads; the union across pages is exactly the 6
active uploads, each exactly once, in key-then-upload-ID order (proving
the `bravo` upload-ID tie-break survives a page boundary); the completed
upload never appears in any page; a follow-up unpaginated call after
aborting the eighth upload confirms it, too, never appears.

## Commands used

```sh
cd /path/to/zeros3 && go build -o /tmp/zeros3-bin .
cd /path/to/zeros3/testing-harnesses
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m5d/pagination
```

## Regression: M5-B multipart harness re-run unmodified

The pre-existing M5-B multipart harness (`harness/m5b/multipart`) was
re-run unmodified against the same `zeros3-bin` build to confirm the
pagination change introduced no regression in the rest of the multipart
lifecycle (crash/restart, completion, abort, negative cases):

```sh
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m5b/multipart
```

Result: **43/43 passed**, unchanged from `results/M5B_MULTIPART_RESULTS.md`.

## Known limitations of this harness

- Does not exercise `ListMultipartUploads`' `delimiter`/`prefix`
  parameters — ZeroS3 does not implement them for this operation (see
  `S3_COMPAT.md`); only the pagination parameters are in scope for M5-D.
- Does not independently verify AWS's exact XML rendering of
  `NextPartNumberMarker` when `ListParts` is *not* truncated (AWS's own
  published docs contain no example of that specific case); ZeroS3 always
  renders it (0 when not truncated), a documented, evidence-informed
  choice — see `S3_COMPAT.md`'s "Compatibility deviations" section.
