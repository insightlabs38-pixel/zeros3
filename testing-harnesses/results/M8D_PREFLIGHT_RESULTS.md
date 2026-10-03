# M8D preflight — rerun of the two M8C-session-skipped validations

`rclone` and `package-killer` require external tooling (a real `rclone`
binary and an `npm`-installed `s3rver`) that was not available in the
M8C implementation session, so `results/M8C_NAMESPACE_REPLICATION_RESULTS.md`
carried their counts forward from the M8B freeze instead of re-running
them. Both are now re-run here, from scratch, with both tools genuinely
installed, against the **exact current merged M8C baseline** — not the
M8C feature-branch tip.

## Baseline under test

```
zeros3 commit (exact merged HEAD):  0f9fc3269a9a986a5b3af5aeebbcc4f742a3cc16
  (Merge pull request #14 from claude/zeros3-m8c-prefix-bucket-e30x9r
   into main; the M8C feature-branch tip 7bc94d4c00e11e5f0e4ea9bf920772d8d201a729
   is 0f9fc327's sole first parent's other side -- an ordinary merge commit,
   tree-identical to 7bc94d4 -- so this is genuinely the same code, now
   confirmed to be the exact ref main/HEAD resolves to.)
Go toolchain:       go1.27.0 linux/amd64
Build command:      CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3-bin .
Binary SHA-256:      260e0dac5d391347a4c39f74ce09d58b5e7ce331e7f5b8f0313f7e3219897fce
```

## Internal baseline (zeros3 repo, same commit)

```
gofmt -l .        -> clean (no output)
go vet ./...      -> clean (no output)
go test ./...     -> ok (59.032s)
go test -race ./... -> ok
zeros3.go LOC:      8946
zeros3_test.go LOC: 16803
internal Test* count: 442
```

## 1A -- rclone

```
rclone version tested: v1.75.0 (downloaded fresh from
  https://downloads.rclone.org/v1.75.0/rclone-v1.75.0-linux-amd64.zip,
  go/version go1.26.5, static, linux/amd64 -- same pinned version as
  every prior rclone run in this repository)
Harness: harness/rclone/main.go (unmodified)
```

Command:

```sh
ZEROS3_BIN=/tmp/zeros3-bin RCLONE_BIN=/usr/local/bin/rclone go run ./harness/rclone
```

Result: **20 passed, 0 failed, 1 documented known limitation** --
byte-for-byte identical count to the M8B freeze this M8C carried forward
(`results/M8B_REPAIR_RESULTS.md`, `results/M8C_NAMESPACE_REPLICATION_RESULTS.md`).
The one known limitation is the same previously-documented, root-caused
one: rclone's forced-literal-payload mode (`use_unsigned_payload=false`)
fails inside rclone itself (non-seekable transfer body) before any
request reaches ZeroS3 -- not a ZeroS3 defect, unchanged since the M5-B
pass that made rclone's *default* (`UNSIGNED-PAYLOAD`) upload path work.

### 1 GiB rclone multipart proof (ad hoc, matching `results/M5B_RCLONE_LARGE_OBJECT_RESULTS.md` methodology)

This specific proof has no persisted `harness/` package (it was
originally run ad hoc at M5-B and its result frozen into
`M5B_RCLONE_LARGE_OBJECT_RESULTS.md`); it was re-run here by hand,
following the same procedure, since environment/resource limits
permitted it (29 GiB free disk, network reachable).

- Fixture: exactly 1 GiB (1073741824 bytes) from `/dev/urandom`, not
  committed to either repository.
- Uploaded via `rclone copy` (ordinary path, no `--s3-upload-cutoff` /
  `--s3-chunk-size` override -- rclone's own default 200 MiB cutoff / 5 MiB
  chunk size).
- rclone logged `Multi-thread Copied (new)` (21.9s, ~46 MiB/s).
- `HeadObject` (AWS SDK for Go v2) confirmed a genuine multipart ETag:
  `"337f229a6b448c4e161380dbffc8d86d-205"` -- 205 parts matches
  ceil(1 GiB / 5 MiB) = 205, a signature a single-PUT object never
  produces.
- Downloaded via `rclone copyto` before a real process restart: SHA-256
  `8d54ef70512c0b079f3f43c4edd488a05a6ff12a35d5705dfa8c300e89fd4146`,
  identical to the source fixture's SHA-256.
- Server killed and restarted fresh on the same store directory;
  downloaded again: same SHA-256, byte-identical.

**Result: PASS.** No regression from the M5-B-recorded large-object
behavior.

## 1B -- Package Killer

```
Harness: harness/package-killer/main.go (unmodified, runSharedMatrix)
s3rver version: 3.7.1, installed into /tmp/pk-s3rver (scratch dir,
  outside both repositories)
```

Command:

```sh
ZEROS3_BIN=/tmp/zeros3-bin S3RVER_BIN=/tmp/pk-s3rver/node_modules/.bin/s3rver \
  go run ./harness/package-killer
```

Result:

```
ZeroS3: 14 passed, 0 failed
s3rver: 14 passed, 0 failed
```

**Decision: GO** -- identical to the count carried forward from the M8B
freeze (`results/PACKAGE_KILLER_RESULTS.md`, reaffirmed unchanged through
M8C). Every required criterion (CreateBucket, ListBuckets, PutObject
w/ Content-Type + metadata, HeadObject, GetObject exact bytes,
ListObjectsV2 list + prefix filter, Range GET, CopyObject, DeleteObject,
DeleteBucket) passed identically on both targets using the same client
code, only endpoint/credential/addressing connection settings differing.

## Environmental caveats

- Both tools (rclone binary, npm/s3rver) were absent from the M8C
  implementation session's environment and are freshly installed here;
  no prior run in *this* environment exists to compare against, only the
  recorded M8B-freeze counts this M8C pass carried forward. The counts
  match exactly.
- s3rver's own live npm metadata (version, deprecation/vulnerability
  warnings surfaced by `npm install`) was not re-audited in this pass;
  the previously-recorded `results/PACKAGE_KILLER_RESULTS.md` audit
  (archived upstream, 3.7.1 still latest) is unchanged and not
  re-verified here since it is unrelated to ZeroS3 behavior.

## Verdict

**No regression.** Both skipped M8C-session validations are now green
against the exact current merged M8C baseline
(`0f9fc3269a9a986a5b3af5aeebbcc4f742a3cc16`), matching their last-known
accepted counts exactly. The Phase 1 hard gate is satisfied; M8D
implementation may proceed. An immutable `m8c-gold` checkpoint (git tag
on the `zeros3` repository, pointing at this exact commit) is recorded
alongside this file.
