# FB-04 · Shared namespace mutation order

**In progress.** The object-root queue batches PUT and DELETE. A
DeleteObjects request submits all eligible keys together while retaining
the request's validation, response order, quiet handling, and per-key
errors. Duplicate and missing keys do not add journal frames.

The group prepares object and history overlays before the append.
Readers can see the prior coherent view during the sync. Queue waiters
only receive a result after the leader has applied or failed the group.
If the flush fails, results derived from the provisional overlay do
not report success. A panic before successful apply poisons the journal
and fails waiters before releasing them.

The focused real signed request test checked 1,000 existing keys with
one sync in both v1 and J2. An exact foreground read completed while
the sync was held, and every deletion survived reopen. The grouped
ENOSPC test preserved the old roots and exact log.

Remaining scope covers the bucket, multipart, and prune paths in the
same queue, bounded performance work, and the cross-operation
linearizability and crash gates.
