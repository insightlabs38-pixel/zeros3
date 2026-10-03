# P2 — ListMultipartUploads prefix/delimiter/CommonPrefixes: external
harness results

External, real-process, real-client evidence for ZeroS3's P2 compatibility
polish pass: `ListMultipartUploads` now supports `prefix`, `delimiter`/
`CommonPrefixes`, and correct pagination/marker interaction across group
boundaries. Layered on top of `zeros3_test.go`'s own exhaustive internal
"P2" test section (algorithmic edge cases, hostile marker-boundary cases,
a 1200-upload scale traversal — all in-process against the real HTTP
handler already). This harness's unique job is proving the same claims
hold against a genuinely independent AWS SDK for Go v2 client, never any
internal ZeroS3 API call.

## Build under test

```
zeros3 commit:     f1f31f0 (branch claude/zeros3-listmultipartuploads-p2-rfwz3k,
                   on top of the exact merged P1 baseline
                   4f7a3b8ed24fdb4bec13182fce391a7e02938fce)
Go toolchain:      go1.27.0 linux/amd64
Reproducible build SHA-256:
                   b085635edac14fba9fad884e77783ce6f90f421b6fe4477ce7968e469e89d1f5
```

## Harness

`harness/p2/list_multipart_uploads` — not part of ZeroS3, never imported
by it, never linked into the shipped binary. Run:

```sh
cd /path/to/zeros3 && go build -o /tmp/zeros3-bin .
cd /path/to/zeros3/testing-harnesses
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/p2/list_multipart_uploads
```

**A note on this sandbox's environment:** this session's outbound-proxy
tooling sets `AWS_ACCESS_KEY_ID=proxy-injected`/
`AWS_SECRET_ACCESS_KEY=proxy-injected` process-wide — the exact same
hazard P1-A's own STATUS.md section documents. The harness's server
subprocess is started with explicit `-access-key`/`-secret-key`/
`-region` flags matching its own client credentials for exactly this
reason, so it is unaffected regardless of the calling shell's ambient
environment.

## Result

**1571 passed, 0 failed.**

### Phase 1 — prefix filtering

Multipart uploads created across `alpha/a`, `alpha/b`, `alpha/sub/c`,
`beta/d`. `ListMultipartUploads` with `prefix=alpha/` returns exactly the
three `alpha/` keys; `beta/d` never appears. A prefix matching zero
uploads returns an empty, non-truncated list.

### Phase 2 — delimiter + CommonPrefixes

Reproduces AWS's own documented `ListMultipartUploads` delimiter example
fixture exactly: `a/file1`, `a/file2`, `a/sub/file3`, `a/sub/file4`,
`a/sub2/file5`, `b/file6`. `prefix=a/&delimiter=/` returns direct uploads
`a/file1`/`a/file2` and `CommonPrefixes` `a/sub/`/`a/sub2/`; the three
grouped uploads never appear as direct `Upload` entries. The combined
direct-upload/CommonPrefix key set matches the expected single ordered
logical namespace.

### Phase 3 — pagination through a mixed Upload/CommonPrefix stream

Logical stream `a/file1` (direct), `a/sub/` (a two-member group), `a/z`
(direct) — 3 logical slots — paginated at `max-uploads=1`, following
`NextKeyMarker`/`NextUploadIdMarker` across pages. Exactly 3 pages
visited; the CommonPrefix `a/sub/` is returned exactly once, consuming
exactly one page, regardless of its two underlying uploads; `a/z` is
never skipped or duplicated.

### Phase 4 — same-key multipart sessions

Four separate `CreateMultipartUpload` calls for the identical key `dup`.
Paginated one at a time (`max-uploads=1`), all four upload IDs are
returned exactly once, in ascending order, across pages.

### Phase 5 — 1500+ uploads, full paginated traversal

1500 multipart uploads (`scale/00000`..`scale/01499`), listed with the
default page size (1000, forcing at least two pages). Zero duplicate
keys across the traversal; exact final count of 1500, no omissions.

### Phase 6 — weird keys

Keys containing a space, `%`, `#`, `?`, a Unicode (Japanese) path
segment, and an XML-sensitive mix (`<`, `>`, `&`, `'`, `"`) all
round-trip exactly through `ListMultipartUploads`. A `prefix`/`delimiter`
query scoped to the Unicode segment returns exactly its one direct
upload.

### Phase 7 — restart, listing identical

A bucket with a mix of direct and grouped uploads is listed
(`delimiter=/`), the server is killed, and a fresh `zeros3 serve`
process is started against the exact same store directory. The
direct-upload set and the `CommonPrefixes` set are both identical
before and after restart — proving no persistent-format change and no
listing-state loss across a restart.

## What this does and doesn't prove

Proves, with a real independent client: `prefix`/`delimiter`/
`CommonPrefixes` filtering and grouping match AWS's own documented
examples exactly; pagination across a mixed direct-Upload/CommonPrefix
logical stream produces no duplicate or omitted result, and a
CommonPrefix consumes exactly one page slot; same-key multiple-upload-ID
markers resolve correctly; a 1500-upload namespace traverses completely
and exactly; Unicode/space/percent/hash/question-mark/XML-sensitive keys
round-trip correctly; and a real process restart leaves the listing
byte-for-byte equivalent (same-shape, same content).

Does not re-derive: the algorithmic proof that a CommonPrefix can never
be duplicated or reintroduced across a marker resume — see
`zeros3_test.go`'s doc comment on `Store.ListMultipartUploads` and
`TestListMultipartUploads_Pagination_MarkerNearCommonPrefixBoundary`;
the hostile marker-exactly-equals-CommonPrefix-text and
just-before/-after-lexically cases — same test; the read-only/
no-storage-mutation fingerprint proof across 9 hostile queries — see
`TestListMultipartUploads_ReadOnly_NoStorageMutation`; or the exact
1200-upload scale traversal at three different page sizes including
determinism across repeats — see
`TestListMultipartUploads_Scale_1200Traversal`. Those are all
exhaustively covered in-process already; this harness exists
specifically for what only an independent third-party client can add.

## Full historical regression

See `zeros3`'s own `STATUS.md` ("P2" section) for the complete
per-harness regression table (every harness in this repository re-run
against this exact commit, all matching their prior baselines with zero
real failures; rclone remains the one disclosed, unaffected-by-inspection
gap this session's sandbox cannot re-run, unchanged from P1) and the
reproducible-build/dependency proof.
