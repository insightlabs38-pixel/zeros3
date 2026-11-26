# FB-04 · Shared namespace mutation order

**COMPLETE for M-B1.** The object-root queue batches PUT and DELETE. A
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

Bucket creation/deletion, upload creation/part/abort/completion,
and history-prune frames share the queue in order. They use separate
batch boundaries because their provisional state differs from the
object-root overlay. Multipart ingest and checksum work stays outside
the queue. All journal writes in these mutation paths release the
namespace lock while the commit lock excludes writers. Focused probes
checked each path with both formats, including exact multipart bytes.

Three interleaved overlay trials of the signed 1,000-key delete
measured 228.9 ms before and 10.3 ms after, excluding fixture setup.
The queue passed an independent interval-order model for concurrent
PUT, conditional create, DELETE and read at 32 histories per format.
The real process-kill rerun checked 26 SIGKILLs and 2,550
acknowledgements across both formats with zero losses and no refusals.
The interval-order model and multipart tests also passed under race.

Ten scheduled requests
deleted 100 keys each, with one steady GET client and one HEAD client.
Three paired trials gave median GET p99 12.85 → 7.90 ms and HEAD p99
11.52 → 7.18 ms. Delete p50 was 37.79 → 9.20 ms. One pair had worse
foreground tails, so the numbers describe this schedule and machine.
The staged-sync probe independently established that the namespace
lock is available during the covered durability operations.

See [reproduction](REPRODUCE.md) and [raw results](results/).
The existing J2 byte and member bounds still apply to oversized
combined groups. Real host power loss and broader deployment
qualification remain open. FB-05 can use the same commit order.
