# M3 external validation results

**Tested against zeros3 commit:** `c05aab672e2c78cbc41e9e6c52dda09036c66f52`
(branch `claude/zeros3-m3-implementation-liru6u`, built from `main`)

All three harnesses below are external, black-box: they only ever talk
to a real `zeros3-bin` server over the S3 wire protocol (via the pinned
AWS SDK for Go v2) or read its `stats -json` CLI output; none of them
import or link against `zeros3`'s Go package.

## CopyObject interop (`harness/m3/copy`) — 24/24 passed

Same-bucket copy (default COPY metadata directive) with a `CopyObjectResult`
ETag, byte-exact `GetObject` round trip, and preserved source metadata;
cross-bucket copy; overwrite of an existing destination key; a missing
source key rejected; a missing destination bucket rejected; the REPLACE
metadata directive publishing new Content-Type/metadata while still
copying the exact source bytes.

```
===== SUMMARY: 24 passed, 0 failed =====
```

## Range GET interop (`harness/m3/range`) — 27/27 passed

Single-byte, mid-object, last-byte, open-ended (`bytes=N-`), suffix
(`bytes=-N`), and a region spanning multiple CDC chunks, each checked for
an exact `Content-Range`, exact body length, and byte-exact content
against a 500KiB deterministic object; an out-of-bounds range rejected
(416).

```
===== SUMMARY: 27 passed, 0 failed =====
```

## Dedup evidence demo (`harness/m3/dedup`) — 7/7 passed

Uploads a 4MiB object twice via ordinary `PutObject` calls, stops the
server, and reads dedup evidence back through `zeros3 stats -json` (a
genuinely external, black-box measurement — this driver never touches
ZeroS3's Go package or on-disk store format directly):

```
--- Identical-object reuse (2 uploads of the same 4194304-byte object, real S3 PutObject calls) ---
logical_current_bytes=8388608 logical_chunk_reference_bytes=8388608 logical_chunk_reference_count=114
scope_unique_chunk_bytes=4194304 scope_unique_chunk_count=57 chunk_store_file_bytes=4194304
dedup_avoided_bytes=4194304 dedup_reduction=50.0%
```

Then restarts the server against the same store and uploads a
near-duplicate (a 4001-byte insertion 50000 bytes into the same object):

```
--- Edited-object reuse (4194304-byte original vs 4198305-byte edited, 4001-byte insertion near the start) ---
edited object: logical_current_bytes=4198305 scope_unique_chunk_bytes=4198305 scope_exclusive_chunk_bytes=104247 scope_shared_chunk_bytes=4094058
reused (shared with copy1/copy2) = 4094058 bytes (97.5% of the edited object)
```

**97.5% of the edited object's bytes were reused from the earlier
upload** — measured externally through the CLI's own JSON stats output,
not asserted internally.

```
===== SUMMARY: 7 passed, 0 failed =====
```

## Preserved M2 result

The M2 harness (`harness/m2`) was re-run against this same zeros3 commit
and remains **41/41 passed** — see `M2_RESULTS.md`.

## How to reproduce

```sh
cd zeros3 && go build -o /tmp/zeros3-bin .
cd testing-harnesses
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m2
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m3/copy
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m3/range
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m3/dedup
```
