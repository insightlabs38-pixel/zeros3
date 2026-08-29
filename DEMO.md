# Demo rehearsal

A deterministic, product-first walkthrough for the submission demo.
Nothing here requires network access beyond the local `zeros3` server;
every command is copy-pasteable against a real build. Target ~4:00-4:30
of actual content, comfortably under a 5-minute limit — the beats below
are ordered so any suffix can be cut if time runs short (cut from the
bottom up; section 6, delta sync, is the first thing to drop since
sections 1-5 alone already prove the core S3-interop/CDC/durability
story).

All commands assume a shell in the repository root, with the pinned AWS
CLI/SDK-based commands run from a *separate* checkout of
[`zeros3-testing`](https://github.com/insightlabs38-pixel/zeros3-testing)
(never from inside this repository — that separation is the point).

## 0. Setup (once, before recording)

```sh
go version                              # confirm go1.27.x
go build -o zeros3 zeros3.go
rm -rf demo-store && mkdir demo-store
./zeros3 serve -store demo-store -addr 127.0.0.1:9000 &
```

Credentials for any S3 client pointed at it:

```
AKIAZEROS3EXAMPLE01 / zeros3exampleSecretKeyForM1TestingOnly01, region us-east-1
```

## 1. Identity (≈20s)

> "ZeroS3 — S3 on the outside, content-addressed storage underneath.
> A local S3-compatible object store, one Go file, zero third-party
> runtime dependencies."

```sh
ls *.go                                 # zeros3.go + the test file, nothing else
./zeros3 serve -store demo-store -addr 127.0.0.1:9000   # already running from step 0
```

## 2. Real S3 client round trip (≈55s)

Using the AWS CLI configured with `--endpoint-url http://127.0.0.1:9000`
and path-style addressing (or the AWS SDK Go v2 harness in
`zeros3-testing`) — the point is this is an *ordinary* client, not a
custom toy:

```sh
aws --endpoint-url http://127.0.0.1:9000 s3 mb s3://demo-bucket
aws --endpoint-url http://127.0.0.1:9000 s3 cp fixture-v1.bin s3://demo-bucket/object.bin --no-progress
aws --endpoint-url http://127.0.0.1:9000 s3api head-object --bucket demo-bucket --key object.bin
aws --endpoint-url http://127.0.0.1:9000 s3 cp s3://demo-bucket/object.bin - | sha256sum
aws --endpoint-url http://127.0.0.1:9000 s3api get-object \
    --bucket demo-bucket --key object.bin --range bytes=0-99 /tmp/first100.bin
```

Show the downloaded SHA-256 matches the fixture's own SHA-256 — exact
byte reconstruction, not just "a 200 response" — and that the range GET
returns exactly 100 bytes.

## 3. CDC/CAS dedup payoff (≈45s)

Upload a similar-but-edited revision, then show `stats` scoped to just
that object (`-bucket`/`-key`) — the whole-store view mixes in the
first, unrelated object and always reads ~50% for any two-object store
regardless of edit size, which understates the point:

```sh
aws --endpoint-url http://127.0.0.1:9000 s3 cp fixture-v2-edited.bin s3://demo-bucket/object-v2.bin --no-progress
./zeros3 stats -store demo-store -bucket demo-bucket -key object-v2.bin
```

Point at the `sharing` line: of this object's ~64 MiB, only ~64 KiB is
`exclusive` (the actual edit plus CDC's normal boundary churn around it)
and the rest is `shared outside scope` — reused, not re-uploaded. The
CDC/CAS architecture reuses bytes automatically; this is not a special
"diff" feature.

## 4. CopyObject: zero new payload bytes (≈25s)

```sh
./zeros3 stats -store demo-store -json | grep -o '"chunk_store_file_bytes":[0-9]*' > /tmp/before.txt
aws --endpoint-url http://127.0.0.1:9000 s3api copy-object \
    --bucket demo-bucket --key object-copy.bin \
    --copy-source demo-bucket/object.bin
./zeros3 stats -store demo-store -json | grep -o '"chunk_store_file_bytes":[0-9]*' > /tmp/after.txt
diff /tmp/before.txt /tmp/after.txt && echo "chunk_store_file_bytes unchanged"
```

No external JSON tool required — `grep -o` pulls the one stable field name
`stats -json` guarantees. Identical `chunk_store_file_bytes` before/after —
the copy published a new manifest (new identity, new `Last-Modified`)
without moving a single new payload byte.

## 5. Durability/integrity (≈35s)

```sh
kill %1   # or Ctrl-C the server
./zeros3 serve -store demo-store -addr 127.0.0.1:9000 &
aws --endpoint-url http://127.0.0.1:9000 s3 cp s3://demo-bucket/object.bin - | sha256sum
./zeros3 verify -store demo-store -deep
```

Same hash after a full process restart — the visibility journal replay,
not a lucky page-cache hit — followed by a clean `verify -deep` exit:
structural + per-chunk + whole-object-digest checks all pass, zero issues.

## 6. Delta sync (`zeros3 sync`) (≈45s)

A ZeroS3-specific extension (M6/M6C), not part of the S3 wire protocol:
ingest a file using far less transfer than a full upload when the store
already holds most of the bytes, via the same CDC chunking used above.
Uses its own `sync-v1.bin`/`sync-v2-edited.bin` fixture pair (not
`fixture-v1.bin`/`fixture-v2-edited.bin` from section 3) so the store
doesn't already hold these bytes from an earlier step — otherwise even
the *first* sync would show 100% reuse and the demo wouldn't show
anything:

```sh
./zeros3 sync sync-v1.bin s3://demo-bucket/synced.bin
./zeros3 sync sync-v2-edited.bin s3://demo-bucket/synced.bin
```

The first `sync`'s summary shows 0% reuse (nothing was on the server
yet); the second, after only a small edit, reuses ~99.8% and uploads
only the chunks actually touched by the edit (`Chunks reused`/`Uploaded
payload`/`Transfer avoided`/`Reuse`) — the rest is recognized as already
present from the first sync. Then confirm it produced an ordinary
object, indistinguishable from any other:

```sh
aws --endpoint-url http://127.0.0.1:9000 s3 cp s3://demo-bucket/synced.bin - | sha256sum
```

matches `sha256sum sync-v2-edited.bin` exactly.

matches `sha256sum fixture-v2-edited.bin` exactly.

## 7. Proof (≈35s)

```sh
cat go.mod                 # no `require` block
go list -deps . | grep -c .
./scripts/reproducible_build.sh
```

Zero dependencies (`deps-proof.txt`/`STDLIB.md` have the full detail) and
two independent builds producing byte-identical SHA-256 hashes, printed
directly.

## 8. Close (≈10s)

> "Known limitations are documented plainly in README.md/STATUS.md —
> single-writer-process, no IAM/ACL, no --delete for directory sync.
> What's here is tested, measured, and reproducible."

## Fixture generation (deterministic, not checked in)

`/dev/urandom` is **not** deterministic — two rehearsals would produce two
different fixtures with two different expected hashes, which contradicts
"deterministic" fixture generation. Use a fixed-seed pseudorandom generator
instead, matching the same seeded approach `zeros3_test.go`'s
`genRandomBytes` helper uses internally for its own dedup tests. This needs
only the Go toolchain already required to build ZeroS3 — no extra tool:

```sh
cat > /tmp/genfixture.go <<'EOF'
package main

import (
	"math/rand"
	"os"
	"strconv"
)

// Deterministic: the same seed and size always produce the same bytes.
func main() {
	seed, _ := strconv.ParseInt(os.Args[2], 10, 64)
	size, _ := strconv.Atoi(os.Args[3])
	buf := make([]byte, size)
	rand.New(rand.NewSource(seed)).Read(buf)
	os.WriteFile(os.Args[1], buf, 0o644)
}
EOF

# fixture-v1.bin: 64 MiB of structured non-zero data, seed 1
go run /tmp/genfixture.go fixture-v1.bin 1 67108864

# fixture-v2-edited.bin: v1 with a small controlled insertion 50000 bytes
# in (deterministic offset/content, matching the shape of
# TestDedup_EditedObjectReuseBeatsFixedSizeChunking's edit)
head -c 50000 fixture-v1.bin > fixture-v2-edited.bin
go run /tmp/genfixture.go /tmp/insertion.bin 2 4001
cat /tmp/insertion.bin >> fixture-v2-edited.bin
tail -c +50001 fixture-v1.bin >> fixture-v2-edited.bin

# sync-v1.bin/sync-v2-edited.bin: a SEPARATE pair (different seeds), same
# shape, for section 6 -- section 6 must sync content the store does not
# already hold (from sections 2/3's ordinary PUTs of fixture-v1/v2), or
# even the *first* sync would show 100% reuse and prove nothing.
go run /tmp/genfixture.go sync-v1.bin 3 67108864
head -c 50000 sync-v1.bin > sync-v2-edited.bin
go run /tmp/genfixture.go /tmp/sync-insertion.bin 4 4001
cat /tmp/sync-insertion.bin >> sync-v2-edited.bin
tail -c +50001 sync-v1.bin >> sync-v2-edited.bin
```

Re-running the same commands always reproduces byte-identical fixtures (and
therefore the same expected SHA-256 values) — the actual meaning of
"deterministic" here.

Do not commit fixtures, logs, or the `demo-store` directory used for
rehearsal.
