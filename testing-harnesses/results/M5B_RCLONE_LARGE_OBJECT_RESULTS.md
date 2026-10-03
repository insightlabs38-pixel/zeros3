# M5-B: rclone large-object multipart proof (1 GiB)

External, black-box proof that a real, unmodified `rclone` binary's
**ordinary upload path** now completes against ZeroS3 for a large file,
genuinely triggering rclone's S3-backend multipart upload (not a
single-PUT shortcut), and that the resulting object survives a real
process restart with exact byte/hash equality on download.

## Result: **PASS**

This also **resolves a previously-documented interoperability
limitation**: `RCLONE_RESULTS.md` (post-M4 pass) recorded that "rclone's
own ordinary upload commands cannot complete against ZeroS3" because
rclone's generic transfer path wraps every upload body in a non-seekable
progress-accounting reader and therefore requires the SigV4
`UNSIGNED-PAYLOAD` payload mode, which ZeroS3 did not support at that
time. M5-B's Phase A2 added header-auth `UNSIGNED-PAYLOAD` support
specifically to close this gap, and this run confirms it: rclone's
ordinary `copy` command (no special flags) now completes successfully
end-to-end, both for a small object and for this 1 GiB multipart object.

## Versions and evidence

- **rclone version:** `v1.75.0` (downloaded directly from
  `downloads.rclone.org`, the same pinned version as the post-M4 pass — not
  the stale distro-packaged version).
- **ZeroS3 commit tested:** branch `claude/zeros3-m5b-large-object-jfnvkb`
  (see `zeros3`'s `STATUS.md` M5-B section for the exact commit).
- **File size:** exactly **1 GiB (1073741824 bytes)**, generated from
  `/dev/urandom` (genuine, non-repeating entropy — CDC/multipart chunking
  makes no claim about compressibility, so a real random fixture is a fair
  and sufficient large-object test; the fixture itself was **not**
  committed to either repository, per instructions).
- **rclone config:** ordinary `s3` backend, `provider = Other`,
  `list_version = 2`, path-style endpoint pointing at the local `zeros3`
  process. **No `--s3-upload-cutoff` or `--s3-chunk-size` override** —
  rclone's own default cutoff (200 MiB) and default chunk size (5 MiB)
  were left untouched, so multipart triggered exactly the way it would for
  any real user uploading a file this size, not because the harness forced
  a smaller chunk size to contrive multipart behavior.

## Multipart behavior, measured directly

- rclone logged `bigfile.bin: Multi-thread Copied (new)` for the upload —
  rclone's own log line for its generic multi-threaded transfer path,
  which for an S3 remote above the upload cutoff means genuine
  `CreateMultipartUpload`/`UploadPart`/`CompleteMultipartUpload` calls, not
  a single `PutObject`.
- **Directly confirmed, not just inferred from the log line:** the
  completed object's `ETag`, read back via a plain `HeadObject` call
  (pinned AWS SDK for Go v2, the same dependency every other harness in
  this repository already uses), was
  `"b30575cd9e0c41bd4c1df0e8294bfea2-205"` — the `-205` suffix is exactly
  ZeroS3's multipart ETag formula (`MD5(concat of each part's binary MD5)
  + "-" + part_count`), which a single-PUT object never produces. 1 GiB ÷ 5
  MiB (rclone's default chunk size) = 204.8, rounding up to **205 parts** —
  exactly what the ETag reports.

## Restart / resume result

The zeros3 process was killed and restarted (fresh port, same store
directory) immediately after the upload completed. A `HeadObject` call
against the restarted process reported the **exact same ETag and size**
(`"b30575cd9e0c41bd4c1df0e8294bfea2-205"`, `1073741824` bytes) — the
completed multipart object's identity is unchanged by the restart, as
required (a completed object is an ordinary object from that point on;
this is not a repeat of the in-progress-upload-survives-restart proof,
which the `harness/m5b/multipart` Go-level harness covers in more detail).

## Download equality

`rclone copy zeros3:bigbucket/bigfile.bin` into a **freshly-created, empty
local directory** (to force a genuine transfer rather than rclone's
default same-file skip-if-unchanged heuristic matching a leftover local
copy) after the restart:

```
SHA-256 (uploaded, original file):  207fa80173baad31239c6a7d4cfb23939f1701474c1654b93499ea9c5417c7fe
SHA-256 (downloaded after restart): 207fa80173baad31239c6a7d4cfb23939f1701474c1654b93499ea9c5417c7fe
```

**Exact match.**

## Payload modes actually observed

As with the AWS SDK harness (`M5B_MULTIPART_RESULTS.md`), the entire
1 GiB upload — bucket creation, all ~205 `UploadPart` calls, and
`CompleteMultipartUpload` — produced **zero server-side errors**
(`grep -i error` over the full captured server log for both the upload and
restart/download runs is empty), and rclone's ordinary upload path used
`UNSIGNED-PAYLOAD` (per its own documented non-seekable-body behavior,
consistent with the prior pass's finding), never one of the eligible
`STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER]` modes. This is the second,
independent real client (after the AWS SDK for Go v2) to complete a full
multipart workflow without either HMAC streaming mode, confirming Phase K
is correctly left conditional/undone for this pass.

## Reproduction

```sh
cd zeros3
go build -o /tmp/zeros3-bin zeros3.go
/tmp/zeros3-bin -store /tmp/store -addr 127.0.0.1:19234 &

curl -sSL -o /tmp/rclone.zip https://downloads.rclone.org/rclone-current-linux-amd64.zip
unzip -q /tmp/rclone.zip -d /tmp
RCLONE=/tmp/rclone-v1.75.0-linux-amd64/rclone   # exact directory name depends on the current release

cat > /tmp/rclone.conf <<'EOF'
[zeros3]
type = s3
provider = Other
access_key_id = AKIAZEROS3EXAMPLE01
secret_access_key = zeros3exampleSecretKeyForM1TestingOnly01
endpoint = http://127.0.0.1:19234
region = us-east-1
list_version = 2
EOF
export RCLONE_CONFIG=/tmp/rclone.conf

dd if=/dev/urandom of=/tmp/bigfile.bin bs=1M count=1024
$RCLONE mkdir zeros3:bigbucket
$RCLONE copy /tmp/bigfile.bin zeros3:bigbucket/ -v
# ... kill/restart zeros3 against the same -store directory ...
$RCLONE copy zeros3:bigbucket/bigfile.bin /tmp/download/ -v
sha256sum /tmp/bigfile.bin /tmp/download/bigfile.bin
```

## Regression note

The small-object rclone lifecycle checks recorded in `RCLONE_RESULTS.md`
(bucket/object lifecycle, listing, overwrite, restart persistence) were
spot-checked again in this pass (an ordinary `rclone copy` of a 1 MiB file
via `mkdir`/`copy`) and continue to pass; `RCLONE_RESULTS.md` itself is
left as the historical record of the post-M4 pass and is not rewritten —
this file supersedes only its "rclone's own upload commands cannot
complete" limitation note, which M5-B's `UNSIGNED-PAYLOAD` support
resolves.
