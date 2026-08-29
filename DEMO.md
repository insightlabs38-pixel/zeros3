# Demo rehearsal

A deterministic, product-first walkthrough for the submission demo.
Nothing here requires network access beyond the local `zeros3` server;
every command is copy-pasteable against a real build. Aim to finish the
rehearsed content comfortably under the hackathon time limit — the beats
below are ordered so any suffix can be cut if time runs short (cut from
the bottom up).

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

## 1. Pitch (≈15s)

> "ZeroS3 — S3 on the outside, content-addressed storage underneath.
> A local S3-compatible object store, one Go file, zero third-party
> runtime dependencies."

## 2. Build & run (≈20s)

Already built in step 0 — just show it running:

```sh
./zeros3 serve -store demo-store -addr 127.0.0.1:9000
```

## 3. Real S3 client round trip (≈60s)

Using the AWS SDK Go v2 harness (or the AWS CLI configured with
`--endpoint-url http://127.0.0.1:9000` and path-style addressing):

```sh
aws --endpoint-url http://127.0.0.1:9000 s3 mb s3://demo-bucket
aws --endpoint-url http://127.0.0.1:9000 s3 cp fixture-v1.bin s3://demo-bucket/object.bin
aws --endpoint-url http://127.0.0.1:9000 s3api head-object --bucket demo-bucket --key object.bin
aws --endpoint-url http://127.0.0.1:9000 s3 cp s3://demo-bucket/object.bin - | sha256sum
```

Show the downloaded SHA-256 matches the fixture's own SHA-256 — exact
byte reconstruction, not just "a 200 response."

## 4. Restart durability (≈25s)

```sh
kill %1   # or Ctrl-C the server
./zeros3 serve -store demo-store -addr 127.0.0.1:9000 &
aws --endpoint-url http://127.0.0.1:9000 s3 cp s3://demo-bucket/object.bin - | sha256sum
```

Same hash after a full process restart — the visibility journal replay,
not a lucky page-cache hit.

## 5. Dedup payoff (≈45s)

Upload a similar-but-edited revision, then show `stats`:

```sh
aws --endpoint-url http://127.0.0.1:9000 s3 cp fixture-v2-edited.bin s3://demo-bucket/object-v2.bin
./zeros3 stats -store demo-store
```

Point at `scope_unique_chunk_bytes` vs. `logical_current_bytes`, and
`dedup_reduction` — the CDC/CAS architecture reusing bytes automatically,
not a special "diff" feature.

## 6. CopyObject: zero new payload bytes (≈30s)

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
without moving a single payload byte. (`zeros3 stats -store demo-store`,
without `-json`, prints the same figure human-readably if a live read is
preferred over the diff.)

## 7. Range GET (≈20s)

```sh
aws --endpoint-url http://127.0.0.1:9000 s3api get-object \
    --bucket demo-bucket --key object.bin --range bytes=0-99 /tmp/first100.bin
```

## 8. Verify (≈15s)

```sh
./zeros3 verify -store demo-store -deep
```

Clean exit, zero issues — structural + per-chunk + whole-object-digest
checks all pass.

## 9. Zero-dependency proof (≈25s)

```sh
cat go.mod                 # no `require` block
go list -deps . | grep -c .
cat deps-proof.txt | tail -5
```

## 10. Reproducible build (≈25s)

```sh
./scripts/reproducible_build.sh
```

Two independent builds, matching SHA-256 hashes, printed directly.

## 11. Single implementation file (≈20s)

```sh
wc -l zeros3.go zeros3_test.go
ls *.go
```

One implementation file; the organizer-approved test file is the only
other Go source. Optionally flash a couple of `STDLIB.md`'s
substitution-table rows.

## 12. Close

> "Known limitations are documented plainly in STATUS.md — no versioning,
> no multipart, single-writer-process. What's here is tested, measured,
> and reproducible."

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
```

Re-running the same commands always reproduces byte-identical fixtures (and
therefore the same expected SHA-256 values) — the actual meaning of
"deterministic" here.

Do not commit fixtures, logs, or the `demo-store` directory used for
rehearsal.
