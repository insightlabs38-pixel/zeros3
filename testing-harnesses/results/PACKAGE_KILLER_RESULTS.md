# Package Killer: ZeroS3 vs. s3rver — GO

Following `PACKAGE_KILLER.md`'s exact GO/NO-GO gate: the same frozen AWS
SDK for Go v2 test logic (`harness/package-killer/main.go`,
`runSharedMatrix`), called once per target, with **only** endpoint/
credential/addressing connection settings changed between calls. The claim
target is s3rver's principal standalone local-S3-server development/
testing use case only — not its Node.js embedding/programmatic API.

## Decision: **GO**

Every required criterion in `PACKAGE_KILLER.md` passed identically on both
targets, using the identical Go client code.

## Versions and evidence (re-checked at submission time, not reused from planning)

- **ZeroS3 commit tested:** `1042dec8c15c054cd0c1353474131c8f24b31aec`
  (`claude/zeros3-post-m4-package-killer-9afzph`)
- **zeros3-testing commit/state:** this repository's HEAD at the commit
  that adds this file
- **Go toolchain:** `go1.27.0 linux/amd64`
- **AWS SDK for Go v2 module versions** (same pin as every other harness
  in this repository, from `go.mod`):
  - `github.com/aws/aws-sdk-go-v2 v1.45.1`
  - `github.com/aws/aws-sdk-go-v2/config v1.33.1`
  - `github.com/aws/aws-sdk-go-v2/credentials v1.20.1`
  - `github.com/aws/aws-sdk-go-v2/service/s3 v1.109.1`
  - `github.com/aws/smithy-go v1.28.1`
- **s3rver version tested:** `3.7.1` — re-checked directly against the
  live npm registry at submission time (`npm view s3rver version`), not
  reused from the planning bundle's snapshot. This is still npm's
  `latest` dist-tag; the package has not published a new version since
  **2022-06-26** (`npm view s3rver time.modified`). ~1.31M downloads in
  the 30 days before this check (`api.npmjs.org/downloads/point/last-month/s3rver`).
  Its GitHub repository (`jamhall/s3rver`) is confirmed **archived**
  (checked directly against the GitHub API at submission time), 601
  stars, 156 forks, 58 open issues — a real, still-widely-used, but
  unmaintained package, matching this project's earlier "archived
  upstream but still used" note.
- **s3rver runtime dependency evidence** (`npm view s3rver dependencies`,
  re-checked at submission time — exact match to the planning-time
  snapshot, confirming it hasn't changed since the package hasn't been
  republished): 11 direct runtime dependencies —
  `@koa/router`, `busboy`, `commander`, `fast-xml-parser`, `fs-extra`,
  `he`, `koa`, `koa-logger`, `lodash`, `statuses`, `winston` (plus their
  own transitive trees). None of this ever enters the ZeroS3 Go module;
  s3rver was installed only into an ephemeral scratch directory outside
  both repositories to run this comparison.
- **Node.js:** `v22.22.2` / npm `10.9.7` (used only to run s3rver as
  comparison infrastructure; never a ZeroS3 build or runtime requirement).

## Exact shared workload

One Go function (`runSharedMatrix`) executed unmodified against each
target: `CreateBucket` → `ListBuckets` → `PutObject` (Content-Type + two
user-metadata keys) → `PutObject` (second key, for listing) →
`HeadObject` (Content-Length/Content-Type/metadata) → `GetObject` (exact
byte equality + Content-Type + metadata) → `ListObjectsV2` (whole-bucket
+ prefix-filtered) → Range GET and CopyObject (recorded as
differentiators, not required parity, per `PACKAGE_KILLER.md`) →
`DeleteObject` + confirm `HeadObject` now errors → cleanup →
`DeleteBucket` + confirm `ListBuckets` no longer shows it.

## Per-operation result

| Operation | ZeroS3 | s3rver | Required for GO? |
|---|---|---|---|
| CreateBucket | PASS | PASS | yes |
| ListBuckets | PASS | PASS | yes |
| PutObject (Content-Type + metadata) | PASS | PASS | yes |
| PutObject (second key) | PASS | PASS | yes (listing setup) |
| HeadObject (Content-Length/Content-Type/metadata) | PASS | PASS | yes |
| GetObject (exact bytes + Content-Type + metadata) | PASS | PASS | yes |
| ListObjectsV2 (whole bucket) | PASS | PASS | yes |
| ListObjectsV2 (prefix filter) | PASS | PASS | yes |
| DeleteObject | PASS | PASS | yes |
| HeadObject confirms object gone after delete | PASS | PASS | yes |
| DeleteBucket | PASS | PASS | yes |
| ListBuckets confirms bucket gone after delete | PASS | PASS | yes |
| [differentiator] Range GET | PASS | PASS | no — both happen to support it |
| [differentiator] CopyObject | PASS | PASS | no — both happen to support it |

**14/14 passed on ZeroS3, 14/14 passed on s3rver.** Authentication (every
call above is an ordinary signed SigV4 request from the AWS SDK) is
implicitly proven by every row: no row disables or bypasses signing on
either target.

## What was allowed to differ (and nothing else did)

Per `target` struct values only:

| Setting | ZeroS3 | s3rver |
|---|---|---|
| Endpoint | `http://127.0.0.1:<ephemeral port>` | `http://127.0.0.1:<ephemeral port>` |
| Access key / secret | `AKIAZEROS3EXAMPLE01` / `zeros3exampleSecretKeyForM1TestingOnly01` (ZeroS3's own default keypair) | `S3RVER` / `S3RVER` (s3rver's own documented default keypair, `lib/models/account.js`'s `DUMMY_ACCOUNT`) |
| Region | `us-east-1` | `us-east-1` |
| Addressing | path-style (`UsePathStyle: true`) | path-style (`UsePathStyle: true`, matched on the server side by s3rver's own `--no-vhost-buckets` startup flag, since s3rver defaults to vhost-style bucket addressing) |

No test/application logic, request sequence, or assertion differs between
the two calls to `runSharedMatrix`. `--no-vhost-buckets` is an ordinary
s3rver startup flag selecting the addressing mode the shared client
config already requests — a target-required connection setting, not a
workaround.

## GO criteria checklist (`PACKAGE_KILLER.md`)

- [x] Same user job — both serve as a local S3-compatible endpoint for dev/test.
- [x] Same client — the identical frozen AWS SDK Go v2 harness function.
- [x] Switch cost — only endpoint/credentials/addressing changed (table above).
- [x] Bucket baseline — CreateBucket/ListBuckets/DeleteBucket pass on both.
- [x] Object baseline — PutObject/GetObject/HeadObject/DeleteObject pass on both.
- [x] Listing — ListObjectsV2 (whole-bucket and prefix-filtered) passes on both.
- [x] Metadata — Content-Type and user metadata survive on both.
- [x] Authentication — every call is an ordinary signed SDK request.
- [x] Evidence — this document.
- [x] Claim discipline — see wording below; no Node embedding API claim.

No automatic-NO-GO condition applies: nothing here required a
per-target application-logic change, no SDK harness modification beyond
`target` connection settings, no weakening of ZeroS3's own correctness,
and no ACL/CORS/website/events/embedding-API work was needed.

## Claim (used verbatim, no stronger)

> ZeroS3 reimplements the principal standalone use case of s3rver: a local
> S3-compatible server for development and testing. The same ordinary S3
> client workflow can be pointed at ZeroS3 by changing endpoint/connection
> settings. ZeroS3 ships as one Go implementation file with zero
> third-party runtime dependencies; it does not replace s3rver's Node.js
> embedding API.

## Limitations of this replacement claim

- Node.js embedding/programmatic API parity is explicitly **not** claimed
  (s3rver is usable as an in-process Node library; ZeroS3 is a standalone
  Go binary).
- ACL, CORS, static website hosting, and S3 event notifications — all
  features s3rver supports to some extent — are not claimed and were
  never exercised here; `PACKAGE_KILLER.md` explicitly excludes them from
  the replacement claim.
- This comparison used s3rver's own default in-process signature
  verification (no `--allow-mismatched-signatures`); a stricter or looser
  s3rver configuration was not explored, since the goal is ordinary,
  default behavior on both sides.
- Persistence-across-restart, CDC/CAS dedup, and zero-payload CopyObject
  are real ZeroS3 differentiators (see `STATUS.md`/`README.md`), not
  required or claimed as parity items here.

## Reproduction

```sh
# 1. build zeros3
cd /path/to/zeros3 && git checkout 1042dec8c15c054cd0c1353474131c8f24b31aec
go build -o /tmp/zeros3-bin .

# 2. install s3rver (pin the version you record) into a scratch directory
#    OUTSIDE both repositories -- never inside zeros3 or zeros3-testing
mkdir -p /tmp/pk-s3rver && cd /tmp/pk-s3rver
npm init -y && npm install s3rver@3.7.1

# 3. run the shared-matrix harness
cd /path/to/zeros3/testing-harnesses
ZEROS3_BIN=/tmp/zeros3-bin \
  S3RVER_BIN=/tmp/pk-s3rver/node_modules/.bin/s3rver \
  go run ./harness/package-killer
```
