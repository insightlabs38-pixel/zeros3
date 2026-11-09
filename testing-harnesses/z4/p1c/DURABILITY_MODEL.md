# Z4-P1C durability model: small-object segments

Written before the segment publication path was implemented; the implementation and the crash-state tests are checked against it.
Names are those of `zeros3.go` (section "4d. Small-object segments"). Background: [`../p1b/DURABILITY_MODEL.md`](../p1b/DURABILITY_MODEL.md).

## 1. Objects and their states

| object | what it is | durable when |
|---|---|---|
| segment `segments/<seq>.seg` | append-only file: pack-v1 header + pack-v1 records (raw codec) | its directory entry is fsynced (create) and every record the journal depends on is covered by a successful `fsync` of the file |
| record | `sha256 | logical | stored | codec | rsv | payload` (chunk **or** small manifest) | a successful segment `fsync` that started after the record was written |
| locator entry (`packState`) | in-memory (digest -> segment, offset, lengths) | not durable; rebuilt at open |
| journal frame | existing `visibility.log` frame naming `manifest_uuid` + `manifest_sha256` | successful journal `fsync` = **commit point** |

Visibility levels of a record: **appended** (in the page cache, in no index) < **durable** (segment fsync returned, then and only then inserted into the locator, so `casStat`/`casRead`/`readManifest` can see it) < **referenced** (a journal frame naming it is fsynced) < **acknowledged** (HTTP 2xx, after the apply).

## 2. Ordering of one small PUT (new default for known sizes < 256 KiB, and every small manifest)

| # | step | barrier |
|---|---|---|
| 1 | chunk bytes are produced and hashed by the request goroutine (CDC, SHA-256); copies are held in the request (bounded: object < 256 KiB) | none |
| 2 | the request enqueues `{chunk records not already durable, manifest record}` on the segment queue | none |
| 3 | a leader (any waiting request; no timer, no helper goroutine) takes the queue (<= 256 requests, <= 8 MiB), drops digests that are already durable or repeated in the batch, serialises **all records of the batch into one buffer** | none |
| 4 | `WriteAt(buf, segment.size)`; a short write is an error | none |
| 5 | `fsync(segment)` | **data barrier** |
| 6 | the records are inserted into the locator (one new immutable level), `segment.size` advances, the running body hash advances | records now durable and visible |
| 7 | every request of the batch is released; each submits its journal frame to the existing group-committed journal queue | |
| 8 | journal write + `fsync` | **commit point** |
| 9 | namespace apply, acknowledgement | |

The journal frame is therefore never written before the barrier of step 5 for every record it references (the request that wrote the frame waited for step 7). A frame can only reference a manifest record that was durable when the frame was written.

Segment creation (first use, rollover): `open(O_CREATE|O_EXCL)`, write the 16-byte header, `fsync(file)`, `fsync(segments/)` (and `fsync(store root)` when `segments/` was just created) **before** the segment becomes the active one, i.e. before any request can depend on it. `FORMAT.json` is raised to level 11 (`ensureStoreFormat`, itself write-tmp/fsync/rename/fsync-dir) **before the first segment file is created**.

Sealing (rollover or maintenance), only for segments with at least one durable record: append `index || footer` at the durable size, `fsync(file)`, `rename(segments/<seq>.seg, packs/<body_sha256>.pack)`, `fsync(packs/)`, `fsync(segments/)`, then swap the locator's pack descriptor. The locator keeps serving the same (offset, length) pairs because the file is not rewritten.

## 3. Commit point and failure semantics

* **Commit point = journal fsync**, unchanged. Everything before it is unreachable garbage after a crash.
* A record that is durable but unreferenced (crash between 5 and 8, failed or cancelled request, failed journal write) stays in the segment as dead data. It is never rolled back in place; `repack`/`gc` reclaim it when its sealed pack is partly/fully dead. This is what lets a successful request share a segment with a failed one: nothing about the failed request is undone, and nothing the successful request depends on can be removed.
* **Duplicate digests**: a digest already durable is skipped (`casStat` before enqueue and again by the leader); the same digest in two requests of one batch is written once; the same digest in two consecutive batches cannot happen because the leader of batch *n+1* starts only after batch *n* finished (durable or failed) and re-checks the locator. A digest racing with a loose publisher is harmless (two copies).
* **Write error / short write / `ENOSPC`** (step 4): the batch fails (all members get the error), the leader truncates the segment back to the last durable size (best effort), and **retires** the segment: it is never appended to again, a fresh segment is created for the next batch. Records of earlier successful batches stay served from it.
* **`fsync` error** (step 5): same. Linux may drop dirty pages after a failed `fsync` and report success on a retry, so the file is *never* fsynced again to claim durability of the failed batch; its tail is truncated and the segment retired. Earlier acknowledged records were covered by earlier successful fsyncs. All members of the batch fail; none reaches the journal.
* **Uncertain fsync outcome** (process dies during step 5): indistinguishable from a crash between 4 and 5; the records are either present (valid prefix) or cut at a torn tail; no frame was written, so nothing references them.
* **Leader panic or crash hook**: the leader's deferred handler fails every member of its batch, retires the segment and passes leadership on; the queue and the segment map contain only in-flight work.
* **Cancelled request**: a request that is still queued is removed (its records are not written); a request already inside a batch cannot be cancelled and its records become dead data when it does not commit.
* **Journal failure** (step 8): existing behaviour: the journal is poisoned, the whole journal batch fails, none of it becomes visible; the records stay as dead data.
* **Conditional PUT / same key races**: unchanged (decided at the journal commit point, in queue order).
* **Rollover spanning requests**: a batch is written entirely to one segment; rollover happens after a batch (soft target 32 MiB, hard cap 2x, beyond which batches fail instead of growing the segment).
* **Journal frame survives, segment data does not** (media loss, not a crash effect, because the barrier of step 5 precedes the frame): detected as missing chunk/manifest by `verify`; `repair` can fetch the chunks from a peer; an inline manifest has no repair path (same as a lost manifest file today).

## 4. Recovery (at `OpenStore`, before serving) and the segment state machine

States: **created** (header written; may be a 0..15-byte or header-only file) -> **active** (appended to; persisted as the file) -> **flushing** (a batch is between 4 and 6; reconstructible: it is the file tail) -> **durable** (prefix covered by fsync) -> **sealing** (index/footer appended, maybe renamed) -> **sealed** (an ordinary pack in `packs/`) -> **retired** (sealed or fully dead) -> **reclaimable** (all records dead; `gc` removes the pack). Persisted: the file bytes and names only. Everything else is reconstructed.

For each `segments/*.seg` in sequence order:
1. A file shorter than 16 bytes or with a wrong header is a never-used segment (creation was interrupted before its first acknowledged use): removed.
2. If the file is a complete pack-v1 (valid footer, index and body hash): the crash happened between the seal fsync and the rename. Finish the seal (`rename` to `packs/<id>.pack`; if that pack already exists the segment file is removed).
3. Otherwise scan records from offset 16 with **bounded, overflow-checked arithmetic**: a record is valid iff its 44-byte header fits, `codec == 0`, reserved bytes are 0, `logical == stored <= 256 KiB`, the payload fits in the file, and `SHA-256(payload) == digest`. Scanning stops at the first invalid record or at EOF; the work is bounded by the file size (`<= 2 x` target). **CRC32C is not used**: content addressing already gives a stronger per-record check and the same torn-tail detection.
4. The invalid tail (if any) is copied to `tmp/seg-torn-<seq>-<offset>` (collectable by `gc`) and the segment is truncated to the valid prefix, then fsynced. Power-loss torn tails always lie after the last fsynced byte, so no acknowledged record is cut; mid-file media corruption is also truncated at the corrupt record but its remainder is preserved in the quarantine copy and `verify` reports the lost chunks as missing.
5. An empty valid prefix removes the file. The valid segment with the highest sequence and size < target is re-adopted as the **active** segment (no tiny pack per restart); every other recovered segment is sealed.
6. Records enter the locator after the scan; sealing and re-adoption use the scanned entries and running body hash, never the in-memory state of the previous process.

The reopen is idempotent: a crash at any point of recovery (after truncate, during seal, between rename and directory fsync) leaves a state that is again one of the cases above. `packs/<id>.pack` and `segments/<seq>.seg` holding the same bytes at once is resolved by removing the segment file.

## 5. What reads and maintenance may assume

* `casStat`/`casRead`/`readVerifiedManifest` see a segment record exactly like a packed record (same locator, same header re-check and SHA-256 re-hash). A read racing the seal rename retries once on a fresh locator snapshot.
* `gc`, `repack`, `compact`, `prune`, `doctor`, `verify -deep`, `bundle` and `tier` operations first **quiesce and seal** all active segments (`sealSegments`), so the pack machinery only ever sees ordinary sealed packs; an active segment is therefore never collected, never unbounded (soft cap 32 MiB, hard cap 64 MiB) and never "live forever": every maintenance operation or clean restart above the minimum size turns it into a pack that is subject to normal liveness rules.
* The liveness set that pack maintenance uses includes the SHA-256 of every live root's manifest, so inline manifest records are kept while their root is reachable and reclaimed with their pack otherwise.
* No other code opens `segments/`.

## 6. Power-loss reasoning (what the tests model)

The crash-state model (`TestP1C_CrashState_*`) represents a file as `(durable prefix, volatile suffix)` plus a set of durable/volatile directory entries, and for each barrier enumerates the states a power cut may leave: volatile suffix kept entirely, dropped entirely, cut at an arbitrary byte (torn record), or kept with a zero-filled hole in front of later bytes. It then runs recovery and checks the invariants of section 7. Real `kill -9` tests cover process death only. **No block-device fault harness exists in this environment: no claim of tested power loss on real hardware is made.**

## 7. Invariants checked

I1 no acknowledged object loses bytes; I2 no acknowledged object references missing data; I3 no silent hash mismatch; I4 no uncommitted namespace update visible; I5 recovery terminates in time bounded by the segment size; I6 no invalid offset/length is trusted (every recovered record is re-hashed, every read re-checks the header); I7 no GC of live data; I8 the only copy of a record is never removed before its replacement is durable (seal renames, never copies); I9 no irrecoverable format transition (the floor is raised before the first segment exists, never lowered); I10 no unbounded queue/tail/goroutine (batch caps, hard segment cap, no helper goroutines).
