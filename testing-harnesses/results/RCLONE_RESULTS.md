# rclone interoperability results (T1)

> **Superseded in part by M5-B.** The "KNOWN-LIMITATION: rclone default
> upload (UNSIGNED-PAYLOAD)" finding below was accurate for the ZeroS3
> commit this file was written against, but M5-B added header-auth
> `UNSIGNED-PAYLOAD` support specifically to close this gap. See
> `M5B_RCLONE_LARGE_OBJECT_RESULTS.md` for confirmation that rclone's
> ordinary (unpatched, no special flags) upload path now completes
> end-to-end, including a genuine 1 GiB multipart upload. This file is
> otherwise left as the historical record of the post-M4 pass and is not
> rewritten.

Real, black-box `rclone` interoperability proof against a built `zeros3`
binary, per `PACKAGE_KILLER.md`/`TEST_MATRIX.md`'s T1 secondary-client
requirement. `rclone` is used exactly as any user would invoke it from a
shell, with ordinary S3-remote configuration (endpoint, credentials,
path-style addressing, `list_version=2`) — never patched, never given
ZeroS3-specific application logic.

## Versions tested

- **ZeroS3 commit:** `1042dec8c15c054cd0c1353474131c8f24b31aec`
  (`claude/zeros3-post-m4-package-killer-9afzph`)
- **zeros3-testing commit:** this repository's HEAD at the time this file
  was committed (see the commit that adds this file)
- **rclone:** `v1.75.0` (`go/version: go1.26.5`, static, linux/amd64) —
  downloaded directly from `https://downloads.rclone.org/v1.75.0/`, not the
  distro-packaged `1.60.1` (too old to represent a current client)
- **Go toolchain:** `go1.27.0 linux/amd64`

## How to reproduce

```sh
# 1. build zeros3 from the pinned commit
cd /path/to/zeros3 && git checkout 1042dec8c15c054cd0c1353474131c8f24b31aec
go build -o /tmp/zeros3-bin .

# 2. install rclone v1.75.0 (or note your own pinned version)
curl -sSL -o /tmp/rclone.zip https://downloads.rclone.org/v1.75.0/rclone-v1.75.0-linux-amd64.zip
cd /tmp && unzip -q rclone.zip
install -m 755 rclone-v1.75.0-linux-amd64/rclone /usr/local/bin/rclone

# 3. run the harness
cd /path/to/zeros3/testing-harnesses
ZEROS3_BIN=/tmp/zeros3-bin RCLONE_BIN=/usr/local/bin/rclone go run ./harness/rclone
```

## Result

**19 passed, 0 failed, 2 documented known limitations.**

```
PASS: CreateBucket via `rclone mkdir`
PASS: ListBuckets via `rclone lsd`
PASS: ListBuckets shows the created bucket
KNOWN-LIMITATION: rclone default upload (UNSIGNED-PAYLOAD) against ZeroS3
KNOWN-LIMITATION: rclone forced literal-payload upload (use_unsigned_payload=false) against ZeroS3
PASS: ListObjectsV2 via `rclone lsjson`
PASS: lsjson shows the seeded object with the correct size
PASS: GetObject via `rclone cat`
PASS: downloaded bytes match the uploaded bytes exactly
PASS: `rclone hashsum MD5` runs against the object's ETag
PASS: rclone-reported MD5 matches the object's real MD5
PASS: GetObject via `rclone cat` after overwrite
PASS: rclone sees the overwritten content, not the old revision
PASS: GetObject via `rclone cat` after a full zeros3 process restart
PASS: rclone reads back the same bytes after restart (journal replay, not a page-cache hit)
PASS: DeleteObject via `rclone deletefile`
PASS: ListObjectsV2 via `rclone lsjson` after delete
PASS: bucket is empty after `rclone deletefile`
PASS: DeleteBucket via `rclone rmdir`
PASS: ListBuckets via `rclone lsd` after DeleteBucket
PASS: deleted bucket no longer appears
```

## Workflow exercised

Bucket create (`rclone mkdir`) → bucket list (`rclone lsd`) → object list
(`rclone lsjson`) → download + byte equality (`rclone cat`) → hash equality
(`rclone hashsum MD5`, read against the object's real S3 ETag) → overwrite
+ re-verify → full process restart + re-verify (persistence) → delete
object (`rclone deletefile`) → confirm empty listing → delete bucket
(`rclone rmdir`) → confirm bucket gone.

## Content-MD5 (T1, B1) also exercised here

The object bytes this harness verifies through `rclone` were uploaded with
an explicit `Content-MD5` header (via the pinned AWS SDK's
`PutObjectInput.ContentMD5` field — see "Why the AWS SDK appears here"
below), independently exercising ZeroS3's newly added Content-MD5
validation (`validateContentMD5Header`, `zeros3.go` section 9) against a
real external client, in addition to the in-repo unit tests in
`zeros3_test.go`.

## Why the AWS SDK appears here (and the verified upload limitation)

`rclone`'s ordinary upload commands (`copy`, `copyto`, `sync`) cannot
complete a `PutObject` against ZeroS3, for a reason this harness verifies
directly rather than assumes (`checkRcloneUploadLimitation` in
`harness/rclone/main.go`):

1. **rclone's default configuration** sends `X-Amz-Content-Sha256:
   UNSIGNED-PAYLOAD`. ZeroS3 rejects this with `AccessDenied` — this is
   ZeroS3's own documented, frozen M1 SigV4 decision ("Authorization-header
   AWS4-HMAC-SHA256... literal `X-Amz-Content-Sha256` payload hashes only
   (no `UNSIGNED-PAYLOAD`, no presigned URLs, no `aws-chunked`/trailer
   mode)" — `STATUS.md`, `S3_COMPAT.md`). `UNSIGNED-PAYLOAD` is grouped in
   that same sentence with presigned URLs and `aws-chunked`, both
   explicitly out of scope for this correction pass, so this pass
   deliberately did not add support for it either.
2. **Forcing rclone to sign a literal payload hash instead**
   (`--s3-use-unsigned-payload=false` — an ordinary, documented rclone
   S3-backend option, not a patch) does not work around this: rclone
   wraps every transfer body in a non-seekable progress-accounting reader,
   so its own AWS-SDK-v2-based signer then fails *locally*, before any
   request reaches ZeroS3, with `failed to compute payload hash: failed to
   seek body to start, request stream is not seekable`. This reproduced
   identically across `--disable-http2`, `--multi-thread-streams=0`,
   `--use-server-modtime`, and `--s3-disable-checksum` combinations — a
   structural property of rclone's generic transfer path, not a ZeroS3
   defect and not fixable through ZeroS3 configuration.

Net result: making rclone's own upload command work end-to-end against
ZeroS3 would require ZeroS3 to accept `UNSIGNED-PAYLOAD` — a SigV4
protocol-surface change grouped by ZeroS3's own pre-existing documentation
with presigned URLs and `aws-chunked`. Per this task's explicit scope
boundary ("do not implement... `aws-chunked`, or another later-tier
feature solely because an optional rclone mode wants it"), this was not
implemented in this pass. It is recorded here as a verified, honest,
root-caused limitation, not silently worked around: the harness seeds and
overwrites object bytes with the already-pinned AWS SDK for Go v2 (the
same dependency every other harness in this repository already uses, not
a new one) so the rest of the ordinary S3 workflow — listing, download,
hash equality, delete, bucket lifecycle, and restart persistence — could
still be verified end-to-end through the real `rclone` binary.

## Scope discipline

- No `aws-chunked`, presign, or multipart support was added to satisfy
  this harness.
- No ZeroS3-specific application logic was added to `rclone`'s
  configuration — every setting used (`endpoint`, `access_key_id`,
  `secret_access_key`, `region`, `force_path_style`, `list_version`) is an
  ordinary rclone S3-backend option any generic (non-AWS) S3-compatible
  target commonly needs.
- `rclone` itself is external test infrastructure only: not vendored,
  not imported by `zeros3`, not committed to this repository (only the
  Go harness source that drives the external `rclone`/`zeros3` binaries
  is committed).
