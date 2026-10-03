# M8A — remote-to-remote delta replication external validation

External, real-two-server, real-AWS-SDK-v2 black-box proof of
`zeros3 replicate` (M8A). Every fixture is written through the AWS SDK
(never zeros3's internal CAS directly); `zeros3 replicate` itself runs
as a real subprocess, driving two independent, real `zeros3 serve`
subprocesses on separate stores and ports.

## Build under test

```
zeros3 commit:     1fb79a1e1549933634f52eaeedc6a5b66a696b1e (branch claude/zeros3-m8a-baseline-inh9hk)
Go toolchain:      go1.27.0 linux/amd64
Build command:     CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o zeros3 zeros3.go
Binary SHA-256:    efc0cb0956b39fc05fd11eb42422298f6d0aa776d5a70e41c94c87aad180e3fc
```

## Harness

`harness/m8a/remote_delta/main.go`. See its own header comment for the
full black-box rationale.

### Reproduction

```sh
cd /path/to/zeros3
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-buildid=" -o /tmp/zeros3-m8a-bin zeros3.go

cd /path/to/zeros3/testing-harnesses
ZEROS3_BIN=/tmp/zeros3-m8a-bin go run ./harness/m8a/remote_delta
```

## Result

**34 passed, 0 failed, 4 informational.**

## Phase-by-phase

### Phase 1 — setup via the real AWS SDK

Store B (destination) seeded with a 24,000,000-byte deterministic
object (`already-present`) via `PutObject`; Store A (source) then gets
the actual replication target (`target-object`): the same content plus
an 8,192-byte insertion at the midpoint, via `PutObject` with a custom
`harness` metadata key. Neither store's internal CAS was ever populated
directly.

### Phase 2 — real `zeros3 replicate` CLI

```
Logical scanned:     22.90 MiB
Chunks:              362
Chunks reused:       361
Uploaded payload:    60.64 KiB (1 unique chunks)
Transfer avoided:    22.84 MiB
Reuse:               99.7%
```

A strong, honest reuse figure (not a manufactured all-zero case, per the
M8A task's own instruction): only the one chunk touched by the 8KiB
insertion crossed the wire between the two independent servers.

### Phase 3 — independent destination verification (AWS SDK)

`GetObject` returns byte-identical content; `HeadObject` reports the
correct `Content-Length` and preserves the `harness` user-metadata key —
all via a completely independent AWS SDK client against the destination
server, never zeros3's own HTTP client.

### Phase 4 — restart

Destination server killed and restarted from the same store directory;
`GetObject` still returns exact bytes. `zeros3 verify -deep` on the
destination store afterward: `journal_ok=true`, 2 manifests / 363 chunks
checked, 0 missing/corrupt/invalid, 0 unreachable — a replicated object
is completely indistinguishable from an ordinary one to verify.

### Phase 5 — resume across a real process interruption

A 20,000,000-byte source object; `zeros3 replicate` started as a real
subprocess and killed after 150ms (well before a transfer this size
could complete over real HTTP). `HeadObject` on the destination
afterward correctly returns not-found — the interrupted attempt never
committed a partial/visible object (commit is the one atomic step, never
reached). A second, uninterrupted `replicate` run then completes
correctly:

```
Logical scanned:     19.07 MiB
Chunks:              302
Chunks reused:       30
Uploaded payload:    17.11 MiB (272 unique chunks)
Transfer avoided:    1.96 MiB
Reuse:               10.3%
```

The resumed run's own reuse figure (10.3%) reflects whatever subset of
chunks the killed first attempt happened to land in the destination's
CAS before being killed — a real, timing-dependent number, not a fixed
one; the invariant that matters and is what's actually asserted is
"correct content after resume," which holds. `GetObject` after the
resumed run returns exact bytes, confirmed via AWS SDK.

### Phase 6 — conflict (racing AWS SDK write)

A `zeros3 replicate` subprocess started against a 20MB source object;
300ms later, a real AWS SDK `PutObject` races a write onto the same
destination key. This run's outcome: **the AWS SDK write won** — the
destination holds exactly the interloper's content (never a mix), and
`replicate` correctly exited non-zero (the destination changed since
`replicate` observed it via HEAD, a safe-mode conflict per M6B/M8A8).
Both orderings are legitimate outcomes of a real race; the harness
checks the one invariant that must hold regardless of which side wins.

### Phase 7 — source mutation mid-flight

A `zeros3 replicate` subprocess started against a 20MB source object;
80ms later, a real AWS SDK `PutObject` overwrites that *same source*
object with different content while replication is still in flight.
`replicate` still completed successfully (M8A operates on the
immutable revision it captured at object-descriptor time, never a live
re-scan of the source's current pointer — see `STATUS.md`'s "M8A"
section). This run's outcome: **the destination captured the original
(pre-overwrite) revision** — never a mix of the two. The source's own
*current* object correctly reflects the overwrite afterward, confirming
this was a captured-revision replication, not a lost write.

## Caveats

- Phases 5, 6, and 7 are best-effort/timing-based, exactly like the
  existing `harness/m6/sync` harness's own interruption/race phases:
  a real external subprocess offers no deterministic injection point.
  The invariants actually asserted (no partial commit after a kill,
  correct content after resume, exactly-one-writer's-content after a
  race, never a mixed revision after a source overwrite) hold
  regardless of which way each race landed on this run — see
  `zeros3_test.go`'s `TestReplicate_ResumeAcrossRealProcessInterruption`,
  `TestReplicate_DestinationConflict_ConcurrentWriteDuringReplicationRejectedSafely`,
  and `TestReplicate_SourceOverwrittenDuringReplicationDoesNotProduceMixedRevision`
  for the deterministic, hook-based versions of the same three
  guarantees.
- This harness exercises exactly the scenarios M8A's task description
  calls out (Phases 1-7); it is a black-box supplement to, not a
  replacement for, the 33 internal `TestReplicate_*` tests in
  `zeros3_test.go`, which cover the full required test matrix
  (capabilities, descriptor edge cases, chunk retrieval, negotiation
  accounting, commit/conflict, resume, stats) in more exhaustive and
  deterministic detail.
