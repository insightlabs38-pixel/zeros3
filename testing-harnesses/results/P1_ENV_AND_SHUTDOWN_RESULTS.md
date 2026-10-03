# P1 — Environment-variable credentials + HTTP hardening/graceful
shutdown: external harness results

External, real-process, real-client evidence for ZeroS3's P1 operational
hardening pass (P1-A environment credentials, P1-B HTTP hardening +
graceful shutdown), layered on top of `zeros3_test.go`'s own exhaustive
internal "P1-A"/"P1-B" test sections (precedence edge cases, deadline
expiry, second-signal, non-corruption — all in-process against real
subprocesses/real OS signals already). This harness's unique job is
proving the same claims hold against a genuinely independent AWS SDK for
Go v2 client and real `SIGINT`/`SIGTERM` delivery, not zeros3's own
hand-rolled SigV4 test signer.

## Harness

`harness/p1/env_and_shutdown` — not part of ZeroS3, never imported by it.
Run:

```sh
cd /path/to/zeros3 && go build -o /tmp/zeros3-bin .
cd /path/to/zeros3/testing-harnesses
ZEROS3_BIN=/tmp/zeros3-bin go run ./harness/p1/env_and_shutdown
```

## Result

**23 passed, 0 failed.**

### P1-A Phase 1 — server credentials from the environment only

`zeros3 serve` started with no `-access-key`/`-secret-key`/`-region`
flags at all, only `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/
`AWS_REGION` in its process environment. A real AWS SDK Go v2 client
configured with the matching static credentials: `CreateBucket`,
`PutObject`, `GetObject` all succeed and the round-tripped content is
byte-identical. A second client configured with wrong credentials
against the same server is rejected. 5/5 passed.

### P1-A Phase 2 — explicit CLI flags are authoritative over environment

`zeros3 serve -access-key REAL-FLAG-ACCESS-KEY -secret-key
REAL-FLAG-SECRET-KEY`, started with `AWS_ACCESS_KEY_ID`/
`AWS_SECRET_ACCESS_KEY` set in its environment to different, wrong
values. A client using the *explicit flag* credentials succeeds; a
client using the *environment* credentials is rejected — proving P1-A2's
precedence (explicit flag always wins) holds against a real server
process and a real client, not just the internal flag-resolution unit
tests. 2/2 passed.

### P1-B Phase 1 — idle SIGINT/SIGTERM graceful shutdown

For each of `SIGTERM` and `SIGINT` against an idle real `zeros3 serve`
subprocess: the process exits with code 0, well under the 30s grace
period, and a restart against the exact same store directory succeeds
cleanly. 6/6 passed (3 checks × 2 signals).

### P1-B Phase 3 — active PUT completes within the grace period

A real AWS SDK `PutObject` whose request body is deliberately paced at
the actual TCP wire-transfer layer (a custom `http.RoundTripper` wraps
the request *after* the SDK has already computed its SigV4 payload hash
and finished any local-only body pre-reads, so the pacing applies to the
genuine network transfer, not to the SDK's off-the-wire hashing pass —
see the harness's `throttlingTransport` doc comment for why an earlier,
naive `io.ReadSeeker`-based pacing attempt did not actually keep a
connection active and had to be redesigned). `SIGTERM` is sent
mid-transfer; the PUT still completes successfully, the server exits
cleanly (code 0) once it finishes, and `zeros3 verify -deep` on the
resulting store reports no issues. 5/5 passed.

### P1-B Phase 5 — grace-period expiry does not hang the process

A PUT paced to take far longer (~400s) than the 30s grace period.
`SIGTERM` mid-transfer: the process does not hang — it exits only after
genuinely waiting out the full grace period (not early, not never),
with a nonzero exit code (P1-B's documented signal that the grace period
expired with active work still in flight). A restart against the same
store succeeds, and `zeros3 verify -deep` reports no corruption — the
interrupted, never-acknowledged write left no partial/mixed state,
exactly as ZeroS3's existing journal/CAS crash-recovery model (unchanged
by P1) already guarantees for an abrupt kill. 3/3 passed (plus the
restart + verify above, folded into this phase's own 3 checks and the
final verify check, 4 total).

## What this does and doesn't prove

Proves, with a real independent client and real signals: environment
credential fallback authenticates a genuine AWS SDK client; explicit
flags really do override the environment against a real server process;
`SIGINT`/`SIGTERM` really do trigger a clean, bounded shutdown; an
active real upload really does survive a signal arriving mid-transfer;
a genuinely-too-slow request really does force a bounded, non-hanging
exit without corrupting the store.

Does not re-derive: exact precedence-table edge cases (empty env,
partial credential pairs, missing access/secret) — see
`zeros3_test.go`'s `TestEnvOverride_*`/`TestApplyCredentialEnvFallback_*`
tests; the second-signal-forces-immediate-exit behavior — see
`TestServe_SIGTERM_SecondSignalForcesImmediateExit_RealProcess`; new
connections being refused once shutdown starts — see
`TestServe_SIGTERM_ActivePUT_CompletesWithinGrace_RealProcess`; or TLS
— see `TestServe_TLS_*` and this harness's sibling note in
`STATUS.md`'s "P1-C" section. Those are all exhaustively covered
in-process already; this harness exists specifically for what only an
independent third-party client and real OS-level signals can add.
