# FB-16 · Incremental namespace reconciliation

Opt-in recursive replication compares authenticated projections of the
source and destination before invoking the existing native batch or
per-object transfer path. The projection contains relative keys and the
same content-and-metadata identity used for replication equivalence.
A scope-pair boundary gives both peers the same portable layout.
Credentials remain independent. The server retains up to two scope
projections and refreshes changed keys from checked v1 or J2 journal
suffixes. A missing or different boundary anchor rebuilds the affected
scope. Reopen creates fresh generation tokens.

An equal projection takes one logical round, with the two authenticated
root requests issued in parallel. Four fixed radix levels identify
changed leaf partitions. Compact hints locate candidate rows. The
destination applies proposed edits only to a dry projection and
compares its full SHA-256 root with the authenticated source. A mismatch
retries with full entry digests. Each response is authenticated against
the exact request. A fresh root challenge prevents replay.

| Generated namespace | Differences | Application bytes | Bytes per difference |
| --- | ---:| ---:| ---:|
| 1,000,000 | 0 | 314 | — |
| 1,000,000 | 1 | 1,857 | 1,857 |
| 1,000,000 | 1,000 | 411,913 | 412 |
| 1,000,000 | 100,000 | 19,853,792 | 199 |

The application metric counts request and response bodies, including
response authentication. HTTP headers and transport framing are separate.
Divergence took four tree rounds and three row and proof rounds after
opening. The fixed tree had no per-session namespace re-encode. A cold
projection reads the scope once. Generated rows qualify metadata behavior.

Thirty-six randomized histories matched the independent key oracle,
including first and last keys, removals, and metadata changes. A
forced short-tag collision took the exact fallback. Signed tests
covered compact J2 updates, a scoped directory marker, a challenged
root replay, a modified response tag, and generation expiry.

## Transfer and mirror

Additive behavior remains the default. Explicit mirror requires
`replicate -recursive -incremental -mirror` and a destination serving
with `-projection-mirror`. Dry-run reports proposed removals. The client
rechecks the source generation before each removal batch. The
destination only removes the exact local root observed during planning,
using its manifest digest and journal sequence inside the existing
ordered commit queue. A same-content replacement invalidates that
guard. A missing source bucket refuses the mirror plan. Ordinary
history retention applies after removal.

Three paired signed loopback rounds changed seven of 144 objects with
J2 and binary records enabled. The median request and response bodies
totaled 345,964 bytes for enumeration and 189,911 for the projection.
Payload uploads were 86,016 bytes and destination allocation was
1,384,448 bytes in both modes. Median process CPU was 24.52 and
16.03 ms, respectively. Median signed GET p99 was 4.23 and 3.06 ms
across 96 mixed-workload range probes per trial. The projection lane
used 21 requests versus seven. WAN round-trip cost remains open.

## Sampled global lookup

**M-G2 remains research only.** On the C19 identical-receiver control,
native replication used 20,737 two-leg bytes. A global lookup could add
an authorization and network round trip without reducing payload there.
On a missing 4.18 MB object, cache value depends on hit probability,
and on the saved source-leg time after deducting lookup, authentication,
and downstream delivery. A repeat consumer with matching authorization
scope is required to price it. No such consumer was identified.

[Reproduction](REPRODUCE.md) · [Evidence](results/)
