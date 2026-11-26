# FB-01R: transaction-aware journal recovery

Code on `research/next`: `ded8f92` (rollback reporting), `fb35ad4` (checkpoint
fallback), `015a2f1` (ack watermark, degraded views), `fcb37c0` (X14-R, SIGKILL),
`c7368f4` (Design B prototype, tests only), `609374b` (measurements, fuzz, docs),
`1575906` (evidence). `main` unchanged at `2ac9cab`. The journal format is
unchanged; one sidecar file is added. Go 1.27.0, standard library, one file.

## Verdict

Design A, an ack watermark in a sidecar file, is implemented and on by default
in lazy mode. It removes the refusal for interrupted writes within the boot
that wrote them, at no extra fsync, and detects the frame-boundary cut that v1
could not (S6). After a host restart, lazy mode still refuses an interrupted
write; sync mode (`ZEROS3_JOURNAL_WATERMARK=sync`) recovers it automatically
for one extra fsync per group commit, 12-27% fewer engine puts per second
here. No trial of Design A, in any mode, lost an acknowledged mutation.
Design B (a v2 journal with group commit records) is a working prototype, not
a production format; in its zero-extra-fsync piggyback mode it loses the last
acknowledged group to media damage or truncation inside a one-group seal lag.

## Why FB-01 refuses, and what v1 cannot tell apart

A v1 journal is a run of CRC-checked frames with no commit boundary. When the
tail is damaged, the frames alone cannot say whether the damage is:

- a write that never reached its fsync (never acknowledged: safe to drop);
- an acknowledged frame damaged by the medium or cut afterwards (must refuse);
- a whole acknowledged group cut at a frame boundary, which leaves no damage
  at all (S6: undetectable in v1).

FB-01 therefore refused every damaged tail. Its 50-56% figure is the share of
X14 trials with an injected torn final group that refused, conditional on that
injected fault: it is not a probability that a real crash refuses. A real
process kill rarely tears a write: in 30 random SIGKILLs during 8 concurrent
writers, none left a torn tail (a kill interrupts a write to a regular file
only between pages, so small groups land whole). Torn tails come from host crashes and power loss, and from
a short write (ENOSPC, EIO) followed by a crash before the rollback.

## Designs compared

| | v1 (FB-01) | A lazy (default) | A sync | B piggyback | B sync |
|---|---|---|---|---|---|
| Boundary | none | sidecar slot: acknowledged end, frame, seq, CRC, boot | same, fsynced | header seal + commit frame per group | same |
| fsync per group | 1 | 1 | 2 | 1 | 2 |
| pwrite per group | 1 | 2 (512 B slot) | 2 | 2 | 2 |
| Bytes per group | 0 | 0 (4.5 KiB file) | 0 | 48 (+8 KiB header) | 48 |
| fsyncs at open + close | 0 | 4 | 4 | 2 | 2 |
| Same-boot interrupted write | refuse | recover | recover | recover | recover |
| After a host restart | refuse | refuse | recover | recover | recover |
| Acknowledged damage, S6 cut | refuse; S6 lost | refuse | refuse | one-group window | refuse |
| Older builds | | use the store, ignore the file | same | cannot read it | cannot read it |

## Recovery results

X14-R on the engine, 300 trials per cell, owner open; LOSS is 0 in every cell
(`results/x14r_engine.jsonl`):

| Scenario | same boot | restart, lazy | restart, sync |
|---|---|---|---|
| S0 process kill: final group cut at any byte | 299 recover, 1 ok | 299 refuse | 299 recover |
| S1 power loss: torn final group | 84 recover, 84 refuse | 168 refuse | 168 recover |
| S2 torn, garbage sectors | 84 recover, 79 refuse | 163 refuse | 163 recover |
| S8 torn, stale sectors | 91 recover, 79 refuse | 170 refuse | 170 recover |
| S3, S3x, S4, S5, S6, S7 acknowledged damage | 300 refuse | 300 refuse | 300 refuse |

The rest of S1, S2 and S8 opened clean. Their same-boot column is a model only:
a process crash leaves a prefix of the write, so the non-prefix shapes refuse
there as anomalies. S6 now refuses 300/300 (FB-01: 62% silent loss), 173 by the
watermark and 127 by checkpoint contradiction. Shared opens serve a proven
tail's prefix as consistent; after a lazy restart they serve a degraded prefix.

Design B, 300 trials, on the prototype (`results/design_b.jsonl`): every crash
scenario recovers in both modes. Piggyback mode loses the last acknowledged
group in S3 128/300, S5 106, S6 30 and S7 21 trials, all inside its one-group
window; sync mode refuses all acknowledged damage with no loss.

SIGKILL (`results/kill.jsonl`): 42 kills of a child running 8 writers, at
random and before the fsync, after the fsync but before the acknowledgement,
after the apply and after an injected torn write. 25,630 acknowledged
operations, 0 lost, 0 refusals, 3 automatic recoveries (1,334 bytes); a
structural verify passes afterwards. Without the watermark the torn write
refuses and salvage at the reported offset loses nothing.

Fuzz, 60 s each, no failures: slot decoder 956,779 execs, tail judgement
87,068, v2 recovery 514,239 (`results/fuzz_*.txt`). Compatibility: 36/36 checks
against `2ac9cab` and `4b66f16` both ways, including a store extended by an old
build behind a stale watermark (`results/compat.jsonl`). `go test -count=1 .`
passes at `1575906` (1,725 s).

## Costs

One run, rounds interleaved, ext4 on a 4-CPU VM (`results/measure.jsonl`).
Journal level, one 300-byte frame per group, p50 acknowledgement and groups/s:
v1 173 us / 4,885; A lazy 170 us / 5,344; A sync 299 us / 2,810; B piggyback
183 us / 4,558; B sync 278 us / 2,990. Allocations per group: v1 and A 1,
the B prototype 5.

Engine, 1 KiB PutObject, puts/s at 1, 8 and 32 writers:

| Mode | 1 | 8 | 32 | p50 at 1 writer |
|---|---|---|---|---|
| off (v1) | 1,976 | 5,565 | 13,234 | 430 us |
| lazy | 2,026 | 5,961 | 15,273 | 431 us |
| sync | 1,451 (-27%) | 4,161 (-25%) | 11,700 (-12%) | 581 us |

Lazy is within run-to-run spread of off (an earlier run had it 1-9% slower).
An open over 20,001 frames (6.3 MB) took 42-60 ms in every mode (37-41 ms in
the earlier run); an automatic recovery took the same; a refusal 23 ms, then salvage and open 100 ms plus
the operator. The v2 prototype's scan is 2.6 times v1's (7.4 against 2.9 ms).

## On-disk changes and migration

- New file `journal/visibility.ack`: two 512-byte slots at offsets 0 and 4096
  ("ZSWM", version 1, generation, flags, boot id, store tag, offset, frame
  offset, seq, last CRC, pid, time, CRC32C). The newer valid slot counts.
- No change to the journal, checkpoints or store format. Older builds ignore
  the file; this build creates it on its first owner open. With it, an open
  refuses what FB-01 accepted only where acknowledged history is missing
  (S6). `off` removes it. Salvage moves it into quarantine.
- An open that changes nothing in the same boot leaves the file's bytes as
  they were, so repeated opens do not churn the store.
- Design B would be a new journal format: old builds cannot read it, and a
  migration would rewrite the journal or start a v2 file after the v1 one.

## What remains undetectable

- Damage to frames an older build appended, before this build next opens the
  store: the watermark lags, so such damage would be taken for an interrupted
  write. In the crash model that is right; under media damage it is not.
- A slot that lies: an fsync that did not persist (sync mode), or a watermark
  file restored from a different moment than the journal.
- Consistent forgery of journal and slot (CRC32C is not a MAC).
- Lazy mode after a host restart cannot prove anything about the tail; it
  refuses, so this costs availability, not data.
- Design B piggyback: one group of media damage or truncation after a restart.

Damage past a current watermark is discarded whatever its cause, but nothing
acknowledged lies there, and the bytes and the proof are kept in
`journal/quarantine/tail-*/`.

## Recommended interim behaviour

Keep Design A in lazy mode as the default: same-boot recovery and S6
detection for no extra fsync. Offer sync mode for deployments that must
restart unattended after power loss, and document its cost. Keep v1 frames;
take Design B only with a future format change that needs in-journal group
boundaries for other reasons, and then in sync mode or with a seal fsync
bounded by policy.

## Other corrections in FB-01R

- `appendFrames` reports the rollback outcome with the write or fsync error
  ("rolled back to offset N", or both causes).
- Checkpoint fallback: a V4 generation whose trailer binds it to this store
  and journal but whose body fails replays the whole journal instead of
  refusing; unreadable trailers, foreign files, contradictions and legacy
  layouts still refuse.
- `OpenStoreShared` refuses a degraded view (`errStoreDegraded`);
  `OpenStoreSharedPrefix` serves it with `Degraded()` set. `verify`, `repair`,
  `export` and `namespace-checkpoint` refuse; `stats`, `versions`, `tier
  status` and `doctor` serve the prefix with a warning.

## Readiness

Ready on `research/next` for review: implemented, fuzzed, compatible both ways,
full suite green. Before `main`: the owner's choice of default mode, a run on
the production filesystem, and the FB-01 gates still open (no CI by decision;
fault seams at fsync sites outside the journal).

Reproduce: `REPRODUCE.md`. Decisions: `../ADR_DELTA.md`. Ledger: `../LEDGER.md`.
