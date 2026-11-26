# FB-J2 write and recovery contract

The starting engine is cd9c0d5. The frozen main reference is 2ac9caba.
FB-03 is present. No FB-04 unified mutation queue or production v2
journal was present at preflight.

## Current publication

1. Ingest produces verified immutable components. Loose writes, segment
   publication and pack sealing use their own durability barriers.
   The manifest can be published concurrently with component sync.
   The journal may reference it only after both publications succeed.
2. PUT, CopyObject, version restore and native import reach
   `commitObjectRootChecked`. The ordered root queue resolves conditions
   and history against an overlay for earlier requests in the same
   batch. `rootCommitMu` excludes competing namespace writers. The
   queue releases `Store.mu` before calling `Journal.appendFrames`.
3. The journal serializes v1 frames with consecutive sequences, writes
   them using WriteAt, and syncs the journal. Lazy watermark updates
   then write a sidecar slot. Sync mode also syncs that file before
   completion. Only then does the queue install all entries and their
   history under `Store.mu` and resolve individual requests.
4. Bucket create/delete, single DELETE, history pruning and multipart
   create, part, abort and complete call `appendFrame` through separate
   mutation paths. Several hold `Store.mu` across the journal call.
   Complete publishes a manifest before recording the completed object
   and consuming the upload. DeleteObjects currently calls the single
   delete in request order and preserves per-key errors. FB-04 must
   retain these results when it shares a flush.
5. Checkpoint and namespace generation publication bind to a verified
   byte offset, final sequence and journal prefix. A file describing
   newer history than the journal may serve as evidence of damage.
   Snapshots publish their own durable descriptor and reference exact
   manifests. Shared readers open read-only, can race a live append,
   and expose any explicitly allowed degraded prefix. GET and HEAD
   use the published namespace and do not scan the log per request.

## States and retries

Prepared data and journal bytes may exist without a visible namespace
mutation. The writer has a durable group only after its file sync
succeeds. The namespace linearizes at apply under its publication lock.
The response follows apply. A complete group can survive without a
response and valid unacknowledged groups can replay. A failed or
ambiguous durability operation poisons the writer; the next open
classifies the bytes it finds rather than continuing at a guessed cursor.
Conditional and idempotent operations must reevaluate against replayed
state. The replay unit must include every member of a committed group or
none, including overwritten values, deletions and multipart visibility.

## Failure regimes

- Normal process and host failure assume successful sync preserves
  earlier durable bytes. Before the sync, page and sector persistence
  may arrive in any order. A complete, verified next group can replay.
  A verified footer must bind its exact transaction bytes and parent.
- Later media damage can remove acknowledged bytes. A valid older
  acknowledgment slot is only a lower bound when its companion is
  invalid. Retiring an uncertain suffix then can discard acknowledged
  history. The v1 guard refuses automatic tail discard in this state.
  A coherent clean-boundary rollback of the log and all available
  witness state still leaves no evidence of the missing transactions.
- One same-file flush can cover both a body/footer and a witness, but
  neither write is ordered durably before the successful sync. A
  coherent witness ahead of the body may refer to a never-acknowledged
  transaction or a damaged acknowledged one. Refusal protects both
  interpretations at a cost to host-crash availability.
- A checkpoint or independently durable witness can strengthen the
  guarantee only as far as its verified coverage. Physical power-loss
  and independent-media qualification remain separate.

## Benchmark separation

FB-01R's measured engine baseline is 1,976 / 5,565 / 13,234 PUT/s in
off, 2,026 / 5,961 / 15,273 in lazy, and 1,451 / 4,161 / 11,700 in
sync at 1 / 8 / 32 writers. These are historical local results. Prototype
comparisons will hold the filesystem, record sizes, durability edge
and replay oracle constant. Engine admission, buffer construction,
append, sync, read contention, and cold replay require separate timing.
