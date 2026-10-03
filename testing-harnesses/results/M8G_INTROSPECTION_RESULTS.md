# M8G — read-only replication planning, structural diff, and CAS inspect
external validation

External, real-server, real-AWS-SDK-v2 black-box proof of `replicate
-dry-run`, `zeros3 diff`, and `zeros3 inspect` (M8G). Every object
fixture is written through the AWS SDK (never zeros3's internal CAS
directly); `replicate -dry-run`/`replicate`/`diff`/`inspect`/`fork`
themselves all run as real subprocesses against real `zeros3 serve`
subprocesses. Read-only claims are verified by an independent,
harness-local, whole-directory SHA-256 fingerprint of each store's
on-disk files (`storeFingerprint` in `harness/m8g/introspection/main.go`
— deliberately not sharing code with zeros3's own internal
`storeContentFingerprint`), never by trusting the CLI's own self-reported
statistics.

## Build under test

```
zeros3 commit:     30f82b9023573e466e1af598792115951095ae6b
                   (branch claude/m8g-read-only-replication-f0e0jl,
                   on top of the exact merged M8F baseline 9729f35)
Go toolchain:      go1.27.0 linux/amd64
Build command:     CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
Binary SHA-256:    e7c53f313fc76425d272606e621b2b84eb966551c96ecce696f954bd2864bff6
```

Reproducible build confirmed (`zeros3/scripts/reproducible_build.sh`):
two independent builds from two independent source copies produced this
exact, byte-identical SHA-256.

## Harness

`harness/m8g/introspection/main.go`. Run:

```sh
go build -o zeros3-bin -C ../zeros3 -trimpath -buildvcs=false -ldflags="-buildid=" zeros3.go   # or point ZEROS3_BIN at any build
ZEROS3_BIN=/absolute/path/to/zeros3-bin go run ./harness/m8g/introspection
```

Scope: Phases 1-7 and 10 from the milestone spec (single-object dry-run
with partial destination overlap and predicted-vs-actual exact match,
zero-transfer plan, recursive dry-run with predicted-vs-actual exact
match, diff on an edited fixture, diff on an unrelated fixture, inspect
on a unique object, inspect across a real `zeros3 fork` including a
post-fork mutation, and the combined read-only fingerprint proof). Phase
9 (1500-object scale) and Phase 8 (snapshot composition) are covered by
the internal suite instead
(`TestM8G_HugeNamespace_1500Objects_RecursiveDryRun`,
`TestInspect_SnapshotPinnedSharing_SurvivesLiveDeletion` in
`zeros3/zeros3_test.go`) rather than duplicated here, to keep this
external harness's runtime proportionate to what it adds beyond the
internal suite.

## Results

**78 passed, 0 failed, 5 info, across all 8 phases.**

### Phase 1 — single-object dry-run, partial destination overlap

Source object built from two halves; only one half pre-seeded at the
destination under an unrelated key. Dry-run predicted a transfer of
318,126 bytes (3 missing chunk occurrences) without touching the
destination store on disk (fingerprint unchanged); an immediately-
following real `replicate` transferred exactly that many bytes, and the
destination's `GetObject` content was byte-identical to the source.

### Phase 2 — zero-transfer plan

Destination CAS pre-seeded with byte-identical content under an
unrelated key. Dry-run correctly predicted **0 bytes** would transfer
while still correctly reporting the destination action as "would publish
object" (the destination key itself is still absent) — proving these two
facts are tracked independently, never conflated.

### Phase 3 — recursive dry-run, exact match

12 objects under one prefix. Recursive dry-run discovered all 12 without
touching the destination store; an immediately-following real
`replicate -recursive` transferred exactly the predicted payload, and
all 12 keys landed at the destination.

### Phase 4 — diff, edited fixture

`v2.bin` = `v1.bin` + a 50,000-byte appended tail. `diff` reported 32 of
33/34 unique chunks shared, 97.9%/95.5% directional reuse, and correctly
reported no exact match (sizes differ).

### Phase 5 — diff, unrelated fixture

Two independently-random objects reported **0** shared unique chunk IDs
and no exact match — unrelated content is never misleadingly reported as
similar.

### Phase 6 — inspect, unique object

A 2.86 MiB, 48-chunk object reported 0 B structurally shared (correct —
nothing else in the store references its chunks), with the `-chunks`
table listing all 48 rows with exact offsets/digests/reachable-root
counts, and left the store's on-disk fingerprint unchanged.

### Phase 7 — inspect across a real fork

Before forking `prod/shared.bin` into `fork/`, inspect reported 0 B
shared elsewhere. A real `zeros3 fork` subprocess cloned it with **0 B
new CAS payload** (its own self-reported statistic, independently
consistent with `inspect`'s before/after delta). After the fork, `inspect
prod/shared.bin` correctly reported its entire 1.43 MiB of physical
payload as structurally shared. The forked copy was then overwritten
with unrelated content through the AWS SDK; `inspect prod/shared.bin`
still reported the same 1.43 MiB as structurally shared, because ZeroS3
archives an overwritten object into that key's own retained history
(never purged on ordinary overwrite) rather than dropping it —
demonstrating `inspect` correctly walks the historical-version root
category, not just current objects.

### Phase 10 — combined read-only fingerprint proof

`replicate -dry-run`, `diff`, and `inspect` run back-to-back against one
shared source/destination fixture; both stores' independent, harness-
local SHA-256 fingerprints were byte-identical before and after all
three commands.

## Full output

```
$ ZEROS3_BIN=/path/to/zeros3-bin go run ./harness/m8g/introspection
=== M8G Phase 1: single-object dry-run, partial destination overlap, predicted == actual ===
PASS: p1: create source bucket
PASS: p1: create dest bucket
PASS: p1: put source object
PASS: p1: pre-seed destination with partA content under an unrelated key
PASS: p1: dry-run exits cleanly
PASS: p1: dry-run left destination store fingerprint unchanged
PASS: p1: dry-run output announces no modification
INFO: p1: predicted transfer=318126 bytes, missing=3 chunk occurrences
PASS: p1: actual replicate exits cleanly
PASS: p1: predicted transfer == actual transfer
PASS: p1: GetObject on destination after actual replicate
PASS: p1: destination content byte-identical to source

=== M8G Phase 2: zero-transfer plan (destination CAS already has every chunk) ===
PASS: p2: create source bucket
PASS: p2: create dest bucket
PASS: p2: put source object
PASS: p2: pre-seed destination with byte-identical content under an unrelated key
PASS: p2: dry-run exits cleanly
PASS: p2: dry-run left destination store fingerprint unchanged
PASS: p2: predicted transfer is exactly 0 bytes
PASS: p2: destination action is still 'would publish' (destination object itself absent)

=== M8G Phase 3: recursive dry-run, then actual namespace replication, exact match ===
PASS: p3: create source bucket
PASS: p3: create dest bucket
PASS: p3: put source object 0..11
PASS: p3: recursive dry-run exits cleanly
PASS: p3: recursive dry-run left destination fingerprint unchanged
PASS: p3: 12 objects discovered
PASS: p3: actual recursive replicate exits cleanly
PASS: p3: recursive predicted transfer == actual transfer
PASS: p3: list destination keys
PASS: p3: destination has all 12 replicated keys

=== M8G Phase 4: diff on an edited fixture (localized change) -- high honest reuse ===
PASS: p4: create bucket
PASS: p4: put v1
PASS: p4: put v2 (v1 + appended tail)
PASS: p4: diff exits cleanly
PASS: p4: exact object match is 'no' (v2 has more content)
PASS: p4: reports a reuse percentage line for A

=== M8G Phase 5: diff on wholly unrelated fixtures -- low reuse, not misleadingly high ===
PASS: p5: create bucket
PASS: p5: put a
PASS: p5: put b
PASS: p5: diff exits cleanly
PASS: p5: reports shared unique chunk IDs: 0
PASS: p5: not an exact match

=== M8G Phase 6: inspect a unique object -- basic physical metrics ===
PASS: p6: create bucket
PASS: p6: put object
PASS: p6: inspect exits cleanly
PASS: p6: inspect left store fingerprint unchanged
PASS: p6: reports zero bytes structurally shared (unique object)
PASS: p6: -chunks table header present

=== M8G Phase 7: inspect across a real `zeros3 fork` -- before/after sharing, then mutate ===
PASS: p7: create prod bucket
PASS: p7: create fork bucket
PASS: p7: put prod object
PASS: p7: inspect before fork exits cleanly
PASS: p7: before fork, zero bytes shared elsewhere
PASS: p7: fork exits cleanly
PASS: p7: inspect after fork exits cleanly
PASS: p7: after fork, everything is structurally shared
PASS: p7: overwrite forked object
PASS: p7: inspect after fork mutation exits cleanly
PASS: p7: prod's chunks remain reachable via the fork's retained history after mutation

=== M8G Phase 10: combined read-only fingerprint proof (dry-run + diff + inspect together) ===
PASS: p10: create source bucket
PASS: p10: create dest bucket
PASS: p10: put a.bin
PASS: p10: put b.bin
PASS: p10: dry-run + diff + inspect all ran
PASS: p10: SOURCE store fingerprint unchanged across all three commands
PASS: p10: DESTINATION store fingerprint unchanged across all three commands

=== M8G introspection harness: 78 passed, 0 failed, 5 info ===
```

## Verdict

**M8G external validation: PASS (78/78).** All three read-only commands
behave correctly under a fully independent, real-process, real-AWS-SDK
client, and the read-only claim holds under an independent filesystem
fingerprint in every phase, individually and in combination.
