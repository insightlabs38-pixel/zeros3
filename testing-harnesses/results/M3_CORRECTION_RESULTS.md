# M3 correction pass — external validation results

**Tested against zeros3 commit:** `04ac4a0600eaad9fe300baf3a24c7b72d1ebc679`
(branch `claude/zeros3-m3-m4-corrections-62o7ir`)

This is a follow-up to `M3_RESULTS.md` after the M3 correction pass (see
`STATUS.md`'s "M3 correction pass" section in the `zeros3` repository for
the full A1–A5 writeup). It does not replace or rewrite `M3_RESULTS.md`,
which remains the historical record of the original M3 landing.

All harnesses below are external, black-box: they only ever talk to a
real `zeros3-bin` server over the S3 wire protocol (via the pinned AWS
SDK for Go v2) or read its `stats -json` CLI output; none of them import
or link against `zeros3`'s Go package.

## CopyObject interop (`harness/m3/copy`) — 46/46 passed

Extended for the correction pass with two new kinds of case, in addition
to every case `M3_RESULTS.md` already covered (same/cross-bucket copy,
overwrite, missing source/destination, both metadata directives):

- **A1 — new destination identity, proven externally.** The source
  object's `Last-Modified` is captured via `HeadObject` before any copy.
  After a >1-second sleep (past HTTP's one-second `Last-Modified`
  resolution) and a same-bucket `COPY`-directive copy, the destination's
  `Last-Modified` is asserted **strictly after** the source's — and,
  separately, the source's own `Last-Modified` is asserted **unchanged**
  by the copy. The same check is repeated for the `REPLACE` directive.
- **A2 — encoded/tricky source keys, sent the way the real SDK actually
  sends them.** Four source keys (`with space.bin`, `100%done.bin`,
  `a+b plus.bin`, `dir/sub/tricky.bin`) are uploaded, then copied with
  `CopySource` built as the exact **raw, unencoded** `bucket + "/" +
  key` string — matching the pinned AWS SDK Go v2's actual wire
  behavior (confirmed by direct request inspection during the
  correction pass: `CopySource` receives zero percent-encoding of its
  own). Each copy is confirmed to round-trip the exact source bytes.

```
===== SUMMARY: 46 passed, 0 failed =====
```

### Confirms both fixes are real regressions, not just theoretical

The same (post-correction) harness was also run against a `zeros3-bin`
built from the pre-correction-pass commit (`cd6367b`, `main` before this
pass), to confirm these two new assertion kinds are genuine regression
tests rather than assertions that would have passed either way:

```
FAIL: M3 correction: destination Last-Modified is a new, later timestamp
      than the source's (not reused): dst=... src=... (equal)
FAIL: CopyObject (tricky source key "100%done.bin"): operation error S3:
      CopyObject, https response error StatusCode: 400, ...
      api error InvalidArgument: invalid x-amz-copy-source: invalid key
      encoding
FAIL: GetObject (copy of tricky source key "100%done.bin"): ... NoSuchKey
===== SUMMARY: 42 passed, 3 failed =====
```

Both failure modes are exactly what A1/A2 describe: the destination
reused the source's timestamp under the old code, and a source key with
an unescaped `%` was rejected outright.

## Preserved results (re-run, unaffected by this pass)

- **M2** (`harness/m2`) — **41/41 passed**, unchanged from `M2_RESULTS.md`.
- **M3 Range GET** (`harness/m3/range`) — **27/27 passed**, unchanged
  from `M3_RESULTS.md`.
- **M3 dedup evidence** (`harness/m3/dedup`) — **7/7 passed**, unchanged
  from `M3_RESULTS.md` (identical-object reuse 100% of duplicate bytes;
  edited-object reuse measured at 97.5% for this run's corpus/offsets —
  a measurement, not a fixed target, and expected to vary slightly run
  to run with the harness's random corpus).

## How to reproduce

```sh
cd zeros3 && go build -o /tmp/zeros3-bin .
cd testing-harnesses
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m2
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m3/copy
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m3/range
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/m3/dedup
```
