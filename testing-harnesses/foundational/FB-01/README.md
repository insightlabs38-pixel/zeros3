# FB-01: journal recovery (M-A0, M-A1)

Code: `research/next`, `be82fc0` (harness, reproduction) and `d2727f4` (strict
recovery), plus this evidence commit. `main` unchanged at `2ac9cab`. No format
change. Go 1.27.0, standard library, `zeros3.go` single file.

## Defects reproduced on pinned old code

| Case (40 objects unless noted) | 2337f48 | main 2ac9cab | now |
|---|---|---|---|
| F6-JNL-01 length bit 20 or 16, early frame | LOSS 4/40, owner cuts 12964 -> 1372 B | same | REFUSE |
| length bit 12, final frame | LOSS 39/40, owner cuts | same | REFUSE |
| length bit 0 or bit 23 | REFUSE | REFUSE | REFUSE |
| F4-JNL-01 cut below checkpoint (boundary or mid-frame) | LOSS 14/40 | n/a (no v4) | REFUSE |
| cut mid-frame above checkpoint | LOSS 39/40, owner cuts | n/a | owner REFUSE, shared PREFIX |
| cut at a frame boundary above checkpoint (S6) | LOSS 35/40 | n/a | LOSS 35/40, undetectable in v1 |
| length bit 20 past checkpoint (60 objects) | LOSS 34/60, cuts 19404 -> 11032 B | n/a | REFUSE |

Every REFUSE leaves the journal byte-identical; PREFIX is a read-only open serving
the valid prefix with the tail reported (`results/repro_*.jsonl`, `fb01_d2727f4.jsonl`).

## What changed

- `scanJournalRange` validates with bounded allocation and reports the earliest
  damage: offset, frame seq, cause, any later valid frame with a plausible seq,
  any one-bit length correction that checks out, and a confidence.
- An owner open never cuts the journal and never skips a checkpoint that is
  present but damaged, foreign or contradicted (E2). It refuses with
  `errJournalRefused`, naming offset, frame and cause, and changes nothing.
- A read-only open waits up to 200 ms for a writer's frame, then serves the
  prefix, logs the tail and exposes it as `Store.JournalTail()`.
- An open that skips the checkpoint still checks it against the journal, so
  F4-JNL-01 refuses with `ZEROS3_NAMESPACE_CHECKPOINT=off` too.
- `zeros3 journal inspect [-json]`; `zeros3 journal salvage -cut N
  -i-understand-data-loss` (offline, frame boundary, original and moved
  checkpoints kept in `journal/quarantine/salvage-*/` with hashes).
- A failed group write or fsync rolls back to the last acknowledged frame; if
  that fails too, the journal stays poisoned and the next open refuses.
- Test seams, nil in release: `testJournalFault` (journal-write, journal-sync,
  journal-rollback, journal-open, checkpoint-write) and `testJournalPolicy`
  (P0 = 2337f48 rules, P0R = P0 plus resync guard), for comparison only.

## X14 on the real engine (300 trials per scenario, 6 real stores)

Refuse % / silent-loss % of trials (`results/x14_engine.jsonl`, which also has
lost-mutation share, torn-group acceptance, preservation and open time).

| Scenario | strict owner | strict shared | P0R | P0 |
|---|---|---|---|---|
| S1 crash, torn final group | 56 / 0 | 28 / 0 | 28 / 0 | 28 / 0 |
| S2 torn, garbage sectors | 55 / 0 | 25 / 0 | 25 / 0 | 25 / 0 |
| S8 torn, stale journal sectors | 50 / 0 | 24 / 0 | 24 / 0 | 24 / 0 |
| S3 final group damaged, clean close | 100 / 0 | 100 / 0 | 96 / 3.7 | 86 / 14.3 |
| S3x final group damaged after ack | 100 / 0 | 100 / 0 | 98 / 2.3 | 92 / 8.3 |
| S4 earlier group damaged | 100 / 0 | 100 / 0 | 100 / 0 | 90 / 10.3 |
| S5 crash + previous group damaged | 100 / 0 | 100 / 0 | 98 / 2.0 | 93 / 7.3 |
| S6 cut at a frame boundary | 38 / 62 (S6 residual) | 38 / 62 | 0 / 100 | 0 / 100 |
| S7 cut mid-frame | 100 / 0 | 40 / 0 (60 PREFIX) | 0 / 100 | 0 / 100 |

P0R does not remove loss (S3, S3x, S5, S7). Strict removes it except the S6
residual, at the cost of refusing 50-56% of plain crashes (P0R 24-28%) until an
operator salvages: the tradeoff for the marker and seal design (M-C2). Strict
refusals change nothing; P0 and P0R cut the journal whenever they accept a tail.
Median open 0.2-0.9 ms. Whole frames of an unacknowledged torn group stay
visible when nothing after them refuses (strict owner 1/300 S1 trials, others
65-72/300): not acknowledged loss, but v1 cannot tell a partial group.

## Other evidence

- Flip sweep over every header and CRC bit and 3 payload bytes of 28 frames
  (versions, deletes, reopen, a 6-frame group): 5460 flips, all refused, 0 loss.
- Checkpoint conflicts refuse by kind and salvage rebuilds; partial group, ENOSPC
  and EIO rollback; a reader racing a writer: 81 opens, 0 refused, 0 lost (`-race` too).
- Fuzz 30 s each, no failures (34577 and 28532 execs). `compat.sh`: 2337f48 and
  2ac9cab both ways, 24/24 checks, clean journals reopen with the same SHA-256.
- `go test -short .` passes (989 s); ten older tests now expect refusal, then salvage.

## Gates and status

- G-COMPAT pass. G-FAULT pass for the journal and checkpoint seams. G-MODEL:
  S1-S8, S3x run on the engine; S6 residual open (needs markers and seal, M-C2).
- G-FUZZ open: bounded local runs only; no CI in the repo; adding it needs the owner's go-ahead.
- M-A0 PARTIAL: journal seams, fuzz, compat and repro done. Not started: seams
  at other fsync sites, other decoders' fuzz targets, corpus manifest,
  benchmark runner, ListObjectsV2 regression, docs-drift sweep.
- M-A1 PARTIAL: S4 loss 0%, refusals name offset, frame and cause, old journals
  open unchanged, flip sweep 0 loss, no clean journal refused. Open: S6 residual.

Reproduce: `REPRODUCE.md`. Decisions: `../ADR_DELTA.md`. Ledger: `../LEDGER.md`.
